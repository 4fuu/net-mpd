//go:build darwin

package main

import "github.com/4fuu/net-mpd/internal/macdriver"

func run(f func()) {
	macdriver.RunApp(f)
}
