package app

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"wacalls/internal/app/events"
	"wacalls/internal/app/session"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

func callServerWithEmptySession(id string) *Server {
	b := events.NewBroker(nil, slog.Default())
	mgr := session.NewManager(session.Deps{Broker: b, Log: slog.Default()})
	mgr.NewSession(id, "", &whatsmeow.Client{Store: &store.Device{}})
	return &Server{authorize: bearerAuthorizer(""), broker: b, sessions: mgr}
}

func TestAcceptUnknownCallIs404(t *testing.T) {
	s := callServerWithEmptySession("s1")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("POST", "/api/sessions/s1/calls/ghost/accept", nil))
	if rec.Code != 404 {
		t.Fatalf("accept unknown call: want 404, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestWebRTCUnknownCallIs404(t *testing.T) {
	s := callServerWithEmptySession("s1")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("POST", "/api/sessions/s1/calls/ghost/webrtc",
		strings.NewReader(`{"sdp_offer":"x"}`)))
	if rec.Code != 404 {
		t.Fatalf("webrtc unknown call: want 404, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestEndUnknownCallIs404(t *testing.T) {
	s := callServerWithEmptySession("s1")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/sessions/s1/calls/ghost", nil))
	if rec.Code != 404 {
		t.Fatalf("end unknown call: want 404, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRejectUnknownCallIs404(t *testing.T) {
	s := callServerWithEmptySession("s1")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("POST", "/api/sessions/s1/calls/ghost/reject", nil))
	if rec.Code != 404 {
		t.Fatalf("reject unknown call: want 404, got %d %s", rec.Code, rec.Body.String())
	}
}

// A call belonging to another session must survive an end or reject aimed at it
// through a session that does not host it.
//
// This is the case the 404 tests above cannot show on their own: the registry is
// process-wide and keyed by call ID alone, so before the guard the request took
// effect on the registry even though the session never had the call. The record
// was marked ended, call.ended fired and the entry was dropped, while the real
// call carried on with audio and recording running and nothing left able to end
// it.
func TestEndDoesNotReachAnotherSessionsCall(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"end", "DELETE", "/api/sessions/s1/calls/c2"},
		{"reject", "POST", "/api/sessions/s1/calls/c2/reject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := callServerWithEmptySession("s1")
			s.sessions.NewSession("s2", "", &whatsmeow.Client{Store: &store.Device{}})
			s.broker.UpsertCall(events.CallRecord{
				SessionID: "s2",
				CallID:    "c2",
				Direction: "outbound",
				Peer:      "5514981120008",
				Status:    events.StatusRinging,
			})

			rec := httptest.NewRecorder()
			s.routes().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))

			if rec.Code != 404 {
				t.Fatalf("want 404, got %d %s", rec.Code, rec.Body.String())
			}

			got, ok := s.broker.GetCall("c2")
			if !ok {
				t.Fatal("the other session's call was dropped from the registry")
			}
			if got.Status == events.StatusEnded {
				t.Fatalf("the other session's call was marked %q", got.Status)
			}
		})
	}
}
