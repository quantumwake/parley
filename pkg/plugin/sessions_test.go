package plugin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/naming"
	"github.com/quantumwake/parley/pkg/store"
)

func TestSessionsOfferResumeOnlyWhereTheTranscriptIs(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111-2222-3333-444444444444")
	env := s["aaaaaaaa-1111-2222-3333-444444444444"]
	author := authorOf(env)
	st, err := StoreFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}

	here, gone, other := "11111111-aaaa-bbbb-cccc-000000000001", "22222222-aaaa-bbbb-cccc-000000000002", "33333333-aaaa-bbbb-cccc-000000000003"
	base := time.Date(2026, 9, 19, 9, 0, 0, 0, time.Local)
	for i, id := range []string{here, gone} {
		c := naming.Conversation{Started: base.Add(time.Duration(i) * time.Hour), Title: "work " + id[:8], Session: id, Agent: author}
		if _, err := st.Open(ctx, naming.AgentLogName(author, "repo", id, c.Started), c.Scope()); err != nil {
			t.Fatal(err)
		}
	}

	// A session another identity recorded must not be listed.
	c := naming.Conversation{Started: base, Title: "theirs", Session: other, Agent: "someone-else"}
	if _, err := st.Open(ctx, naming.AgentLogName("someone-else", "repo", other, c.Started), c.Scope()); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	claude := t.TempDir()
	dir := filepath.Join(claude, "projects", "-encoded-cwd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	row := `{"type":"user","cwd":"` + work + `","sessionId":"` + here + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, here+".jsonl"), []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Sessions(ctx, env, claude, 20, &out); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if !strings.Contains(got, "cd "+work+" && claude --resume "+here) {
		t.Fatalf("a session with a local transcript must print a resume line: %q", got)
	}

	if strings.Contains(got, "claude --resume "+gone) {
		t.Fatalf("no transcript, so no resume line: %q", got)
	}

	if !strings.Contains(got, "no Claude Code transcript for "+gone) {
		t.Fatalf("a session without a transcript must say why: %q", got)
	}

	if strings.Contains(got, other) || strings.Contains(got, "theirs") {
		t.Fatalf("another identity's session leaked into the list: %q", got)
	}

	if strings.Index(got, "work "+gone[:8]) > strings.Index(got, "work "+here[:8]) {
		t.Fatalf("newest first: %q", got)
	}
}

func TestSessionsSaysWhenTheDirectoryIsGone(t *testing.T) {
	claude := t.TempDir()
	dir := filepath.Join(claude, "projects", "x")
	_ = os.MkdirAll(dir, 0o755)
	id := "44444444-aaaa-bbbb-cccc-000000000004"
	row := `{"cwd":"/no/such/dir/anywhere"}` + "\n"
	_ = os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(row), 0o600)

	if got := resumeLine(claude, id); !strings.Contains(got, "no longer exists") || strings.Contains(got, "claude --resume") {
		t.Fatalf("a resume into a missing directory must not be offered: %q", got)
	}
}

func TestResumedSessionIsOneEntry(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111-2222-3333-444444444444")
	env := s["aaaaaaaa-1111-2222-3333-444444444444"]
	author := authorOf(env)
	st, err := StoreFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}

	id := "55555555-aaaa-bbbb-cccc-000000000005"
	first := time.Date(2026, 9, 19, 9, 0, 0, 0, time.Local)
	for i, title := range []string{"first run", ""} {
		c := naming.Conversation{Started: first.Add(time.Duration(i) * time.Hour), Title: title, Session: id, Agent: author}
		if _, err := st.Open(ctx, naming.AgentLogName(author, "repo", id, c.Started), c.Scope()); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	if err := Sessions(ctx, env, t.TempDir(), 20, &out); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if n := strings.Count(got, "no Claude Code transcript for"); n != 1 {
		t.Fatalf("a resumed session must list once, got %d: %q", n, got)
	}

	if !strings.Contains(got, "(2 recordings)") || !strings.Contains(got, "first run") {
		t.Fatalf("recordings are counted and the earlier title survives an untitled resume: %q", got)
	}

	if !strings.Contains(got, first.Add(time.Hour).Format("2006-01-02 15:04")) {
		t.Fatalf("time is the latest recording's: %q", got)
	}
}

func TestSessionIDLabelIsNotTrusted(t *testing.T) {
	claude := t.TempDir()
	dir := filepath.Join(claude, "projects", "x")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "real-session.jsonl"), []byte(`{"cwd":"`+claude+`"}`+"\n"), 0o600)

	for _, id := range []string{"*", "../projects/x/real-session", "real-session; rm -rf ~", "real-session\x1b[2J", "a/b", ""} {
		got := resumeLine(claude, id)
		if strings.Contains(got, "claude --resume") || strings.Contains(got, "\x1b") {
			t.Fatalf("id %q must not produce a resume line or echo control bytes: %q", id, got)
		}
	}

	if got := resumeLine(claude, "real-session"); !strings.Contains(got, "claude --resume real-session") {
		t.Fatalf("a well-formed id with a transcript still resumes: %q", got)
	}
}

func TestLimitCountsSessionsAndKeepsTheNewest(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111-2222-3333-444444444444")
	env := s["aaaaaaaa-1111-2222-3333-444444444444"]
	author := authorOf(env)
	st, err := StoreFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.Local)
	// session -> start hours after base; "old" is recorded twice (a resume)
	rec := []struct {
		id    string
		title string
		hour  int
	}{
		{"66666666-aaaa-bbbb-cccc-000000000001", "oldest", 1},
		{"66666666-aaaa-bbbb-cccc-000000000001", "oldest", 2},
		{"66666666-aaaa-bbbb-cccc-000000000002", "middle", 30},
		{"66666666-aaaa-bbbb-cccc-000000000003", "newest", 60},
	}

	for _, r := range rec {
		c := naming.Conversation{Started: base.Add(time.Duration(r.hour) * time.Hour), Title: r.title, Session: r.id, Agent: author}
		if _, err := st.Open(ctx, naming.AgentLogName(author, "repo", r.id, c.Started), c.Scope()); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	if err := Sessions(ctx, env, t.TempDir(), 2, &out); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if n := strings.Count(got, "no Claude Code transcript for"); n != 2 {
		t.Fatalf("--limit 2 must show 2 sessions, got %d: %q", n, got)
	}

	if !strings.Contains(got, "newest") || !strings.Contains(got, "middle") || strings.Contains(got, "oldest") {
		t.Fatalf("the two newest sessions are shown, not the first two recordings: %q", got)
	}
}

func TestNoClaudeDirDoesNotGlobTheWorkingDirectory(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, "projects", "x")
	_ = os.MkdirAll(dir, 0o755)
	id := "77777777-aaaa-bbbb-cccc-000000000007"
	_ = os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(`{"cwd":"`+work+`"}`+"\n"), 0o600)

	old, _ := os.Getwd()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}

	defer os.Chdir(old)

	if got := resumeLine("", id); strings.Contains(got, "claude --resume") {
		t.Fatalf("an unknown claude dir must not resolve against the working directory: %q", got)
	}
}

func TestOnlyConversationsThisMemberOwnsAreListed(t *testing.T) {
	mine := store.Namespace{ID: "1", DisplayName: "mine", Owner: "member-a"}
	theirs := store.Namespace{ID: "2", DisplayName: "labelled with my name by someone else", Owner: "member-b"}
	tenantWide := store.Namespace{ID: "3", DisplayName: "no owner", Owner: ""}
	all := []store.Namespace{mine, theirs, tenantWide}

	got := keepOwned(all, "member-a")
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("a label another member set must not make a session mine: %+v", got)
	}

	if got := keepOwned(all, ""); len(got) != 3 {
		t.Fatalf("with no known membership there is nothing to check, so every label match stays: %+v", got)
	}
}
