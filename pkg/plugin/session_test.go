package plugin

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quantumwake/statefs/pkg/identityfile"
)

// sessions sets up one machine (one identity, one data dir) and returns an
// Env per Claude Code session on it, plus a second machine's Env.
func sessions(t *testing.T, ids ...string) (map[string]Env, Env) {
	t.Helper()
	t.Setenv("STATEFS_AI_STORE", "file:"+t.TempDir())
	t.Setenv("STATEFS_AI_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	mk := func(user string) Env {
		dir := t.TempDir()
		f, _ := identityfile.Generate(user)
		_ = identityfile.Write(filepath.Join(dir, "identity"), f)
		return Env{DataDir: dir, IdentityPath: filepath.Join(dir, "identity")}
	}

	machine := mk("agent")
	out := map[string]Env{}
	for _, id := range ids {
		e := machine
		e.Session = id
		out[id] = e
	}

	return out, mk("other")
}

// Two sessions under one identity on one machine: each sees the other's
// posts and not its own, each keeps its own cursor, and a handle declared
// in one does not rename the other. This is the retro's B, C and E.
func TestSessionsSharingAnIdentitySeeEachOther(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer

	if err := CreateShared(ctx, a, "issues", "", nil, &out); err != nil {
		t.Fatal(err)
	}

	if err := Join(ctx, a, "issues", "full", "all", "reviewer", &out); err != nil {
		t.Fatal(err)
	}

	if err := Join(ctx, b, "issues", "full", "all", "author", &out); err != nil {
		t.Fatal(err)
	}

	if err := Post(ctx, a, "issues", "question", "does the cursor race?", "*", "", nil, &out); err != nil {
		t.Fatal(err)
	}

	got := Inject(ctx, b)
	if !strings.Contains(got, "does the cursor race?") {
		t.Fatalf("B must see A's post although they share an identity: %q", got)
	}

	if !strings.Contains(got, "reviewer (agent#aaaaaaaa)") {
		t.Fatalf("A's post must carry A's handle and session: %q", got)
	}

	if mine := Inject(ctx, a); mine != "" {
		t.Fatalf("A must not be shown its own post: %q", mine)
	}

	// B's read advances the machine cursor. A third session that has not
	// joined gets nothing, but after joining preserves the machine cursor.
	if err := Post(ctx, b, "issues", "comment", "yes, per machine", "reviewer", "", nil, &out); err != nil {
		t.Fatal(err)
	}

	c := a
	c.Session = "cccccccc-3333"
	if got := Inject(ctx, c); got != "" {
		t.Fatalf("c has not joined, so should get nothing: %q", got)
	}

	// c joins and sees new posts, but not the earlier ones (cursor is at machine head)
	if err := Join(ctx, c, "issues", "full", "all", "", &out); err != nil {
		t.Fatal(err)
	}

	if err := Post(ctx, b, "issues", "comment", "after c joined", "reviewer", "", nil, &out); err != nil {
		t.Fatal(err)
	}

	if got := Inject(ctx, c); !strings.Contains(got, "after c joined") || strings.Contains(got, "does the cursor race?") {
		t.Fatalf("c sees posts after joining: %q", got)
	}

	if got := Inject(ctx, a); !strings.Contains(got, "post.comment author (agent#bbbbbbbb) to:reviewer") {
		t.Fatalf("A must get B's answer, addressed by handle: %q", got)
	}

	if participantOf(a, mustID(t, a, "issues")) != "reviewer" || participantOf(b, mustID(t, b, "issues")) != "author" {
		t.Fatal("handles are per session")
	}
}

func mustID(t *testing.T, env Env, name string) string {
	t.Helper()
	for _, s := range Subscriptions(env) {
		if s.Name == name {
			return s.ID
		}
	}

	t.Fatalf("not following %s", name)
	return ""
}

func TestWaitReturnsOthersPostsNotMine(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)

	// A's own post does not end A's wait.
	_ = Post(ctx, a, "issues", "comment", "mine", "*", "", nil, &out)
	out.Reset()
	if err := Wait(ctx, a, nil, 3*WaitPoll, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "no new posts") {
		t.Fatalf("own post must not wake the wait: %q", out.String())
	}

	// B's post, landing mid-wait, does.
	go func() {
		time.Sleep(WaitPoll / 2)
		_ = Post(ctx, b, "issues", "comment", "theirs", "*", "", nil, &bytes.Buffer{})
	}()

	out.Reset()
	if err := Wait(ctx, a, []string{"issues"}, time.Minute, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "theirs") || strings.Contains(out.String(), "mine") || !strings.Contains(out.String(), "1 new posts") {
		t.Fatalf("wait must print the other session's post only: %q", out.String())
	}

	if err := Wait(ctx, a, []string{"elsewhere"}, time.Second, &out); err == nil {
		t.Fatal("waiting on a conversation not followed must fail")
	}
}

