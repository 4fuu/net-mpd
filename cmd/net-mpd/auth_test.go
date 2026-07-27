package main

import (
	"path/filepath"
	"testing"
)

func TestDefaultCookiePathUsesNetMPDHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NET_MPD_HOME", home)
	if got, want := defaultCookiePath(), filepath.Join(home, "cookie"); got != want {
		t.Fatalf("defaultCookiePath() = %q, want %q", got, want)
	}
}

func TestAuthCommands(t *testing.T) {
	for _, command := range []string{"login", "status", "refresh", "logout", "import-cookie", "cookie-path"} {
		if !isAuthCommand(command) {
			t.Fatalf("%q was not recognized as an auth command", command)
		}
	}
	if isAuthCommand("serve") {
		t.Fatal("serve was incorrectly recognized as an auth command")
	}
}

func TestAuthCommandRejectsUnexpectedArguments(t *testing.T) {
	if err := runAuthCommand("cookie-path", []string{"unexpected"}); err == nil {
		t.Fatal("cookie-path accepted an unexpected positional argument")
	}
	if err := runAuthCommand("import-cookie", nil); err == nil {
		t.Fatal("import-cookie accepted a missing source path")
	}
}
