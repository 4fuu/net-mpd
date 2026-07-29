package mpd

import (
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/4fuu/net-mpd/internal/ncm"
	"github.com/4fuu/net-mpd/internal/player"
	"github.com/4fuu/net-mpd/internal/sysmedia"
)

type QueueItem struct {
	ID   int64
	Song ncm.Song
}
type Status struct {
	Volume                          int
	Repeat, Random, Single, Consume bool
	Version                         int64
	Queue                           []QueueItem
	State                           string
	Current                         int
	Elapsed                         time.Duration
	Error                           string
}
type eventSubscription struct {
	pending map[string]struct{}
	wake    chan struct{}
}
type State struct {
	mu                              sync.Mutex
	transport                       sync.Mutex
	catalog                         *Catalog
	player                          player.Player
	queue                           []QueueItem
	nextID                          int64
	current                         int
	state                           string
	elapsed                         time.Duration
	started                         time.Time
	volume                          int
	repeat, random, single, consume bool
	version                         int64
	generation                      uint64
	request                         uint64
	playbackError                   string
	outputEnabled                   bool
	subs                            map[*eventSubscription]struct{}
	media                           sysmedia.Control
	// Power save: when no clients are connected and playback stays paused
	// for pauseTimeout, the audio backend is released (sleeping=true).
	pauseTimeout time.Duration
	clients      int
	sleepTimer   *time.Timer
	sleeping     bool
}

func NewState(c *Catalog, p player.Player) *State {
	return &State{catalog: c, player: p, nextID: 1, current: -1, state: "stop", volume: 100, outputEnabled: true, subs: map[*eventSubscription]struct{}{}}
}

// AttachMedia connects the OS Now Playing / media-key session.
func (s *State) AttachMedia(c sysmedia.Control) {
	s.mu.Lock()
	s.media = c
	s.mu.Unlock()
	s.publishMedia()
}
func (s *State) notify(kind string) {
	s.mu.Lock()
	for sub := range s.subs {
		sub.pending[kind] = struct{}{}
		select {
		case sub.wake <- struct{}{}:
		default:
		}
	}
	s.updateSleepTimerLocked()
	s.mu.Unlock()
}
func (s *State) Notify(kinds ...string) {
	for _, kind := range kinds {
		s.notify(kind)
	}
}
func (s *State) Subscribe() (*eventSubscription, func()) {
	sub := &eventSubscription{pending: make(map[string]struct{}), wake: make(chan struct{}, 1)}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	return sub, func() {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
	}
}
func (s *State) takePending(sub *eventSubscription, want map[string]bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var kinds []string
	for kind := range sub.pending {
		if len(want) == 0 || want[kind] {
			kinds = append(kinds, kind)
			delete(sub.pending, kind)
		}
	}
	return kinds
}

// SetPauseTimeout configures power save: when no MPD client is connected
// and playback remains paused for d, the audio backend is released and the
// paused position is remembered. d <= 0 disables the feature.
func (s *State) SetPauseTimeout(d time.Duration) {
	s.mu.Lock()
	s.pauseTimeout = d
	s.updateSleepTimerLocked()
	s.mu.Unlock()
}

// ClientConnected / ClientDisconnected track open MPD connections.
func (s *State) ClientConnected()    { s.addClient(1) }
func (s *State) ClientDisconnected() { s.addClient(-1) }
func (s *State) addClient(d int) {
	s.mu.Lock()
	s.clients += d
	if s.clients < 0 {
		s.clients = 0
	}
	s.updateSleepTimerLocked()
	s.mu.Unlock()
}

// updateSleepTimerLocked arms or disarms the power-save timer based on the
// current state. s.mu must be held.
func (s *State) updateSleepTimerLocked() {
	armed := s.pauseTimeout > 0 && s.clients == 0 && s.state == "pause" && !s.sleeping
	if armed && s.sleepTimer == nil {
		s.sleepTimer = time.AfterFunc(s.pauseTimeout, s.enterSleep)
	} else if !armed && s.sleepTimer != nil {
		s.sleepTimer.Stop()
		s.sleepTimer = nil
	}
}

