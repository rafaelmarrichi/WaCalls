package call

import (
	"log/slog"
	"testing"
	"time"
)

// Observer() must not take m.mu.
//
// This exists because it did, once, and the result was a deadlock in the media
// path that looked nothing like one: the contact answered, the engine logged
// "remote accepted call", and then the call went silent until it timed out as
// unanswered. emitState calls OnStateChange while holding m.mu, the fork's
// recorder starts from that callback, and starting it asked for the observer.
func TestObserverDoesNotTakeTheCallLock(t *testing.T) {
	cm := NewCallManager(fakeSock{}, slog.Default())

	// Exactly the situation OnStateChange runs in.
	cm.mu.Lock()
	defer cm.mu.Unlock()

	pronto := make(chan struct{})
	go func() {
		_ = cm.Observer()
		close(pronto)
	}()

	select {
	case <-pronto:
	case <-time.After(2 * time.Second):
		t.Fatal("Observer() bloqueou com m.mu tomado; OnStateChange roda sob essa trava")
	}
}

func TestObserverNuncaDevolveNil(t *testing.T) {
	cm := NewCallManager(fakeSock{}, slog.Default())
	cm.observer = nil

	if cm.Observer() == nil {
		t.Fatal("Observer() devolveu nil, e quem chama usa sem checar")
	}
}
