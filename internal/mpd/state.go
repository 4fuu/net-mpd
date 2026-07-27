package mpd

import (
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/4fuu/net-mpd/internal/ncm"
	"github.com/4fuu/net-mpd/internal/player"
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
}
type eventSubscription struct {
	pending map[string]struct{}
	wake    chan struct{}
}
type State struct {
	mu                              sync.Mutex
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
	subs                            map[*eventSubscription]struct{}
}

func NewState(c *Catalog, p player.Player) *State {
	return &State{catalog: c, player: p, nextID: 1, current: -1, state: "stop", volume: 100, subs: map[*eventSubscription]struct{}{}}
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
func (s *State) Snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.elapsed
	if s.state == "play" {
		e += time.Since(s.started)
	}
	return Status{s.volume, s.repeat, s.random, s.single, s.consume, s.version, append([]QueueItem(nil), s.queue...), s.state, s.current, e}
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
	_ = s.player.Stop()
	s.mu.Lock()
	s.generation++
	s.request++
	s.queue = nil
	s.current = -1
	s.state = "stop"
	s.elapsed = 0
	s.version++
	s.mu.Unlock()
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
		s.generation++
		s.request++
	}
	s.version++
	s.mu.Unlock()
	if was {
		_ = s.player.Stop()
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
	info, err := s.catalog.Resolve(song)
	if err != nil {
		return fmt.Errorf("song is not playable")
	}
	s.mu.Lock()
	if request != s.request {
		s.mu.Unlock()
		return nil
	}
	pos = -1
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
	s.mu.Unlock()
	err = s.player.Play(info.URL, off, vol, func() { s.naturalEnd(gen) })
	if err != nil {
		s.mu.Lock()
		if gen == s.generation {
			s.state = "stop"
			s.elapsed = 0
		}
		s.mu.Unlock()
		s.notify("player")
		return err
	}
	s.notify("player")
	return nil
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
	s.mu.Unlock()
	if consumed {
		s.notify("playlist")
	}
	if pos >= 0 {
		if err := s.playAt(pos, 0); err != nil {
			s.notify("player")
		}
	} else {
		s.notify("player")
	}
}
func (s *State) Stop() {
	_ = s.player.Stop()
	s.mu.Lock()
	s.generation++
	s.request++
	s.state = "stop"
	s.elapsed = 0
	s.mu.Unlock()
	s.notify("player")
}
func (s *State) Pause(on bool) error {
	st := s.Snapshot()
	if on {
		if st.State == "play" {
			_ = s.player.Stop()
			s.mu.Lock()
			s.generation++
			s.request++
			s.elapsed = st.Elapsed
			s.state = "pause"
			s.mu.Unlock()
			s.notify("player")
		}
		return nil
	}
	if st.State == "pause" {
		return s.playAt(st.Current, st.Elapsed)
	}
	return nil
}
func (s *State) Seek(off time.Duration) error {
	st := s.Snapshot()
	if st.Current < 0 {
		return fmt.Errorf("no current song")
	}
	return s.playAt(st.Current, off)
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
	if st.State == "play" {
		if err := s.playAt(st.Current, st.Elapsed); err != nil {
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