// enterSleep releases the audio backend after a long unattended pause. The
// position is remembered so the next play/resume restarts from it.
func (s *State) enterSleep() {
	s.transport.Lock()
	defer s.transport.Unlock()
	s.mu.Lock()
	if s.sleeping || s.state != "pause" || s.clients > 0 || s.pauseTimeout <= 0 {
		s.mu.Unlock()
		return
	}
	if position := s.player.Position(); position > 0 {
		s.elapsed = position
	}
	s.generation++ // invalidate pending backend callbacks before releasing it
	s.sleeping = true
	s.sleepTimer = nil
	at := s.elapsed
	s.mu.Unlock()
	_ = s.player.Stop()
	log.Printf("power save: released audio backend paused at %s, no clients connected", at.Round(time.Second))
}

func (s *State) Snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.elapsed
	if s.state == "play" || s.state == "pause" {
		if position := s.player.Position(); position > 0 || e == 0 {
			e = position
		} else if s.state == "play" {
			e += time.Since(s.started)
		}
	}
	return Status{s.volume, s.repeat, s.random, s.single, s.consume, s.version, append([]QueueItem(nil), s.queue...), s.state, s.current, e, s.playbackError}
}
func (s *State) Add(song ncm.Song) int64 {
	return s.Insert(song, -1)
}
func (s *State) Insert(song ncm.Song, pos int) int64 {
	ids := s.InsertBlock([]ncm.Song{song}, pos)
	return ids[0]
}
func (s *State) InsertBlock(songs []ncm.Song, pos int) []int64 {
	if len(songs) == 0 {
		return nil
	}
	s.mu.Lock()
	items := make([]QueueItem, len(songs))
	ids := make([]int64, len(songs))
	for i, song := range songs {
		ids[i] = s.nextID
		items[i] = QueueItem{s.nextID, song}
		s.nextID++
	}
	if pos < 0 || pos >= len(s.queue) {
		s.queue = append(s.queue, items...)
	} else {
		tail := append([]QueueItem(nil), s.queue[pos:]...)
		s.queue = append(s.queue[:pos], items...)
		s.queue = append(s.queue, tail...)
		if s.current >= pos {
			s.current += len(items)
		}
	}
	s.version++
	s.mu.Unlock()
	s.notify("playlist")
	return ids
}
func (s *State) Clear() {
	s.mu.Lock()
	s.generation++
	s.request++
	s.queue = nil
	s.current = -1
	s.state = "stop"
	s.elapsed = 0
	s.sleeping = false
	s.version++
	s.mu.Unlock()
	s.transport.Lock()
	_ = s.player.Stop()
	s.transport.Unlock()
	s.notify("playlist")
}
func (s *State) Delete(pos int) error {
	return s.DeleteRange(pos, pos+1)
}
func (s *State) DeleteRange(start, end int) error {
	s.mu.Lock()
	if start < 0 || start >= len(s.queue) || end <= start || end > len(s.queue) {
		s.mu.Unlock()
		return fmt.Errorf("bad song range")
	}
	was := s.current >= start && s.current < end
	removed := end - start
	s.queue = append(s.queue[:start], s.queue[end:]...)
	if s.current >= end {
		s.current -= removed
	} else if was {
		s.current = -1
		s.state = "stop"
		s.elapsed = 0
		s.sleeping = false
		s.generation++
		s.request++
	}
	s.version++
	s.mu.Unlock()
	if was {
		s.transport.Lock()
		_ = s.player.Stop()
		s.transport.Unlock()
	}
	s.notify("playlist")
	return nil
}
func (s *State) DeleteID(id int64) error {
	st := s.Snapshot()
	for i, q := range st.Queue {
		if q.ID == id {
			return s.Delete(i)
		}
	}
	return fmt.Errorf("song id not found")
}
func (s *State) Move(from, to int) error {
	return s.MoveRange(from, from+1, to)
}
func (s *State) MoveRange(start, end, to int) error {
	s.mu.Lock()
	if start < 0 || end <= start || end > len(s.queue) || to < 0 || to > len(s.queue)-(end-start) {
		s.mu.Unlock()
		return fmt.Errorf("bad song range")
	}
	currentID := int64(-1)
	if s.current >= 0 {
		currentID = s.queue[s.current].ID
	}
	block := append([]QueueItem(nil), s.queue[start:end]...)
	s.queue = append(s.queue[:start], s.queue[end:]...)
	tail := append([]QueueItem(nil), s.queue[to:]...)
	s.queue = append(s.queue[:to], block...)
	s.queue = append(s.queue, tail...)
	if currentID >= 0 {
		for i := range s.queue {
			if s.queue[i].ID == currentID {
				s.current = i
				break
			}
		}
	}
	s.version++
	s.mu.Unlock()
	s.notify("playlist")
	return nil
}
func (s *State) Swap(a, b int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a < 0 || b < 0 || a >= len(s.queue) || b >= len(s.queue) {
		return fmt.Errorf("bad song index")
	}
	s.queue[a], s.queue[b] = s.queue[b], s.queue[a]
	if s.current == a {
		s.current = b
	} else if s.current == b {
		s.current = a
	}
	s.version++
	go s.notify("playlist")
	return nil
}
func (s *State) Shuffle() {
	_ = s.ShuffleRange(0, len(s.Snapshot().Queue))
}
func (s *State) ShuffleRange(start, end int) error {
	s.mu.Lock()
	if start < 0 || end < start || end > len(s.queue) {
		s.mu.Unlock()
		return fmt.Errorf("bad song range")
	}
	currentID := int64(-1)
	if s.current >= 0 && s.current < len(s.queue) {
		currentID = s.queue[s.current].ID
	}
	rand.Shuffle(end-start, func(i, j int) {
		i, j = i+start, j+start
		s.queue[i], s.queue[j] = s.queue[j], s.queue[i]
	})
	if currentID >= 0 {
		for i := range s.queue {
			if s.queue[i].ID == currentID {
				s.current = i
				break
			}
		}
	}
	s.version++
	s.mu.Unlock()
	s.notify("playlist")
	return nil
}
func (s *State) Play(pos int) error { return s.playAt(pos, 0) }
func (s *State) playAt(pos int, off time.Duration) error {
	s.mu.Lock()
	if pos < 0 {
		pos = s.current
		if pos < 0 && len(s.queue) > 0 {
			pos = 0
		}
	}
	if pos < 0 || pos >= len(s.queue) {
		s.mu.Unlock()
		return fmt.Errorf("bad song index")
	}
	itemID := s.queue[pos].ID
	song := s.queue[pos].Song
	s.request++
	request := s.request
	vol := s.volume
	s.mu.Unlock()
	return s.playReserved(itemID, song, off, vol, request)
}
func (s *State) playReserved(itemID int64, song ncm.Song, off time.Duration, vol int, request uint64) error {
	info, err := s.catalog.Resolve(song)
	if err != nil {
		log.Printf("resolve song %d failed: %v", song.ID, err)
		s.mu.Lock()
		s.playbackError = err.Error()
		s.state = "stop"
		s.mu.Unlock()
		s.notify("player")
		return fmt.Errorf("song is not playable")
	}
	// Write LRC before playback notify so rmpc's SongChanged handler can find it.
	if err := s.catalog.EnsureLyrics(song); err != nil {
		log.Printf("lyrics for song %d: %v", song.ID, err)
	}
	s.transport.Lock()
	defer s.transport.Unlock()
	s.mu.Lock()
	if !s.outputEnabled {
		s.mu.Unlock()
		return fmt.Errorf("audio output is disabled")
	}
	if request != s.request {
		s.mu.Unlock()
		return nil
	}
	pos := -1
	for i := range s.queue {
		if s.queue[i].ID == itemID {
			pos = i
			break
		}
	}
	if pos < 0 {
		s.mu.Unlock()
		return fmt.Errorf("song left the queue")
	}
	s.generation++
	gen := s.generation
	s.current = pos
	s.elapsed = off
	s.started = time.Now()
	s.state = "play"
	s.sleeping = false
	s.playbackError = ""
	s.mu.Unlock()
	err = s.player.Play(info.URL, info.Type, off, vol, func(err error) { s.playbackEnd(gen, err) })
	if err != nil {
		log.Printf("start playback for song %d failed: %v", song.ID, err)
		s.mu.Lock()
		if gen == s.generation {
			s.state = "stop"
			s.elapsed = 0
			s.playbackError = err.Error()
		}
		s.mu.Unlock()
		s.notify("player")
		s.publishMedia()
		return err
	}
	s.notify("player")
	s.publishMedia()
	return nil
}
func (s *State) playbackEnd(gen uint64, err error) {
	if err == nil {
		s.naturalEnd(gen)
		return
	}
	s.mu.Lock()
	if gen != s.generation {
		s.mu.Unlock()
		return
	}
	s.state = "stop"
	s.playbackError = err.Error()
	s.elapsed = 0
	s.mu.Unlock()
	log.Printf("playback ended with error: %v", err)
	s.notify("player")
	s.publishMedia()
}
func (s *State) naturalEnd(gen uint64) {
	s.mu.Lock()
	if gen != s.generation {
		s.mu.Unlock()
		return
	}
	pos := s.current
	consumed := s.consume && pos >= 0
	if consumed {
		s.queue = append(s.queue[:pos], s.queue[pos+1:]...)
		s.version++
		if pos >= len(s.queue) {
			pos = -1
		}
	} else if s.single {
		if !s.repeat {
			pos = -1
		}
	} else {
		if s.random && len(s.queue) > 0 {
			pos = rand.Intn(len(s.queue))
		} else {
			pos++
		}
		if pos >= len(s.queue) {
			if s.repeat {
				pos = 0
			} else {
				pos = -1
			}
		}
	}
	s.state = "stop"
	s.elapsed = 0
	var item QueueItem
	var request uint64
	var vol int
	advance := pos >= 0 && pos < len(s.queue)
	if advance {
		item = s.queue[pos]
		s.request++
		request = s.request
		vol = s.volume
	}
	s.mu.Unlock()
	if consumed {
		s.notify("playlist")
	}
	if advance {
		if err := s.playReserved(item.ID, item.Song, 0, vol, request); err != nil {
			s.notify("player")
			s.publishMedia()
		}
	} else {
		s.notify("player")
		s.publishMedia()
	}
}
func (s *State) Stop() {
	s.mu.Lock()
	s.generation++
	s.request++
	s.state = "stop"
	s.elapsed = 0
	s.sleeping = false
	s.mu.Unlock()
	s.transport.Lock()
	_ = s.player.Stop()
	s.transport.Unlock()
	s.notify("player")
	s.publishMedia()
}
func (s *State) Pause(on bool) error {
	s.transport.Lock()
	defer s.transport.Unlock()
	s.mu.Lock()
	gen := s.generation
	state := s.state
	current := s.current
	sleeping := s.sleeping
	elapsed := s.elapsed
	var itemID int64
	if current >= 0 && current < len(s.queue) {
		itemID = s.queue[current].ID
	}
	s.mu.Unlock()
	if on {
		if state == "play" {
			if err := s.player.Pause(); err != nil {
				return err
			}
			position := s.player.Position()
			s.mu.Lock()
			committed := gen == s.generation && s.state == "play" && s.current == current && current >= 0 && current < len(s.queue) && s.queue[current].ID == itemID
			if committed {
				s.elapsed = position
				s.state = "pause"
			}
			s.mu.Unlock()
			if committed {
				s.notify("player")
				s.publishMedia()
			}
		}
		return nil
	}
	if state == "pause" {
		if sleeping {
			// Power-save wake: the backend was released, restart the current
			// song from the remembered position (re-resolves the stream URL).
			s.transport.Unlock()
			err := s.playAt(current, elapsed)
			s.transport.Lock()
			return err
		}
		s.mu.Lock()
		enabled := s.outputEnabled
		s.mu.Unlock()
		if !enabled {
			return fmt.Errorf("audio output is disabled")
		}
		if err := s.player.Resume(); err != nil {
			return err
		}
		s.mu.Lock()
		committed := gen == s.generation && s.state == "pause" && s.current == current && current >= 0 && current < len(s.queue) && s.queue[current].ID == itemID
		if committed {
			s.started = time.Now()
			s.state = "play"
		}
		s.mu.Unlock()
		if committed {
			s.notify("player")
			s.publishMedia()
		}
	}
	return nil
}
func (s *State) Seek(off time.Duration) error {
	s.transport.Lock()
	defer s.transport.Unlock()
	s.mu.Lock()
	gen := s.generation
	state := s.state
	current := s.current
	sleeping := s.sleeping
	var itemID int64
	if current >= 0 && current < len(s.queue) {
		itemID = s.queue[current].ID
	}
	s.mu.Unlock()
	if current < 0 {
		return fmt.Errorf("no current song")
	}
	if sleeping && state == "pause" {
		// Backend is released; just move the remembered resume position.
		s.mu.Lock()
		committed := gen == s.generation && s.sleeping && s.state == "pause" && s.current == current && current < len(s.queue) && s.queue[current].ID == itemID
		if committed {
			s.elapsed = off
			s.started = time.Now()
		}
		s.mu.Unlock()
		if committed {
			s.notify("player")
			s.publishMedia()
		}
		return nil
	}
	if err := s.player.Seek(off); err != nil {
		return err
	}
	s.mu.Lock()
	committed := gen == s.generation && s.state == state && s.current == current && current < len(s.queue) && s.queue[current].ID == itemID
	if committed {
		s.elapsed = off
		s.started = time.Now()
	}
	s.mu.Unlock()
	if committed {
		s.notify("player")
		s.publishMedia()
	}
	return nil
}
func (s *State) Next() error {
	st := s.Snapshot()
	p := st.Current + 1
	if p >= len(st.Queue) {
		if st.Repeat {
			p = 0
		} else {
			return fmt.Errorf("end of playlist")
		}
	}
	return s.playAt(p, 0)
}
func (s *State) Previous() error {
	st := s.Snapshot()
	p := st.Current - 1
	if p < 0 {
		p = 0
	}
	return s.playAt(p, 0)
}
func (s *State) SetVolume(v int) error {
	s.transport.Lock()
	defer s.transport.Unlock()
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	s.mu.Lock()
	old := s.volume
	s.volume = v
	s.mu.Unlock()
	s.notify("mixer")
	st := s.Snapshot()
	if st.State == "play" || st.State == "pause" {
		if err := s.player.SetVolume(v); err != nil {
			s.mu.Lock()
			s.volume = old
			s.mu.Unlock()
			s.notify("mixer")
			return err
		}
	}
	return nil
}
func (s *State) Options(name string, v bool) {
	s.mu.Lock()
	switch name {
	case "repeat":
		s.repeat = v
	case "random":
		s.random = v
	case "single":
		s.single = v
	case "consume":
		s.consume = v
	}
	s.mu.Unlock()
	s.notify("options")
}

