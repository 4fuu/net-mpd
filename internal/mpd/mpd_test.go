package mpd

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4fuu/net-mpd/internal/ncm"
)

type fakeMusic struct {
	mu         sync.Mutex
	song       ncm.Song
	cover      []byte
	resolveErr error
	playlists  []ncm.Playlist
	tracks     map[int64][]ncm.Song
	nextID     int64
	mutations  []string
	trackCalls int
	trackHit   chan struct{}
	trackGo    chan struct{}
	resolveHit chan struct{}
	resolveGo  chan struct{}
}

func (f *fakeMusic) Account() (ncm.User, error) { return ncm.User{ID: 1, Nickname: "测试"}, nil }
func (f *fakeMusic) UserPlaylists(int64) ([]ncm.Playlist, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ncm.Playlist(nil), f.playlists...), nil
}
func (f *fakeMusic) PlaylistTracks(id int64) ([]ncm.Song, error) {
	return f.PlaylistTracksKnown(id, nil)
}
func (f *fakeMusic) PlaylistTracksKnown(id int64, known map[int64]ncm.Song) ([]ncm.Song, error) {
	f.mu.Lock()
	f.trackCalls++
	hit, proceed := f.trackHit, f.trackGo
	if hit != nil {
		f.trackHit = nil
		f.trackGo = nil
	}
	tracks := append([]ncm.Song(nil), f.tracks[id]...)
	f.mu.Unlock()
	if hit != nil {
		close(hit)
		<-proceed
	}
	if len(known) == 0 {
		return tracks, nil
	}
	// Mirror production: prefer caller-known metadata when IDs match.
	out := make([]ncm.Song, len(tracks))
	for i, s := range tracks {
		if k, ok := known[s.ID]; ok {
			out[i] = k
		} else {
			out[i] = s
		}
	}
	return out, nil
}
func (f *fakeMusic) SearchSongs(string, int) ([]ncm.Song, error) { return []ncm.Song{f.song}, nil }
func (f *fakeMusic) DailyRecommendSongs() ([]ncm.Song, error) {
	return []ncm.Song{{ID: 101, Title: "daily", Duration: time.Minute}}, nil
}
func (f *fakeMusic) PersonalFM() ([]ncm.Song, error) {
	return []ncm.Song{{ID: 102, Title: "fm", Duration: time.Minute}}, nil
}
func (f *fakeMusic) RecentSongs(int) ([]ncm.Song, error) {
	return []ncm.Song{{ID: 103, Title: "recent", Duration: time.Minute}}, nil
}
func (f *fakeMusic) CloudSongs() ([]ncm.Song, error) {
	return []ncm.Song{{ID: 104, Title: "cloud", Duration: time.Minute}}, nil
}
func (f *fakeMusic) IntelligenceList(songID, playlistID int64) ([]ncm.Song, error) {
	return []ncm.Song{
		{ID: 201, Title: "intel-a", Duration: time.Minute},
		{ID: 202, Title: "intel-b", Duration: time.Minute},
	}, nil
}
func (f *fakeMusic) ResolveURL(int64) (ncm.PlayableInfo, error) {
	f.mu.Lock()
	err, hit, proceed := f.resolveErr, f.resolveHit, f.resolveGo
	if hit != nil {
		f.resolveHit = nil
		f.resolveGo = nil
	}
	f.mu.Unlock()
	if hit != nil {
		close(hit)
		<-proceed
	}
	if err != nil {
		return ncm.PlayableInfo{}, err
	}
	return ncm.PlayableInfo{URL: "fake://audio"}, nil
}
func (f *fakeMusic) Cover(ncm.Song) ([]byte, error) { return f.cover, nil }
func (f *fakeMusic) Lyrics(int64) (ncm.Lyrics, error) {
	return ncm.Lyrics{Original: "[00:00.00]test lyric\n[00:05.00]line two\n"}, nil
}
func (f *fakeMusic) CreatePlaylist(name string) (ncm.Playlist, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	p := ncm.Playlist{ID: f.nextID, Name: name}
	f.playlists = append(f.playlists, p)
	f.tracks[p.ID] = nil
	f.mutations = append(f.mutations, "create "+name)
	return p, nil
}
func (f *fakeMusic) RenamePlaylist(id int64, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.playlists {
		if f.playlists[i].ID == id {
			f.playlists[i].Name = name
			f.mutations = append(f.mutations, "rename "+name)
			return nil
		}
	}
	return errors.New("playlist not found")
}
func (f *fakeMusic) DeletePlaylist(id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.playlists {
		if f.playlists[i].ID == id {
			f.playlists = append(f.playlists[:i], f.playlists[i+1:]...)
			delete(f.tracks, id)
			f.mutations = append(f.mutations, "rm")
			return nil
		}
	}
	return errors.New("playlist not found")
}
func (f *fakeMusic) AddPlaylistTracks(id int64, ids []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sid := range ids {
		if sid == f.song.ID {
			f.tracks[id] = append(f.tracks[id], f.song)
		} else {
			return errors.New("song not found")
		}
	}
	f.mutations = append(f.mutations, "add")
	return nil
}
func (f *fakeMusic) DeletePlaylistTracks(id int64, ids []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sid := range ids {
		for i, s := range f.tracks[id] {
			if s.ID == sid {
				f.tracks[id] = append(f.tracks[id][:i], f.tracks[id][i+1:]...)
				break
			}
		}
	}
	f.mutations = append(f.mutations, "delete")
	return nil
}

