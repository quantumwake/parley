package plugin

import (
	"testing"

	"github.com/quantumwake/parley/pkg/event"
)

// A person to one seat wakes that seat and nobody else. @everyone wakes
// every seat. An agent post that names two seats wakes both, and an old
// single-to row still wakes its one recipient.
func TestRecipientsWakeOnlyWhoTheyName(t *testing.T) {
	personToA := event.Event{Kind: event.KindPostComment, To: "champion", Participant: "Kasra Rasaee"}
	if holdsTurn(personToA, false, nil) {
		t.Fatal("a person's post to one seat woke another")
	}

	if !holdsTurn(personToA, true, nil) {
		t.Fatal("the named seat did not wake")
	}

	both := event.Event{Kind: event.KindPostComment, SessionID: "agent-1", To: "A", CC: []string{"B"}}
	if !addressesAny(both, "B", "B", "sess-b") {
		t.Fatal("the second mention did not address B")
	}

	if addressesAny(both, "C", "C", "sess-c") {
		t.Fatal("a seat that was not named was addressed")
	}

	if !holdsTurn(both, true, nil) || holdsTurn(both, false, nil) {
		t.Fatal("an agent post to A and B did not wake only those seats")
	}

	every := event.Event{Kind: event.KindPostComment, Participant: "Kasra Rasaee", To: "everyone"}
	if !holdsTurn(every, false, nil) {
		t.Fatal("@everyone did not wake a seat")
	}

	old := event.Event{Kind: event.KindPostQuestion, To: "grok"}
	if !holdsTurn(old, true, nil) || holdsTurn(old, false, nil) {
		t.Fatal("an old single-to row did not wake only its recipient")
	}

	bare := event.Event{Kind: event.KindPostComment, Participant: "Kasra Rasaee"}
	if holdsTurn(bare, false, nil) {
		t.Fatal("a person's comment with no @ woke a seat")
	}

	ask := event.Event{Kind: event.KindPostQuestion, Participant: "Kasra Rasaee"}
	if !holdsTurn(ask, false, nil) {
		t.Fatal("an unaddressed question did not wake")
	}

	to, cc := SplitRecipients([]string{"A"}, "see @B about it")
	if to != "A" || len(cc) != 1 || cc[0] != "B" {
		t.Fatalf("text mention was not stored: %q %v", to, cc)
	}
}
