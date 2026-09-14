package plugin

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"
)

var eventIDRe = regexp.MustCompile(`\(event ([0-9A-Z]{26})\)`)

// post posts and answers the new event's id, failing the test on error.
func post(t *testing.T, env Env, kind, text, replyTo string, opts ...PostOption) string {
	t.Helper()
	var out bytes.Buffer
	if err := Post(context.Background(), env, "issues", kind, text, "*", replyTo, nil, &out, opts...); err != nil {
		t.Fatalf("post %s: %v", kind, err)
	}

	m := eventIDRe.FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no event id in %q", out.String())
	}

	return m[1]
}

func postErr(env Env, kind, text, replyTo string, opts ...PostOption) error {
	return Post(context.Background(), env, "issues", kind, text, "*", replyTo, nil, &bytes.Buffer{}, opts...)
}

func workSessions(t *testing.T) (a, b, c Env) {
	t.Helper()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222", "cccccccc-3333")
	a, b, c = s["aaaaaaaa-1111"], s["bbbbbbbb-2222"], s["cccccccc-3333"]
	follow(t, a, "issues")
	for _, e := range []Env{b, c} {
		if err := Join(context.Background(), e, "issues", "full", "all", "", &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}

	return a, b, c
}

// A request is claimed once; a second claim is refused; a handover opens it
// again for someone else; a resolved close ends it.
func TestWorkRequestClaimHandoverResolve(t *testing.T) {
	a, b, c := workSessions(t)

	req := post(t, a, "request", "fix the load-older loop in the console", "")
	claimB := post(t, b, "claim", "on it: parley, branch fix/load-older-loop", req)

	if err := postErr(c, "claim", "me too", req); err == nil || !strings.Contains(err.Error(), "already claimed by") {
		t.Fatalf("a second claim is refused: %v", err)
	}

	if err := postErr(b, "close", "done", claimB); err == nil || !strings.Contains(err.Error(), "needs an outcome") {
		t.Fatalf("a close needs an outcome: %v", err)
	}

	post(t, b, "close", "handing over, out of time", claimB, WithOutcome(OutcomeHandedOver))
	claimC := post(t, c, "claim", "taking it over", req)

	var out bytes.Buffer
	if err := ListWork(context.Background(), c, nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "claimed by") || !strings.Contains(out.String(), "(mine)") {
		t.Fatalf("parley work shows the new holder as mine: %q", out.String())
	}

	if err := postErr(b, "close", "stale", claimB, WithOutcome(OutcomeResolved)); err == nil {
		t.Fatal("a claim that no longer holds the request cannot close it")
	}

	post(t, c, "close", "fixed in #14", claimC, WithOutcome(OutcomeResolved))

	out.Reset()
	_ = ListWork(context.Background(), a, nil, false, &out)
	if !strings.Contains(out.String(), "no open or claimed work") {
		t.Fatalf("resolved work is not listed: %q", out.String())
	}

	out.Reset()
	_ = ListWork(context.Background(), a, nil, true, &out)
	if !strings.Contains(out.String(), "closed, resolved") {
		t.Fatalf("--all shows it closed: %q", out.String())
	}
}

// Only a request is claimable: a question gets an answer, and work nobody
// asked for is a claim with no reply.
func TestOnlyRequestsAreClaimable(t *testing.T) {
	a, b, _ := workSessions(t)

	q := post(t, a, "question", "why does the console loop on older rows?", "")
	if err := postErr(b, "claim", "investigating", q); err == nil || !strings.Contains(err.Error(), "only a request can be claimed") {
		t.Fatalf("claiming a question is refused with guidance: %v", err)
	}

	own := post(t, b, "claim", "unprompted: Event.tags unmarshal error, parley fix/tolerant-tags", "")
	if err := postErr(a, "claim", "mine", own); err == nil || !strings.Contains(err.Error(), "that is a claim") {
		t.Fatalf("an unprompted claim cannot be claimed: %v", err)
	}

	var out bytes.Buffer
	_ = ListWork(context.Background(), a, nil, false, &out)
	if !strings.Contains(out.String(), "claim") || !strings.Contains(out.String(), "Event.tags") {
		t.Fatalf("unprompted work is listed as claimed: %q", out.String())
	}

	post(t, b, "close", "shipped", own, WithOutcome(OutcomeResolved))
}

// Delivery shows where a work post's item stands now.
func TestWorkPostsAreDeliveredWithTheirState(t *testing.T) {
	a, b, _ := workSessions(t)
	req := post(t, a, "request", "review PR 12", "")
	post(t, b, "claim", "reviewing", req)

	got := Inject(context.Background(), a)
	if !strings.Contains(got, "[work: claimed by") {
		t.Fatalf("the claim is delivered with its state: %q", got)
	}

	if !strings.Contains(WorkGuide, "answering a question needs no claim") {
		t.Fatal("the guide says a question needs no claim")
	}
}
