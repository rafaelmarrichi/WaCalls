package call

import "wacalls/internal/voip/core"

// Observer exposes the per-call observer to app-level components added by the
// fork, so their goroutines are registered with the same accounting the media
// stack uses and the upstream leak checks keep meaning something.
//
// Safe to call from the onCall hook: the observer is assigned in createCall
// before the hook runs.
func (m *CallManager) Observer() core.CallObserver {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.observer == nil {
		return core.NopObserver{}
	}
	return m.observer
}
