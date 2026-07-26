package mpd

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/4fuuu/net-mpd/internal/ncm"
)

type Server struct {
	Catalog *Catalog
	State   *State
}

func NewServer(c *Catalog, s *State) *Server { return &Server{c, s} }
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
	s      *Server
	c      net.Conn
	lines  chan request
	limit  int
	list   []string
	listOK bool
}

func (s *Server) handle(conn net.Conn) {
	cl := &client{s: s, c: conn, lines: make(chan request, 8), limit: 8192}
	defer conn.Close()
	_, _ = io.WriteString(conn, "OK MPD 0.23.5\n")
	go func() {
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

var supported = []string{"commands", "ping", "close", "binarylimit", "status", "stats", "currentsong", "playlistinfo", "playlistid", "idle", "noidle", "lsinfo", "listplaylists", "listplaylistinfo", "list", "find", "search", "add", "load", "clear", "delete", "deleteid", "move", "moveid", "swap", "swapid", "shuffle", "play", "playid", "pause", "stop", "next", "previous", "seekcur", "setvol", "getvol", "repeat", "random", "single", "consume", "command_list_begin", "command_list_ok_begin", "command_list_end", "albumart", "readpicture", "findadd", "searchadd"}

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
	case "commands":
		for _, x := range supported {
			fmt.Fprintf(&b, "command: %s\n", x)
		}
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
	case "stats":
		all, e := c.s.Catalog.AllSongs()
		if e != nil {
			return nil, false, 50, e
		}
		fmt.Fprintf(&b, "artists: %d\nalbums: %d\nsongs: %d\nuptime: 0\nplaytime: 0\ndb_playtime: 0\ndb_update: %d\n", count(all, "artist"), count(all, "album"), len(all), time.Now().Unix())
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
		return c.idle(a), false, 0, nil
	case "noidle":
	case "listplaylists":
		for _, p := range c.s.Catalog.Playlists() {
			fmt.Fprintf(&b, "playlist: %s\nLast-Modified: 1970-01-01T00:00:00Z\n", p.Name)
		}
	case "lsinfo":
		if len(a) == 1 || a[1] == "" {
			for _, p := range c.s.Catalog.Playlists() {
				fmt.Fprintf(&b, "directory: %s\n", p.Name)
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
	case "listplaylistinfo":
		name, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		ss, e := c.s.Catalog.PlaylistSongs(name)
		if e != nil {
			return nil, false, 50, e
		}
		for _, s := range ss {
			b.Write(songLines(s, nil))
		}
	case "load":
		name, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		ss, e := c.s.Catalog.PlaylistSongs(name)
		if e != nil {
			return nil, false, 50, e
		}
		for _, s := range ss {
			c.s.State.Add(s)
		}
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
			pos, e = strconv.Atoi(a[2])
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
	case "move", "moveid", "swap", "swapid":
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
		} else if cmd == "moveid" {
			x = findID(st.Queue, int64(x))
		}
		if strings.HasPrefix(cmd, "move") {
			e = c.s.State.Move(x, y)
		} else {
			e = c.s.State.Swap(x, y)
		}
		if e != nil {
			return nil, false, 50, e
		}
	case "shuffle":
		c.s.State.Shuffle()
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
		c.s.State.SetVolume(v)
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
