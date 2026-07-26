package mpd

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/4fuuu/net-mpd/internal/ncm"
)

type MusicService interface {
	Account() (ncm.User, error)
	UserPlaylists(int64) ([]ncm.Playlist, error)
	PlaylistTracks(int64) ([]ncm.Song, error)
	SearchSongs(string, int) ([]ncm.Song, error)
	ResolveURL(int64) (ncm.PlayableInfo, error)
	Cover(ncm.Song) ([]byte, error)
}

type Catalog struct {
	service    MusicService
	mu         sync.RWMutex
	user       ncm.User
	playlists  []ncm.Playlist
	tracks     map[int64][]ncm.Song
	byURI      map[string]ncm.Song
	covers     map[string][]byte
	coverOrder []string
}

func NewCatalog(service MusicService) (*Catalog, error) {
	u, err := service.Account()
	if err != nil {
		return nil, err
	}
	ps, err := service.UserPlaylists(u.ID)
	if err != nil {
		return nil, err
	}
	return &Catalog{service: service, user: u, playlists: append([]ncm.Playlist(nil), ps...), tracks: make(map[int64][]ncm.Song), byURI: make(map[string]ncm.Song), covers: make(map[string][]byte)}, nil
}
func SongURI(id int64) string     { return "netease://song/" + strconv.FormatInt(id, 10) }
func (c *Catalog) User() ncm.User { c.mu.RLock(); defer c.mu.RUnlock(); return c.user }
func (c *Catalog) Playlists() []ncm.Playlist {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]ncm.Playlist(nil), c.playlists...)
}
func (c *Catalog) Playlist(name string) (ncm.Playlist, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range c.playlists {
		if p.Name == name || strconv.FormatInt(p.ID, 10) == name {
			return p, true
		}
	}
	return ncm.Playlist{}, false
}
func (c *Catalog) PlaylistSongs(name string) ([]ncm.Song, error) {
	p, ok := c.Playlist(name)
	if !ok {
		return nil, fmt.Errorf("playlist not found")
	}
	c.mu.RLock()
	songs, ok := c.tracks[p.ID]
	c.mu.RUnlock()
	if !ok {
		loaded, err := c.service.PlaylistTracks(p.ID)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		if songs, ok = c.tracks[p.ID]; !ok {
			songs = append([]ncm.Song(nil), loaded...)
			c.tracks[p.ID] = songs
			for _, s := range songs {
				c.byURI[SongURI(s.ID)] = s
			}
		}
		c.mu.Unlock()
	}
	return append([]ncm.Song(nil), songs...), nil
}
func (c *Catalog) AllSongs() ([]ncm.Song, error) {
	ps := c.Playlists()
	seen := map[int64]bool{}
	var out []ncm.Song
	for _, p := range ps {
		ss, e := c.PlaylistSongs(strconv.FormatInt(p.ID, 10))
		if e != nil {
			return nil, e
		}
		for _, s := range ss {
			if !seen[s.ID] {
				seen[s.ID] = true
				out = append(out, s)
			}
		}
	}
	return out, nil
}
func (c *Catalog) Song(uri string) (ncm.Song, bool) {
	c.mu.RLock()
	s, ok := c.byURI[uri]
	c.mu.RUnlock()
	return s, ok
}
func (c *Catalog) Search(q string, limit int) ([]ncm.Song, error) {
	ss, e := c.service.SearchSongs(q, limit)
	if e == nil {
		c.mu.Lock()
		for _, s := range ss {
			c.byURI[SongURI(s.ID)] = s
		}
		c.mu.Unlock()
	}
	return ss, e
}
func (c *Catalog) Resolve(s ncm.Song) (ncm.PlayableInfo, error) { return c.service.ResolveURL(s.ID) }
func (c *Catalog) Cover(uri string) ([]byte, error) {
	c.mu.RLock()
	b, ok := c.covers[uri]
	s, sok := c.byURI[uri]
	c.mu.RUnlock()
	if ok {
		return append([]byte(nil), b...), nil
	}
	if !sok {
		return nil, fmt.Errorf("song not found")
	}
	b, e := c.service.Cover(s)
	if e != nil {
		return nil, e
	}
	c.mu.Lock()
	if len(c.covers) >= 32 {
		old := c.coverOrder[0]
		c.coverOrder = c.coverOrder[1:]
		delete(c.covers, old)
	}
	c.covers[uri] = append([]byte(nil), b...)
	c.coverOrder = append(c.coverOrder, uri)
	c.mu.Unlock()
	return b, nil
}
func matchText(value, op, want string) bool {
	v, w := strings.ToLower(value), strings.ToLower(want)
	switch op {
	case "contains":
		return strings.Contains(v, w)
	case "starts_with":
		return strings.HasPrefix(v, w)
	default:
		return v == w
	}
}
