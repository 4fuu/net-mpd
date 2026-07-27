package mpd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/4fuu/net-mpd/internal/ncm"
)

type MusicService interface {
	Account() (ncm.User, error)
	UserPlaylists(int64) ([]ncm.Playlist, error)
	PlaylistTracks(int64) ([]ncm.Song, error)
	SearchSongs(string, int) ([]ncm.Song, error)
	ResolveURL(int64) (ncm.PlayableInfo, error)
	Cover(ncm.Song) ([]byte, error)
	Lyrics(int64) (ncm.Lyrics, error)
	CreatePlaylist(string) (ncm.Playlist, error)
	RenamePlaylist(int64, string) error
	DeletePlaylist(int64) error
	AddPlaylistTracks(int64, []int64) error
	DeletePlaylistTracks(int64, []int64) error
}

type Catalog struct {
	service    MusicService
	refreshMu  sync.Mutex
	loadMu     sync.Mutex
	mu         sync.RWMutex
	user       ncm.User
	playlists  []ncm.Playlist
	tracks     map[int64][]ncm.Song
	byURI      map[string]ncm.Song
	covers     map[string][]byte
	coverOrder []string
	refreshed  time.Time
	lyricsDir  string
	lyricsDone map[int64]bool
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
	return &Catalog{service: service, user: u, playlists: append([]ncm.Playlist(nil), ps...), tracks: make(map[int64][]ncm.Song), byURI: make(map[string]ncm.Song), covers: make(map[string][]byte), lyricsDone: map[int64]bool{}, refreshed: time.Now()}, nil
}

// SetLyricsDir configures where LRC files are written for rmpc.
// Layout matches rmpc's get_lrc_path for URIs like netease://song/<id>:
//
//	<dir>/netease:/song/<id>.lrc
func (c *Catalog) SetLyricsDir(dir string) {
	c.mu.Lock()
	c.lyricsDir = dir
	c.mu.Unlock()
}
func (c *Catalog) LyricsDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lyricsDir
}

// LRCPath is the on-disk path rmpc resolves for SongURI(id).
func LRCPath(dir string, songID int64) string {
	return filepath.Join(dir, "netease:", "song", strconv.FormatInt(songID, 10)+".lrc")
}
func SongURI(id int64) string     { return "netease://song/" + strconv.FormatInt(id, 10) }
func (c *Catalog) User() ncm.User { c.mu.RLock(); defer c.mu.RUnlock(); return c.user }

// MPD playlist names must not contain '/': clients such as rmpc treat it as a
// path separator when parsing lsinfo/listplaylists entries, then call
// listplaylistinfo with only the final segment and get "playlist not found".
const playlistPathSafe = "／" // fullwidth solidus, looks like '/' but is not a path sep

func sanitizePlaylistName(name string) string {
	return strings.ReplaceAll(name, "/", playlistPathSafe)
}

// PlaylistName is the name exposed over the MPD protocol.
func PlaylistName(p ncm.Playlist) string { return sanitizePlaylistName(p.Name) }

func playlistNameMatch(p ncm.Playlist, name string) bool {
	if p.Name == name || strconv.FormatInt(p.ID, 10) == name {
		return true
	}
	safe := sanitizePlaylistName(p.Name)
	return safe == name || safe == sanitizePlaylistName(name)
}

