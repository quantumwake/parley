package plugin

import (
	"path/filepath"
	"testing"
)

// statefs.ai's API is chosen like the directory: the environment, then the
// config, then the default.
func TestStatefsAIEndpointPrecedence(t *testing.T) {
	t.Setenv("STATEFS_AI_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("STATEFS_AI_APP", "")

	if got := EnvFromProcess().StatefsAI; got != DefaultStatefsAI {
		t.Fatalf("default: %q", got)
	}

	if err := SaveConfig(Config{StatefsAI: "https://app.dev.statefs.ai/"}); err != nil {
		t.Fatal(err)
	}
	if got := EnvFromProcess().StatefsAI; got != "https://app.dev.statefs.ai" {
		t.Fatalf("config: %q", got)
	}

	t.Setenv("STATEFS_AI_APP", "http://localhost:8080")
	if got := EnvFromProcess().StatefsAI; got != "http://localhost:8080" {
		t.Fatalf("environment wins: %q", got)
	}
}
