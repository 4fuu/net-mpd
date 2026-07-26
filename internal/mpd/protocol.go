package mpd

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/4fuuu/net-mpd/internal/ncm"
)

// Lex parses MPD command arguments, preserving UTF-8 and supporting quoted escapes.
func Lex(line string) ([]string, error) {
	var out []string
	for i := 0; i < len(line); {
		for i < len(line) && unicode.IsSpace(rune(line[i])) {
			i++
		}
		if i == len(line) {
			break
		}
		var b strings.Builder
		quoted := false
		if line[i] == '"' {
			quoted = true
			i++
		}
		for i < len(line) {
			ch := line[i]
			if ch == '\\' {
				i++
				if i == len(line) {
					return nil, fmt.Errorf("trailing escape")
				}
				b.WriteByte(line[i])
				i++
				continue
			}
			if quoted {
				if ch == '"' {
					i++
					quoted = false
					break
				}
				b.WriteByte(ch)
				i++
			} else {
				if unicode.IsSpace(rune(ch)) {
					break
				}
				b.WriteByte(ch)
				i++
			}
		}
		if quoted {
			return nil, fmt.Errorf("unterminated quote")
		}
		out = append(out, b.String())
	}
	return out, nil
}
func bt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func findID(q []QueueItem, id int64) int {
	for i, x := range q {
		if x.ID == id {
			return i
		}
	}
	return -1
}
func parseRange(a []string, n int) (int, int) {
	if len(a) < 2 {
		return 0, n
	}
	v := a[1]
	if !strings.Contains(v, ":") {
		i, e := strconv.Atoi(v)
		if e == nil {
			return i, i + 1
		}
		return 0, n
	}
	p := strings.SplitN(v, ":", 2)
	x, _ := strconv.Atoi(p[0])
	y := n
	if p[1] != "" {
		y, _ = strconv.Atoi(p[1])
	}
	return x, y
}
func commandRange(v string, n int) (int, int, error) {
	if !strings.Contains(v, ":") {
		i, err := strconv.Atoi(v)
		return i, i + 1, err
	}
	p := strings.SplitN(v, ":", 2)
	start, err := strconv.Atoi(p[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid range")
	}
	end := n
	if p[1] != "" {
		end, err = strconv.Atoi(p[1])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid range")
		}
	}
	return start, end, nil
}
func count(ss []ncm.Song, tag string) int {
	m := map[string]bool{}
	for _, s := range ss {
		if tag == "album" {
			m[s.Album] = true
		} else {
			for _, a := range s.Artists {
				m[a] = true
			}
		}
	}
	return len(m)
}

func (c *client) idle(a []string) []byte {
	want := map[string]bool{}
	for _, x := range a[1:] {
		want[strings.ToLower(x)] = true
	}
	ch, cancel := c.s.State.Subscribe()
	defer cancel()
	for {
		select {
		case kind := <-ch:
			if len(want) == 0 || want[kind] {
				return []byte("changed: " + kind + "\n")
			}
		case req := <-c.lines:
			if req.err != nil {
				return nil
			}
			x, e := Lex(req.line)
			if e == nil && len(x) > 0 && strings.EqualFold(x[0], "noidle") {
				return nil
			}
		}
	}
}

type condition struct{ tag, op, value string }

func conditions(a []string) []condition {
	text := strings.Join(a, " ")
	text = strings.TrimSpace(strings.Trim(text, "()"))
	parts := strings.Split(text, " AND ")
	var out []condition
	for _, p := range parts {
		p = strings.TrimSpace(strings.Trim(p, "()"))
		f := strings.Fields(p)
		if len(f) >= 3 {
			v := strings.Join(f[2:], " ")
			v = strings.Trim(v, "'\"")
			out = append(out, condition{strings.ToLower(f[0]), strings.ToLower(f[1]), v})
		}
	}
	return out
}
func values(s ncm.Song, tag string) []string {
	switch strings.ToLower(tag) {
	case "artist", "albumartist":
		return s.Artists
	case "album":
		return []string{s.Album}
	case "title":
		return []string{s.Title}
	case "file":
		return []string{SongURI(s.ID)}
	case "any":
		return append([]string{s.Title, s.Album}, s.Artists...)
	}
	return nil
}
func matches(s ncm.Song, cs []condition) bool {
	for _, c := range cs {
		ok := false
		for _, v := range values(s, c.tag) {
			if matchText(v, c.op, c.value) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
func (c *client) query(a []string, cmd string) ([]byte, bool, int, error) {
	if cmd == "list" {
		tag, e := arg(a, 1)
		if e != nil {
			return nil, false, 2, e
		}
		ss, e := c.s.Catalog.AllSongs()
		if e != nil {
			return nil, false, 50, e
		}
		var filterArgs, groups []string
		for i := 2; i < len(a); i++ {
			if strings.EqualFold(a[i], "group") && i+1 < len(a) {
				groups = append(groups, a[i+1])
				i++
			} else {
				filterArgs = append(filterArgs, a[i])
			}
		}
		cs := conditions(filterArgs)
		seen := map[string]bool{}
		var b bytes.Buffer
		key := canonical(tag)
		for _, s := range ss {
			if matches(s, cs) {
				for _, v := range values(s, tag) {
					groupValues := make([]string, 0, len(groups))
					for _, group := range groups {
						vals := values(s, group)
						if len(vals) > 0 {
							groupValues = append(groupValues, vals[0])
						} else {
							groupValues = append(groupValues, "")
						}
					}
					dedupeKey := v + "\x00" + strings.Join(groupValues, "\x00")
					if !seen[dedupeKey] {
						seen[dedupeKey] = true
						fmt.Fprintf(&b, "%s: %s\n", key, v)
						for i, group := range groups {
							fmt.Fprintf(&b, "%s: %s\n", canonical(group), groupValues[i])
						}
					}
				}
			}
		}
		return b.Bytes(), false, 0, nil
	}
	cloud := strings.HasPrefix(cmd, "search")
	cs := conditions(a[1:])
	var ss []ncm.Song
	var e error
	if cloud {
		q := ""
		for _, x := range cs {
			if x.tag == "any" {
				q = x.value
				break
			}
		}
		if q != "" {
			ss, e = c.s.Catalog.Search(q, 100)
		} else {
			ss, e = c.s.Catalog.AllSongs()
		}
	} else {
		ss, e = c.s.Catalog.AllSongs()
	}
	if e != nil {
		return nil, false, 50, e
	}
	add := strings.HasSuffix(cmd, "add")
	var b bytes.Buffer
	for _, s := range ss {
		if matches(s, cs) {
			if add {
				c.s.State.Add(s)
			} else {
				b.Write(songLines(s, nil))
			}
		}
	}
	return b.Bytes(), false, 0, nil
}
func canonical(s string) string {
	switch strings.ToLower(s) {
	case "artist":
		return "Artist"
	case "album":
		return "Album"
	case "albumartist":
		return "AlbumArtist"
	case "title":
		return "Title"
	}
	return s
}
