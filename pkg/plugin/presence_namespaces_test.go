package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// The presence ping names conversations the way the API's grant check does:
// by the statefs namespace id, never by the display name. A ping of names is
// answered 200 with every namespace skipped, so the seat never appears.
func TestPresencePingNamesConversationsByNamespaceID(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	sub := Subscription{
		Name:     "statefs.ai website and portal",
		ID:       "68fe17c5-0232-4c27-b1be-e1de13a58792",
		Mode:     "full",
		JoinedMs: 1,
	}
	if err := saveSub(a, sub); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Namespaces []string `json:"namespaces"`
		State      string   `json:"state"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "t1", "expires_at": time.Now().Add(15 * time.Minute)})
		case "/api/v1/agent/presence":
			_ = json.NewDecoder(r.Body).Decode(&got)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "pinged": len(got.Namespaces)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a.StatefsAI = srv.URL

	if err := sendPresence(context.Background(), a, "listening"); err != nil {
		t.Fatal(err)
	}

	if want := []string{sub.ID}; !reflect.DeepEqual(got.Namespaces, want) {
		t.Fatalf("the ping names the namespace by id, not display name: got %v, want %v", got.Namespaces, want)
	}

	if got.State != "listening" {
		t.Fatalf("state = %q, want listening", got.State)
	}
}
