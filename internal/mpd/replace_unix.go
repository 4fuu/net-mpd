//go:build !windows

package mpd

import "os"

func replaceFile(source, destination string) error { return os.Rename(source, destination) }
