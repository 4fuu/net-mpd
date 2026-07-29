package mpd

import (
	"fmt"
	"path/filepath"
	"runtime"
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
	DailyRecommendSongs() ([]ncm.Song, error)
	PersonalFM() ([]ncm.Song, error)
	RecentSongs(int) ([]ncm.Song, error)
	CloudSongs() ([]ncm.Song, error)
	IntelligenceList(songID, playlistID int64) ([]ncm.Song, error)
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

// trackCacheService optionally skips song/detail for IDs already in the catalog.
type trackCacheService interface {
	PlaylistTracksKnown(int64, map[int64]ncm.Song) ([]ncm.Song, error)
}

// Virtual playlist IDs are negative so they never collide with NetEase IDs.
const (
	virtualDailyRecommendID int64 = -1
	virtualPersonalFMID     int64 = -2
	virtualRecentSongsID    int64 = -3
	virtualCloudID          int64 = -4
	virtualIntelligenceID   int64 = -5

	// Personal FM returns ~3 songs per request; a few batches is enough for a
	// usable queue without hammering the radio endpoint on every browse.
	personalFMBatches = 5

	// Appended to the liked-songs playlist name, e.g. "我喜欢的音乐（心动模式）".
	intelligenceNameSuffix = "（心动模式）"
)

// Fixed virtual modes (excluding 心动模式, which is derived from the liked list).
// Order here is the display order after liked + intelligence in composePlaylists.
var virtualPlaylists = []ncm.Playlist{
	{ID: virtualPersonalFMID, Name: "私人FM"},
	{ID: virtualDailyRecommendID, Name: "每日推荐"},
	{ID: virtualRecentSongsID, Name: "最近播放"},
	{ID: virtualCloudID, Name: "云盘"},
}

func isVirtualPlaylist(id int64) bool {
	return id < 0
}

// virtualPlaylistAliases lets clients address virtual lists by musicfox menu keys.
var virtualPlaylistAliases = map[string]int64{
	"每日推荐":            virtualDailyRecommendID,
	"daily_recommend": virtualDailyRecommendID,
	"daily_songs":     virtualDailyRecommendID,
	"私人FM":            virtualPersonalFMID,
	"personal_fm":     virtualPersonalFMID,
	"最近播放":            virtualRecentSongsID,
	"recent_songs":    virtualRecentSongsID,
	"recent":          virtualRecentSongsID,
	"云盘":              virtualCloudID,
	"cloud":           virtualCloudID,
	"心动模式":            virtualIntelligenceID,
	"intelligence":    virtualIntelligenceID,
}

// likedPlaylist is the NetEase "我喜欢的音乐" list. go-musicfox and the official
// client treat the first entry of the user-playlist API as that list.
func likedPlaylist(user []ncm.Playlist) (ncm.Playlist, bool) {
	if len(user) == 0 {
		return ncm.Playlist{}, false
	}
	return user[0], true
}

func intelligencePlaylist(liked ncm.Playlist) ncm.Playlist {
	return ncm.Playlist{
		ID:   virtualIntelligenceID,
		Name: liked.Name + intelligenceNameSuffix,
	}
}

// composePlaylists puts special/mode lists first, then the rest of the user's
// stored playlists:
//
//  1. 我喜欢的音乐 (liked)
//  2. 我喜欢的音乐（心动模式）
//  3. 私人FM / 每日推荐 / 最近播放 / 云盘
//  4. remaining user playlists
func composePlaylists(user []ncm.Playlist) []ncm.Playlist {
	out := make([]ncm.Playlist, 0, len(virtualPlaylists)+len(user)+1)
	liked, ok := likedPlaylist(user)
	if ok {
		out = append(out, liked)
		out = append(out, intelligencePlaylist(liked))
	}
	out = append(out, virtualPlaylists...)
	if len(user) > 1 {
		out = append(out, user[1:]...)
	}
	return out
}

func isReservedPlaylistName(name string) bool {
	name = stripOrderPrefix(sanitizePlaylistName(name))
	if _, ok := virtualPlaylistAliases[name]; ok {
		return true
	}
	for _, p := range virtualPlaylists {
		if playlistNameMatch(p, name) {
			return true
		}
	}
	if name == "心动模式" || strings.HasSuffix(name, intelligenceNameSuffix) ||
		strings.HasSuffix(sanitizePlaylistName(name), intelligenceNameSuffix) {
		return true
	}
	return false
}

type Catalog struct {
	service   MusicService
	refreshMu sync.Mutex
	mu        sync.RWMutex
	user      ncm.User
	playlists []ncm.Playlist
	tracks    map[int64][]ncm.Song
	byURI     map[string]ncm.Song
	// inflight coalesces concurrent loads of the same playlist id.
	inflight       map[int64]*trackLoad
	covers         map[string][]byte
	coverOrder     []string
	maxCovers      int
	refreshed      time.Time
	lyricsDir      string
	lyricsDone     map[int64]bool
	maxLyricsFiles int
	maxLyricsBytes int64
}

type trackLoad struct {
	done  chan struct{}
	songs []ncm.Song
	err   error
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
	return &Catalog{
		service:        service,
		user:           u,
		playlists:      append([]ncm.Playlist(nil), ps...),
		tracks:         make(map[int64][]ncm.Song),
		byURI:          make(map[string]ncm.Song),
		inflight:       make(map[int64]*trackLoad),
		covers:         make(map[string][]byte),
		maxCovers:      defaultMaxCovers,
		lyricsDone:     map[int64]bool{},
		maxLyricsFiles: defaultMaxLyricsFiles,
		maxLyricsBytes: defaultMaxLyricsBytes,
		refreshed:      time.Now(),
	}, nil
}

// SetLyricsDir configures where LRC files are written for rmpc.
// Layout matches rmpc's get_lrc_path for URIs like netease://song/<id>:
//
//	<dir>/netease:/song/<id>.lrc
//
// Triggers a background prune of oversized lyrics caches.
func (c *Catalog) SetLyricsDir(dir string) {
	c.mu.Lock()
	c.lyricsDir = dir
	c.mu.Unlock()
	go c.pruneLyricsCache()
}
func (c *Catalog) LyricsDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lyricsDir
}

