package store

import (
	"errors"
	"fmt"
	"testing"
)

// A 401 is its own error, and still a refusal to callers that only ask that.
func TestUnauthenticatedIsARefusal(t *testing.T) {
	err := fmt.Errorf("%w: HTTP 401 authentication required", ErrUnauthenticated)
	if !errors.Is(err, ErrUnauthenticated) || !errors.Is(err, ErrRefused) {
		t.Fatalf("a 401 is unauthenticated and refused: %v", err)
	}

	if errors.Is(fmt.Errorf("%w: HTTP 403", ErrRefused), ErrUnauthenticated) {
		t.Fatal("a 403 is not unauthenticated")
	}
}
