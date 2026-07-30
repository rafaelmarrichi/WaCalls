package events

import "time"

// The call.recording webhook and the announcement SSE event, both added by the
// fork. Kept in their own file so reapplying the patch after an upstream
// snapshot touches webhook.go in one place only.

// RecordingRecord describes a finished recording.
//
// The consumer fetches the file over the recording endpoint, addressed by
// session and call id. The absolute path on the engine's disk is deliberately
// not in the payload: it is of no use to the consumer and would end up copied
// into its logs and its database, describing this machine's filesystem layout.
type RecordingRecord struct {
	CallID     string `json:"callId"`
	SessionID  string `json:"sessionId"`
	Path       string `json:"-"`
	DurationMs int64  `json:"durationMs"`
	SizeBytes  int64  `json:"sizeBytes"`
	// SHA256 of the WAV as written here, so whoever downloads it can tell a
	// truncated transfer from a complete one.
	SHA256 string `json:"sha256"`
	// FramesDropped counts 20 ms frames of audio lost to buffer overflow. Any
	// non-zero value means the recording is missing audio the call delivered.
	FramesDropped int64 `json:"framesDropped"`
	// Truncated is set when the file hit its size ceiling and recording stopped
	// before the call did.
	Truncated bool `json:"truncated"`
	// Failed is set when closing the file errored. The recording is announced
	// anyway: it holds real conversation up to the point of failure, and an
	// unannounced file sits on the engine's disk invisible to the consumer,
	// which is the only party that applies the retention rules. Better to hand
	// it over marked as suspect than to leave audio nobody will ever delete.
	Failed bool `json:"failed"`
}

// EmitRecording announces a finished recording over the webhook and the event
// stream.
//
// `snapshot` is the call as it was when teardown began, and passing it is not
// optional in practice. Closing a recording runs off the hot path now, so by the
// time this fires the call is usually already out of the registry: looking it up
// here would produce a payload with an empty status, peer and direction, which
// any consumer validating its input rejects. And it does reject, silently, three
// retries later, with the audio sitting on disk and the panel showing "sem
// gravação".
//
// Pass nil only when there is genuinely no call record to describe.
func (b *Broker) EmitRecording(snapshot *CallRecord, r RecordingRecord) {
	rec := CallRecord{SessionID: r.SessionID, CallID: r.CallID}

	switch {
	case snapshot != nil:
		rec = *snapshot
	default:
		// Último recurso, para quem chamar sem retrato.
		if live, ok := b.GetCall(r.CallID); ok {
			rec = *live
		}
	}

	b.webhooks.enqueueRecording(rec, r)
	b.broadcast(map[string]any{
		"type": "call-recording", "sessionId": r.SessionID, "id": r.CallID,
		"durationMs": r.DurationMs, "sizeBytes": r.SizeBytes,
		"framesDropped": r.FramesDropped, "truncated": r.Truncated,
	})
}

// EmitAnnounceDone reports that the announcement finished playing, which is the
// signal that the line is free for the agent. Completed is false when playback
// was cut short, and a caller that requires the announcement (a recorded call
// always does) has to treat that as a failure.
func (b *Broker) EmitAnnounceDone(sessionID, callID, asset string, completed bool, playedMs int64) {
	b.broadcast(map[string]any{
		"type": "call-announce-done", "sessionId": sessionID, "id": callID,
		"asset": asset, "completed": completed, "playedMs": playedMs,
	})
}

func (d *webhookDispatcher) enqueueRecording(rec CallRecord, r RecordingRecord) {
	if d == nil {
		return
	}
	ev := webhookEvent{
		ID:        newDeliveryID(),
		Event:     "call.recording",
		SentAt:    time.Now().UnixMilli(),
		Call:      rec,
		Recording: &r,
	}
	select {
	case d.queue <- ev:
	default:
		d.log.Warn("webhook queue full, dropping event", "event", ev.Event, "call_id", r.CallID)
	}
}
