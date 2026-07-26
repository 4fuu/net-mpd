package mpd

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/4fuuu/net-mpd/internal/ncm"
)

type fakeMusic struct {
	song       ncm.Song
	cover      []byte
	resolveErr error
}

func (f *fakeMusic) Account() (ncm.User, error) { return ncm.User{ID: 1, Nickname: "测试"}, nil }
func (f *fakeMusic) UserPlaylists(int64) ([]ncm.Playlist, error) {
	return []ncm.Playlist{{ID: 2, Name: "中文 列表", TrackCount: 1}}, nil
}
func (f *fakeMusic) PlaylistTracks(int64) ([]ncm.Song, error)    { return []ncm.Song{f.song}, nil }
func (f *fakeMusic) SearchSongs(string, int) ([]ncm.Song, error) { return []ncm.Song{f.song}, nil }
func (f *fakeMusic) ResolveURL(int64) (ncm.PlayableInfo, error) {
	if f.resolveErr != nil {
		return ncm.PlayableInfo{}, f.resolveErr
	}
	return ncm.PlayableInfo{URL: "fake://audio"}, nil
}
func (f *fakeMusic) Cover(ncm.Song) ([]byte, error) { return f.cover, nil }

type fakePlayer struct{ end func() }

func (f *fakePlayer) Play(_ string, _ time.Duration, _ int, end func()) error {
	f.end = end
	return nil
}
func (f *fakePlayer) Stop() error  { return nil }
func (f *fakePlayer) Close() error { return nil }

func fixture(t *testing.T) (*Server, *fakeMusic) {
	t.Helper()
	m := &fakeMusic{song: ncm.Song{ID: 7, Title: "歌 曲", Artists: []string{"艺人"}, Album: "专辑", Duration: 3 * time.Minute}, cover: []byte{0, 1, 2, 3, 4}}
	c, e := NewCatalog(m)
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.PlaylistSongs("中文 列表")
	if e != nil {
		t.Fatal(e)
	}
	return NewServer(c, NewState(c, &fakePlayer{})), m
}
func connect(t *testing.T, s *Server) (net.Conn, *bufio.Reader) {
	t.Helper()
	a, b := net.Pipe()
	go s.handle(a)
	r := bufio.NewReader(b)
	line, e := r.ReadString('\n')
	if e != nil || line != "OK MPD 0.23.5\n" {
		t.Fatalf("greeting %q %v", line, e)
	}
	return b, r
}
func response(t *testing.T, c net.Conn, r *bufio.Reader, cmd string) string {
	t.Helper()
	_, _ = io.WriteString(c, cmd+"\n")
	var b strings.Builder
	for {
		line, e := r.ReadString('\n')
		if e != nil {
			t.Fatal(e)
		}
		b.WriteString(line)
		if line == "OK\n" || strings.HasPrefix(line, "ACK ") {
			return b.String()
		}
	}
}

func TestLex(t *testing.T) {
	a, e := Lex(`find Title "中文 \"歌\" \\ test"`)
	if e != nil {
		t.Fatal(e)
	}
	if len(a) != 3 || a[2] != `中文 "歌" \ test` {
		t.Fatalf("%q", a)
	}
}
func TestQueueFramingAndBinary(t *testing.T) {
	s, _ := fixture(t)
	c, r := connect(t, s)
	defer c.Close()
	if got := response(t, c, r, `add "netease://song/7"`); got != "OK\n" {
		t.Fatal(got)
	}
	got := response(t, c, r, "playlistinfo")
	want := "file: netease://song/7\nId: 1\nTitle: 歌 曲\nArtist: 艺人\nAlbum: 专辑\nduration: 180.000\n"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("%q", got)
	}
	got = response(t, c, r, "command_list_ok_begin\nping\nstatus\ncommand_list_end")
	if !strings.Contains(got, "list_OK\npartition: default") || !strings.HasSuffix(got, "list_OK\nOK\n") {
		t.Fatalf("%q", got)
	}
	_ = response(t, c, r, "binarylimit 2")
	_, _ = io.WriteString(c, "albumart netease://song/7 1\n")
	head, _ := r.ReadString('\n')
	typ, _ := r.ReadString('\n')
	bin, _ := r.ReadString('\n')
	raw := make([]byte, 3)
	_, _ = io.ReadFull(r, raw)
	ok, _ := r.ReadString('\n')
	if head != "size: 5\n" || typ != "type: image/jpeg\n" || bin != "binary: 2\n" || string(raw) != string([]byte{1, 2, '\n'}) || ok != "OK\n" {
		t.Fatalf("binary %q %q %q %v %q", head, typ, bin, raw, ok)
	}
}
func TestIdleNoIdleAndEvent(t *testing.T) {
	s, _ := fixture(t)
	c, r := connect(t, s)
	defer c.Close()
	_, _ = io.WriteString(c, "idle player\nnoidle\n")
	if x, _ := r.ReadString('\n'); x != "OK\n" {
		t.Fatal(x)
	}
	_, _ = io.WriteString(c, "idle mixer\n")
	time.Sleep(10 * time.Millisecond)
	s.State.SetVolume(42)
	if x, _ := r.ReadString('\n'); x != "changed: mixer\n" {
		t.Fatal(x)
	}
	if x, _ := r.ReadString('\n'); x != "OK\n" {
		t.Fatal(x)
	}
}

