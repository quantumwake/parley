package plugin

import (
	"bytes"
	"context"
	"os"
	"testing"
)

// `parley participant --help` once named a seat "--help", and every post
// from it said so (2026-09-25). A flag must never be a handle, whichever
// door it comes through.
func TestAFlagIsNeverAHandle(t *testing.T) {
	a := followIssues(t)
	for _, h := range []string{"--help", "-h", "-x", "--as=me"} {
		if _, err := SetParticipant(a, h); err == nil {
			t.Fatalf("%q was accepted as a handle", h)
		}
	}

	if h := Participant(a); h != "" {
		t.Fatalf("a refused handle was written anyway: %q", h)
	}
}

// The rule is narrow enough that every name a seat has chosen so far
// still passes: a check that refused "grok-parley" would be a regression.
func TestTheHandlesSeatsActuallyChoseStillPass(t *testing.T) {
	for _, h := range []string{"champion", "reviewer", "portal", "grok-parley", "grok-7d", "a", "seat_2", "v0.3", "ABC"} {
		if err := ValidHandle(h); err != nil {
			t.Fatalf("%q was refused: %v", h, err)
		}
	}

	for _, h := range []string{"", " ", "-lead", ".hidden", "_x", "has space", "émile", "a/b", "@me", "thirty-three-characters-long-xxxx"} {
		if ValidHandle(h) == nil {
			t.Fatalf("%q was accepted", h)
		}
	}
}

// A seat named before this check keeps "--help" in its handle file. It
// must read as no handle, so the session-start notice asks it to choose,
// rather than go on speaking as a flag.
func TestAFlagStoredBeforeTheCheckReadsAsNoHandle(t *testing.T) {
	a := followIssues(t)
	if _, err := SetParticipant(a, "champion"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(handleFile(a), []byte("--help\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if h := Participant(a); h != "" {
		t.Fatalf("a stored flag reads back as the handle %q", h)
	}
}

// The handle is also copied into each followed conversation, and that
// copy is what a post carries. A stored flag there must not reach a post.
func TestAFlagStoredOnAConversationDoesNotReachAPost(t *testing.T) {
	a := followIssues(t)
	subs := Subscriptions(a)
	if len(subs) == 0 {
		t.Fatal("no subscription to corrupt; this test would prove nothing")
	}

	s := subs[0]
	s.Participant = "--help"
	if err := saveSub(a, s); err != nil {
		t.Fatal(err)
	}

	for _, got := range Subscriptions(a) {
		if got.Participant == "--help" {
			t.Fatalf("%s still speaks as %q", got.Name, got.Participant)
		}
	}
}

// `join --as` is the third door.
func TestJoinRefusesAFlagForItsHandle(t *testing.T) {
	a := followIssues(t)
	var out bytes.Buffer
	if err := Join(context.Background(), a, "issues", "full", "all", "--help", &out); err == nil {
		t.Fatal("join accepted --help as the handle")
	}
}
