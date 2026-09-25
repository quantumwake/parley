package plugin

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// The gap the owner named: a session that never passed `--as` sends no
// handle, so everything downstream falls back to the identity — the
// laptop's name on every chip.
func TestASessionWithNoHandleIsTheProblemBeingFixed(t *testing.T) {
	a := followIssues(t)
	if h := Participant(a); h != "" {
		t.Fatalf("a session that chose nothing reports %q", h)
	}
}

// Choosing a handle reaches conversations ALREADY followed: a handle that
// only applied to later joins would leave a seat speaking under two names.
func TestChoosingAHandleReachesConversationsAlreadyFollowed(t *testing.T) {
	ctx := context.Background()
	a := followIssues(t)
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "second", "", nil, &out)
	_ = Join(ctx, a, "second", "full", "all", "", &out)

	updated, err := SetParticipant(a, "champion")
	if err != nil {
		t.Fatal(err)
	}

	if updated != 2 {
		t.Fatalf("updated %d conversations, want both", updated)
	}

	for _, s := range Subscriptions(a) {
		if s.Participant != "champion" {
			t.Fatalf("%s still speaks as %q", s.Name, s.Participant)
		}
	}

	if h := Participant(a); h != "champion" {
		t.Fatalf("the session reports %q", h)
	}
}

// A later join inherits it, so forgetting the flag does not put a seat
// back to being a laptop name.
func TestALaterJoinInheritsTheHandle(t *testing.T) {
	ctx := context.Background()
	a := followIssues(t)
	if _, err := SetParticipant(a, "champion"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	_ = CreateShared(ctx, a, "later", "", nil, &out)
	if err := Join(ctx, a, "later", "full", "all", "", &out); err != nil {
		t.Fatal(err)
	}

	for _, s := range Subscriptions(a) {
		if s.Name == "later" && s.Participant != "champion" {
			t.Fatalf("a later join speaks as %q", s.Participant)
		}
	}
}

// A handle declared for ONE conversation still wins there: `join --as`
// is how a seat speaks under a different name in one place.
func TestAConversationsOwnHandleWinsOverTheSessions(t *testing.T) {
	ctx := context.Background()
	a := followIssues(t)
	if _, err := SetParticipant(a, "champion"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	_ = CreateShared(ctx, a, "elsewhere", "", nil, &out)
	if err := Join(ctx, a, "elsewhere", "full", "all", "observer", &out); err != nil {
		t.Fatal(err)
	}

	var id string
	for _, s := range Subscriptions(a) {
		if s.Name == "elsewhere" {
			id = s.ID
		}
	}

	if got := ParticipantFor(a, id); got != "observer" {
		t.Fatalf("the conversation's own handle lost: %q", got)
	}
}

// What a post carries is the handle, because that is what a reader sees.
// Checked at the delivery rather than through a wait: a comment is talk,
// and talk rides the next turn rather than waking anyone.
func TestAPostCarriesTheHandle(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)

	if _, err := SetParticipant(a, "champion"); err != nil {
		t.Fatal(err)
	}

	// A follow the session did not join itself — a machine follow it
	// inherited at session start — carries no handle of its own. The post
	// must still go out under the session's, or a seat is anonymous in
	// exactly the conversations it did not opt into by hand.
	for _, sub := range Subscriptions(a) {
		sub.Participant = ""
		if err := saveSub(a, sub); err != nil {
			t.Fatal(err)
		}
	}

	if err := Post(ctx, a, "issues", "comment", "under my own name", "", "", nil, &out); err != nil {
		t.Fatal(err)
	}

	text, _ := InjectHold(ctx, b)
	if !strings.Contains(text, "champion") {
		t.Fatalf("the post did not carry the handle: %q", text)
	}
}

// A handle is required and a machine-wide call is refused: this is a
// session's name, not the laptop's.
func TestAHandleNeedsANameAndASession(t *testing.T) {
	a := followIssues(t)
	if _, err := SetParticipant(a, "   "); err == nil {
		t.Fatal("an empty handle was accepted")
	}

	terminal := a
	terminal.Session = ""
	if _, err := SetParticipant(terminal, "champion"); err == nil {
		t.Fatal("a handle was set with no session to speak for")
	}
}
