package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is what `statefs-ai enroll` leaves behind so hooks need no
// environment: the directory and the identity file. Environment variables
// still win when set. Lives in ~/.statefs-ai/config.json (per user, per
// host, like the identity).
type Config struct {
	Directory string `json:"directory"`
	Identity  string `json:"identity"`
	Tenant    string `json:"tenant,omitempty"`
}

// ConfigPath is the per-user config location.
func ConfigPath() string {
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
