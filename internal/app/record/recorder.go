package record

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"wacalls/internal/voip/core"
)

// A Recorder writes one stereo WAV per call: what we sent on the left channel,
// what the contact sent on the right.
//
// The hard problem is that the two sides arrive on independent clocks. The
// contact's audio comes with the RTP packets, in 60 ms frames, with loss and
// concealment. Our own audio comes from the browser data channel, and stops
// completely whenever no browser is attached, which is the normal state at the
// start of every dialer call. Interleaving the two buffers as they arrive
// accumulates drift and desynchronises the recording.
//
// So the Recorder owns a clock of its own. A goroutine wakes every 20 ms and
// always writes exactly one frame, taking 320 samples from each side and padding
// with silence whatever is missing. That keeps the file aligned with wall-clock
// time regardless of what arrived, and it is what puts the agent's voice at the
// right offset when they join a call already in progress.
//
// The number of frames written is derived from elapsed time rather than from the
// number of ticks received, because a starved goroutine makes a ticker drop
// ticks, and dropped ticks would silently shorten the file.

const (
	// One frame is 20 ms at 16 kHz.
	frameSamples   = 320
	tickInterval   = 20 * time.Millisecond
	framesPerFlush = 50 // one second of audio

	// Catch-up ceiling per wake-up, so a long stall cannot turn into an
	// unbounded burst of writes.
	maxCatchUpFrames = 50

	defaultBufferMs = 2000
	defaultMaxBytes = 200 << 20

	writeBufferBytes = 64 << 10
)

type Options struct {
	// Dir is the recording root. Files land in Dir/SessionID/CallID.wav.
	Dir       string
	SessionID string
	CallID    string
	Log       *slog.Logger
	// Observer registers the clock goroutine with the per-call accounting the
	// media stack uses, so the upstream leak checks stay meaningful.
	Observer core.CallObserver
	// MaxBytes caps the file. Recording stops and the result is flagged
	// truncated once the cap is reached. Zero means the default.
	MaxBytes int64
	// BufferMs is how much audio each side may hold before the oldest is
	// discarded. Zero means the default.
	BufferMs int

	// manual suppresses the clock goroutine so tests can drive frames one by
	// one. Unexported on purpose: only this package can set it, so production
	// callers cannot accidentally create a recorder that never writes.
	manual bool
}

// Result describes a finished recording.
type Result struct {
	Path       string
	DurationMs int64
	SizeBytes  int64
	SHA256     string
	// FramesDropped counts 20 ms frames worth of audio lost to buffer overflow,
	// summed over both sides. Non-zero means the recording is missing audio that
	// the call itself delivered fine.
	FramesDropped int64
	// Truncated is set when MaxBytes cut the recording short.
	Truncated bool
}

type Recorder struct {
	callID    string
	sessionID string
	path      string
	log       *slog.Logger
	maxBytes  int64
	// capacityFrames bounds the drain on close.
	capacityFrames int

	mu       sync.Mutex
	outbound *ring
	inbound  *ring

	file *os.File
	w    *bufio.Writer

	frames    atomic.Int64
	dataBytes atomic.Int64
	truncated atomic.Bool

	stop     chan struct{}
	stopOnce sync.Once
	finished chan struct{}

	closeOnce sync.Once
	result    Result
	closeErr  error
}

// New creates the file, writes the placeholder header and starts the clock.
func New(opts Options) (*Recorder, error) {
	if opts.Dir == "" {
		return nil, fmt.Errorf("record: empty directory")
	}
	if opts.CallID == "" {
		return nil, fmt.Errorf("record: empty call id")
	}

	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	bufferMs := opts.BufferMs
	if bufferMs <= 0 {
		bufferMs = defaultBufferMs
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}

	dir := filepath.Join(opts.Dir, opts.SessionID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("record: mkdir: %w", err)
	}

	path := filepath.Join(dir, opts.CallID+".wav")
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("record: create: %w", err)
	}

	w := bufio.NewWriterSize(f, writeBufferBytes)
	if err := writeHeader(w); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("record: header: %w", err)
	}

	capacity := bufferMs * sampleRate / 1000
	r := &Recorder{
		callID:         opts.CallID,
		sessionID:      opts.SessionID,
		path:           path,
		log:            log.With("call_id", opts.CallID, "recording", path),
		maxBytes:       maxBytes,
		capacityFrames: capacity / frameSamples,
		outbound:       newRing(capacity),
		inbound:        newRing(capacity),
		file:           f,
		w:              w,
		stop:           make(chan struct{}),
		finished:       make(chan struct{}),
	}

	if opts.manual {
		close(r.finished)
	} else {
		obs := opts.Observer
		if obs == nil {
			obs = core.NopObserver{}
		}
		done := obs.TrackGoroutine()
		go r.run(done)
	}

	r.log.Info("recording started")
	return r, nil
}

// Path is where this recording is being written.
func (r *Recorder) Path() string { return r.path }

