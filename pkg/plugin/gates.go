package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Delivery is a chain of gates, cheapest first, and every post leaves it
// with one verdict:
//
//	react    wake the agent: the wait exits with it, the Stop hook holds
//	         the turn for it
//	context  put it in front of the agent on its next prompt, but never
//	         interrupt for it
//	display  show it to the PERSON (the status line's counts, one line at
//	         most), and keep it out of the model's context
//	ignore   count it, show it to nobody
//
// The first gates are deterministic and free, and are the ones that decide
// almost everything: a post addressed to another agent, a question already
// claimed or answered, talk rather than an ask (see holdsTurn). Only what
// still says react reaches the configured gates, which are commands — one
// per intent, run in order, none by default.
//
// A gate may only LOWER a verdict, never raise one. A broken, slow or
// hostile gate can then quiet a post but can never manufacture an
// interruption, and its answer can never overrule the deterministic gates
// above it. A gate that fails, times out or answers nonsense leaves the
// verdict where it was: a broken chain makes parley noisy, not deaf.
//
// Gates run on the wait (background listener) path only, never while the
// person's own prompt is being handled: a model call must not sit between
// enter and their turn.
//
// Every verdict is recorded under <data>/verdicts/, so a wrong "ignore" can
// be found afterwards. Nothing else in parley reads those files back; they
// are for people, and for the status line's counts.

// Verdict is what a gate chain decides about one post.
type Verdict string

const (
	VerdictReact   Verdict = "react"
	VerdictContext Verdict = "context"
	VerdictDisplay Verdict = "display"
	VerdictIgnore  Verdict = "ignore"
)

// verdictRank orders the verdicts from loudest to quietest. A gate may only
// move a post down this list.
func verdictRank(v Verdict) int {
	switch v {
	case VerdictReact:
		return 3
	case VerdictContext:
		return 2
	case VerdictDisplay:
		return 1
	case VerdictIgnore:
		return 0
	}

	return -1 // not a verdict
}

// Gate is one configured stage: a command parley runs for a post that has
// survived the deterministic gates. Several gates may be configured, one
// per intent; they run in order and stop at the quietest verdict.
type Gate struct {
	Name      string `json:"name"`
	Command   string `json:"command"`              // run with `sh -c`, the post on stdin
	TimeoutMs int    `json:"timeout_ms,omitempty"` // per post; DefaultGateTimeout when 0
}

// DefaultGateTimeout bounds one gate call for one post. A gate is a fast
// model or a script; a slow one must not hold up the listener.
const DefaultGateTimeout = 10 * time.Second

// GateInput is the JSON one gate reads on stdin. It is what the gate needs
// to judge the post and nothing else: no keys, no cursors, no other posts.
type GateInput struct {
	Conversation   string `json:"conversation"`
	ConversationID string `json:"conversation_id,omitempty"`
	Position       int64  `json:"position"`
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Author         string `json:"author"`
	To             string `json:"to,omitempty"`
	ReplyTo        string `json:"reply_to,omitempty"`
	Text           string `json:"text"`
	Verdict        string `json:"verdict"`   // where the chain stands now
	Me             string `json:"me"`        // this identity
	Session        string `json:"session"`   // this session
	Addressed      bool   `json:"addressed"` // the post names this reader
}

// GateOutput is what a gate writes on stdout.
type GateOutput struct {
	Verdict string `json:"verdict"`
	Why     string `json:"why,omitempty"`
}

// GateMaxInputBytes caps the post text handed to a gate: a judgement about
// whether to react does not need a whole design document, and a gate that
// is a model is paid for by the token.
const GateMaxInputBytes = 4 << 10

