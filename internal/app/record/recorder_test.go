package record

import (
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newManual(t *testing.T) *Recorder {
	t.Helper()
	r, err := New(Options{
		Dir:       t.TempDir(),
		SessionID: "sess-1",
		CallID:    "call-abc",
		Log:       quiet(),
		manual:    true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// tick drives n frames through the recorder without the clock goroutine, so the
// tests are deterministic instead of sleeping for real durations.
func tick(t *testing.T, r *Recorder, n int) {
	t.Helper()
	left := make([]float32, frameSamples)
	right := make([]float32, frameSamples)
	frame := make([]byte, frameSamples*bytesPerFrame)
	for i := range n {
		if !r.writeFrame(left, right, frame) {
			t.Fatalf("writeFrame stopped early at frame %d of %d", i, n)
		}
	}
}

func tone(n int, level float32) []float32 {
	pcm := make([]float32, n)
	for i := range pcm {
		pcm[i] = level
	}
	return pcm
}

type decoded struct {
	left, right []int16
	riffSize    uint32
	dataSize    uint32
	channels    uint16
	sampleRate  uint32
}

func readWAV(t *testing.T, path string) decoded {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(raw) < headerSize {
		t.Fatalf("file shorter than a WAV header: %d bytes", len(raw))
	}
	if string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		t.Fatalf("not a RIFF/WAVE file: %q", raw[0:12])
	}

	d := decoded{
		riffSize:   binary.LittleEndian.Uint32(raw[offsetRiffSize:]),
		dataSize:   binary.LittleEndian.Uint32(raw[offsetDataSize:]),
		channels:   binary.LittleEndian.Uint16(raw[22:]),
		sampleRate: binary.LittleEndian.Uint32(raw[24:]),
	}

	body := raw[headerSize:]
	for i := 0; i+3 < len(body); i += 4 {
		d.left = append(d.left, int16(binary.LittleEndian.Uint16(body[i:])))
		d.right = append(d.right, int16(binary.LittleEndian.Uint16(body[i+2:])))
	}
	return d
}

func allZero(samples []int16) bool {
	for _, s := range samples {
		if s != 0 {
			return false
		}
	}
	return true
}

// A frame with only one side feeding must put silence in the other channel, not
// shift the audio that did arrive. This is the whole reason the recorder keeps a
// clock instead of interleaving whatever shows up.
func TestFrameWithOneSideEmptyIsSilentInThatChannel(t *testing.T) {
	r := newManual(t)

	r.WriteInbound(tone(frameSamples, 0.5))
	tick(t, r, 1)

	if _, err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	d := readWAV(t, r.Path())
	if len(d.left) != frameSamples {
		t.Fatalf("expected %d sample pairs, got %d", frameSamples, len(d.left))
	}
	if !allZero(d.left) {
		t.Error("outbound side never fed, but the left channel is not silent")
	}
	if allZero(d.right) {
		t.Error("inbound audio was fed, but the right channel is silent")
	}
}

// The case the dialer depends on: nobody is in the audio when the contact
// answers, and the agent joins some seconds later. Their voice must land at the
// offset where they actually joined, not at the beginning of the file.
func TestAgentJoiningMidCallLandsAtTheRightOffset(t *testing.T) {
	r := newManual(t)

	const framesBefore = 1500 // 30 s at 20 ms per frame

	// Thirty seconds of the contact talking alone.
	for range framesBefore {
		r.WriteInbound(tone(frameSamples, 0.4))
		tick(t, r, 1)
	}

	// The agent attaches and starts talking.
	for range 100 {
		r.WriteInbound(tone(frameSamples, 0.4))
		r.WriteOutbound(tone(frameSamples, 0.6))
		tick(t, r, 1)
	}

	if _, err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	d := readWAV(t, r.Path())

	before := framesBefore * frameSamples
	if !allZero(d.left[:before]) {
		t.Error("agent audio leaked into the first 30 seconds, where they were not connected")
	}
	if allZero(d.left[before:]) {
		t.Error("agent audio missing after they joined")
	}
	if allZero(d.right[:before]) {
		t.Error("contact audio missing from the first 30 seconds")
	}

	wantMs := int64((framesBefore + 100) * 20)
	res, _ := r.Close() // idempotent, returns the stored result
	if res.DurationMs != wantMs {
		t.Errorf("duration: want %d ms, got %d", wantMs, res.DurationMs)
	}
}

// Overflow must cost the oldest audio and be counted, never block the media path.
func TestRingOverflowDropsOldestAndCounts(t *testing.T) {
	r := newManual(t)

	// A 100 ms buffer holds 1600 samples; feed four times that.
	r2, err := New(Options{Dir: t.TempDir(), SessionID: "s", CallID: "c", Log: quiet(), manual: true, BufferMs: 100})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _, _ = r2.Close() }()
	_, _ = r.Close()

	capacity := 100 * sampleRate / 1000
	for range 4 {
		r2.WriteInbound(tone(capacity, 0.5))
	}

	r2.mu.Lock()
	dropped := r2.inbound.dropped
	held := r2.inbound.count
	r2.mu.Unlock()

	if dropped != int64(3*capacity) {
		t.Errorf("dropped: want %d samples, got %d", 3*capacity, dropped)
	}
	if held != capacity {
		t.Errorf("held: want %d samples, got %d", capacity, held)
	}

	tick(t, r2, capacity/frameSamples)
	res, err := r2.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if res.FramesDropped == 0 {
		t.Error("audio was dropped but the result reports zero frames lost")
	}
}

// The header carries two sizes that are only known at the end. If the rewrite is
// wrong the file plays as truncated, or not at all, with no error anywhere.
func TestHeaderMatchesFileSize(t *testing.T) {
	r := newManual(t)

	const frames = 137
	for range frames {
		r.WriteOutbound(tone(frameSamples, 0.2))
		r.WriteInbound(tone(frameSamples, 0.3))
		tick(t, r, 1)
	}

	res, err := r.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	info, err := os.Stat(r.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	d := readWAV(t, r.Path())
	wantData := uint32(frames * frameSamples * bytesPerFrame)

	if d.dataSize != wantData {
		t.Errorf("data size in header: want %d, got %d", wantData, d.dataSize)
	}
	if got := d.riffSize + 8; got != uint32(info.Size()) {
		t.Errorf("riff size in header implies %d bytes, file is %d", got, info.Size())
	}
	if res.SizeBytes != info.Size() {
		t.Errorf("result size %d does not match file size %d", res.SizeBytes, info.Size())
	}
	if d.channels != channels || d.sampleRate != sampleRate {
		t.Errorf("format: want %d channels at %d Hz, got %d at %d", channels, sampleRate, d.channels, d.sampleRate)
	}
	if res.SHA256 == "" {
		t.Error("no sha256 in the result")
	}
}

// Reading our own header proves it is self-consistent. This proves a real decoder
// agrees, which is what matters when the file reaches a browser or ffmpeg.
func TestFfprobeSeesStereo16k(t *testing.T) {
	bin, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}

	r := newManual(t)
	for range 50 {
		r.WriteOutbound(tone(frameSamples, 0.5))
		r.WriteInbound(tone(frameSamples, -0.5))
		tick(t, r, 1)
	}
	if _, err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	out, err := exec.Command(bin, "-v", "error",
		"-show_entries", "stream=channels,sample_rate,codec_name",
		"-of", "csv=p=0", r.Path()).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}

	got := strings.TrimSpace(string(out))
	if !strings.Contains(got, "pcm_s16le") || !strings.Contains(got, "16000") || !strings.Contains(got, "2") {
		t.Errorf("ffprobe disagrees with our header: %q", got)
	}
}

// Audio still buffered when a call ends is the end of the conversation. Stopping
// the clock and closing the file without writing it out loses exactly the part
// most likely to matter.
func TestCloseDrainsWhatIsStillBuffered(t *testing.T) {
	r := newManual(t)

	// Fed but never ticked, so all of it is still in the buffers.
	const frames = 8
	for range frames {
		r.WriteOutbound(tone(frameSamples, 0.5))
		r.WriteInbound(tone(frameSamples, -0.5))
	}

	out, in := r.Buffered()
	if out != frames*frameSamples || in != frames*frameSamples {
		t.Fatalf("expected %d samples buffered per side, got %d and %d",
			frames*frameSamples, out, in)
	}

	res, err := r.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	if want := int64(frames * 20); res.DurationMs != want {
		t.Errorf("duration: want %d ms of drained audio, got %d", want, res.DurationMs)
	}

	d := readWAV(t, r.Path())
	if len(d.left) != frames*frameSamples {
		t.Fatalf("want %d sample pairs, got %d", frames*frameSamples, len(d.left))
	}
	if allZero(d.left) || allZero(d.right) {
		t.Error("buffered audio was dropped instead of written on close")
	}
}

// Teardown reaches Close from more than one path in the session state machine.
func TestCloseIsIdempotent(t *testing.T) {
	r := newManual(t)
	tick(t, r, 3)

	first, err := r.Close()
	if err != nil {
		t.Fatalf("first Close: %v", err)
	}
	second, err := r.Close()
	if err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if first != second {
		t.Errorf("second Close returned a different result:\n first  %+v\n second %+v", first, second)
	}
}

// The clock derives frame count from elapsed time, so a starved goroutine cannot
// silently shorten the recording. With the real ticker running, the file length
// must track wall-clock time.
func TestRealClockTracksWallClock(t *testing.T) {
	r, err := New(Options{Dir: t.TempDir(), SessionID: "s", CallID: "c", Log: quiet()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const elapsed = 400 * time.Millisecond
	deadline := time.Now().Add(elapsed)
	for time.Now().Before(deadline) {
		r.WriteInbound(tone(frameSamples, 0.5))
		time.Sleep(20 * time.Millisecond)
	}

	res, err := r.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	wantMs := elapsed.Milliseconds()
	// Two frames of slack in each direction for scheduler noise.
	if res.DurationMs < wantMs-40 || res.DurationMs > wantMs+40 {
		t.Errorf("recorded %d ms for %d ms of wall clock", res.DurationMs, wantMs)
	}
}

// The cap exists so one stuck call cannot fill the disk.
func TestSizeCapStopsRecording(t *testing.T) {
	r, err := New(Options{
		Dir: t.TempDir(), SessionID: "s", CallID: "c", Log: quiet(), manual: true,
		MaxBytes: headerSize + 10*frameSamples*bytesPerFrame,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	left := make([]float32, frameSamples)
	right := make([]float32, frameSamples)
	frame := make([]byte, frameSamples*bytesPerFrame)

	written := 0
	for range 50 {
		if !r.writeFrame(left, right, frame) {
			break
		}
		written++
	}

	if written >= 50 {
		t.Fatal("the size cap never stopped the recording")
	}

	res, err := r.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !res.Truncated {
		t.Error("recording stopped at the cap but the result is not flagged truncated")
	}
}
