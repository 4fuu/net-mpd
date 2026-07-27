# net-mpd

[![CI](https://github.com/4fuu/net-mpd/actions/workflows/ci.yml/badge.svg)](https://github.com/4fuu/net-mpd/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/4fuu/net-mpd)](https://github.com/4fuu/net-mpd/releases/latest)

`net-mpd` is an MPD 0.23.5-compatible server that exposes a NetEase Cloud Music
account to **[rmpc](https://github.com/mierak/rmpc)**. It is built and tested
around rmpc's library browser, queue, lyrics pane, and sticker usage. Other MPD
clients may work for basic playback, but rmpc is the intended front-end.

net-mpd manages its own login and persistent cookie jar, and resolves temporary
playback URLs only when a song starts.

## Requirements

- Go 1.26 or newer when building from source
- [rmpc](https://github.com/mierak/rmpc) as the MPD client

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

After logging in once with `net-mpd login`, run it at user login with Homebrew
Services:

```bash
brew services start net-mpd
```

## Run

Download the archive for your platform from
[GitHub Releases](https://github.com/4fuu/net-mpd/releases/latest), or build
from source:

```bash
go build -o net-mpd ./cmd/net-mpd
```

Log in before starting the server:

```bash
./net-mpd login
```

Scan the terminal QR code with the NetEase Cloud Music mobile app and confirm
the login. net-mpd saves the resulting cookie in its own application directory.
It does not require Musicfox to be installed.

Then start the server:

```bash
./net-mpd
```

The server listens on `127.0.0.1:6600`. Override its settings when needed:

```bash
./net-mpd -listen 127.0.0.1:16600 \
  -cookie /path/to/net-mpd/cookie \
  -password "your MPD password"
```

The MPD password defaults to `NET_MPD_PASSWORD`; an empty value disables MPD
authentication. Configure rmpc's `password` field when authentication is on.
Song stickers persist in `stickers.json` beside the cookie by default (override
with `-stickers`).

## Login and cookie management

```bash
# QR-code login
./net-mpd login

# Inspect or refresh the current login
./net-mpd status
./net-mpd refresh

# Show where net-mpd stores its cookie
./net-mpd cookie-path

# Revoke the remote session and remove the local cookie
./net-mpd logout
```

The default cookie locations are:

- Windows: `%AppData%\net-mpd\cookie`
- Linux: `$XDG_CONFIG_HOME/net-mpd/cookie` or `~/.config/net-mpd/cookie`
- macOS: `~/Library/Application Support/net-mpd/cookie`

Set `NET_MPD_HOME` to move net-mpd's application directory, or use `-cookie`
with both the authentication command and server invocation.

Existing persistent jars, including a Musicfox jar, can be explicitly imported
once. This is optional and is not part of the normal login flow:

```bash
./net-mpd import-cookie /path/to/existing/cookie
```

Cookie files are created with owner-only permissions where the operating system
supports them. Cookie values and temporary playback URLs are never logged.

## rmpc

Point rmpc at the server in its `config.ron`:

```ron
(
    address: "127.0.0.1:6600",
)
```

Then start `rmpc`. User playlists appear both in the directories pane and the
stored-playlists pane. Songs use stable `netease://song/<id>` MPD URIs.
Playlist names that contain `/` (common for date-style NetEase titles like
`25/05`) are exposed with a fullwidth slash `／` so MPD clients do not treat
them as nested paths.

### Lyrics

When a song starts, net-mpd fetches NetEase LRC lyrics into its lyrics cache
(default: beside the cookie, e.g.
`~/Library/Application Support/net-mpd/lyrics` on macOS). Point rmpc at that
directory:

```ron
(
    address: "127.0.0.1:6600",
    lyrics_dir: Some("/Users/YOU/Library/Application Support/net-mpd/lyrics"),
    enable_lyrics_index: true,
    enable_lyrics_hot_reload: true,
)
```

Override the cache with `-lyrics /path/to/dir` if needed. Restart rmpc (or
switch tracks) after the first play so the Lyrics pane picks up the new file.

## Supported features

- Playlist and directory browsing, artist/album tag views, and cloud search
- Queue add/load/delete/move/swap/shuffle and command lists
- Play, pause, stop, seek, next/previous, volume and playback options
- Native in-process playback controls without restarting the audio stream
- macOS Now Playing / Control Center / headset media keys
- Library refresh, complete-library listing, and NetEase playlist editing
- MPD idle notifications and chunked `albumart`/`readpicture` cover delivery
- MPD password authentication, persistent local song stickers, and one
  toggleable native audio output
- NetEase LRC lyrics written for rmpc's Lyrics pane

This is intentionally not a complete MPD implementation. Tag views are built
from songs already present in your NetEase playlists; the first `list`/`find`
after startup may take a moment while the library cache warms up.

## TODO

Planned or incomplete work (not a commitment to ship order):

- [ ] **Windows SMTC** — System Media Transport Controls (taskbar / media keys /
  flyout), similar to go-musicfox. macOS Now Playing is done; Windows is stubbed
  with a `TODO(windows)` in `internal/sysmedia/sysmedia_other.go`.
- [ ] **Playlist track reordering** — MPD `playlistmove` and related stored-playlist
  edit commands.
- [ ] **Partitions / mounts** — multi-partition and mount APIs (not needed for rmpc
  NetEase use).
- [ ] **Client-to-client messaging** — MPD `channels` / `subscribe` / `message`.
- [ ] **Broader MPD client support** — protocol gaps beyond what rmpc uses day to
  day (e.g. extra tags, `listfiles`, fuller `stats`).
- [ ] **Faster cold start for tag browsers** — smarter library warm-up / indexing so
  first `list Artist` / `list Album` is snappier on large accounts.
- [ ] **Playback URL robustness** — more CDN / quality fallbacks when a resolved
  URL is blocked by network or proxy rules.
- [ ] **Optional rmpcd integration docs** — stickers, playcount, and lyrics plugins
  when running rmpc + rmpcd against net-mpd.

## Test

```bash
go test ./...

# Optional live test against an existing Musicfox login
export MUSICFOX_COOKIE_FILE=/path/to/go-musicfox/data/cookie
go test -run TestMusicfoxSession -v ./internal/ncm
```

## Acknowledgements

net-mpd builds on work from the [go-musicfox](https://github.com/go-musicfox/go-musicfox)
project and its NetEase Cloud Music SDK
([netease-music](https://github.com/go-musicfox/netease-music)). Playback backends,
session/cookie handling patterns, and macOS media-control ideas draw heavily from
musicfox. Thanks to the musicfox authors and contributors.

## License

The original net-mpd source is available under the [MIT License](LICENSE).
Release binaries link to GPL-3.0-licensed UnblockNeteaseMusic code through the
go-musicfox SDK, so the combined binaries are distributed under GPL-3.0 terms.
Every release includes the applicable license texts, third-party notices, and a
Corresponding Source archive with vendored dependency sources. See
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
