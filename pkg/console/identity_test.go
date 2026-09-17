package console

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quantumwake/statefs/pkg/identityfile"

	"github.com/quantumwake/parley/pkg/plugin"
	"github.com/quantumwake/parley/pkg/store"
)

// tokenDirectory answers every token exchange with an unsigned token for
// the username in the assertion's key file: enough for the console to
// read who it now is.
func tokenDirectory(t *testing.T, username string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/token" {
			http.NotFound(w, r)
			return
		}

		payload, _ := json.Marshal(map[string]any{"sub": username, "tenant": "acme", "caps": []string{"read", "write"}})
		tok := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
		_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_in": 900})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func consoleWithIdentities(t *testing.T) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STATEFS_AI_CONFIG", filepath.Join(home, "config.json"))
	t.Setenv("STATEFS_AI_STORE", "")
	dir := tokenDirectory(t, "bot-1")
	for path, user := range map[string]string{
		filepath.Join(home, ".statefs", "identity"):                      "alice",
		filepath.Join(home, ".statefs", "identities", "bot", "identity"): "bot-1",
	} {
		f, _ := identityfile.Generate(user)
		f.Directory = dir.URL
		if err := identityfile.Write(path, f); err != nil {
			t.Fatal(err)
		}
	}

	env := plugin.Env{DataDir: t.TempDir(), Directory: dir.URL, IdentityPath: filepath.Join(home, ".statefs", "identity")}
	return &Server{env: env, st: store.NewFake(), claims: plugin.Claims{Sub: "alice"}}, home
}

func send(s *Server, method, path, host, origin, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestConsoleListsAndSwitchesIdentity(t *testing.T) {
	s, _ := consoleWithIdentities(t)
	const host = "127.0.0.1:4711"

	rec := send(s, "GET", "/v1/identities", host, "", "", "")
	var list struct {
		Identities []plugin.LocalIdentity `json:"identities"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Identities) != 2 || !list.Identities[0].Current || list.Identities[1].Name != "bot" {
		t.Fatalf("identities: %d %s", rec.Code, rec.Body.String())
	}

	if rec := send(s, "POST", "/v1/identity", host, "http://"+host, "application/json", `{"name":"nope"}`); rec.Code != 404 {
		t.Fatalf("an unknown identity: %d %s", rec.Code, rec.Body.String())
	}

	if rec := send(s, "POST", "/v1/identity", host, "http://"+host, "application/json", `{"name":"bot"}`); rec.Code != 200 {
		t.Fatalf("switch: %d %s", rec.Code, rec.Body.String())
	}

	rec = send(s, "GET", "/v1/me", host, "", "", "")
	if !strings.Contains(rec.Body.String(), `"username":"bot-1"`) {
		t.Fatalf("me after the switch: %s", rec.Body.String())
	}

	if c := plugin.LoadConfig(); c.Identity != "" {
		t.Fatalf("the console switch must not change the machine's default: %+v", c)
	}
}

// Only the console's own page may switch: another site in the browser, a
// form post, or a name rebound to this port are refused.
func TestConsoleIdentitySwitchIsLocalOnly(t *testing.T) {
	s, _ := consoleWithIdentities(t)
	const host = "127.0.0.1:4711"
	body := `{"name":"bot"}`
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"another origin": send(s, "POST", "/v1/identity", host, "https://evil.example", "application/json", body),
		"a form post":    send(s, "POST", "/v1/identity", host, "http://"+host, "text/plain", body),
		"a rebound name": send(s, "POST", "/v1/identity", "evil.example:4711", "http://evil.example:4711", "application/json", body),
		"list, rebound":  send(s, "GET", "/v1/identities", "evil.example:4711", "", "", ""),
	} {
		if rec.Code != 403 {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	if rec := send(s, "GET", "/v1/me", host, "", "", ""); !strings.Contains(rec.Body.String(), `"username":"alice"`) {
		t.Fatalf("refused switches leave the identity alone: %s", rec.Body.String())
	}

	if rec := send(s, "GET", "/v1/identities", "localhost:4711", "", "", ""); rec.Code != 200 {
		t.Fatalf("localhost is the console's own host: %d", rec.Code)
	}
}
