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
	"github.com/4fuu/net-mpd/internal/sysmedia"
)

var version = "dev"

func main() {
	log.SetOutput(sdkLogFilter{Writer: os.Stderr})
	if len(os.Args) > 1 && isAuthCommand(os.Args[1]) {
		if err := runAuthCommand(os.Args[1], os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	// On macOS AVPlayer needs the AppKit run loop; other platforms just call
	// runServer directly (see run_*.go).
	run(runServer)
}

func runServer() {
	listenAddr := flag.String("listen", "127.0.0.1:6600", "MPD listen address")
	cookiePath := flag.String("cookie", defaultCookiePath(), "net-mpd cookie file")
	password := flag.String("password", os.Getenv("NET_MPD_PASSWORD"), "MPD client password (default NET_MPD_PASSWORD)")
	stickerPath := flag.String("stickers", "", "sticker JSON file (default beside cookie)")
	lyricsPath := flag.String("lyrics", "", "lyrics cache directory for rmpc (default beside cookie)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	music, err := ncm.Open(*cookiePath)
	if err != nil {
		log.Fatal(err)
	}
	catalog, err := mpd.NewCatalog(music)
	if err != nil {
		log.Fatalf("authenticate NetEase session: %v (run %s login)", err, filepath.Base(os.Args[0]))
	}
	if *lyricsPath == "" {
		*lyricsPath = filepath.Join(filepath.Dir(*cookiePath), "lyrics")
	}
	if err := os.MkdirAll(*lyricsPath, 0o700); err != nil {
		log.Fatalf("lyrics directory: %v", err)
	}
	catalog.SetLyricsDir(*lyricsPath)
	log.Printf("lyrics cache: %s (set rmpc lyrics_dir to this path)", *lyricsPath)
	// Warm the library cache so rmpc tag browsers (list Artist/Album) are responsive.
	go func() {
		if _, err := catalog.AllSongs(); err != nil {
			log.Printf("library warm-up failed: %v", err)
		}
	}()
	backend, err := player.NewNative()
	if err != nil {
		log.Fatalf("initialize native audio player: %v", err)
	}
	defer backend.Close()
	state := mpd.NewState(catalog, backend)
	media := sysmedia.New(state)
	defer media.Release()
	state.AttachMedia(media)
	server := mpd.NewServer(catalog, state)
	server.SetPassword(*password)
	if *stickerPath == "" {
		*stickerPath = filepath.Join(filepath.Dir(*cookiePath), "stickers.json")
	}
	if err := server.SetStickerPath(*stickerPath); err != nil {
		log.Fatalf("open sticker store: %v", err)
	}

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

func init() {
	flag.Usage = func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [options]\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "\nAuthentication commands:")
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "  login          Log in with a NetEase Cloud Music QR code")
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "  status         Show the current login")
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "  refresh        Refresh and persist the login session")
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "  logout         Log out and remove local credentials")
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "  import-cookie  Import an existing persistent cookie jar")
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "  cookie-path    Print the default cookie path")
	}
}
