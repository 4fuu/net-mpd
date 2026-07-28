package ncm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cookiejar "github.com/juju/persistent-cookiejar"
)

func TestParseAccount(t *testing.T) {
	u, err := parseAccount("account", 200, []byte(`{"code":200,"account":{"id":42},"profile":{"userId":42,"nickname":"fox"}}`))
	if err != nil || u.ID != 42 || u.Nickname != "fox" {
		t.Fatalf("got %#v, %v", u, err)
	}
}

func TestParsePlaylist(t *testing.T) {
	p, more, err := parsePlaylists("playlists", 200, []byte(`{"code":200,"more":true,"playlist":[{"id":1,"name":"list","trackCount":3,"coverImgUrl":"cover"}]}`))
	if err != nil || !more || len(p) != 1 || p[0].TrackCount != 3 {
		t.Fatalf("got %#v, %v, %v", p, more, err)
	}
}

func TestParseCreatedPlaylist(t *testing.T) {
	for _, tc := range []struct {
		body string
		name string
		id   int64
	}{
		{`{"playlist":{"id":42,"name":"server name"}}`, "server name", 42},
		{`{"id":43}`, "requested name", 43},
	} {
		got, err := parseCreatedPlaylist("requested name", []byte(tc.body))
		if err != nil || got.ID != tc.id || got.Name != tc.name {
			t.Fatalf("parseCreatedPlaylist(%s) = %#v, %v", tc.body, got, err)
		}
	}
	if _, err := parseCreatedPlaylist("name", []byte(`{"playlist":{}}`)); err == nil {
		t.Fatal("missing playlist ID was accepted")
	}
}

func TestParseSpecialModeSongs(t *testing.T) {
	daily, err := parseDailyRecommendSongs("daily", 200, []byte(`{"code":200,"data":{"dailySongs":[{"id":1,"name":"d","ar":[{"name":"a"}],"al":{"id":2,"name":"al","picUrl":"p"},"dt":1000}]}}`))
	if err != nil || len(daily) != 1 || daily[0].ID != 1 || daily[0].Artists[0] != "a" {
		t.Fatalf("daily: %#v %v", daily, err)
	}
	fm, err := parsePersonalFM("fm", 200, []byte(`{"code":200,"data":[{"id":3,"name":"f","artists":[{"name":"b"}],"album":{"id":4,"name":"alb","picUrl":"p"},"duration":2000}]}`))
	if err != nil || len(fm) != 1 || fm[0].Duration != 2000*time.Millisecond || fm[0].Album != "alb" {
		t.Fatalf("fm: %#v %v", fm, err)
	}
	recent, err := parseRecentSongs("recent", 200, []byte(`{"code":200,"data":{"list":[{"resourceType":"SONG","data":{"id":5,"name":"r","ar":[{"name":"c"}],"al":{"id":6,"name":"x"},"dt":3000}},{"resourceType":"VOICE","data":{"id":99,"name":"skip"}}]}}`))
	if err != nil || len(recent) != 1 || recent[0].ID != 5 {
		t.Fatalf("recent: %#v %v", recent, err)
	}
	cloud, more, err := parseCloudSongs("cloud", 200, []byte(`{"code":200,"hasMore":true,"data":[{"songId":7,"songName":"fallback","simpleSong":{"id":7,"name":"cloud","ar":[{"name":"d"}],"al":{"id":8,"name":"y","picUrl":"p"},"dt":4000}}]}`))
	if err != nil || !more || len(cloud) != 1 || cloud[0].Title != "cloud" || cloud[0].Artists[0] != "d" {
		t.Fatalf("cloud: %#v more=%v err=%v", cloud, more, err)
	}
	// unmatched cloud entry without simpleSong fields still uses top-level ids
	cloud2, _, err := parseCloudSongs("cloud", 200, []byte(`{"code":200,"hasMore":false,"data":[{"songId":11,"songName":"only-top"}]}`))
	if err != nil || len(cloud2) != 1 || cloud2[0].ID != 11 || cloud2[0].Title != "only-top" {
		t.Fatalf("cloud fallback: %#v %v", cloud2, err)
	}
	intel, err := parseIntelligenceSongs("intel", 200, []byte(`{"code":200,"data":[{"songInfo":{"id":9,"name":"i","ar":[{"name":"e"}],"al":{"id":10,"name":"z","picUrl":"p"},"dt":5000}}]}`))
	if err != nil || len(intel) != 1 || intel[0].ID != 9 || intel[0].Duration != 5000*time.Millisecond {
		t.Fatalf("intelligence: %#v %v", intel, err)
	}
}

