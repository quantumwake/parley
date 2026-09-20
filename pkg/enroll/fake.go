package enroll

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/quantumwake/statefs/pkg/assertion"
)

// FakeDirectory is the test double for the three identity endpoints the
// plugin touches: mint an enrollment token (admin), enroll a public key
// (single use), exchange a signed assertion for an acting token. It
// verifies signatures for real so a wrong key or a replay fails here too.
type FakeDirectory struct {
	Server *httptest.Server

	mu        sync.Mutex
	tokens    map[string]fakeToken           // raw token -> binding
	keys      map[string][]ed25519.PublicKey // username -> registered keys
	Minted    int
	Enrolled  int
	Exchanges int
}

type fakeToken struct {
	username string
	used     bool
	expires  time.Time
}

// NewFakeDirectory starts the fake; call Close when done.
func NewFakeDirectory() *FakeDirectory {
	f := &FakeDirectory{tokens: map[string]fakeToken{}, keys: map[string][]ed25519.PublicKey{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenant/enroll-tokens", f.mint)
	mux.HandleFunc("POST /auth/enroll", f.enroll)
	mux.HandleFunc("POST /auth/token", f.token)
	f.Server = httptest.NewServer(mux)
	return f
}

// Close stops the server.
func (f *FakeDirectory) Close() { f.Server.Close() }

// URL is the directory base URL.
func (f *FakeDirectory) URL() string { return f.Server.URL }

// MintToken is the admin action, callable directly from tests.
func (f *FakeDirectory) MintToken(username string, ttl time.Duration) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Minted++
	raw := "en_" + base64.RawURLEncoding.EncodeToString([]byte(username+"/"+time.Now().Format(time.RFC3339Nano)+"/"+itoa(f.Minted)))
	f.tokens[raw] = fakeToken{username: username, expires: time.Now().Add(ttl)}
	return raw
}

// EnrollURL renders the URL an admin would hand to the plugin.
func (f *FakeDirectory) EnrollURL(username string) string {
	return Request{Directory: f.URL(), Token: f.MintToken(username, 10*time.Minute)}.String()
}

func (f *FakeDirectory) mint(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		http.Error(w, `{"error":"admin credential required"}`, http.StatusUnauthorized)
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Username == "" {
		http.Error(w, `{"error":"body must be {username}"}`, http.StatusBadRequest)
		return
	}

	raw := f.MintToken(req.Username, 10*time.Minute)
	_ = json.NewEncoder(w).Encode(map[string]any{"token": raw, "username": req.Username, "expires_at": time.Now().Add(10 * time.Minute).Unix()})
}

func (f *FakeDirectory) enroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token     string   `json:"token"`
		PublicKey string   `json:"public_key"`
		Alg       string   `json:"alg"`
		Label     string   `json:"label"`
		Caps      []string `json:"caps"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Token == "" {
		http.Error(w, `{"error":"body must be {token, public_key, alg?, label, caps?}"}`, http.StatusBadRequest)
		return
	}

	pub, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize || (req.Alg != "" && req.Alg != "ed25519") {
		http.Error(w, `{"error":"public_key must be base64 raw key material of a supported alg (ed25519)"}`, http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[req.Token]
	if !ok || t.used || time.Now().After(t.expires) {
		http.Error(w, `{"error":"invalid, expired, or used enrollment token"}`, http.StatusUnauthorized)
		return
	}

	t.used = true
	f.tokens[req.Token] = t
	f.keys[t.username] = append(f.keys[t.username], ed25519.PublicKey(pub))
	f.Enrolled++
	_ = json.NewEncoder(w).Encode(map[string]any{"authenticator": map[string]any{
		"username": t.username, "kind": "public_key", "alg": "ed25519", "label": req.Label, "caps": req.Caps,
	}})
}

func (f *FakeDirectory) token(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Assertion string `json:"assertion"`
		APIKey    string `json:"api_key"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Assertion == "" {
		http.Error(w, `{"error":"body must be {assertion}"}`, http.StatusBadRequest)
		return
	}

	a, body, sig, err := assertion.Parse(req.Assertion)
	if err != nil || a.Aud != assertion.Audience {
		http.Error(w, `{"error":"malformed assertion"}`, http.StatusUnauthorized)
		return
	}

	if d := time.Since(time.Unix(a.IAT, 0)); d > 30*time.Second || d < -30*time.Second {
		http.Error(w, `{"error":"assertion outside the clock window"}`, http.StatusUnauthorized)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys[a.Sub] {
		if ed25519.Verify(k, body, sig) {
			f.Exchanges++
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "act_" + a.Sub + "_" + itoa(f.Exchanges), "expires_in": 900})
			return
		}
	}

	http.Error(w, `{"error":"no registered key verifies this assertion"}`, http.StatusUnauthorized)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}

	return string(b)
}
