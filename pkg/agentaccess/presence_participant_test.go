package agentaccess

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The chip should name the seat, not the machine identity every seat on a
// laptop shares: the ping carries the handle this session speaks under, and
// omits it when the session declared none (statefs.ai @936, parley @302).
func TestPresenceCarriesTheSeatHandle(t *testing.T) {
	for _, c := range []struct {
		name        string
		participant string
		want        any
	}{
		{"a seat with a handle", "reviewer", "reviewer"},
		{"a session with none", "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			pub, priv, _ := ed25519.GenerateKey(nil)
			now := time.Unix(1_790_000_000, 0)
			f := &fake{pub: pub, now: now, tokenTTL: 15 * time.Minute}
			var got map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/agent/presence" {
					f.ServeHTTP(w, r)
					return
				}
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &got)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			cl := &Client{Base: srv.URL, Username: "ana-agent", Key: priv, Now: func() time.Time { return now }}
			if err := cl.Presence(context.Background(), "s1", "working", c.participant, []string{"statefs"}); err != nil {
				t.Fatal(err)
			}
			if got["participant"] != c.want {
				t.Fatalf("participant = %v, want %v (body %v)", got["participant"], c.want, got)
			}
			if got["state"] != "working" || got["session"] != "s1" {
				t.Fatalf("the rest of the ping is unchanged: %v", got)
			}
		})
	}
}
