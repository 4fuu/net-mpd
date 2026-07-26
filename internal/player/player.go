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
	opMu       sync.Mutex
	mu         sync.Mutex
	command    string
	cmd        *exec.Cmd
	done       chan struct{}
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
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.stopAndWait()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return fmt.Errorf("player is closed")
	}
	p.generation++
	gen := p.generation
	args := []string{"-nodisp", "-autoexit", "-loglevel", "quiet", "-volume", strconv.Itoa(volume)}
	if offset > 0 {
		args = append(args, "-ss", strconv.FormatFloat(offset.Seconds(), 'f', 3, 64))
	}
	args = append(args, url)
	cmd := exec.Command(p.command, args...)
	if err := cmd.Start(); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("start ffplay: %w", err)
	}
	done := make(chan struct{})
	p.cmd = cmd
	p.done = done
	p.mu.Unlock()
	go func() {
		err := cmd.Wait()
		close(done)
		p.mu.Lock()
		valid := !p.closed && p.generation == gen && p.cmd == cmd
		if valid {
			p.cmd = nil
		}
		p.mu.Unlock()
		if valid && onEnd != nil {
			onEnd()
		}
		_ = err
	}()
	return nil
}

func (p *FFPlay) stopAndWait() {
	p.mu.Lock()
	cmd, done := p.cmd, p.done
	if cmd == nil {
		p.mu.Unlock()
		return
	}
	p.generation++
	p.cmd = nil
	p.done = nil
	p.mu.Unlock()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	<-done
}

func (p *FFPlay) Stop() error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.stopAndWait()
	return nil
}
func (p *FFPlay) Close() error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.stopAndWait()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
	}
	return nil
}
