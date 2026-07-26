# net-mpd

`net-mpd` is an MPD 0.23.5-compatible server that exposes a logged-in
go-musicfox/NetEase Cloud Music account to MPD clients such as RMPC. It uses
go-musicfox's existing cookie jar and resolves temporary playback URLs only
when a song starts.

## Requirements

- A working, logged-in [go-musicfox](https://github.com/go-musicfox/go-musicfox)
- `ffplay` available on `PATH` (or supplied with `-ffplay`)
- Go 1.26 or newer when building from source

## Run

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
client-to-client messaging are not supported. Volume changes take effect on the
next play, resume, or seek because playback is delegated to `ffplay`.

## Test

```powershell
go test ./...

# Optional live test against an existing Musicfox login
$env:MUSICFOX_COOKIE_FILE = "C:\path\to\go-musicfox\data\cookie"
go test -run TestMusicfoxSession -v ./internal/ncm
```
