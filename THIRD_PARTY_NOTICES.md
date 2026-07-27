# Third-party notices

net-mpd uses third-party Go modules. Release archives include the license text
for every runtime module under `licenses/`.

In particular, `github.com/go-musicfox/netease-music` imports and links
`github.com/cnsilvan/UnblockNeteaseMusic`, which is licensed under the GNU
General Public License version 3. The resulting release executables are
therefore distributed subject to GPL-3.0. The project's original source remains
available under its MIT license, which is GPL-compatible.

Each GitHub Release also contains a `net-mpd-<version>-source` archive. It holds
the exact net-mpd release source and the vendored source of all Go module
dependencies needed to build the corresponding executables.
