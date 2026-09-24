package plugin

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// closedPipe is stdout after the shell that owned it has gone: every write
// fails. The posts must not be counted as delivered.
type closedPipe struct{}

func (closedPipe) Write([]byte) (int, error) { return 0, errors.New("write: broken pipe") }

func TestDevNullIsNotSomewhereToDeliver(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()

	// Whoever the parent is: a sink is a sink.
	for _, ppid := range []int{1, 4242} {
		if err := canDeliverTo(null, ppid); !errors.Is(err, ErrWaitDiscarded) {
			t.Fatalf("ppid %d: canDeliverTo(/dev/null) = %v, want ErrWaitDiscarded", ppid, err)
		}
	}
}

func TestAnOrphanedPipeRetiresButAFileDoesNot(t *testing.T) {
	r, wpipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer wpipe.Close()

	// The shell is gone (reparented to init) and the pipe has no reader.
	if err := canDeliverTo(wpipe, 1); !errors.Is(err, ErrWaitOrphaned) {
		t.Fatalf("orphaned pipe = %v, want ErrWaitOrphaned", err)
	}

	// The same pipe under a live shell is the normal background task.
	if err := canDeliverTo(wpipe, 4242); err != nil {
		t.Fatalf("a background task under a live shell = %v, want nil", err)
	}

	// `nohup parley wait > log &`: the shell went, but the output is still
	// there to be read, so it is left alone.
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := canDeliverTo(f, 1); err != nil {
		t.Fatalf("an orphan writing to a file = %v, want nil", err)
	}
}

// The whole point: a wait that cannot be heard must not consume. It refuses
// before it claims anything, and the posts are still there for the next one.
func TestAWaitThatCannotBeHeardRefusesAndConsumesNothing(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)
	withWaitStore(t, a)

	if err := Post(ctx, b, "issues", "question", "does anyone read this", "", "", nil, &out); err != nil {
		t.Fatal(err)
	}

	prev := waitCanDeliver
	waitCanDeliver = func() error { return ErrWaitDiscarded }
	out.Reset()
	err := Wait(ctx, a, nil, time.Minute, &out)
	waitCanDeliver = prev
	if !errors.Is(err, ErrWaitDiscarded) {
		t.Fatalf("a wait pointed at a sink returned %v, want ErrWaitDiscarded", err)
	}

	if out.Len() != 0 {
		t.Fatalf("it printed %q", out.String())
	}

	// It refused before taking anything on: no armed listener is recorded,
	// so `parley status` and the session-start notice do not claim one.
	if _, err := os.Stat(waitFile(a)); !os.IsNotExist(err) {
		t.Fatalf("a refused wait recorded itself as armed: %v", err)
	}

	// The post is untouched, so the next wait delivers it.
	out.Reset()
	if err := Wait(ctx, a, nil, time.Minute, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "does anyone read this") {
		t.Fatalf("the refused wait consumed the post: %q", out.String())
	}
}

// A wait whose writing fails has already moved the cursors past the rows,
// so the print was the only copy. It keeps it, and the session's next turn
// carries it.
func TestPostsAWaitCouldNotPrintSurviveToTheNextTurn(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)
	withWaitStore(t, a)

	if err := Post(ctx, b, "issues", "question", "the post nobody heard", "", "", nil, &out); err != nil {
		t.Fatal(err)
	}

	if err := Wait(ctx, a, nil, time.Minute, closedPipe{}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(deliveringPath(a)); err != nil {
		t.Fatalf("the undelivered posts were not kept: %v", err)
	}

	text, _ := InjectHold(ctx, a)
	if !strings.Contains(text, "the post nobody heard") {
		t.Fatalf("the next turn did not carry the post: %q", text)
	}

	// Shown once: the second turn is quiet again.
	if text, _ := InjectHold(ctx, a); strings.Contains(text, "the post nobody heard") {
		t.Fatalf("the post was shown twice: %q", text)
	}
}

// The ordinary path must not pay for it: a delivery that landed is not
// repeated on the next turn.
func TestADeliveryThatLandedIsNotShownAgain(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)
	withWaitStore(t, a)

	if err := Post(ctx, b, "issues", "question", "heard you", "", "", nil, &out); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := Wait(ctx, a, nil, time.Minute, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "heard you") {
		t.Fatalf("the wait did not deliver: %q", out.String())
	}

	if _, err := os.Stat(deliveringPath(a)); !os.IsNotExist(err) {
		t.Fatalf("a delivery that landed was still held: %v", err)
	}

	if text, _ := InjectHold(ctx, a); strings.Contains(text, "heard you") {
		t.Fatalf("the next turn repeated a delivered post: %q", text)
	}
}

// A wait is orphaned after the fact, not at birth: the shell exits while it
// is polling. It must notice on the next round rather than poll, ping and
// hold the poller lock for a session that has gone deaf.
func TestAWaitOrphanedMidFlightRetires(t *testing.T) {
	ctx := context.Background()
	a := followIssues(t)
	withWaitStore(t, a)

	rounds := 0
	prev := waitCanDeliver
	waitCanDeliver = func() error {
		rounds++
		if rounds > 2 {
			return ErrWaitOrphaned
		}

		return nil
	}
	defer func() { waitCanDeliver = prev }()

	var out bytes.Buffer
	err := Wait(ctx, a, nil, time.Minute, &out)
	if !errors.Is(err, ErrWaitOrphaned) {
		t.Fatalf("an orphaned wait returned %v, want ErrWaitOrphaned", err)
	}
}
