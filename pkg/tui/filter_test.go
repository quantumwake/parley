package tui

import (
	"testing"

	"github.com/quantumwake/parley/pkg/plugin"
)

func TestVisibleFilter(t *testing.T) {
	m := model{rows: []plugin.SharedRow{
		{Name: "parley development", Description: "cli"},
		{Name: "observer reports", Description: "health"},
	}, filter: "obs"}
	got := m.visible()
	if len(got) != 1 || got[0].Name != "observer reports" {
		t.Fatalf("%+v", got)
	}
}
