//go:build darwin

// Adapted from go-musicfox's Darwin AVPlayer backend (MIT licensed).
package player

import (
	"fmt"
	"sync"
	"time"

	"github.com/4fuu/net-mpd/internal/macdriver"
)

type Native struct {
	mu         sync.Mutex
	av         *macdriver.Player
	generation uint64
	onEnd      func(error)
	closed     bool
}

func NewNative() (*Native, error) {
	p := &Native{}
	av, err := macdriver.NewPlayer(p.notification)
	if err != nil {
		return nil, err
	}
	p.av = av
	return p, nil
}

func (p *Native) Play(url string, format string, offset time.Duration, volume int, onEnd func(error)) error {
	_ = format // AVFoundation performs media format detection.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("native player is closed")
	}
	p.generation++
	p.onEnd = nil // replacement deliberately suppresses the previous callback.
	p.av.Pause()
	p.av.Clear()
	if err := p.av.SetItem(url); err != nil {
		return err
	}
	p.onEnd = onEnd
	p.av.SetVolume(float32(clampVolume(volume)) / 100)
	if offset < 0 {
		offset = 0
	}
	if offset > 0 {
		p.seekLocked(offset)
	}
	p.av.Play()
	return nil
}

func (p *Native) notification(failed bool, item uintptr) {
	p.mu.Lock()
	if p.closed || p.onEnd == nil || !p.av.IsCurrent(item) {
		p.mu.Unlock()
		return
	}
	cb := p.onEnd
	p.onEnd = nil
	var err error
	if failed {
		err = p.av.ErrorForItem(item)
	}
	p.mu.Unlock()
	cb(err)
}
func (p *Native) ready() error {
	if p.closed {
		return fmt.Errorf("native player is closed")
	}
	return nil
}
func (p *Native) Pause() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ready(); err != nil {
		return err
	}
	p.av.Pause()
	return nil
}
func (p *Native) Resume() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ready(); err != nil {
		return err
	}
	p.av.Play()
	return nil
}
func (p *Native) seekLocked(d time.Duration) {
	p.av.Seek(macdriver.CMTime{Value: d.Nanoseconds(), Timescale: 1_000_000_000, Flags: 1})
}
func (p *Native) Seek(d time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ready(); err != nil {
		return err
	}
	if d < 0 {
		d = 0
	}
	p.seekLocked(d)
	return nil
}
func clampVolume(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
func (p *Native) SetVolume(v int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ready(); err != nil {
		return err
	}
	p.av.SetVolume(float32(clampVolume(v)) / 100)
	return nil
}
func (p *Native) Position() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0
	}
	t := p.av.Position()
	if t.Timescale <= 0 {
		return 0
	}
	return time.Duration(float64(t.Value) / float64(t.Timescale) * float64(time.Second))
}
func (p *Native) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ready(); err != nil {
		return err
	}
	p.generation++
	p.onEnd = nil
	p.av.Pause()
	p.av.Clear()
	return nil
}
func (p *Native) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.generation++
	p.onEnd = nil
	p.av.Pause()
	p.av.Close()
	return nil
}
