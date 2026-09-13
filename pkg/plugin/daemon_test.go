package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/capture"
	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/spool"
)

// session is one Claude Code session as the daemon sees it: hook rows in
// the spool and assistant blocks in the transcript.
type session struct {
	t          *testing.T
	env        Env
	id         string
	transcript string
	sp         spool.Session
}

func newSession(t *testing.T) *session {
	tmp := t.TempDir()
	t.Setenv("STATEFS_AI_STORE", "file:"+filepath.Join(tmp, "store"))
	t.Setenv("STATEFS_AI_REDACT", "")
	old := quietWindow
	quietWindow = 500 * time.Millisecond
	t.Cleanup(func() { quietWindow = old })
	env := Env{DataDir: filepath.Join(tmp, "data"), IdentityPath: filepath.Join(tmp, "no-identity"), Thinking: true}
	id := "a8fbd728-20c0-4519-9199-0ae0cfe0837a"
	return &session{t: t, env: env, id: id, transcript: filepath.Join(tmp, id+".jsonl"), sp: spool.Session{Dir: SpoolDir(env), ID: id}}
}

func (s *session) hook(ins ...capture.HookInput) {
	s.t.Helper()
	for _, in := range ins {
		in.SessionID, in.TranscriptPath, in.CWD = s.id, s.transcript, "/repo"
		e, _ := capture.FromHook(in, "anonymous", time.Now())
		if err := s.sp.Append(e, in.HookEventName == "SessionEnd"); err != nil {
			s.t.Fatal(err)
		}
	}
}

