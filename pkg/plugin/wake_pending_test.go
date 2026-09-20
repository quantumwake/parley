package plugin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A wake file holds posts the identity poller already took off this session's
// cursor and handed to its waiter to print. If that waiter ended first (its
// lifetime ran out, it was replaced or killed), the next wait must print them:
// nothing else will, because the cursor has moved.
func TestNextWaitPrintsAWakeTheLastWaiterNeverRead(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)

	wf := wakeFile(b)
	if err := os.MkdirAll(filepath.Dir(wf), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(wf, []byte("the ask the poller took off b's cursor\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := Wait(ctx, b, nil, 3*WaitPoll, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "the ask the poller took off b's cursor") {
		t.Fatalf("a wake the last waiter never read must be printed, got %q", out.String())
	}

	if _, err := os.Stat(wf); err == nil {
		t.Fatal("a printed wake must be removed so it is not printed twice")
	}
}
