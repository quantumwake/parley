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

	metas, err := st.Find(ctx, store.Scope{"kind": "conversation", "mode": "agent", "agent": author}, limit)
	if err != nil {
		return err
	}

	heads := make([]store.Position, len(metas))
	for i := range heads {
		heads[i] = store.HeadUnknown
	}

	Parallel(len(metas), 8, func(i int) {
		if h, err := st.Head(ctx, metas[i].ID); err == nil {
			heads[i] = h
		}
	})

	if len(metas) == 0 {
		fmt.Fprintf(w, "no recorded sessions for %s\n", author)
		return nil
	}

	active := ActivityByID(env)
	rows := groupSessions(metas, heads, active)
	for _, r := range rows {
		when := "unknown time"
		if !r.last.IsZero() {
			when = r.last.Local().Format("2006-01-02 15:04")
		}

		count := "? rows"
		if r.rows >= 0 {
			count = fmt.Sprintf("%d rows", r.rows)
		}

		extra := ""
		if r.recordings > 1 {
			extra += fmt.Sprintf("  (%d recordings)", r.recordings)
		}

		if r.id != "" && r.id == env.Session {
			extra += "  (this session)"
		}

		fmt.Fprintf(w, "%s  %-9s %s%s\n", when, count, r.title, extra)
		fmt.Fprintf(w, "    %s\n", resumeLine(claudeDir, r.id, author))
	}

	return nil
}

// sessionRow is one Claude Code session. A resumed session is recorded as
// a new conversation each time, so several namespaces can share one id.
type sessionRow struct {
	id         string
	title      string
	last       time.Time
	rows       store.Position
	recordings int
}

func groupSessions(metas []store.Namespace, heads []store.Position, active map[string]time.Time) []sessionRow {
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
			r = &sessionRow{id: id, rows: store.HeadUnknown}
			byID[key] = r
			order = append(order, r)
		}

		r.recordings++
		if heads[i] != store.HeadUnknown {
			if r.rows < 0 {
				r.rows = 0
			}

			r.rows += heads[i]
		}

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

func resumeLine(claudeDir, id, author string) string {
	if id == "" {
		return "no session id recorded; cannot resume"
	}

	path := transcriptPath(claudeDir, id)
	if path == "" {
		return fmt.Sprintf("cannot resume from here: no transcript for %s on this machine (recorded by %s)", id, author)
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

// transcriptPath finds a session's Claude Code transcript under claudeDir
// (~/.claude by default): projects/<encoded cwd>/<session>.jsonl.
func transcriptPath(claudeDir, session string) string {
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