func (s *session) say(uuid, text string) {
	s.t.Helper()
	line, _ := json.Marshal(map[string]any{"type": "assistant", "uuid": uuid, "sessionId": s.id, "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"message": map[string]any{"model": "m", "role": "assistant", "content": []map[string]any{{"type": "text", "text": text}}}})
	f, err := os.OpenFile(s.transcript, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		s.t.Fatal(err)
	}

	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

func (s *session) daemon(ctx context.Context) chan error {
	done := make(chan error, 1)
	go func() {
		done <- RunDaemon(ctx, s.env, DaemonOptions{SessionID: s.id, TranscriptPath: s.transcript, CWD: "/repo", IdleTimeout: time.Minute})
	}()
	return done
}

// delivered waits until the spool is fully acked and holds want text.
func (s *session) delivered(ctx context.Context, want string) {
	s.t.Helper()
	for {
		b, _ := os.ReadFile(s.sp.Path())
		if len(b) > 0 && int64(len(b)) == s.sp.AckOffset() && strings.Contains(string(b), want) {
			return
		}

		select {
		case <-ctx.Done():
			s.t.Fatalf("spool never fully delivered with %q", want)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (s *session) rows(ctx context.Context) []event.Event {
	s.t.Helper()
	st, err := StoreFromEnv(s.env)
	if err != nil {
		s.t.Fatal(err)
	}

	names := Names(s.env)
	if len(names) != 1 {
		s.t.Fatalf("want one recorded conversation, got %v", names)
	}

	var rows []event.Event
	for _, id := range names {
		for e, err := range conversation.Attach(st, id).Scan(ctx, 0, 0) {
			if err != nil {
				s.t.Fatal(err)
			}

			rows = append(rows, e)
		}
	}

	return rows
}

func wait(t *testing.T, ctx context.Context, done chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatalf("%s: daemon did not exit", what)
	}
}

// A resumed session keeps its session id and transcript. The daemon of
// the first run must keep recording through the resume instead of exiting
// at the first session.end, and a daemon started later for another resume
// must neither repeat seq nor re-deliver blocks the transcript already had.
func TestDaemonRecordsAResumedSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s := newSession(t)

	s.hook(capture.HookInput{HookEventName: "SessionStart", Source: "startup"}, capture.HookInput{HookEventName: "UserPromptSubmit", Prompt: "first"})
	s.say("u1", "one")
	done := s.daemon(ctx)
	s.delivered(ctx, `"one"`)

	// `claude --resume` right after exit: SessionEnd, then SessionStart
	// {source: resume} and a prompt, while the daemon is finishing run 1.
	s.hook(capture.HookInput{HookEventName: "SessionEnd", Reason: "prompt_input_exit"},
		capture.HookInput{HookEventName: "SessionStart", Source: "resume"}, capture.HookInput{HookEventName: "UserPromptSubmit", Prompt: "second"})
	s.delivered(ctx, `"second"`)
	time.Sleep(3 * quietWindow) // past the first run's hand-off

	// The resumed run goes on well after that.
	s.say("u2", "two")
	s.hook(capture.HookInput{HookEventName: "SessionEnd", Reason: "prompt_input_exit"})
	wait(t, ctx, done, "run 2")
	if daemonRunning(s.env, s.id) {
		t.Fatal("an exited daemon must not hold the session lock")
	}

	// A resume after the daemon exited gets a new daemon, which re-reads
	// the transcript from the start.
	s.hook(capture.HookInput{HookEventName: "SessionStart", Source: "resume"}, capture.HookInput{HookEventName: "UserPromptSubmit", Prompt: "third"})
	s.say("u3", "three")
	done = s.daemon(ctx)
	s.delivered(ctx, `"three"`)
	s.hook(capture.HookInput{HookEventName: "SessionEnd", Reason: "other"})
	wait(t, ctx, done, "run 3")

	rows := s.rows(ctx)
	var got []string
	for i, r := range rows {
		label := string(r.Kind)
		var c struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(r.Content, &c) == nil && c.Text != "" {
			label += ":" + c.Text
		}

		got = append(got, label)
		if r.Seq != int64(i+1) {
			t.Fatalf("row %d (%s) seq %d: seq must count delivered rows across daemons\n%v", i, label, r.Seq, got)
		}
	}

	want := []string{
		"session.start", "user.message:first", "assistant.text:one", "session.end",
		"session.start", "user.message:second", "assistant.text:two", "session.end",
		"session.start", "user.message:third", "assistant.text:three", "session.end",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("conversation:\n got  %v\n want %v", got, want)
	}
}

// A daemon from before the lock is still honored by its live pid file, so
// a plugin update mid-session does not start a second pusher; once the
// session has a lock file, only the lock counts.
func TestEnsureDaemonHonorsAPreLockDaemon(t *testing.T) {
	s := newSession(t)
	s.env.Self = filepath.Join(t.TempDir(), "no-such-parley")
	_ = os.MkdirAll(s.env.DataDir, 0o700)
	in := Input{SessionID: s.id, TranscriptPath: s.transcript, CWD: "/repo"}
	if err := os.WriteFile(filepath.Join(s.env.DataDir, "daemon-"+s.id+".pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ensureDaemon(s.env, in); err != nil {
		t.Fatalf("a live pre-lock daemon must stop a spawn: %v", err)
	}

	lock, err := lockDaemon(s.env, s.id, 0)
	if err != nil {
		t.Fatal(err)
	}

	lock.Close() // the lock file exists, nobody holds it
	if err := ensureDaemon(s.env, in); err == nil {
		t.Fatal("with a free lock the hook must try to start a daemon (the stale pid file is ignored)")
	}
}

// Only one daemon records a session: a second one exits without pushing.
func TestSecondDaemonForASessionExits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := newSession(t)
	lock, err := lockDaemon(s.env, s.id, 0)
	if err != nil {
		t.Fatal(err)
	}

	defer lock.Close()
	if !daemonRunning(s.env, s.id) {
		t.Fatal("a held lock is a running daemon")
	}

	s.hook(capture.HookInput{HookEventName: "SessionStart", Source: "startup"}, capture.HookInput{HookEventName: "UserPromptSubmit", Prompt: "first"})
	wait(t, ctx, s.daemon(ctx), "second daemon")
	if s.sp.AckOffset() != 0 {
		t.Fatal("the second daemon must not push")
	}
}
