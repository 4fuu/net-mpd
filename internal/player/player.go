package player

import (
	"time"
)

// Player is the native playback backend used by the MPD state machine.
type Player interface {
	Play(url, format string, offset time.Duration, volume int, onEnd func(error)) error
	Pause() error
	Resume() error
	Seek(time.Duration) error
	SetVolume(int) error
	Position() time.Duration
	Stop() error
	Close() error
}
