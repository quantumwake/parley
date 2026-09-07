package plugin

import (
	"os"
	"path/filepath"
	"strings"
)

// The names record maps display name -> namespace id for conversations
// this plugin opened, so lookups and replays do not depend on the
// directory's name search (which needs manage) or on a scan. One file per
// conversation under <data>/names/, written by atomic rename, so any
// number of daemons can record at once with no shared file and no lock.

func namesDir(env Env) string { return filepath.Join(env.DataDir, "names") }

func nameFile(env Env, name string) string {
	return filepath.Join(namesDir(env), strings.NewReplacer("/", "%2F", "#", "%23", " ", "%20").Replace(name))
}

func nameOf(file string) string {
	return strings.NewReplacer("%2F", "/", "%23", "#", "%20", " ").Replace(filepath.Base(file))
}

// Names returns every recorded display name -> id.
func Names(env Env) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(namesDir(env))
	if err != nil {
		return out
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		b, err := os.ReadFile(filepath.Join(namesDir(env), e.Name()))
		if err != nil {
			continue
		}

		out[nameOf(e.Name())] = strings.TrimSpace(string(b))
	}

	return out
}

func namesGet(env Env, name string) string {
	b, err := os.ReadFile(nameFile(env, name))
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(b))
}

// NamesPut records one name.
func NamesPut(env Env, name, id string) {
	_ = os.MkdirAll(namesDir(env), 0o700)
	tmp := nameFile(env, name) + ".tmp"
	if os.WriteFile(tmp, []byte(id+"\n"), 0o600) == nil {
		_ = os.Rename(tmp, nameFile(env, name))
	}
}
