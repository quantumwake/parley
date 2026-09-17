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

const (
	testToken = "launch-token"
	testHost  = "127.0.0.1:4711"
	testPage  = "http://" + testHost
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
	return &Server{env: env, st: store.NewFake(), claims: plugin.Claims{Sub: "alice"}, token: testToken, port: "4711"}, home
}

// request is one call to the console API; the zero value of each field is
// what the console's own page sends.
type request struct {
	method, path, body  string
	host, origin, ctype string
	token               string
	noToken, noOrigin   bool
}

func (s *Server) do(q request) *httptest.ResponseRecorder {
	if q.method == "" {
		q.method = "GET"
	}

	req := httptest.NewRequest(q.method, q.path, strings.NewReader(q.body))
	req.Host = testHost
	if q.host != "" {
		req.Host = q.host
	}

	if !q.noToken {
		tok := testToken
		if q.token != "" {
			tok = q.token
		}

		req.Header.Set("Authorization", "Bearer "+tok)
	}

	if q.method != "GET" {
		ctype, origin := "application/json", "http://"+req.Host
		if q.ctype != "" {
			ctype = q.ctype
		}

		if q.origin != "" {
			origin = q.origin
		}

		req.Header.Set("Content-Type", ctype)
		if !q.noOrigin {
			req.Header.Set("Origin", origin)
		}
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestConsoleListsAndSwitchesIdentity(t *testing.T) {
	s, _ := consoleWithIdentities(t)

	rec := s.do(request{path: "/v1/identities"})
	var list struct {
		Identities []plugin.LocalIdentity `json:"identities"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Identities) != 2 || !list.Identities[0].Current || list.Identities[1].Name != "bot" {
		t.Fatalf("identities: %d %s", rec.Code, rec.Body.String())
	}

	if rec := s.do(request{method: "POST", path: "/v1/identity", body: `{"name":"nope"}`}); rec.Code != 404 {
		t.Fatalf("an unknown identity: %d %s", rec.Code, rec.Body.String())
	}

	if rec := s.do(request{method: "POST", path: "/v1/identity", body: `{"name":"bot"}`}); rec.Code != 200 {
		t.Fatalf("switch: %d %s", rec.Code, rec.Body.String())
	}

	if rec := s.do(request{path: "/v1/me"}); !strings.Contains(rec.Body.String(), `"username":"bot-1"`) {
		t.Fatalf("me after the switch: %s", rec.Body.String())
	}

	if c := plugin.LoadConfig(); c.Identity != "" {
		t.Fatalf("the console switch must not change the machine's default: %+v", c)
	}
}

// SP-9: only the page this launch opened reaches the API. Every route is
// behind the same guard, reads included, so another local page or a
// rebound DNS name can neither read conversations nor post as this
// identity.
func TestConsoleGuardRefusesEverythingButItsOwnPage(t *testing.T) {
	s, _ := consoleWithIdentities(t)
	post := `{"kind":"comment","text":"hi"}`
	routes := []request{
		{path: "/v1/me"},
		{path: "/v1/identities"},
		{path: "/v1/conversations"},
		{path: "/v1/conversations/x/events"},
		{path: "/v1/conversations/x/head"},
		{path: "/v1/subscriptions"},
		{method: "POST", path: "/v1/identity", body: `{"name":"bot"}`},
		{method: "POST", path: "/v1/conversations/x/posts", body: post},
		{method: "POST", path: "/v1/subscriptions", body: `{"name":"x"}`},
		{method: "DELETE", path: "/v1/subscriptions/x"},
	}

	for _, q := range routes {
		name := q.method + " " + q.path
		cases := map[string]struct {
			q    request
			want int
		}{
			"no token":          {q, 401},
			"a wrong token":     {q, 401},
			"a rebound name":    {q, 403},
			"another port":      {q, 403},
			"a non-loopback IP": {q, 403},
		}
		c := cases["no token"]
		c.q.noToken = true
		cases["no token"] = c
		c = cases["a wrong token"]
		c.q.token = "guess"
		cases["a wrong token"] = c
		c = cases["a rebound name"]
		c.q.host = "evil.example:4711"
		cases["a rebound name"] = c
		c = cases["another port"]
		c.q.host = "127.0.0.1:9999"
		cases["another port"] = c
		c = cases["a non-loopback IP"]
		c.q.host = "192.168.1.5:4711"
		cases["a non-loopback IP"] = c
		if q.method != "" {
			for label, mut := range map[string]func(*request){
				"a foreign origin": func(r *request) { r.origin = "https://evil.example" },
				"no origin":        func(r *request) { r.noOrigin = true },
				"a form post":      func(r *request) { r.ctype = "application/x-www-form-urlencoded" },
				"text/plain":       func(r *request) { r.ctype = "text/plain" },
			} {
				w := q
				mut(&w)
				cases[label] = struct {
					q    request
					want int
				}{w, 403}
			}
		}

		for label, c := range cases {
			if rec := s.do(c.q); rec.Code != c.want {
				t.Errorf("%s with %s: %d want %d (%s)", name, label, rec.Code, c.want, rec.Body.String())
			}
		}
	}

	if rec := s.do(request{path: "/v1/me"}); !strings.Contains(rec.Body.String(), `"username":"alice"`) {
		t.Fatalf("refused requests leave the identity alone: %s", rec.Body.String())
	}
}

// The console's own page passes, on 127.0.0.1 or localhost, and the page
// itself loads without a token (that is where the token is picked up).
func TestConsoleGuardAdmitsItsOwnPage(t *testing.T) {
	s, _ := consoleWithIdentities(t)
	for _, q := range []request{
		{path: "/v1/me"},
		{path: "/v1/me", host: "localhost:4711"},
		{path: "/v1/subscriptions"},
		{method: "POST", path: "/v1/subscriptions", body: `{}`},        // reaches the handler: 400 for a missing name, not 401/403
		{method: "DELETE", path: "/v1/subscriptions/nothing-followed"}, // reaches the handler
	} {
		if rec := s.do(q); rec.Code == 401 || rec.Code == 403 {
			t.Errorf("%s %s on %s refused: %d %s", q.method, q.path, q.host, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = testHost
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code == 401 || rec.Code == 403 {
		t.Fatalf("the page loads without a token: %d", rec.Code)
	}
}