// applyGates answers each post's verdict, running the configured chain
// over the ones that would otherwise wake the agent. With no gates
// configured (the default) the deterministic verdict stands and no process
// is started — but every verdict is still recorded, so the counts a person
// sees are the whole story and not only the gated part.
func applyGates(ctx context.Context, env Env, me string, items []pendingPost) []Verdict {
	out := make([]Verdict, len(items))
	for i, it := range items {
		out[i] = VerdictContext
		if it.hold {
			out[i] = VerdictReact
		}
	}

	for i, it := range items {
		gate, why := "", ""
		for _, g := range env.Gates {
			if out[i] != VerdictReact {
				break // only what would still interrupt is worth a gate
			}

			v, reason, err := runGate(ctx, g, gateInputOf(env, me, it, out[i]))
			switch {
			case err != nil:
				gate, why = g.Name, "gate failed, verdict unchanged: "+err.Error()
			case verdictRank(v) < 0 || verdictRank(v) >= verdictRank(out[i]):
				gate, why = g.Name, reason // a gate may only lower
			default:
				out[i], gate, why = v, g.Name, reason
			}

			if err != nil {
				break // fail open: a broken chain makes parley noisy, not deaf
			}
		}

		// One record per post, whatever decided it: the free rules alone
		// (the default install) are as worth counting as a gate's answer.
		recordVerdict(env, it, out[i], gate, why)
	}

	return out
}

func gateInputOf(env Env, me string, it pendingPost, v Verdict) GateInput {
	text, _ := postText(it.e)
	if len(text) > GateMaxInputBytes {
		text = text[:GateMaxInputBytes]
	}

	return GateInput{
		Conversation: it.sub.Name,
		Position:     it.pos - 1,
		ID:           it.e.ID,
		Kind:         string(it.e.Kind),
		Author:       speakerOf(it.e),
		To:           it.e.To,
		ReplyTo:      it.e.ReplyTo,
		Text:         text,
		Verdict:      string(v),
		Me:           me,
		Session:      env.Session,
		Addressed:    it.mine,
	}
}

// runGate runs one gate for one post: the input on stdin, one JSON object
// on stdout. Anything else — a non-zero exit, a timeout, unparsable output
// — is an error, and an error never changes a verdict.
func runGate(ctx context.Context, g Gate, in GateInput) (Verdict, string, error) {
	if strings.TrimSpace(g.Command) == "" {
		return "", "", fmt.Errorf("gate %q has no command", g.Name)
	}

	timeout := DefaultGateTimeout
	if g.TimeoutMs > 0 {
		timeout = time.Duration(g.TimeoutMs) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(in)
	if err != nil {
		return "", "", err
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", g.Command)
	// A gate that backgrounds a helper leaves that grandchild holding the
	// output pipes, and cmd.Wait would block on them long after the
	// context killed the shell. WaitDelay gives up on the pipes shortly
	// after the kill, so the timeout is the timeout.
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(firstLine(stderr.String(), 200)); msg != "" {
			return "", "", fmt.Errorf("%s: %s", err, msg)
		}

		return "", "", err
	}

	var res GateOutput
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		return "", "", fmt.Errorf("gate %q did not answer JSON {\"verdict\":...}: %w", g.Name, err)
	}

	return Verdict(res.Verdict), res.Why, nil
}

// VerdictRow is one line of the record. Rows are appended, never rewritten.
// Exported so the console can read a conversation's recent decisions back
// (RecentVerdicts); the JSON field names are the on-disk format and do not
// change with the Go name.
type VerdictRow struct {
	AtMs           int64  `json:"at_ms"`
	Text           string `json:"text,omitempty"` // one line of the post, for the person's counts
	Conversation   string `json:"conversation"`
	ConversationID string `json:"conversation_id,omitempty"`
	Position       int64  `json:"position"`
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Author         string `json:"author"`
	Verdict        string `json:"verdict"`
	Gate           string `json:"gate,omitempty"`
	Why            string `json:"why,omitempty"`
}

// verdictPath files a conversation's record under its namespace id, not
// its name: a name is whatever was typed at `join` (case, or the id
// itself), and one machine's data directory is shared by every identity
// and tenant, so two conversations called "issues" in two tenants would
// otherwise share a file. The name is carried in the rows for display.
func verdictPath(env Env, id string) string {
	return filepath.Join(env.DataDir, "verdicts", filepath.Base(id)+".jsonl")
}

