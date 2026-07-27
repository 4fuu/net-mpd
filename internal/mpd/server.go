package mpd

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/4fuu/net-mpd/internal/ncm"
)

type Server struct {
	Catalog  *Catalog
	State    *State
	started  time.Time
	job      atomic.Uint64
	password string
	Stickers *StickerStore
}

func NewServer(c *Catalog, s *State) *Server {
	return &Server{Catalog: c, State: s, started: time.Now()}
}

// SetPassword configures authentication. It must be called before Serve.
func (s *Server) SetPassword(password string) { s.password = password }
func (s *Server) SetStickerPath(path string) error {
	st, e := OpenStickerStore(path)
	if e == nil {
		s.Stickers = st
	}
	return e
}
func (s *Server) Serve(l net.Listener) error {
	for {
		c, e := l.Accept()
		if e != nil {
			return e
		}
		go s.handle(c)
	}
}

type request struct {
	line string
	err  error
}
type client struct {
	s             *Server
	c             net.Conn
	lines         chan request
	events        *eventSubscription
	limit         int
	list          []string
	listOK        bool
	authenticated bool
}

func (s *Server) handle(conn net.Conn) {
	events, cancel := s.State.Subscribe()
	cl := &client{s: s, c: conn, lines: make(chan request, 8), events: events, limit: 8192, authenticated: s.password == ""}
	defer cancel()
	defer conn.Close()
	_, _ = io.WriteString(conn, "OK MPD 0.23.5\n")
	go func() {
		defer close(cl.lines)
		r := bufio.NewReader(conn)
		for {
			line, e := r.ReadString('\n')
			cl.lines <- request{strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), e}
			if e != nil {
				return
			}
		}
	}()
	for req := range cl.lines {
		if req.err != nil {
			return
		}
		if !cl.process(req.line) {
			return
		}
	}
}
func (c *client) write(b []byte) { _, _ = c.c.Write(b) }
func ack(code, idx int, cmd string, e error) []byte {
	return []byte(fmt.Sprintf("ACK [%d@%d] {%s} %s\n", code, idx, cmd, e.Error()))
}
func (c *client) process(line string) bool {
	a, e := Lex(line)
	if e != nil {
		c.write(ack(2, 0, "", e))
		return true
	}
	if len(a) == 0 {
		return true
	}
	cmd := strings.ToLower(a[0])
	if !c.authenticated && cmd != "password" && cmd != "close" {
		c.write(ack(4, 0, a[0], fmt.Errorf("permission denied")))
		return true
	}
	if cmd == "command_list_begin" || cmd == "command_list_ok_begin" {
		c.list = []string{}
		c.listOK = cmd == "command_list_ok_begin"
		return true
	}
	if cmd == "command_list_end" {
		for i, l := range c.list {
			aa, er := Lex(l)
			if er != nil {
				c.write(ack(2, i, "", er))
				c.list = nil
				return true
			}
			b, close, code, er := c.exec(aa)
			if er != nil {
				c.write(ack(code, i, aa[0], er))
				c.list = nil
				return true
			}
			c.write(b)
			if c.listOK {
				c.write([]byte("list_OK\n"))
			}
			if close {
				return false
			}
		}
		c.write([]byte("OK\n"))
		c.list = nil
		return true
	}
	if c.list != nil {
		c.list = append(c.list, line)
		return true
	}
	b, close, code, er := c.exec(a)
	if er != nil {
		c.write(ack(code, 0, a[0], er))
	} else {
		c.write(b)
		c.write([]byte("OK\n"))
	}
	return !close
}

var supported = []string{"password", "commands", "notcommands", "tagtypes", "urlhandlers", "decoders", "clearerror", "outputs", "enableoutput", "disableoutput", "toggleoutput", "sticker", "ping", "close", "binarylimit", "status", "stats", "update", "rescan", "listall", "listallinfo", "currentsong", "playlistinfo", "playlistid", "idle", "noidle", "lsinfo", "listplaylists", "listplaylist", "listplaylistinfo", "save", "rename", "rm", "playlistadd", "playlistdelete", "playlistclear", "list", "find", "search", "add", "load", "clear", "delete", "deleteid", "move", "moveid", "swap", "swapid", "shuffle", "play", "playid", "pause", "stop", "next", "previous", "seekcur", "setvol", "getvol", "volume", "repeat", "random", "single", "consume", "command_list_begin", "command_list_ok_begin", "command_list_end", "albumart", "readpicture", "findadd", "searchadd"}

