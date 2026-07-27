//go:build windows

package player

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/saltosystems/winrt-go"
	"github.com/saltosystems/winrt-go/windows/foundation"
	"github.com/saltosystems/winrt-go/windows/media/playback"
)

// This backend is adapted from go-musicfox's WinRT MediaPlayer backend,
// distributed under the MIT License.

var errNativeClosed = errors.New("native player is closed")
var roUninitialize = syscall.NewLazyDLL("combase.dll").NewProc("RoUninitialize")
var playerEventGUID = winrt.ParameterizedInstanceGUID(
	foundation.GUIDTypedEventHandler,
	playback.SignatureMediaPlayer,
	"cinterface(IInspectable)",
)
var playerFailedEventGUID = winrt.ParameterizedInstanceGUID(
	foundation.GUIDTypedEventHandler,
	playback.SignatureMediaPlayer,
	playback.SignatureMediaPlayerFailedEventArgs,
)

type nativeCall struct {
	fn   func(*nativeState) error
	done chan error
}

type nativeState struct {
	player  *playback.MediaPlayer
	ended   *foundation.TypedEventHandler
	failed  *foundation.TypedEventHandler
	opened  *foundation.TypedEventHandler
	endTok  foundation.EventRegistrationToken
	failTok foundation.EventRegistrationToken
	openTok foundation.EventRegistrationToken
	gen     uint64
	closed  bool
}

// Native is a concurrent-safe Windows Runtime audio player. All WinRT calls
// are serialized onto the single OS thread which owns the COM apartment.
type Native struct {
	mu    sync.Mutex
	calls chan nativeCall
	dead  bool
}

// NewNative creates one WinRT MediaPlayer and its owning COM apartment.
func NewNative() (*Native, error) {
	n := &Native{calls: make(chan nativeCall)}
	ready := make(chan error, 1)
	go n.run(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return n, nil
}

func (n *Native) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.RoInitialize(1); err != nil { // RO_INIT_MULTITHREADED
		ready <- fmt.Errorf("initialize Windows Runtime: %w", err)
		return
	}
	defer roUninitialize.Call()
	p, err := playback.NewMediaPlayer()
	if err != nil {
		ready <- fmt.Errorf("create MediaPlayer: %w", err)
		return
	}
	s := &nativeState{player: p}
	ready <- nil
	for call := range n.calls {
		call.done <- call.fn(s)
	}
}

func (n *Native) call(fn func(*nativeState) error) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.dead {
		return errNativeClosed
	}
	done := make(chan error, 1)
	n.calls <- nativeCall{fn: fn, done: done}
	return <-done
}

func (s *nativeState) removeEvents() {
	if s.ended != nil {
		_ = s.player.RemoveMediaEnded(s.endTok)
		s.ended.Release()
		s.ended = nil
	}
	if s.failed != nil {
		_ = s.player.RemoveMediaFailed(s.failTok)
		s.failed.Release()
		s.failed = nil
	}
	if s.opened != nil {
		_ = s.player.RemoveMediaOpened(s.openTok)
		s.opened.Release()
		s.opened = nil
	}
}

func (s *nativeState) cancel() error {
	s.gen++
	s.removeEvents()
	if err := s.player.Pause(); err != nil {
		return err
	}
	return s.player.SetUriSource(nil)
}

