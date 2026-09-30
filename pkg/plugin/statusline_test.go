package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The status line says who this session is: its handle, then its identity.
func TestStatusLineNamesTheHandleAndTheIdentity(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]

	if _, err := SetParticipant(a, "parley-delivery"); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := StatusLineWho(a, &out); err != nil {
		t.Fatal(err)
	}

	got, who := out.String(), authorOf(a)
	if who == "" {
		t.Fatal("the fixture has no identity")
	}

	if !strings.HasPrefix(got, "parley-delivery · ") || !strings.Contains(got, who) {
		t.Fatalf("want the handle then the identity %q, got %q", who, got)
	}

	if strings.Contains(got, "no handle") || strings.Contains(got, "no session") {
		t.Fatalf("a session with a handle is not told it has none: %q", got)
	}
}

// A session that has not chosen a handle is told so: that is the gap that
// leaves every seat on a machine speaking as one name.
func TestStatusLineSaysWhenThereIsNoHandle(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]

	var out strings.Builder
	if err := StatusLineWho(a, &out); err != nil {
		t.Fatal(err)
	}

	if got := out.String(); !strings.Contains(got, "no handle") || !strings.Contains(got, authorOf(a)) {
		t.Fatalf("no handle chosen: %q", got)
	}
}

// A handle written before handles were checked can be a flag taken for a
// name. It reads as no handle here too, so the line does not show "--help".
func TestStatusLineDoesNotShowAFlagAsAHandle(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]

	if err := os.MkdirAll(filepath.Dir(handleFile(a)), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(handleFile(a), []byte("--help\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := StatusLineWho(a, &out); err != nil {
		t.Fatal(err)
	}

	if got := out.String(); strings.Contains(got, "--help") || !strings.Contains(got, "no handle") {
		t.Fatalf("a flag is not a handle: %q", got)
	}
}

// With no session the line says so; with no identity it says that. Neither
// is an error: a status line must always draw.
func TestStatusLineNamesAMissingSessionAndAMissingIdentity(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]

	noSession := a
	noSession.Session = ""

	var out strings.Builder
	if err := StatusLineWho(noSession, &out); err != nil {
		t.Fatal(err)
	}

	if got := out.String(); !strings.Contains(got, "no session") || !strings.Contains(got, authorOf(a)) {
		t.Fatalf("no session: %q", got)
	}

	noIdentity := a
	noIdentity.IdentityPath = filepath.Join(t.TempDir(), "absent")

	out.Reset()
	if err := StatusLineWho(noIdentity, &out); err != nil {
		t.Fatal(err)
	}

	if got := out.String(); !strings.Contains(got, "not enrolled") {
		t.Fatalf("no identity: %q", got)
	}
}

// Claude Code names the session on stdin. Anything that is not a plain id is
// refused, because the id becomes a directory name.
func TestSessionFromStatusInput(t *testing.T) {
	const wait = 500 * time.Millisecond

	for name, c := range map[string]struct{ in, want string }{
		"a session id":             {`{"session_id":"87dba06a-49fa-4505-87dc-aa01cded45ec"}`, "87dba06a-49fa-4505-87dc-aa01cded45ec"},
		"grok sessionId":           {`{"sessionId":"grok-1"}`, "grok-1"},
		"antigravity alias":        {`{"conversation_id":"agy-1"}`, "agy-1"},
		"session_id wins":          {`{"session_id":"claude-1","sessionId":"grok-1"}`, "claude-1"},
		"other fields around it":   {`{"model":{"id":"x"},"session_id":"abc123","cwd":"/tmp"}`, "abc123"},
		"surrounding spaces":       {`{"session_id":"  abc123  "}`, "abc123"},
		"no session id":            {`{"cwd":"/tmp"}`, ""},
		"malformed":                {`{"session_id":`, ""},
		"empty":                    {``, ""},
		"not an object":            {`"abc123"`, ""},
		"a path to leave":          {`{"session_id":"../../etc"}`, ""},
		"a slash":                  {`{"session_id":"a/b"}`, ""},
		"a leading dot":            {`{"session_id":".hidden"}`, ""},
		"too long":                 {`{"session_id":"` + strings.Repeat("a", 129) + `"}`, ""},
		"sessionId path":           {`{"sessionId":"../../etc"}`, ""},
		"sessionId slash":          {`{"sessionId":"a/b"}`, ""},
		"sessionId leading dot":    {`{"sessionId":".hidden"}`, ""},
		"sessionId too long":       {`{"sessionId":"` + strings.Repeat("b", 129) + `"}`, ""},
		"conversation_id path":     {`{"conversation_id":"../../etc"}`, ""},
		"conversation_id slash":    {`{"conversation_id":"a/b"}`, ""},
		"conversation_id dot":      {`{"conversation_id":".hidden"}`, ""},
		"conversation_id too long": {`{"conversation_id":"` + strings.Repeat("c", 129) + `"}`, ""},
	} {
		if got := SessionFromStatusInput(strings.NewReader(c.in), wait); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// A stdin that never delivers cannot stall a redraw.
func TestSessionFromStatusInputGivesUpOnAStdinThatNeverAnswers(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	start := time.Now()
	if got := SessionFromStatusInput(r, 150*time.Millisecond); got != "" {
		t.Fatalf("nothing was written, got %q", got)
	}

	if took := time.Since(start); took > time.Second {
		t.Fatalf("it waited %v for a stdin that never answered", took)
	}
}
