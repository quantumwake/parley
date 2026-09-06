package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// The names map is a small per-machine record of display name -> namespace
// id for conversations this plugin opened, so lookups and replays do not
// depend on the directory's name search (which needs manage) or on a scan.
var namesMu sync.Mutex

func namesPath(env Env) string { return filepath.Join(env.DataDir, "names.json") }

func namesLoad(env Env) map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(namesPath(env)); err == nil {
		_ = json.Unmarshal(b, &m)
	}

	return m
}

func namesGet(env Env, name string) string {
	namesMu.Lock()
	defer namesMu.Unlock()
	return namesLoad(env)[name]
}

// NamesPut records one name.
func NamesPut(env Env, name, id string) {
	namesMu.Lock()
	defer namesMu.Unlock()
	m := namesLoad(env)
	m[name] = id
	_ = os.MkdirAll(env.DataDir, 0o700)
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := namesPath(env) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, namesPath(env))
	}
}
