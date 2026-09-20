package plugin

import (
	"errors"
	"strings"
	"testing"
)

func TestChooseTenantsReadsTheDirectorysList(t *testing.T) {
	err := errors.New(`token exchange: HTTP 400: {"error":"choose a tenant","memberships":["acme","other-org"]}`)
	if got := strings.Join(chooseTenants(err), ","); got != "acme,other-org" {
		t.Fatalf("%q", got)
	}

	for _, other := range []string{
		`token exchange: HTTP 401: {"error":"invalid credentials"}`,
		`token exchange: HTTP 400: choose a tenant`,
		`dial tcp: no such host`,
	} {
		if got := chooseTenants(errors.New(other)); got != nil {
			t.Fatalf("%s: %v", other, got)
		}
	}
}
