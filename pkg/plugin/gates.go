package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quantumwake/parley/pkg/event"
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

// StatusLineChannels is the per-conversation line `parley statusline
// --channels` prints: each followed conversation, its unread count, and how
// its posts were judged. It was the status line's default until the person
// decided it told them nothing they used, and the default became who this
// session is (StatusLineWho). It reads local files only — no directory call,
// no model call — because a status line is drawn on every redraw.
//
// Collapsed by design (the person's ruling): counts, and at most one line
// of the newest post meant for them. The whole post is one `parley read`
// away, or a click in the console.
func StatusLineChannels(env Env, w interface{ Write([]byte) (int, error) }) error {
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

// SpoolMaxAge is how long a post stays kept for a next prompt. Its clock
// starts when the post is first kept and does not restart when a Stop that
// does not hold the turn keeps it again, so a session that is never shown
// its kept posts loses them after this instead of carrying them for days:
// the observer seat's spool held 1,863 lines and 1,614 posts, some of them
// days old and kept up to nine times (2026-09-29). Past it a post is still
// in its conversation, one `parley read` away.
const SpoolMaxAge = 6 * time.Hour

// SpoolMaxEntries caps the posts one session keeps. The newest stay.
const SpoolMaxEntries = 200

// spoolNow is the spool's clock; a test moves it.
var spoolNow = time.Now

// keptEntry is one kept post. The spool file is one JSON object per line.
// A spool written before entries carried their time holds bare JSON
// strings; readKept still reads those.
type keptEntry struct {
	Kept int64  `json:"k"`            // unix ms when the post was first kept
	ID   string `json:"id,omitempty"` // the post's event id (headerID)
	Line string `json:"l"`            // the post as a prompt shows it
}

// key is what makes two entries the same post: its conversation and its
// event id. A line that names no post is keyed by its text.
func (e keptEntry) key() string {
	return lineKey(e.Line, e.ID)
}

// lineKey is keptEntry.key for a rendered line.
func lineKey(line, id string) string {
	if id == "" {
		id = headerID(line)
	}

	if id == "" {
		return "text\x00" + line
	}

	conv, _ := lineWhere(line)

	return conv + "\x00" + id
}

// keptSince remembers, for the lines this process drained, when each was
// first kept, so that keeping them again (a Stop that does not hold the turn
// drains the spool and keeps what it did not show) does not restart their
// clock. Without it a post kept on every Stop would never expire.
var keptSince = struct {
	sync.Mutex
	at map[string]int64
}{at: map[string]int64{}}

func rememberKept(entries []keptEntry) {
	keptSince.Lock()
	defer keptSince.Unlock()

	if len(keptSince.at) > 4*SpoolMaxEntries {
		keptSince.at = map[string]int64{}
	}

	for _, e := range entries {
		keptSince.at[e.key()] = e.Kept
	}
}

// keptAt is when a line was first kept: when this process drained it, the
// time it carried; otherwise now.
func keptAt(line string, now time.Time) int64 {
	keptSince.Lock()
	defer keptSince.Unlock()

	if k, ok := keptSince.at[lineKey(line, "")]; ok {
		return k
	}

	return now.UnixMilli()
}

// compactKept is the spool's rules, applied on every write and every
// drain: an entry older than SpoolMaxAge is gone; a post kept more than once
// is kept once, with the earliest time it was kept; and past
// SpoolMaxEntries only the newest stay. The order of what stays is kept.
func compactKept(in []keptEntry, now time.Time) []keptEntry {
	cutoff := now.Add(-SpoolMaxAge).UnixMilli()
	index := map[string]int{}
	out := make([]keptEntry, 0, len(in))

	for _, e := range in {
		if e.Line == "" || e.Kept < cutoff {
			continue
		}

		k := e.key()
		if i, ok := index[k]; ok {
			if e.Kept < out[i].Kept {
				out[i].Kept = e.Kept
			}

			continue
		}

		index[k] = len(out)
		out = append(out, e)
	}

	if len(out) <= SpoolMaxEntries {
		return out
	}

	order := make([]int, len(out))
	for i := range order {
		order[i] = i
	}

	// Newest first; of two kept at the same moment, the later written.
	sort.SliceStable(order, func(a, b int) bool {
		if out[order[a]].Kept != out[order[b]].Kept {
			return out[order[a]].Kept > out[order[b]].Kept
		}

		return order[a] > order[b]
	})

	stay := make(map[int]bool, SpoolMaxEntries)
	for _, i := range order[:SpoolMaxEntries] {
		stay[i] = true
	}

	trimmed := make([]keptEntry, 0, SpoolMaxEntries)
	for i, e := range out {
		if stay[i] {
			trimmed = append(trimmed, e)
		}
	}

	return trimmed
}

// parseKept reads one line in either form. A bare string is a delivery
// file's line, or a spool line written before entries carried their time.
// Its time is the post's own (the event id is a ULID, never later than now),
// or zero when the id carries none (a person's post from the portal has a
// UUID). Zero is unknown age, which the spool's rules (compactKept) treat as
// expired: the observer seat's old spool held 213 lines of the owner's
// portal posts, days old, which would otherwise have come back as new. A
// delivery file is read with readTaken, which does not expire anything.
func parseKept(raw []byte, now time.Time) (keptEntry, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return keptEntry{}, false
	}

	if raw[0] == '"' {
		var l string
		if json.Unmarshal(raw, &l) != nil || l == "" {
			return keptEntry{}, false
		}

		e := keptEntry{ID: headerID(l), Line: l}
		if t := event.ULIDTime(e.ID); !t.IsZero() {
			e.Kept = min(t.UnixMilli(), now.UnixMilli())
		}

		return e, true
	}

	var e keptEntry
	if json.Unmarshal(raw, &e) != nil || e.Line == "" {
		return keptEntry{}, false
	}

	if e.ID == "" {
		e.ID = headerID(e.Line)
	}

	return e, true
}

