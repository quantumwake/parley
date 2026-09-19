package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/store"
)

func mustStore(t *testing.T, env Env) store.Store {
	t.Helper()
	st, err := StoreFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}

	return st
}

// gateEnv is a session following "issues", with the gates given.
func gateEnv(t *testing.T, gates ...Gate) (a, b Env) {
	t.Helper()
	a, b, _ = workSessions(t)
	b.Gates = gates
	return a, b
}

// A gate quiets a post that would otherwise wake the agent, and what it
// quiets is kept for the next prompt instead of being lost.
func TestGateQuietsAPostAndKeepsItForTheNextPrompt(t *testing.T) {
	a, b := gateEnv(t, Gate{Name: "intent", Command: `printf '{"verdict":"context","why":"not for you"}'`})
	post(t, a, "question", "who owns the runner identity?", "")

	items := pending(context.Background(), b, mustStore(t, b), Subscriptions(b))
	if len(items) != 1 || !items[0].hold {
		t.Fatalf("the question would wake an ungated session: %+v", items)
	}

	wake, kept := splitByVerdict(context.Background(), b, items)
	if len(wake) != 0 {
		t.Fatalf("the gate quiets it: %+v", wake)
	}

	if len(kept) != 1 || !strings.Contains(kept[0], "who owns the runner") {
		t.Fatalf("it is kept for the next prompt: %q", kept)
	}

	spoolContext(b, kept)
	text, hold := InjectHold(context.Background(), b)
	if hold || !strings.Contains(text, "who owns the runner") {
		t.Fatalf("the next prompt sees it as context, and is not held: hold=%v %q", hold, text)
	}

	if again := drainContext(b); len(again) != 0 {
		t.Fatalf("what was kept is shown once: %q", again)
	}
}

