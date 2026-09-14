package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
)

var eventIDRe = regexp.MustCompile(`\(event ([0-9A-Z]{26})\)`)

// post posts and answers the new event's id and the command's output.
func post(t *testing.T, env Env, kind, text, replyTo string, opts ...PostOption) (string, string) {
	t.Helper()
	var out bytes.Buffer
	if err := Post(context.Background(), env, "issues", kind, text, "*", replyTo, nil, &out, opts...); err != nil {
		t.Fatalf("post %s: %v", kind, err)
	}

	m := eventIDRe.FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no event id in %q", out.String())
	}

	return m[1], out.String()
}

func postErr(env Env, kind, text, replyTo string, opts ...PostOption) error {
	return Post(context.Background(), env, "issues", kind, text, "*", replyTo, nil, &bytes.Buffer{}, opts...)
}

// workSessions: a and b are two sessions of one identity; o is another
// identity. All follow "issues".
func workSessions(t *testing.T) (a, b, o Env) {
	t.Helper()
	s, other := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b, o = s["aaaaaaaa-1111"], s["bbbbbbbb-2222"], other
	o.Session = "cccccccc-3333"
	follow(t, a, "issues")
	for _, e := range []Env{b, o} {
		if err := Join(context.Background(), e, "issues", "full", "all", "", &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}

	return a, b, o
}

func listWork(t *testing.T, env Env, all bool) string {
	t.Helper()
	var out bytes.Buffer
	if err := ListWork(context.Background(), env, nil, all, &out); err != nil {
		t.Fatal(err)
	}

	return out.String()
}

// A request is claimed once; a second claim is refused; the holder hands it
// over and someone else takes it; the holder resolves it.
func TestWorkRequestClaimHandoverResolve(t *testing.T) {
	a, b, o := workSessions(t)

	req, _ := post(t, a, "request", "fix the load-older loop in the console", "")
	claimB, _ := post(t, b, "claim", "on it: parley, branch fix/load-older-loop", req)

	if err := postErr(o, "claim", "me too", req); err == nil || !strings.Contains(err.Error(), "already claimed by") {
		t.Fatalf("a second claim is refused: %v", err)
	}

	if err := postErr(b, "close", "done", claimB); err == nil || !strings.Contains(err.Error(), "needs an outcome") {
		t.Fatalf("a close needs an outcome: %v", err)
	}

	if err := postErr(b, "comment", "fyi", "", WithOutcome(OutcomeResolved)); err == nil || !strings.Contains(err.Error(), "outcome belongs on a close") {
		t.Fatalf("an outcome on a comment is refused: %v", err)
	}

	if err := postErr(b, "close", "done", "", WithOutcome(OutcomeResolved)); err == nil || !strings.Contains(err.Error(), "replies to your claim") {
		t.Fatalf("a close without a reply is refused with guidance: %v", err)
	}

	post(t, b, "close", "handing over", claimB, WithOutcome(OutcomeHandedOver))
	if got := listWork(t, a, false); !strings.Contains(got, "open again (handed_over)") {
		t.Fatalf("handed over work is open again: %q", got)
	}

	claimO, _ := post(t, o, "claim", "taking it over", req)
	if got := listWork(t, o, false); !strings.Contains(got, "(mine)") {
		t.Fatalf("the new holder sees it as mine: %q", got)
	}

	if err := postErr(b, "close", "stale", claimB, WithOutcome(OutcomeResolved)); err == nil || !strings.Contains(err.Error(), "no longer holds") {
		t.Fatalf("a claim that no longer holds cannot close: %v", err)
	}

	post(t, o, "close", "fixed in #14", claimO, WithOutcome(OutcomeResolved))
	if got := listWork(t, a, false); !strings.Contains(got, "no open or claimed work") {
		t.Fatalf("resolved work is not listed: %q", got)
	}

	if got := listWork(t, a, true); !strings.Contains(got, "closed, resolved") {
		t.Fatalf("--all shows it closed: %q", got)
	}
}

// Only the holder closes a claim; only the requester (or the holder) closes
// a request; a request is withdrawn, not handed over.
func TestOnlyTheOwnerCloses(t *testing.T) {
	a, b, o := workSessions(t)

	req, _ := post(t, a, "request", "review PR 15", "")
	claim, _ := post(t, b, "claim", "reviewing", req)

	if err := postErr(o, "close", "dropping yours", claim, WithOutcome(OutcomeDropped)); err == nil || !strings.Contains(err.Error(), "can close their claim") {
		t.Fatalf("another identity cannot close a claim: %v", err)
	}

	if err := postErr(o, "close", "closing theirs", req, WithOutcome(OutcomeResolved)); err == nil || !strings.Contains(err.Error(), "requester or the holder") {
		t.Fatalf("another identity cannot close a request: %v", err)
	}

	if err := postErr(a, "close", "moving it", req, WithOutcome(OutcomeHandedOver)); err == nil || !strings.Contains(err.Error(), "closing the claim") {
		t.Fatalf("a request is not handed over directly: %v", err)
	}

	post(t, a, "close", "not needed any more", req, WithOutcome(OutcomeDropped))
	if got := listWork(t, a, true); !strings.Contains(got, "closed, withdrawn") {
		t.Fatalf("a requester's dropped close withdraws it: %q", got)
	}
}

// Only open work is claimable: a question gets an answer; work nobody asked
// for is a claim with no reply, and handing it over makes it claimable.
func TestOnlyOpenWorkIsClaimable(t *testing.T) {
	a, b, o := workSessions(t)

	q, _ := post(t, a, "question", "why does the console loop on older rows?", "")
	if err := postErr(b, "claim", "investigating", q); err == nil || !strings.Contains(err.Error(), "answer a question with kind answer") {
		t.Fatalf("claiming a question is refused with guidance: %v", err)
	}

	own, _ := post(t, b, "claim", "unprompted: Event.tags unmarshal error, parley fix/tolerant-tags", "")
	if err := postErr(o, "claim", "mine", own); err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("unprompted work in progress cannot be claimed: %v", err)
	}

	if got := listWork(t, a, false); !strings.Contains(got, "in progress") || !strings.Contains(got, "Event.tags") {
		t.Fatalf("unprompted work is listed in progress: %q", got)
	}

	post(t, b, "close", "out of time", own, WithOutcome(OutcomeHandedOver))
	post(t, o, "claim", "picking it up", own)
	if got := listWork(t, o, false); !strings.Contains(got, "claimed by") {
		t.Fatalf("handed-over unprompted work is claimable: %q", got)
	}
}