func TestIdleRetainsEventBetweenIdleCalls(t *testing.T) {
	s, _ := fixture(t)
	c, r := connect(t, s)
	defer c.Close()
	_, _ = io.WriteString(c, "idle\nnoidle\n")
	if got, _ := r.ReadString('\n'); got != "OK\n" {
		t.Fatal(got)
	}
	if got := response(t, c, r, `add "netease://song/7"`); got != "OK\n" {
		t.Fatal(got)
	}
	_, _ = io.WriteString(c, "idle playlist\n")
	if got, _ := r.ReadString('\n'); got != "changed: playlist\n" {
		t.Fatal(got)
	}
	if got, _ := r.ReadString('\n'); got != "OK\n" {
		t.Fatal(got)
	}
}

func TestIdleCoalescingDoesNotDropSubsystems(t *testing.T) {
	s, _ := fixture(t)
	c, r := connect(t, s)
	defer c.Close()
	for i := 0; i < 12; i++ {
		s.State.Add(ncm.Song{ID: int64(i + 1)})
	}
	s.State.Stop()
	_, _ = io.WriteString(c, "idle player\n")
	if got, _ := r.ReadString('\n'); got != "changed: player\n" {
		t.Fatal(got)
	}
	if got, _ := r.ReadString('\n'); got != "OK\n" {
		t.Fatal(got)
	}
}

func TestPositionedQueueOperations(t *testing.T) {
	s, _ := fixture(t)
	c, r := connect(t, s)
	defer c.Close()
	_ = response(t, c, r, `add "netease://song/7"`)
	_ = response(t, c, r, `add "netease://song/7"`)
	_ = response(t, c, r, `add "netease://song/7" 1`)
	if got := len(s.State.Snapshot().Queue); got != 3 {
		t.Fatalf("queue length = %d", got)
	}
	_ = response(t, c, r, `moveid 3 0`)
	if got := s.State.Snapshot().Queue[0].ID; got != 3 {
		t.Fatalf("first queue ID = %d", got)
	}
	_ = response(t, c, r, `delete "0:2"`)
	if got := len(s.State.Snapshot().Queue); got != 1 {
		t.Fatalf("queue length after range delete = %d", got)
	}
}

func TestGroupedList(t *testing.T) {
	s, _ := fixture(t)
	c, r := connect(t, s)
	defer c.Close()
	got := response(t, c, r, `list Album group Artist`)
	if !strings.Contains(got, "Album: 专辑\nArtist: 艺人\n") {
		t.Fatalf("grouped list response %q", got)
	}
}

func TestRelativePositionAndMoveRange(t *testing.T) {
	st := Status{Current: 2, Queue: make([]QueueItem, 5)}
	for input, want := range map[string]int{"+0": 3, "+1": 4, "-0": 2, "-1": 1, "5": 5} {
		got, err := queuePosition(input, st)
		if err != nil || got != want {
			t.Fatalf("queuePosition(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	s, _ := fixture(t)
	for i := 0; i < 4; i++ {
		s.State.Add(ncm.Song{ID: int64(i + 1)})
	}
	if err := s.State.MoveRange(1, 3, 0); err != nil {
		t.Fatal(err)
	}
	got := s.State.Snapshot().Queue
	if got[0].Song.ID != 2 || got[1].Song.ID != 3 || got[2].Song.ID != 1 || got[3].Song.ID != 4 {
		t.Fatalf("unexpected queue order: %#v", got)
	}
	moveState := Status{Current: 4, Queue: make([]QueueItem, 6)}
	if got, err := movePosition("+0", moveState, 1, 3); err != nil || got != 3 {
		t.Fatalf("move position = %d, %v; want 3", got, err)
	}
	if _, err := movePosition("+0", Status{Current: 1, Queue: make([]QueueItem, 6)}, 1, 3); err == nil {
		t.Fatal("relative move with current in source range should fail")
	}
}

func TestFailedPlayDoesNotInvalidateActivePlayer(t *testing.T) {
	s, music := fixture(t)
	s.State.Add(music.song)
	if err := s.State.Play(0); err != nil {
		t.Fatal(err)
	}
	backend := s.State.player.(*fakePlayer)
	activeEnd := backend.end
	music.resolveErr = errors.New("unavailable")
	if err := s.State.Play(0); err == nil {
		t.Fatal("expected unavailable song error")
	}
	activeEnd()
	if got := s.State.Snapshot().State; got != "stop" {
		t.Fatalf("active player callback was invalidated; state = %s", got)
	}
}
