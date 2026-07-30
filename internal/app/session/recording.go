package session

import (
	"fmt"
	"sync"
	"sync/atomic"

	"wacalls/internal/app/events"
	"wacalls/internal/app/player"
	"wacalls/internal/app/record"
	"wacalls/internal/voip/call"
)

// Recording and announcement playback for a session's live calls.
//
// The single most important thing in this file is feedOutbound. Everything that
// goes to the contact passes through it: the agent's microphone and the recorded
// announcement. Two reasons it has to be one funnel rather than two paths.
//
// The announcement must end up in the recording. It is the proof that the contact
// was told the call was being recorded, and a recording that omits it is useless
// as evidence. Recording only the microphone, which is the obvious wiring, would
// leave the announcement out.
//
// And while the announcement plays, the microphone must be dropped rather than
// mixed. An agent who is already attached and talking would otherwise be heard
// over the legal notice.

// AudioConfig turns recording and announcements on. Both are off when empty, so
// an unconfigured build behaves like upstream.
type AudioConfig struct {
	RecordDir      string
	RecordMaxBytes int64
	Library        *player.Library
}

func (c AudioConfig) recordingEnabled() bool { return c.RecordDir != "" }

// callAudio holds the recorder and the announcement player of each live call in
// one session.
type callAudio struct {
	mu        sync.Mutex
	recorders map[string]*record.Recorder
	players   map[string]*player.Player
	// Frame counters per call, reported once at teardown.
	//
	// They exist because "the contact could not hear the agent" is otherwise
	// almost undiagnosable from the outside: the recording proves audio reached
	// this layer, but not that it reached the encoder. These separate the two.
	contadores map[string]*contadorDeAudio
}

type contadorDeAudio struct {
	doNavegador    int64
	descartados    int64
	paraOContato   int64
	semCallManager int64
}

func newCallAudio() *callAudio {
	return &callAudio{
		recorders:  map[string]*record.Recorder{},
		players:    map[string]*player.Player{},
		contadores: map[string]*contadorDeAudio{},
	}
}

func (s *Session) contador(callID string) *contadorDeAudio {
	s.audio.mu.Lock()
	defer s.audio.mu.Unlock()

	c, ok := s.audio.contadores[callID]
	if !ok {
		c = &contadorDeAudio{}
		s.audio.contadores[callID] = c
	}
	return c
}

// startRecording opens a recording for a call that just connected. Idempotent,
// because the connected state is reported again after a media reconnect and must
// not start a second file.
func (s *Session) startRecording(callID string, cm *call.CallManager) {
	if !s.mgr.audioCfg.recordingEnabled() {
		return
	}

	s.audio.mu.Lock()
	_, exists := s.audio.recorders[callID]
	s.audio.mu.Unlock()
	if exists {
		return
	}

	rec, err := record.New(record.Options{
		Dir:       s.mgr.audioCfg.RecordDir,
		SessionID: s.id,
		CallID:    callID,
		Log:       s.log,
		Observer:  cm.Observer(),
		MaxBytes:  s.mgr.audioCfg.RecordMaxBytes,
	})
	if err != nil {
		// A call that cannot be recorded still has to work. Our API decides
		// whether a call without a recording is acceptable, and it knows the
		// tenant's rules; the engine does not.
		s.log.Error("could not start recording for this call", "call_id", callID, "err", err)
		return
	}

	s.audio.mu.Lock()
	if _, raced := s.audio.recorders[callID]; raced {
		s.audio.mu.Unlock()
		_, _ = rec.Close()
		return
	}
	s.audio.recorders[callID] = rec
	s.audio.mu.Unlock()
}

func (s *Session) recorderFor(callID string) *record.Recorder {
	s.audio.mu.Lock()
	defer s.audio.mu.Unlock()
	return s.audio.recorders[callID]
}

// finishRecording closes the file and announces it. Idempotent: teardown reaches
// this from the ended state change, from the ended callback and from session
// shutdown, and all three run for a normal hang-up.
func (s *Session) finishRecording(callID string) {
	s.audio.mu.Lock()
	rec := s.audio.recorders[callID]
	delete(s.audio.recorders, callID)
	s.audio.mu.Unlock()

	if rec == nil {
		return
	}

	res, err := rec.Close()
	if err != nil {
		s.log.Error("closing the recording failed", "call_id", callID, "err", err)
		return
	}

	s.mgr.broker.EmitRecording(events.RecordingRecord{
		CallID:        callID,
		SessionID:     s.id,
		Path:          res.Path,
		DurationMs:    res.DurationMs,
		SizeBytes:     res.SizeBytes,
		SHA256:        res.SHA256,
		FramesDropped: res.FramesDropped,
		Truncated:     res.Truncated,
	})
}

// stopAnnouncement ends any announcement on a call and forgets it.
func (s *Session) stopAnnouncement(callID string) {
	s.audio.mu.Lock()
	p := s.audio.players[callID]
	delete(s.audio.players, callID)
	s.audio.mu.Unlock()

	p.Stop()
}

// announcing reports whether an announcement currently owns the line.
func (s *Session) announcing(callID string) bool {
	s.audio.mu.Lock()
	p := s.audio.players[callID]
	s.audio.mu.Unlock()
	return p.Playing()
}

// teardownCallAudio releases both the recording and the announcement of a call.
func (s *Session) teardownCallAudio(callID string) {
	s.stopAnnouncement(callID)
	s.finishRecording(callID)
	s.reportarContadores(callID)
}