// Two claims that both passed the check: the earlier holds, the later is
// told, and it cannot close what it never held.
func TestAClaimRaceIsResolvedAndTold(t *testing.T) {
	a, b, o := workSessions(t)
	req, _ := post(t, a, "request", "rebuild the console dist", "")

	// b's claim lands without the check, as a racing claim would.
	st, err := StoreFromEnv(b)
	if err != nil {
		t.Fatal(err)
	}
	raced := event.Event{ID: event.NewID(), TSMs: time.Now().UnixMilli(), Source: event.SourceClaudeCode, Kind: event.KindPostClaim,
		Identity: authorOf(b), SessionID: b.Session, ParentID: req, ReplyTo: req, Thread: req, Content: json.RawMessage(`{"text":"mine"}`)}
	if _, err := conversation.Attach(st, mustID(t, b, "issues")).Append(context.Background(), false, raced); err != nil {
		t.Fatal(err)
	}

	// o's claim passed its check on a stale fold: append it the same way,
	// then post nothing; the fold decides.
	late := raced
	late.ID, late.Identity, late.SessionID, late.Content = event.NewID(), authorOf(o), o.Session, json.RawMessage(`{"text":"also mine"}`)
	if _, err := conversation.Attach(st, mustID(t, o, "issues")).Append(context.Background(), false, late); err != nil {
		t.Fatal(err)
	}

	l, err := readWork(context.Background(), o, st, mustID(t, o, "issues"))
	if err != nil {
		t.Fatal(err)
	}

	if note := claimOutcome(l, late.ID); !strings.Contains(note, "does not hold") {
		t.Fatalf("the later claim is told it lost: %q", note)
	}

	if err := postErr(o, "close", "cleaning up", late.ID, WithOutcome(OutcomeDropped)); err == nil || !strings.Contains(err.Error(), "never held the work") {
		t.Fatalf("a lost claim cannot close: %v", err)
	}

	if mark := workMark(l, late); !strings.Contains(mark, "lost") {
		t.Fatalf("delivery marks the lost claim: %q", mark)
	}
}

// A close written by another client with no valid outcome changes nothing,
// and its mark says it was ignored.
func TestAnInvalidCloseIsIgnored(t *testing.T) {
	a, b, _ := workSessions(t)
	req, _ := post(t, a, "request", "tidy docs", "")
	claim, _ := post(t, b, "claim", "tidying", req)

	st, _ := StoreFromEnv(b)
	bad := event.Event{ID: event.NewID(), TSMs: time.Now().UnixMilli(), Source: event.SourceProduct, Kind: event.KindPostClose,
		Identity: authorOf(b), ParentID: claim, ReplyTo: claim, Thread: claim, Content: json.RawMessage(`{"text":"done"}`)}
	if _, err := conversation.Attach(st, mustID(t, b, "issues")).Append(context.Background(), false, bad); err != nil {
		t.Fatal(err)
	}

	l, _ := readWork(context.Background(), a, st, mustID(t, a, "issues"))
	if got := workMark(l, bad); !strings.Contains(got, "ignored: no valid outcome") {
		t.Fatalf("the invalid close is marked ignored: %q", got)
	}

	if got := listWork(t, a, false); !strings.Contains(got, "claimed by") {
		t.Fatalf("the work is still claimed: %q", got)
	}
}

// Delivery shows where a work post's item stands now, including in digest
// mode for requests.
func TestWorkPostsAreDeliveredWithTheirState(t *testing.T) {
	a, b, _ := workSessions(t)
	req, _ := post(t, a, "request", "review PR 12", "")
	post(t, b, "claim", "reviewing", req)

	if got := Inject(context.Background(), a); !strings.Contains(got, "[work: claimed by") {
		t.Fatalf("the claim is delivered with its state: %q", got)
	}

	if !isDigest(event.Event{Kind: event.KindPostRequest}) {
		t.Fatal("a digest follower still sees requests")
	}

	if g := WorkGuide("parley"); len(g) > 500 || !strings.Contains(g, "need no claim") || !strings.Contains(g, "`parley work`") {
		t.Fatalf("the guide is short and says a question needs no claim: %d %q", len(g), g)
	}
}

// A fold continues from its cache instead of re-reading the conversation.
func TestWorkFoldIsCached(t *testing.T) {
	a, _, _ := workSessions(t)
	post(t, a, "request", "one", "")
	st, _ := StoreFromEnv(a)
	id := mustID(t, a, "issues")
	if _, err := readWork(context.Background(), a, st, id); err != nil {
		t.Fatal(err)
	}

	cached := loadWorkCache(a, id)
	if cached.Next == 0 || len(cached.Items) != 1 {
		t.Fatalf("the fold is cached: %+v", cached)
	}

	post(t, a, "request", "two", "")
	l, _ := readWork(context.Background(), a, st, id)
	if len(l.Items) != 2 || l.Next <= cached.Next {
		t.Fatalf("the fold continued from the cache: %+v", l)
	}
}