func TestParseSongShapes(t *testing.T) {
	short, err := parsePlaylistTracks("tracks", 200, []byte(`{"code":200,"playlist":{"tracks":[{"id":1,"name":"short","ar":[{"name":"a"}],"al":{"id":2,"name":"album","picUrl":"pic"},"dt":1234}]}}`))
	if err != nil || short[0].Duration != 1234*time.Millisecond || short[0].Artists[0] != "a" {
		t.Fatalf("short: %#v %v", short, err)
	}
	long, err := parseSearchSongs("search", 200, []byte(`{"code":200,"result":{"songs":[{"id":3,"name":"long","artists":[{"name":"b"}],"album":{"id":4,"name":"other","picUrl":"pic2"},"duration":5678}]}}`))
	if err != nil || long[0].AlbumID != 4 || long[0].Duration != 5678*time.Millisecond {
		t.Fatalf("long: %#v %v", long, err)
	}
}

func TestFetchPlaylistTracksUsesWeapiAndBatchesSongDetails(t *testing.T) {
	trackIDs := make([]map[string]int64, songDetailBatch+1)
	for i := range trackIDs {
		trackIDs[i] = map[string]int64{"id": int64(i + 1)}
	}
	detail, err := json.Marshal(map[string]interface{}{
		"code":     200,
		"playlist": map[string]interface{}{"trackIds": trackIDs},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	call := func(api string, data map[string]interface{}) (float64, []byte, error) {
		calls++
		if calls == 1 {
			if api != playlistDetailAPI || data["id"] != "42" {
				t.Fatalf("detail call = %q, %#v", api, data)
			}
			return 200, detail, nil
		}
		if api != songDetailAPI {
			t.Fatalf("song detail API = %q", api)
		}
		want := songDetailBatch
		start := 1
		if calls == 3 {
			want = 1
			start = songDetailBatch + 1
		}
		songs := make([]map[string]interface{}, want)
		for i := range songs {
			id := start + i
			songs[i] = map[string]interface{}{
				"id":   id,
				"name": fmt.Sprintf("song-%d", id),
				"ar":   []map[string]string{{"name": "artist"}},
				"al":   map[string]interface{}{"id": 9, "name": "album"},
				"dt":   1000,
			}
		}
		body, err := json.Marshal(map[string]interface{}{"code": 200, "songs": songs})
		return 200, body, err
	}

	songs, err := fetchPlaylistTracks(42, call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(songs) != songDetailBatch+1 || songs[len(songs)-1].ID != songDetailBatch+1 {
		t.Fatalf("calls=%d songs=%d last=%#v", calls, len(songs), songs[len(songs)-1])
	}
}

func TestFetchPlaylistTracksUsesEmbeddedTracksAndKnownCache(t *testing.T) {
	detail, err := json.Marshal(map[string]interface{}{
		"code": 200,
		"playlist": map[string]interface{}{
			"trackIds": []map[string]int64{{"id": 1}, {"id": 2}, {"id": 3}},
			"tracks": []map[string]interface{}{
				{"id": 1, "name": "a", "ar": []map[string]string{{"name": "x"}}, "al": map[string]interface{}{"id": 9, "name": "al"}, "dt": 1000},
				{"id": 2, "name": "b", "ar": []map[string]string{{"name": "y"}}, "al": map[string]interface{}{"id": 9, "name": "al"}, "dt": 1000},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	call := func(api string, _ map[string]interface{}) (float64, []byte, error) {
		calls++
		if calls == 1 {
			return 200, detail, nil
		}
		if api != songDetailAPI {
			t.Fatalf("unexpected api %s", api)
		}
		return 200, []byte(`{"code":200,"songs":[{"id":3,"name":"c","ar":[{"name":"z"}],"al":{"id":9,"name":"al"},"dt":1000}]}`), nil
	}
	// id 3 is missing from embedded tracks → one song/detail call.
	songs, err := fetchPlaylistTracks(1, call, nil)
	if err != nil || calls != 2 || len(songs) != 3 || songs[2].Title != "c" {
		t.Fatalf("embedded partial: calls=%d songs=%#v err=%v", calls, songs, err)
	}
	// known cache supplies id 3 → detail only, no song/detail.
	calls = 0
	songs, err = fetchPlaylistTracks(1, call, map[int64]Song{3: {ID: 3, Title: "cached"}})
	if err != nil || calls != 1 || len(songs) != 3 || songs[2].Title != "cached" {
		t.Fatalf("known cache: calls=%d songs=%#v err=%v", calls, songs, err)
	}
}

func TestFetchPlaylistTracksReturnsBusinessErrorWithoutRetry(t *testing.T) {
	calls := 0
	_, err := fetchPlaylistTracks(42, func(string, map[string]interface{}) (float64, []byte, error) {
		calls++
		return 405, []byte(`{"code":405,"message":"too frequent"}`), nil
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "code 405") || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestParseURLResponse(t *testing.T) {
	p, fallback, err := parseURLResponse("url", 200, []byte(`{"code":200,"data":[{"url":"https://example/music","type":"mp3","size":99,"freeTrialInfo":null}]}`))
	if err != nil || fallback || p.Size != 99 {
		t.Fatalf("got %#v, %v, %v", p, fallback, err)
	}
	_, fallback, err = parseURLResponse("url", 200, []byte(`{"code":200,"data":[{"url":null,"freeTrialInfo":null}]}`))
	if err == nil || !fallback || !strings.Contains(err.Error(), "not playable") {
		t.Fatalf("got fallback=%v err=%v", fallback, err)
	}
}

func TestOpenCreatesAndSavesCookieJar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "cookie")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		t.Fatalf("cookie file was not created: %v", err)
	}
}

func TestPersistentCookieJarCopyCompatibility(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source-cookie")
	source, err := cookiejar.New(&cookiejar.Options{Filename: sourcePath})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://music.163.com")
	source.SetCookies(u, []*http.Cookie{{
		Name:    "MUSIC_U",
		Value:   "test-session",
		Path:    "/",
		Expires: time.Now().Add(time.Hour),
	}})
	if err := source.Save(); err != nil {
		t.Fatal(err)
	}
	destinationPath := filepath.Join(t.TempDir(), "destination", "cookie")
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0700); err != nil {
		t.Fatal(err)
	}
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destinationPath, sourceBytes, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cookie := range reopened.jar.Cookies(u) {
		found = found || cookie.Name == "MUSIC_U" && cookie.Value == "test-session"
	}
	if !found {
		t.Fatal("copied MUSIC_U cookie was not found")
	}
}

func TestClearLocalCredentialsReportsRemovalFailure(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "cookie"))
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "not-a-cookie-file")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "child"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	c.cookiePath = blocked
	if err := c.clearLocalCredentials(); err == nil {
		t.Fatal("expected local credential removal to fail")
	}
}

func TestResponseMessage(t *testing.T) {
	if got := responseMessage([]byte(`{"code":801,"message":"waiting"}`)); got != "waiting" {
		t.Fatalf("message = %q", got)
	}
	if got := responseMessage([]byte(`{"code":800,"msg":"expired"}`)); got != "expired" {
		t.Fatalf("msg = %q", got)
	}
}

func TestMusicfoxSession(t *testing.T) {
	cookiePath := os.Getenv("MUSICFOX_COOKIE_FILE")
	if cookiePath == "" {
		t.Skip("MUSICFOX_COOKIE_FILE is not set")
	}
	c, err := Open(cookiePath)
	if err != nil {
		t.Fatal(err)
	}
	user, err := c.Account()
	if err != nil {
		t.Fatal(err)
	}
	playlists, err := c.UserPlaylists(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(playlists) == 0 {
		t.Fatal("logged-in account has no playlists")
	}
	songs, err := c.PlaylistTracks(playlists[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(songs) == 0 {
		t.Fatal("first playlist has no tracks")
	}
	playable, err := c.ResolveURL(songs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if playable.URL == "" {
		t.Fatal("resolved an empty playback URL")
	}
}