func (s *State) ClearError() bool {
	s.mu.Lock()
	changed := s.playbackError != ""
	s.playbackError = ""
	s.mu.Unlock()
	return changed
}

func (s *State) OutputEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outputEnabled
}

// SetOutputEnabled is serialized with backend starts and resumes.
func (s *State) SetOutputEnabled(enabled bool) {
	s.transport.Lock()
	s.mu.Lock()
	changed := s.outputEnabled != enabled
	s.outputEnabled = enabled
	if changed && !enabled {
		s.generation++
		s.request++
		s.state = "stop"
		s.elapsed = 0
		s.sleeping = false
	}
	s.mu.Unlock()
	if changed && !enabled {
		_ = s.player.Stop()
	}
	s.transport.Unlock()
	if changed && !enabled {
		s.notify("player")
		s.publishMedia()
	}
}

func (s *State) publishMedia() {
	s.mu.Lock()
	media := s.media
	s.mu.Unlock()
	if media == nil {
		return
	}
	st := s.Snapshot()
	info := sysmedia.PlayingInfo{State: st.State, Elapsed: st.Elapsed}
	if st.Current >= 0 && st.Current < len(st.Queue) {
		song := st.Queue[st.Current].Song
		info.TrackID = song.ID
		info.Title = song.Title
		info.Album = song.Album
		info.Artist = strings.Join(song.Artists, "/")
		info.CoverURL = song.CoverURL
		info.Duration = song.Duration
	}
	media.SetPlayingInfo(info)
}

// sysmedia.Controller implementation (macOS Now Playing remote commands).

func (s *State) CtrlPause() { _ = s.Pause(true) }
func (s *State) CtrlResume() {
	st := s.Snapshot()
	switch st.State {
	case "pause":
		_ = s.Pause(false)
	case "stop":
		_ = s.Play(-1)
	}
}
func (s *State) CtrlToggle() {
	st := s.Snapshot()
	if st.State == "play" {
		s.CtrlPause()
		return
	}
	s.CtrlResume()
}
func (s *State) CtrlStop()                { s.Stop() }
func (s *State) CtrlNext()                { _ = s.Next() }
func (s *State) CtrlPrevious()            { _ = s.Previous() }
func (s *State) CtrlSeek(d time.Duration) { _ = s.Seek(d) }