// readKept answers a taken spool file's entries and removes the file.
func readKept(taken string, now time.Time) []keptEntry {
	b, err := os.ReadFile(taken)
	_ = os.Remove(taken)

	if err != nil {
		return nil
	}

	var out []keptEntry
	for _, line := range bytes.Split(b, []byte("\n")) {
		if e, ok := parseKept(line, now); ok {
			out = append(out, e)
		}
	}

	return out
}

// writeKept appends entries to the spool. Nothing is written for none.
func writeKept(path string, entries []keptEntry) {
	if path == "" || len(entries) == 0 {
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

	for _, e := range entries {
		if b, err := json.Marshal(e); err == nil {
			_, _ = f.Write(append(b, '\n'))
		}
	}
}

// spoolContext keeps rendered posts for the next prompt. A post that cannot
// be kept is dropped rather than promoted to an interruption.
//
// It takes the spool, adds these posts, applies compactKept and writes the
// result back, so a post already kept is not kept a second time whichever
// route keeps it again: a wait keeping a post the prompt hook also read, a
// Stop keeping what it drained. A writer that appends while this runs
// starts a new file, and the next drain merges the two.
func spoolContext(env Env, lines []string) {
	path := contextPath(env)
	if path == "" || len(lines) == 0 {
		return
	}

	now := spoolNow()
	add := make([]keptEntry, 0, len(lines))
	for _, l := range lines {
		add = append(add, keptEntry{Kept: keptAt(l, now), ID: headerID(l), Line: l})
	}

	var all []keptEntry
	if taken, ok := takeFile(path); ok {
		all = readKept(taken, now)
	}

	writeKept(path, compactKept(append(all, add...), now))
}

// drainContext answers what was kept for this prompt and forgets it: the
// posts are shown once, like any delivery. What it answers has been through
// compactKept: no post twice, nothing past SpoolMaxAge, at most
// SpoolMaxEntries.
func drainContext(env Env) []string {
	taken, ok := takeFile(contextPath(env))
	if !ok {
		return nil
	}

	now := spoolNow()
	entries := compactKept(readKept(taken, now), now)
	rememberKept(entries)

	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = e.Line
	}

	return lines
}

// takeSeq tells apart two takes by one process.
var takeSeq atomic.Int64

// takeFile takes a spool file (the kept posts, or a delivery in flight) by
// renaming it to a name of this take's own, and answers that name. A writer
// appending at this moment then creates a new file instead of losing lines
// to the taker, and a second taker (a hook draining, or a read unspooling)
// at the same moment takes that new file, never this copy. A shared name
// would not do: a rename replaces an existing target, so a second take could
// overwrite a first one still unread.
func takeFile(path string) (string, bool) {
	if path == "" {
		return "", false
	}

	taken := fmt.Sprintf("%s.taken.%d.%d", path, os.Getpid(), takeSeq.Add(1))
	if os.Rename(path, taken) != nil {
		return "", false
	}

	return taken, true
}

// readTaken answers a taken file's lines and removes it. It reads both the
// bare-string lines a delivery file holds and a spool's entries.
func readTaken(taken string) []string {
	entries := readKept(taken, spoolNow())

	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Line)
	}

	return out
}

// postHeader is a rendered post's own " (<event id>) @<position>:", which
// formatPost writes before the text. The speaker's "(…)" and a work note's
// "(…) at @n]" do not have this shape.
var postHeader = regexp.MustCompile(` \(([^()\s]+)\) @\d+:`)

// headerID is the event id of the post a kept line renders: the first
// header token in the line. The first is the post's own, since its text
// comes after it; a later one is text quoting some other post.
func headerID(line string) string {
	m := postHeader.FindStringSubmatch(line)
	if m == nil {
		return ""
	}

	return m[1]
}

// unspool forgets the posts a session has just been shown some other way,
// so a post is displayed once whichever route reaches it first. A comment is
// kept for the next prompt when a wait wakes for something else; if the
// session then reads the channel itself (`parley read`, with or without
// --peek, which is how an agent catches up), the same comment would be shown
// again at the next prompt, and again in the tokens it costs. Posts not in
// ids stay kept.
//
// A kept line is matched by its own header's event id (headerID), not by
// the id appearing anywhere: a comment quoting a shown post's header line is
// a different post, and stays. Nothing about the file's format changes, and
// a spool written by an earlier version is handled the same way.
func unspool(env Env, ids []string) {
	if len(ids) == 0 {
		return
	}

	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			want[id] = true
		}
	}

	if len(want) == 0 {
		return
	}

	path := contextPath(env)
	taken, ok := takeFile(path)
	if !ok {
		return
	}

	now := spoolNow()
	var keep []keptEntry
	for _, e := range readKept(taken, now) {
		if !want[e.ID] {
			keep = append(keep, e)
		}
	}

	writeKept(path, compactKept(keep, now))
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
