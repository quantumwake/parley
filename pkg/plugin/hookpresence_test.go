package plugin

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain keeps every test in this package from starting a real detached
// ping: an Env built by EnvFromProcess names the test binary as Self, and
// spawning it would run the suite again in the background.
func TestMain(m *testing.M) {
	presenceSpawn = func(Env, string, string) error { return nil }
	os.Exit(m.Run())
}

// spyPresence records the states hooks spawn a ping for, and never spawns.
func spyPresence(t *testing.T) *[]string {
	t.Helper()
	var sent []string
	prev := presenceSpawn
	presenceSpawn = func(_ Env, state, _ string) error {
		sent = append(sent, state)
		return nil
	}
	t.Cleanup(func() { presenceSpawn = prev })
	return &sent
}

func TestEachHookReportsItsState(t *testing.T) {
	for _, c := range []struct {
		event   string
		blocked bool
		want    string
	}{
		{"SessionStart", false, "starting"},
		{"UserPromptSubmit", false, "thinking"},
		{"PreToolUse", false, "working"},
		{"PostToolUse", false, "working"},
		{"Stop", false, "listening"},
		{"Stop", true, "thinking"}, // a blocked Stop keeps the agent working
		{"SessionEnd", false, ""},
	} {
		if got := hookStateFor(c.event, c.blocked); got != c.want {
			t.Errorf("%s blocked=%v: %q, want %q", c.event, c.blocked, got, c.want)
		}
	}
}

func TestAHookSendsAChangeAtOnceAndARepeatOnlyAfterResend(t *testing.T) {
	_, b := gateEnv(t)
	sent := spyPresence(t)
	now := time.UnixMilli(1_700_000_000_000)

	hookPresence(b, "thinking", "", now)
	hookPresence(b, "working", "", now.Add(time.Second))            // a change goes out at once
	hookPresence(b, "working", "", now.Add(2*time.Second))          // a repeat inside hookResend does not
	hookPresence(b, "working", "", now.Add(time.Second+hookResend)) // past it, it is sent again
	if got := strings.Join(*sent, ","); got != "thinking,working,working" {
		t.Fatalf("sent %q", got)
	}
}

func TestAFailedPingBacksTheHooksOff(t *testing.T) {
	_, b := gateEnv(t)
	sent := spyPresence(t)
	prev := presenceSend
	presenceSend = func(context.Context, Env, string) error { return errors.New("statefs.ai down") }
	t.Cleanup(func() { presenceSend = prev })

	if err := PresencePing(context.Background(), b, "working"); err == nil {
		t.Fatal("the ping reports its failure")
	}
	hookPresence(b, "thinking", "", time.Now())
	if len(*sent) != 0 {
		t.Fatalf("hooks send nothing while backing off: %v", *sent)
	}
	if st := readPresence(b).State; st != "thinking" {
		t.Fatalf("but the state is still recorded for wait: %q", st)
	}
}

func TestAWaitCarriesAFreshBusyStateAndThenListens(t *testing.T) {
	_, b := gateEnv(t)
	spyPresence(t)
	now := time.UnixMilli(1_700_000_000_000)
	if got := waitPresenceState(b, now); got != "listening" {
		t.Fatalf("no hook yet: %q", got)
	}
	hookPresence(b, "working", "", now)
	if got := waitPresenceState(b, now.Add(30*time.Second)); got != "working" {
		t.Fatalf("a fresh busy state is carried, not overwritten with listening: %q", got)
	}
	if got := waitPresenceState(b, now.Add(hookStateFresh+time.Second)); got != "listening" {
		t.Fatalf("a stale one is not: %q", got)
	}
	hookPresence(b, "listening", "", now.Add(time.Minute))
	if got := waitPresenceState(b, now.Add(time.Minute+time.Second)); got != "listening" {
		t.Fatalf("Stop's listening is listening: %q", got)
	}
}

// The hook must not wait on the network: a ping that never returns does not
// delay it, because the ping runs in a detached process, not in the hook.
func TestTheStopHookDoesNotWaitOnPresence(t *testing.T) {
	_, b := gateEnv(t)
	sent := spyPresence(t)
	prev := presenceSend
	presenceSend = func(ctx context.Context, _ Env, _ string) error { <-ctx.Done(); return ctx.Err() } // a service that never answers
	t.Cleanup(func() { presenceSend = prev })

	start := time.Now()
	var out bytes.Buffer
	in := strings.NewReader(`{"hook_event_name":"Stop","session_id":"` + b.Session + `"}`)
	if err := Handle(context.Background(), b, in, &out); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the Stop hook took %s", d)
	}
	if strings.Join(*sent, ",") != "listening" {
		t.Fatalf("Stop reports listening: %v", *sent)
	}
}

// No session, no presence: a plain terminal's hook reports nothing.
func TestNoSessionNoPresence(t *testing.T) {
	_, b := gateEnv(t)
	sent := spyPresence(t)
	b.Session = ""
	hookPresence(b, "working", "", time.Now())
	if len(*sent) != 0 {
		t.Fatalf("sent %v", *sent)
	}
}

// A hook is a short lived process; the session that spawned it is its
// parent. Recording that parent is what later lets a reader tell an open
// seat from one whose terminal was closed, so the hook must write it.
func TestAHookRecordsTheSessionItBelongsTo(t *testing.T) {
	_, b := gateEnv(t)
	spyPresence(t)

	prev := hookOwnerPID
	hookOwnerPID = func() int { return 4242 }
	t.Cleanup(func() { hookOwnerPID = prev })

	hookPresence(b, "working", "", time.Now())

	if got := readPresence(b).OwnerPID; got != 4242 {
		t.Fatalf("presence records owner pid %d, want the session's 4242", got)
	}
}

// The detached ping and the backoff writer are not children of the
// session, so neither may stamp its own parent over the owner. Both read
// the file before writing it; this pins that they keep the field.
func TestTheBackoffWriterKeepsTheRecordedOwner(t *testing.T) {
	_, b := gateEnv(t)
	spyPresence(t)

	prev := hookOwnerPID
	hookOwnerPID = func() int { return 4242 }
	t.Cleanup(func() { hookOwnerPID = prev })

	prevSend := presenceSend
	presenceSend = func(context.Context, Env, string) error { return errors.New("statefs.ai down") }
	t.Cleanup(func() { presenceSend = prevSend })

	hookPresence(b, "working", "", time.Now())
	_ = PresencePing(context.Background(), b, "working")

	if readPresence(b).DownUntilMs == 0 {
		t.Fatal("the failed ping did not write a backoff, so this proves nothing")
	}

	if got := readPresence(b).OwnerPID; got != 4242 {
		t.Fatalf("the owner pid became %d after a backoff write, want 4242", got)
	}
}