// LRCPath is the on-disk path rmpc resolves for SongURI(id).
// On Unix/macOS rmpc maps netease://song/<id> → <dir>/netease:/song/<id>.lrc.
// Windows forbids ':' in path components, so we use netease_ there instead.
func LRCPath(dir string, songID int64) string {
	host := "netease:"
	if runtime.GOOS == "windows" {
		host = "netease_"
	}
	return filepath.Join(dir, host, "song", strconv.FormatInt(songID, 10)+".lrc")
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

// rmpc's Playlists pane always sorts by name (see rmpc playlists.rs
// sorted_by name), so bare server order is ignored. Every playlist name is
// prefixed with a zero-padded index ("01 - …") so alphabetical order matches
// composePlaylists order. Only listplaylists/lsinfo display names are affected;
// lookups still accept bare NetEase names and aliases.
func orderPrefix(n, total int) string {
	if n < 1 {
		return ""
	}
	width := len(strconv.Itoa(total))
	if width < 2 {
		width = 2
	}
	return fmt.Sprintf("%0*d - ", width, n)
}

func stripOrderPrefix(name string) string {
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 0 || !strings.HasPrefix(name[i:], " - ") {
		return name
	}
	return name[i+3:]
}

// PlaylistName is the bare name (no order prefix).
func PlaylistName(p ncm.Playlist) string { return sanitizePlaylistName(p.Name) }

// displayPlaylistName is what listplaylists/lsinfo show to clients.
func displayPlaylistName(p ncm.Playlist, index, total int) string {
	return orderPrefix(index, total) + PlaylistName(p)
}

func playlistNameMatch(p ncm.Playlist, name string) bool {
	name = stripOrderPrefix(name)
	if p.Name == name || strconv.FormatInt(p.ID, 10) == name {
		return true
	}
	safe := sanitizePlaylistName(p.Name)
	want := sanitizePlaylistName(name)
	return safe == name || safe == want || sanitizePlaylistName(stripOrderPrefix(p.Name)) == want
}

func (c *Catalog) Playlists() []ncm.Playlist {
	c.mu.RLock()
	defer c.mu.RUnlock()
	src := composePlaylists(c.playlists)
	total := len(src)
	out := make([]ncm.Playlist, total)
	for i, p := range src {
		p.Name = displayPlaylistName(p, i+1, total)
		out[i] = p
	}
	return out
}
func (c *Catalog) Playlist(name string) (ncm.Playlist, bool) {
	name = strings.TrimPrefix(name, "netease://playlist/")
	name = strings.TrimPrefix(name, "playlist/")
	name = stripOrderPrefix(name)
	c.mu.RLock()
	ps := composePlaylists(c.playlists)
	total := len(ps)
	c.mu.RUnlock()
	if id, ok := virtualPlaylistAliases[name]; ok {
		for i, p := range ps {
			if p.ID == id {
				p.Name = displayPlaylistName(p, i+1, total)
				return p, true
			}
		}
		return ncm.Playlist{}, false
	}
	for i, p := range ps {
		if playlistNameMatch(p, name) {
			p.Name = displayPlaylistName(p, i+1, total)
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
	return c.playlistSongsByID(p.ID)
}

func (c *Catalog) playlistSongsByID(id int64) ([]ncm.Song, error) {
	c.mu.Lock()
	if songs, ok := c.tracks[id]; ok {
		out := append([]ncm.Song(nil), songs...)
		c.mu.Unlock()
		return out, nil
	}
	// Join an in-flight load for this id instead of issuing a duplicate request.
	if load, ok := c.inflight[id]; ok {
		c.mu.Unlock()
		<-load.done
		if load.err != nil {
			return nil, load.err
		}
		return append([]ncm.Song(nil), load.songs...), nil
	}
	load := &trackLoad{done: make(chan struct{})}
	c.inflight[id] = load
	c.mu.Unlock()

	loaded, err := c.loadPlaylistTracks(id)
	c.mu.Lock()
	if err != nil {
		load.err = err
	} else if songs, ok := c.tracks[id]; ok {
		// Another writer (refresh) won the race; prefer the newer cache.
		load.songs = songs
	} else {
		load.songs = append([]ncm.Song(nil), loaded...)
		c.tracks[id] = load.songs
		c.indexSongs(load.songs)
	}
	delete(c.inflight, id)
	close(load.done)
	c.mu.Unlock()
	if load.err != nil {
		return nil, load.err
	}
	return append([]ncm.Song(nil), load.songs...), nil
}

func (c *Catalog) indexSongs(songs []ncm.Song) {
	for _, s := range songs {
		c.byURI[SongURI(s.ID)] = s
	}
}

func (c *Catalog) knownSongs() map[int64]ncm.Song {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.byURI) == 0 {
		return nil
	}
	out := make(map[int64]ncm.Song, len(c.byURI))
	for _, s := range c.byURI {
		out[s.ID] = s
	}
	return out
}

func (c *Catalog) loadPlaylistTracks(id int64) ([]ncm.Song, error) {
	switch id {
	case virtualDailyRecommendID:
		return c.service.DailyRecommendSongs()
	case virtualPersonalFMID:
		return c.loadPersonalFM()
	case virtualRecentSongsID:
		return c.service.RecentSongs(100)
	case virtualCloudID:
		return c.service.CloudSongs()
	case virtualIntelligenceID:
		return c.loadIntelligence()
	default:
		return c.loadUserPlaylistTracks(id)
	}
}

func (c *Catalog) loadUserPlaylistTracks(id int64) ([]ncm.Song, error) {
	known := c.knownSongs()
	if svc, ok := c.service.(trackCacheService); ok {
		return svc.PlaylistTracksKnown(id, known)
	}
	return c.service.PlaylistTracks(id)
}

// loadIntelligence seeds 心动模式 from the first track of the liked playlist
// (musicfox uses the currently selected song; for a stored MPD playlist we
// always start from track 0).
func (c *Catalog) loadIntelligence() ([]ncm.Song, error) {
	c.mu.RLock()
	liked, ok := likedPlaylist(c.playlists)
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("liked playlist not found")
	}
	likedTracks, err := c.playlistSongsByID(liked.ID)
	if err != nil {
		return nil, err
	}
	if len(likedTracks) == 0 {
		return nil, fmt.Errorf("liked playlist is empty")
	}
	seed := likedTracks[0]
	recs, err := c.service.IntelligenceList(seed.ID, liked.ID)
	if err != nil {
		return nil, err
	}
	out := make([]ncm.Song, 0, 1+len(recs))
	out = append(out, seed)
	seen := map[int64]bool{seed.ID: true}
	for _, s := range recs {
		if seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		out = append(out, s)
	}
	return out, nil
}

func (c *Catalog) loadPersonalFM() ([]ncm.Song, error) {
	seen := map[int64]bool{}
	var out []ncm.Song
	for i := 0; i < personalFMBatches; i++ {
		batch, err := c.service.PersonalFM()
		if err != nil {
			if len(out) == 0 {
				return nil, err
			}
			break
		}
		if len(batch) == 0 {
			break
		}
		added := 0
		for _, s := range batch {
			if seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			out = append(out, s)
			added++
		}
		if added == 0 {
			break
		}
	}
	return out, nil
}

func (c *Catalog) AllSongs() ([]ncm.Song, error) {
	// Tag browsers / listall only index the user's stored playlists. Virtual
	// modes (FM, daily recommend, …) stay on-demand so a full library walk
	// does not hammer dynamic recommendation APIs.
	c.mu.RLock()
	ps := append([]ncm.Playlist(nil), c.playlists...)
	c.mu.RUnlock()
	seen := map[int64]bool{}
	var out []ncm.Song
	for _, p := range ps {
		ss, e := c.playlistSongsByID(p.ID)
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
		return c.refreshScoped(ps, scope)
	}
	return c.refreshAll(ps)
}

func (c *Catalog) lookupIn(ps []ncm.Playlist, name string) (ncm.Playlist, bool) {
	normalized := strings.TrimPrefix(strings.TrimPrefix(name, "netease://playlist/"), "playlist/")
	composed := composePlaylists(ps)
	if id, ok := virtualPlaylistAliases[normalized]; ok {
		for _, p := range composed {
			if p.ID == id {
				return p, true
			}
		}
		return ncm.Playlist{}, false
	}
	for _, p := range composed {
		if playlistNameMatch(p, normalized) {
			return p, true
		}
	}
	return ncm.Playlist{}, false
}

// refreshScoped reloads only the named playlist and keeps every other cache.
func (c *Catalog) refreshScoped(ps []ncm.Playlist, scope string) error {
	target, ok := c.lookupIn(ps, scope)
	if !ok {
		return fmt.Errorf("playlist not found")
	}
	// Drop the target cache first so loadPlaylistTracks cannot serve stale data.
	c.mu.Lock()
	delete(c.tracks, target.ID)
	// Liked-seeded intelligence depends on liked tracks; bust it when liked moves.
	if liked, ok := likedPlaylist(ps); ok && target.ID == liked.ID {
		delete(c.tracks, virtualIntelligenceID)
	}
	c.playlists = append([]ncm.Playlist(nil), ps...)
	c.mu.Unlock()

	ss, err := c.loadPlaylistTracks(target.ID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.tracks[target.ID] = append([]ncm.Song(nil), ss...)
	c.indexSongs(ss)
	c.refreshed = time.Now()
	c.mu.Unlock()
	return nil
}

// refreshAll reloads the playlist list. User playlists whose trackCount is
// unchanged keep their track cache; changed/new ones reload. Virtual mode
// caches are dropped so the next browse refetches dynamic content.
func (c *Catalog) refreshAll(ps []ncm.Playlist) error {
	c.mu.RLock()
	oldMeta := make(map[int64]ncm.Playlist, len(c.playlists))
	for _, p := range c.playlists {
		oldMeta[p.ID] = p
	}
	oldTracks := make(map[int64][]ncm.Song, len(c.tracks))
	for id, ss := range c.tracks {
		if isVirtualPlaylist(id) {
			continue // always drop virtual on full update
		}
		oldTracks[id] = ss
	}
	c.mu.RUnlock()

	tracks := make(map[int64][]ncm.Song, len(ps))
	var toLoad []ncm.Playlist
	for _, p := range ps {
		if prev, ok := oldMeta[p.ID]; ok && prev.TrackCount == p.TrackCount {
			if ss, ok := oldTracks[p.ID]; ok {
				tracks[p.ID] = append([]ncm.Song(nil), ss...)
				continue
			}
		}
		toLoad = append(toLoad, p)
	}
	for _, p := range toLoad {
		ss, err := c.loadUserPlaylistTracks(p.ID)
		if err != nil {
			return err
		}
		tracks[p.ID] = append([]ncm.Song(nil), ss...)
	}

	byURI := make(map[string]ncm.Song, len(c.byURI))
	for _, ss := range tracks {
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

// reloadPlaylist re-fetches one user playlist's tracks and patches the cache.
func (c *Catalog) reloadPlaylist(id int64) error {
	ss, err := c.loadUserPlaylistTracks(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.tracks[id] = append([]ncm.Song(nil), ss...)
	c.indexSongs(ss)
	// Intelligence is derived from liked; invalidate so the next open reseeds.
	if liked, ok := likedPlaylist(c.playlists); ok && liked.ID == id {
		delete(c.tracks, virtualIntelligenceID)
	}
	for i := range c.playlists {
		if c.playlists[i].ID == id {
			c.playlists[i].TrackCount = len(ss)
			break
		}
	}
	c.refreshed = time.Now()
	c.mu.Unlock()
	return nil
}

func errVirtualPlaylistReadOnly() error {
	return fmt.Errorf("virtual playlist is read-only")
}

func (c *Catalog) CreatePlaylist(name string, songs []QueueItem) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	// Keep NetEase-side names free of '/', matching what MPD clients see.
	name = sanitizePlaylistName(name)
	if isReservedPlaylistName(name) {
		return fmt.Errorf("playlist name is reserved")
	}
	p, err := c.service.CreatePlaylist(name)
	if err != nil {
		return err
	}
	ids := make([]int64, len(songs))
	local := make([]ncm.Song, 0, len(songs))
	for i := range songs {
		ids[i] = songs[i].Song.ID
		local = append(local, songs[i].Song)
	}
	if len(ids) > 0 {
		if err = c.service.AddPlaylistTracks(p.ID, ids); err != nil {
			return err
		}
	}
	p.TrackCount = len(local)
	c.mu.Lock()
	c.playlists = append(c.playlists, p)
	c.tracks[p.ID] = append([]ncm.Song(nil), local...)
	c.indexSongs(local)
	c.refreshed = time.Now()
	c.mu.Unlock()
	return nil
}
func (c *Catalog) RenamePlaylist(name, newName string) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	p, ok := c.Playlist(name)
	if !ok {
		return fmt.Errorf("playlist not found")
	}
	if isVirtualPlaylist(p.ID) {
		return errVirtualPlaylistReadOnly()
	}
	newName = sanitizePlaylistName(newName)
	if err := c.service.RenamePlaylist(p.ID, newName); err != nil {
		return err
	}
	c.mu.Lock()
	for i := range c.playlists {
		if c.playlists[i].ID == p.ID {
			c.playlists[i].Name = newName
			break
		}
	}
	// Liked rename changes the intelligence display name; cache entries stay valid.
	delete(c.tracks, virtualIntelligenceID)
	c.refreshed = time.Now()
	c.mu.Unlock()
	return nil
}
func (c *Catalog) DeletePlaylist(name string) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	p, ok := c.Playlist(name)
	if !ok {
		return fmt.Errorf("playlist not found")
	}
	if isVirtualPlaylist(p.ID) {
		return errVirtualPlaylistReadOnly()
	}
	if err := c.service.DeletePlaylist(p.ID); err != nil {
		return err
	}
	c.mu.Lock()
	for i := range c.playlists {
		if c.playlists[i].ID == p.ID {
			c.playlists = append(c.playlists[:i], c.playlists[i+1:]...)
			break
		}
	}
	delete(c.tracks, p.ID)
	delete(c.tracks, virtualIntelligenceID)
	c.refreshed = time.Now()
	c.mu.Unlock()
	return nil
}
func (c *Catalog) AddPlaylistSong(name string, song ncm.Song) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	p, ok := c.Playlist(name)
	if !ok {
		return fmt.Errorf("playlist not found")
	}
	if isVirtualPlaylist(p.ID) {
		return errVirtualPlaylistReadOnly()
	}
	if err := c.service.AddPlaylistTracks(p.ID, []int64{song.ID}); err != nil {
		return err
	}
	c.mu.Lock()
	if ss, ok := c.tracks[p.ID]; ok {
		c.tracks[p.ID] = append(ss, song)
		c.indexSongs([]ncm.Song{song})
		for i := range c.playlists {
			if c.playlists[i].ID == p.ID {
				c.playlists[i].TrackCount = len(c.tracks[p.ID])
				break
			}
		}
		if liked, ok := likedPlaylist(c.playlists); ok && liked.ID == p.ID {
			delete(c.tracks, virtualIntelligenceID)
		}
		c.refreshed = time.Now()
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return c.reloadPlaylist(p.ID)
}
func (c *Catalog) DeletePlaylistSong(name string, pos int) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	p, ok := c.Playlist(name)
	if !ok {
		return fmt.Errorf("playlist not found")
	}
	if isVirtualPlaylist(p.ID) {
		return errVirtualPlaylistReadOnly()
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
	c.mu.Lock()
	if cached, ok := c.tracks[p.ID]; ok && pos < len(cached) {
		c.tracks[p.ID] = append(cached[:pos:pos], cached[pos+1:]...)
		for i := range c.playlists {
			if c.playlists[i].ID == p.ID {
				c.playlists[i].TrackCount = len(c.tracks[p.ID])
				break
			}
		}
		if liked, ok := likedPlaylist(c.playlists); ok && liked.ID == p.ID {
			delete(c.tracks, virtualIntelligenceID)
		}
		c.refreshed = time.Now()
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return c.reloadPlaylist(p.ID)
}
func (c *Catalog) ClearPlaylist(name string) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	p, ok := c.Playlist(name)
	if !ok {
		return fmt.Errorf("playlist not found")
	}
	if isVirtualPlaylist(p.ID) {
		return errVirtualPlaylistReadOnly()
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
	c.mu.Lock()
	c.tracks[p.ID] = nil
	for i := range c.playlists {
		if c.playlists[i].ID == p.ID {
			c.playlists[i].TrackCount = 0
			break
		}
	}
	if liked, ok := likedPlaylist(c.playlists); ok && liked.ID == p.ID {
		delete(c.tracks, virtualIntelligenceID)
	}
	c.refreshed = time.Now()
	c.mu.Unlock()
	return nil
}
func (c *Catalog) Resolve(s ncm.Song) (ncm.PlayableInfo, error) { return c.service.ResolveURL(s.ID) }

func (c *Catalog) Cover(uri string) ([]byte, error) {
	c.mu.Lock()
	if b, ok := c.covers[uri]; ok {
		// Promote to most-recently-used.
		for i, u := range c.coverOrder {
			if u == uri {
				c.coverOrder = append(append(c.coverOrder[:i:i], c.coverOrder[i+1:]...), uri)
				break
			}
		}
		out := append([]byte(nil), b...)
		c.mu.Unlock()
		return out, nil
	}
	s, sok := c.byURI[uri]
	maxCovers := c.maxCovers
	if maxCovers <= 0 {
		maxCovers = defaultMaxCovers
	}
	c.mu.Unlock()
	if !sok {
		return nil, fmt.Errorf("song not found")
	}
	b, e := c.service.Cover(s)
	if e != nil {
		return nil, e
	}
	c.mu.Lock()
	// Another goroutine may have filled the cache while we fetched.
	if existing, ok := c.covers[uri]; ok {
		for i, u := range c.coverOrder {
			if u == uri {
				c.coverOrder = append(append(c.coverOrder[:i:i], c.coverOrder[i+1:]...), uri)
				break
			}
		}
		out := append([]byte(nil), existing...)
		c.mu.Unlock()
		return out, nil
	}
	for len(c.covers) >= maxCovers && len(c.coverOrder) > 0 {
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
