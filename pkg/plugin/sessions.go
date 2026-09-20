package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quantumwake/parley/pkg/naming"
	"github.com/quantumwake/parley/pkg/store"
)

// Sessions lists the agent sessions this identity recorded, newest first,
// and for each says whether Claude Code can resume it from this machine:
// the resume line is printed only when the session's transcript exists
// under claudeDir, so it is never offered when it cannot work.
func Sessions(ctx context.Context, env Env, claudeDir string, limit int, w io.Writer) error {
	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	author := authorOf(env)
	if author == "" {
		return fmt.Errorf("sessions: no identity to list sessions for; run `parley enroll`")
	}

	metas, err := st.Find(ctx, store.Scope{"kind": "conversation", "mode": "agent", "agent": author}, sessionScan)
	if err != nil {
		return err
	}

	// The agent label is set by whoever creates a conversation, so it alone
	// does not say a session is this identity's; the owner is set by the
	// directory from the creating token and does.
	scanned := len(metas)
	metas = keepOwned(metas, MyClaims(ctx, env).Membership)
	if len(metas) == 0 {
		fmt.Fprintf(w, "no recorded sessions for %s\n", author)
		return nil
	}

	rows := groupSessions(metas, ActivityByID(env))
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}

	// The row count is a member read per recording, so only the sessions shown pay it.
	var shown []int
	for _, r := range rows {
		shown = append(shown, r.members...)
	}

	heads := make(map[int]store.Position, len(shown))
	var mu sync.Mutex
	Parallel(len(shown), 8, func(i int) {
		if h, err := st.Head(ctx, metas[shown[i]].ID); err == nil {
			mu.Lock()
			heads[shown[i]] = h
			mu.Unlock()
		}
	})

	for _, r := range rows {
		when := "unknown time"
		if !r.last.IsZero() {
			when = r.last.Local().Format("2006-01-02 15:04")
		}

		total, known := store.Position(0), false
		for _, m := range r.members {
			if h, ok := heads[m]; ok {
				total += h
				known = true
			}
		}

		count := "? rows"
		if known {
			count = fmt.Sprintf("%d rows", total)
		}

		extra := ""
		if len(r.members) > 1 {
			extra += fmt.Sprintf("  (%d recordings)", len(r.members))
		}

		if r.id != "" && r.id == env.Session {
			extra += "  (this session)"
		}

		fmt.Fprintf(w, "%s  %-9s %s%s\n", when, count, r.title, extra)
		fmt.Fprintf(w, "    %s\n", resumeLine(claudeDir, r.id))
	}

	if scanned >= sessionScan {
		fmt.Fprintf(w, "(looked at the first %d recordings; older sessions may not be listed)\n", sessionScan)
	}

	return nil
}

// keepOwned drops conversations another member created and labelled with this
// identity's name. With no known membership (no directory, a fake store) every
// label match stays: there is nothing to check ownership against.
func keepOwned(metas []store.Namespace, membership string) []store.Namespace {
	if membership == "" {
		return metas
	}

	out := make([]store.Namespace, 0, len(metas))
	for _, m := range metas {
		if m.Owner == membership {
			out = append(out, m)
		}
	}

	return out
}

// sessionScan bounds the directory query. The directory does not sort by
// time, so the newest sessions are picked from this many recordings, not
// from a page of the limit's size.
const sessionScan = 500

// sessionRow is one Claude Code session. A resumed session is recorded as
// a new conversation each time, so several namespaces can share one id.
type sessionRow struct {
	id      string
	title   string
	last    time.Time
	members []int // indexes into the metas the row was grouped from
}

func groupSessions(metas []store.Namespace, active map[string]time.Time) []sessionRow {
	byID := map[string]*sessionRow{}
	var order []*sessionRow
	for i, m := range metas {
		id := str(m.Scope["session"])
		key := id
		if key == "" {
			key = m.ID
		}

		r := byID[key]
		if r == nil {
			r = &sessionRow{id: id}
			byID[key] = r
			order = append(order, r)
		}

		r.members = append(r.members, i)
		at := startedAt(m)
		if a := active[m.ID]; a.After(at) {
			at = a
		}

		if at.After(r.last) {
			r.last = at
			if t := titleOf(m); t != "" {
				r.title = t
			}
		} else if r.title == "" {
			r.title = titleOf(m)
		}
	}

	out := make([]sessionRow, 0, len(order))
	for _, r := range order {
		out = append(out, *r)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].last.After(out[j].last) })
	return out
}

func resumeLine(claudeDir, id string) string {
	if !validSessionID(id) {
		return "no usable session id recorded; cannot resume"
	}

	path := transcriptPath(claudeDir, id)
	if path == "" {
		return fmt.Sprintf("no Claude Code transcript for %s on this machine; parley resumes Claude Code sessions only", id)
	}

	cwd := transcriptCWD(path)
	if cwd == "" {
		return "claude --resume " + id
	}

	if _, err := os.Stat(cwd); err != nil {
		return fmt.Sprintf("cannot resume: its directory %s no longer exists (session %s)", cwd, id)
	}

	return fmt.Sprintf("cd %s && claude --resume %s", shellQuote(cwd), id)
}

// validSessionID accepts what a Claude Code session id looks like. The id is
// a label any client can set, and it goes into a file glob and a line the
// user may paste into a shell, so anything else is refused.
func validSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}

	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}

	return true
}

// transcriptPath finds a session's Claude Code transcript under claudeDir
// (~/.claude by default): projects/<encoded cwd>/<session>.jsonl.
func transcriptPath(claudeDir, session string) string {
	if claudeDir == "" {
		return ""
	}

	hits, _ := filepath.Glob(filepath.Join(claudeDir, "projects", "*", session+".jsonl"))
	if len(hits) == 0 {
		return ""
	}

	return hits[0]
}

// transcriptCWD reads the working directory from the first lines of a
// transcript; the encoded project directory name is lossy, the field is not.
func transcriptCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}

	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for n := 0; n < 50 && sc.Scan(); n++ {
		var row struct {
			CWD string `json:"cwd"`
		}

		if json.Unmarshal(sc.Bytes(), &row) == nil && row.CWD != "" {
			return row.CWD
		}
	}

	return ""
}

// ClaudeDir is where Claude Code keeps its data: CLAUDE_CONFIG_DIR, else ~/.claude.
func ClaudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".claude")
}

// startedAt is the start time from the scope label, else from the display
// name, which older conversations carry only there.
func startedAt(m store.Namespace) time.Time {
	var ms int64
	switch v := m.Scope["started_ms"].(type) {
	case int64:
		ms = v
	case int:
		ms = int64(v)
	case float64:
		ms = int64(v)
	case json.Number:
		ms, _ = v.Int64()
	}

	if ms > 0 {
		return time.UnixMilli(ms)
	}

	if t, ok := naming.StartedFromName(m.DisplayName); ok {
		return t
	}

	return time.Time{}
}

func shellQuote(s string) string {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '/' || r == '.' || r == '_' || r == '-') {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}

	return s
}
