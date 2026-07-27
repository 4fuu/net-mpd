//go:build !darwin

package sysmedia

// New returns a no-op media session on non-Darwin platforms.
//
// TODO(windows): wire SystemMediaTransportControls (SMTC) like go-musicfox's
// internal/remote_control/remote_control_windows.go so taskbar media keys and
// the Windows media flyout show title/artist/album and can pause/next/seek.
func New(Controller) Control {
	return noop{}
}

type noop struct{}

func (noop) SetPlayingInfo(PlayingInfo) {}
func (noop) Release()                   {}
