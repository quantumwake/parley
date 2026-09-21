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
	if err := Post(context.Background(), env, "issues", kind, text, "", replyTo, nil, &out, opts...); err != nil {
		t.Fatalf("post %s: %v", kind, err)
	}

	m := eventIDRe.FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no event id in %q", out.String())
	}

	return m[1], out.String()
}

func postErr(env Env, kind, text, replyTo string, opts ...PostOption) error {
	return Post(context.Background(), env, "issues", kind, text, "", replyTo, nil, &bytes.Buffer{}, opts...)
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

	// A holder of another identity may resolve a request, not withdraw it.
	req2, _ := post(t, a, "request", "rebuild the docs", "")
	post(t, o, "claim", "on it", req2)
	if err := postErr(o, "close", "not mine to drop", req2, WithOutcome(OutcomeDropped)); err == nil || !strings.Contains(err.Error(), "only the requester can withdraw") {
		t.Fatalf("the holder cannot withdraw the request: %v", err)
	}

	post(t, o, "close", "rebuilt", req2, WithOutcome(OutcomeResolved))

	if err := postErr(o, "close", "moving it", req, WithOutcome(OutcomeHandedOver)); err == nil || !strings.Contains(err.Error(), "requester or the holder") {
		t.Fatalf("a stranger is told who may close, before anything else: %v", err)
	}

	post(t, a, "close", "not needed any more", req, WithOutcome(OutcomeDropped))
	if got := listWork(t, a, true); !strings.Contains(got, "closed, withdrawn") {
		t.Fatalf("a requester's dropped close withdraws it: %q", got)
	}
}

// Only open work is claimable: a question is claimed once and answered
// once; work nobody asked for is a claim with no reply, and handing it over
// makes it claimable.
func TestOnlyOpenWorkIsClaimable(t *testing.T) {
	a, b, o := workSessions(t)

	q, _ := post(t, a, "question", "why does the console loop on older rows?", "")
	post(t, b, "claim", "investigating", q)
	if err := postErr(o, "claim", "me too", q); err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("a claimed question is not claimed again: %v", err)
	}

	post(t, b, "answer", "it re-reads from the cursor it saved", q)
	if err := postErr(o, "claim", "late", q); err == nil || !strings.Contains(err.Error(), "already answered that question") {
		t.Fatalf("an answered question is not claimable, and says who answered: %v", err)
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

	// Unprompted work handed over and not taken can be ended by its starter.
	other, _ := post(t, b, "claim", "unprompted: flaky test", "")
	post(t, b, "close", "later", other, WithOutcome(OutcomeHandedOver))
	post(t, b, "close", "not worth it", other, WithOutcome(OutcomeDropped))
	if got := listWork(t, a, true); !strings.Contains(got, "flaky test") || !strings.Contains(got, "closed, dropped") {
		t.Fatalf("reopened unprompted work can be ended by its starter: %q", got)
	}
	if got := listWork(t, o, false); !strings.Contains(got, "claimed by") {
		t.Fatalf("handed-over unprompted work is claimable: %q", got)
	}
}

