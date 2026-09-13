package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// A Claude Code session receives nothing while it is idle: hooks only run
// around turns. What does wake an idle session is a background shell task
// finishing, so `parley wait` is built to be that task: it blocks until a
// followed conversation has a post from someone else, prints it, and exits.
// The agent handles the posts and starts it again.

// WaitAdvice tells an agent how to be woken. It is printed after join, and
// said at session start when the machine follows anything.
const WaitAdvice = "to be woken when someone posts, run `parley wait` as a background shell task (Bash run_in_background); it exits with the new posts, so run it again after handling them"

// WaitPoll is how often wait checks the conversations.
const WaitPoll = 2 * time.Second

// Wait blocks until at least one followed conversation (all of them, or
// those named) has rows this session has not seen and did not write, then
// prints them and advances this session's cursors. It returns after
// timeout with a line saying nothing arrived; timeout 0 waits indefinitely.
func Wait(ctx context.Context, env Env, names []string, timeout time.Duration, w io.Writer) error {
	subs, err := waitSet(ctx, env, names)
	if err != nil {
		return err
	}

	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	var deadline <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		deadline = timer.C
	}

	for {
		if items := pending(ctx, env, st, subs); len(items) > 0 {
			for _, it := range items {
				fmt.Fprintf(w, "[%s] %s\n", it.sub.Name, formatPost(it.e, it.sub.Name, it.pos-1, 0))
			}

			fmt.Fprintf(w, "%d new posts. Handle them, then run `parley wait` in the background again.\n", len(items))
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			fmt.Fprintf(w, "no new posts in %s. Run `parley wait` in the background again to keep listening.\n", timeout)
			return nil
		case <-time.After(WaitPoll):
		}

		// Re-read cursors each round: this session may have read the
		// conversation by hand while waiting.
		if subs, err = waitSet(ctx, env, names); err != nil {
			return err
		}
	}
}

// waitSet is the subscriptions to wait on.
func waitSet(ctx context.Context, env Env, names []string) ([]Subscription, error) {
	subs := Subscriptions(env)
	if len(subs) == 0 {
		return nil, errors.New("wait: not following any conversation; join one first")
	}

	if len(names) == 0 {
		return subs, nil
	}

	var out []Subscription
	for _, name := range names {
		found := false
		for _, s := range subs {
			if s.Name == name || s.ID == name {
				out = append(out, s)
				found = true
			}
		}

		if !found {
			return nil, fmt.Errorf("wait: not following %q; join it first", name)
		}
	}

	return out, nil
}
