package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is what `parley enroll` leaves behind so hooks need no
// environment: the directory and the identity file. Environment variables
// still win when set. Lives in ~/.statefs-ai/config.json (per user, per
// host, like the identity).
type Config struct {
	Directory string `json:"directory"`
	Identity  string `json:"identity"`
	Tenant    string `json:"tenant,omitempty"`
	StatefsAI string `json:"statefs_ai,omitempty"` // statefs.ai's API, for a dev or other installation
	Gates     []Gate `json:"gates,omitempty"`      // delivery gates, run last and only on the wait path; none by default
}

// DefaultDirectory is empty on purpose: a public plugin must not probe a
// production directory. Enroll writes the directory into the config; the
// environment can still set STATEFS_DIRECTORY.
const DefaultDirectory = ""

// DefaultStatefsAI is statefs.ai's public app, used when neither
// STATEFS_AI_APP nor the config names one.
const DefaultStatefsAI = "https://app.statefs.ai"

// ConfigPath is the per-user config location; STATEFS_AI_CONFIG overrides
// it (tests point it at a temp file so they never touch the real one).
func ConfigPath() string {
	if p := os.Getenv("STATEFS_AI_CONFIG"); p != "" {
		return p
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ".statefs-ai/config.json"
	}

	return filepath.Join(home, ".statefs-ai", "config.json")
}

// LoadConfig reads the config; a missing file is an empty config.
func LoadConfig() Config {
	var c Config
	if b, err := os.ReadFile(ConfigPath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}

	return c
}

// SaveConfig writes the config with owner-only permissions.
func SaveConfig(c Config) error {
	if err := os.MkdirAll(filepath.Dir(ConfigPath()), 0o700); err != nil {
		return err
	}

	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	tmp := ConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, ConfigPath())
}
