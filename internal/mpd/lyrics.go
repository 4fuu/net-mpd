package mpd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/4fuu/net-mpd/internal/ncm"
)

// lrcFormatVersion is embedded in [by:net-mpd/N]. Bump when the on-disk LRC
// shape changes so EnsureLyrics rewrites stale cache entries.
const lrcFormatVersion = 2

const (
	defaultMaxLyricsFiles = 1000
	defaultMaxLyricsBytes = 50 << 20 // 50 MiB
	defaultMaxCovers      = 32
)

type timedLyricLine struct {
	timeMs int64
	stamp  string
	text   string
}

func lrcByTag() string {
	return fmt.Sprintf("[by:net-mpd/%d]", lrcFormatVersion)
}

func lrcFileCurrent(body string) bool {
	return strings.Contains(body, lrcByTag())
}

func formatLRC(song ncm.Song, ly ncm.Lyrics) string {
	var b strings.Builder
	if len(song.Artists) > 0 {
		fmt.Fprintf(&b, "[ar:%s]\n", strings.Join(song.Artists, "/"))
	}
	if song.Album != "" {
		fmt.Fprintf(&b, "[al:%s]\n", song.Album)
	}
	if song.Title != "" {
		fmt.Fprintf(&b, "[ti:%s]\n", song.Title)
	}
	b.WriteString(lrcByTag())
	b.WriteByte('\n')
	b.WriteString(mergeLyricTexts(ly.Original, ly.Translated))
	return b.String()
}

// mergeLyricTexts pairs NetEase original/translated LRC by timestamp and emits
// "original / translation" on one line so rmpc can show both.
func mergeLyricTexts(original, translated string) string {
	origLines := parseTimedLyricLines(original)
	if len(origLines) == 0 {
		return strings.TrimRight(original, "\n") + "\n"
	}

	transByMs := make(map[int64]string)
	for _, line := range parseTimedLyricLines(translated) {
		if line.text == "" {
			continue
		}
		if _, ok := transByMs[line.timeMs]; !ok {
			transByMs[line.timeMs] = line.text
		}
	}

	var b strings.Builder
	for _, line := range origLines {
		text := line.text
		if tr, ok := transByMs[line.timeMs]; ok && tr != "" && tr != text {
			if text == "" {
				text = tr
			} else {
				text = text + " / " + tr
			}
		}
		if text == "" {
			fmt.Fprintf(&b, "[%s]\n", line.stamp)
		} else {
			fmt.Fprintf(&b, "[%s]%s\n", line.stamp, text)
		}
	}
	return b.String()
}

func parseTimedLyricLines(s string) []timedLyricLine {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []timedLyricLine
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] != '[' {
			continue
		}
		var stamps []string
		rest := line
		for strings.HasPrefix(rest, "[") {
			end := strings.IndexByte(rest, ']')
			if end <= 1 {
				break
			}
			tag := rest[1:end]
			rest = rest[end+1:]
			if isLRCTimestampTag(tag) {
				stamps = append(stamps, tag)
				if !strings.HasPrefix(rest, "[") {
					break
				}
				continue
			}
			// Metadata (ar/ti/by/…) — drop the whole line.
			stamps = nil
			break
		}
		if len(stamps) == 0 {
			continue
		}
		text := strings.TrimSpace(rest)
		for _, stamp := range stamps {
			ms, ok := parseLRCStampMs(stamp)
			if !ok {
				continue
			}
			out = append(out, timedLyricLine{timeMs: ms, stamp: stamp, text: text})
		}
	}
	return out
}

func isLRCTimestampTag(tag string) bool {
	if tag == "" {
		return false
	}
	c := tag[0]
	return c >= '0' && c <= '9' && strings.Contains(tag, ":")
}

