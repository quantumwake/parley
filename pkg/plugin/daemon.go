package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/quantumwake/statefs.ai/pkg/capture"
	"github.com/quantumwake/statefs.ai/pkg/spool"
)

// DaemonOptions describe one session's capture daemon.
type DaemonOptions struct {
	SessionID      string
	TranscriptPath string
	CWD            string
	IdleTimeout    time.Duration // exit after this long with no session.end (default 4 h)
}

// RunDaemon tails the transcript into the spool and pushes the spool to
// the store until session.end is delivered (or the idle timeout). It is
// the long-lived half of capture; hooks are the short-lived half.
func RunDaemon(ctx context.Context, env Env, o DaemonOptions) error {
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 4 * time.Hour
	}

	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	author := authorOf(env)
	if author == "" {
		author = "anonymous"
	}

	sp := spool.Session{Dir: SpoolDir(env), ID: o.SessionID}
	ctx, cancel := context.WithTimeout(ctx, o.IdleTimeout)
	defer cancel()

	tailer := &capture.Tailer{Path: o.TranscriptPath, Author: author, CaptureThinking: env.Thinking,
		Emit: func(e capture.Event) error { return sp.Append(e, false) }}
	tailCtx, stopTail := context.WithCancel(ctx)
	tailDone := make(chan error, 1)
	go func() { tailDone <- tailer.Run(tailCtx) }()

	pusher := &capture.Pusher{
		Store: st, Session: sp, Agent: author, Name: nameFrom(o.CWD, o.SessionID),
		Redact:    redactFromEnv(),
		BeforeEnd: func(ctx context.Context) { waitQuiet(ctx, o.TranscriptPath, 1500*time.Millisecond, 10*time.Second) },
		OnDelivered: func(seq int64, e capture.Event, pos capture.Position) {
			fmt.Printf("%s delivered seq=%d kind=%s pos=%d\n", time.Now().UTC().Format(time.RFC3339), seq, e.Kind, pos)
		},
	}
	pusher.OnOpen = func(displayName, id string) { NamesPut(env, displayName, id) }
	err = pusher.Run(ctx) // returns when session.end is delivered
	stopTail()
	<-tailDone
	if err != nil {
		return err
	}

	// The tailer may have appended final blocks after the push saw
	// session.end; deliver anything still spooled.
	return pusher.Once(context.Background())
}

// waitQuiet returns once path has not grown for quiet, or after max.
func waitQuiet(ctx context.Context, path string, quiet, max time.Duration) {
	deadline := time.Now().Add(max)
	last, _ := fileSize(path)
	stable := time.Now()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}

		n, _ := fileSize(path)
		if n != last {
			last, stable = n, time.Now()
			continue
		}

		if time.Since(stable) >= quiet {
			return
		}
	}
}

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}

	return st.Size(), nil
}

func nameFrom(cwd, session string) string {
	if cwd != "" {
		return filepath.Base(cwd)
	}

	return session
}

// redactFromEnv reads STATEFS_AI_REDACT: a "|"-separated list of regexes.
func redactFromEnv() []*regexp.Regexp {
	v := os.Getenv("STATEFS_AI_REDACT")
	if v == "" {
		return nil
	}

	var out []*regexp.Regexp
	for _, p := range strings.Split(v, "|") {
		if re, err := regexp.Compile(p); err == nil {
			out = append(out, re)
		}
	}

	return out
}
