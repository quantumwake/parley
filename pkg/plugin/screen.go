package plugin

import (
	"context"
	"os"
	"strings"
	"time"
)

// screenTimeout bounds one screening pass. A slow model does not hold
// delivery past this. Posts still come through, marked not screened.
const screenTimeout = 2 * time.Second

// screenExec runs the operator's screener. Tests replace it. The default
// does not call a model: there is no key in parley. A command is later.
var screenExec = func(ctx context.Context, _ Env, it pendingPost) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return "", nil
}

// screenCommand is set when the operator opted in. Empty means off.
func screenCommand() string {
	return strings.TrimSpace(os.Getenv("PARLEY_SCREEN_CMD"))
}

func needsScreen(it pendingPost) bool {
	if os.Getenv("PARLEY_SCREEN_ALL") == "1" {
		return true
	}
	for _, tag := range it.e.Tags {
		switch strings.ToLower(tag) {
		case "bridge", "slack", "whatsapp":
			return true
		}
	}
	return false
}

// screenPosts returns a header per post. Empty means deliver as-is.
// A flagged post is still delivered. A dead screener is "not screened".
func screenPosts(ctx context.Context, env Env, items []pendingPost) []string {
	out := make([]string, len(items))
	if screenCommand() == "" {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, screenTimeout)
	defer cancel()
	for i, it := range items {
		if !needsScreen(it) {
			continue
		}
		if ctx.Err() != nil {
			out[i] = "⚠ not screened"
			continue
		}
		why, err := screenExec(ctx, env, it)
		if err != nil || ctx.Err() != nil {
			out[i] = "⚠ not screened"
			continue
		}
		if why != "" {
			out[i] = "⚠ screened: " + why
		}
	}
	return out
}
