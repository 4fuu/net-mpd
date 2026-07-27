// Package sysmedia bridges net-mpd playback to the host OS media session
// (Now Playing / lock screen / headset buttons).
package sysmedia

import "time"

// PlayingInfo is the snapshot pushed to the system media UI.
type PlayingInfo struct {
	Title    string
	Artist   string
	Album    string
	CoverURL string
	TrackID  int64
	Duration time.Duration
	Elapsed  time.Duration
	// State is one of "play", "pause", "stop".
	State string
}

// Controller is implemented by mpd.State so remote commands can drive playback.
type Controller interface {
	CtrlPause()
	CtrlResume()
	CtrlToggle()
	CtrlStop()
	CtrlNext()
	CtrlPrevious()
	CtrlSeek(time.Duration)
}

// Control is the platform media session.
type Control interface {
	SetPlayingInfo(PlayingInfo)
	Release()
}
