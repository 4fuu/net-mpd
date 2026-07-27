//go:build darwin

package macdriver

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// AVPlayer loads media and posts notifications asynchronously on the main
// run loop. Without NSApplication/CFRunLoop pumping, Play() appears to succeed
// but nothing is fetched, Position stays 0, and end/fail callbacks never fire.
//
// RunApp mirrors go-musicfox: lock the OS main thread, start a prohibited
// (headless) NSApplication, run the real program on a background goroutine,
// then keep the run loop alive until that program returns.

var (
	appKitOnce     sync.Once
	appKitErr      error
	classNSApp     objc.Class
	selSharedApp   = objc.RegisterName("sharedApplication")
	selSetPolicy   = objc.RegisterName("setActivationPolicy:")
	selActivate    = objc.RegisterName("activateIgnoringOtherApps:")
	selRun         = objc.RegisterName("run")
	selStop        = objc.RegisterName("stop:")
	selTerminate   = objc.RegisterName("terminate:")
	selPerformMain = objc.RegisterName("performSelectorOnMainThread:withObject:waitUntilDone:")
)

const nsApplicationActivationPolicyProhibited = 2

func loadAppKit() error {
	appKitOnce.Do(func() {
		if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL); err != nil {
			appKitErr = err
			return
		}
		classNSApp = objc.GetClass("NSApplication")
		if classNSApp == 0 {
			appKitErr = fmt.Errorf("NSApplication class not found")
		}
	})
	return appKitErr
}

// RunApp must be called from main. It never returns until f returns.
func RunApp(f func()) {
	runtime.LockOSThread()
	if err := loadAppKit(); err != nil {
		// Fall back to running without a run loop; playback will not work.
		f()
		return
	}
	app := objc.ID(classNSApp).Send(selSharedApp)
	if app == 0 {
		f()
		return
	}
	app.Send(selSetPolicy, nsApplicationActivationPolicyProhibited)
	app.Send(selActivate, true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
		// Stop the run loop from the main thread.
		app.Send(selPerformMain, selTerminate, objc.ID(0), true)
	}()
	app.Send(selRun)
	<-done
}