type fakePlayer struct {
	end      func(error)
	position time.Duration
}

func (f *fakePlayer) Play(_, _ string, offset time.Duration, _ int, end func(error)) error {
	f.end = end
	f.position = offset
	return nil
}
func (f *fakePlayer) Pause() error                { return nil }
func (f *fakePlayer) Resume() error               { return nil }
func (f *fakePlayer) Seek(at time.Duration) error { f.position = at; return nil }
func (f *fakePlayer) SetVolume(int) error         { return nil }
func (f *fakePlayer) Position() time.Duration     { return f.position }
func (f *fakePlayer) Stop() error                 { f.end = nil; return nil }
func (f *fakePlayer) Close() error                { return nil }

type transportBarrierPlayer struct {
	operation string
	entered   chan struct{}
	release   chan struct{}
	position  time.Duration
}

func (p *transportBarrierPlayer) wait(operation string) {
	if p.operation == operation {
		close(p.entered)
		<-p.release
	}
}
func (p *transportBarrierPlayer) Play(_, _ string, offset time.Duration, _ int, _ func(error)) error {
	p.position = offset
	return nil
}
func (p *transportBarrierPlayer) Pause() error  { p.wait("pause"); return nil }
func (p *transportBarrierPlayer) Resume() error { p.wait("resume"); return nil }
func (p *transportBarrierPlayer) Seek(at time.Duration) error {
	p.wait("seek")
	p.position = at
	return nil
}
func (p *transportBarrierPlayer) SetVolume(int) error     { return nil }
func (p *transportBarrierPlayer) Position() time.Duration { return p.position }
func (p *transportBarrierPlayer) Stop() error             { return nil }
func (p *transportBarrierPlayer) Close() error            { return nil }

type blockingPlayer struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	active  bool
}

func (p *blockingPlayer) Play(_, _ string, _ time.Duration, _ int, _ func(error)) error {
	close(p.started)
	<-p.release
	p.mu.Lock()
	p.active = true
	p.mu.Unlock()
	return nil
}
func (p *blockingPlayer) Pause() error             { return nil }
func (p *blockingPlayer) Resume() error            { return nil }
func (p *blockingPlayer) Seek(time.Duration) error { return nil }
func (p *blockingPlayer) SetVolume(int) error      { return nil }
func (p *blockingPlayer) Position() time.Duration  { return 0 }
func (p *blockingPlayer) Stop() error {
	p.mu.Lock()
	p.active = false
	p.mu.Unlock()
	return nil
}
func (p *blockingPlayer) Close() error { return nil }
func (p *blockingPlayer) isActive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