// A gate may only quiet. One that answers "react" for a post the free rules
// had already quieted, or answers rubbish, changes nothing.
func TestAGateMayOnlyLowerAVerdict(t *testing.T) {
	a, b := gateEnv(t, Gate{Name: "loud", Command: `printf '{"verdict":"react"}'`})

	// Addressed to someone else: quiet before any gate runs.
	if err := Post(context.Background(), a, "issues", "question", "engineer, is it indexed?", "engineer", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	items := pending(context.Background(), b, mustStore(t, b), Subscriptions(b))
	wake, _ := splitByVerdict(context.Background(), b, items)
	if len(wake) != 0 {
		t.Fatalf("a gate cannot raise a post the free rules quieted: %+v", wake)
	}

	// Rubbish out of the gate leaves the verdict alone.
	b.Gates = []Gate{{Name: "broken", Command: `printf 'not json'`}}
	post(t, a, "question", "what stamps last_append_at?", "")
	items = pending(context.Background(), b, mustStore(t, b), Subscriptions(b))
	wake, _ = splitByVerdict(context.Background(), b, items)
	if len(wake) != 1 {
		t.Fatalf("a broken gate leaves the post as it was — noisy, not deaf: %+v", wake)
	}
}

// A gate that hangs is given up on, and the post keeps its verdict.
func TestASlowGateFailsOpen(t *testing.T) {
	a, b := gateEnv(t, Gate{Name: "slow", Command: `sleep 5`, TimeoutMs: 100})
	post(t, a, "question", "does the twin resume from its tail?", "")

	start := time.Now()
	items := pending(context.Background(), b, mustStore(t, b), Subscriptions(b))
	wake, _ := splitByVerdict(context.Background(), b, items)
	if len(wake) != 1 {
		t.Fatalf("the post survives a gate that never answers: %+v", wake)
	}

	if time.Since(start) > 2*time.Second {
		t.Fatalf("the gate's timeout bounds the wait: %s", time.Since(start))
	}
}

// Every decision is recorded, and the status line counts them.
func TestVerdictsAreRecordedAndCounted(t *testing.T) {
	a, b := gateEnv(t, Gate{Name: "intent", Command: `printf '{"verdict":"ignore","why":"chatter"}'`})
	post(t, a, "question", "anyone around?", "")

	items := pending(context.Background(), b, mustStore(t, b), Subscriptions(b))
	splitByVerdict(context.Background(), b, items)

	raw, err := os.ReadFile(verdictPath(b, "issues"))
	if err != nil {
		t.Fatalf("the decision is recorded so a wrong ignore can be found: %v", err)
	}

	if !strings.Contains(string(raw), `"verdict":"ignore"`) || !strings.Contains(string(raw), "chatter") {
		t.Fatalf("the record says what was decided and why: %s", raw)
	}

	counts := VerdictCounts(b, time.Hour)
	if len(counts) != 1 || counts[0].Ignore != 1 {
		t.Fatalf("the counts are per conversation: %+v", counts)
	}

	var line strings.Builder
	if err := StatusLine(b, &line); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(line.String(), "issues") {
		t.Fatalf("the status line names the conversation: %q", line.String())
	}
}

// With no gates configured — the default — the free rules decide
// everything, and the decision is still recorded: the counts a person sees
// must be the whole story, not only the gated part.
func TestNoGatesByDefault(t *testing.T) {
	a, b := gateEnv(t)
	post(t, a, "question", "is the move audited?", "")

	items := pending(context.Background(), b, mustStore(t, b), Subscriptions(b))
	wake, kept := splitByVerdict(context.Background(), b, items)
	if len(wake) != 1 || len(kept) != 0 {
		t.Fatalf("an ungated question wakes the agent: wake=%+v kept=%+v", wake, kept)
	}

	raw, err := os.ReadFile(verdictPath(b, "issues"))
	if err != nil || !strings.Contains(string(raw), `"verdict":"react"`) || strings.Contains(string(raw), `"gate"`) {
		t.Fatalf("the free rules' verdict is recorded, with no gate named: %v %s", err, raw)
	}

	counts := VerdictCounts(b, time.Hour)
	if len(counts) != 1 || counts[0].React != 1 {
		t.Fatalf("and it is counted: %+v", counts)
	}
}

// A post that does not hold the turn is kept by the Stop hook, not thrown
// away: the read has already moved the cursor past it.
func TestTheStopHookKeepsWhatItDoesNotHoldFor(t *testing.T) {
	a, b := gateEnv(t)
	if err := Post(context.Background(), a, "issues", "question", "engineer, is it indexed?", "engineer", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	in := strings.NewReader(`{"hook_event_name":"Stop","session_id":"` + b.Session + `"}`)
	if err := Handle(context.Background(), b, in, &out); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(out.String(), `"decision":"block"`) {
		t.Fatalf("a post for another agent does not hold the turn: %q", out.String())
	}

	text, hold := InjectHold(context.Background(), b)
	if hold || !strings.Contains(text, "is it indexed") {
		t.Fatalf("but it is kept and shown on the next prompt: hold=%v %q", hold, text)
	}
}

// Outside a session there is nowhere to keep a quieted post, so a plain
// terminal's wait still prints everything.
func TestASessionlessWaitKeepsPrintingEverything(t *testing.T) {
	a, b := gateEnv(t, Gate{Name: "quiet", Command: `printf '{"verdict":"ignore"}'`})
	post(t, a, "status", "rebuilt", "")
	post(t, a, "question", "anyone?", "")

	b.Session = ""
	items := pending(context.Background(), b, mustStore(t, b), Subscriptions(b))
	wake, kept := splitByVerdict(context.Background(), b, items)
	if len(wake) != len(items) || len(kept) != 0 {
		t.Fatalf("nothing is quieted where nothing can be kept: wake=%d items=%d kept=%+v", len(wake), len(items), kept)
	}
}

// Whatever a person posts is theirs to be answered: their direction often
// arrives as a comment, and filing it as talk is worse than answering too
// often. An agent's comment is still talk.
func TestAPersonsPostAlwaysHoldsTheTurn(t *testing.T) {
	a, b := gateEnv(t)

	post(t, a, "comment", "an agent thinking aloud", "")
	if _, hold := InjectHold(context.Background(), b); hold {
		t.Fatal("an agent's comment is talk")
	}

	st := mustStore(t, b)
	id := mustID(t, a, "issues")
	body, err := json.Marshal(map[string]any{"text": "lets go over the top priority items"})
	if err != nil {
		t.Fatal(err)
	}

	// A person's post: an identity, and no session or participant.
	person := event.Event{ID: event.NewID(), Source: event.SourceClaudeCode, Kind: event.KindPostComment, Identity: "krasaee", Content: body, TSMs: time.Now().UnixMilli()}
	if _, err := conversation.Attach(st, id).Append(context.Background(), true, person); err != nil {
		t.Fatal(err)
	}

	text, hold := InjectHold(context.Background(), b)
	if !hold || !strings.Contains(text, "top priority items") {
		t.Fatalf("a person's comment holds the turn: hold=%v %q", hold, text)
	}
}

// Creating a conversation follows it: owning one you do not follow has no
// use, and the creator is the one who invites everybody else into it.
func TestCreateFollowsWhatItCreated(t *testing.T) {
	a, _ := gateEnv(t)
	var out bytes.Buffer
	if err := CreateShared(context.Background(), a, "proposals", "where proposals go", nil, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "you follow it") || !strings.Contains(out.String(), "parley wait") {
		t.Fatalf("create says it followed, and how to be woken: %q", out.String())
	}

	found := false
	for _, s := range Subscriptions(a) {
		found = found || s.Name == "proposals"
	}

	if !found {
		t.Fatalf("the creator is subscribed: %+v", Subscriptions(a))
	}
}

// A session that follows a conversation and has no listener is told so on
// every prompt, not only when it started: nothing parley can do reaches an
// idle session, so saying it while it is true is the whole remedy.
func TestEveryPromptSaysWhenNoListenerIsArmed(t *testing.T) {
	a, b := gateEnv(t)
	post(t, a, "comment", "anything", "")

	var out bytes.Buffer
	in := strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"` + b.Session + `"}`)
	if err := Handle(context.Background(), b, in, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "no listener is armed") || !strings.Contains(out.String(), "parley wait") {
		t.Fatalf("the prompt carries the notice: %q", out.String())
	}
}