// Play replaces any current item. format is reserved for future stream-source
// support; MediaPlayer determines the format from the URI/response metadata.
func (n *Native) Play(url string, format string, offset time.Duration, volume int, onEnd func(error)) error {
	_ = format
	return n.call(func(s *nativeState) error {
		if s.closed {
			return errNativeClosed
		}
		_ = s.cancel()
		s.gen++
		gen := s.gen
		complete := func(err error) {
			// Re-enter the owner thread to atomically claim this generation.
			go func() {
				var cb func(error)
				_ = n.call(func(s *nativeState) error {
					if !s.closed && s.gen == gen {
						s.gen++
						s.removeEvents()
						cb = onEnd
					}
					return nil
				})
				if cb != nil {
					cb(err)
				}
			}()
		}
		s.ended = foundation.NewTypedEventHandler(ole.NewGUID(playerEventGUID), func(*foundation.TypedEventHandler, unsafe.Pointer, unsafe.Pointer) {
			complete(nil)
		})
		var err error
		s.endTok, err = s.player.AddMediaEnded(s.ended)
		if err != nil {
			s.removeEvents()
			return fmt.Errorf("register MediaEnded: %w", err)
		}
		s.failed = foundation.NewTypedEventHandler(ole.NewGUID(playerFailedEventGUID), func(_ *foundation.TypedEventHandler, _ unsafe.Pointer, args unsafe.Pointer) {
			message := "WinRT media playback failed"
			if args != nil {
				if text, e := (*playback.MediaPlayerFailedEventArgs)(args).GetErrorMessage(); e == nil && text != "" {
					message = text
				}
			}
			complete(errors.New(message))
		})
		s.failTok, err = s.player.AddMediaFailed(s.failed)
		if err != nil {
			s.removeEvents()
			return fmt.Errorf("register MediaFailed: %w", err)
		}
		if offset > 0 {
			s.opened = foundation.NewTypedEventHandler(ole.NewGUID(playerEventGUID), func(*foundation.TypedEventHandler, unsafe.Pointer, unsafe.Pointer) {
				go func() {
					seekErr := n.call(func(s *nativeState) error {
						if s.closed || s.gen != gen {
							return nil
						}
						return s.player.SetPosition(foundation.TimeSpan{Duration: offset.Nanoseconds() / 100})
					})
					if seekErr != nil {
						complete(fmt.Errorf("set initial position: %w", seekErr))
					}
				}()
			})
			s.openTok, err = s.player.AddMediaOpened(s.opened)
			if err != nil {
				s.removeEvents()
				return fmt.Errorf("register MediaOpened: %w", err)
			}
		}
		uri, err := foundation.UriCreateUri(url)
		if err != nil {
			s.removeEvents()
			return fmt.Errorf("create media URI: %w", err)
		}
		defer uri.Release()
		if err = s.player.SetVolume(float64(clampVolume(volume)) / 100); err != nil {
			s.removeEvents()
			return fmt.Errorf("set volume: %w", err)
		}
		if err = s.player.SetUriSource(uri); err != nil {
			s.removeEvents()
			return fmt.Errorf("set media source: %w", err)
		}
		if err = s.player.Play(); err != nil {
			_ = s.cancel()
			return fmt.Errorf("start playback: %w", err)
		}
		return nil
	})
}

func clampVolume(volume int) int {
	if volume < 0 {
		return 0
	}
	if volume > 100 {
		return 100
	}
	return volume
}

func (n *Native) Pause() error {
	return n.call(func(s *nativeState) error {
		if s.closed {
			return errNativeClosed
		}
		return s.player.Pause()
	})
}
func (n *Native) Resume() error {
	return n.call(func(s *nativeState) error {
		if s.closed {
			return errNativeClosed
		}
		return s.player.Play()
	})
}
func (n *Native) Seek(at time.Duration) error {
	if at < 0 {
		at = 0
	}
	return n.call(func(s *nativeState) error {
		if s.closed {
			return errNativeClosed
		}
		return s.player.SetPosition(foundation.TimeSpan{Duration: at.Nanoseconds() / 100})
	})
}
func (n *Native) SetVolume(volume int) error {
	return n.call(func(s *nativeState) error {
		if s.closed {
			return errNativeClosed
		}
		return s.player.SetVolume(float64(clampVolume(volume)) / 100)
	})
}
func (n *Native) Position() time.Duration {
	var position time.Duration
	_ = n.call(func(s *nativeState) error {
		if s.closed {
			return errNativeClosed
		}
		span, err := s.player.GetPosition()
		if err == nil {
			position = time.Duration(span.Duration) * 100
		}
		return err
	})
	return position
}
func (n *Native) Stop() error {
	return n.call(func(s *nativeState) error {
		if s.closed {
			return errNativeClosed
		}
		return s.cancel()
	})
}
func (n *Native) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.dead {
		return nil
	}
	done := make(chan error, 1)
	n.calls <- nativeCall{done: done, fn: func(s *nativeState) error {
		if s.closed {
			return nil
		}
		s.closed = true
		s.gen++
		s.removeEvents()
		_ = s.player.Pause()
		err := s.player.Close()
		s.player.Release()
		return err
	}}
	err := <-done
	n.dead = true
	close(n.calls)
	return err
}
