package session

import (
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"wacalls/internal/app/player"
	"wacalls/internal/voip/call"
)

// These tests cover the wiring rather than the recorder or the player, which have
// their own. What is specific to this layer is the funnel: everything on its way
// to the contact goes through one function, and that is what makes the
// announcement part of the recording and keeps the microphone out of it while the
// announcement plays.

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// monoWAV builds a valid asset: mono, 16 kHz, 16-bit PCM.
func monoWAV(samples int) []byte {
	data := make([]byte, samples*2)
	for i := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(int16(8000)))
	}

	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(36+len(data)))
	out = append(out, "WAVE"...)
	out = append(out, "fmt "...)
	out = binary.LittleEndian.AppendUint32(out, 16)
	out = binary.LittleEndian.AppendUint16(out, 1)
	out = binary.LittleEndian.AppendUint16(out, 1)
	out = binary.LittleEndian.AppendUint32(out, 16000)
	out = binary.LittleEndian.AppendUint32(out, 32000)
	out = binary.LittleEndian.AppendUint16(out, 2)
	out = binary.LittleEndian.AppendUint16(out, 16)
	out = append(out, "data"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	return append(out, data...)
}

// sessionWithAudio builds a session whose manager has recording and
// announcements switched on.
func sessionWithAudio(t *testing.T) (*Session, string, *player.Library) {
	t.Helper()

	recordDir := t.TempDir()
	lib := player.NewLibrary(t.TempDir())

	m := newTestManager(t)
	m.audioCfg = AudioConfig{RecordDir: recordDir, Library: lib}

	s := m.addUnconnected(t, "gravação")
	s.log = quiet()

	return s, recordDir, lib
}

// wavChannels reads back the left and right channels of a finished recording.
func wavChannels(t *testing.T, path string) (left, right []int16) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(raw) < 44 {
		t.Fatalf("file is shorter than a WAV header: %d bytes", len(raw))
	}
	body := raw[44:]
	for i := 0; i+3 < len(body); i += 4 {
		left = append(left, int16(binary.LittleEndian.Uint16(body[i:])))
		right = append(right, int16(binary.LittleEndian.Uint16(body[i+2:])))
	}
	return left, right
}

func anyNonZero(samples []int16) bool {
	for _, s := range samples {
		if s != 0 {
			return true
		}
	}
	return false
}

func tone(n int, level float32) []float32 {
	pcm := make([]float32, n)
	for i := range pcm {
		pcm[i] = level
	}
	return pcm
}

// The recorder is only opened when a call connects, so a call that was never
// answered must not leave an empty file behind. On a dialer that is most calls.
func TestNoRecordingBeforeTheCallConnects(t *testing.T) {
	s, recordDir, _ := sessionWithAudio(t)

	// Audio arriving with no recorder open must be dropped, not crash.
	s.feedInbound("call-never-answered", tone(320, 0.5))
	s.feedOutbound("call-never-answered", tone(320, 0.5), true)

	entries, err := os.ReadDir(filepath.Join(recordDir, s.ID()))
	if err == nil && len(entries) > 0 {
		t.Errorf("a call that never connected left %d file(s) on disk", len(entries))
	}
}

// With recording switched off nothing must be written, and the audio path has to
// keep working.
func TestRecordingDisabledWritesNothing(t *testing.T) {
	m := newTestManager(t)
	s := m.addUnconnected(t, "sem gravação")
	s.log = quiet()

	cm := call.NewCallManager(nil, quiet())
	s.startRecording("call-1", cm)

	if s.recorderFor("call-1") != nil {
		t.Error("a recorder was opened with recording switched off")
	}

	s.feedInbound("call-1", tone(320, 0.5))
	s.feedOutbound("call-1", tone(320, 0.5), true)
	s.teardownCallAudio("call-1")
}

// Reconnects report the connected state again. A second recorder would open a
// second file over the first and lose the call.
func TestStartRecordingIsIdempotent(t *testing.T) {
	s, _, _ := sessionWithAudio(t)
	cm := call.NewCallManager(nil, quiet())

	s.startRecording("call-1", cm)
	first := s.recorderFor("call-1")
	if first == nil {
		t.Fatal("no recorder after the call connected")
	}

	s.startRecording("call-1", cm)
	if second := s.recorderFor("call-1"); second != first {
		t.Error("the connected state being reported twice opened a second recorder")
	}

	s.teardownCallAudio("call-1")
}

// The announcement has to be in the recording. It is the evidence that the
// contact was told the call was being recorded, and the obvious wiring (recording
// the microphone) leaves it out, because the announcement is injected on a
// different path.
func TestAnnouncementLandsInTheRecording(t *testing.T) {
	s, recordDir, lib := sessionWithAudio(t)

	if _, err := lib.Store("aviso", monoWAV(1600)); err != nil {
		t.Fatalf("Store: %v", err)
	}

	cm := call.NewCallManager(nil, quiet())
	s.startRecording("call-1", cm)

	rec := s.recorderFor("call-1")
	if rec == nil {
		t.Fatal("no recorder")
	}

	// What the announcement does: outbound audio that is not the microphone.
	pcm, err := lib.Get("aviso")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for off := 0; off < len(pcm); off += 320 {
		end := min(off+320, len(pcm))
		s.feedOutbound("call-1", pcm[off:end], false)
	}

	s.teardownCallAudio("call-1")

	left, _ := wavChannels(t, filepath.Join(recordDir, s.ID(), "call-1.wav"))
	if !anyNonZero(left) {
		t.Error("the announcement is not in the recording, so the recording does not prove it played")
	}
}

