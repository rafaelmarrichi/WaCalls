package player

import (
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// buildWAV assembles a WAV file in memory. extraChunk, when non-empty, is
// inserted between fmt and data to mimic what real audio tools emit.
func buildWAV(t *testing.T, samples []int16, chans uint16, rate uint32, bits uint16, format uint16, extraChunk []byte) []byte {
	t.Helper()

	data := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(s))
	}

	body := []byte{}
	body = append(body, "fmt "...)
	body = binary.LittleEndian.AppendUint32(body, 16)
	body = binary.LittleEndian.AppendUint16(body, format)
	body = binary.LittleEndian.AppendUint16(body, chans)
	body = binary.LittleEndian.AppendUint32(body, rate)
	body = binary.LittleEndian.AppendUint32(body, rate*uint32(chans)*uint32(bits)/8)
	body = binary.LittleEndian.AppendUint16(body, chans*bits/8)
	body = binary.LittleEndian.AppendUint16(body, bits)

	body = append(body, extraChunk...)

	body = append(body, "data"...)
	body = binary.LittleEndian.AppendUint32(body, uint32(len(data)))
	body = append(body, data...)

	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(4+len(body)))
	out = append(out, "WAVE"...)
	out = append(out, body...)
	return out
}

func ramp(n int) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = int16(i % 1000)
	}
	return s
}

func monoWAV(t *testing.T, n int) []byte {
	t.Helper()
	return buildWAV(t, ramp(n), 1, 16000, 16, 1, nil)
}

func TestDecodeAcceptsMono16k(t *testing.T) {
	pcm, err := decodeWAV(monoWAV(t, 800))
	if err != nil {
		t.Fatalf("decodeWAV: %v", err)
	}
	if len(pcm) != 800 {
		t.Fatalf("want 800 samples, got %d", len(pcm))
	}
	if pcm[0] != 0 {
		t.Errorf("first sample: want 0, got %v", pcm[0])
	}
	if want := float32(1) / 32768; pcm[1] != want {
		t.Errorf("second sample: want %v, got %v", want, pcm[1])
	}
}

// Files exported by ordinary audio tools carry extra chunks before the data. A
// parser that assumed a 44-byte header would decode those bytes as audio.
func TestDecodeSkipsExtraChunks(t *testing.T) {
	list := []byte("LIST")
	list = binary.LittleEndian.AppendUint32(list, 10)
	list = append(list, "INFOhello\x00"...)

	pcm, err := decodeWAV(buildWAV(t, ramp(320), 1, 16000, 16, 1, list))
	if err != nil {
		t.Fatalf("decodeWAV with a LIST chunk: %v", err)
	}
	if len(pcm) != 320 {
		t.Fatalf("want 320 samples, got %d", len(pcm))
	}
}

// An odd-sized chunk is followed by a pad byte. Missing that shifts every later
// chunk by one and the data chunk is never found.
func TestDecodeHandlesOddSizedChunkPadding(t *testing.T) {
	odd := []byte("note")
	odd = binary.LittleEndian.AppendUint32(odd, 3)
	odd = append(odd, 'a', 'b', 'c', 0) // 3 bytes plus the pad byte

	if _, err := decodeWAV(buildWAV(t, ramp(320), 1, 16000, 16, 1, odd)); err != nil {
		t.Fatalf("decodeWAV with an odd-sized chunk: %v", err)
	}
}

// Wrong format is rejected rather than resampled: playing a notice at the wrong
// speed is worse than failing, because the API turns a failure into a dropped
// call and a caller never hears a garbled legal notice.
func TestDecodeRejectsWrongFormats(t *testing.T) {
	cases := []struct {
		name  string
		build func() []byte
	}{
		{"estéreo", func() []byte { return buildWAV(t, ramp(640), 2, 16000, 16, 1, nil) }},
		{"8 kHz", func() []byte { return buildWAV(t, ramp(320), 1, 8000, 16, 1, nil) }},
		{"44,1 kHz", func() []byte { return buildWAV(t, ramp(320), 1, 44100, 16, 1, nil) }},
		{"8 bits", func() []byte { return buildWAV(t, ramp(320), 1, 16000, 8, 1, nil) }},
		{"não PCM", func() []byte { return buildWAV(t, ramp(320), 1, 16000, 16, 3, nil) }},
		{"vazio", func() []byte { return buildWAV(t, nil, 1, 16000, 16, 1, nil) }},
		{"não é WAV", func() []byte { return []byte("this is not audio at all") }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := decodeWAV(c.build()); err == nil {
				t.Fatal("expected the decoder to reject this file")
			}
		})
	}
}

