package call

import "wacalls/internal/voip/core"

// Observer exposes the per-call observer to app-level components added by the
// fork, so their goroutines are registered with the same accounting the media
// stack uses and the upstream leak checks keep meaning something.
//
// **This must never take m.mu, and nothing reachable from a state callback may
// take it either.** emitState invokes OnStateChange from six call sites that
// already hold that mutex, so grabbing it here deadlocks the goroutine handling
// the call. The symptom is brutal and does not look like a deadlock: the contact
// answers, the engine logs "remote accepted call", and then nothing. The state
// update never reaches the broker, so no call.active webhook is ever sent and
// the caller sits in a ringing call that eventually times out as unanswered.
//
// Reading without the lock is safe: observer is assigned in createCall, on the
// same goroutine, before the CallManager is published to the registry or handed
// to the onCall hook, and nothing ever reassigns it.
func (m *CallManager) Observer() core.CallObserver {
	if m.observer == nil {
		return core.NopObserver{}
	}
	return m.observer
}
