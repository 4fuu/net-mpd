package ncm

import (
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
