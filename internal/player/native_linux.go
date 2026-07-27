//go:build linux

package player

// This backend is substantially adapted from go-musicfox's Beep player.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gopxl/beep"
	"github.com/gopxl/beep/effects"
	"github.com/gopxl/beep/flac"
	"github.com/gopxl/beep/mp3"
	"github.com/gopxl/beep/speaker"
	"github.com/gopxl/beep/vorbis"
	"github.com/gopxl/beep/wav"
)

const (
	nativeRate       = beep.SampleRate(44100)
	maxResponseBytes = int64(1 << 30)
	initialBytes     = int64(256 << 10)
)

var speakerOnce struct {
	sync.Mutex
	initialized bool
	err         error
}

// Native is the Linux in-process Beep/Oto playback backend.
type Native struct {
	opMu sync.Mutex
	mu   sync.Mutex

	closed bool
	gen    uint64
	track  *nativeTrack
	ctrl   *beep.Ctrl
	volume *effects.Volume
	pos    *positionStreamer
}

type nativeTrack struct {
	path     string
	file     *os.File
	stream   beep.StreamSeekCloser
	rate     beep.SampleRate
	format   string
	cancel   context.CancelFunc
	done     chan struct{}
	download error
	mu       sync.Mutex
}

type positionStreamer struct {
	stream beep.Streamer
	rate   beep.SampleRate
	mu     sync.Mutex
	pos    int
}

func (p *positionStreamer) Stream(samples [][2]float64) (int, bool) {
	n, ok := p.stream.Stream(samples)
	p.mu.Lock()
	p.pos += n
	p.mu.Unlock()
	return n, ok
}
func (p *positionStreamer) Err() error { return p.stream.Err() }

func NewNative() (*Native, error) {
	speakerOnce.Lock()
	defer speakerOnce.Unlock()
	if !speakerOnce.initialized {
		speakerOnce.err = speaker.Init(nativeRate, nativeRate.N(100*time.Millisecond))
		speakerOnce.initialized = speakerOnce.err == nil
	}
	if speakerOnce.err != nil {
		return nil, fmt.Errorf("initialize Oto speaker: %w", speakerOnce.err)
	}
	return &Native{}, nil
}

func (p *Native) Play(rawURL string, format string, offset time.Duration, volume int, onEnd func(error)) error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.stopLocked()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("native player is closed")
	}
	p.gen++
	gen := p.gen
	p.mu.Unlock()

	t, bf, err := prepareNativeTrack(rawURL, format)
	if err != nil {
		return err
	}
	if offset < 0 {
		offset = 0
	}
	if offset > 0 {
		if !trackDownloaded(t) {
			closeNativeTrack(t)
			return errors.New("cannot seek before the response is fully cached")
		}
		if err := t.stream.Seek(bf.SampleRate.N(offset)); err != nil {
			closeNativeTrack(t)
			return fmt.Errorf("initial seek: %w", err)
		}
	}
	resampled := beep.Resample(4, bf.SampleRate, nativeRate, &growingStreamer{track: t})
	pos := &positionStreamer{stream: resampled, rate: nativeRate, pos: nativeRate.N(offset)}
	vol := &effects.Volume{Streamer: pos, Base: 2}
	setEffectVolume(vol, volume)
	ctrl := &beep.Ctrl{Streamer: vol}
	p.mu.Lock()
	p.track, p.ctrl, p.volume, p.pos = t, ctrl, vol, pos
	p.mu.Unlock()
	speaker.Play(beep.Seq(ctrl, beep.Callback(func() { go p.finished(gen, onEnd) })))
	return nil
}

func (p *Native) finished(gen uint64, cb func(error)) {
	p.mu.Lock()
	if p.closed || p.gen != gen || p.track == nil {
		p.mu.Unlock()
		return
	}
	t := p.track
	p.track, p.ctrl, p.volume, p.pos = nil, nil, nil, nil
	p.mu.Unlock()
	err := t.stream.Err()
	t.mu.Lock()
	if err == nil {
		err = t.download
	}
	t.mu.Unlock()
	closeNativeTrack(t)
	if cb != nil {
		cb(err)
	}
}

func (p *Native) Pause() error  { return p.setPaused(true) }
func (p *Native) Resume() error { return p.setPaused(false) }
func (p *Native) setPaused(paused bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("native player is closed")
	}
	if p.ctrl == nil {
		return errors.New("nothing is playing")
	}
	speaker.Lock()
	p.ctrl.Paused = paused
	speaker.Unlock()
	return nil
}

