# net-mpd

[![CI](https://github.com/4fuu/net-mpd/actions/workflows/ci.yml/badge.svg)](https://github.com/4fuu/net-mpd/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/4fuu/net-mpd)](https://github.com/4fuu/net-mpd/releases/latest)

`net-mpd` is an MPD 0.23.5-compatible server that exposes a logged-in
go-musicfox/NetEase Cloud Music account to MPD clients such as RMPC. It uses
go-musicfox's existing cookie jar and resolves temporary playback URLs only
when a song starts.

## Requirements

- A working, logged-in [go-musicfox](https://github.com/go-musicfox/go-musicfox)
- `ffplay` available on `PATH` (or supplied with `-ffplay`)
- Go 1.26 or newer when building from source

## Run

Download the archive for your platform from
[GitHub Releases](https://github.com/4fuu/net-mpd/releases/latest). Windows
users should extract `net-mpd.exe`, install an FFmpeg build that provides
`ffplay.exe`, and ensure it is on `PATH`.

To build from source instead:

```powershell
go build ./cmd/net-mpd
.\net-mpd.exe
```

The server listens on `127.0.0.1:6600`. It auto-detects standard and Scoop
go-musicfox cookie locations. Override its settings when needed:

```powershell
.\net-mpd.exe -listen 127.0.0.1:16600 `
  -cookie C:\path\to\go-musicfox\data\cookie `
  -ffplay C:\path\to\ffplay.exe
```

The cookie is loaded in place. It is not copied into this repository or logged.

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
- MPD idle notifications and chunked `albumart`/`readpicture` cover delivery

This is intentionally not a complete MPD implementation. Stored-playlist
editing, database updates, outputs/partitions, stickers, mounts, and
client-to-client messaging are not supported. Changing volume restarts the
current `ffplay` stream at the current playback position.

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