// Asset names arrive over HTTP. A name that escapes the directory would let a
// caller read or overwrite any file the process can reach.
func TestLibraryRejectsUnsafeNames(t *testing.T) {
	lib := NewLibrary(t.TempDir())

	for _, name := range []string{
		"../../etc/passwd", "..", "/etc/passwd", "a/b", "", ".hidden",
		"nome com espaço", "aviso;rm -rf",
	} {
		if _, err := lib.Get(name); err == nil {
			t.Errorf("Get(%q) was accepted", name)
		}
		if _, err := lib.Store(name, monoWAV(t, 320)); err == nil {
			t.Errorf("Store(%q) was accepted", name)
		}
	}
}

func TestLibraryStoreThenGet(t *testing.T) {
	dir := t.TempDir()
	lib := NewLibrary(dir)

	ms, err := lib.Store("aviso-tenant1", monoWAV(t, 16000))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if ms != 1000 {
		t.Errorf("duration: want 1000 ms, got %d", ms)
	}

	if _, err := os.Stat(filepath.Join(dir, "aviso-tenant1.wav")); err != nil {
		t.Errorf("asset not on disk: %v", err)
	}

	pcm, err := lib.Get("aviso-tenant1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(pcm) != 16000 {
		t.Errorf("want 16000 samples, got %d", len(pcm))
	}

	// A re-upload has to replace what is cached, otherwise a tenant who fixes
	// their announcement keeps playing the old one until the process restarts.
	if _, err := lib.Store("aviso-tenant1", monoWAV(t, 8000)); err != nil {
		t.Fatalf("re-Store: %v", err)
	}
	pcm, err = lib.Get("aviso-tenant1")
	if err != nil {
		t.Fatalf("Get after re-Store: %v", err)
	}
	if len(pcm) != 8000 {
		t.Errorf("cache kept the old asset: %d samples", len(pcm))
	}

	if err := lib.Delete("aviso-tenant1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := lib.Get("aviso-tenant1"); err == nil {
		t.Error("Get succeeded after Delete")
	}
}

func TestLibraryRejectsBadUpload(t *testing.T) {
	lib := NewLibrary(t.TempDir())
	if _, err := lib.Store("aviso", buildWAV(t, ramp(320), 2, 44100, 16, 1, nil)); err == nil {
		t.Fatal("a stereo 44.1 kHz upload was accepted")
	}
	if _, err := lib.Get("aviso"); err == nil {
		t.Error("the rejected upload still landed in the library")
	}
}

func TestDisabledLibrary(t *testing.T) {
	lib := NewLibrary("")
	if lib.Enabled() {
		t.Error("a library with no directory reports itself enabled")
	}
	if _, err := lib.Get("aviso"); err != ErrNoLibrary {
		t.Errorf("Get: want ErrNoLibrary, got %v", err)
	}
	if _, err := lib.Store("aviso", monoWAV(t, 320)); err != ErrNoLibrary {
		t.Errorf("Store: want ErrNoLibrary, got %v", err)
	}
}

type sink struct {
	mu     sync.Mutex
	frames [][]float32
}

func (s *sink) push(pcm []float32) {
	s.mu.Lock()
	cp := make([]float32, len(pcm))
	copy(cp, pcm)
	s.frames = append(s.frames, cp)
	s.mu.Unlock()
}

func (s *sink) flat() []float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []float32
	for _, f := range s.frames {
		out = append(out, f...)
	}
	return out
}

func (s *sink) sizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, len(s.frames))
	for i, f := range s.frames {
		out[i] = len(f)
	}
	return out
}

