package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/quantumwake/statefs.ai/pkg/conversation"
	"github.com/quantumwake/statefs.ai/pkg/event"
	"github.com/quantumwake/statefs.ai/pkg/store"
)

// Describe sets a conversation's title, description and tags: labels on the
// namespace (so listings and searches see them) and a meta.purpose row in
// the log (so the history of what it was about survives). With an empty
// target it describes the session recording on this machine right now, so
// the agent inside a session can describe its own work.
func Describe(ctx context.Context, env Env, target, title, description string, tags []string, w io.Writer) error {
	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	id := ""
	if target == "" {
		id = liveConversation(env)
		if id == "" {
			return errors.New("describe: no session is recording on this machine right now; name a conversation")
		}
	} else if id, err = resolveAny(ctx, env, st, target); err != nil {
		return err
	}

	labels := store.Scope{}
	if title != "" {
		labels["title"] = title
	}

	if description != "" {
		labels["description"] = description
	}

	if len(tags) > 0 {
		labels["tags"] = tags
	}

	if len(labels) == 0 {
		return errors.New("describe needs --title, --description or --tags")
	}

	labelErr := st.Describe(ctx, id, labels)
	if labelErr != nil && !errors.Is(labelErr, store.ErrRefused) {
		return labelErr
	}

	body, _ := json.Marshal(map[string]any{"name": title, "purpose": description, "tags": tags})
	e := event.Event{ID: event.NewID(), TSMs: time.Now().UnixMilli(), Source: event.SourceProduct, Kind: event.KindMetaPurpose,
		Role: event.RoleSystem, Author: authorOf(env), Content: body}
	if _, err := conversation.Attach(st, id).Append(ctx, false, e); err != nil {
		return err
	}

	fmt.Fprintf(w, "described %s: %s\n", id, strings.TrimSpace(title+" "+description))
	if labelErr != nil {
		fmt.Fprintln(w, "note: the description is in the conversation's log (meta.purpose); relabeling the namespace for listings needs the own capability on this identity's key (re-enroll with --caps read,write,own) or an admin")
	}

	return nil
}

// liveConversation is the namespace id of the session whose capture daemon
// is running here (the newest when several are).
func liveConversation(env Env) string {
	pids, _ := filepath.Glob(filepath.Join(env.DataDir, "daemon-*.pid"))
	var best string
	var bestAt time.Time
	for _, pf := range pids {
		b, err := os.ReadFile(pf)
		if err != nil {
			continue
		}

		var pid int
		if _, err := fmt.Sscanf(string(b), "%d", &pid); err != nil || !ProcessAlive(pid) {
			continue
		}

		sid := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(pf), "daemon-"), ".pid")
		for _, n := range NamesByTime(env) {
			if strings.HasSuffix(n.Name, "#"+SessionTag(sid)) && n.At.After(bestAt) {
				best, bestAt = n.ID, n.At
			}
		}
	}

	return best
}
