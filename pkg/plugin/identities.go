package plugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/quantumwake/statefs/pkg/identityfile"
)

// LocalIdentity is one identity file on this machine, described by its
// public side only: the private key is read to validate the file and
// never leaves this package.
type LocalIdentity struct {
	Name      string `json:"name"`      // "default" for ~/.statefs/identity, the directory name under ~/.statefs/identities, else the path
	Path      string `json:"path"`      // the identity file
	Username  string `json:"username"`  // the statefs identity it proves
	Directory string `json:"directory"` // the installation it enrolled with, when recorded
	Current   bool   `json:"current"`   // the one env acts as
}

// ErrNoIdentity is a name or path that is not a readable identity file.
var ErrNoIdentity = errors.New("no such identity")

// Identities lists the identity files on this machine: the default path,
// every ~/.statefs/identities/<name>/identity, and the configured or
// acting file when it lives elsewhere. current is the path env acts as.
func Identities(current string) []LocalIdentity {
	var paths []string
	paths = append(paths, identityfile.DefaultPath())
	if home, err := os.UserHomeDir(); err == nil {
		if entries, err := os.ReadDir(filepath.Join(home, ".statefs", "identities")); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					paths = append(paths, filepath.Join(home, ".statefs", "identities", e.Name(), "identity"))
				}
			}
		}
	}

	paths = append(paths, LoadConfig().Identity, current)
	seen := map[string]bool{}
	var out []LocalIdentity
	for _, p := range paths {
		if p == "" {
			continue
		}

		abs := absPath(p)
		if seen[abs] {
			continue
		}

		seen[abs] = true
		id, err := describeIdentity(abs)
		if err != nil {
			continue
		}

		id.Current = current != "" && abs == absPath(current)
		out = append(out, id)
	}

	// The default first, then enrolled names, then files elsewhere.
	rank := func(id LocalIdentity) int {
		switch {
		case id.Name == "default":
			return 0
		case id.Name != id.Path:
			return 1
		}

		return 2
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) < rank(out[j])
		}

		return out[i].Name < out[j].Name
	})
	return out
}

// ResolveIdentity answers the identity a name or path names, as
// IdentityArg resolves an --identity flag, checked to be a readable file.
func ResolveIdentity(v string) (LocalIdentity, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return LocalIdentity{}, fmt.Errorf("%w: name one (parley identity list)", ErrNoIdentity)
	}

	p := IdentityArg(v)
	if v == "default" {
		if _, err := os.Stat(v); err != nil {
			p = identityfile.DefaultPath()
		}
	}

	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}

	id, err := describeIdentity(absPath(p))
	if err != nil {
		return LocalIdentity{}, fmt.Errorf("%w: %s (parley identity list)", ErrNoIdentity, v)
	}

	return id, nil
}

// UseIdentity makes id the machine's configured identity, as
// `parley enroll --default` does. The acting tenant belonged to the
// previous identity, so it is replaced by tenant (empty clears it); the
// directory follows the identity when its file records one.
func UseIdentity(id LocalIdentity, tenant string) (Config, error) {
	cfg := LoadConfig()
	cfg.Identity = id.Path
	cfg.Tenant = tenant
	if id.Directory != "" {
		cfg.Directory = strings.TrimRight(id.Directory, "/")
	}

	return cfg, SaveConfig(cfg)
}

// WithIdentity answers env acting as id: the directory follows the
// identity when its file records one, and the tenant is cleared.
func (e Env) WithIdentity(id LocalIdentity) Env {
	e.IdentityPath = id.Path
	e.Tenant = ""
	if id.Directory != "" {
		e.Directory = strings.TrimRight(id.Directory, "/")
	}

	return e
}

func describeIdentity(path string) (LocalIdentity, error) {
	f, err := identityfile.Read(path)
	if err != nil {
		return LocalIdentity{}, err
	}

	if f.Username == "" {
		return LocalIdentity{}, ErrNoIdentity
	}

	return LocalIdentity{Name: identityName(path), Path: path, Username: f.Username, Directory: f.Directory}, nil
}

func identityName(path string) string {
	if path == absPath(identityfile.DefaultPath()) {
		return "default"
	}

	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".statefs", "identities")
		if filepath.Base(path) == "identity" && filepath.Dir(filepath.Dir(path)) == dir {
			return filepath.Base(filepath.Dir(path))
		}
	}

	return path
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}

	return p
}
