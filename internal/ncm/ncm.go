package ncm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-musicfox/netease-music/service"
	neteaseutil "github.com/go-musicfox/netease-music/util"
	cookiejar "github.com/juju/persistent-cookiejar"
)

type User struct {
	ID       int64
	Nickname string
}

type Playlist struct {
	ID         int64
	Name       string
	TrackCount int
	CoverURL   string
}

type Song struct {
	ID       int64
	Title    string
	Artists  []string
	Album    string
	AlbumID  int64
	Duration time.Duration
	CoverURL string
}

type PlayableInfo struct {
	URL  string
	Type string
	Size int64
}

// Lyrics holds NetEase LRC text. Translated may be empty.
type Lyrics struct {
	Original   string
	Translated string
}

type Client struct {
	jar        *cookiejar.Jar
	cookiePath string
	httpClient *http.Client
}

const (
	playlistDetailAPI = "https://music.163.com/weapi/v3/playlist/detail"
	songDetailAPI     = "https://music.163.com/weapi/v3/song/detail"
	songDetailBatch   = 500
)

var sdkMu sync.Mutex

type weapiCaller func(string, map[string]interface{}) (float64, []byte, error)

func (c *Client) lockSDK() {
	sdkMu.Lock()
	neteaseutil.SetGlobalCookieJar(c.jar)
	neteaseutil.HTTPClientTimeout = 15 * time.Second
}

