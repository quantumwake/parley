package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A ping is its own process: the hook spawns `parley presence` and exits.
// Before the token cache that cost two calls every few seconds — in
// production 666 sign-ins against 665 pings — and the sign-in sat in front
// of everything else an agent's commands did.
func TestPingsAcrossProcessesSignInOnce(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	if err := saveSub(a, Subscription{Name: "c", ID: "68fe17c5-0232-4c27-b1be-e1de13a58792", Mode: "full", JoinedMs: 1}); err != nil {
		t.Fatal(err)
	}

	var signIns, pings atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/token":
			signIns.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "t1", "expires_at": time.Now().Add(15 * time.Minute)})
		case "/api/v1/agent/presence":
			if r.Header.Get("Authorization") != "Bearer t1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			pings.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a.StatefsAI = srv.URL

	// Each call is a fresh client, as a fresh process is; they share only
	// the data directory.
	for range 5 {
		if err := sendPresence(context.Background(), a, "listening"); err != nil {
			t.Fatal(err)
		}
	}

	if n, p := signIns.Load(), pings.Load(); n != 1 || p != 5 {
		t.Fatalf("%d sign-ins for %d pings, want 1 for 5", n, p)
	}
}