func (p *Native) Seek(d time.Duration) error {
	if d < 0 {
		d = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("native player is closed")
	}
	if p.track == nil || p.pos == nil {
		return errors.New("nothing is playing")
	}
	if !trackDownloaded(p.track) {
		return errors.New("cannot seek until the response is fully cached")
	}
	p.track.mu.Lock()
	downloadErr := p.track.download
	p.track.mu.Unlock()
	if downloadErr != nil {
		return fmt.Errorf("cannot seek after download failure: %w", downloadErr)
	}
	reader, err := os.Open(p.track.path)
	if err != nil {
		return fmt.Errorf("open completed audio cache: %w", err)
	}
	stream, format, err := decodeNative(p.track.format, reader)
	if err != nil {
		reader.Close()
		return fmt.Errorf("decode completed audio cache: %w", err)
	}
	if err = stream.Seek(format.SampleRate.N(d)); err != nil {
		stream.Close()
		return fmt.Errorf("seek: %w", err)
	}
	resampled := beep.Resample(4, format.SampleRate, nativeRate, stream)
	position := &positionStreamer{stream: resampled, rate: nativeRate, pos: nativeRate.N(d)}
	speaker.Lock()
	old := p.track.stream
	p.track.stream = stream
	p.track.rate = format.SampleRate
	p.pos = position
	p.volume.Streamer = position
	speaker.Unlock()
	_ = old.Close()
	return nil
}

func (p *Native) SetVolume(v int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("native player is closed")
	}
	if p.volume == nil {
		return errors.New("nothing is playing")
	}
	speaker.Lock()
	setEffectVolume(p.volume, v)
	speaker.Unlock()
	return nil
}

func setEffectVolume(v *effects.Volume, n int) {
	if n < 0 {
		n = 0
	}
	if n > 100 {
		n = 100
	}
	v.Silent = n == 0
	if n > 0 {
		v.Volume = math.Log2(float64(n) / 100)
	}
}

func (p *Native) Position() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pos == nil {
		return 0
	}
	p.pos.mu.Lock()
	defer p.pos.mu.Unlock()
	return nativeRate.D(p.pos.pos)
}

func (p *Native) Stop() error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.stopLocked()
	return nil
}

func (p *Native) stopLocked() {
	p.mu.Lock()
	p.gen++
	t, ctrl := p.track, p.ctrl
	p.track, p.ctrl, p.volume, p.pos = nil, nil, nil, nil
	p.mu.Unlock()
	if t != nil {
		t.cancel()
	}
	if ctrl != nil {
		speaker.Lock()
		ctrl.Streamer = beep.Silence(0)
		speaker.Unlock()
	}
	if t != nil {
		closeNativeTrack(t)
	}
}

func (p *Native) Close() error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.stopLocked()
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	// Do not close speaker: Oto's context is intentionally retained process-wide.
	return nil
}

func prepareNativeTrack(rawURL, hint string) (*nativeTrack, beep.Format, error) {
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		cancel()
		return nil, beep.Format{}, fmt.Errorf("create audio request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		return nil, beep.Format{}, fmt.Errorf("fetch audio: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		cancel()
		return nil, beep.Format{}, fmt.Errorf("fetch audio: HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxResponseBytes {
		resp.Body.Close()
		cancel()
		return nil, beep.Format{}, errors.New("audio response exceeds 1 GiB limit")
	}
	f, err := os.CreateTemp("", "net-mpd-audio-*")
	if err != nil {
		resp.Body.Close()
		cancel()
		return nil, beep.Format{}, fmt.Errorf("create private audio cache: %w", err)
	}
	_ = f.Chmod(0600)
	t := &nativeTrack{path: f.Name(), file: f, cancel: cancel, done: make(chan struct{})}
	discard := func() {
		cancel()
		_ = resp.Body.Close()
		_ = f.Close()
		_ = os.Remove(f.Name())
	}
	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	buf := make([]byte, initialBytes)
	n, readErr := io.ReadFull(limited, buf)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		discard()
		return nil, beep.Format{}, fmt.Errorf("read audio: %w", readErr)
	}
	if _, err = f.Write(buf[:n]); err != nil {
		discard()
		return nil, beep.Format{}, fmt.Errorf("cache audio: %w", err)
	}
	kind := detectFormat(hint, resp.Header.Get("Content-Type"), buf[:n])
	if kind == "" {
		discard()
		return nil, beep.Format{}, errors.New("unsupported or unrecognized audio format")
	}
	if readErr == nil {
		go downloadRemainder(t, resp.Body, limited, int64(n))
	} else {
		resp.Body.Close()
		close(t.done)
	}
	reader, err := os.Open(t.path)
	if err != nil {
		closeNativeTrack(t)
		return nil, beep.Format{}, err
	}
	t.format = kind
	stream, bf, err := decodeNative(kind, reader)
	if err != nil {
		closeNativeTrack(t)
		return nil, beep.Format{}, fmt.Errorf("decode %s audio: %w", kind, err)
	}
	t.stream = stream
	t.rate = bf.SampleRate
	return t, bf, nil
}