func arg(a []string, n int) (string, error) {
	if len(a) <= n {
		return "", fmt.Errorf("missing argument")
	}
	return a[n], nil
}
func atoi(a []string, n int) (int, error) {
	v, e := arg(a, n)
	if e != nil {
		return 0, e
	}
	i, e := strconv.Atoi(v)
	if e != nil {
		return 0, fmt.Errorf("invalid integer")
	}
	return i, nil
}
func boolarg(a []string, n int) (bool, error) { i, e := atoi(a, n); return i != 0, e }
func songLines(s ncm.Song, id *int64) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "file: %s\n", SongURI(s.ID))
	if id != nil {
		fmt.Fprintf(&b, "Id: %d\n", *id)
	}
	fmt.Fprintf(&b, "Title: %s\n", s.Title)
	for _, x := range s.Artists {
		fmt.Fprintf(&b, "Artist: %s\n", x)
	}
	fmt.Fprintf(&b, "Album: %s\nduration: %.3f\n", s.Album, s.Duration.Seconds())
	return b.Bytes()
}
func (c *client) exec(a []string) ([]byte, bool, int, error) {
	cmd := strings.ToLower(a[0])
	st := c.s.State.Snapshot()
	var b bytes.Buffer
	switch cmd {
	case "password":
		if len(a) != 2 {
			return nil, false, 2, fmt.Errorf("invalid password arguments")
		}
		v, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		if subtle.ConstantTimeCompare([]byte(v), []byte(c.s.password)) != 1 {
			return nil, false, 3, fmt.Errorf("incorrect password")
		}
		c.authenticated = true
	case "commands":
		for _, x := range supported {
			fmt.Fprintf(&b, "command: %s\n", x)
		}
	case "notcommands":
	case "tagtypes":
		if len(a) != 1 {
			return nil, false, 2, fmt.Errorf("tagtypes operations are unsupported")
		}
		for _, x := range []string{"Artist", "Album", "Title"} {
			fmt.Fprintf(&b, "tagtype: %s\n", x)
		}
	case "urlhandlers":
		if len(a) != 1 {
			return nil, false, 2, fmt.Errorf("invalid urlhandlers arguments")
		}
		for _, x := range []string{"http", "https", "netease"} {
			fmt.Fprintf(&b, "handler: %s\n", x)
		}
	case "decoders":
		if len(a) != 1 {
			return nil, false, 2, fmt.Errorf("invalid decoders arguments")
		}
		for _, x := range []string{"mp3", "flac", "ogg", "wav"} {
			fmt.Fprintf(&b, "plugin: native\nsuffix: %s\n", x)
		}
	case "clearerror":
		if len(a) != 1 {
			return nil, false, 2, fmt.Errorf("invalid clearerror arguments")
		}
		if c.s.State.ClearError() {
			c.s.State.Notify("player")
		}
	case "outputs":
		if len(a) != 1 {
			return nil, false, 2, fmt.Errorf("invalid outputs arguments")
		}
		enabled := c.s.State.OutputEnabled()
		fmt.Fprintf(&b, "outputid: 0\noutputname: Native audio\nplugin: native\noutputenabled: %d\n", bt(enabled))
	case "enableoutput", "disableoutput", "toggleoutput":
		if len(a) != 2 {
			return nil, false, 2, fmt.Errorf("invalid output arguments")
		}
		id, e := atoi(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		if id != 0 {
			return nil, false, 50, fmt.Errorf("no such audio output")
		}
		old := c.s.State.OutputEnabled()
		enabled := cmd == "enableoutput" || (cmd == "toggleoutput" && !old)
		c.s.State.SetOutputEnabled(enabled)
		if old != enabled {
			c.s.State.Notify("output")
		}
	case "sticker":
		return c.sticker(a)
	case "ping":
	case "close":
		return nil, true, 0, nil
	case "binarylimit":
		v, e := atoi(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		if v < 1 {
			return nil, false, 2, fmt.Errorf("invalid binary limit")
		}
		c.limit = v
	case "status":
		fmt.Fprintf(&b, "partition: default\nvolume: %d\nrepeat: %d\nrandom: %d\nsingle: %d\nconsume: %d\nplaylist: %d\nplaylistlength: %d\nstate: %s\n", st.Volume, bt(st.Repeat), bt(st.Random), bt(st.Single), bt(st.Consume), st.Version, len(st.Queue), st.State)
		if st.Current >= 0 && st.Current < len(st.Queue) {
			q := st.Queue[st.Current]
			fmt.Fprintf(&b, "song: %d\nsongid: %d\nelapsed: %.3f\nduration: %.3f\n", st.Current, q.ID, st.Elapsed.Seconds(), q.Song.Duration.Seconds())
		}
		if st.Error != "" {
			fmt.Fprintf(&b, "error: %s\n", st.Error)
		}
	case "stats":
		all, e := c.s.Catalog.AllSongs()
		if e != nil {
			return nil, false, 50, e
		}
		var total time.Duration
		for _, s := range all {
			total += s.Duration
		}
		fmt.Fprintf(&b, "artists: %d\nalbums: %d\nsongs: %d\nuptime: %d\nplaytime: 0\ndb_playtime: %d\ndb_update: %d\n", count(all, "artist"), count(all, "album"), len(all), int64(time.Since(c.s.started).Seconds()), int64(total.Seconds()), c.s.Catalog.LastRefresh().Unix())
	case "update", "rescan":
		scope := ""
		if len(a) > 1 {
			scope = a[1]
		}
		if e := c.s.Catalog.Refresh(scope); e != nil {
			return nil, false, 50, e
		}
		job := c.s.job.Add(1)
		fmt.Fprintf(&b, "updating_db: %d\n", job)
		c.s.State.Notify("update", "database", "stored_playlist")
	case "listall", "listallinfo":
		var ss []ncm.Song
		var e error
		if len(a) > 1 && a[1] != "" {
			ss, e = c.s.Catalog.PlaylistSongs(a[1])
		} else {
			ss, e = c.s.Catalog.AllSongs()
		}
		if e != nil {
			return nil, false, 50, e
		}
		for _, s := range ss {
			if cmd == "listallinfo" {
				b.Write(songLines(s, nil))
			} else {
				fmt.Fprintf(&b, "file: %s\n", SongURI(s.ID))
			}
		}
	case "currentsong":
		if st.Current >= 0 && st.Current < len(st.Queue) {
			q := st.Queue[st.Current]
			b.Write(songLines(q.Song, &q.ID))
			fmt.Fprintf(&b, "Pos: %d\n", st.Current)
		}
	case "playlistinfo", "playlistid":
		items := st.Queue
		if cmd == "playlistid" && len(a) > 1 {
			id, _ := strconv.ParseInt(a[1], 10, 64)
			items = nil
			for _, q := range st.Queue {
				if q.ID == id {
					items = append(items, q)
				}
			}
		}
		start, end := parseRange(a, len(items))
		for i, q := range items {
			if i >= start && i < end {
				b.Write(songLines(q.Song, &q.ID))
				fmt.Fprintf(&b, "Pos: %d\n", i)
			}
		}
	case "idle":
		data, closed := c.idle(a)
		return data, closed, 0, nil
	case "noidle":
	case "listplaylists":
		for _, p := range c.s.Catalog.Playlists() {
			fmt.Fprintf(&b, "playlist: %s\nLast-Modified: 1970-01-01T00:00:00Z\n", p.Name)
		}
	case "lsinfo":
		if len(a) == 1 || a[1] == "" {
			for _, p := range c.s.Catalog.Playlists() {
				fmt.Fprintf(&b, "playlist: %s\nLast-Modified: 1970-01-01T00:00:00Z\n", p.Name)
			}
		} else {
			ss, e := c.s.Catalog.PlaylistSongs(a[1])
			if e != nil {
				return nil, false, 50, e
			}
			for _, s := range ss {
				b.Write(songLines(s, nil))
			}
		}
	case "listplaylist", "listplaylistinfo":
		name, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		ss, e := c.s.Catalog.PlaylistSongs(name)
		if e != nil {
			return nil, false, 50, e
		}
		for _, s := range ss {
			if cmd == "listplaylistinfo" {
				b.Write(songLines(s, nil))
			} else {
				fmt.Fprintf(&b, "file: %s\n", SongURI(s.ID))
			}
		}
	case "save":
		name, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		if e = c.s.Catalog.CreatePlaylist(name, st.Queue); e != nil {
			return nil, false, 50, e
		}
		c.s.State.Notify("stored_playlist", "database")
	case "rename":
		old, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		name, e := arg(a, 2)
		if e != nil {
			return nil, false, 2, e
		}
		if e = c.s.Catalog.RenamePlaylist(old, name); e != nil {
			return nil, false, 50, e
		}
		c.s.State.Notify("stored_playlist", "database")
	case "rm":
		name, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		if e = c.s.Catalog.DeletePlaylist(name); e != nil {
			return nil, false, 50, e
		}
		c.s.State.Notify("stored_playlist", "database")
	case "playlistadd":
		name, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		uri, e := arg(a, 2)
		if e != nil {
			return nil, false, 2, e
		}
		song, ok := c.s.Catalog.Song(uri)
		if !ok {
			return nil, false, 50, fmt.Errorf("song not found")
		}
		if e = c.s.Catalog.AddPlaylistSong(name, song); e != nil {
			return nil, false, 50, e
		}
		c.s.State.Notify("stored_playlist", "database")
	case "playlistdelete":
		name, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		pos, e := atoi(a, 2)
		if e != nil {
			return nil, false, 2, e
		}
		if e = c.s.Catalog.DeletePlaylistSong(name, pos); e != nil {
			return nil, false, 50, e
		}
		c.s.State.Notify("stored_playlist", "database")
	case "playlistclear":
		name, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		if e = c.s.Catalog.ClearPlaylist(name); e != nil {
			return nil, false, 50, e
		}
		c.s.State.Notify("stored_playlist", "database")
	case "load":
		name, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		ss, e := c.s.Catalog.PlaylistSongs(name)
		if e != nil {
			return nil, false, 50, e
		}
		start, end := 0, len(ss)
		if len(a) > 2 {
			start, end = parseRange([]string{"", a[2]}, len(ss))
			if start < 0 || end < start || end > len(ss) {
				return nil, false, 2, fmt.Errorf("invalid range")
			}
		}
		pos := -1
		if len(a) > 3 {
			pos, e = queuePosition(a[3], st)
			if e != nil {
				return nil, false, 2, e
			}
		}
		c.s.State.InsertBlock(ss[start:end], pos)
	case "add":
		u, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		song, ok := c.s.Catalog.Song(u)
		if !ok {
			return nil, false, 50, fmt.Errorf("song not found")
		}
		pos := -1
		if len(a) > 2 {
			pos, e = queuePosition(a[2], st)
			if e != nil {
				return nil, false, 2, fmt.Errorf("invalid position")
			}
		}
		c.s.State.Insert(song, pos)
	case "clear":
		c.s.State.Clear()
	case "delete", "deleteid":
		v, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		if cmd == "delete" {
			start, end, rangeErr := commandRange(v, len(st.Queue))
			if rangeErr != nil {
				return nil, false, 2, rangeErr
			}
			e = c.s.State.DeleteRange(start, end)
		} else {
			id, parseErr := strconv.ParseInt(v, 10, 64)
			if parseErr != nil {
				return nil, false, 2, fmt.Errorf("invalid song id")
			}
			e = c.s.State.DeleteID(id)
		}
		if e != nil {
			return nil, false, 50, e
		}
	case "move", "moveid":
		source, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		dest, e := arg(a, 2)
		if e != nil {
			return nil, false, 2, e
		}
		if cmd == "moveid" {
			id, parseErr := strconv.ParseInt(source, 10, 64)
			if parseErr != nil {
				return nil, false, 2, fmt.Errorf("invalid song id")
			}
			from := findID(st.Queue, id)
			to, positionErr := movePosition(dest, st, from, from+1)
			if positionErr != nil {
				return nil, false, 2, positionErr
			}
			e = c.s.State.Move(from, to)
		} else {
			start, end, rangeErr := commandRange(source, len(st.Queue))
			if rangeErr != nil {
				return nil, false, 2, rangeErr
			}
			to, positionErr := movePosition(dest, st, start, end)
			if positionErr != nil {
				return nil, false, 2, positionErr
			}
			e = c.s.State.MoveRange(start, end, to)
		}
		if e != nil {
			return nil, false, 50, e
		}
	case "swap", "swapid":
		x, e := atoi(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		y, e := atoi(a, 2)
		if e != nil {
			return nil, false, 2, e
		}
		if cmd == "swapid" {
			x = findID(st.Queue, int64(x))
			y = findID(st.Queue, int64(y))
		}
		e = c.s.State.Swap(x, y)
		if e != nil {
			return nil, false, 50, e
		}
	case "shuffle":
		start, end := parseRange(a, len(st.Queue))
		if e := c.s.State.ShuffleRange(start, end); e != nil {
			return nil, false, 50, e
		}
	case "play", "playid":
		p := -1
		if len(a) > 1 {
			p, _ = strconv.Atoi(a[1])
			if cmd == "playid" {
				p = findID(st.Queue, int64(p))
			}
		}
		if e := c.s.State.Play(p); e != nil {
			return nil, false, 50, e
		}
	case "pause":
		on := st.State == "play"
		var e error
		if len(a) > 1 {
			on, e = boolarg(a, 1)
		}
		if e != nil {
			return nil, false, 2, e
		}
		if e = c.s.State.Pause(on); e != nil {
			return nil, false, 50, e
		}
	case "stop":
		c.s.State.Stop()
	case "next":
		if e := c.s.State.Next(); e != nil {
			return nil, false, 50, e
		}
	case "previous":
		if e := c.s.State.Previous(); e != nil {
			return nil, false, 50, e
		}
	case "seekcur":
		v, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		sec, e := strconv.ParseFloat(v, 64)
		if e != nil {
			return nil, false, 2, fmt.Errorf("invalid time")
		}
		if strings.HasPrefix(v, "+") || strings.HasPrefix(v, "-") {
			sec += st.Elapsed.Seconds()
		}
		if sec < 0 {
			sec = 0
		}
		if e = c.s.State.Seek(time.Duration(sec * float64(time.Second))); e != nil {
			return nil, false, 50, e
		}
	case "setvol":
		v, e := atoi(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		if e = c.s.State.SetVolume(v); e != nil {
			return nil, false, 50, e
		}
	case "volume":
		v, e := atoi(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		if e = c.s.State.SetVolume(st.Volume + v); e != nil {
			return nil, false, 50, e
		}
	case "getvol":
		fmt.Fprintf(&b, "volume: %d\n", st.Volume)
	case "repeat", "random", "single", "consume":
		v, e := boolarg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		c.s.State.Options(cmd, v)
	case "albumart", "readpicture":
		u, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		off, e := atoi(a, 2)
		if e != nil {
			return nil, false, 2, e
		}
		data, e := c.s.Catalog.Cover(u)
		if e != nil {
			return nil, false, 50, fmt.Errorf("cover not found")
		}
		if off < 0 || off > len(data) {
			return nil, false, 50, fmt.Errorf("bad offset")
		}
		n := len(data) - off
		if n > c.limit {
			n = c.limit
		}
		fmt.Fprintf(&b, "size: %d\ntype: image/jpeg\nbinary: %d\n", len(data), n)
		b.Write(data[off : off+n])
		b.WriteByte('\n')
	case "find", "search", "findadd", "searchadd", "list":
		return c.query(a, cmd)
	default:
		return nil, false, 5, fmt.Errorf("unknown command")
	}
	return b.Bytes(), false, 0, nil
}