// While the announcement plays, the microphone is dropped rather than mixed in.
// An agent already attached and talking would otherwise be heard over the notice.
func TestMicrophoneIsDroppedWhileAnnouncing(t *testing.T) {
	s, _, _ := sessionWithAudio(t)

	cm := call.NewCallManager(nil, quiet())
	s.startRecording("call-1", cm)

	// A long asset with no sink: all this needs is a player that reports itself
	// as playing, which is what feedOutbound checks.
	p := player.Start(tone(160000, 0.5), player.Options{
		CallID: "call-1", Log: quiet(), Sink: func([]float32) {},
	})
	s.audio.mu.Lock()
	s.audio.players["call-1"] = p
	s.audio.mu.Unlock()

	if !s.announcing("call-1") {
		t.Fatal("the announcement does not own the line")
	}

	// The agent talks over the notice. This must not reach the recording.
	for range 25 {
		s.feedOutbound("call-1", tone(320, 0.9), true)
	}

	p.Stop()
	p.Wait()
	s.audio.mu.Lock()
	delete(s.audio.players, "call-1")
	s.audio.mu.Unlock()

	if s.announcing("call-1") {
		t.Fatal("the line was not handed back after the announcement stopped")
	}

	rec := s.recorderFor("call-1")
	if rec == nil {
		t.Fatal("no recorder")
	}

	// The clock drains the buffer as it writes frames, so this reads what has not
	// been written yet. What matters is the comparison: dropped audio can never
	// show up here, and audio that got through does, at least briefly.
	if out, _ := rec.Buffered(); out != 0 {
		t.Errorf("%d samples of microphone audio reached the recorder during the announcement", out)
	}

	// And once the announcement is over, the microphone is back.
	for range 25 {
		s.feedOutbound("call-1", tone(320, 0.9), true)
	}
	if out, _ := rec.Buffered(); out == 0 {
		t.Error("the microphone never came back after the announcement")
	}

	s.teardownCallAudio("call-1")
}

// The agent joins during the announcement, so they have to hear it. Without this
// they would sit in silence with no idea when they may start talking.
func TestAnnouncementAlsoGoesToTheBrowserLeg(t *testing.T) {
	s, _, _ := sessionWithAudio(t)

	cm := call.NewCallManager(nil, quiet())
	s.startRecording("call-1", cm)

	// Sem perna de navegador anexada, nada pode estourar: é o estado normal no
	// começo de toda chamada da discadora.
	s.feedOutbound("call-1", tone(320, 0.5), false)

	// O caminho do microfone não pode ecoar de volta para o fone do atendente.
	// Aqui só é possível checar que a chamada não quebra; o eco em si é evitado
	// por o ramo só valer para áudio injetado.
	s.feedOutbound("call-1", tone(320, 0.5), true)

	rec := s.recorderFor("call-1")
	if rec == nil {
		t.Fatal("no recorder")
	}
	if out, _ := rec.Buffered(); out == 0 {
		t.Error("nada chegou ao canal de saída da gravação")
	}

	s.teardownCallAudio("call-1")
}

// Teardown reaches the audio from more than one path, and every one of them runs
// for an ordinary hang-up.
func TestTeardownIsIdempotentAndClosesTheFile(t *testing.T) {
	s, recordDir, _ := sessionWithAudio(t)

	cm := call.NewCallManager(nil, quiet())
	s.startRecording("call-1", cm)
	s.feedInbound("call-1", tone(3200, 0.4))

	s.teardownCallAudio("call-1")
	s.teardownCallAudio("call-1")
	s.teardownAllCallAudio()

	if s.recorderFor("call-1") != nil {
		t.Error("the recorder is still registered after teardown")
	}

	path := filepath.Join(recordDir, s.ID(), "call-1.wav")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// A closed recording has its two size fields rewritten, so the header agrees
	// with the file on disk.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	riff := binary.LittleEndian.Uint32(raw[4:])
	if int64(riff)+8 != info.Size() {
		t.Errorf("header says %d bytes, file is %d: the recording was not closed properly",
			riff+8, info.Size())
	}
}

// Session shutdown drains calls without going through removeCall, so anything
// still open there would leak a file handle and a goroutine per call.
func TestTeardownAllReleasesEveryCall(t *testing.T) {
	s, _, _ := sessionWithAudio(t)
	cm := call.NewCallManager(nil, quiet())

	for _, id := range []string{"call-1", "call-2", "call-3"} {
		s.startRecording(id, cm)
		s.feedInbound(id, tone(320, 0.3))
	}

	s.teardownAllCallAudio()

	s.audio.mu.Lock()
	remaining := len(s.audio.recorders) + len(s.audio.players)
	s.audio.mu.Unlock()

	if remaining != 0 {
		t.Errorf("%d call(s) still holding audio resources after shutdown", remaining)
	}
}
