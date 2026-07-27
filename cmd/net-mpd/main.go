package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/4fuu/net-mpd/internal/mpd"
	"github.com/4fuu/net-mpd/internal/ncm"
	"github.com/4fuu/net-mpd/internal/player"
)

var version = "dev"

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:6600", "MPD listen address")
	cookiePath := flag.String("cookie", "", "go-musicfox cookie file (auto-detected by default)")
	ffplay := flag.String("ffplay", "ffplay", "ffplay executable path")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	if *cookiePath == "" {
		*cookiePath = findCookie()
	}
	if *cookiePath == "" {
		log.Fatal("go-musicfox cookie not found; pass -cookie or set MUSICFOX_COOKIE_FILE")
	}

	music, err := ncm.Open(*cookiePath)
	if err != nil {
		log.Fatal(err)
	}
	catalog, err := mpd.NewCatalog(music)
	if err != nil {
		log.Fatalf("authenticate with go-musicfox session: %v", err)
	}
	backend := player.NewFFPlay(*ffplay)
	defer backend.Close()
	state := mpd.NewState(catalog, backend)
	server := mpd.NewServer(catalog, state)

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	log.Printf("MPD 0.23.5 listening on %s for %s", listener.Addr(), catalog.User().Nickname)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case <-signals:
	case err := <-errCh:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			log.Fatal(err)
		}
	}
}

func findCookie() string {
	if path := os.Getenv("MUSICFOX_COOKIE_FILE"); isFile(path) {
		return path
	}
	var candidates []string
	if root := os.Getenv("MUSICFOX_ROOT"); root != "" {
		candidates = append(candidates, filepath.Join(root, "data", "cookie"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, "scoop", "persist", "go-musicfox", "data", "data", "cookie"),
			filepath.Join(home, ".local", "share", "go-musicfox", "cookie"),
		)
	}
	for _, base := range []string{os.Getenv("XDG_DATA_HOME"), os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA")} {
		if base != "" {
			candidates = append(candidates, filepath.Join(base, "go-musicfox", "cookie"))
		}
	}
	for _, path := range candidates {
		if isFile(path) {
			return path
		}
	}
	return ""
}

func isFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func init() {
	flag.Usage = func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [options]\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
}
