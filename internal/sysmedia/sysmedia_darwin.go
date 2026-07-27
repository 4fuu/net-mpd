//go:build darwin

// macOS Now Playing + remote commands via MediaPlayer.framework.
// Adapted from go-musicfox's remote_control / macdriver/mediaplayer (MIT).
package sysmedia

import (
	"sync"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/4fuu/net-mpd/internal/macdriver"
)

// MPRemoteCommandHandlerStatus. purego encodes int32 as ObjC 'i', which is what
// MediaPlayer accepts when validating addTarget:action: (same as go-musicfox).
type handlerStatus int32

const (
	mpStateUnknown = 0
	mpStatePlaying = 1
	mpStatePaused  = 2
	mpStateStopped = 3

	mpHandlerSuccess handlerStatus = 0
	mpHandlerFailed  handlerStatus = 200

	mpMediaTypeMusic = 1

	keyElapsed     = "MPNowPlayingInfoPropertyElapsedPlaybackTime"
	keyRate        = "MPNowPlayingInfoPropertyPlaybackRate"
	keyDefaultRate = "MPNowPlayingInfoPropertyDefaultPlaybackRate"
	keyMediaTypeNP = "MPNowPlayingInfoPropertyMediaType"
	keyDuration    = "playbackDuration"
	keyPersistent  = "persistentID"
	keyTitle       = "title"
	keyAlbum       = "albumTitle"
	keyArtist      = "artist"
	keyAlbumArtist = "albumArtist"
	keyMediaType   = "mediaType"
	keyArtwork     = "artwork"
)

var (
	classNowPlaying   objc.Class
	classCommandCtr   objc.Class
	classDict         objc.Class
	classNumber       objc.Class
	classString       objc.Class
	classURL          objc.Class
	classImage        objc.Class
	classArtwork      objc.Class
	handlerClass      objc.Class
	mediaPlayerLoaded sync.Once

	selDefaultCenter   = objc.RegisterName("defaultCenter")
	selSharedCenter    = objc.RegisterName("sharedCommandCenter")
	selSetPlayback     = objc.RegisterName("setPlaybackState:")
	selSetNowPlaying   = objc.RegisterName("setNowPlayingInfo:")
	selAlloc           = objc.RegisterName("alloc")
	selInit            = objc.RegisterName("init")
	selRelease         = objc.RegisterName("release")
	selNew             = objc.RegisterName("new")
	selSetObjectForKey = objc.RegisterName("setObject:forKey:")
	selNumberWithInt   = objc.RegisterName("numberWithInt:")
	selNumberWithDbl   = objc.RegisterName("numberWithDouble:")
	selInitUTF8        = objc.RegisterName("initWithUTF8String:")
	selURLWithString   = objc.RegisterName("URLWithString:")
	selInitWithURL     = objc.RegisterName("initWithContentsOfURL:")
	selInitWithImage   = objc.RegisterName("initWithImage:")
	selAddTargetAction = objc.RegisterName("addTarget:action:")
	selPlayCommand     = objc.RegisterName("playCommand")
	selPauseCommand    = objc.RegisterName("pauseCommand")
	selStopCommand     = objc.RegisterName("stopCommand")
	selToggleCommand   = objc.RegisterName("togglePlayPauseCommand")
	selNextCommand     = objc.RegisterName("nextTrackCommand")
	selPrevCommand     = objc.RegisterName("previousTrackCommand")
	selSeekCommand     = objc.RegisterName("changePlaybackPositionCommand")
	selPositionTime    = objc.RegisterName("positionTime")

	selHandlePlay   = objc.RegisterName("netMPDHandlePlay:")
	selHandlePause  = objc.RegisterName("netMPDHandlePause:")
	selHandleStop   = objc.RegisterName("netMPDHandleStop:")
	selHandleToggle = objc.RegisterName("netMPDHandleToggle:")
	selHandleNext   = objc.RegisterName("netMPDHandleNext:")
	selHandlePrev   = objc.RegisterName("netMPDHandlePrev:")
	selHandleSeek   = objc.RegisterName("netMPDHandleSeek:")

	controller Controller
)

type darwinControl struct {
	mu         sync.Mutex
	center     objc.ID
	commands   objc.ID
	handler    objc.ID
	artwork    objc.ID
	artworkURL string
	closed     bool
}