func Open(cookiePath string) (*Client, error) {
	if cookiePath == "" {
		return nil, errors.New("cookie jar path is empty")
	}
	if info, err := os.Stat(cookiePath); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("open cookie jar %q: path is not a regular file", cookiePath)
		}
		if err := os.Chmod(cookiePath, 0600); err != nil {
			return nil, fmt.Errorf("secure cookie jar %q: %w", cookiePath, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(cookiePath), 0700); err != nil {
		return nil, fmt.Errorf("create cookie directory: %w", err)
	}
	jar, err := cookiejar.New(&cookiejar.Options{Filename: cookiePath})
	if err != nil {
		return nil, fmt.Errorf("load cookie jar %q: %w", cookiePath, err)
	}
	return &Client{jar: jar, cookiePath: cookiePath, httpClient: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (c *Client) call(fn func() (float64, []byte)) (float64, []byte) {
	c.lockSDK()
	defer sdkMu.Unlock()
	return fn()
}

func (c *Client) Account() (User, error) {
	code, body := c.call(func() (float64, []byte) { return (&service.UserAccountService{}).AccountInfo() })
	return parseAccount("account", code, body)
}

func (c *Client) UserPlaylists(uid int64) ([]Playlist, error) {
	var all []Playlist
	for offset := 0; ; offset += 100 {
		s := &service.UserPlaylistService{Uid: strconv.FormatInt(uid, 10), Limit: "100", Offset: strconv.Itoa(offset)}
		code, body := c.call(s.UserPlaylist)
		items, more, err := parsePlaylists("user playlists", code, body)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if !more {
			return all, nil
		}
	}
}

func (c *Client) PlaylistTracks(id int64) ([]Song, error) {
	c.lockSDK()
	defer sdkMu.Unlock()
	return fetchPlaylistTracks(id, func(api string, data map[string]interface{}) (float64, []byte, error) {
		return neteaseutil.CallWeapi(api, data, c.jar)
	})
}

func fetchPlaylistTracks(id int64, call weapiCaller) ([]Song, error) {
	code, body, err := call(playlistDetailAPI, map[string]interface{}{
		"id": strconv.FormatInt(id, 10),
		"n":  "100000",
		"s":  "0",
	})
	if err != nil {
		return nil, fmt.Errorf("playlist tracks: fetch detail: %w", err)
	}
	if err := checkResponse("playlist tracks", code, body); err != nil {
		return nil, err
	}
	var detail struct {
		Playlist struct {
			TrackIDs []struct {
				ID int64 `json:"id"`
			} `json:"trackIds"`
		} `json:"playlist"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		return nil, fmt.Errorf("playlist tracks: decode detail: %w", err)
	}

	trackIDs := detail.Playlist.TrackIDs
	songs := make([]Song, 0, len(trackIDs))
	for start := 0; start < len(trackIDs); start += songDetailBatch {
		end := min(start+songDetailBatch, len(trackIDs))
		ids := make([]string, end-start)
		refs := make([]struct {
			ID string `json:"id"`
		}, end-start)
		for i, track := range trackIDs[start:end] {
			ids[i] = strconv.FormatInt(track.ID, 10)
			refs[i].ID = ids[i]
		}
		encodedRefs, err := json.Marshal(refs)
		if err != nil {
			return nil, fmt.Errorf("playlist tracks: encode song IDs: %w", err)
		}
		code, body, err = call(songDetailAPI, map[string]interface{}{
			"c":   string(encodedRefs),
			"ids": "[" + strings.Join(ids, ",") + "]",
		})
		if err != nil {
			return nil, fmt.Errorf("playlist tracks: fetch songs: %w", err)
		}
		batch, err := parseSongDetails("playlist tracks", code, body)
		if err != nil {
			return nil, err
		}
		songs = append(songs, batch...)
	}
	return songs, nil
}

func (c *Client) CreatePlaylist(name string) (Playlist, error) {
	s := &service.PlaylistCreateService{Name: name}
	code, body := c.call(s.PlaylistCreate)
	if err := checkResponse("create playlist", code, body); err != nil {
		return Playlist{}, err
	}
	return parseCreatedPlaylist(name, body)
}

func parseCreatedPlaylist(name string, body []byte) (Playlist, error) {
	var r struct {
		Playlist struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"playlist"`
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Playlist{}, fmt.Errorf("create playlist: decode response: %w", err)
	}
	if r.Playlist.ID == 0 {
		r.Playlist.ID = r.ID
	}
	if r.Playlist.ID == 0 {
		return Playlist{}, errors.New("create playlist: playlist ID missing")
	}
	if r.Playlist.Name == "" {
		r.Playlist.Name = name
	}
	return Playlist{ID: r.Playlist.ID, Name: r.Playlist.Name}, nil
}

func (c *Client) RenamePlaylist(id int64, name string) error {
	s := &service.PlaylistNameUpdateService{Id: strconv.FormatInt(id, 10), Name: name}
	code, body := c.call(s.PlaylistNameUpdate)
	return checkResponse("rename playlist", code, body)
}

func (c *Client) DeletePlaylist(id int64) error {
	s := &service.PlaylistDeleteService{ID: strconv.FormatInt(id, 10)}
	code, body := c.call(s.PlaylistDelete)
	return checkResponse("delete playlist", code, body)
}

func (c *Client) AddPlaylistTracks(id int64, songIDs []int64) error {
	ids := make([]string, len(songIDs))
	for i, songID := range songIDs {
		ids[i] = strconv.FormatInt(songID, 10)
	}
	s := &service.PlaylistTrackAddService{Id: strconv.FormatInt(id, 10), SongIds: ids}
	code, body := c.call(s.AddTracks)
	return checkResponse("add playlist tracks", code, body)
}

func (c *Client) DeletePlaylistTracks(id int64, songIDs []int64) error {
	ids := make([]string, len(songIDs))
	for i, songID := range songIDs {
		ids[i] = strconv.FormatInt(songID, 10)
	}
	s := &service.PlaylistTrackDeleteService{Id: strconv.FormatInt(id, 10), SongIds: ids}
	code, body := c.call(s.DeleteTracks)
	return checkResponse("delete playlist tracks", code, body)
}

func (c *Client) SearchSongs(query string, limit int) ([]Song, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	s := &service.SearchService{S: query, Type: "1", Limit: strconv.Itoa(limit), Offset: "0"}
	code, body := c.call(s.Search)
	return parseSearchSongs("search songs", code, body)
}

func (c *Client) ResolveURL(songID int64) (PlayableInfo, error) {
	id := strconv.FormatInt(songID, 10)
	// Prefer v1, then fall back to the legacy linuxapi endpoint. Some v1 CDN
	// hosts (or proxy rules in front of them) return 403 even though the API
	// response looks valid; probe before handing the URL to the player.
	var candidates []PlayableInfo
	v1 := &service.SongUrlV1Service{ID: id, Level: service.Standard, EncodeType: "mp3"}
	c.lockSDK()
	code, body, callErr := v1.SongUrl()
	sdkMu.Unlock()
	if callErr == nil {
		if info, _, err := parseURLResponse("resolve song URL v1", code, body); err == nil {
			candidates = append(candidates, info)
		}
	}
	// Higher quality via v1 as a second try when standard is unreachable.
	v1h := &service.SongUrlV1Service{ID: id, Level: service.Higher, EncodeType: "mp3"}
	c.lockSDK()
	code, body, callErr = v1h.SongUrl()
	sdkMu.Unlock()
	if callErr == nil {
		if info, _, err := parseURLResponse("resolve song URL v1 higher", code, body); err == nil {
			candidates = append(candidates, info)
		}
	}
	for _, br := range []string{"320000", "128000"} {
		legacy := &service.SongUrlService{ID: id, Br: br}
		code, body = c.call(legacy.SongUrl)
		if info, _, err := parseURLResponse("resolve song URL fallback", code, body); err == nil {
			candidates = append(candidates, info)
		}
	}
	seen := map[string]bool{}
	var lastErr error
	for _, info := range candidates {
		if info.URL == "" || seen[info.URL] {
			continue
		}
		seen[info.URL] = true
		if err := probePlayableURL(info.URL); err != nil {
			lastErr = err
			continue
		}
		if info.Type == "" {
			info.Type = "mp3"
		}
		return info, nil
	}
	if lastErr != nil {
		return PlayableInfo{}, fmt.Errorf("song is not playable: %w", lastErr)
	}
	return PlayableInfo{}, fmt.Errorf("song is not playable (no URL from API)")
}

// probePlayableURL does a tiny ranged GET so we reject CDN 403/404 before the
// native player reports a silent stuck "play" state. Full URLs are never logged.
func probePlayableURL(raw string) error {
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return fmt.Errorf("invalid playback URL: %w", err)
	}
	// Preserve '+' in signed query strings; url.Parse keeps them in RawQuery,
	// but re-encoding via Query() would turn them into spaces.
	req.Header.Set("Range", "bytes=0-1")
	req.Header.Set("Referer", "https://music.163.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("playback URL unreachable: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64))
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		return nil
	}
	return fmt.Errorf("playback URL HTTP %d", resp.StatusCode)
}

