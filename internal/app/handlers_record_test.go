package app

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wacalls/internal/app/events"
	"wacalls/internal/app/player"
	"wacalls/internal/app/session"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

func recordServer(t *testing.T, recordDir, audioDir string) *Server {
	t.Helper()
	b := events.NewBroker(nil, slog.Default())
	audio := session.AudioConfig{
		RecordDir: recordDir,
		Library:   player.NewLibrary(audioDir),
	}
	mgr := session.NewManager(session.Deps{Broker: b, Log: slog.Default(), Audio: audio})
	mgr.NewSession("s1", "", &whatsmeow.Client{Store: &store.Device{}})
	return &Server{
		authorize: bearerAuthorizer(""),
		broker:    b,
		sessions:  mgr,
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		audio:     audio,
	}
}

// monoAsset builds a valid announcement asset: mono, 16 kHz, 16-bit PCM.
func monoAsset(samples int) []byte {
	data := make([]byte, samples*2)
	for i := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(int16(i%3000)))
	}

	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(36+len(data)))
	out = append(out, "WAVE"...)
	out = append(out, "fmt "...)
	out = binary.LittleEndian.AppendUint32(out, 16)
	out = binary.LittleEndian.AppendUint16(out, 1)     // PCM
	out = binary.LittleEndian.AppendUint16(out, 1)     // mono
	out = binary.LittleEndian.AppendUint32(out, 16000) // 16 kHz
	out = binary.LittleEndian.AppendUint32(out, 32000) // byte rate
	out = binary.LittleEndian.AppendUint16(out, 2)     // block align
	out = binary.LittleEndian.AppendUint16(out, 16)    // bits
	out = append(out, "data"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	return append(out, data...)
}

// The call id reaches the filesystem, so a name that walks out of the recording
// directory would expose any file the process can read.
func TestRecordingPathRejectsTraversal(t *testing.T) {
	s := recordServer(t, "/data/recordings", "")

	for _, callID := range []string{
		"../../etc/passwd", "..", "a/b", "", ".hidden", "id with space", "id;rm",
	} {
		path, unconfigured, badID := s.recordingPath("s1", callID)
		if !badID {
			t.Errorf("call id %q was accepted and resolved to %s", callID, path)
		}
		if unconfigured {
			t.Errorf("call id %q reported as unconfigured instead of malformed", callID)
		}
	}

	for _, sessionID := range []string{"../other", "..", "s/1", ""} {
		path, _, badID := s.recordingPath(sessionID, "call1")
		if !badID {
			t.Errorf("session id %q was accepted and resolved to %s", sessionID, path)
		}
	}

	path, unconfigured, badID := s.recordingPath("s1", "call-abc")
	if unconfigured || badID {
		t.Fatal("a legitimate pair was rejected")
	}
	if want := filepath.Join("/data/recordings", "s1", "call-abc.wav"); path != want {
		t.Errorf("path: want %s, got %s", want, path)
	}
}