// New registers Now Playing metadata and remote command handlers.
func New(c Controller) Control {
	mediaPlayerLoaded.Do(func() {
		if _, err := purego.Dlopen("/System/Library/Frameworks/MediaPlayer.framework/MediaPlayer", purego.RTLD_GLOBAL); err != nil {
			return
		}
		if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL); err != nil {
			return
		}
		classNowPlaying = objc.GetClass("MPNowPlayingInfoCenter")
		classCommandCtr = objc.GetClass("MPRemoteCommandCenter")
		classDict = objc.GetClass("NSMutableDictionary")
		classNumber = objc.GetClass("NSNumber")
		classString = objc.GetClass("NSString")
		classURL = objc.GetClass("NSURL")
		classImage = objc.GetClass("NSImage")
		classArtwork = objc.GetClass("MPMediaItemArtwork")
		var err error
		handlerClass, err = objc.RegisterClass(
			"NetMPDRemoteCommandHandler",
			objc.GetClass("NSObject"),
			nil,
			nil,
			[]objc.MethodDef{
				{Cmd: selHandlePlay, Fn: handlePlay},
				{Cmd: selHandlePause, Fn: handlePause},
				{Cmd: selHandleStop, Fn: handleStop},
				{Cmd: selHandleToggle, Fn: handleToggle},
				{Cmd: selHandleNext, Fn: handleNext},
				{Cmd: selHandlePrev, Fn: handlePrev},
				{Cmd: selHandleSeek, Fn: handleSeek},
			},
		)
		if err != nil {
			handlerClass = 0
		}
	})
	if classNowPlaying == 0 || classCommandCtr == 0 || handlerClass == 0 {
		return noopDarwin{}
	}
	controller = c
	d := &darwinControl{}
	macdriver.Autorelease(func() {
		d.center = objc.ID(classNowPlaying).Send(selDefaultCenter)
		d.commands = objc.ID(classCommandCtr).Send(selSharedCenter)
		d.handler = objc.ID(handlerClass).Send(selNew)
		d.registerCommands()
		d.center.Send(selSetPlayback, mpStateStopped)
	})
	return d
}

func (d *darwinControl) registerCommands() {
	if d.commands == 0 || d.handler == 0 {
		return
	}
	bind := func(getter objc.SEL, action objc.SEL) {
		cmd := d.commands.Send(getter)
		if cmd != 0 {
			cmd.Send(selAddTargetAction, d.handler, action)
		}
	}
	bind(selPlayCommand, selHandlePlay)
	bind(selPauseCommand, selHandlePause)
	bind(selStopCommand, selHandleStop)
	bind(selToggleCommand, selHandleToggle)
	bind(selNextCommand, selHandleNext)
	bind(selPrevCommand, selHandlePrev)
	bind(selSeekCommand, selHandleSeek)
}

func (d *darwinControl) SetPlayingInfo(info PlayingInfo) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.center == 0 {
		return
	}
	macdriver.Autorelease(func() {
		dic := objc.ID(classDict).Send(selAlloc).Send(selInit)
		if dic == 0 {
			return
		}
		defer dic.Send(selRelease)

		setStr := func(key, val string) {
			if val == "" {
				return
			}
			k, v := nsString(key), nsString(val)
			dic.Send(selSetObjectForKey, v, k)
			k.Send(selRelease)
			v.Send(selRelease)
		}
		setInt := func(key string, val int32) {
			k := nsString(key)
			n := objc.ID(classNumber).Send(selNumberWithInt, val)
			dic.Send(selSetObjectForKey, n, k)
			k.Send(selRelease)
		}
		setDbl := func(key string, val float64) {
			k := nsString(key)
			n := objc.ID(classNumber).Send(selNumberWithDbl, val)
			dic.Send(selSetObjectForKey, n, k)
			k.Send(selRelease)
		}

		total := info.Duration.Seconds()
		elapsed := info.Elapsed.Seconds()
		if total < 0 {
			total = 0
		}
		if elapsed < 0 {
			elapsed = 0
		}
		setDbl(keyDuration, total)
		setDbl(keyElapsed, elapsed)
		setDbl(keyDefaultRate, 1.0)
		rate := 0.0
		state := mpStateStopped
		switch info.State {
		case "play":
			rate = 1.0
			state = mpStatePlaying
		case "pause":
			state = mpStatePaused
		default:
			state = mpStateStopped
		}
		setDbl(keyRate, rate)
		setInt(keyMediaTypeNP, 1) // audio
		setInt(keyMediaType, mpMediaTypeMusic)
		if info.TrackID != 0 {
			// persistentID is NSNumber; keep within int32 range for the binding.
			id := info.TrackID
			if id > 0x7fffffff {
				id = id & 0x7fffffff
			}
			setInt(keyPersistent, int32(id))
		}
		setStr(keyTitle, info.Title)
		setStr(keyArtist, info.Artist)
		setStr(keyAlbum, info.Album)
		setStr(keyAlbumArtist, info.Artist)

		if info.CoverURL != "" {
			if art := d.artworkFor(info.CoverURL); art != 0 {
				k := nsString(keyArtwork)
				dic.Send(selSetObjectForKey, art, k)
				k.Send(selRelease)
			}
		}

		d.center.Send(selSetPlayback, state)
		d.center.Send(selSetNowPlaying, dic)
		// Re-bind commands; some macOS versions drop targets after info updates.
		d.registerCommands()
	})
}

