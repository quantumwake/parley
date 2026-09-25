package plugin

import (
	"encoding/json"
	"fmt"
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
	// Features names the optional paths this machine has turned on
	// (features.go). Empty is the default and means "behave as parley did
	// before any of them existed".
	Features []string `json:"features,omitempty"`
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

// LoadConfig reads the config; a missing OR unreadable file is an empty
// config, because a hook must keep working rather than fail a turn over
// one. A caller that is about to WRITE the config must use ReadConfig
// instead: replacing a file it could not read loses whatever was in it.
func LoadConfig() Config {
	c, _ := ReadConfig()
	return c
}

// ReadConfig is LoadConfig, and says whether the file on disk was
// understood. A file that exists and does not parse answers an error, so
// a writer can refuse instead of quietly saving over an enrollment it
// never saw.
func ReadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(ConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil // never enrolled: an empty config is the truth
		}

		return c, err
	}

	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("config %s is not readable JSON: %w", ConfigPath(), err)
	}

	return c, nil
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

	// A fixed ".tmp" name is a shared mutable file: two parley processes
	// writing at once truncate each other's and one renames a half-written
	// config, which LoadConfig then reads as an EMPTY config — a machine
	// that looks unenrolled. Several seats share this file on one laptop,
	// so this is a matter of when, not whether. A unique name per writer
	// makes the rename the only shared moment, which is atomic.
	f, err := os.CreateTemp(filepath.Dir(ConfigPath()), ".config-*.json")
	if err != nil {
		return err
	}

	tmp := f.Name()
	defer os.Remove(tmp) // a no-op once the rename below has moved it

	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}

	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmp, ConfigPath())
}