func downloadRemainder(t *nativeTrack, body io.ReadCloser, r io.Reader, already int64) {
	defer body.Close()
	defer close(t.done)
	defer t.file.Close()
	n, err := io.Copy(t.file, r)
	if err == nil && already+n > maxResponseBytes {
		err = errors.New("audio response exceeds 1 GiB limit")
	}
	t.mu.Lock()
	t.download = err
	t.mu.Unlock()
}

type growingStreamer struct {
	track     *nativeTrack
	finalized bool
}

func (g *growingStreamer) Stream(samples [][2]float64) (int, bool) {
	for {
		n, ok := g.track.stream.Stream(samples)
		if n != 0 {
			return n, ok
		}
		select {
		case <-g.track.done:
			if g.finalized {
				return 0, false
			}
			g.track.mu.Lock()
			downloadErr := g.track.download
			g.track.mu.Unlock()
			if downloadErr != nil {
				return 0, false
			}
			pos := g.track.stream.Position()
			_ = g.track.stream.Close()
			f, err := os.Open(g.track.path)
			if err != nil {
				g.track.setError(fmt.Errorf("open completed audio cache: %w", err))
				return 0, false
			}
			s, _, err := decodeNative(g.track.format, f)
			if err == nil {
				err = s.Seek(pos)
			}
			if err != nil {
				if s != nil {
					_ = s.Close()
				}
				g.track.setError(fmt.Errorf("resume completed audio cache: %w", err))
				return 0, false
			}
			g.track.stream = s
			g.finalized = true
			continue
		case <-time.After(50 * time.Millisecond):
			pos := g.track.stream.Position()
			_ = g.track.stream.Close()
			f, err := os.Open(g.track.path)
			if err != nil {
				return 0, false
			}
			s, _, err := decodeNative(g.track.format, f)
			if err != nil {
				f.Close()
				continue
			}
			if err = s.Seek(pos); err != nil {
				s.Close()
				continue
			}
			g.track.stream = s
		}
	}
}
func (g *growingStreamer) Err() error { return g.track.stream.Err() }

func (t *nativeTrack) setError(err error) {
	t.mu.Lock()
	if t.download == nil {
		t.download = err
	}
	t.mu.Unlock()
}

func decodeNative(kind string, r io.ReadSeekCloser) (beep.StreamSeekCloser, beep.Format, error) {
	switch kind {
	case "mp3":
		return mp3.Decode(r)
	case "flac":
		return flac.Decode(r)
	case "ogg":
		return vorbis.Decode(r)
	case "wav":
		return wav.Decode(r)
	default:
		return nil, beep.Format{}, errors.New("unsupported audio format")
	}
}

func detectFormat(hint, contentType string, b []byte) string {
	normalize := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(strings.Split(s, ";")[0]))
		switch s {
		case "mp3", "audio/mpeg", "audio/mp3":
			return "mp3"
		case "flac", "audio/flac", "audio/x-flac":
			return "flac"
		case "ogg", "vorbis", "audio/ogg", "application/ogg":
			return "ogg"
		case "wav", "wave", "audio/wav", "audio/x-wav", "audio/wave":
			return "wav"
		}
		return ""
	}
	if f := normalize(hint); f != "" {
		return f
	}
	if f := normalize(contentType); f != "" {
		return f
	}
	if len(b) >= 4 && string(b[:4]) == "fLaC" {
		return "flac"
	}
	if len(b) >= 4 && string(b[:4]) == "OggS" {
		return "ogg"
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE" {
		return "wav"
	}
	if len(b) >= 3 && string(b[:3]) == "ID3" {
		return "mp3"
	}
	if len(b) >= 2 && b[0] == 0xff && b[1]&0xe0 == 0xe0 {
		return "mp3"
	}
	return ""
}

func trackDownloaded(t *nativeTrack) bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}
func closeNativeTrack(t *nativeTrack) {
	if t == nil {
		return
	}
	t.cancel()
	<-t.done
	if t.stream != nil {
		_ = t.stream.Close()
	}
	if t.file != nil {
		_ = t.file.Close()
	}
	_ = os.Remove(t.path)
}
