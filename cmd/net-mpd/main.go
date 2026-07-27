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
	if len(os.Args) > 1 && isAuthCommand(os.Args[1]) {
		if err := runAuthCommand(os.Args[1], os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	listenAddr := flag.String("listen", "127.0.0.1:6600", "MPD listen address")
	cookiePath := flag.String("cookie", defaultCookiePath(), "net-mpd cookie file")
	ffplay := flag.String("ffplay", "ffplay", "ffplay executable path")
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
