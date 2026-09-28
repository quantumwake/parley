package plugin

import (
	"bytes"
	"context"
	"errors"
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

	if !strings.Contains(got, "no Claude Code or Codex transcript for "+gone) {
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
	if n := strings.Count(got, "no Claude Code or Codex transcript for"); n != 1 {
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
	if n := strings.Count(got, "no Claude Code or Codex transcript for"); n != 2 {
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

func TestOwnershipCheckFailsClosedWithADirectory(t *testing.T) {
	known := Claims{Membership: "member-a"}

	if m, err := ownerToCheck(false, Claims{}, errors.New("no directory")); m != "" || err != nil {
		t.Fatalf("a file-backed store has no memberships, so every label match passes: %q, %v", m, err)
	}

	if m, err := ownerToCheck(true, known, nil); m != "member-a" || err != nil {
		t.Fatalf("with a directory and a known membership, ownership is checked: %q, %v", m, err)
	}

	if _, err := ownerToCheck(true, Claims{}, errors.New("token exchange failed")); err == nil || !strings.Contains(err.Error(), "token exchange failed") {
		t.Fatalf("claims that cannot be resolved must refuse, saying why, not fall back to the label: %v", err)
	}

	if _, err := ownerToCheck(true, Claims{Sub: "someone"}, nil); err == nil {
		t.Fatal("a signed-in identity with no membership cannot be checked and must refuse")
	}
}

// withCodexHome points the resume lookup at a Codex home of the test's own.
func withCodexHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	prev := codexDir
	codexDir = func() string { return home }
	t.Cleanup(func() { codexDir = prev })
	return home
}

// writeRollout writes a Codex rollout the way codex-cli 0.157 names and
// opens one: sessions/YYYY/MM/DD/rollout-<time>-<id>.jsonl, session_meta
// first (statefs-cloud-microvms @459).
func writeRollout(t *testing.T, home, day, stamp, id, metaID, cwd string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", filepath.FromSlash(day))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	meta := `{"timestamp":"2026-09-27T04:15:00Z","type":"session_meta","payload":{"id":"` + metaID + `","session_id":"` + metaID + `","cwd":"` + cwd + `"}}` + "\n"
	next := `{"type":"response_item","payload":{"type":"message","role":"user"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-"+stamp+"-"+id+".jsonl"), []byte(meta+next), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexSessionResumesFromItsRollout(t *testing.T) {
	home := withCodexHome(t)
	work := t.TempDir()
	id := "01a0e112-cad0-72e3-9d03-3dec90a15854"
	writeRollout(t, home, "2026/09/27", "2026-09-27T04-15-00", id, id, work)

	got := resumeLine(t.TempDir(), id)
	if got != "cd "+work+" && codex resume "+id {
		t.Fatalf("a Codex session resumes with codex, from its directory: %q", got)
	}
}

func TestClaudeTranscriptWinsOverACodexRollout(t *testing.T) {
	home := withCodexHome(t)
	id := "88888888-aaaa-bbbb-cccc-000000000008"
	writeRollout(t, home, "2026/09/27", "2026-09-27T04-15-00", id, id, t.TempDir())

	claude := t.TempDir()
	dir := filepath.Join(claude, "projects", "x")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(`{"cwd":"`+claude+`"}`+"\n"), 0o600)

	if got := resumeLine(claude, id); !strings.Contains(got, "claude --resume "+id) {
		t.Fatalf("a Claude Code transcript is the session's own: %q", got)
	}
}

func TestCodexResumeUsesTheLatestRollout(t *testing.T) {
	home := withCodexHome(t)
	id := "99999999-aaaa-bbbb-cccc-000000000009"
	old, last := t.TempDir(), t.TempDir()
	writeRollout(t, home, "2026/09/26", "2026-09-26T08-00-00", id, id, old)
	writeRollout(t, home, "2026/09/28", "2026-09-28T09-30-00", id, id, last)

	if got := resumeLine("", id); got != "cd "+last+" && codex resume "+id {
		t.Fatalf("the latest rollout names where it was last run: %q", got)
	}
}

func TestCodexRolloutThatIsNotTheSessionsGivesNoDirectory(t *testing.T) {
	home := withCodexHome(t)
	id := "aaaaaaaa-aaaa-bbbb-cccc-00000000000a"
	// The file name says id, its session_meta says otherwise: its cwd is not
	// this session's, so no cd is offered, only the resume.
	writeRollout(t, home, "2026/09/27", "2026-09-27T04-15-00", id, "someone-else", t.TempDir())

	if got := resumeLine("", id); got != "codex resume "+id {
		t.Fatalf("a mismatched session_meta must not lend its directory: %q", got)
	}
}

func TestCodexResumeSaysWhenTheDirectoryIsGone(t *testing.T) {
	home := withCodexHome(t)
	id := "bbbbbbbb-aaaa-bbbb-cccc-00000000000b"
	writeRollout(t, home, "2026/09/27", "2026-09-27T04-15-00", id, id, "/no/such/dir/anywhere")

	if got := resumeLine("", id); !strings.Contains(got, "no longer exists") || strings.Contains(got, "codex resume") {
		t.Fatalf("a resume into a missing directory must not be offered: %q", got)
	}
}

func TestCodexLookupRefusesIDsThatAreGlobs(t *testing.T) {
	home := withCodexHome(t)
	known := "cccccccc-aaaa-bbbb-cccc-00000000000c"
	writeRollout(t, home, "2026/09/27", "2026-09-27T04-15-00", known, known, home)

	for _, id := range []string{"*", "cccccccc-*", "../sessions/2026/09/27/x", "a/b", ""} {
		if got := resumeLine("", id); strings.Contains(got, "codex resume") {
			t.Fatalf("id %q must not produce a resume line: %q", id, got)
		}
	}
}

func TestNoCodexHomeDoesNotGlobTheWorkingDirectory(t *testing.T) {
	prev := codexDir
	codexDir = func() string { return "" }
	t.Cleanup(func() { codexDir = prev })

	work := t.TempDir()
	id := "dddddddd-aaaa-bbbb-cccc-00000000000d"
	writeRollout(t, work, "2026/09/27", "2026-09-27T04-15-00", id, id, work)

	old, _ := os.Getwd()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}

	defer os.Chdir(old)

	if got := resumeLine("", id); strings.Contains(got, "codex resume") {
		t.Fatalf("an unknown Codex home must not resolve against the working directory: %q", got)
	}
}
