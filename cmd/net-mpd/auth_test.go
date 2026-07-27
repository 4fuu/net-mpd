package main

import (
	"bytes"
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

func TestSDKLogFilterDropsCredentialDiagnostics(t *testing.T) {
	var out bytes.Buffer
	filter := sdkLogFilter{Writer: &out}
	secret := []byte("url: https://example, reqOptions: Cookies:[MUSIC_U=secret], resCookies: []")
	if n, err := filter.Write(secret); err != nil || n != len(secret) {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if out.Len() != 0 {
		t.Fatalf("credential diagnostic was written: %q", out.String())
	}
	if _, err := filter.Write([]byte("server ready\n")); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "server ready\n" {
		t.Fatalf("ordinary log = %q", got)
	}
}