// reportarContadores logs how much audio moved in each direction and forgets it.
//
// One line per call, and it answers the question that is otherwise a guess: if
// paraOContato is high the audio reached the encoder and the fault is further
// down; if it is zero while doNavegador is high, it never left this layer.
func (s *Session) reportarContadores(callID string) {
	s.audio.mu.Lock()
	c := s.audio.contadores[callID]
	delete(s.audio.contadores, callID)
	s.audio.mu.Unlock()

	if c == nil {
		return
	}

	s.log.Info("call audio frames",
		"call_id", callID,
		"from_browser", atomic.LoadInt64(&c.doNavegador),
		"dropped_during_announcement", atomic.LoadInt64(&c.descartados),
		"forwarded_to_contact", atomic.LoadInt64(&c.paraOContato),
		"no_call_manager", atomic.LoadInt64(&c.semCallManager),
	)
}

// teardownAllCallAudio releases everything, for session shutdown.
func (s *Session) teardownAllCallAudio() {
	s.audio.mu.Lock()
	ids := make([]string, 0, len(s.audio.recorders)+len(s.audio.players))
	for id := range s.audio.recorders {
		ids = append(ids, id)
	}
	for id := range s.audio.players {
		if _, dup := s.audio.recorders[id]; !dup {
			ids = append(ids, id)
		}
	}
	for id := range s.audio.contadores {
		if _, dup := s.audio.recorders[id]; !dup {
			if _, dup2 := s.audio.players[id]; !dup2 {
				ids = append(ids, id)
			}
		}
	}
	s.audio.mu.Unlock()

	for _, id := range ids {
		s.teardownCallAudio(id)
	}
}

// feedOutbound is the only path for audio going to the contact.
//
// fromBrowser distinguishes the agent's microphone from audio we are injecting,
// because the microphone is dropped while an announcement is playing and the
// announcement itself obviously is not.
func (s *Session) feedOutbound(callID string, pcm []float32, fromBrowser bool) {
	contador := s.contador(callID)

	if fromBrowser {
		atomic.AddInt64(&contador.doNavegador, 1)

		if s.announcing(callID) {
			atomic.AddInt64(&contador.descartados, 1)
			return
		}
	}

	s.recorderFor(callID).WriteOutbound(pcm)

	// Injected audio also goes to the browser leg, so an agent who is already
	// attached hears the announcement along with the contact.
	//
	// That is what lets the agent join during the announcement instead of after
	// it: they hear it end and know exactly when they may speak, without
	// watching a timer. Their microphone still cannot reach the contact until
	// the announcement is over, which the check above guarantees.
	//
	// Only for injected audio. Echoing the microphone back would put the agent's
	// own voice in their headset.
	if !fromBrowser {
		if b := s.getBridge(callID); b != nil {
			_ = b.WritePCM(pcm)
		}
	}

	if cm, ok := s.calls.Get(callID); ok {
		cm.FeedCapturedPCM(pcm)
		atomic.AddInt64(&contador.paraOContato, 1)
	} else {
		atomic.AddInt64(&contador.semCallManager, 1)
	}
}

// feedInbound records what the contact sent. The audio itself continues to the
// browser through the bridge, untouched by recording.
func (s *Session) feedInbound(callID string, pcm []float32) {
	s.recorderFor(callID).WriteInbound(pcm)
}

// PlayAnnouncement plays an asset into a live call and reports how long it runs,
// so the caller knows when the line is free for the agent.
//
// Only one announcement at a time per call: a second request replaces the first
// rather than letting two overlap into noise.
func (s *Session) PlayAnnouncement(callID, asset string) (int64, error) {
	cm, ok := s.calls.Get(callID)
	if !ok {
		return 0, fmt.Errorf("no such call %s", callID)
	}

	lib := s.mgr.audioCfg.Library
	if !lib.Enabled() {
		return 0, player.ErrNoLibrary
	}

	pcm, err := lib.Get(asset)
	if err != nil {
		return 0, err
	}

	s.stopAnnouncement(callID)

	// Registered under the lock so a very short asset cannot finish, and run its
	// OnDone, before this player is even recorded. OnDone takes the same lock, so
	// it waits the few microseconds until the registration is done.
	s.audio.mu.Lock()
	defer s.audio.mu.Unlock()

	// Declared before Start so OnDone can compare against this exact player. The
	// mutex is what makes reading it safe: OnDone only reads p while holding the
	// lock, and the lock is not released until p has been assigned and stored.
	var p *player.Player

	p = player.Start(pcm, player.Options{
		CallID:   callID,
		Log:      s.log,
		Observer: cm.Observer(),
		Sink:     func(frame []float32) { s.feedOutbound(callID, frame, false) },
		OnDone: func(completed bool, playedMs int64) {
			s.audio.mu.Lock()
			// A replacement announcement may already own the slot.
			if s.audio.players[callID] == p {
				delete(s.audio.players, callID)
			}
			s.audio.mu.Unlock()
			s.mgr.broker.EmitAnnounceDone(s.id, callID, asset, completed, playedMs)
		},
	})
	s.audio.players[callID] = p

	return p.DurationMs(), nil
}

// StopAnnouncement cuts an announcement short.
func (s *Session) StopAnnouncement(callID string) error {
	if _, ok := s.calls.Get(callID); !ok {
		return fmt.Errorf("no such call %s", callID)
	}
	s.stopAnnouncement(callID)
	return nil
}

// Announcing reports whether an announcement owns the line of a call.
func (s *Session) Announcing(callID string) bool { return s.announcing(callID) }