// recordVerdict appends one decision. A record that cannot be written is
// not worth failing delivery over, but it is the only way a wrong "ignore"
// is ever found, so it is written before the verdict takes effect.
func recordVerdict(env Env, it pendingPost, v Verdict, gate, why string) {
	if env.DataDir == "" {
		return
	}

	text, _ := postText(it.e)
	row := VerdictRow{
		AtMs: time.Now().UnixMilli(), Text: firstLine(text, 140), Conversation: it.sub.Name, ConversationID: it.sub.ID, Position: it.pos - 1,
		ID: it.e.ID, Kind: string(it.e.Kind), Author: speakerOf(it.e),
		Verdict: string(v), Gate: gate, Why: firstLine(why, 200),
	}

	b, err := json.Marshal(row)
	if err != nil {
		return
	}

	path := verdictPath(env, it.sub.ID)
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = f.Write(append(b, '\n'))
}

// VerdictCount is how one conversation's posts were judged, for the status
// line and the console.
type VerdictCount struct {
	Conversation   string `json:"conversation"`
	ConversationID string `json:"conversation_id,omitempty"`
	React          int    `json:"react"`
	Context        int    `json:"context"`
	Display        int    `json:"display"`
	Ignore         int    `json:"ignore"`
	LastText       string `json:"last_text,omitempty"` // one line of the newest display post
}

// VerdictCounts reads the record back, counting the last window of
// decisions per conversation. It reads files this machine wrote; a record
// that is missing or unreadable counts as nothing, never as an error.
func VerdictCounts(env Env, since time.Duration) []VerdictCount {
	if env.DataDir == "" {
		return nil
	}

	entries, err := os.ReadDir(filepath.Join(env.DataDir, "verdicts"))
	if err != nil {
		return nil
	}

	cutoff := time.Now().Add(-since).UnixMilli()
	byName := map[string]*VerdictCount{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(env.DataDir, "verdicts", e.Name()))
		if err != nil {
			continue
		}

		// Every session of this machine records its own verdict for the
		// same post, and they differ (a post addressed to one session is
		// react there and context elsewhere). Count each post once, by its
		// newest decision, or a two-session machine doubles every count.
		seen := map[string]bool{}
		lines := strings.Split(string(b), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			var row VerdictRow
			if lines[i] == "" || json.Unmarshal([]byte(lines[i]), &row) != nil || row.AtMs < cutoff || seen[row.ID] {
				continue
			}

			seen[row.ID] = true
			c := byName[row.Conversation]
			if c == nil {
				c = &VerdictCount{Conversation: row.Conversation, ConversationID: row.ConversationID}
				byName[row.Conversation] = c
			}

			switch Verdict(row.Verdict) {
			case VerdictReact:
				c.React++
			case VerdictContext:
				c.Context++
			case VerdictDisplay:
				c.Display++
				c.LastText = row.Author + ": " + row.Text
			case VerdictIgnore:
				c.Ignore++
			}
		}
	}

	out := make([]VerdictCount, 0, len(byName))
	for _, c := range byName {
		out = append(out, *c)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Conversation < out[j].Conversation })
	return out
}

// RecentVerdicts reads back one conversation's verdict record, newest
// first, capped at limit: the posts behind the status line's (and the
// console's) counts. It reads this machine's own file; a record that is
// missing or unreadable answers no rows, never an error — the counts stay
// the only promise, this is the detail behind them.
func RecentVerdicts(env Env, id string, limit int) []VerdictRow {
	if env.DataDir == "" || limit <= 0 {
		return nil
	}

	b, err := os.ReadFile(verdictPath(env, id))
	if err != nil {
		return nil
	}

	// Newest first, one row per post: each session of this machine wrote
	// its own decision for the same post, and the console would otherwise
	// chip a post with the oldest of them and list it twice in the digest.
	lines := strings.Split(string(b), "\n")
	out := make([]VerdictRow, 0, limit)
	seen := map[string]bool{}
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		var row VerdictRow
		if lines[i] == "" || json.Unmarshal([]byte(lines[i]), &row) != nil || seen[row.ID] {
			continue
		}

		seen[row.ID] = true
		out = append(out, row)
	}

	return out
}

// StatusLineWindow is how far back the status line counts.
const StatusLineWindow = 12 * time.Hour

