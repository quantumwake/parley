package plugin

import "testing"

import "github.com/quantumwake/parley/pkg/event"

// A portal post has no session and the person's name in Participant.
// Cloud @287 and @289 were that shape, addressed to one seat, and the
// other seats did not wake. An agent post addressed to someone else still
// must not.
func TestAPersonPostWakesEvenWhenThePortalSetsTheirName(t *testing.T) {
	person := event.Event{Kind: event.KindPostComment, To: "grok-cloud", Participant: "Kasra Rasaee"}
	if !holdsTurn(person, false, nil) {
		t.Fatal("a person post addressed to one seat did not wake the others")
	}

	agent := person
	agent.SessionID = "01a0c903-62ee-7661-bb29-d0f8bf7f00f9"
	agent.Participant = "grok-cloud"
	if holdsTurn(agent, false, nil) {
		t.Fatal("an agent post addressed to someone else woke this seat")
	}
}
