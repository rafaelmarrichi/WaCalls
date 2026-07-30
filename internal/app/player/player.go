package player

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"wacalls/internal/voip/core"
)

// A Player pushes a pre-recorded announcement into a live call.
//
// Two reasons this exists. The legal one: no call may be recorded without the
// contact being told, so the announcement has to reach the line before the agent
// does. The practical one: a contact who answers and hears absolute silence hangs
// up, and silence is exactly what the dialer produces between the moment someone
// answers and the moment an agent attaches.
//
// The samples are paced rather than dumped. The audio extension keeps a small
// capture buffer and discards the excess, so handing it five seconds at once
// would play only the last fraction of it. One 20 ms frame per 20 ms tick is the
// rate the send loop drains at.

const (
	frameSamples = 320
	tickInterval = 20 * time.Millisecond
)

type Options struct {
	CallID string
	Log    *slog.Logger
	// Sink receives each frame on its way to the contact.
	Sink func([]float32)
	// OnDone runs once when playback finishes or is stopped, whichever comes
	// first. Used to publish the announcement-finished event and to hand the
	// line back to the agent's microphone. playedMs is how much actually
	// reached the line, which is what tells a cut-short announcement from a
	// complete one when completed is false.
	OnDone func(completed bool, playedMs int64)
	// Observer registers the playback goroutine with the per-call accounting.
	Observer core.CallObserver
}

type Player struct {
	callID  string
	log     *slog.Logger
	total   int
	playing atomic.Bool

	stop     chan struct{}
	stopOnce sync.Once
	finished chan struct{}
}

// Start begins playing samples and returns immediately.
func Start(pcm []float32, opts Options) *Player {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	p := &Player{
		callID:   opts.CallID,
		log:      log.With("call_id", opts.CallID),
		total:    len(pcm),
		stop:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	p.playing.Store(true)

	obs := opts.Observer
	if obs == nil {
		obs = core.NopObserver{}
	}
	done := obs.TrackGoroutine()

	go p.run(pcm, opts.Sink, opts.OnDone, done)
	return p
}

// DurationMs is how long the whole asset takes to play. The caller needs this to
// know when the line is free for the agent.
func (p *Player) DurationMs() int64 { return durationMs(p.total) }

// Playing reports whether the announcement still owns the line. While it does,
// the agent's microphone is dropped rather than mixed in, so a notice cannot be
// heard over someone talking.
func (p *Player) Playing() bool { return p != nil && p.playing.Load() }

// Stop ends playback early. Idempotent, and safe to call after it finished on its
// own, because call teardown always stops the player and may race with the end of
// the asset.
//
// Deliberately does not wait for the goroutine: OnDone runs on that goroutine, so
// a callback that ends up calling Stop would deadlock against its own completion.
// Use Wait when the caller genuinely needs playback to be over.
func (p *Player) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() { close(p.stop) })
}

// Wait blocks until playback has finished and OnDone has run.
func (p *Player) Wait() {
	if p == nil {
		return
	}
	<-p.finished
}

func (p *Player) run(pcm []float32, sink func([]float32), onDone func(bool, int64), done func()) {
	defer done()
	defer close(p.finished)

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	stopped := false
	offset := 0

	for offset < len(pcm) && !stopped {
		select {
		case <-p.stop:
			stopped = true
		case <-ticker.C:
			end := min(offset+frameSamples, len(pcm))
			if sink != nil {
				// The last frame is short; the audio extension buffers by length,
				// so a partial frame needs no padding.
				sink(pcm[offset:end])
			}
			offset = end
		}
	}

	completed := !stopped
	if completed {
		p.log.Info("announcement played", "duration_ms", durationMs(len(pcm)))
	} else {
		p.log.Info("announcement stopped before the end", "played_ms", durationMs(offset))
	}

	// Cleared before OnDone so a callback that re-enables the microphone cannot
	// observe the line as still owned by the announcement.
	p.playing.Store(false)
	if onDone != nil {
		onDone(completed, durationMs(offset))
	}
}
