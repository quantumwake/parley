package plugin

import (
	"context"
	"fmt"
	"io"
	"strings"

	sfs "github.com/quantumwake/statefs/client"
)

// CleanupConformance deletes every namespace the store conformance suite
// left in the tenant (display name prefix "conformance/" or scope
// purpose:conformance). Deleting needs a credential with the manage
// capability; with less it lists what it would delete and reports each
// refusal. dryRun lists only.
func CleanupConformance(ctx context.Context, directory string, dryRun bool, w io.Writer) error {
	c := sfs.New(directory, "", nil)
	c.Credentials = sfs.CredentialsFromEnv()
	seen := map[string]sfs.NamespaceMeta{}
	for _, filter := range []map[string]any{{"purpose": "conformance"}, {}} {
		metas, err := c.FindNamespaces(ctx, filter, 1000)
		if err != nil {
			return err
		}

		for _, m := range metas {
			if strings.HasPrefix(m.DisplayName, "conformance/") || m.Scope["purpose"] == "conformance" {
				seen[m.Namespace] = m
			}
		}
	}

	fmt.Fprintf(w, "%d conformance namespaces\n", len(seen))
	var refused int
	for id, m := range seen {
		if dryRun {
			fmt.Fprintf(w, "  %-45s %s\n", m.DisplayName, id)
			continue
		}

		if _, err := c.DeleteNamespace(ctx, id); err != nil {
			refused++
			fmt.Fprintf(w, "  %-45s refused: %v\n", m.DisplayName, err)
			continue
		}

		fmt.Fprintf(w, "  %-45s deleted\n", m.DisplayName)
	}

	if refused > 0 {
		return fmt.Errorf("%d namespaces not deleted (a credential with the manage capability is required)", refused)
	}

	return nil
}