const maxCoverSize = 10 << 20

func (c *Client) Cover(song Song) ([]byte, error) {
	if song.CoverURL == "" {
		return nil, errors.New("cover: empty URL")
	}
	sep := "?"
	if strings.Contains(song.CoverURL, "?") {
		sep = "&"
	}
	resp, err := c.httpClient.Get(song.CoverURL + sep + "param=512y512")
	if err != nil {
		return nil, fmt.Errorf("cover: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cover: HTTP status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverSize+1))
	if err != nil {
		return nil, fmt.Errorf("cover: %w", err)
	}
	if len(b) > maxCoverSize {
		return nil, fmt.Errorf("cover: response exceeds %d bytes", maxCoverSize)
	}
	return b, nil
}

// Lyrics fetches timed LRC lyrics for a song. Missing lyrics return an empty
// Original without error so callers can skip writing a file.
func (c *Client) Lyrics(songID int64) (Lyrics, error) {
	s := &service.LyricService{ID: strconv.FormatInt(songID, 10)}
	code, body := c.call(s.Lyric)
	if err := checkResponse("lyrics", code, body); err != nil {
		return Lyrics{}, err
	}
	var r struct {
		Lrc struct {
			Lyric string `json:"lyric"`
		} `json:"lrc"`
		Tlyric struct {
			Lyric string `json:"lyric"`
		} `json:"tlyric"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Lyrics{}, fmt.Errorf("lyrics: decode response: %w", err)
	}
	orig := strings.TrimSpace(r.Lrc.Lyric)
	// NetEase returns placeholders for instrumentals; treat as empty.
	if isPlaceholderLyric(orig) {
		orig = ""
	}
	trans := strings.TrimSpace(r.Tlyric.Lyric)
	if isPlaceholderLyric(trans) {
		trans = ""
	}
	return Lyrics{Original: orig, Translated: trans}, nil
}

func isPlaceholderLyric(s string) bool {
	if s == "" {
		return true
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		content := line
		if i := strings.LastIndex(line, "]"); i >= 0 && strings.HasPrefix(line, "[") {
			content = strings.TrimSpace(line[i+1:])
		}
		if content == "" {
			continue
		}
		switch content {
		case "纯音乐，请欣赏", "暂无歌词", "暂无歌词~":
			continue
		}
		return false
	}
	return true
}

type envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Msg     string `json:"msg"`
}

func checkResponse(op string, sdkCode float64, body []byte) error {
	var e envelope
	if err := json.Unmarshal(body, &e); err != nil {
		return fmt.Errorf("%s: decode response: %w", op, err)
	}
	code := e.Code
	if code == 0 {
		code = int(sdkCode)
	}
	if code != 200 {
		msg := e.Message
		if msg == "" {
			msg = e.Msg
		}
		if msg != "" {
			return fmt.Errorf("%s: code %d: %s", op, code, msg)
		}
		return fmt.Errorf("%s: code %d", op, code)
	}
	return nil
}

func parseAccount(op string, code float64, body []byte) (User, error) {
	if err := checkResponse(op, code, body); err != nil {
		return User{}, err
	}
	var r struct {
		Account *struct {
			ID int64 `json:"id"`
		} `json:"account"`
		Profile *struct {
			UserID   int64  `json:"userId"`
			Nickname string `json:"nickname"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return User{}, fmt.Errorf("%s: decode response: %w", op, err)
	}
	if r.Account == nil || r.Profile == nil {
		return User{}, fmt.Errorf("%s: not logged in (account/profile missing)", op)
	}
	id := r.Account.ID
	if id == 0 {
		id = r.Profile.UserID
	}
	if id == 0 {
		return User{}, fmt.Errorf("%s: not logged in (user ID missing)", op)
	}
	return User{ID: id, Nickname: r.Profile.Nickname}, nil
}

func parsePlaylists(op string, code float64, body []byte) ([]Playlist, bool, error) {
	if err := checkResponse(op, code, body); err != nil {
		return nil, false, err
	}
	var r struct {
		More     bool `json:"more"`
		Playlist []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			TrackCount int    `json:"trackCount"`
			CoverURL   string `json:"coverImgUrl"`
		} `json:"playlist"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, false, fmt.Errorf("%s: decode response: %w", op, err)
	}
	out := make([]Playlist, len(r.Playlist))
	for i, p := range r.Playlist {
		out[i] = Playlist{p.ID, p.Name, p.TrackCount, p.CoverURL}
	}
	return out, r.More, nil
}

type songJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Ar   []struct {
		Name string `json:"name"`
	} `json:"ar"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Al struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		PicURL string `json:"picUrl"`
	} `json:"al"`
	Album struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		PicURL string `json:"picUrl"`
	} `json:"album"`
	DT         int64 `json:"dt"`
	DurationMS int64 `json:"duration"`
}

