package statefs

import (
	"context"
	"fmt"
	"strings"
)

// RelayMember is the https URL of the member that serves ns, from the
// directory's route. The daemon builds its upstream from this and never
// from a request's Host header. A route that is not https is refused.
func (s *Store) RelayMember(ctx context.Context, ns string) (string, error) {
	target, err := s.c.Route(ctx, ns)
	if err != nil {
		return "", err
	}

	u := strings.TrimRight(target.ReadURL(), "/")
	if !strings.HasPrefix(u, "https://") {
		return "", fmt.Errorf("relay: member URL is not https")
	}

	return u, nil
}
