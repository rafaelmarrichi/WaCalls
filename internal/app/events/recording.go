package events

import "time"

// The call.recording webhook and the announcement SSE event, both added by the
// fork. Kept in their own file so reapplying the patch after an upstream
// snapshot touches webhook.go in one place only.

// RecordingRecord describes a finished recording. Path is local to the engine:
// the consumer fetches the file over the recording endpoint and deletes it once
// it has been stored elsewhere.
type RecordingRecord struct {
	CallID     string `json:"callId"`
	SessionID  string `json:"sessionId"`
	Path       string `json:"path"`
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
}

// EmitRecording announces a finished recording over the webhook and the event
// stream.
//
// This fires while the call is being torn down, before call.ended, because the
// recorder is closed as part of teardown and the file has to be announced while
// the call record still exists. Consumers must not assume the call is already
// marked ended when this arrives.
func (b *Broker) EmitRecording(r RecordingRecord) {
	rec := CallRecord{SessionID: r.SessionID, CallID: r.CallID}
	if live, ok := b.GetCall(r.CallID); ok {
		rec = *live
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