// WorkMarks keys the mark for a request, a question and a claim by each
// one's own event id, for the console to look up beside the row it shows.
func TestWorkMarks(t *testing.T) {
	a, b, _ := workSessions(t)
	req, _ := post(t, a, "request", "rebuild the index", "")
	claim, _ := post(t, b, "claim", "on it", req)
	q, _ := post(t, a, "question", "who owns the runner?", "")

	st := mustStore(t, a)
	marks, err := WorkMarks(context.Background(), a, st, mustID(t, a, "issues"))
	if err != nil {
		t.Fatal(err)
	}

	if m := marks[req]; m.Kind != "request" || m.State != "claimed" || m.Holder == "" {
		t.Fatalf("the request is keyed by its own id, and shows who claimed it: %+v", m)
	}

	if m := marks[claim]; m.Kind != "request" || m.State != "claimed" {
		t.Fatalf("the claim is keyed to the item it holds: %+v", m)
	}

	if m := marks[q]; m.Kind != "question" || m.State != "open" {
		t.Fatalf("an unclaimed, unanswered question is open: %+v", m)
	}

	post(t, b, "answer", "the application does", q)
	marks, err = WorkMarks(context.Background(), a, st, mustID(t, a, "issues"))
	if err != nil {
		t.Fatal(err)
	}

	if m := marks[q]; m.State != "closed" || m.Outcome != "answered" {
		t.Fatalf("an answered question closes with that outcome: %+v", m)
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

	if g := WorkGuide("parley"); len(g) > 520 || !strings.Contains(g, "needs no claim") || !strings.Contains(g, "`parley work`") {
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

// A cache from other fold rules, or past the conversation's head, is
// discarded.
func TestWorkCacheIsDiscardedWhenStale(t *testing.T) {
	a, _, _ := workSessions(t)
	post(t, a, "request", "one", "")
	st, _ := StoreFromEnv(a)
	id := mustID(t, a, "issues")

	stale := newWorkLog()
	stale.Version, stale.Next = workFoldVersion-1, 1
	saveWorkCache(a, id, stale)
	if l, _ := readWork(context.Background(), a, st, id); len(l.Items) != 1 {
		t.Fatalf("an old fold version is refolded: %+v", l)
	}

	ahead := newWorkLog()
	ahead.Next = 99
	saveWorkCache(a, id, ahead)
	if l, _ := readWork(context.Background(), a, st, id); len(l.Items) != 1 || l.Next > 99 {
		t.Fatalf("a cache past head is refolded: %+v", l)
	}
}

// A question holds a turn until it is claimed or answered, and a post
// addressed to another agent never holds one: that is what keeps a channel
// of agents from all answering the same question.
func TestOnlyPostsForThisSessionHoldTheTurn(t *testing.T) {
	a, b, o := workSessions(t)

	q, _ := post(t, a, "question", "who owns the indexer's key?", "")
	text, hold := InjectHold(context.Background(), b)
	if !hold || !strings.Contains(text, "who owns the indexer") {
		t.Fatalf("an unanswered question is delivered and holds the turn: hold=%v %q", hold, text)
	}

	// o has not seen it yet; b's answer arrives first.
	post(t, b, "answer", "the application does", q)
	if text, hold := InjectHold(context.Background(), o); hold {
		t.Fatalf("an answered question is delivered but does not hold the turn: %q", text)
	}

	// Addressed to someone else: delivered, never held.
	var out bytes.Buffer
	if err := Post(context.Background(), a, "issues", "question", "engineer, is the twin indexed?", "engineer", "", nil, &out); err != nil {
		t.Fatal(err)
	}

	text, hold = InjectHold(context.Background(), o)
	if hold {
		t.Fatalf("a question for another agent does not hold the turn: %q", text)
	}

	if !strings.Contains(text, "is the twin indexed") {
		t.Fatalf("it is still delivered as context: %q", text)
	}
}

func TestEveryoneHoldsEveryListener(t *testing.T) {
	a, b, o := workSessions(t)
	if err := Post(context.Background(), a, "issues", "comment", "all of you, evaluate this", "everyone", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, hold := InjectHold(context.Background(), b); !hold {
		t.Fatal("b must evaluate @everyone")
	}
	if _, hold := InjectHold(context.Background(), o); !hold {
		t.Fatal("o must evaluate @everyone")
	}
}

// Delivery says where a question stands, so a reader can see it is taken.
func TestQuestionStateIsDelivered(t *testing.T) {
	a, b, o := workSessions(t)
	q, _ := post(t, a, "question", "why is the cursor stuck?", "")
	post(t, b, "claim", "digging", q)

	if got, _ := InjectHold(context.Background(), o); !strings.Contains(got, "[question: claimed by") {
		t.Fatalf("the question is delivered as claimed: %q", got)
	}
}

// Two sessions of one identity start the same work without being asked. The
// second is not refused, but its post says who holds the subject, at which
// position and in which session, and only the first is listed as work.
func TestUnpromptedClaimsOnOneSubjectCollide(t *testing.T) {
	a, b, o := workSessions(t)

	first, out := post(t, a, "claim", "writing the ideas log", "", WithSubject("statefs.ai/docs/IDEAS.md"))
	if strings.Contains(out, "does not hold") {
		t.Fatalf("the first claim on a subject holds it: %q", out)
	}

	// The same subject written differently, from another session of one identity.
	second, out := post(t, b, "claim", "also writing the ideas log", "", WithSubject("  StateFS.ai/docs/ideas.MD "))
	for _, want := range []string{"does not hold", "claimed the same subject first at @", "#aaaaaaaa", "no close is needed", "coordinate"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the second claim is told it lost, and to whom (%q missing): %q", want, out)
		}
	}

	if got := listWork(t, o, false); strings.Count(got, "\n    ") != 1 || !strings.Contains(got, "#aaaaaaaa") || strings.Contains(got, "also writing") {
		t.Fatalf("one item, held by the first session, shown with its session: %q", got)
	}

	// A different subject and a claim with none are both unaffected.
	if _, out := post(t, o, "claim", "the console", "", WithSubject("pkg/console")); strings.Contains(out, "does not hold") {
		t.Fatalf("another subject is another item: %q", out)
	}

	if _, out := post(t, o, "claim", "no subject named", ""); strings.Contains(out, "does not hold") {
		t.Fatalf("a claim without a subject collides with nothing: %q", out)
	}

	if err := postErr(b, "close", "cleaning up", second, WithOutcome(OutcomeDropped)); err == nil || !strings.Contains(err.Error(), "never held the work") {
		t.Fatalf("the claim that lost cannot close: %v", err)
	}

	// The subject is free again once its holder lets go of it.
	post(t, a, "close", "written", first, WithOutcome(OutcomeResolved))
	if _, out := post(t, b, "claim", "revising it", "", WithSubject("statefs.ai/docs/IDEAS.md")); strings.Contains(out, "does not hold") {
		t.Fatalf("a closed subject can be claimed again: %q", out)
	}
}

// A claim on a subject whose holder handed it over starts a new item, and the
// newest item on a subject is the one a later claim collides with.
func TestSubjectAfterHandover(t *testing.T) {
	a, b, o := workSessions(t)

	first, _ := post(t, a, "claim", "starting", "", WithSubject("branch fix/x"))
	post(t, a, "close", "not mine any more", first, WithOutcome(OutcomeHandedOver))

	if _, out := post(t, b, "claim", "taking it", "", WithSubject("branch fix/x")); strings.Contains(out, "does not hold") {
		t.Fatalf("nobody holds a handed-over subject: %q", out)
	}

	if _, out := post(t, o, "claim", "and me", "", WithSubject("branch fix/x")); !strings.Contains(out, "does not hold") {
		t.Fatalf("the new holder is the one collided with: %q", out)
	}
}

// The fold, not the poster's cache, decides. A claim that was appended by
// another host after this one's pre-read is told it lost by the read after
// the append, so it is the read after the append that must run for an
// unprompted claim.
func TestSubjectCollisionSeenByTheFoldAfterAppend(t *testing.T) {
	a, b, _ := workSessions(t)
	st, err := StoreFromEnv(a)
	if err != nil {
		t.Fatal(err)
	}

	id := mustID(t, a, "issues")
	claim := func(env Env, subject string) event.Event {
		return event.Event{ID: event.NewID(), TSMs: time.Now().UnixMilli(), Source: event.SourceClaudeCode, Kind: event.KindPostClaim,
			Identity: authorOf(env), SessionID: env.Session, Content: json.RawMessage(`{"text":"mine","subject":"` + subject + `"}`)}
	}

	early, late := claim(a, "pkg/x"), claim(b, "PKG/X")
	for _, e := range []event.Event{early, late} {
		e.Thread = e.ID
		if _, err := conversation.Attach(st, id).Append(context.Background(), false, e); err != nil {
			t.Fatal(err)
		}
	}

	l, err := readWork(context.Background(), a, st, id)
	if err != nil {
		t.Fatal(err)
	}

	if note := claimOutcome(l, early.ID); note != "" {
		t.Fatalf("the earlier claim holds: %q", note)
	}

	if note := claimOutcome(l, late.ID); !strings.Contains(note, "claimed the same subject first") {
		t.Fatalf("the later claim is told it lost: %q", note)
	}

	if strings.Count(claimOutcome(l, late.ID), "session") > 1 {
		t.Fatalf("the session is shown once: %q", claimOutcome(l, late.ID))
	}
}

// The fold caps a subject even when the row did not come through Post.
func TestFoldCapsAnOversizedSubject(t *testing.T) {
	a, _, _ := workSessions(t)
	st, err := StoreFromEnv(a)
	if err != nil {
		t.Fatal(err)
	}

	id := mustID(t, a, "issues")
	huge := strings.Repeat("a", maxSubject+50)
	e := event.Event{ID: event.NewID(), TSMs: time.Now().UnixMilli(), Source: event.SourceClaudeCode, Kind: event.KindPostClaim,
		Identity: authorOf(a), SessionID: a.Session, Content: json.RawMessage(`{"text":"mine","subject":"` + huge + `"}`)}
	e.Thread = e.ID
	if _, err := conversation.Attach(st, id).Append(context.Background(), false, e); err != nil {
		t.Fatal(err)
	}

	l, err := readWork(context.Background(), a, st, id)
	if err != nil {
		t.Fatal(err)
	}

	item := l.Items[e.ID]
	if item == nil || len(item.Subject) != maxSubject {
		t.Fatalf("fold caps the subject: %+v", item)
	}
}

// A subject means something only on work nobody requested.
func TestSubjectIsRefusedWhereItMeansNothing(t *testing.T) {
	a, b, _ := workSessions(t)
	req, _ := post(t, a, "request", "do a thing", "")

	for _, tc := range []struct{ kind, reply string }{{"comment", ""}, {"request", ""}, {"claim", req}} {
		if err := postErr(b, tc.kind, "x", tc.reply, WithSubject("pkg/x")); err == nil || !strings.Contains(err.Error(), "subject names work nobody requested") {
			t.Fatalf("a subject on a %s (reply %q) is refused with guidance: %v", tc.kind, tc.reply, err)
		}
	}

	if err := postErr(b, "claim", "x", "", WithSubject(strings.Repeat("a", maxSubject+1))); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("a subject is bounded: %v", err)
	}
}

// The guide tells an agent to name a subject, and stays short.
func TestWorkGuideNamesTheSubject(t *testing.T) {
	if g := WorkGuide("parley"); !strings.Contains(g, "`subject`") {
		t.Fatalf("the guide asks for a subject: %q", g)
	}
}
