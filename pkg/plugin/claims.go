package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	sfs "github.com/quantumwake/statefs/client"
)

// Claims is what the acting token says about this identity. Read from the
// token's payload without verification: it is our own token, used only to
// interpret directory answers (ownership), never to grant anything.
type Claims struct {
	Sub        string   `json:"sub"`
	Identity   string   `json:"identity"`
	Membership string   `json:"membership"`
	Tenant     string   `json:"tenant"`
	IsAdmin    bool     `json:"is_admin"`
	Caps       []string `json:"caps"`
	Kind       string   `json:"kind"`
}

// MyClaims exchanges (or reuses) the acting token and decodes it. Zero
// Claims when no directory or identity is configured.
func MyClaims(ctx context.Context, env Env) Claims {
	if env.Directory == "" || env.IdentityPath == "" {
		return Claims{}
	}

	c := sfs.New(env.Directory, "", nil)
	c.Credentials = sfs.Credentials{KeyFile: env.IdentityPath, Tenant: env.Tenant}
	tok, err := c.ActingToken(ctx)
	if err != nil {
		return Claims{}
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return Claims{}
	}

	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}
	}

	var cl Claims
	_ = json.Unmarshal(b, &cl)
	return cl
}
