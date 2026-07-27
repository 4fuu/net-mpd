//go:build darwin

// Package macdriver contains the minimal Foundation/AVFoundation bridge used
// by net-mpd. Adapted from go-musicfox's macdriver and Darwin AVPlayer backend.
//
// MIT License
// Copyright (c) 2015-present Microsoft Corporation
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies, subject to inclusion of this notice. THE SOFTWARE IS PROVIDED "AS IS",
// WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED.
package macdriver

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

type CMTime struct {
	Value     int64
	Timescale int32
	Flags     uint32
	Epoch     int64
}

var (
	classPlayer, classItem, classString, classURL, classCenter, classSignature, classInvocation objc.Class
	observerClass                                                                               objc.Class
	selAlloc                                                                                    = objc.RegisterName("alloc")
	selInit                                                                                     = objc.RegisterName("init")
	selRelease                                                                                  = objc.RegisterName("release")
	selObject                                                                                   = objc.RegisterName("object")
	selPlay                                                                                     = objc.RegisterName("play")
	selPause                                                                                    = objc.RegisterName("pause")
	selCurrentTime                                                                              = objc.RegisterName("currentTime")
	selReplace                                                                                  = objc.RegisterName("replaceCurrentItemWithPlayerItem:")
	selSeek                                                                                     = objc.RegisterName("seekToTime:")
	selVolume                                                                                   = objc.RegisterName("setVolume:")
	selItemURL                                                                                  = objc.RegisterName("playerItemWithURL:")
	selStringInit                                                                               = objc.RegisterName("initWithUTF8String:")
	selURLString                                                                                = objc.RegisterName("URLWithString:")
	selDefault                                                                                  = objc.RegisterName("defaultCenter")
	selAdd                                                                                      = objc.RegisterName("addObserver:selector:name:object:")
	selRemove                                                                                   = objc.RegisterName("removeObserver:name:object:")
	selFinish                                                                                   = objc.RegisterName("netMPDFinish:")
	selFail                                                                                     = objc.RegisterName("netMPDFail:")
	selError                                                                                    = objc.RegisterName("error")
	selDescription                                                                              = objc.RegisterName("localizedDescription")
	selUTF8                                                                                     = objc.RegisterName("UTF8String")
	selSignature                                                                                = objc.RegisterName("instanceMethodSignatureForSelector:")
	selInvocation                                                                               = objc.RegisterName("invocationWithMethodSignature:")
	selSetSelector                                                                              = objc.RegisterName("setSelector:")
	selSetArgument                                                                              = objc.RegisterName("setArgument:atIndex:")
	selInvoke                                                                                   = objc.RegisterName("invokeWithTarget:")
	selGetReturn                                                                                = objc.RegisterName("getReturnValue:")

	poolPush func() unsafe.Pointer
	poolPop  func(unsafe.Pointer)

	dispatchMu sync.RWMutex
	dispatch   = map[objc.ID]func(bool, objc.ID){}
)

func init() {
	foundation, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	if _, err = purego.Dlopen("/System/Library/Frameworks/AVFoundation.framework/AVFoundation", purego.RTLD_GLOBAL); err != nil {
		panic(err)
	}
	objcLib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	_ = foundation
	purego.RegisterLibFunc(&poolPush, objcLib, "objc_autoreleasePoolPush")
	purego.RegisterLibFunc(&poolPop, objcLib, "objc_autoreleasePoolPop")
	classPlayer, classItem = objc.GetClass("AVPlayer"), objc.GetClass("AVPlayerItem")
	classString, classURL = objc.GetClass("NSString"), objc.GetClass("NSURL")
	classCenter = objc.GetClass("NSNotificationCenter")
	classSignature, classInvocation = objc.GetClass("NSMethodSignature"), objc.GetClass("NSInvocation")
	observerClass, err = objc.RegisterClass("NetMPDAVPlayerObserver", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: selFinish, Fn: observerFinish}, {Cmd: selFail, Fn: observerFail},
	})
	if err != nil {
		panic(err)
	}
}

func autorelease(body func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p := poolPush()
	defer poolPop(p)
	body()
}

func observerFinish(id objc.ID, _ objc.SEL, note objc.ID) { dispatchNote(id, false, note) }
func observerFail(id objc.ID, _ objc.SEL, note objc.ID)   { dispatchNote(id, true, note) }
func dispatchNote(id objc.ID, failed bool, note objc.ID) {
	dispatchMu.RLock()
	fn := dispatch[id]
	dispatchMu.RUnlock()
	if fn != nil {
		fn(failed, note.Send(selObject))
	}
}

// Player owns retained AVPlayer and observer objects. Calls are expected to be
// serialized by its caller; notification delivery may occur concurrently.
type Player struct {
	player, observer, item objc.ID
	current                atomic.Uintptr
}