func (c *Catalog) Playlists() []ncm.Playlist {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ncm.Playlist, len(c.playlists))
	for i, p := range c.playlists {
		p.Name = PlaylistName(p)
		out[i] = p
	}
	return out
}
func (c *Catalog) Playlist(name string) (ncm.Playlist, bool) {
	name = strings.TrimPrefix(name, "netease://playlist/")
	name = strings.TrimPrefix(name, "playlist/")
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range c.playlists {
		if playlistNameMatch(p, name) {
			p.Name = PlaylistName(p)
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
		c.loadMu.Lock()
		defer c.loadMu.Unlock()
		c.mu.RLock()
		songs, ok = c.tracks[p.ID]
		c.mu.RUnlock()
	}
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
func (c *Catalog) LastRefresh() time.Time { c.mu.RLock(); defer c.mu.RUnlock(); return c.refreshed }
func (c *Catalog) Refresh(scope string) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	return c.refreshLocked(scope)
}
func (c *Catalog) refreshLocked(scope string) error {
	ps, err := c.service.UserPlaylists(c.User().ID)
	if err != nil {
		return err
	}
	if scope != "" {
		normalized := strings.TrimPrefix(strings.TrimPrefix(scope, "netease://playlist/"), "playlist/")
		found := false
		for _, p := range ps {
			if playlistNameMatch(p, normalized) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("playlist not found")
		}
	}
	tracks := make(map[int64][]ncm.Song, len(ps))
	byURI := make(map[string]ncm.Song)
	for _, p := range ps {
		ss, loadErr := c.service.PlaylistTracks(p.ID)
		if loadErr != nil {
			return loadErr
		}
		tracks[p.ID] = append([]ncm.Song(nil), ss...)
		for _, s := range ss {
			byURI[SongURI(s.ID)] = s
		}
	}
	c.mu.Lock()
	c.playlists = append([]ncm.Playlist(nil), ps...)
	c.tracks = tracks
	c.byURI = byURI
	c.refreshed = time.Now()
	c.mu.Unlock()
	return nil
}
func (c *Catalog) CreatePlaylist(name string, songs []QueueItem) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	// Keep NetEase-side names free of '/', matching what MPD clients see.
	name = sanitizePlaylistName(name)
	p, err := c.service.CreatePlaylist(name)
	if err != nil {
		return err
	}
	ids := make([]int64, len(songs))
	for i := range songs {
		ids[i] = songs[i].Song.ID
	}
	if len(ids) > 0 {
		if err = c.service.AddPlaylistTracks(p.ID, ids); err != nil {
			return err
		}
	}
	return c.refreshLocked("")
}
func (c *Catalog) RenamePlaylist(name, newName string) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	p, ok := c.Playlist(name)
	if !ok {
		return fmt.Errorf("playlist not found")
	}
	newName = sanitizePlaylistName(newName)
	if err := c.service.RenamePlaylist(p.ID, newName); err != nil {
		return err
	}
	return c.refreshLocked("")
}
func (c *Catalog) DeletePlaylist(name string) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	p, ok := c.Playlist(name)
	if !ok {
		return fmt.Errorf("playlist not found")
	}
	if err := c.service.DeletePlaylist(p.ID); err != nil {
		return err
	}
	return c.refreshLocked("")
}
func (c *Catalog) AddPlaylistSong(name string, song ncm.Song) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	p, ok := c.Playlist(name)
	if !ok {
		return fmt.Errorf("playlist not found")
	}
	if err := c.service.AddPlaylistTracks(p.ID, []int64{song.ID}); err != nil {
		return err
	}
	return c.refreshLocked("")
}
func (c *Catalog) DeletePlaylistSong(name string, pos int) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	p, ok := c.Playlist(name)
	if !ok {
		return fmt.Errorf("playlist not found")
	}
	ss, err := c.PlaylistSongs(name)
	if err != nil {
		return err
	}
	if pos < 0 || pos >= len(ss) {
		return fmt.Errorf("invalid position")
	}
	if err = c.service.DeletePlaylistTracks(p.ID, []int64{ss[pos].ID}); err != nil {
		return err
	}
	return c.refreshLocked("")
}
func (c *Catalog) ClearPlaylist(name string) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	p, ok := c.Playlist(name)
	if !ok {
		return fmt.Errorf("playlist not found")
	}
	ss, err := c.PlaylistSongs(name)
	if err != nil {
		return err
	}
	ids := make([]int64, len(ss))
	for i := range ss {
		ids[i] = ss[i].ID
	}
	if len(ids) > 0 {
		if err = c.service.DeletePlaylistTracks(p.ID, ids); err != nil {
			return err
		}
	}
	return c.refreshLocked("")
}
func (c *Catalog) Resolve(s ncm.Song) (ncm.PlayableInfo, error) { return c.service.ResolveURL(s.ID) }

// EnsureLyrics fetches NetEase lyrics and writes an LRC file rmpc can open.
// Safe to call repeatedly; failures are non-fatal for playback.
func (c *Catalog) EnsureLyrics(song ncm.Song) error {
	c.mu.RLock()
	dir := c.lyricsDir
	done := c.lyricsDone[song.ID]
	c.mu.RUnlock()
	if dir == "" {
		return nil
	}
	path := LRCPath(dir, song.ID)
	if done {
		return nil
	}
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		c.mu.Lock()
		c.lyricsDone[song.ID] = true
		c.mu.Unlock()
		return nil
	}
	ly, err := c.service.Lyrics(song.ID)
	if err != nil {
		return err
	}
	if ly.Original == "" {
		c.mu.Lock()
		c.lyricsDone[song.ID] = true
		c.mu.Unlock()
		return nil
	}
	body := formatLRC(song, ly)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	c.mu.Lock()
	c.lyricsDone[song.ID] = true
	c.mu.Unlock()
	return nil
}

func formatLRC(song ncm.Song, ly ncm.Lyrics) string {
	var b strings.Builder
	if len(song.Artists) > 0 {
		fmt.Fprintf(&b, "[ar:%s]\n", strings.Join(song.Artists, "/"))
	}
	if song.Album != "" {
		fmt.Fprintf(&b, "[al:%s]\n", song.Album)
	}
	if song.Title != "" {
		fmt.Fprintf(&b, "[ti:%s]\n", song.Title)
	}
	fmt.Fprintf(&b, "[by:net-mpd]\n")
	b.WriteString(strings.TrimRight(ly.Original, "\n"))
	b.WriteByte('\n')
	if ly.Translated != "" {
		// Bilingual: keep original block then translation block so rmpc shows both.
		b.WriteByte('\n')
		b.WriteString(strings.TrimRight(ly.Translated, "\n"))
		b.WriteByte('\n')
	}
	return b.String()
}

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
