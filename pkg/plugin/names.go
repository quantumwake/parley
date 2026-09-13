package plugin

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quantumwake/parley/pkg/naming"
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

// NamedAt is one recorded conversation with its last activity: the time
// of the newest row the daemon delivered, kept as the record's mtime.
type NamedAt struct {
	Name string
	ID   string
	At   time.Time
}

// NamesByTime lists recorded conversations, most recently active first.
func NamesByTime(env Env) []NamedAt {
	entries, err := os.ReadDir(namesDir(env))
	if err != nil {
		return nil
	}

	var out []NamedAt
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue
		}

		b, err := os.ReadFile(filepath.Join(namesDir(env), e.Name()))
		if err != nil {
			continue
		}

		out = append(out, NamedAt{Name: nameOf(e.Name()), ID: strings.TrimSpace(string(b)), At: info.ModTime()})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
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

// NamesTouch stamps a recorded name with its last activity.
func NamesTouch(env Env, name string, at time.Time) {
	_ = os.Chtimes(nameFile(env, name), at, at)
}

// ActivityByID maps namespace id to last activity for every conversation
// recorded from this machine: a directory listing, no store reads.
func ActivityByID(env Env) map[string]time.Time {
	out := map[string]time.Time{}
	for _, n := range NamesByTime(env) {
		if n.At.After(out[n.ID]) {
			out[n.ID] = n.At
		}
	}

	return out
}

// forgetName drops a recorded name (after a delete).
func forgetName(env Env, name string) { _ = os.Remove(nameFile(env, name)) }

// SessionTag is naming.SessionTag, for callers that only import plugin.
func SessionTag(session string) string { return naming.SessionTag(session) }
