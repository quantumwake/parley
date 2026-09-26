package plugin

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// Scenario tests for "does a post ever reach a session twice, and does a
// session's cursor ever run past the conversation's head". Three sessions of
// one identity, as on a laptop: a, b and c. b writes; a and c read.

func say(t *testing.T, env Env, conv, text string) {
	t.Helper()
	if err := Post(context.Background(), env, conv, "comment", text, "", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

// ask is a post that wakes a wait (a question); say is a plain comment, which
// only rides the context spool to the next prompt.
func ask(t *testing.T, env Env, conv, text string) {
	t.Helper()
	if err := Post(context.Background(), env, conv, "question", text, "", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func count(haystack, needle string) int { return strings.Count(haystack, needle) }

// cursorOf is the session's cursor for one conversation, as the hook sees it.
func cursorOf(t *testing.T, env Env, conv string) int64 {
	t.Helper()
	for _, s := range Subscriptions(env) {
		if s.Name == conv {
			return s.Cursor
		}
	}
	t.Fatalf("%s does not follow %s", env.Session, conv)
	return -1
}

func headOf(t *testing.T, env Env, conv string) int64 {
	t.Helper()
	st, err := StoreFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range Subscriptions(env) {
		if s.Name == conv {
			h, err := st.Head(context.Background(), s.ID)
			if err != nil {
				t.Fatal(err)
			}
			return int64(h)
		}
	}
	t.Fatalf("no %s", conv)
	return -1
}

// The prompt hook shows each post once, and a resumed session (the same
// session id, a new process) does not see it again.
func TestScenarioTheHookShowsEachPostOnceAcrossAResume(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
	ctx := context.Background()

	for _, w := range []string{"one", "two", "three"} {
		say(t, b, "issues", w)
	}

	first := Inject(ctx, a)
	for _, w := range []string{"one", "two", "three"} {
		if count(first, w) != 1 {
			t.Fatalf("first prompt: %q appears %d times\n%s", w, count(first, w), first)
		}
	}

	if got := Inject(ctx, a); strings.TrimSpace(got) != "" {
		t.Fatalf("a second prompt with nothing new showed:\n%s", got)
	}

	// A resume is the same session id in a new process: the same Env, and
	// the state on disk is all that carries over.
	resumed := a
	if got := Inject(ctx, resumed); strings.TrimSpace(got) != "" {
		t.Fatalf("a resumed session was shown something it had read:\n%s", got)
	}

	say(t, b, "issues", "four")
	third := Inject(ctx, resumed)
	if count(third, "four") != 1 || count(third, "one") != 0 || count(third, "three") != 0 {
		t.Fatalf("after the resume only the new post should show:\n%s", third)
	}
}

// A session's cursor never passes the conversation's head: not after reads,
// not after a hook, not after a wait.
func TestScenarioACursorNeverPassesTheHead(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
	ctx := context.Background()
	withWaitStore(t, a)

	check := func(step string) {
		t.Helper()
		if c, h := cursorOf(t, a, "issues"), headOf(t, a, "issues"); c > h {
			t.Fatalf("%s: the cursor is %d and the head is %d", step, c, h)
		}
	}

	check("after joining")

	for i := 0; i < 4; i++ {
		say(t, b, "issues", fmt.Sprintf("post-%d", i))
		check(fmt.Sprintf("after post %d", i))
	}

	_ = Inject(ctx, a)
	check("after the hook")

	ask(t, b, "issues", "later")
	var out bytes.Buffer
	if err := Wait(ctx, a, nil, 5*time.Second, &out); err != nil {
		t.Fatal(err)
	}
	check("after a wait")

	if err := Read(ctx, a, "issues", 0, true, 0, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	check("after a peek from the start")

	if err := Read(ctx, a, "issues", 0, false, 0, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	check("after a read from the start")
}

// A wait and the hook never both deliver one post, in either order.
func TestScenarioAWaitAndTheHookNeverBothDeliver(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
	ctx := context.Background()
	withWaitStore(t, a)

	// A wait delivers it, then the hook must not.
	ask(t, b, "issues", "delivered-by-wait")
	var w1 bytes.Buffer
	if err := Wait(ctx, a, nil, 5*time.Second, &w1); err != nil {
		t.Fatal(err)
	}
	if count(w1.String(), "delivered-by-wait") != 1 {
		t.Fatalf("the wait should deliver it once:\n%s", w1.String())
	}
	if got := Inject(ctx, a); count(got, "delivered-by-wait") != 0 {
		t.Fatalf("the hook repeated what a wait delivered:\n%s", got)
	}

	// The hook delivers it, then a wait must not.
	ask(t, b, "issues", "delivered-by-hook")
	if got := Inject(ctx, a); count(got, "delivered-by-hook") != 1 {
		t.Fatalf("the hook should deliver it once:\n%s", got)
	}
	var w2 bytes.Buffer
	if err := Wait(ctx, a, nil, 300*time.Millisecond, &w2); err != nil {
		t.Fatal(err)
	}
	if count(w2.String(), "delivered-by-hook") != 0 {
		t.Fatalf("a wait repeated what the hook delivered:\n%s", w2.String())
	}
}

// Back-to-back waits, each armed after the last exited (the pattern of an
// agent that re-arms after every post), each print only what is new.
func TestScenarioBackToBackWaitsEachPrintOnlyWhatIsNew(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
	ctx := context.Background()
	withWaitStore(t, a)

	seen := map[string]int{}

	for i := 0; i < 5; i++ {
		word := fmt.Sprintf("word-%d", i)
		ask(t, b, "issues", word)

		var out bytes.Buffer
		if err := Wait(ctx, a, nil, 5*time.Second, &out); err != nil {
			t.Fatal(err)
		}

		for j := 0; j <= i; j++ {
			w := fmt.Sprintf("word-%d", j)
			n := count(out.String(), w)
			seen[w] += n

			if j == i && n != 1 {
				t.Fatalf("wait %d: %q printed %d times, want 1\n%s", i, w, n, out.String())
			}
			if j < i && n != 0 {
				t.Fatalf("wait %d repeated the earlier post %q\n%s", i, w, out.String())
			}
		}
	}

	for w, n := range seen {
		if n != 1 {
			t.Fatalf("%q was delivered %d times in all", w, n)
		}
	}
}

// Two sessions of one identity wait together: the poller and a non-poller.
// Each post reaches each session exactly once.
func TestScenarioTwoWaitingSessionsEachGetEachPostOnce(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222", "cccccccc-3333")
	a, b, c := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"], s["cccccccc-3333"]
	follow(t, a, "issues")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
	_ = Join(context.Background(), c, "issues", "full", "all", "", &bytes.Buffer{})
	ctx := context.Background()
	withWaitStore(t, a)

	for round := 0; round < 3; round++ {
		word := fmt.Sprintf("round-%d", round)

		var wg sync.WaitGroup
		outs := map[string]*bytes.Buffer{"a": {}, "b": {}}

		for name, env := range map[string]Env{"a": a, "b": b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = Wait(ctx, env, nil, 10*time.Second, outs[name])
			}()
		}

		time.Sleep(200 * time.Millisecond)
		ask(t, c, "issues", word)
		wg.Wait()

		for name, out := range outs {
			if count(out.String(), word) != 1 {
				t.Fatalf("round %d: session %s got %q %d times\n%s", round, name, word, count(out.String(), word), out.String())
			}

			for prior := 0; prior < round; prior++ {
				if p := fmt.Sprintf("round-%d", prior); count(out.String(), p) != 0 {
					t.Fatalf("round %d: session %s was shown the earlier post %q again\n%s", round, name, p, out.String())
				}
			}
		}
	}
}

// A new session of the same identity inherits the machine's follows. The
// question is where its cursor starts: the machine watermark, which is only
// as fresh as the last write to the machine record. This reports what a
// brand-new session is shown of a conversation the other sessions have long
// since read.
func TestScenarioANewSessionIsNotReplayedWhatTheMachineAlreadyRead(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222", "cccccccc-3333")
	a, b, c := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"], s["cccccccc-3333"]
	follow(t, a, "issues")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
	ctx := context.Background()

	for _, w := range []string{"old-one", "old-two", "old-three"} {
		say(t, b, "issues", w)
	}

	_ = Inject(ctx, a) // a reads all of it; the head is 3 and a's cursor is 3

	// c is a new session of the same identity, started well after.
	if n := inheritMachineFollows(c); n != 1 {
		t.Fatalf("c should have inherited 1 follow, got %d", n)
	}

	got := Inject(ctx, c)
	t.Logf("new session c cursor=%d head=%d; first prompt shows:\n%s", cursorOf(t, c, "issues"), headOf(t, c, "issues"), got)

	for _, w := range []string{"old-one", "old-two", "old-three"} {
		if count(got, w) != 0 {
			t.Errorf("a brand-new session was replayed %q, which the machine had already read", w)
		}
	}
}

// The realistic mix: comments (which only spool for the next prompt) and
// questions (which wake a wait), arriving while sessions alternate between
// waits and prompts. Every post must reach the session exactly once across
// the two channels, and none may be lost.
func TestScenarioCommentsAndQuestionsReachASessionExactlyOnceAcrossWaitAndHook(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
	ctx := context.Background()
	withWaitStore(t, a)

	shown := map[string]int{}
	tally := func(out string) {
		for i := 0; i < 12; i++ {
			w := fmt.Sprintf("msg-%02d", i)
			shown[w] += count(out, w)
		}
	}

	for i := 0; i < 12; i++ {
		w := fmt.Sprintf("msg-%02d", i)
		if i%3 == 0 {
			ask(t, b, "issues", w) // wakes a wait
		} else {
			say(t, b, "issues", w) // spools for the next prompt
		}

		switch i % 4 {
		case 0, 1: // a wait, then a prompt
			var out bytes.Buffer
			_ = Wait(ctx, a, nil, 400*time.Millisecond, &out)
			tally(out.String())
			tally(Inject(ctx, a))
		case 2: // a prompt only
			tally(Inject(ctx, a))
		case 3: // nothing this turn: the posts pile up unread
		}
	}

	// A final prompt and a final wait collect anything still spooled.
	tally(Inject(ctx, a))
	var last bytes.Buffer
	_ = Wait(ctx, a, nil, 400*time.Millisecond, &last)
	tally(last.String())
	tally(Inject(ctx, a))

	for i := 0; i < 12; i++ {
		w := fmt.Sprintf("msg-%02d", i)
		if shown[w] != 1 {
			t.Errorf("%s reached the session %d times, want exactly once", w, shown[w])
		}
	}
}

// A conversation this session has left is silent for it: the hook shows
// nothing from it and a wait does not wake for it, while another session that
// still follows it keeps hearing it.
func TestScenarioALeftConversationIsSilentForThatSessionOnly(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222", "cccccccc-3333")
	a, b, c := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"], s["cccccccc-3333"]
	follow(t, a, "issues", "chatter")
	for _, e := range []Env{b, c} {
		_ = Join(context.Background(), e, "issues", "full", "all", "", &bytes.Buffer{})
		_ = Join(context.Background(), e, "chatter", "full", "all", "", &bytes.Buffer{})
	}
	ctx := context.Background()
	withWaitStore(t, a)

	if err := Leave(a, "chatter", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"comment", "question", "report", "status"} {
		if err := Post(ctx, b, "chatter", kind, "left-"+kind, "", "", nil, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	// an @everyone question, which holds a turn for everyone who follows
	if err := Post(ctx, b, "chatter", "question", "@everyone left-everyone", "everyone", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	got := Inject(ctx, a)
	if strings.Contains(got, "left-") {
		t.Fatalf("the hook showed a conversation this session left:\n%s", got)
	}

	// a stayed on "issues", and it still hears that.
	ask(t, b, "issues", "still-followed")
	var out bytes.Buffer
	if err := Wait(ctx, a, nil, 5*time.Second, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "still-followed") || strings.Contains(out.String(), "left-") {
		t.Fatalf("the wait should deliver the followed post and nothing from the left one:\n%s", out.String())
	}

	// c never left and still hears the conversation a left.
	if got := Inject(ctx, c); !strings.Contains(got, "left-comment") {
		t.Fatalf("a session that did not leave lost the conversation:\n%s", got)
	}
}

// The hypothesis behind "the hook replays posts I already read": a comment
// that arrives with a question is spooled while the wait wakes for the
// question. If the session then LOOKS at the channel itself (`parley read`,
// with or without --peek, which is what an agent does to catch up), the
// next prompt still shows the spooled comment, because nothing removes a
// spooled line when the same post is read another way. This test records
// what happens; it fails when the post is shown again.
func TestScenarioASpooledCommentIsNotShownAgainAfterTheSessionReadItItself(t *testing.T) {
	for _, peek := range []bool{true, false} {
		t.Run(fmt.Sprintf("peek=%v", peek), func(t *testing.T) {
			s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
			a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
			follow(t, a, "issues")
			_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
			ctx := context.Background()
			withWaitStore(t, a)

			say(t, b, "issues", "spooled-comment") // context, not a wake
			ask(t, b, "issues", "wake-me")         // a wake

			var out bytes.Buffer
			if err := Wait(ctx, a, nil, 5*time.Second, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "wake-me") {
				t.Fatalf("the wait should have woken for the question:\n%s", out.String())
			}

			var read bytes.Buffer
			if err := Read(ctx, a, "issues", 0, peek, 0, &read); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(read.String(), "spooled-comment") {
				t.Fatalf("the read should show the comment:\n%s", read.String())
			}

			if got := Inject(ctx, a); strings.Contains(got, "spooled-comment") {
				t.Errorf("the prompt showed a comment the session had already read itself (peek=%v):\n%s", peek, got)
			}
		})
	}
}

// "No listener is armed" is WaitLive, which needs every conversation the
// session follows to be among the positions its wait has recorded. A
// conversation added while a wait is already running (a new follow, or one
// inherited at a restart) is not there yet. This measures how long the hook
// would wrongly say "none armed" for a wait that is in fact running.
func TestScenarioAConversationAddedToARunningWaitDoesNotMakeTheHookSayNoneIsArmed(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	follow(t, a, "issues")
	withWaitStore(t, a)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Wait(context.Background(), a, nil, 4*time.Second, &bytes.Buffer{})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !WaitLive(a) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !WaitLive(a) {
		t.Fatal("a running wait is not live even before anything changed")
	}

	follow(t, a, "extra") // a conversation added while the wait runs
	start := time.Now()

	if WaitLive(a) {
		t.Logf("still live the instant after adding a conversation")
		<-done
		return
	}

	for !WaitLive(a) && time.Since(start) < 3*time.Second {
		time.Sleep(20 * time.Millisecond)
	}

	t.Errorf("the hook said no listener was armed for %v after a conversation was added to a running wait (live again: %v)", time.Since(start).Round(10*time.Millisecond), WaitLive(a))
	<-done
}
