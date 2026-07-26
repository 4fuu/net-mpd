package player

import (
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// Player is the minimal playback backend used by the MPD state machine.
type Player interface {
	Play(url string, offset time.Duration, volume int, onEnd func()) error
	Stop() error
	Close() error
}

// FFPlay runs one ffplay child process at a time.
type FFPlay struct {
	mu         sync.Mutex
	command    string
	cmd        *exec.Cmd
	generation uint64
	closed     bool
}

func NewFFPlay(command string) *FFPlay {
	if command == "" {
		command = "ffplay"
	}
	return &FFPlay{command: command}
}

func (p *FFPlay) Play(url string, offset time.Duration, volume int, onEnd func()) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("player is closed")
	}
	p.stopLocked()
	p.generation++
	gen := p.generation
	args := []string{"-nodisp", "-autoexit", "-loglevel", "quiet", "-volume", strconv.Itoa(volume)}
	if offset > 0 {
		args = append(args, "-ss", strconv.FormatFloat(offset.Seconds(), 'f', 3, 64))
	}
	args = append(args, url)
	cmd := exec.Command(p.command, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffplay: %w", err)
	}
	p.cmd = cmd
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		valid := !p.closed && p.generation == gen && p.cmd == cmd
		if valid {
			p.cmd = nil
		}
		p.mu.Unlock()
		if valid && err == nil && onEnd != nil {
			onEnd()
		}
	}()
	return nil
}

func (p *FFPlay) stopLocked() {
	p.generation++
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	p.cmd = nil
}

func (p *FFPlay) Stop() error { p.mu.Lock(); defer p.mu.Unlock(); p.stopLocked(); return nil }
func (p *FFPlay) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.stopLocked()
		p.closed = true
	}
	return nil
}
