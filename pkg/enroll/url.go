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
}

// ErrBadURL is the parse refusal.
var ErrBadURL = errors.New("enroll: not an enrollment URL")

// ParseURL accepts the forms an admin console or a person may hand over:
//
//	https://directory.statefs.io/enroll?token=en_...[&tenant=acme]
//	https://directory.statefs.io/enroll#en_...
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
		token := q.Get("token")
		if token == "" {
			token = u.Fragment
		}

		if token == "" {
			return Request{}, fmt.Errorf("%w: missing token", ErrBadURL)
		}

		dir := u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/enroll")
		return Request{Directory: strings.TrimRight(dir, "/"), Token: token, Tenant: q.Get("tenant")}, nil
	}

	return Request{}, ErrBadURL
}

// String renders the canonical https form (token included; treat as secret).
func (r Request) String() string {
	v := url.Values{"token": {r.Token}}
	if r.Tenant != "" {
		v.Set("tenant", r.Tenant)
	}

	return r.Directory + "/enroll?" + v.Encode()
}