func TestRecordingDownload(t *testing.T) {
	dir := t.TempDir()
	s := recordServer(t, dir, "")

	if err := os.MkdirAll(filepath.Join(dir, "s1"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte("RIFF....WAVEfake recording bytes")
	if err := os.WriteFile(filepath.Join(dir, "s1", "call-1.wav"), body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/s1/calls/call-1/recording", nil))

	if rec.Code != 200 {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != string(body) {
		t.Errorf("body: want %q, got %q", body, got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("content type: want audio/wav, got %q", ct)
	}
}

func TestRecordingOfUnknownCallIs404(t *testing.T) {
	s := recordServer(t, t.TempDir(), "")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/s1/calls/ghost/recording", nil))
	if rec.Code != 404 {
		t.Fatalf("want 404, got %d %s", rec.Code, rec.Body.String())
	}
}

// With recording switched off there is no directory to serve from, and saying so
// beats a confusing 500.
//
// The message has to name the configuration, because the caller uses it to tell
// a misconfigured engine (worth alerting on) from a call that simply has no
// recording (routine). Reporting both the same way made them indistinguishable.
func TestRecordingDisabledSaysSo(t *testing.T) {
	s := recordServer(t, "", "")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/s1/calls/call-1/recording", nil))

	if rec.Code != 404 {
		t.Fatalf("want 404, got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Errorf("body should say recording is not configured, got %s", rec.Body.String())
	}
}

// A malformed id is a bad request, not a missing recording. Same reason: the
// caller has to be able to tell them apart.
func TestMalformedIdOnRecordingIs400(t *testing.T) {
	s := recordServer(t, t.TempDir(), "")

	// %2e%2e%2f is "../" encoded, which survives the mux without being collapsed.
	for _, path := range []string{
		"/api/sessions/s1/calls/%2e%2e%2f%2e%2e%2fetc%2fpasswd/recording",
		"/api/sessions/s1/calls/id%20com%20espaco/recording",
	} {
		rec := httptest.NewRecorder()
		s.routes().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 400 {
			t.Errorf("GET %s: want 400, got %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

// Deleting is what the consumer does after storing the file elsewhere, and a
// retry after a successful delete must not look like a failure.
func TestRecordingDeleteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := recordServer(t, dir, "")

	if err := os.MkdirAll(filepath.Join(dir, "s1"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "s1", "call-1.wav")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	for attempt := range 2 {
		rec := httptest.NewRecorder()
		s.routes().ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/sessions/s1/calls/call-1/recording", nil))
		if rec.Code != 204 {
			t.Fatalf("attempt %d: want 204, got %d %s", attempt, rec.Code, rec.Body.String())
		}
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the file is still on disk after the delete")
	}
}

func TestAudioUpload(t *testing.T) {
	audioDir := t.TempDir()
	s := recordServer(t, "", audioDir)

	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("PUT", "/api/audio/aviso-t1",
		strings.NewReader(string(monoAsset(80000))))) // five seconds

	if rec.Code != 200 {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}

	var body struct{ DurationMs int64 }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.DurationMs != 5000 {
		t.Errorf("durationMs: want 5000, got %d", body.DurationMs)
	}

	if _, err := os.Stat(filepath.Join(audioDir, "aviso-t1.wav")); err != nil {
		t.Errorf("asset not on disk: %v", err)
	}

	rec = httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/audio/aviso-t1", nil))
	if rec.Code != 204 {
		t.Fatalf("delete: want 204, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAudioUploadRejectsBadFile(t *testing.T) {
	s := recordServer(t, "", t.TempDir())

	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("PUT", "/api/audio/aviso",
		strings.NewReader("this is an mp3, not a wav")))

	if rec.Code != 400 {
		t.Fatalf("want 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAudioUploadWithoutLibraryIs404(t *testing.T) {
	s := recordServer(t, "", "")

	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("PUT", "/api/audio/aviso",
		strings.NewReader(string(monoAsset(320)))))

	if rec.Code != 404 {
		t.Fatalf("want 404, got %d %s", rec.Code, rec.Body.String())
	}
}

// The call is checked before the body, matching the sibling call handlers: there
// is no point validating a request against a call that does not exist.
func TestPlayOnUnknownCallIs404(t *testing.T) {
	s := recordServer(t, "", t.TempDir())
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("POST",
		"/api/sessions/s1/calls/ghost/play", strings.NewReader(`{"asset":"aviso"}`)))
	if rec.Code != 404 {
		t.Fatalf("want 404, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestParsePlayRequest(t *testing.T) {
	cases := []struct {
		name, body, wantAsset string
		wantErr               bool
	}{
		{name: "asset simples", body: `{"asset":"aviso"}`, wantAsset: "aviso"},
		{name: "interruptible false", body: `{"asset":"aviso","interruptible":false}`, wantAsset: "aviso"},
		{name: "sem asset", body: `{}`, wantErr: true},
		{name: "asset vazio", body: `{"asset":""}`, wantErr: true},
		{name: "corpo inválido", body: `not json`, wantErr: true},
		{name: "corpo vazio", body: ``, wantErr: true},
		// Barge-in is not implemented, and a legal notice is the last thing that
		// should silently ignore what the caller asked for.
		{name: "interruptible true", body: `{"asset":"aviso","interruptible":true}`, wantErr: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			asset, err := parsePlayRequest(strings.NewReader(c.body))
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got asset %q", asset)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if asset != c.wantAsset {
				t.Errorf("asset: want %q, got %q", c.wantAsset, asset)
			}
		})
	}
}

func TestStopPlayUnknownCallIs404(t *testing.T) {
	s := recordServer(t, "", t.TempDir())
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("POST", "/api/sessions/s1/calls/ghost/stopplay", nil))
	if rec.Code != 404 {
		t.Fatalf("want 404, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecordEndpointsRequireAKnownSession(t *testing.T) {
	s := recordServer(t, t.TempDir(), t.TempDir())

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/sessions/nope/calls/c1/recording"},
		{"DELETE", "/api/sessions/nope/calls/c1/recording"},
		{"POST", "/api/sessions/nope/calls/c1/stopplay"},
	} {
		rec := httptest.NewRecorder()
		s.routes().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != 404 {
			t.Errorf("%s %s: want 404, got %d", tc.method, tc.path, rec.Code)
		}
	}
}
