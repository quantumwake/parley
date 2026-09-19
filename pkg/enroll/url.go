// Package enroll turns an enrollment URL minted by a statefs.io tenant
// admin into a local identity: a keypair is generated on this machine, the
// public half is registered through the single-use token, and the private
// half is written to the SDK's identity file. From then on the SDK
// exchanges signed assertions for acting tokens by itself.
package enroll

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Request is what an enrollment URL carries.
type Request struct {
	Directory string // directory base URL, e.g. https://directory.statefs.io
	Token     string // the single-use enrollment token (en_...)
	Tenant    string // optional acting tenant for identities with several memberships
	StatefsAI string // optional statefs.ai API of the installation that issued the link
}

// ErrBadURL is the parse refusal.
var ErrBadURL = errors.New("enroll: not an enrollment URL")

// ParseURL accepts the forms an admin console or a person may hand over:
//
//	https://directory.statefs.io/enroll?token=en_...[&tenant=acme]
//	https://directory.statefs.io/enroll#en_...
//	https://directory.statefs.io/enroll#en_...&statefs_ai=https://app.statefs.ai
//	statefs://enroll?directory=https://directory.statefs.io&token=en_...
//	en_...                          (bare token; directory must come from elsewhere)
func ParseURL(s string) (Request, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "en_") {
		return Request{Token: s}, nil
	}

	u, err := url.Parse(s)
	if err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrBadURL, err)
	}

	q := u.Query()
	switch {
	case u.Scheme == "statefs" && u.Host == "enroll":
		r := Request{Directory: strings.TrimRight(q.Get("directory"), "/"), Token: q.Get("token"), Tenant: q.Get("tenant")}
		if r.Directory == "" || r.Token == "" {
			return Request{}, fmt.Errorf("%w: statefs://enroll needs directory and token", ErrBadURL)
		}

		return r, nil
	case (u.Scheme == "https" || u.Scheme == "http") && strings.HasSuffix(u.Path, "/enroll"):
		// The fragment is the token, optionally followed by key=value pairs
		// (statefs_ai, tenant); it never reaches a server log.
		token, frag := fragmentFields(u.Fragment)
		if t := q.Get("token"); t != "" {
			token = t
		}

		if token == "" {
			return Request{}, fmt.Errorf("%w: missing token", ErrBadURL)
		}

		tenant := q.Get("tenant")
		if tenant == "" {
			tenant = frag.Get("tenant")
		}

		dir := u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/enroll")
		return Request{Directory: strings.TrimRight(dir, "/"), Token: token, Tenant: tenant,
			StatefsAI: strings.TrimRight(firstNonEmpty(q.Get("statefs_ai"), frag.Get("statefs_ai")), "/")}, nil
	}

	return Request{}, ErrBadURL
}

// fragmentFields splits "en_...&k=v&k=v" into the token (the first part
// without '=', or token=) and the rest.
func fragmentFields(fragment string) (string, url.Values) {
	fields := url.Values{}
	token := ""
	for _, part := range strings.Split(fragment, "&") {
		k, v, kv := strings.Cut(part, "=")
		switch {
		case !kv && token == "":
			token = part
		case kv && k == "token":
			token, _ = url.QueryUnescape(v)
		case kv:
			v, _ = url.QueryUnescape(v)
			fields.Set(k, v)
		}
	}

	return token, fields
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}

	return ""
}

// String renders the canonical https form (token included; treat as secret).
func (r Request) String() string {
	v := url.Values{"token": {r.Token}}
	if r.Tenant != "" {
		v.Set("tenant", r.Tenant)
	}

	return r.Directory + "/enroll?" + v.Encode()
}
