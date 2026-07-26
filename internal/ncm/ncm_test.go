package ncm

import (
	"os"
	"strings"
	"testing"
	"time"
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
}
