package main

import (
	"bytes"
	"io"
)

type sdkLogFilter struct {
	io.Writer
}

func (w sdkLogFilter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("reqOptions:")) && bytes.Contains(p, []byte("resCookies:")) {
		return len(p), nil
	}
	return w.Writer.Write(p)
}