// Buffered reports how many samples each side is holding, waiting for the next
// frame. Useful as a diagnostic, since a buffer that keeps growing means the
// clock goroutine is not keeping up with what arrives.
func (r *Recorder) Buffered() (outbound, inbound int) {
	if r == nil {
		return 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outbound.count, r.inbound.count
}

// WriteOutbound records audio on its way to the contact: the agent's microphone
// and any injected announcement. Both go through one funnel so the announcement
// is part of the recording, which is what makes the recording evidence that the
// announcement played.
func (r *Recorder) WriteOutbound(pcm []float32) {
	if r == nil || len(pcm) == 0 {
		return
	}
	r.mu.Lock()
	r.outbound.write(pcm)
	r.mu.Unlock()
}

// WriteInbound records audio received from the contact.
func (r *Recorder) WriteInbound(pcm []float32) {
	if r == nil || len(pcm) == 0 {
		return
	}
	r.mu.Lock()
	r.inbound.write(pcm)
	r.mu.Unlock()
}

func (r *Recorder) run(done func()) {
	defer done()
	defer close(r.finished)

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	start := time.Now()
	left := make([]float32, frameSamples)
	right := make([]float32, frameSamples)
	frame := make([]byte, frameSamples*bytesPerFrame)

	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
		}

		want := int64(time.Since(start) / tickInterval)
		for behind := 0; r.frames.Load() < want && behind < maxCatchUpFrames; behind++ {
			if !r.writeFrame(left, right, frame) {
				return
			}
		}
	}
}

// writeFrame emits exactly one 20 ms frame and reports whether recording should
// continue. Samples are copied out under the mutex and the disk write happens
// outside it, so a slow disk never stalls the media path.
func (r *Recorder) writeFrame(left, right []float32, frame []byte) bool {
	r.mu.Lock()
	r.outbound.read(left)
	r.inbound.read(right)
	r.mu.Unlock()

	interleave(frame, left, right)

	if _, err := r.w.Write(frame); err != nil {
		r.log.Error("recording write failed; stopping this recording", "err", err)
		r.truncated.Store(true)
		return false
	}

	n := r.frames.Add(1)
	total := r.dataBytes.Add(int64(len(frame)))

	if n%framesPerFlush == 0 {
		if err := r.w.Flush(); err != nil {
			r.log.Error("recording flush failed; stopping this recording", "err", err)
			r.truncated.Store(true)
			return false
		}
	}

	if total+headerSize >= r.maxBytes {
		r.log.Warn("recording hit the size cap; stopping this recording",
			"max_bytes", r.maxBytes, "frames", n)
		r.truncated.Store(true)
		return false
	}

	return true
}

// Close stops the clock, patches the header with the real sizes and hashes the
// file. Idempotent: the state machine reaches call teardown from more than one
// path, and every one of them closes the recording.
func (r *Recorder) Close() (Result, error) {
	if r == nil {
		return Result{}, nil
	}
	r.closeOnce.Do(func() {
		r.stopOnce.Do(func() { close(r.stop) })
		<-r.finished

		r.drain()
		r.result, r.closeErr = r.finalize()
	})
	return r.result, r.closeErr
}

// drain writes out whatever is still buffered when the call ends.
//
// Normally this is a frame or two, because the clock keeps up with what arrives.
// It matters when the clock fell behind: that audio is real, it is the end of the
// conversation, and stopping the clock without draining would throw it away. The
// cost is a file that can run slightly past wall-clock time, which is a better
// trade than losing the last words of a call.
func (r *Recorder) drain() {
	left := make([]float32, frameSamples)
	right := make([]float32, frameSamples)
	frame := make([]byte, frameSamples*bytesPerFrame)

	// Bounded by what the buffers can possibly hold, so a bug upstream of here
	// cannot turn teardown into an unbounded write.
	for range r.capacityFrames + 1 {
		out, in := r.Buffered()
		if out == 0 && in == 0 {
			return
		}
		if !r.writeFrame(left, right, frame) {
			return
		}
	}
}

func (r *Recorder) finalize() (Result, error) {
	frames := r.frames.Load()
	dataBytes := r.dataBytes.Load()

	r.mu.Lock()
	droppedOut := r.outbound.dropped
	droppedIn := r.inbound.dropped
	r.mu.Unlock()

	res := Result{
		Path:          r.path,
		DurationMs:    frames * int64(tickInterval/time.Millisecond),
		SizeBytes:     headerSize + dataBytes,
		FramesDropped: (droppedOut + droppedIn) / frameSamples,
		Truncated:     r.truncated.Load(),
	}

	if err := r.w.Flush(); err != nil {
		_ = r.file.Close()
		return res, fmt.Errorf("record: flush: %w", err)
	}
	if err := patchSizes(r.file, uint32(dataBytes)); err != nil {
		_ = r.file.Close()
		return res, fmt.Errorf("record: patch header: %w", err)
	}
	if err := r.file.Close(); err != nil {
		return res, fmt.Errorf("record: close: %w", err)
	}

	// Hashing reads the file back rather than hashing the stream, because the
	// header is rewritten after the fact and a streaming hash would not match
	// the bytes that actually end up on disk.
	sum, err := hashFile(r.path)
	if err != nil {
		return res, fmt.Errorf("record: hash: %w", err)
	}
	res.SHA256 = sum

	r.log.Info("recording finished",
		"duration_ms", res.DurationMs, "size_bytes", res.SizeBytes,
		"dropped_outbound_samples", droppedOut, "dropped_inbound_samples", droppedIn,
		"truncated", res.Truncated)

	return res, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
