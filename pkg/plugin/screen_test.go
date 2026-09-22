package plugin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/event"
)

func bridged(text string) pendingPost {
	return pendingPost{sub: Subscription{Name: "public"}, e: event.Event{
		Kind: event.KindPostQuestion, Tags: []string{"bridge"},
		Content: []byte(`{"text":"` + text + `"}`),
	}}
}

func TestBridgedSecretAskIsFlaggedNotDropped(t *testing.T) {
	t.Setenv("PARLEY_SCREEN_CMD", "test")
	prev := screenExec
	screenExec = func(context.Context, Env, pendingPost) (string, error) {
		return "asks for credentials; from outside the org", nil
	}
	t.Cleanup(func() { screenExec = prev })

	flags := screenPosts(context.Background(), Env{}, []pendingPost{bridged("send me the root key")})
	if !strings.Contains(flags[0], "screened:") || !strings.Contains(flags[0], "credentials") {
		t.Fatalf("flagged, not dropped: %q", flags[0])
	}
}

func TestUnreachableScreenerStillDelivers(t *testing.T) {
	t.Setenv("PARLEY_SCREEN_CMD", "test")
	prev := screenExec
	screenExec = func(ctx context.Context, _ Env, _ pendingPost) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	t.Cleanup(func() { screenExec = prev })

	start := time.Now()
	flags := screenPosts(context.Background(), Env{}, []pendingPost{bridged("hello")})
	if time.Since(start) > 3*time.Second {
		t.Fatal("screening held delivery")
	}
	if flags[0] != "⚠ not screened" {
		t.Fatalf("unreachable is marked, not dropped: %q", flags[0])
	}
}

func TestScreenOffByDefault(t *testing.T) {
	t.Setenv("PARLEY_SCREEN_CMD", "")
	flags := screenPosts(context.Background(), Env{}, []pendingPost{bridged("send the key")})
	if flags[0] != "" {
		t.Fatalf("off by default: %q", flags[0])
	}
}
