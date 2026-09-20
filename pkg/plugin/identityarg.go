package plugin

import (
	"os"
	"path/filepath"
	"strings"
)

// IdentityArg resolves the value of an --identity flag. A path is used as
// given. A bare name (no path separator, no such file) means the extra
// identity enrolled under ~/.statefs/identities/<name>/identity, so
// `--identity swarm-agent-test-1` works without spelling out the path.
func IdentityArg(v string) string {
	if v == "" || strings.ContainsAny(v, `/\`) || strings.HasPrefix(v, "~") {
		return v
	}

	if _, err := os.Stat(v); err == nil {
		return v
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return v
	}

	return filepath.Join(home, ".statefs", "identities", v, "identity")
}
