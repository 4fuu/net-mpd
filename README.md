# net-mpd

[![CI](https://github.com/4fuu/net-mpd/actions/workflows/ci.yml/badge.svg)](https://github.com/4fuu/net-mpd/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/4fuu/net-mpd)](https://github.com/4fuu/net-mpd/releases/latest)

`net-mpd` is an MPD 0.23.5-compatible server that exposes a NetEase Cloud Music
account to MPD clients such as RMPC. It manages its own login and persistent
cookie jar, and resolves temporary playback URLs only when a song starts.

## Requirements

- Go 1.26 or newer when building from source

Audio playback is built in. net-mpd uses Windows MediaPlayer on Windows,
AVPlayer on macOS, and Beep/Oto on Linux; no external player is required.
Linux requires a working PulseAudio-compatible server, or `libasound.so.2` for
the ALSA fallback.

### Homebrew

This repository is also a Homebrew tap for macOS and Linux:

```bash
brew tap 4fuu/net-mpd https://github.com/4fuu/net-mpd
brew install 4fuu/net-mpd/net-mpd
```

The formula installs the matching Intel or ARM64 release binary. Each release
automatically updates [`Formula/net-mpd.rb`](Formula/net-mpd.rb) with the new
calendar version and archive checksums.

## Run

Download the archive for your platform from
[GitHub Releases](https://github.com/4fuu/net-mpd/releases/latest). Windows
users only need to extract `net-mpd.exe`.

Log in before starting the server:

```powershell
.\net-mpd.exe login
```

Scan the terminal QR code with the NetEase Cloud Music mobile app and confirm
the login. net-mpd saves the resulting cookie in its own application directory.
It does not require Musicfox to be installed.

Then start the server:

```powershell
.\net-mpd.exe
```

To build from source:

```powershell
go build ./cmd/net-mpd
```

The server listens on `127.0.0.1:6600`. Override its settings when needed:

```powershell
.\net-mpd.exe -listen 127.0.0.1:16600 `
  -cookie C:\path\to\net-mpd\cookie `
  -password "your MPD password"
```

The MPD password defaults to `NET_MPD_PASSWORD`; an empty value disables MPD
authentication. Configure RMPC's `password` field when authentication is on.
Song stickers persist in `stickers.json` beside the cookie by default (override
with `-stickers`).

## Login and cookie management

```powershell
# QR-code login
.\net-mpd.exe login

# Inspect or refresh the current login
.\net-mpd.exe status
.\net-mpd.exe refresh

# Show where net-mpd stores its cookie
.\net-mpd.exe cookie-path

# Revoke the remote session and remove the local cookie
.\net-mpd.exe logout
```

The default cookie locations are:

- Windows: `%AppData%\net-mpd\cookie`
- Linux: `$XDG_CONFIG_HOME/net-mpd/cookie` or `~/.config/net-mpd/cookie`
- macOS: `~/Library/Application Support/net-mpd/cookie`

Set `NET_MPD_HOME` to move net-mpd's application directory, or use `-cookie`
with both the authentication command and server invocation.

Existing persistent jars, including a Musicfox jar, can be explicitly imported
once. This is optional and is not part of the normal login flow:

```powershell
.\net-mpd.exe import-cookie C:\path\to\existing\cookie
```

Cookie files are created with owner-only permissions where the operating system
supports them. Cookie values and temporary playback URLs are never logged.

## RMPC

Point RMPC at the server in its `config.ron`:

```ron
(
    address: "127.0.0.1:6600",
)
```

Then start `rmpc`. User playlists appear both in the directories pane and the
stored-playlists pane. Songs use stable `netease://song/<id>` MPD URIs.

## Supported features

- Playlist and directory browsing, artist/album tag views, and cloud search
- Queue add/load/delete/move/swap/shuffle and command lists
- Play, pause, stop, seek, next/previous, volume and playback options
- Native in-process playback controls without restarting the audio stream
- Library refresh, complete-library listing, and NetEase playlist editing
- MPD idle notifications and chunked `albumart`/`readpicture` cover delivery
- MPD password authentication, persistent local song stickers, and one
  toggleable native audio output

This is intentionally not a complete MPD implementation. Playlist track
reordering, partitions, mounts, and client-to-client messaging are not yet
supported.

## Test

```powershell
go test ./...

# Optional live test against an existing Musicfox login
$env:MUSICFOX_COOKIE_FILE = "C:\path\to\go-musicfox\data\cookie"
go test -run TestMusicfoxSession -v ./internal/ncm
```

## License

The original net-mpd source is available under the [MIT License](LICENSE).
Release binaries link to GPL-3.0-licensed UnblockNeteaseMusic code through the
go-musicfox SDK, so the combined binaries are distributed under GPL-3.0 terms.
Every release includes the applicable license texts, third-party notices, and a
Corresponding Source archive with vendored dependency sources. See
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
