package plugin

import "testing"

func TestSessionFromEnvFirstSetWins(t *testing.T) {
	t.Setenv("PARLEY_SESSION", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("GROK_SESSION_ID", "")
	if SessionFromEnv() != "" {
		t.Fatal("none set")
	}

	t.Setenv("GROK_SESSION_ID", "grok-1")
	if SessionFromEnv() != "grok-1" {
		t.Fatalf("Grok alone: %q", SessionFromEnv())
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "claude-1")
	if SessionFromEnv() != "claude-1" {
		t.Fatalf("Claude wins over Grok when both are set: %q", SessionFromEnv())
	}

	t.Setenv("PARLEY_SESSION", "parley-1")
	if SessionFromEnv() != "parley-1" {
		t.Fatalf("PARLEY_SESSION is the product name and wins: %q", SessionFromEnv())
	}
}

func TestEnvFromProcessSessionIsGrok(t *testing.T) {
	t.Setenv("PARLEY_SESSION", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("GROK_SESSION_ID", "01a0bf7d-dba2-72a0-ac04-b6ac78805fbb")
	if got := EnvFromProcess().Session; got != "01a0bf7d-dba2-72a0-ac04-b6ac78805fbb" {
		t.Fatalf("EnvFromProcess uses GROK_SESSION_ID: %q", got)
	}
}
