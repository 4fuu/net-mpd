package ncm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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

type Client struct {
	jar        *cookiejar.Jar
	httpClient *http.Client
}

var sdkMu sync.Mutex

func Open(cookiePath string) (*Client, error) {
	info, err := os.Stat(cookiePath)
	if err != nil {
		return nil, fmt.Errorf("open cookie jar %q: %w", cookiePath, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("open cookie jar %q: path is a directory", cookiePath)
	}
	jar, err := cookiejar.New(&cookiejar.Options{Filename: cookiePath})
	if err != nil {
		return nil, fmt.Errorf("load cookie jar %q: %w", cookiePath, err)
	}
	return &Client{jar: jar, httpClient: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (c *Client) call(fn func() (float64, []byte)) (float64, []byte) {
	sdkMu.Lock()
	defer sdkMu.Unlock()
	neteaseutil.SetGlobalCookieJar(c.jar)
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
	s := &service.PlaylistTrackAllService{Id: strconv.FormatInt(id, 10), S: "0"}
	code, body := c.call(s.AllTracks)
	return parsePlaylistTracks("playlist tracks", code, body)
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
	v1 := &service.SongUrlV1Service{ID: id, Level: service.Higher}
	sdkMu.Lock()
	neteaseutil.SetGlobalCookieJar(c.jar)
	code, body, callErr := v1.SongUrl()
	sdkMu.Unlock()
	if callErr != nil {
		return PlayableInfo{}, fmt.Errorf("resolve song URL v1: %w", callErr)
	}
	info, fallback, err := parseURLResponse("resolve song URL v1", code, body)
	if err != nil && !fallback {
		return PlayableInfo{}, err
	}
	if !fallback {
		return info, nil
	}
	legacy := &service.SongUrlService{ID: id, Br: "320000"}
	code, body = c.call(legacy.SongUrl)
	info, _, err = parseURLResponse("resolve song URL fallback", code, body)
	return info, err
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
