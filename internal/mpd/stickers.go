package mpd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type StickerStore struct {
	mu   sync.RWMutex
	path string
	data map[string]map[string]string
}

func OpenStickerStore(path string) (*StickerStore, error) {
	s := &StickerStore{path: path, data: make(map[string]map[string]string)}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) || (err == nil && len(b) == 0) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("read sticker store: %w", err)
	}
	if s.data == nil {
		s.data = make(map[string]map[string]string)
	}
	return s, nil
}

func (s *StickerStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".stickers-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = replaceFile(tmp, s.path); err != nil {
		return err
	}
	return nil
}
func (s *StickerStore) Get(uri, key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[uri][key]
	return v, ok
}
func (s *StickerStore) List(uri string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]string{}
	for k, v := range s.data[uri] {
		out[k] = v
	}
	return out
}
func (s *StickerStore) Set(uri, key, value string) error {
	if key == "" {
		return fmt.Errorf("sticker key is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[uri] == nil {
		s.data[uri] = map[string]string{}
	}
	old, had := s.data[uri][key]
	s.data[uri][key] = value
	if e := s.persistLocked(); e != nil {
		if had {
			s.data[uri][key] = old
		} else {
			delete(s.data[uri], key)
			if len(s.data[uri]) == 0 {
				delete(s.data, uri)
			}
		}
		return e
	}
	return nil
}
func (s *StickerStore) Delete(uri string, key *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.data[uri]
	if key == nil {
		delete(s.data, uri)
	} else if old != nil {
		copy := map[string]string{}
		for k, v := range old {
			copy[k] = v
		}
		delete(old, *key)
		if len(old) == 0 {
			delete(s.data, uri)
		}
		old = copy
	}
	if e := s.persistLocked(); e != nil {
		if old != nil {
			s.data[uri] = old
		}
		return e
	}
	return nil
}

func stickerMatch(value, op, want string) bool {
	vn, ve := strconv.ParseFloat(value, 64)
	wn, we := strconv.ParseFloat(want, 64)
	cmp := strings.Compare(value, want)
	if ve == nil && we == nil {
		if vn < wn {
			cmp = -1
		} else if vn > wn {
			cmp = 1
		} else {
			cmp = 0
		}
	}
	switch op {
	case "=":
		return cmp == 0
	case "!=":
		return cmp != 0
	case "<":
		return cmp < 0
	case ">":
		return cmp > 0
	case "<=":
		return cmp <= 0
	case ">=":
		return cmp >= 0
	case "contains":
		return strings.Contains(value, want)
	}
	return false
}
func sortedStickerKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *client) sticker(a []string) ([]byte, bool, int, error) {
	if c.s.Stickers == nil {
		return nil, false, 50, fmt.Errorf("sticker store unavailable")
	}
	action, e := arg(a, 1)
	if e != nil {
		return nil, false, 2, e
	}
	typ, e := arg(a, 2)
	if e != nil {
		return nil, false, 2, e
	}
	if typ != "song" {
		return nil, false, 2, fmt.Errorf("unsupported sticker type")
	}
	if action == "find" {
		base, e := arg(a, 3)
		if e != nil {
			return nil, false, 2, e
		}
		key, e := arg(a, 4)
		if e != nil {
			return nil, false, 2, e
		}
		if key == "" {
			return nil, false, 2, fmt.Errorf("sticker key is empty")
		}
		op, want := "", ""
		if len(a) > 5 {
			if len(a) != 7 {
				return nil, false, 2, fmt.Errorf("invalid sticker find arguments")
			}
			op, want = a[5], a[6]
			if !map[string]bool{"=": true, "!=": true, "<": true, ">": true, "<=": true, ">=": true, "contains": true}[op] {
				return nil, false, 2, fmt.Errorf("invalid sticker operator")
			}
		}
		ss, e := c.s.Catalog.AllSongs()
		if e != nil {
			return nil, false, 50, e
		}
		var b strings.Builder
		for _, song := range ss {
			uri := SongURI(song.ID)
			if base != "" && !strings.HasPrefix(uri, base) {
				continue
			}
			v, ok := c.s.Stickers.Get(uri, key)
			if ok && (op == "" || stickerMatch(v, op, want)) {
				fmt.Fprintf(&b, "file: %s\nsticker: %s=%s\n", uri, key, v)
			}
		}
		return []byte(b.String()), false, 0, nil
	}
	uri, e := arg(a, 3)
	if e != nil {
		return nil, false, 2, e
	}
	if _, ok := c.s.Catalog.Song(uri); !ok {
		if _, e = c.s.Catalog.AllSongs(); e != nil {
			return nil, false, 50, e
		}
		if _, ok = c.s.Catalog.Song(uri); !ok {
			return nil, false, 50, fmt.Errorf("song not found")
		}
	}
	var b strings.Builder
	switch action {
	case "get":
		key, e := arg(a, 4)
		if e != nil {
			return nil, false, 2, e
		}
		if len(a) != 5 || key == "" {
			return nil, false, 2, fmt.Errorf("invalid sticker get arguments")
		}
		v, ok := c.s.Stickers.Get(uri, key)
		if !ok {
			return nil, false, 50, fmt.Errorf("no such sticker")
		}
		fmt.Fprintf(&b, "sticker: %s=%s\n", key, v)
	case "list":
		if len(a) != 4 {
			return nil, false, 2, fmt.Errorf("invalid sticker list arguments")
		}
		m := c.s.Stickers.List(uri)
		for _, k := range sortedStickerKeys(m) {
			fmt.Fprintf(&b, "sticker: %s=%s\n", k, m[k])
		}
	case "set":
		key, e := arg(a, 4)
		if e != nil {
			return nil, false, 2, e
		}
		value, e := arg(a, 5)
		if e != nil {
			return nil, false, 2, e
		}
		if len(a) != 6 {
			return nil, false, 2, fmt.Errorf("invalid sticker set arguments")
		}
		if key == "" {
			return nil, false, 2, fmt.Errorf("sticker key is empty")
		}
		if e = c.s.Stickers.Set(uri, key, value); e != nil {
			return nil, false, 50, e
		}
		c.s.State.Notify("sticker")
	case "delete":
		var key *string
		if len(a) > 4 {
			k := a[4]
			if k == "" {
				return nil, false, 2, fmt.Errorf("sticker key is empty")
			}
			key = &k
		}
		if len(a) > 5 {
			return nil, false, 2, fmt.Errorf("invalid sticker delete arguments")
		}
		if e = c.s.Stickers.Delete(uri, key); e != nil {
			return nil, false, 50, e
		}
		c.s.State.Notify("sticker")
	default:
		return nil, false, 2, fmt.Errorf("invalid sticker action")
	}
	return []byte(b.String()), false, 0, nil
}