func TestReadWaitIgnoresMyOwnPost(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Post(ctx, a, "issues", "comment", "mine", "*", "", nil, &out)

	start := time.Now()
	out.Reset()
	if err := Read(ctx, a, "issues", -1, false, 3*time.Second, &out); err != nil {
		t.Fatal(err)
	}

	if time.Since(start) < 3*time.Second {
		t.Fatalf("read --wait woke on its own post after %s", time.Since(start))
	}

	if !strings.Contains(out.String(), "next position 1") {
		t.Fatalf("read must say where to continue: %q", out.String())
	}
}

func TestStopHookDeliversPostsOnce(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)
	_ = Post(ctx, b, "issues", "question", "still there?", "*", "", nil, &out)

	hookEnv := a
	hookEnv.Session = "" // hooks learn the session from their input
	if o := run(t, hookEnv, map[string]any{"hook_event_name": "Stop", "session_id": a.Session, "stop_hook_active": true}); o.Decision != "" {
		t.Fatalf("a stop that follows a block must not block again: %+v", o)
	}

	o := run(t, hookEnv, map[string]any{"hook_event_name": "Stop", "session_id": a.Session})
	if o.Decision != "block" || !strings.Contains(o.Reason, "still there?") {
		t.Fatalf("stop must hand the agent the new post: %+v", o)
	}

	if o := run(t, hookEnv, map[string]any{"hook_event_name": "Stop", "session_id": a.Session}); o.Decision != "" {
		t.Fatalf("a delivered post must not block again: %+v", o)
	}
}

// A post from an older client carries no session id. A session-aware
// reader shows it rather than hiding it from every session on the machine.
func TestPostWithoutSessionIsShownToSessions(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	old := a
	old.Session = ""
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Post(ctx, old, "issues", "comment", "from an old client", "*", "", nil, &out)

	if got := Inject(ctx, a); !strings.Contains(got, "from an old client") {
		t.Fatalf("a post without a session id must be shown: %q", got)
	}
}

// A session that starts, then stays quiet while others post and read,
// still gets those posts: its record is taken at SessionStart, not on
// first use from a machine cursor others have moved.
// A session that did not join a channel gets nothing; after joining, it
// preserves any existing cursor so it doesn't miss anything.
func TestSessionMustJoinToSeeChannel(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222", "cccccccc-3333")
	a, b, c := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"], s["cccccccc-3333"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)

	_ = Post(ctx, a, "issues", "comment", "landed before c joined", "*", "", nil, &out)
	if got := Inject(ctx, b); !strings.Contains(got, "landed before c joined") {
		t.Fatalf("b read it and moves the machine cursor: %q", got)
	}

	// c has not joined, so it gets nothing even though the machine record exists
	if got := Inject(ctx, c); got != "" {
		t.Fatalf("c did not join, so should get nothing: %q", got)
	}

	// c joins and preserves the machine cursor (at head when c joins)
	_ = Join(ctx, c, "issues", "full", "all", "", &out)

	// Now c sees posts that land after joining
	_ = Post(ctx, a, "issues", "comment", "landed after c joined", "*", "", nil, &out)
	if got := Inject(ctx, c); !strings.Contains(got, "landed after c joined") {
		t.Fatalf("c joined and should see new posts: %q", got)
	}

	// c's cursor is now at head, not earlier posts; the earlier post was never delivered
	if got := Inject(ctx, c); got != "" {
		t.Fatalf("cursor moved past earlier post, so c gets nothing: %q", got)
	}
}

// A background wait, the Stop hook and prompt injection may deliver for
// one session at the same moment; each row still arrives once.
func TestConcurrentDeliveriesShowEachPostOnce(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)
	for i := 0; i < 5; i++ {
		_ = Post(ctx, b, "issues", "comment", "row", "*", "", nil, &out)
	}

	st, err := StoreFromEnv(a)
	if err != nil {
		t.Fatal(err)
	}

	results := make(chan int, 8)
	for i := 0; i < 8; i++ {
		go func() { results <- len(pending(ctx, a, st, Subscriptions(a))) }()
	}

	total := 0
	for i := 0; i < 8; i++ {
		total += <-results
	}

	if total != 5 {
		t.Fatalf("5 posts delivered %d times across concurrent deliveries", total)
	}
}