func fixture(t *testing.T) (*Server, *fakeMusic) {
	t.Helper()
	song := ncm.Song{ID: 7, Title: "歌 曲", Artists: []string{"艺人"}, Album: "专辑", Duration: 3 * time.Minute}
	m := &fakeMusic{song: song, cover: []byte{0, 1, 2, 3, 4}, playlists: []ncm.Playlist{{ID: 2, Name: "中文 列表", TrackCount: 1}, {ID: 3, Name: "other", TrackCount: 1}}, tracks: map[int64][]ncm.Song{2: {song}, 3: {{ID: 8, Title: "Other", Duration: time.Minute}}}, nextID: 3}
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

func TestPlaylistSongsCoalescesConcurrentLoads(t *testing.T) {
	trackHit := make(chan struct{})
	trackGo := make(chan struct{})
	m := &fakeMusic{
		playlists: []ncm.Playlist{{ID: 2, Name: "list", TrackCount: 1}},
		tracks:    map[int64][]ncm.Song{2: {{ID: 7, Title: "song"}}},
		trackHit:  trackHit,
		trackGo:   trackGo,
	}
	catalog, err := NewCatalog(m)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() {
		_, err := catalog.PlaylistSongs("list")
		errs <- err
	}()
	<-trackHit
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		_, err := catalog.PlaylistSongs("list")
		errs <- err
	}()
	<-secondStarted
	time.Sleep(10 * time.Millisecond)
	close(trackGo)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	calls := m.trackCalls
	m.mu.Unlock()
	if calls != 1 {
		t.Fatalf("PlaylistTracks calls = %d, want 1", calls)
	}
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

func TestPasswordAuthenticationIsPerConnection(t *testing.T) {
	s, _ := fixture(t)
	s.SetPassword("secret")
	c1, r1 := connect(t, s)
	defer c1.Close()
	c2, r2 := connect(t, s)
	defer c2.Close()
	if got := response(t, c1, r1, "status"); !strings.HasPrefix(got, "ACK [4@0]") {
		t.Fatalf("unauthenticated status = %q", got)
	}
	if got := response(t, c1, r1, "command_list_begin"); !strings.HasPrefix(got, "ACK [4@0]") {
		t.Fatalf("command list bypassed authentication: %q", got)
	}
	if got := response(t, c1, r1, "password wrong"); !strings.HasPrefix(got, "ACK [3@0]") {
		t.Fatalf("wrong password = %q", got)
	}
	if got := response(t, c1, r1, "password secret"); got != "OK\n" {
		t.Fatal(got)
	}
	if got := response(t, c1, r1, "ping"); got != "OK\n" {
		t.Fatal(got)
	}
	if got := response(t, c2, r2, "ping"); !strings.HasPrefix(got, "ACK [4@0]") {
		t.Fatalf("authentication leaked between connections: %q", got)
	}
	if got := response(t, c1, r1, "password secret extra"); !strings.HasPrefix(got, "ACK [2@0]") {
		t.Fatalf("extra password argument = %q", got)
	}
}

func TestOutputsProtocolAndPlaybackGating(t *testing.T) {
	s, music := fixture(t)
	c, r := connect(t, s)
	defer c.Close()
	if got := response(t, c, r, "outputs"); !strings.Contains(got, "outputid: 0\n") || !strings.Contains(got, "outputenabled: 1\n") {
		t.Fatalf("outputs = %q", got)
	}
	if got := response(t, c, r, "disableoutput 9"); !strings.HasPrefix(got, "ACK [50@0]") {
		t.Fatalf("invalid output = %q", got)
	}
	_ = response(t, c, r, `add "netease://song/7"`)
	if got := response(t, c, r, "play"); got != "OK\n" {
		t.Fatal(got)
	}
	end := s.State.player.(*fakePlayer).end
	if got := response(t, c, r, "disableoutput 0"); got != "OK\n" {
		t.Fatal(got)
	}
	for _, cmd := range []string{"play", "playid 1", "next", "previous", "pause 0"} {
		if got := response(t, c, r, cmd); cmd != "pause 0" && !strings.HasPrefix(got, "ACK [50@0]") {
			t.Errorf("disabled %s = %q", cmd, got)
		}
	}
	// A completion callback already handed to the backend cannot auto-advance.
	end(nil)
	if s.State.Snapshot().State != "stop" {
		t.Fatal("natural completion started playback while output was disabled")
	}
	if got := response(t, c, r, "enableoutput 0"); got != "OK\n" {
		t.Fatal(got)
	}
	if got := response(t, c, r, "play"); got != "OK\n" {
		t.Fatal(got)
	}
	_ = music
	if got := response(t, c, r, "outputs extra"); !strings.HasPrefix(got, "ACK [2@0]") {
		t.Fatalf("outputs extra = %q", got)
	}
}

func TestOutputAndStickerIdleAndStickerProtocol(t *testing.T) {
	s, _ := fixture(t)
	if err := s.SetStickerPath(filepath.Join(t.TempDir(), "stickers.json")); err != nil {
		t.Fatal(err)
	}
	c, r := connect(t, s)
	defer c.Close()
	commands := []string{
		`sticker set song "netease://song/7" rating 9.5`,
		`sticker set song "netease://song/7" note "good song"`,
	}
	for _, cmd := range commands {
		if got := response(t, c, r, cmd); got != "OK\n" {
			t.Fatalf("%s: %q", cmd, got)
		}
	}
	if got := response(t, c, r, `sticker get song "netease://song/7" rating`); got != "sticker: rating=9.5\nOK\n" {
		t.Fatal(got)
	}
	if got := response(t, c, r, `sticker list song "netease://song/7"`); !strings.Contains(got, "sticker: note=good song\n") {
		t.Fatal(got)
	}
	for _, op := range []string{`> 9`, `>= 9.5`, `!= 8`, `contains .5`} {
		if got := response(t, c, r, `sticker find song "" rating `+op); !strings.Contains(got, "file: netease://song/7\n") {
			t.Errorf("operator %s: %q", op, got)
		}
	}
	for _, cmd := range []string{`sticker get album "netease://song/7" rating`, `sticker get song bad rating`, `sticker set song "netease://song/7" "" value`, `sticker get song "netease://song/7" rating extra`} {
		if got := response(t, c, r, cmd); !strings.HasPrefix(got, "ACK ") {
			t.Errorf("invalid %s = %q", cmd, got)
		}
	}
	_, _ = io.WriteString(c, "idle sticker\n")
	time.Sleep(time.Millisecond)
	s.Stickers.Set("netease://song/7", "x", "y")
	s.State.Notify("sticker")
	if got, _ := r.ReadString('\n'); got != "changed: sticker\n" {
		t.Fatal(got)
	}
	if got, _ := r.ReadString('\n'); got != "OK\n" {
		t.Fatal(got)
	}
	if got := response(t, c, r, `sticker delete song "netease://song/7" rating`); got != "OK\n" {
		t.Fatal(got)
	}
	if got := response(t, c, r, `sticker delete song "netease://song/7"`); got != "OK\n" {
		t.Fatal(got)
	}
	_, _ = io.WriteString(c, "idle output\n")
	time.Sleep(time.Millisecond)
	c2, r2 := connect(t, s)
	defer c2.Close()
	if got := response(t, c2, r2, "disableoutput 0"); got != "OK\n" {
		t.Fatal(got)
	}
	if got, _ := r.ReadString('\n'); got != "changed: output\n" {
		t.Fatal(got)
	}
	if got, _ := r.ReadString('\n'); got != "OK\n" {
		t.Fatal(got)
	}
}

func TestIntrospectionAndClearErrorProtocol(t *testing.T) {
	s, _ := fixture(t)
	c, r := connect(t, s)
	defer c.Close()
	if got := response(t, c, r, "notcommands"); got != "OK\n" {
		t.Fatalf("notcommands = %q", got)
	}
	for cmd, field := range map[string]string{"tagtypes": "tagtype: Artist\n", "urlhandlers": "handler: http\n", "decoders": "plugin: native\n"} {
		if got := response(t, c, r, cmd); !strings.Contains(got, field) {
			t.Errorf("%s = %q", cmd, got)
		}
		if got := response(t, c, r, cmd+" extra"); !strings.HasPrefix(got, "ACK [2@0]") {
			t.Errorf("%s extra = %q", cmd, got)
		}
	}
	s.State.mu.Lock()
	s.State.playbackError = "boom"
	s.State.mu.Unlock()
	sub, cancel := s.State.Subscribe()
	defer cancel()
	if got := response(t, c, r, "clearerror"); got != "OK\n" {
		t.Fatal(got)
	}
	if pending := s.State.takePending(sub, nil); len(pending) != 1 || pending[0] != "player" {
		t.Fatalf("clear event = %v", pending)
	}
	_ = response(t, c, r, "clearerror")
	if pending := s.State.takePending(sub, nil); len(pending) != 0 {
		t.Fatalf("unchanged clear event = %v", pending)
	}
	if got := response(t, c, r, "clearerror extra"); !strings.HasPrefix(got, "ACK [2@0]") {
		t.Fatal(got)
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
	// Group tags must precede the primary tag for rmpc's grouped-list parser.
	if !strings.Contains(got, "Artist: 艺人\nAlbum: 专辑\n") {
		t.Fatalf("grouped list response %q", got)
	}
}

func TestPlaylistSlashNamesAreSafeForRMPC(t *testing.T) {
	song := ncm.Song{ID: 9, Title: "slash song", Artists: []string{"a"}, Album: "b", Duration: time.Minute}
	m := &fakeMusic{
		song:      song,
		playlists: []ncm.Playlist{{ID: 2, Name: "25/05", TrackCount: 1}, {ID: 3, Name: "23/11/16", TrackCount: 1}},
		tracks:    map[int64][]ncm.Song{2: {song}, 3: {song}},
		nextID:    3,
	}
	catalog, err := NewCatalog(m)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(catalog, NewState(catalog, &fakePlayer{}))
	c, r := connect(t, s)
	defer c.Close()

	listed := response(t, c, r, "listplaylists")
	if strings.Contains(listed, "playlist: 25/05\n") || strings.Contains(listed, "playlist: 23/11/16\n") {
		t.Fatalf("raw slash names leaked to client: %q", listed)
	}
	// All playlists carry zero-padded order prefixes; slashes stay sanitized.
	if !strings.Contains(listed, "playlist: 01 - 25／05\n") || !strings.Contains(listed, "playlist: 07 - 23／11／16\n") {
		t.Fatalf("expected ordered sanitized playlist names, got %q", listed)
	}
	ls := response(t, c, r, "lsinfo")
	if !strings.Contains(ls, "playlist: 01 - 25／05\n") {
		t.Fatalf("lsinfo = %q", ls)
	}
	// Clients may still send the original NetEase name, sanitized name, or ordered form.
	for _, name := range []string{`"25／05"`, `"25/05"`, `"01 - 25／05"`, `"23／11／16"`, `"23/11/16"`, `"07 - 23／11／16"`} {
		got := response(t, c, r, "listplaylistinfo "+name)
		if !strings.Contains(got, "file: netease://song/9\n") {
			t.Fatalf("listplaylistinfo %s = %q", name, got)
		}
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
	activeEnd(nil)
	if got := s.State.Snapshot().State; got != "stop" {
		t.Fatalf("active player callback was invalidated; state = %s", got)
	}
}

func TestCompatibilityCommandsAndListings(t *testing.T) {
	s, _ := fixture(t)
	c, r := connect(t, s)
	defer c.Close()
	got := response(t, c, r, "commands")
	for _, cmd := range []string{"update", "rescan", "listall", "listallinfo", "listplaylist", "save", "rename", "rm", "playlistadd", "playlistdelete", "playlistclear"} {
		if !strings.Contains(got, "command: "+cmd+"\n") {
			t.Errorf("commands omitted %s", cmd)
		}
	}
	if strings.Contains(got, "command: playlistmove\n") {
		t.Error("unsupported playlistmove advertised")
	}
	plain := response(t, c, r, `listall "中文 列表"`)
	if plain != "file: netease://song/7\nOK\n" {
		t.Fatalf("listall = %q", plain)
	}
	info := response(t, c, r, `listallinfo "中文 列表"`)
	if !strings.Contains(info, "Title: 歌 曲\n") || !strings.Contains(info, "Artist: 艺人\n") {
		t.Fatalf("listallinfo = %q", info)
	}
	if got := response(t, c, r, `listall missing`); !strings.HasPrefix(got, "ACK ") {
		t.Fatalf("missing scope = %q", got)
	}
	if got := response(t, c, r, `listplaylist "中文 列表"`); got != plain {
		t.Fatalf("listplaylist = %q", got)
	}
	if got := response(t, c, r, `listplaylistinfo "中文 列表"`); !strings.Contains(got, "Title: 歌 曲\n") {
		t.Fatalf("listplaylistinfo = %q", got)
	}
}

func TestUpdateRefreshEventsAndStats(t *testing.T) {
	s, m := fixture(t)
	c, r := connect(t, s)
	defer c.Close()
	sub, cancel := s.State.Subscribe()
	defer cancel()
	m.mu.Lock()
	m.tracks[2] = []ncm.Song{{ID: 9, Title: "fresh", Duration: 2 * time.Minute}}
	m.mu.Unlock()
	first := response(t, c, r, `update "中文 列表"`)
	if got := response(t, c, r, `listall "中文 列表"`); !strings.Contains(got, "netease://song/9") {
		t.Fatalf("scoped refresh not visible: %q", got)
	}
	// Scoped update must not pull unrelated playlists into the URI index.
	if _, ok := s.Catalog.Song(SongURI(8)); ok {
		t.Fatal("scoped refresh force-loaded unrelated playlist")
	}
	m.mu.Lock()
	callsAfterScoped := m.trackCalls
	m.mu.Unlock()

	second := response(t, c, r, "rescan")
	job := func(v string) int {
		fields := strings.Fields(v)
		if len(fields) < 2 {
			t.Fatalf("job response %q", v)
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if job(second) <= job(first) {
		t.Fatalf("non-monotonic jobs: %q %q", first, second)
	}
	pending := s.State.takePending(sub, nil)
	for _, want := range []string{"update", "database", "stored_playlist"} {
		found := false
		for _, got := range pending {
			found = found || got == want
		}
		if !found {
			t.Errorf("missing idle event %s in %v", want, pending)
		}
	}
	if got := len(s.Catalog.Playlists()); got != len(composePlaylists(s.Catalog.playlists)) {
		t.Fatalf("refresh lost playlists: %#v", s.Catalog.Playlists())
	}
	// Full rescan loads playlists that were never cached; unchanged cached lists
	// (中文 列表, still trackCount=1) are not re-fetched.
	m.mu.Lock()
	callsAfterRescan := m.trackCalls
	m.mu.Unlock()
	if callsAfterRescan <= callsAfterScoped {
		t.Fatalf("rescan did not load uncached playlists: before=%d after=%d", callsAfterScoped, callsAfterRescan)
	}
	if songs, err := s.Catalog.PlaylistSongs("other"); err != nil || len(songs) != 1 || songs[0].ID != 8 {
		t.Fatalf("other playlist after rescan: %#v, %v", songs, err)
	}
	if song, ok := s.Catalog.Song(SongURI(8)); !ok || song.ID != 8 {
		t.Fatalf("other playlist song not indexed: %#v, %v", song, ok)
	}
	// Second full rescan with unchanged trackCounts should not refetch.
	m.mu.Lock()
	callsBeforeStable := m.trackCalls
	m.mu.Unlock()
	_ = response(t, c, r, "rescan")
	m.mu.Lock()
	callsAfterStable := m.trackCalls
	m.mu.Unlock()
	if callsAfterStable != callsBeforeStable {
		t.Fatalf("stable rescan refetched tracks: before=%d after=%d", callsBeforeStable, callsAfterStable)
	}
	stats1 := response(t, c, r, "stats")
	time.Sleep(5 * time.Millisecond)
	stats2 := response(t, c, r, "stats")
	value := func(out, key string) int64 {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, key+": ") {
				n, err := strconv.ParseInt(strings.TrimPrefix(line, key+": "), 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
		}
		t.Fatalf("missing %s in %q", key, out)
		return -1
	}
	if value(stats1, "db_update") != value(stats2, "db_update") {
		t.Error("db_update was not stable")
	}
	if value(stats1, "uptime") < 0 {
		t.Error("negative uptime")
	}
	if value(stats1, "db_playtime") != 180 {
		t.Fatalf("db_playtime = %d", value(stats1, "db_playtime"))
	}
}

func TestStoredPlaylistMutations(t *testing.T) {
	s, _ := fixture(t)
	c, r := connect(t, s)
	defer c.Close()
	_ = response(t, c, r, `add "netease://song/7"`)
	for _, cmd := range []string{`save saved`, `rename saved renamed`, `playlistadd renamed "netease://song/7"`} {
		if got := response(t, c, r, cmd); got != "OK\n" {
			t.Fatalf("%s: %q", cmd, got)
		}
	}
	if got := response(t, c, r, `listplaylist renamed`); strings.Count(got, "file: netease://song/7") != 2 {
		t.Fatalf("saved/add not visible: %q", got)
	}
	if got := response(t, c, r, `playlistdelete renamed 9`); !strings.HasPrefix(got, "ACK ") {
		t.Fatalf("invalid position = %q", got)
	}
	if got := response(t, c, r, `playlistadd renamed unknown`); !strings.HasPrefix(got, "ACK ") {
		t.Fatalf("unknown URI = %q", got)
	}
	got := response(t, c, r, "command_list_begin\nping\nplaylistdelete renamed 9\nping\ncommand_list_end")
	if !strings.Contains(got, "ACK [50@1]") {
		t.Fatalf("command-list index = %q", got)
	}
	if got := response(t, c, r, `playlistdelete renamed 0`); got != "OK\n" {
		t.Fatal(got)
	}
	if got := response(t, c, r, `playlistclear renamed`); got != "OK\n" {
		t.Fatal(got)
	}
	if got := response(t, c, r, `listplaylist renamed`); got != "OK\n" {
		t.Fatalf("clear not visible: %q", got)
	}
	if got := response(t, c, r, `rm renamed`); got != "OK\n" {
		t.Fatal(got)
	}
	if got := response(t, c, r, `listplaylist renamed`); !strings.HasPrefix(got, "ACK ") {
		t.Fatalf("rm not visible: %q", got)
	}
}

func TestStopWinsAgainstInFlightBackendStart(t *testing.T) {
	s, music := fixture(t)
	backend := &blockingPlayer{started: make(chan struct{}), release: make(chan struct{})}
	s.State.player = backend
	s.State.Add(music.song)
	playDone := make(chan error, 1)
	go func() { playDone <- s.State.Play(0) }()
	<-backend.started
	stopDone := make(chan struct{})
	go func() {
		s.State.Stop()
		close(stopDone)
	}()
	deadline := time.Now().Add(time.Second)
	for s.State.Snapshot().State != "stop" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(backend.release)
	if err := <-playDone; err != nil {
		t.Fatal(err)
	}
	<-stopDone
	if backend.isActive() {
		t.Fatal("backend remained active after Stop completed")
	}
	if got := s.State.Snapshot().State; got != "stop" {
		t.Fatalf("state = %q, want stop", got)
	}
}

func TestStopWinsAgainstInFlightTransportChanges(t *testing.T) {
	for _, operation := range []string{"pause", "resume", "seek"} {
		t.Run(operation, func(t *testing.T) {
			s, music := fixture(t)
			backend := &transportBarrierPlayer{}
			s.State.player = backend
			s.State.Add(music.song)
			if err := s.State.Play(0); err != nil {
				t.Fatal(err)
			}
			if operation == "resume" {
				if err := s.State.Pause(true); err != nil {
					t.Fatal(err)
				}
			}
			backend.operation = operation
			backend.entered = make(chan struct{})
			backend.release = make(chan struct{})
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "pause":
					done <- s.State.Pause(true)
				case "resume":
					done <- s.State.Pause(false)
				default:
					done <- s.State.Seek(time.Minute)
				}
			}()
			<-backend.entered
			stopDone := make(chan struct{})
			go func() {
				s.State.Stop()
				close(stopDone)
			}()
			for {
				s.State.mu.Lock()
				stopped := s.State.state == "stop"
				s.State.mu.Unlock()
				if stopped {
					break
				}
				runtime.Gosched()
			}
			close(backend.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			<-stopDone
			st := s.State.Snapshot()
			if st.State != "stop" || st.Elapsed != 0 {
				t.Fatalf("Stop lost to in-flight %s: state=%s elapsed=%s", operation, st.State, st.Elapsed)
			}
		})
	}
}

func TestStopInvalidatesReservedNaturalAdvance(t *testing.T) {
	s, music := fixture(t)
	backend := s.State.player.(*fakePlayer)
	s.State.Add(music.song)
	s.State.Add(ncm.Song{ID: 8, Title: "next", Duration: time.Minute})
	if err := s.State.Play(0); err != nil {
		t.Fatal(err)
	}
	activeEnd := backend.end
	hit, proceed := make(chan struct{}), make(chan struct{})
	music.mu.Lock()
	music.resolveHit, music.resolveGo = hit, proceed
	music.mu.Unlock()
	advanceDone := make(chan struct{})
	go func() {
		activeEnd(nil)
		close(advanceDone)
	}()
	<-hit
	s.State.Stop()
	close(proceed)
	<-advanceDone
	if backend.end != nil {
		t.Fatal("natural end started a successor after Stop completed")
	}
}

func TestEnsureLyricsWritesRmpcPath(t *testing.T) {
	dir := t.TempDir()
	s, m := fixture(t)
	s.Catalog.SetLyricsDir(dir)
	song := m.song
	if err := s.Catalog.EnsureLyrics(song); err != nil {
		t.Fatal(err)
	}
	path := LRCPath(dir, song.ID)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected lrc at %s: %v", path, err)
	}
	got := string(b)
	for _, want := range []string{"[ar:艺人]", "[ti:歌 曲]", "[al:专辑]", "[00:00.00]test lyric"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lrc missing %q:\n%s", want, got)
		}
	}
	// second call is cached / no-op
	if err := s.Catalog.EnsureLyrics(song); err != nil {
		t.Fatal(err)
	}
}

func TestVirtualPlaylists(t *testing.T) {
	s, _ := fixture(t)
	c, r := connect(t, s)
	defer c.Close()

	listed := response(t, c, r, "listplaylists")
	// Full server order, zero-padded so rmpc alpha-sort keeps it.
	wantOrder := []string{
		"01 - 中文 列表",
		"02 - 中文 列表（心动模式）",
		"03 - 私人FM",
		"04 - 每日推荐",
		"05 - 最近播放",
		"06 - 云盘",
		"07 - other",
	}
	pos := 0
	for _, name := range wantOrder {
		marker := "playlist: " + name + "\n"
		i := strings.Index(listed[pos:], marker)
		if i < 0 {
			t.Fatalf("listplaylists missing %s after pos %d: %q", name, pos, listed)
		}
		pos += i + len(marker)
	}
	// Bare names and ordered names both resolve.
	for _, name := range []string{"私人FM", "03 - 私人FM", "中文 列表（心动模式）", "02 - 中文 列表（心动模式）"} {
		if _, ok := s.Catalog.Playlist(name); !ok {
			t.Fatalf("Playlist(%q) not found", name)
		}
	}

	cases := []struct {
		name string
		uri  string
	}{
		{"每日推荐", "netease://song/101"},
		{"personal_fm", "netease://song/102"},
		{"最近播放", "netease://song/103"},
		{"cloud", "netease://song/104"},
	}
	for _, tc := range cases {
		got := response(t, c, r, "listplaylistinfo "+strconv.Quote(tc.name))
		if !strings.Contains(got, "file: "+tc.uri+"\n") {
			t.Fatalf("listplaylistinfo %s = %q", tc.name, got)
		}
	}

	// 心动模式: seed = first liked track (id 7 in fixture), then recommendations.
	intel := response(t, c, r, "listplaylistinfo "+strconv.Quote("intelligence"))
	if !strings.Contains(intel, "file: netease://song/7\n") ||
		!strings.Contains(intel, "file: netease://song/201\n") ||
		!strings.Contains(intel, "file: netease://song/202\n") {
		t.Fatalf("intelligence playlist = %q", intel)
	}
	also := response(t, c, r, "listplaylistinfo "+strconv.Quote("中文 列表（心动模式）"))
	if also != intel {
		t.Fatalf("full name and alias differ:\n%s\n%s", also, intel)
	}

	// load virtual playlist into the play queue
	if got := response(t, c, r, "load "+strconv.Quote("私人FM")); got != "OK\n" {
		t.Fatalf("load personal fm = %q", got)
	}
	if q := s.State.Snapshot().Queue; len(q) == 0 || q[0].Song.ID != 102 {
		t.Fatalf("queue after load = %#v", q)
	}

	// virtual playlists are read-only
	for _, cmd := range []string{
		"rm " + strconv.Quote("私人FM"),
		"rename " + strconv.Quote("每日推荐") + " other",
		"playlistclear " + strconv.Quote("云盘"),
		"save " + strconv.Quote("私人FM"),
		"rm " + strconv.Quote("中文 列表（心动模式）"),
		"save " + strconv.Quote("心动模式"),
	} {
		if got := response(t, c, r, cmd); !strings.HasPrefix(got, "ACK ") {
			t.Fatalf("%s should fail, got %q", cmd, got)
		}
	}
}
