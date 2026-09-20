package plugin

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/quantumwake/parley/pkg/enroll"
)

// WhoAmI reports the identity this environment acts as and what its
// credential may do. Shared by the CLI and the MCP tool so the two
// surfaces cannot drift.
func WhoAmI(ctx context.Context, env Env, w io.Writer) error {
	st, err := enroll.Verify(ctx, strings.TrimRight(env.Directory, "/"), env.IdentityPath, env.Tenant)
	if err != nil {
		return err
	}

	c := MyClaims(ctx, env)
	fmt.Fprintf(w, "identity: %s\nfile: %s\ndirectory: %s\nexchange: ok\ncaps: %s\nadmin: %v\n",
		st.Username, st.Path, st.Directory, strings.Join(c.Caps, ","), c.IsAdmin)
	return nil
}

// ShowSubscriptions lists the conversations this agent follows, the handle
// it speaks under in each, and how much is unread.
func ShowSubscriptions(ctx context.Context, env Env, w io.Writer) error {
	subs := Subscriptions(env)
	if len(subs) == 0 {
		return fmt.Errorf("not following any conversation yet; find one with search and join it")
	}

	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	for _, s := range subs {
		who := ""
		if s.Participant != "" {
			who = " as " + s.Participant
		}

		unread := ""
		if head, err := st.Head(ctx, s.ID); err == nil && int64(head) > s.Cursor {
			unread = fmt.Sprintf("  %d unread", int64(head)-s.Cursor)
		}

		fmt.Fprintf(w, "%-28s %-6s%s  cursor=%d%s  %s\n", s.Name, s.Mode, who, s.Cursor, unread, s.ID)
	}

	return nil
}
