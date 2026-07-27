//go:build windows

package player

import "testing"

func TestNativeLifecycle(t *testing.T) {
	p, err := NewNative()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetVolume(42); err != nil {
		p.Close()
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
