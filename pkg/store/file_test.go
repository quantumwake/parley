package store_test

import (
	"testing"

	"github.com/quantumwake/statefs.ai/pkg/store"
	"github.com/quantumwake/statefs.ai/pkg/store/storetest"
)

func TestFileConformance(t *testing.T) {
	storetest.Conformance(t,
		func(t *testing.T) store.Store {
			f, err := store.NewFile(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			return f
		},
		func(s string) string { return s })
}