// parseLRCStampMs accepts mm:ss, mm:ss.xx, mm:ss.xxx, and h:mm:ss(.xxx).
func parseLRCStampMs(tag string) (int64, bool) {
	parts := strings.Split(tag, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var hours, minutes int64
	var secPart string
	var err error
	if len(parts) == 3 {
		hours, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || hours < 0 {
			return 0, false
		}
		minutes, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || minutes < 0 || minutes > 59 {
			return 0, false
		}
		secPart = parts[2]
	} else {
		minutes, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || minutes < 0 {
			return 0, false
		}
		secPart = parts[1]
	}
	// Allow ss.xx or ss:xx (some LRC dialects).
	secPart = strings.Replace(secPart, ":", ".", 1)
	secFloat, err := strconv.ParseFloat(secPart, 64)
	if err != nil || secFloat < 0 || secFloat >= 60 {
		return 0, false
	}
	ms := hours*3600*1000 + minutes*60*1000 + int64(secFloat*1000+0.5)
	return ms, true
}

// EnsureLyrics fetches NetEase lyrics and writes an LRC file rmpc can open.
// Safe to call repeatedly; failures are non-fatal for playback.
// Stale cache entries (missing version tag) are rewritten. The lyrics directory
// is pruned to maxLyricsFiles / maxLyricsBytes after writes.
func (c *Catalog) EnsureLyrics(song ncm.Song) error {
	c.mu.RLock()
	dir := c.lyricsDir
	done := c.lyricsDone[song.ID]
	c.mu.RUnlock()
	if dir == "" {
		return nil
	}
	path := LRCPath(dir, song.ID)
	if done {
		return nil
	}
	if body, err := os.ReadFile(path); err == nil && len(body) > 0 {
		if lrcFileCurrent(string(body)) {
			now := time.Now()
			_ = os.Chtimes(path, now, now)
			c.mu.Lock()
			c.lyricsDone[song.ID] = true
			c.mu.Unlock()
			return nil
		}
		// Stale format — fall through and rewrite.
	}
	ly, err := c.service.Lyrics(song.ID)
	if err != nil {
		return err
	}
	if ly.Original == "" {
		c.mu.Lock()
		c.lyricsDone[song.ID] = true
		c.mu.Unlock()
		return nil
	}
	body := formatLRC(song, ly)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	c.mu.Lock()
	c.lyricsDone[song.ID] = true
	c.mu.Unlock()
	c.pruneLyricsCache()
	return nil
}

type lyricsCacheEntry struct {
	path  string
	size  int64
	mtime time.Time
	id    int64
}

// pruneLyricsCache deletes oldest .lrc files until under the configured limits.
// No-op when limits are zero or the directory is unset.
func (c *Catalog) pruneLyricsCache() {
	c.mu.RLock()
	dir := c.lyricsDir
	maxFiles := c.maxLyricsFiles
	maxBytes := c.maxLyricsBytes
	c.mu.RUnlock()
	if dir == "" {
		return
	}
	if maxFiles <= 0 && maxBytes <= 0 {
		return
	}

	var entries []lyricsCacheEntry
	var total int64
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(info.Name()), ".lrc") {
			return nil
		}
		entries = append(entries, lyricsCacheEntry{
			path:  path,
			size:  info.Size(),
			mtime: info.ModTime(),
			id:    songIDFromLRCPath(path),
		})
		total += info.Size()
		return nil
	})

	withinFiles := maxFiles <= 0 || len(entries) <= maxFiles
	withinBytes := maxBytes <= 0 || total <= maxBytes
	if withinFiles && withinBytes {
		return
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].mtime.Equal(entries[j].mtime) {
			return entries[i].path < entries[j].path
		}
		return entries[i].mtime.Before(entries[j].mtime)
	})

	removed := make([]int64, 0)
	for len(entries) > 0 {
		withinFiles = maxFiles <= 0 || len(entries) <= maxFiles
		withinBytes = maxBytes <= 0 || total <= maxBytes
		if withinFiles && withinBytes {
			break
		}
		e := entries[0]
		entries = entries[1:]
		if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) {
			continue
		}
		total -= e.size
		if total < 0 {
			total = 0
		}
		if e.id != 0 {
			removed = append(removed, e.id)
		}
	}
	if len(removed) == 0 {
		return
	}
	c.mu.Lock()
	for _, id := range removed {
		delete(c.lyricsDone, id)
	}
	c.mu.Unlock()
}

func songIDFromLRCPath(path string) int64 {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	id, err := strconv.ParseInt(base, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// PruneLyricsCache removes old lyrics files according to catalog limits.
// Safe to call at startup.
func (c *Catalog) PruneLyricsCache() {
	c.pruneLyricsCache()
}
