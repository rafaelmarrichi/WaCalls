package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"wacalls/internal/app/player"
)

// Endpoints added by the fork: announcement playback, recording retrieval and
// asset upload. All of them sit behind the same API token as the rest of /api.

// safeSegment is what a path segment may contain before it is allowed anywhere
// near the filesystem. Call and session ids are hex from the engine, but they
// arrive here from the URL, so they are treated as untrusted input.
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}

	id := r.PathValue("id")
	if !sess.HasCall(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}

	asset, err := parsePlayRequest(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	durationMs, err := sess.PlayAnnouncement(id, asset)
	if err != nil {
		s.writeAssetError(w, asset, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"durationMs": durationMs})
}

// parsePlayRequest validates a playback request and returns the asset name.
func parsePlayRequest(body io.Reader) (string, error) {
	var req struct {
		Asset         string `json:"asset"`
		Interruptible *bool  `json:"interruptible"`
	}
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return "", errors.New("invalid JSON body")
	}
	if req.Asset == "" {
		return "", errors.New("asset required")
	}

	// Barge-in is not implemented. Rejecting the flag beats accepting it and
	// playing an uninterruptible notice anyway: the audio this endpoint exists
	// to play is legally required, so silently ignoring a caller's intent about
	// it is the wrong default.
	if req.Interruptible != nil && *req.Interruptible {
		return "", errors.New("interruptible playback is not supported")
	}

	return req.Asset, nil
}

func (s *Server) handleStopPlay(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}

	id := r.PathValue("id")
	if !sess.HasCall(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}

	if err := sess.StopAnnouncement(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// recordingPath resolves where a call's recording lives, rejecting anything that
// could point outside the recording directory.
func (s *Server) recordingPath(sessionID, callID string) (string, bool) {
	if s.audio.RecordDir == "" {
		return "", false
	}
	if !safeSegment.MatchString(sessionID) || !safeSegment.MatchString(callID) {
		return "", false
	}
	return filepath.Join(s.audio.RecordDir, sessionID, callID+".wav"), true
}

func (s *Server) handleRecordingGet(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}

	id := r.PathValue("id")

	// A live call has a file on disk whose header still reads zero length,
	// because the sizes are only known at the end. Handing that out would look
	// like a corrupt recording rather than an unfinished one.
	if sess.HasCall(id) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "the call is still in progress",
		})
		return
	}

	path, ok := s.recordingPath(sess.ID(), id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recording not available"})
		return
	}

	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no recording for this call"})
		return
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the recording"})
		return
	}

	w.Header().Set("Content-Type", "audio/wav")
	// ServeContent rather than a plain copy, so a consumer that lost its
	// connection halfway can resume with a range request instead of downloading
	// the whole file again.
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}

func (s *Server) handleRecordingDelete(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}

	path, ok := s.recordingPath(sess.ID(), r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recording not available"})
		return
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.log.Error("could not delete recording", "path", path, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete the recording"})
		return
	}

	// Missing counts as deleted: the consumer's goal is for the file to be gone,
	// and a retry after a successful delete must not fail.
	w.WriteHeader(http.StatusNoContent)
}

// handleAudioPut receives an announcement asset.
//
// The engine has no storage credentials of its own by design, so our API pushes
// each tenant's announcement here when they upload it. Keeping the asset on the
// engine's disk also keeps the network off the call setup path: a notice that has
// to be fetched when the contact answers is a notice that fails when object
// storage is slow, and a failed notice drops the call.
func (s *Server) handleAudioPut(w http.ResponseWriter, r *http.Request) {
	if !s.audio.Library.Enabled() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "announcements are not configured"})
		return
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the upload"})
		return
	}

	durationMs, err := s.audio.Library.Store(r.PathValue("name"), raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s.log.Info("announcement asset stored", "asset", r.PathValue("name"), "duration_ms", durationMs)
	writeJSON(w, http.StatusOK, map[string]any{"durationMs": durationMs})
}

func (s *Server) handleAudioDelete(w http.ResponseWriter, r *http.Request) {
	if !s.audio.Library.Enabled() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "announcements are not configured"})
		return
	}

	if err := s.audio.Library.Delete(r.PathValue("name")); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAssetError maps announcement failures to statuses the caller can act on.
// A missing asset is the one that matters: our API turns it into a dropped call
// rather than letting a recorded call proceed without its notice.
func (s *Server) writeAssetError(w http.ResponseWriter, asset string, err error) {
	switch {
	case errors.Is(err, player.ErrNoLibrary):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "announcements are not configured"})
	case errors.Is(err, os.ErrNotExist):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such asset: " + asset})
	default:
		s.log.Warn("announcement playback failed", "asset", asset, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
}
