package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/4fuu/net-mpd/internal/ncm"
	"github.com/skip2/go-qrcode"
)

func isAuthCommand(command string) bool {
	switch command {
	case "login", "status", "refresh", "logout", "import-cookie", "cookie-path":
		return true
	default:
		return false
	}
}

func defaultCookiePath() string {
	if home := os.Getenv("NET_MPD_HOME"); home != "" {
		return filepath.Join(home, "cookie")
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base, _ = os.UserHomeDir()
	}
	return filepath.Join(base, "net-mpd", "cookie")
}

func runAuthCommand(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	cookiePath := flags.String("cookie", defaultCookiePath(), "net-mpd cookie file")
	timeout := flags.Duration("timeout", 5*time.Minute, "QR login timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	wantArgs := 0
	if command == "import-cookie" {
		wantArgs = 1
	}
	if flags.NArg() != wantArgs {
		if command == "import-cookie" {
			return errors.New("usage: net-mpd import-cookie [options] <cookie-file>")
		}
		return fmt.Errorf("usage: net-mpd %s [options]", command)
	}
	if command == "cookie-path" {
		fmt.Println(*cookiePath)
		return nil
	}
	client, err := ncm.Open(*cookiePath)
	if err != nil {
		return err
	}
	switch command {
	case "login":
		return login(client, *timeout)
	case "status":
		user, err := client.Account()
		if err != nil {
			return fmt.Errorf("not logged in: %w", err)
		}
		fmt.Printf("Logged in as %s (user %d)\nCookie: %s\n", user.Nickname, user.ID, client.CookiePath())
		return nil
	case "refresh":
		if err := client.RefreshLogin(); err != nil {
			return err
		}
		user, err := client.Account()
		if err != nil {
			return fmt.Errorf("verify refreshed login: %w", err)
		}
		fmt.Printf("Refreshed login for %s\n", user.Nickname)
		return nil
	case "logout":
		remoteErr, localErr := client.Logout()
		if remoteErr != nil {
			fmt.Fprintf(os.Stderr, "Remote logout warning: %v\n", remoteErr)
		}
		if localErr != nil {
			return localErr
		}
		fmt.Printf("Local credentials removed from %s\n", client.CookiePath())
		return nil
	case "import-cookie":
		if err := client.ImportCookies(flags.Arg(0)); err != nil {
			return err
		}
		user, err := client.Account()
		if err != nil {
			return fmt.Errorf("imported cookies are not logged in: %w", err)
		}
		fmt.Printf("Imported login for %s into %s\n", user.Nickname, client.CookiePath())
		return nil
	}
	return fmt.Errorf("unknown command %q", command)
}

func login(client *ncm.Client, timeout time.Duration) error {
	if user, err := client.Account(); err == nil {
		fmt.Printf("Already logged in as %s (user %d)\n", user.Nickname, user.ID)
		return nil
	}
	session, err := client.BeginQRLogin()
	if err != nil {
		return err
	}
	code, err := qrcode.New(session.URL, qrcode.Medium)
	if err != nil {
		return fmt.Errorf("render QR code: %w", err)
	}
	fmt.Println("Scan this QR code with the NetEase Cloud Music app, then confirm login:")
	fmt.Print(code.ToSmallString(false))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lastStatus := 0
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("login canceled: %w", ctx.Err())
		case <-time.After(time.Second):
		}
		status, err := client.CheckQRLogin(session.Key)
		if err != nil {
			return err
		}
		if status.Code != lastStatus {
			switch status.Code {
			case 801:
				fmt.Println("Waiting for scan...")
			case 802:
				fmt.Println("Scanned; confirm login in the app...")
			}
			lastStatus = status.Code
		}
		switch status.Code {
		case 801, 802:
			continue
		case 803:
			user, err := client.Account()
			if err != nil {
				return fmt.Errorf("verify login: %w", err)
			}
			fmt.Printf("Logged in as %s (user %d)\nCookie saved to %s\n", user.Nickname, user.ID, client.CookiePath())
			return nil
		case 800:
			return errors.New("QR code expired; run login again")
		default:
			return fmt.Errorf("unexpected QR status %d: %s", status.Code, status.Message)
		}
	}
}
