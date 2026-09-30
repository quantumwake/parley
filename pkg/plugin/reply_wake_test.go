package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
)

// personReply appends what the portal writes when a person answers a post:
// a reply with no @ and no to.
func personReply(t *testing.T, env Env, kind event.Kind, text, replyTo, to string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"text": text})
	e := event.Event{ID: event.NewID(), Source: event.SourceClaudeCode, Kind: kind, Identity: "krasaee", Participant: "Kasra Rasaee",
		To: to, ReplyTo: replyTo, ParentID: replyTo, Thread: replyTo, Content: body, TSMs: time.Now().UnixMilli()}
	if _, err := conversation.Attach(mustStore(t, env), mustID(t, env, "issues")).Append(context.Background(), true, e); err != nil {
		t.Fatal(err)
	}
}

// A person answers an agent's post without naming it. The agent that wrote
// the post is the one the reply is for: it holds that agent's turn, and
// only that agent's.
func TestAnUnaddressedReplyHoldsTheAuthorsTurn(t *testing.T) {
	a, b, o := workSessions(t)
	mine, _ := post(t, a, "report", "the node is the server", "")
	InjectHold(context.Background(), b)
	InjectHold(context.Background(), o)

	personReply(t, a, event.KindPostComment, "why do we have a server and a node?", mine, "")

	text, hold := InjectHold(context.Background(), a)
	if !hold || !strings.Contains(text, "server and a node") {
		t.Fatalf("the author of the post is woken by a reply to it: hold=%v %q", hold, text)
	}

	for _, other := range []Env{b, o} {
		text, hold := InjectHold(context.Background(), other)
		if hold {
			t.Fatalf("a reply to someone else's post is talk here: %q", text)
		}

		if !strings.Contains(text, "server and a node") {
			t.Fatalf("it is still delivered as context: %q", text)
		}
	}
}

// The listener wakes for it too, not only the Stop hook.
func TestAnUnaddressedReplyWakesTheAuthorsWait(t *testing.T) {
	a, b, _ := workSessions(t)
	mine, _ := post(t, b, "comment", "rebuilt the console", "")
	personReply(t, b, event.KindPostComment, "it still shows the old counts", mine, "")

	var out bytes.Buffer
	if err := Wait(context.Background(), b, nil, 2*WaitPoll, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "old counts") {
		t.Fatalf("the wait of the post's author wakes: %q", out.String())
	}

	out.Reset()
	if err := Wait(context.Background(), a, nil, 2*WaitPoll, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "no new posts") {
		t.Fatalf("another seat's wait does not: %q", out.String())
	}
}

// A reply that names someone goes to whom it names, as before.
func TestANamedReplyGoesOnlyToWhomItNames(t *testing.T) {
	a, _, _ := workSessions(t)
	mine, _ := post(t, a, "comment", "the pool cap is #205", "")
	personReply(t, a, event.KindPostComment, "lets review this with champion", mine, "champion")

	if text, hold := InjectHold(context.Background(), a); hold {
		t.Fatalf("a reply addressed to champion is talk for the author: %q", text)
	}
}

// Claims and closes answer the work, not its author.
func TestAClaimOnMyRequestDoesNotHoldMyTurn(t *testing.T) {
	a, b, _ := workSessions(t)
	req, _ := post(t, a, "request", "tag v0.3.67", "")
	post(t, b, "claim", "tagging", req)

	if text, hold := InjectHold(context.Background(), a); hold {
		t.Fatalf("a claim on my request is bookkeeping: %q", text)
	}
}

// The author is matched by handle as well as session: an agent that
// restarted under a new session keeps its handle, and replies to what it
// wrote before still reach it. A reply to another handle does not.
func TestAReplyReachesTheAuthorByHandle(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	fold := newWorkLog()
	fold.apply(event.Event{ID: "p1", Kind: event.KindPostComment, SessionID: "old-session", Participant: "statefs-architect"}, 1)
	fold.apply(event.Event{ID: "p2", Kind: event.KindPostComment, SessionID: "bbbbbbbb-2222", Participant: "champion"}, 2)

	sub := Subscription{Name: "issues", ID: "ns", Mode: "full", Participant: "statefs-architect"}
	rows := []nsRow{
		{e: event.Event{Kind: event.KindPostComment, Identity: "kasra", ReplyTo: "p1"}, pos: 3},
		{e: event.Event{Kind: event.KindPostComment, Identity: "kasra", ReplyTo: "p2"}, pos: 4},
		{e: event.Event{Kind: event.KindPostComment, Identity: "kasra", ReplyTo: "unknown"}, pos: 5},
	}

	items, _ := fanoutRows(a, sub, rows, fold)
	if len(items) != 3 {
		t.Fatalf("every reply is delivered: %+v", items)
	}

	for _, it := range items {
		want := it.e.ReplyTo == "p1"
		if it.mine != want || it.hold != want {
			t.Fatalf("reply to %s: mine=%v hold=%v, want %v", it.e.ReplyTo, it.mine, it.hold, want)
		}
	}

	// A digest keeps the reply to this handle and drops the others, which
	// are chatter there.
	sub.Mode = "digest"
	items, head := fanoutRows(a, sub, rows, fold)
	if head != 5 || len(items) != 1 || items[0].e.ReplyTo != "p1" {
		t.Fatalf("digest keeps only the reply to this handle: head=%d %+v", head, items)
	}
}
