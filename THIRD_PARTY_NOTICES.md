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

The platform-native playback backends include code adapted from
[go-musicfox](https://github.com/go-musicfox/go-musicfox), distributed under
the MIT License. The retained notice is included in the corresponding source
files and release source archive. Additional playback dependencies and their
license texts are included under `licenses/` in each binary release archive.

## go-musicfox

MIT License

Copyright (c) 2015 - present Microsoft Corporation

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
