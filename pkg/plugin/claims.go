package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	cl, _ := claimsOf(ctx, env)
	return cl
}

// SignIn logs env's identity in and answers the env it acts in. With no
// tenant named, an identity seated in several tenants is asked to choose;
// SignIn then tries each tenant the directory lists, in order, and keeps
// the first it can enter (a key minted in one family of tenants cannot
// enter a seat in another). The error is the directory's own answer.
func SignIn(ctx context.Context, env Env) (Env, Claims, error) {
	cl, err := claimsOf(ctx, env)
	if err == nil {
		return env, cl, nil
	}

	tenants := chooseTenants(err)
	if env.Tenant != "" || len(tenants) == 0 {
		return env, Claims{}, err
	}

	for _, t := range tenants {
		next := env
		next.Tenant = t
		if cl, terr := claimsOf(ctx, next); terr == nil {
			return next, cl, nil
		}
	}

	return env, Claims{}, errors.New("seated in " + strings.Join(tenants, ", ") + ", but its key signs in to none of them")
}

// Tenant is one tenant an identity is seated in, and whether its key can
// sign in there (a key minted in one family of tenants cannot enter a seat
// in another; the directory says only "invalid credentials").
type Tenant struct {
	Slug   string `json:"slug"`
	Usable bool   `json:"usable"`
	Reason string `json:"reason,omitempty"`
}

// Tenants lists the tenants env's identity is seated in, each tried for a
// sign-in. One seat answers that tenant; several come from the directory's
// "choose a tenant" answer.
func Tenants(ctx context.Context, env Env) ([]Tenant, error) {
	env.Tenant = ""
	cl, err := claimsOf(ctx, env)
	if err == nil {
		return []Tenant{{Slug: cl.Tenant, Usable: true}}, nil
	}

	slugs := chooseTenants(err)
	if len(slugs) == 0 {
		return nil, err
	}

	out := make([]Tenant, 0, len(slugs))
	for _, t := range slugs {
		next := env
		next.Tenant = t
		_, terr := claimsOf(ctx, next)
		out = append(out, Tenant{Slug: t, Usable: terr == nil, Reason: errReason(terr)})
	}

	return out, nil
}

func errReason(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// chooseTenants reads the tenants out of a "choose a tenant" refusal, whose
// body lists the identity's memberships; nil for any other error.
func chooseTenants(err error) []string {
	msg := err.Error()
	i := strings.Index(msg, "{")
	if !strings.Contains(msg, "choose a tenant") || i < 0 {
		return nil
	}

	var body struct {
		Memberships []string `json:"memberships"`
	}
	if json.Unmarshal([]byte(msg[i:]), &body) != nil {
		return nil
	}

	return body.Memberships
}

func claimsOf(ctx context.Context, env Env) (Claims, error) {
	if env.Directory == "" || env.IdentityPath == "" {
		return Claims{}, errors.New("no directory or identity configured")
	}

	c := sfs.New(env.Directory, "", nil)
	c.Credentials = sfs.Credentials{KeyFile: env.IdentityPath, Tenant: env.Tenant}
	tok, err := c.ActingToken(ctx)
	if err != nil {
		return Claims{}, err
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("acting token is not a JWT")
	}

	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, err
	}

	var cl Claims
	if err := json.Unmarshal(b, &cl); err != nil {
		return Claims{}, err
	}

	return cl, nil
}
