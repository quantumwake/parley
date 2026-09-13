package statefs_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	sfs "github.com/quantumwake/statefs/client"

	"github.com/quantumwake/parley/pkg/store"
	adapter "github.com/quantumwake/parley/pkg/store/statefs"
	"github.com/quantumwake/parley/pkg/store/storetest"
)

// TestConformanceAgainstStatefsIO runs the S2 contract against a real
// cluster (the dev.statefs.ai tenant). Skipped unless STATEFS_DIRECTORY is
// set; credentials come from the SDK ladder. Every namespace it creates is
// tagged purpose:conformance and deleted afterwards when the credential
// holds the manage capability; otherwise the names are printed so an
// admin can remove them.
func TestConformanceAgainstStatefsIO(t *testing.T) {
	dir := os.Getenv("STATEFS_DIRECTORY")
	if dir == "" {
		t.Skip("STATEFS_DIRECTORY not set; integration test against statefs.io skipped")
	}

	if !sfs.CredentialsFromEnv().Configured() {
		t.Skip("no statefs credentials in the environment")
	}

	run := time.Now().UTC().Format("20060102-150405")
	var created []store.Namespace
	st := adapter.New(adapter.Config{Directory: dir})
	storetest.ConformanceTracked(t,
		func(t *testing.T) store.Store { return st },
		func(s string) string { return fmt.Sprintf("conformance/%s/%s", run, s) },
		func(ns store.Namespace) { created = append(created, ns) })

	ctx := context.Background()
	var left []string
	for _, ns := range created {
		if _, err := st.Client().DeleteNamespace(ctx, ns.ID); err != nil {
			left = append(left, ns.DisplayName)
		}
	}

	if len(left) > 0 {
		t.Logf("could not delete %d conformance namespaces (credential lacks manage?): %v", len(left), left)
	}
}