func convertSong(s songJSON) Song {
	artists := s.Ar
	if len(artists) == 0 {
		artists = s.Artists
	}
	names := make([]string, len(artists))
	for i := range artists {
		names[i] = artists[i].Name
	}
	al := s.Al
	if al.ID == 0 && al.Name == "" {
		al = s.Album
	}
	d := s.DT
	if d == 0 {
		d = s.DurationMS
	}
	return Song{ID: s.ID, Title: s.Name, Artists: names, Album: al.Name, AlbumID: al.ID, Duration: time.Duration(d) * time.Millisecond, CoverURL: al.PicURL}
}

func parsePlaylistTracks(op string, code float64, body []byte) ([]Song, error) {
	return parseSongsAt(op, code, body, true)
}
func parseSearchSongs(op string, code float64, body []byte) ([]Song, error) {
	return parseSongsAt(op, code, body, false)
}
func parseSongDetails(op string, code float64, body []byte) ([]Song, error) {
	if err := checkResponse(op, code, body); err != nil {
		return nil, err
	}
	var r struct {
		Songs []songJSON `json:"songs"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", op, err)
	}
	out := make([]Song, len(r.Songs))
	for i := range r.Songs {
		out[i] = convertSong(r.Songs[i])
	}
	return out, nil
}
func parseSongsAt(op string, code float64, body []byte, playlist bool) ([]Song, error) {
	if err := checkResponse(op, code, body); err != nil {
		return nil, err
	}
	var r struct {
		Playlist struct {
			Tracks []songJSON `json:"tracks"`
		} `json:"playlist"`
		Result struct {
			Songs []songJSON `json:"songs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", op, err)
	}
	in := r.Result.Songs
	if playlist {
		in = r.Playlist.Tracks
	}
	out := make([]Song, len(in))
	for i := range in {
		out[i] = convertSong(in[i])
	}
	return out, nil
}

func parseURLResponse(op string, code float64, body []byte) (PlayableInfo, bool, error) {
	if err := checkResponse(op, code, body); err != nil {
		return PlayableInfo{}, false, err
	}
	var r struct {
		Data []struct {
			URL       string          `json:"url"`
			Type      string          `json:"type"`
			Size      int64           `json:"size"`
			FreeTrial json.RawMessage `json:"freeTrialInfo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return PlayableInfo{}, false, fmt.Errorf("%s: decode response: %w", op, err)
	}
	if len(r.Data) == 0 {
		return PlayableInfo{}, true, fmt.Errorf("%s: song is not playable (missing data)", op)
	}
	d := r.Data[0]
	trial := len(d.FreeTrial) > 0 && string(d.FreeTrial) != "null"
	if d.URL == "" || trial {
		return PlayableInfo{}, true, fmt.Errorf("%s: song is not playable (empty URL or trial-only)", op)
	}
	return PlayableInfo{URL: d.URL, Type: d.Type, Size: d.Size}, false, nil
}
