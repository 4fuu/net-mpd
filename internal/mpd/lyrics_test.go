package mpd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/net-mpd/internal/ncm"
)

func TestMergeLyricTextsBilingual(t *testing.T) {
	orig := "[00:00.00]intro\n[00:06.341]hello world\n[00:10.00]only orig\n"
	trans := "[by:contributor]\n[00:06.341]你好世界\n[00:99.00]orphan\n"
	got := mergeLyricTexts(orig, trans)
	wantLines := []string{
		"[00:00.00]intro",
		"[00:06.341]hello world / 你好世界",
		"[00:10.00]only orig",
	}
	for _, want := range wantLines {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "orphan") || strings.Contains(got, "[by:contributor]") {
		t.Fatalf("unexpected translation metadata/orphan:\n%s", got)
	}
}

func TestMergeLyricTextsNoTranslation(t *testing.T) {
	orig := "[00:00.00]only\n[00:05.00]lines\n"
	got := mergeLyricTexts(orig, "")
	if got != orig {
		t.Fatalf("got %q want %q", got, orig)
	}
}

func TestFormatLRCIncludesVersionAndMerge(t *testing.T) {
	song := ncm.Song{Title: "T", Album: "A", Artists: []string{"X"}}
	ly := ncm.Lyrics{
		Original:   "[00:01.00]foo\n",
		Translated: "[00:01.00]bar\n",
	}
	got := formatLRC(song, ly)
	for _, want := range []string{"[ar:X]", "[al:A]", "[ti:T]", lrcByTag(), "[00:01.00]foo / bar"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	// Old dual-block layout must not appear.
	if strings.Count(got, "[00:01.00]") != 1 {
		t.Fatalf("expected single timed line, got:\n%s", got)
	}
}

func TestParseLRCStampMs(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"00:01.00", 1000},
		{"00:01.500", 1500},
		{"01:02.03", 62030},
		{"1:02:03.5", 3723500},
	}
	for _, tc := range cases {
		got, ok := parseLRCStampMs(tc.in)
		if !ok || got != tc.want {
			t.Fatalf("parseLRCStampMs(%q)=%d,%v want %d,true", tc.in, got, ok, tc.want)
		}
	}
}

func TestEnsureLyricsRewritesStaleFormat(t *testing.T) {
	dir := t.TempDir()
	s, m := fixture(t)
	m.lyrics = &ncm.Lyrics{
		Original:   "[00:01.00]hello\n",
		Translated: "[00:01.00]你好\n",
	}
	// Set dir without racing the background prune against later assertions.
	s.Catalog.mu.Lock()
	s.Catalog.lyricsDir = dir
	s.Catalog.mu.Unlock()

	path := LRCPath(dir, m.song.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := "[ar:old]\n[by:net-mpd]\n[00:01.00]hello\n\n[00:01.00]你好\n"
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Catalog.EnsureLyrics(m.song); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, lrcByTag()) {
		t.Fatalf("stale lrc not rewritten with version tag:\n%s", got)
	}
	if !strings.Contains(got, "[00:01.00]hello / 你好") {
		t.Fatalf("stale lrc not merged:\n%s", got)
	}
	if m.lyricsCalls != 1 {
		t.Fatalf("lyricsCalls=%d want 1", m.lyricsCalls)
	}
}