func NewPlayer(notify func(failed bool, item uintptr)) (*Player, error) {
	var p *Player
	autorelease(func() {
		pid := objc.ID(classPlayer).Send(selAlloc).Send(selInit)
		oid := objc.ID(observerClass).Send(selAlloc).Send(selInit)
		if pid != 0 && oid != 0 {
			p = &Player{player: pid, observer: oid}
			dispatchMu.Lock()
			dispatch[oid] = func(f bool, item objc.ID) {
				if p.current.Load() == uintptr(item) {
					notify(f, uintptr(item))
				}
			}
			dispatchMu.Unlock()
		}
	})
	if p == nil {
		return nil, fmt.Errorf("create AVPlayer")
	}
	return p, nil
}

func nsString(s string) objc.ID { return objc.ID(classString).Send(selAlloc).Send(selStringInit, s) }
func (p *Player) SetItem(rawURL string) error {
	var item objc.ID
	autorelease(func() {
		s := nsString(rawURL)
		defer s.Send(selRelease)
		u := objc.ID(classURL).Send(selURLString, s)
		if u != 0 {
			item = objc.ID(classItem).Send(selItemURL, u)
		}
		if item != 0 {
			item.Send(objc.RegisterName("retain"))
		}
	})
	if item == 0 {
		return fmt.Errorf("AVFoundation rejected media URL")
	}
	p.removeObserver()
	p.player.Send(selReplace, item)
	p.item = item
	p.current.Store(uintptr(item))
	center := objc.ID(classCenter).Send(selDefault)
	for _, n := range []struct {
		name string
		sel  objc.SEL
	}{{"AVPlayerItemDidPlayToEndTimeNotification", selFinish}, {"AVPlayerItemFailedToPlayToEndTimeNotification", selFail}} {
		autorelease(func() { s := nsString(n.name); center.Send(selAdd, p.observer, n.sel, s, item); s.Send(selRelease) })
	}
	return nil
}
func (p *Player) removeObserver() {
	if p.item == 0 {
		return
	}
	p.current.Store(0)
	center := objc.ID(classCenter).Send(selDefault)
	for _, name := range []string{"AVPlayerItemDidPlayToEndTimeNotification", "AVPlayerItemFailedToPlayToEndTimeNotification"} {
		autorelease(func() { s := nsString(name); center.Send(selRemove, p.observer, s, p.item); s.Send(selRelease) })
	}
	p.item.Send(selRelease)
	p.item = 0
}
func invoke(target objc.ID, class objc.Class, sel objc.SEL, arg unsafe.Pointer, result unsafe.Pointer) {
	autorelease(func() {
		sig := objc.ID(class).Send(selSignature, sel)
		inv := objc.ID(classInvocation).Send(selInvocation, sig)
		inv.Send(selSetSelector, sel)
		if arg != nil {
			inv.Send(selSetArgument, arg, 2)
		}
		inv.Send(selInvoke, target)
		if result != nil {
			inv.Send(selGetReturn, result)
		}
	})
}
func (p *Player) Play()         { p.player.Send(selPlay) }
func (p *Player) Pause()        { p.player.Send(selPause) }
func (p *Player) Seek(t CMTime) { invoke(p.player, classPlayer, selSeek, unsafe.Pointer(&t), nil) }
func (p *Player) SetVolume(v float32) {
	invoke(p.player, classPlayer, selVolume, unsafe.Pointer(&v), nil)
}
func (p *Player) Position() CMTime {
	var t CMTime
	invoke(p.player, classPlayer, selCurrentTime, nil, unsafe.Pointer(&t))
	return t
}
func (p *Player) IsCurrent(item uintptr) bool { return p.current.Load() == item && item != 0 }
func (p *Player) ErrorForItem(item uintptr) error {
	if objc.ID(item) == 0 {
		return fmt.Errorf("AVPlayer item failed")
	}
	errID := objc.ID(item).Send(selError)
	if errID == 0 {
		return fmt.Errorf("AVPlayer item failed")
	}
	desc := errID.Send(selDescription).Send(selUTF8)
	if desc == 0 {
		return fmt.Errorf("AVPlayer item failed")
	}
	return fmt.Errorf("AVPlayer item failed: %s", cString(desc))
}
func cString(ptr objc.ID) string {
	b := (*byte)(unsafe.Pointer(ptr))
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(b), n)) != 0 {
		n++
	}
	return unsafe.String(b, n)
}
func (p *Player) Clear() { p.removeObserver(); p.player.Send(selReplace, objc.ID(0)) }
func (p *Player) Close() {
	p.Clear()
	dispatchMu.Lock()
	delete(dispatch, p.observer)
	dispatchMu.Unlock()
	p.observer.Send(selRelease)
	p.player.Send(selRelease)
	p.observer, p.player = 0, 0
}
