package player

import (
	"sync"

	"wacalls/internal/voip/core"
)

// countingObserver tracks goroutine registration the same way the upstream leak
// test does, so playback is held to the same accounting as the media stack.
type countingObserver struct {
	mu       sync.Mutex
	gor      int64
	gorPeak  int64
	memTally int64
}

func (o *countingObserver) TrackGoroutine() func() {
	o.mu.Lock()
	o.gor++
	if o.gor > o.gorPeak {
		o.gorPeak = o.gor
	}
	o.mu.Unlock()
	return func() {
		o.mu.Lock()
		o.gor--
		o.mu.Unlock()
	}
}

func (o *countingObserver) AddMem(b int64) { o.mu.Lock(); o.memTally += b; o.mu.Unlock() }
func (o *countingObserver) ReleaseMem(b int64) {
	o.mu.Lock()
	o.memTally -= b
	o.mu.Unlock()
}
func (o *countingObserver) Mark(string)                  {}
func (o *countingObserver) SrtpRecvDrop(string)          {}
func (o *countingObserver) NoteQuality(core.CallQuality) {}
func (o *countingObserver) End(string, string)           {}

func (o *countingObserver) live() int64 { o.mu.Lock(); defer o.mu.Unlock(); return o.gor }
func (o *countingObserver) peak() int64 { o.mu.Lock(); defer o.mu.Unlock(); return o.gorPeak }

var _ core.CallObserver = (*countingObserver)(nil)
