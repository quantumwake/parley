package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/event"
)

// The observer seat's spool, 2026-09-29: 1,863 lines holding 1,614 posts,
// days old, the most repeated post kept nine times in nine separate turns,
// and every prompt replaying a slice of it as new. These tests pin the rules
// that make that impossible: a post is kept once, a kept post expires, and
// the spool has a size.

func keptLine(conv, id string, pos int, text string) string {
	return fmt.Sprintf("- [%s] post.comment someone (%s) @%d: %s", conv, id, pos, text)
}

// withSpoolClock sets the spool's clock and forgets what this process
// drained, as a separate hook process would.
func withSpoolClock(t *testing.T, at time.Time) {
	t.Helper()
	prev := spoolNow
	spoolNow = func() time.Time { return at }
	t.Cleanup(func() { spoolNow = prev })
}

func forgetDrained() {
	keptSince.Lock()
	keptSince.at = map[string]int64{}
	keptSince.Unlock()
}

// rawKept reads the spool file as it is on disk, without taking it.
func rawKept(t *testing.T, env Env) []keptEntry {
	t.Helper()
	b, err := os.ReadFile(contextPath(env))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}

	var out []keptEntry
	for _, line := range bytes.Split(b, []byte("\n")) {
		if e, ok := parseKept(line, spoolNow()); ok {
			out = append(out, e)
		}
	}
	return out
}

// A post kept by one route and kept again by another (a wait, then a Stop,
// then a hook in a third process) is kept once, however it was rendered.
func TestAPostIsKeptOnceWhicheverRouteKeepsItAgain(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	withSpoolClock(t, time.Now())

	id := event.NewID()
	plain := keptLine("issues", id, 7, "the same post")
	marked := fmt.Sprintf("- [issues] [claimed by x] post.comment someone (%s) @7: the same post", id)

	for _, l := range []string{plain, plain, marked} {
		forgetDrained()
		spoolContext(a, []string{l})
	}

	if on := rawKept(t, a); len(on) != 1 {
		t.Fatalf("kept %d times on disk, want once: %+v", len(on), on)
	}

	if got := drainContext(a); len(got) != 1 {
		t.Fatalf("drained %d, want the post once: %q", len(got), got)
	}
}

// A spool that already holds the same post more than once (written before
// this rule) is shown once.
func TestADuplicatedOldSpoolDrainsEachPostOnce(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	now := time.Now()
	withSpoolClock(t, now)

	id := event.NewIDAt(now.Add(-10 * time.Minute))
	line := keptLine("issues", id, 3, "kept nine times")
	writeRaw(t, a, line, line, line, line, line, line, line, line, line)

	if got := drainContext(a); len(got) != 1 {
		t.Fatalf("drained %d, want once: %q", len(got), got)
	}
}

