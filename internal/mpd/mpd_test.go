package mpd

import (
	"bufio"
	"errors"
	"io"
	"net"
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
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ncm.Song(nil), f.tracks[id]...), nil
}
func (f *fakeMusic) SearchSongs(string, int) ([]ncm.Song, error) { return []ncm.Song{f.song}, nil }
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
	if got := response(t, c, r, `listall "中文 列表"`); !strings.Contains(got, "netease://song/9") {
		t.Fatalf("refresh not visible: %q", got)
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
	// A scoped refresh must publish complete tracks and URI indexes for every playlist.
	if len(s.Catalog.Playlists()) != 2 {
		t.Fatalf("scoped refresh lost playlists: %#v", s.Catalog.Playlists())
	}
	if song, ok := s.Catalog.Song(SongURI(8)); !ok || song.ID != 8 {
		t.Fatalf("scoped refresh did not publish unrelated song: %#v, %v", song, ok)
	}
	if songs, err := s.Catalog.PlaylistSongs("other"); err != nil || len(songs) != 1 || songs[0].ID != 8 {
		t.Fatalf("lazy other playlist: %#v, %v", songs, err)
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