func TestEnsureLyricsSkipsCurrentCache(t *testing.T) {
	dir := t.TempDir()
	s, m := fixture(t)
	s.Catalog.mu.Lock()
	s.Catalog.lyricsDir = dir
	s.Catalog.mu.Unlock()

	path := LRCPath(dir, m.song.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	current := formatLRC(m.song, ncm.Lyrics{Original: "[00:00.00]cached\n"})
	if err := os.WriteFile(path, []byte(current), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Catalog.EnsureLyrics(m.song); err != nil {
		t.Fatal(err)
	}
	if m.lyricsCalls != 0 {
		t.Fatalf("current cache still fetched lyrics: calls=%d", m.lyricsCalls)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "cached") {
		t.Fatalf("cache file changed unexpectedly:\n%s", body)
	}
}

func TestPruneLyricsCacheByFileCount(t *testing.T) {
	dir := t.TempDir()
	s, m := fixture(t)
	s.Catalog.mu.Lock()
	s.Catalog.lyricsDir = dir
	s.Catalog.maxLyricsFiles = 2
	s.Catalog.maxLyricsBytes = 0
	s.Catalog.mu.Unlock()

	// Write three distinct song LRC files with staggered mtimes.
	for i, id := range []int64{11, 12, 13} {
		song := m.song
		song.ID = id
		path := LRCPath(dir, id)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		body := formatLRC(song, ncm.Lyrics{Original: "[00:00.00]x\n"})
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := time.Now().Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatal(err)
		}
		s.Catalog.mu.Lock()
		s.Catalog.lyricsDone[id] = true
		s.Catalog.mu.Unlock()
	}

	s.Catalog.pruneLyricsCache()

	if _, err := os.Stat(LRCPath(dir, 11)); !os.IsNotExist(err) {
		t.Fatalf("oldest lrc should be pruned, stat err=%v", err)
	}
	for _, id := range []int64{12, 13} {
		if _, err := os.Stat(LRCPath(dir, id)); err != nil {
			t.Fatalf("song %d should remain: %v", id, err)
		}
	}
	s.Catalog.mu.RLock()
	_, oldestDone := s.Catalog.lyricsDone[11]
	s.Catalog.mu.RUnlock()
	if oldestDone {
		t.Fatal("lyricsDone not cleared for pruned id")
	}
}

func TestPruneLyricsCacheByBytes(t *testing.T) {
	dir := t.TempDir()
	s, m := fixture(t)
	// One small file ~100B; keep only enough budget for one.
	s.Catalog.mu.Lock()
	s.Catalog.lyricsDir = dir
	s.Catalog.maxLyricsFiles = 0
	s.Catalog.maxLyricsBytes = 1 // force prune to a single survivor after deletions
	s.Catalog.mu.Unlock()

	for i, id := range []int64{21, 22} {
		path := LRCPath(dir, id)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		body := formatLRC(m.song, ncm.Lyrics{Original: "[00:00.00]payload\n"})
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := time.Now().Add(time.Duration(i) * time.Hour)
		_ = os.Chtimes(path, ts, ts)
	}
	s.Catalog.pruneLyricsCache()
	// With maxBytes=1, keep deleting until total <= 1, i.e. no files left
	// (each file is larger than 1 byte).
	left := 0
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".lrc") {
			left++
		}
		return nil
	})
	if left != 0 {
		t.Fatalf("expected all lrc pruned under tiny byte budget, left=%d", left)
	}
}

func TestCoverCacheLRU(t *testing.T) {
	s, m := fixture(t)
	s.Catalog.maxCovers = 2
	// Register three songs in byURI.
	songs := []ncm.Song{
		{ID: 1, Title: "a"},
		{ID: 2, Title: "b"},
		{ID: 3, Title: "c"},
	}
	s.Catalog.mu.Lock()
	for _, song := range songs {
		s.Catalog.byURI[SongURI(song.ID)] = song
	}
	s.Catalog.mu.Unlock()
	m.cover = []byte("img")

	for _, song := range songs[:2] {
		if _, err := s.Catalog.Cover(SongURI(song.ID)); err != nil {
			t.Fatal(err)
		}
	}
	// Touch song 1 so song 2 is the LRU victim.
	if _, err := s.Catalog.Cover(SongURI(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Catalog.Cover(SongURI(3)); err != nil {
		t.Fatal(err)
	}

	s.Catalog.mu.RLock()
	_, has1 := s.Catalog.covers[SongURI(1)]
	_, has2 := s.Catalog.covers[SongURI(2)]
	_, has3 := s.Catalog.covers[SongURI(3)]
	s.Catalog.mu.RUnlock()
	if !has1 || has2 || !has3 {
		t.Fatalf("LRU state has1=%v has2=%v has3=%v want true,false,true", has1, has2, has3)
	}
}