func (d *darwinControl) artworkFor(url string) objc.ID {
	if url == d.artworkURL && d.artwork != 0 {
		return d.artwork
	}
	s := nsString(url)
	defer s.Send(selRelease)
	u := objc.ID(classURL).Send(selURLWithString, s)
	if u == 0 {
		return 0
	}
	img := objc.ID(classImage).Send(selAlloc).Send(selInitWithURL, u)
	if img == 0 {
		return 0
	}
	defer img.Send(selRelease)
	art := objc.ID(classArtwork).Send(selAlloc).Send(selInitWithImage, img)
	if art == 0 {
		return 0
	}
	if d.artwork != 0 {
		d.artwork.Send(selRelease)
	}
	d.artwork = art
	d.artworkURL = url
	return art
}

func (d *darwinControl) Release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.closed = true
	if d.artwork != 0 {
		d.artwork.Send(selRelease)
		d.artwork = 0
	}
	// Shared centers are not released; drop handler retain only.
	if d.handler != 0 {
		d.handler.Send(selRelease)
		d.handler = 0
	}
	if controller != nil {
		controller = nil
	}
}

func nsString(s string) objc.ID {
	return objc.ID(classString).Send(selAlloc).Send(selInitUTF8, s)
}

func handlePlay(id objc.ID, _ objc.SEL, _ objc.ID) handlerStatus {
	if controller == nil {
		return mpHandlerFailed
	}
	controller.CtrlResume()
	return mpHandlerSuccess
}
func handlePause(id objc.ID, _ objc.SEL, _ objc.ID) handlerStatus {
	if controller == nil {
		return mpHandlerFailed
	}
	controller.CtrlPause()
	return mpHandlerSuccess
}
func handleStop(id objc.ID, _ objc.SEL, _ objc.ID) handlerStatus {
	if controller == nil {
		return mpHandlerFailed
	}
	controller.CtrlStop()
	return mpHandlerSuccess
}
func handleToggle(id objc.ID, _ objc.SEL, _ objc.ID) handlerStatus {
	if controller == nil {
		return mpHandlerFailed
	}
	controller.CtrlToggle()
	return mpHandlerSuccess
}
func handleNext(id objc.ID, _ objc.SEL, _ objc.ID) handlerStatus {
	if controller == nil {
		return mpHandlerFailed
	}
	controller.CtrlNext()
	return mpHandlerSuccess
}
func handlePrev(id objc.ID, _ objc.SEL, _ objc.ID) handlerStatus {
	if controller == nil {
		return mpHandlerFailed
	}
	controller.CtrlPrevious()
	return mpHandlerSuccess
}
func handleSeek(id objc.ID, _ objc.SEL, event objc.ID) handlerStatus {
	if controller == nil || event == 0 {
		return mpHandlerFailed
	}
	// MPChangePlaybackPositionCommandEvent.positionTime is NSTimeInterval (double).
	pos := objc.Send[float64](event, selPositionTime)
	controller.CtrlSeek(time.Duration(pos * float64(time.Second)))
	return mpHandlerSuccess
}

type noopDarwin struct{}

func (noopDarwin) SetPlayingInfo(PlayingInfo) {}
func (noopDarwin) Release()                   {}