// StatusLine is one line for Claude Code's settings.json statusLine: each
// followed conversation, its unread count, and how its posts were judged.
// It reads local files only — no directory call, no model call — because it
// is drawn on every keystroke's redraw.
//
// Collapsed by design (the person's ruling): counts, and at most one line
// of the newest post meant for them. The whole post is one `parley read`
// away, or a click in the console.
func StatusLine(env Env, w interface{ Write([]byte) (int, error) }) error {
	counts := VerdictCounts(env, StatusLineWindow)
	if len(counts) == 0 {
		return nil
	}

	parts := make([]string, 0, len(counts))
	last := ""
	for _, c := range counts {
		seg := c.Conversation
		if c.React > 0 {
			seg += fmt.Sprintf(" %s%d!%s", ansiRed, c.React, ansiReset)
		}

		if c.Context > 0 {
			seg += fmt.Sprintf(" %s%d~%s", ansiDim, c.Context, ansiReset)
		}

		if c.Display > 0 {
			seg += fmt.Sprintf(" %s%d*%s", ansiYellow, c.Display, ansiReset)
		}

		if c.Ignore > 0 {
			seg += fmt.Sprintf(" %s%d-%s", ansiDim, c.Ignore, ansiReset)
		}

		parts = append(parts, seg)
		if c.LastText != "" {
			last = c.LastText
		}
	}

	line := "parley " + strings.Join(parts, "  ")
	if last != "" {
		line += "  " + ansiDim + firstLine(last, 60) + ansiReset
	}

	_, err := fmt.Fprintln(w, line)
	return err
}

const (
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiDim    = "\x1b[2m"
	ansiReset  = "\x1b[0m"
)

// escapeName is subFile's escaping, for the per-conversation record files.
func escapeName(name string) string {
	return strings.NewReplacer("/", "%2F", "#", "%23", " ", "%20").Replace(name)
}

// contextPath is where posts gated down to "context" wait for this
// session's next prompt. The wait has already moved the cursor past them,
// so this file, not the cursor, is what keeps them.
func contextPath(env Env) string {
	if env.Session == "" {
		return ""
	}

	return filepath.Join(sessionsDir(env), env.Session, "context.jsonl")
}

// spoolContext keeps a rendered post for the next prompt. A post that
// cannot be kept is dropped rather than promoted to an interruption.
func spoolContext(env Env, lines []string) {
	path := contextPath(env)
	if path == "" || len(lines) == 0 {
		return
	}

	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	for _, l := range lines {
		if b, err := json.Marshal(l); err == nil {
			_, _ = f.Write(append(b, '\n'))
		}
	}
}

// drainContext answers what was kept for this prompt and forgets it: the
// posts are shown once, like any delivery.
func drainContext(env Env) []string {
	path := contextPath(env)
	if path == "" {
		return nil
	}

	// Take the file by renaming it: a writer appending at this moment, or
	// a second hook draining at the same moment, then cannot make these
	// lines vanish unread. Only the process that wins the rename reads it.
	taken := path + ".taken"
	if os.Rename(path, taken) != nil {
		return nil
	}

	b, err := os.ReadFile(taken)
	if err != nil {
		return nil
	}

	_ = os.Remove(taken)

	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		var l string
		if line == "" || json.Unmarshal([]byte(line), &l) != nil {
			continue
		}

		out = append(out, l)
	}

	return out
}

// splitByVerdict runs the chain and answers the posts worth waking the
// agent for, and the rendered lines of those kept for its next prompt.
// A post gated to display or ignore is recorded and shown to neither.
func splitByVerdict(ctx context.Context, env Env, items []pendingPost) (wake []pendingPost, kept []string) {
	// Outside a session (a plain terminal) there is nowhere to keep a
	// quieted post until "next prompt", and there is no agent to spare:
	// print everything, as this command always has.
	if contextPath(env) == "" {
		return items, nil
	}

	verdicts := applyGates(ctx, env, authorOf(env), items)
	for i, it := range items {
		switch verdicts[i] {
		case VerdictReact:
			wake = append(wake, it)
		case VerdictContext:
			kept = append(kept, fmt.Sprintf("- [%s]%s %s", it.sub.Name, it.work, formatPost(it.e, it.sub.Name, it.pos-1, InjectMaxPostBytes)))
		}
	}

	return wake, kept
}