// Every sample of the asset has to reach the line, in order, paced one frame per
// tick. Handing the whole asset over at once would overflow the capture buffer
// and play only its tail.
func TestPlayerPushesEverySampleInOrder(t *testing.T) {
	pcm, err := decodeWAV(monoWAV(t, 1000)) // three frames plus a short one
	if err != nil {
		t.Fatalf("decodeWAV: %v", err)
	}

	s := &sink{}
	var completed bool
	var playedMs int64
	doneCh := make(chan struct{})

	p := Start(pcm, Options{
		CallID: "c1", Log: quiet(), Sink: s.push,
		OnDone: func(ok bool, ms int64) { completed, playedMs = ok, ms; close(doneCh) },
	})

	if !p.Playing() {
		t.Error("Playing() is false right after Start")
	}
	if want := int64(62); p.DurationMs() != want {
		t.Errorf("DurationMs: want %d, got %d", want, p.DurationMs())
	}

	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("playback never finished")
	}

	if !completed {
		t.Error("OnDone reported an incomplete playback for an asset that ran to the end")
	}
	if playedMs != p.DurationMs() {
		t.Errorf("playedMs %d does not match the asset duration %d", playedMs, p.DurationMs())
	}
	if p.Playing() {
		t.Error("Playing() is still true after the asset ended")
	}

	got := s.flat()
	if len(got) != len(pcm) {
		t.Fatalf("sink received %d samples, asset has %d", len(got), len(pcm))
	}
	for i := range pcm {
		if got[i] != pcm[i] {
			t.Fatalf("sample %d differs: want %v, got %v", i, pcm[i], got[i])
		}
	}

	sizes := s.sizes()
	for i, n := range sizes[:len(sizes)-1] {
		if n != frameSamples {
			t.Errorf("frame %d has %d samples, want %d", i, n, frameSamples)
		}
	}
	if last := sizes[len(sizes)-1]; last != 1000%frameSamples {
		t.Errorf("last frame has %d samples, want %d", last, 1000%frameSamples)
	}
}

// Teardown can land in the middle of the announcement. It must cut, report the
// playback as incomplete, and hand the line back.
func TestStopCutsPlaybackAndReleasesTheLine(t *testing.T) {
	pcm, err := decodeWAV(monoWAV(t, 16000)) // one full second
	if err != nil {
		t.Fatalf("decodeWAV: %v", err)
	}

	s := &sink{}
	var completed bool
	var playedMs int64
	doneCh := make(chan struct{})

	p := Start(pcm, Options{
		CallID: "c1", Log: quiet(), Sink: s.push,
		OnDone: func(ok bool, ms int64) { completed, playedMs = ok, ms; close(doneCh) },
	})

	time.Sleep(60 * time.Millisecond)
	p.Stop()

	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not finish the playback")
	}
	p.Wait()

	if completed {
		t.Error("OnDone reported a complete playback for one that was cut short")
	}
	if p.Playing() {
		t.Error("the announcement still owns the line after Stop")
	}
	if n := len(s.flat()); n >= len(pcm) {
		t.Errorf("Stop did not cut anything: %d of %d samples played", n, len(pcm))
	}
	// playedMs is what tells the API how much of the notice the contact actually
	// heard, so a cut announcement has to report less than the full asset.
	if playedMs <= 0 || playedMs >= p.DurationMs() {
		t.Errorf("playedMs %d should be between zero and the full %d ms", playedMs, p.DurationMs())
	}

	// Teardown reaches Stop from more than one path.
	p.Stop()
	p.Stop()
}

// A call that ends during the announcement must not leave the playback goroutine
// behind. One leaked goroutine per abandoned call adds up fast on a dialer.
func TestStopDuringPlaybackLeavesNoGoroutine(t *testing.T) {
	pcm, err := decodeWAV(monoWAV(t, 160000)) // ten seconds
	if err != nil {
		t.Fatalf("decodeWAV: %v", err)
	}

	base := runtime.NumGoroutine()

	for range 20 {
		p := Start(pcm, Options{CallID: "c", Log: quiet(), Sink: func([]float32) {}})
		time.Sleep(2 * time.Millisecond)
		p.Stop()
		p.Wait()
	}

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base {
		t.Errorf("goroutine leak: baseline %d, now %d", base, n)
	}
}

// The observer accounting has to see the playback goroutine, so the upstream leak
// checks keep meaning something once announcements are in play.
func TestPlaybackGoroutineIsTracked(t *testing.T) {
	pcm, _ := decodeWAV(monoWAV(t, 3200))

	obs := &countingObserver{}
	p := Start(pcm, Options{CallID: "c", Log: quiet(), Observer: obs, Sink: func([]float32) {}})

	if obs.peak() == 0 {
		t.Fatal("the playback goroutine was never registered with the observer")
	}

	p.Stop()
	p.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for obs.live() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := obs.live(); n != 0 {
		t.Errorf("%d goroutines still registered after Stop", n)
	}
}
