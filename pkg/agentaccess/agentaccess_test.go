package agentaccess

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fake is statefs.ai's agent surface: it verifies the assertion against the
// identity's public key (as statefs would), then serves people to a bearer.
type fake struct {
	pub            ed25519.PublicKey
	now            time.Time
	signIns        atomic.Int32
	peopleCode     int // answer this instead of people when set
	revoke         atomic.Bool
	tokenTTL       time.Duration
	lastQuery      string
	agent          string
	personaCode    int
	personaBody    string
	personaSession string
	personaDeny    int // answer this many 401s before the body, even with a good token
	workflowPath   string
	workflowBody   string
	workflowDeny   int
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.agent = r.Header.Get("User-Agent")
	switch r.URL.Path {
	case "/api/v1/agent/token":
		var in struct{ Assertion string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		body, sig, ok := strings.Cut(in.Assertion, ".")
		b, _ := base64.RawURLEncoding.DecodeString(body)
		s, _ := base64.RawURLEncoding.DecodeString(sig)
		var a struct {
			Sub, Aud, Nonce string
			IAT             int64
		}
		_ = json.Unmarshal(b, &a)
		if !ok || !ed25519.Verify(f.pub, b, s) || a.Aud != Audience || a.Sub != "ana-agent" || a.Nonce == "" || a.IAT != f.now.Unix() {
			w.WriteHeader(401)
			return
		}
		n := f.signIns.Add(1)
		f.revoke.Store(false)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "t" + string(rune('0'+n)), "expires_at": f.now.Add(f.tokenTTL)})
	case "/api/v1/agent/people":
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer t") || f.revoke.Load() {
			w.WriteHeader(401)
			return
		}
		if f.peopleCode != 0 {
			w.WriteHeader(f.peopleCode)
			return
		}
		f.lastQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"people":[{"name":"Bob Smith","agents":[{"label":"laptop","identity":"bob-agent-1"}]}]}`))
	case "/api/v1/agent/persona":
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer t") || f.revoke.Load() {
			w.WriteHeader(401)
			return
		}
		f.personaSession = r.URL.Query().Get("session")
		if f.personaDeny > 0 {
			f.personaDeny--
			w.WriteHeader(401)
			return
		}
		if f.personaCode != 0 {
			w.WriteHeader(f.personaCode)
			return
		}
		if f.personaBody == "" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(f.personaBody))
	default:
		if strings.HasPrefix(r.URL.Path, "/api/v1/agent/project/") {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer t") {
				w.WriteHeader(401)
				return
			}
			f.workflowPath = r.URL.EscapedPath()
			if f.workflowDeny > 0 {
				f.workflowDeny--
				w.WriteHeader(401)
				return
			}
			if f.workflowBody == "" {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write([]byte(f.workflowBody))
			return
		}
		w.WriteHeader(404)
	}
}

func setup(t *testing.T) (*fake, *Client) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_790_000_000, 0)
	f := &fake{pub: pub, now: now, tokenTTL: 15 * time.Minute}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	return f, &Client{Base: srv.URL, Username: "ana-agent", Key: priv, Now: func() time.Time { return f.now }, UserAgent: "parley/9.9.9"}
}

func TestPeopleSignsInOnceAndReusesTheToken(t *testing.T) {
	f, c := setup(t)
	for range 2 {
		p, err := c.People(context.Background(), "bo", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 1 || p[0].Agents[0].Identity != "bob-agent-1" {
			t.Fatalf("%+v", p)
		}
	}
	if f.signIns.Load() != 1 {
		t.Fatalf("signed in %d times", f.signIns.Load())
	}
	if f.agent != "parley/9.9.9" {
		t.Fatalf("User-Agent %q", f.agent)
	}
	if f.lastQuery != "q=bo&limit=10" {
		t.Fatalf("query %q", f.lastQuery)
	}
}

func TestPeopleSignsInAgainNearExpiry(t *testing.T) {
	f, c := setup(t)
	if _, err := c.People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(14*time.Minute + 30*time.Second)
	if _, err := c.People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}
	if f.signIns.Load() != 2 {
		t.Fatalf("signed in %d times", f.signIns.Load())
	}
}

func TestPeopleSignsInOnceMoreWhenTheTokenIsRefused(t *testing.T) {
	f, c := setup(t)
	if _, err := c.People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}
	f.revoke.Store(true)
	if _, err := c.People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}
	if f.signIns.Load() != 2 {
		t.Fatalf("signed in %d times", f.signIns.Load())
	}
}

func TestRefusalsMapToTheirErrors(t *testing.T) {
	_, c := setup(t)
	_, other, _ := ed25519.GenerateKey(nil)
	c.Key = other
	if _, err := c.People(context.Background(), "bo", 10); !errors.Is(err, ErrSignIn) {
		t.Fatalf("wrong key: %v", err)
	}

	for code, want := range map[int]error{403: ErrLookupOff, 503: ErrUnavailable} {
		f, c := setup(t)
		f.peopleCode = code
		if _, err := c.People(context.Background(), "bo", 10); !errors.Is(err, want) {
			t.Fatalf("%d: %v", code, err)
		}
	}
}

func TestUnreachableIsUnavailable(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	c := &Client{Base: "http://127.0.0.1:1", Username: "ana-agent", Key: priv}
	if _, err := c.People(context.Background(), "bo", 10); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestPersonaReadsTheSeatAndTreats404AsNone(t *testing.T) {
	f, c := setup(t)
	f.personaBody = `{"persona_id":"builtin:reviewer","name":"Reviewer","instructions":"Read the diff.","version":3}`
	p, err := c.Persona(context.Background(), "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if f.personaSession != "reviewer" || p.ID != "builtin:reviewer" || p.Name != "Reviewer" || p.Instructions != "Read the diff." || p.Version != 3 {
		t.Fatalf("session %q persona %+v", f.personaSession, p)
	}
	if f.signIns.Load() != 1 {
		t.Fatalf("sign-ins %d", f.signIns.Load())
	}

	f.personaBody = ""
	if _, err := c.Persona(context.Background(), "reviewer"); !errors.Is(err, ErrNoPersona) {
		t.Fatalf("404: %v", err)
	}
	if _, err := c.Persona(context.Background(), "  "); !errors.Is(err, ErrNoPersona) {
		t.Fatalf("blank session: %v", err)
	}
}

func TestPersonaRetriesOnceAfterA401(t *testing.T) {
	f, c := setup(t)
	f.personaDeny = 1
	f.personaBody = `{"persona_id":"builtin:reviewer","name":"Reviewer","instructions":"Read.","version":1}`
	p, err := c.Persona(context.Background(), "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if p.Instructions != "Read." || f.signIns.Load() != 2 {
		t.Fatalf("persona %+v sign-ins %d", p, f.signIns.Load())
	}
}

func TestPersonaEscapesTheSessionQuery(t *testing.T) {
	f, c := setup(t)
	f.personaBody = `{"instructions":"Read."}`
	if _, err := c.Persona(context.Background(), "a&b"); err != nil {
		t.Fatal(err)
	}
	if f.personaSession != "a&b" {
		t.Fatalf("session %q", f.personaSession)
	}
}

func TestPersonaDownOrRefusedDoesNotSucceed(t *testing.T) {
	f, c := setup(t)
	f.personaCode = 401
	if _, err := c.Persona(context.Background(), "reviewer"); !errors.Is(err, ErrSignIn) {
		t.Fatalf("401: %v", err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	down := &Client{Base: "http://127.0.0.1:1", Username: "ana-agent", Key: priv}
	if _, err := down.Persona(context.Background(), "reviewer"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestWorkflowReadsTheProjectsAgentRoute(t *testing.T) {
	f, c := setup(t)
	f.workflowDeny = 1
	f.workflowBody = `{"milestones":[{"key":"ready","name":"Ready"}],"stages":[{"key":"review","name":"Review","milestone":"ready","agents":["reviewer"]}],"approvers":["kasra@example.com"]}`
	wf, err := c.Workflow(context.Background(), "proj/1")
	if err != nil {
		t.Fatal(err)
	}
	if f.workflowPath != "/api/v1/agent/project/proj%2F1/workflow" {
		t.Fatalf("the project id is one escaped path segment: %q", f.workflowPath)
	}
	if len(wf.Stages) != 1 || wf.Stages[0].Milestone != "ready" || wf.Stages[0].Agents[0] != "reviewer" || wf.Approvers[0] != "kasra@example.com" || wf.Milestones[0].Key != "ready" {
		t.Fatalf("workflow %+v", wf)
	}
	if f.signIns.Load() != 2 {
		t.Fatalf("a 401 signs in again once: %d", f.signIns.Load())
	}

	f.workflowBody = ""
	if _, err := c.Workflow(context.Background(), "proj"); !errors.Is(err, ErrNoWorkflow) {
		t.Fatalf("404: %v", err)
	}
	if _, err := c.Workflow(context.Background(), " "); !errors.Is(err, ErrNoWorkflow) {
		t.Fatalf("no project: %v", err)
	}
}