func writeRaw(t *testing.T, env Env, lines ...string) {
	t.Helper()
	path := contextPath(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	var b bytes.Buffer
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(append(j, '\n'))
	}

	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A kept post expires SpoolMaxAge after it was first kept. A Stop that
// drains it and keeps it again does not restart that clock, and neither does
// another process keeping the same post later.
func TestAKeptPostExpiresAndKeepingItAgainDoesNotRestartItsClock(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	t0 := time.Now()

	line := keptLine("issues", event.NewIDAt(t0), 1, "kept at t0")

	withSpoolClock(t, t0)
	spoolContext(a, []string{line})

	// Five hours on, a Stop drains it and, not showing it, keeps it again.
	withSpoolClock(t, t0.Add(5*time.Hour))
	if got := drainContext(a); len(got) != 1 {
		t.Fatalf("still kept at five hours: %q", got)
	}
	spoolContext(a, []string{line})

	// Another process keeps the same post again at five and a half hours.
	withSpoolClock(t, t0.Add(5*time.Hour+30*time.Minute))
	forgetDrained()
	spoolContext(a, []string{line})

	if on := rawKept(t, a); len(on) != 1 || on[0].Kept != t0.UnixMilli() {
		t.Fatalf("kept once with its first time, want k=%d: %+v", t0.UnixMilli(), on)
	}

	withSpoolClock(t, t0.Add(SpoolMaxAge+time.Minute))
	if got := drainContext(a); len(got) != 0 {
		t.Fatalf("past SpoolMaxAge from when it was first kept, it is gone: %q", got)
	}
}

// Past SpoolMaxEntries only the newest stay, and the file itself never
// holds more.
func TestTheSpoolKeepsOnlyTheNewestPastItsCap(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	t0 := time.Now()
	extra := 50

	for i := 0; i < SpoolMaxEntries+extra; i++ {
		withSpoolClock(t, t0.Add(time.Duration(i)*time.Millisecond))
		spoolContext(a, []string{keptLine("issues", event.NewID(), i, fmt.Sprintf("post-%04d", i))})

		if on := len(rawKept(t, a)); on > SpoolMaxEntries {
			t.Fatalf("after %d posts the file holds %d, past the cap", i+1, on)
		}
	}

	got := drainContext(a)
	if len(got) != SpoolMaxEntries {
		t.Fatalf("drained %d, want the cap %d", len(got), SpoolMaxEntries)
	}

	if !strings.Contains(got[0], fmt.Sprintf("post-%04d", extra)) || !strings.Contains(got[len(got)-1], fmt.Sprintf("post-%04d", SpoolMaxEntries+extra-1)) {
		t.Fatalf("the newest stay, in order: first %q, last %q", got[0], got[len(got)-1])
	}
}

// A spool written before entries carried their time is still read. Its
// posts take the post's own time, so days-old ones expire on the first
// drain; a post whose id carries no time (a person's, from the portal) is of
// unknown age and goes too, rather than coming back as new.
func TestAnOldSpoolIsReadAndItsDaysOldPostsExpire(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	now := time.Now()
	withSpoolClock(t, now)

	writeRaw(t, a,
		keptLine("general", event.NewIDAt(now.Add(-72*time.Hour)), 32, "three days old"),
		keptLine("issues", event.NewIDAt(now.Add(-10*time.Minute)), 5, "ten minutes old"),
		keptLine("general", event.NewIDAt(now.Add(-71*time.Hour)), 35, "also days old"),
		keptLine("portal", "a5aa91d9-bde5-4b08-91aa-912892109ad0", 9, "a person's post, no time in its id"),
	)

	got := drainContext(a)
	if len(got) != 1 || !strings.Contains(got[0], "ten minutes old") {
		t.Fatalf("want only the recent post: %q", got)
	}
}

// A person's post in a killed wait's delivery file is never dropped: that
// file is the only copy of a post whose cursor has moved, and the spool's
// expiry rules do not apply to it.
func TestADeliveredPersonsPostIsNeverDroppedForHavingNoTime(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	withSpoolClock(t, time.Now())

	line := keptLine("portal", "7c348168-61c8-4291-a65b-76f9357f1f3f", 1271, "Kasra, typed in the portal")
	holdDelivery(a, []string{line})

	if got := drainDelivery(a); len(got) != 1 || got[0] != line {
		t.Fatalf("the delivery keeps a timeless post: %q", got)
	}
}

// A post kept now, whatever its id, lives its full SpoolMaxAge: the
// unknown-age rule is for old spools only.
func TestAPersonsPostKeptNowIsNotTreatedAsOld(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	t0 := time.Now()
	withSpoolClock(t, t0)

	spoolContext(a, []string{keptLine("portal", "a5aa91d9-bde5-4b08-91aa-912892109ad0", 9, "typed just now")})

	withSpoolClock(t, t0.Add(SpoolMaxAge-time.Minute))
	if got := drainContext(a); len(got) != 1 {
		t.Fatalf("a person's post kept now is still kept near SpoolMaxAge: %q", got)
	}
}

// A post handed over by two routes in one turn (a killed wait's delivery
// and the kept spool) is shown once in that turn.
func TestABatchShowsAPostOnceWhenTwoRoutesHandItOver(t *testing.T) {
	_, b := gateEnv(t)
	withSpoolClock(t, time.Now())

	line := keptLine("issues", event.NewID(), 4, "handed over twice")
	holdDelivery(b, []string{line})
	spoolContext(b, []string{line})

	if got := strings.Count(Inject(context.Background(), b), "handed over twice"); got != 1 {
		t.Fatalf("shown %d times in one turn, want once", got)
	}
}

// The observer's failure, reproduced: posts keep arriving, Stops that do not
// hold the turn keep draining and re-keeping the spool, and a prompt shows a
// batch now and then. No post is shown twice, the spool never passes its cap
// or holds a post twice, and once SpoolMaxAge has passed nothing old comes
// back.
func TestAStopThatNeverHoldsNeitherRepeatsNorGrowsTheSpool(t *testing.T) {
	ctx := context.Background()
	a, b := gateEnv(t)
	t0 := time.Now()

	stop := func() {
		var out bytes.Buffer
		in := strings.NewReader(`{"hook_event_name":"Stop","session_id":"` + b.Session + `"}`)
		if err := Handle(ctx, b, in, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), `"decision":"block"`) {
			t.Fatalf("an agent's comments do not hold the turn: %q", out.String())
		}
	}

	shown := map[int]int{}
	count := func(text string, upTo int) {
		for i := 0; i < upTo; i++ {
			shown[i] += strings.Count(text, fmt.Sprintf("observer-post-%04d ", i))
		}
	}

	const rounds, perRound = 30, 8
	total := 0
	for r := 0; r < rounds; r++ {
		withSpoolClock(t, t0.Add(time.Duration(r)*time.Minute))
		for i := 0; i < perRound; i++ {
			post(t, a, "comment", fmt.Sprintf("observer-post-%04d end", total), "")
			total++
		}

		stop()
		stop()

		if r%3 == 2 {
			count(Inject(ctx, b), total)
		}

		on := rawKept(t, b)
		if len(on) > SpoolMaxEntries {
			t.Fatalf("round %d: the spool holds %d, past the cap", r, len(on))
		}

		keys := map[string]bool{}
		for _, e := range on {
			if keys[e.key()] {
				t.Fatalf("round %d: a post is kept twice on disk: %q", r, e.Line)
			}
			keys[e.key()] = true
		}
	}

	for i := 0; i < total; i++ {
		if shown[i] > 1 {
			t.Fatalf("observer-post-%04d was shown %d times, want at most once", i, shown[i])
		}
	}

	// Long after: nothing kept then comes back as new.
	withSpoolClock(t, t0.Add(SpoolMaxAge+time.Hour))
	if text := Inject(ctx, b); strings.Contains(text, "observer-post-") {
		t.Fatalf("after SpoolMaxAge the old posts replay: %q", firstLine(text, 300))
	}
}

// The overflow note names each conversation once, from its first unshown
// position, even when its lines are interleaved with another's.
func TestTheOverflowNoteListsAConversationOnce(t *testing.T) {
	rest := []string{
		keptLine("issues", event.NewID(), 12, "a"),
		keptLine("chatter", event.NewID(), 40, "b"),
		keptLine("issues", event.NewID(), 10, "c"),
		keptLine("chatter", event.NewID(), 41, "d"),
		keptLine("issues", event.NewID(), 15, "e"),
	}

	note := overflowNote(rest)
	if strings.Count(note, "in issues") != 1 || strings.Count(note, "in chatter") != 1 {
		t.Fatalf("each conversation once: %q", note)
	}

	if !strings.Contains(note, "3 more in issues from @10") || !strings.Contains(note, "2 more in chatter from @40") {
		t.Fatalf("counts and the first unshown position: %q", note)
	}
}
