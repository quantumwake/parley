package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/quantumwake/statefs.ai/pkg/capture"
	"github.com/quantumwake/statefs.ai/pkg/conversation"
	"github.com/quantumwake/statefs.ai/pkg/event"
	"github.com/quantumwake/statefs.ai/pkg/store"
)

// Replay prints a conversation from position from. With diff set, it
// re-reads the transcript and reports every text or thinking block whose
// derived id is missing from the conversation, and every id present more
// than once: the M1 oracle in command form.
func Replay(ctx context.Context, env Env, ref string, from int64, asJSON bool, diff string, w io.Writer) error {
	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	id := ref
	if !looksLikeID(ref) {
		found, err := LookupName(ctx, env, st, ref)
		if err != nil {
			return err
		}

		if found == "" {
			return fmt.Errorf("replay: no conversation named %q", ref)
		}

		id = found
	}

	conv := conversation.Attach(st, id)
	seen := map[string]int{}
	var n int
	for e, err := range conv.Scan(ctx, store.Position(from), 0) {
		if err != nil {
			return err
		}

		n++
		seen[e.ID]++
		if asJSON {
			b, _ := json.Marshal(e)
			fmt.Fprintln(w, string(b))
			continue
		}

		fmt.Fprintf(w, "%5d %-20s %-10s %s\n", e.Seq, e.Kind, e.Role, snippet(e))
	}

	fmt.Fprintf(w, "%d rows\n", n)
	if diff == "" {
		return nil
	}

	var missing, dup int
	tl := &capture.Tailer{Path: diff, Author: "", CaptureThinking: env.Thinking, Emit: func(e event.Event) error {
		if seen[e.ID] == 0 {
			missing++
			fmt.Fprintf(w, "missing: %s %s\n", e.Kind, e.ID)
		}

		return nil
	}}
	if err := tl.ReadOnce(); err != nil {
		return err
	}

	for id, c := range seen {
		if c > 1 {
			dup++
			fmt.Fprintf(w, "duplicate: %s x%d\n", id, c)
		}
	}

	fmt.Fprintf(w, "diff: %d missing, %d duplicated\n", missing, dup)
	if missing > 0 || dup > 0 {
		return fmt.Errorf("replay differs from transcript")
	}

	return nil
}

// LookupName resolves a display name to a namespace id: the local names
// map written by the daemon first, then a scope search matched here.
// Never creates anything.
func LookupName(ctx context.Context, env Env, st store.Store, name string) (string, error) {
	if id := namesGet(env, name); id != "" {
		return id, nil
	}

	metas, err := st.Find(ctx, store.Scope{"kind": "conversation"}, 1000)
	if err != nil {
		return "", err
	}

	for _, m := range metas {
		if strings.EqualFold(m.DisplayName, name) {
			return m.ID, nil
		}
	}

	return "", nil
}

func looksLikeID(s string) bool {
	return strings.HasPrefix(s, "file-") || (len(s) == 36 && strings.Count(s, "-") == 4)
}

func snippet(e event.Event) string {
	var m map[string]any
	_ = json.Unmarshal(e.Content, &m)
	for _, k := range []string{"text", "reason", "cwd"} {
		if v, ok := m[k].(string); ok && v != "" {
			v = strings.ReplaceAll(v, "\n", " ")
			if len(v) > 70 {
				v = v[:70] + "..."
			}

			return v
		}
	}

	if e.ToolName != "" {
		return e.ToolName + " " + e.ToolUseID
	}

	return ""
}
