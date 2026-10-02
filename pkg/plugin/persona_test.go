package plugin

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/agentaccess"
	"github.com/quantumwake/parley/pkg/enroll"
)

func stubPersona(t *testing.T, tmux string, fetch func(context.Context, Env, string) (agentaccess.Persona, error)) {
	t.Helper()
	prevTmux, prevFetch := tmuxSessionName, fetchPersona
	t.Cleanup(func() {
		tmuxSessionName = prevTmux
		fetchPersona = prevFetch
	})
	tmuxSessionName = func() string { return tmux }
	if fetch != nil {
		fetchPersona = fetch
	}
}

func TestSeatSessionPrefersTheTmuxName(t *testing.T) {
	stubPersona(t, "reviewer", nil)
	env := Env{Session: "s1", DataDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Dir(handleFile(env)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handleFile(env), []byte("closer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := seatSession(env); got != "reviewer" {
		t.Fatalf("seat %q", got)
	}

	tmuxSessionName = func() string { return "" }
	if got := seatSession(env); got != "closer" {
		t.Fatalf("handle %q", got)
	}

	tmuxSessionName = func() string { return "--help" }
	if got := seatSession(env); got != "closer" {
		t.Fatalf("bad tmux name should fall back, got %q", got)
	}
}

func TestReadTmuxSessionWithoutTmuxIsEmpty(t *testing.T) {
	t.Setenv("TMUX", "")
	if got := readTmuxSession(); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionStartInjectsTheSeatPersonaOnce(t *testing.T) {
	dir := enroll.NewFakeDirectory()
	defer dir.Close()
	tmp := t.TempDir()
	env := Env{
		EnrollURL:    dir.EnrollURL("laptop-agent"),
		IdentityPath: filepath.Join(tmp, "identity"),
		DataDir:      tmp,
		Directory:    dir.URL(),
	}
	var calls int
	stubPersona(t, "reviewer", func(_ context.Context, _ Env, session string) (agentaccess.Persona, error) {
		calls++
		if session != "reviewer" {
			t.Errorf("session %q", session)
		}
		return agentaccess.Persona{Name: "Reviewer", Instructions: "Read the diff."}, nil
	})

	// The first start enrolls. The persona is read on the next start, once
	// the identity exists.
	_ = run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s1", "cwd": "/repo"})
	env.EnrollURL = ""
	o := run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s1", "cwd": "/repo"})
	if !strings.Contains(o.AdditionalContext, `Persona "Reviewer":`) || !strings.Contains(o.AdditionalContext, "Read the diff.") {
		t.Fatalf("context: %s", o.AdditionalContext)
	}
	if calls != 1 {
		t.Fatalf("reads %d", calls)
	}

	o = run(t, env, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s1", "prompt": "hi"})
	if strings.Contains(o.AdditionalContext, "Read the diff.") {
		t.Fatalf("persona injected mid-session: %s", o.AdditionalContext)
	}
	if calls != 1 {
		t.Fatalf("reads after prompt %d", calls)
	}
}

func TestSessionStartWithNoPersonaOrADownAPIStillStarts(t *testing.T) {
	dir := enroll.NewFakeDirectory()
	defer dir.Close()
	tmp := t.TempDir()
	env := Env{
		EnrollURL:    dir.EnrollURL("laptop-agent"),
		IdentityPath: filepath.Join(tmp, "identity"),
		DataDir:      tmp,
		Directory:    dir.URL(),
	}
	stubPersona(t, "reviewer", func(context.Context, Env, string) (agentaccess.Persona, error) {
		return agentaccess.Persona{}, agentaccess.ErrNoPersona
	})
	_ = run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s1"})
	env.EnrollURL = ""
	o := run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s1"})
	if strings.Contains(o.AdditionalContext, "Persona") || !strings.Contains(o.AdditionalContext, "enrolled") {
		t.Fatalf("context: %s", o.AdditionalContext)
	}

	fetchPersona = func(context.Context, Env, string) (agentaccess.Persona, error) {
		return agentaccess.Persona{}, errors.New("statefs.ai is unavailable")
	}
	o = run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s2"})
	if strings.Contains(o.AdditionalContext, "Persona") || !strings.Contains(o.AdditionalContext, "enrolled") {
		t.Fatalf("down: %s", o.AdditionalContext)
	}
	log, _ := os.ReadFile(filepath.Join(tmp, "hooks.log"))
	if !strings.Contains(string(log), "persona") || !strings.Contains(string(log), "unavailable") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestSessionStartGivesUpOnAHungAPIWithinTheDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hang := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})}
	go hang.Serve(ln)
	t.Cleanup(func() { hang.Close() })
	dir := enroll.NewFakeDirectory()
	defer dir.Close()
	tmp := t.TempDir()
	env := Env{
		EnrollURL:    dir.EnrollURL("laptop-agent"),
		IdentityPath: filepath.Join(tmp, "identity"),
		DataDir:      tmp,
		Directory:    dir.URL(),
		StatefsAI:    "http://" + ln.Addr().String(),
	}
	stubPersona(t, "reviewer", nil)
	_ = run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s1"})
	env.EnrollURL = ""

	start := time.Now()
	o := run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s1"})
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("a hung API held session start for %s; the 2s deadline is the cap", elapsed)
	}
	if strings.Contains(o.AdditionalContext, "Persona") || !strings.Contains(o.AdditionalContext, "enrolled") {
		t.Fatalf("context: %s", o.AdditionalContext)
	}
}

func TestFormatPersonaSkipsEmptyInstructions(t *testing.T) {
	if formatPersona(agentaccess.Persona{Name: "Reviewer"}) != "" {
		t.Fatal("empty instructions were injected")
	}
	if got := formatPersona(agentaccess.Persona{Instructions: "  Read.  "}); got != "\nPersona:\nRead.\n" {
		t.Fatalf("%q", got)
	}
}
