package plugin

import (
	"fmt"
	"strings"

	"github.com/quantumwake/parley/pkg/event"
)

// Lodestar stage 1 (parley half): two post kinds. The fold and the board
// live in statefs.ai. We only write and refuse.

func isLodestar(k event.Kind) bool {
	return k == event.KindPostObjective || k == event.KindPostAssessment
}

func checkLodestar(k event.Kind, text string, o postOptions) (map[string]any, error) {
	switch k {
	case event.KindPostObjective:
		return objectiveContent(text, o)
	case event.KindPostAssessment:
		return assessmentContent(text, o)
	default:
		return nil, nil
	}
}

func objectiveContent(text string, o postOptions) (map[string]any, error) {
	goal := strings.TrimSpace(o.goal)
	if goal == "" {
		goal = strings.TrimSpace(text)
	}
	if goal == "" {
		return nil, fmt.Errorf("objective needs goal")
	}
	if strings.TrimSpace(o.doneWhen) == "" {
		return nil, fmt.Errorf("objective needs done_when")
	}
	if strings.TrimSpace(o.owner) == "" {
		return nil, fmt.Errorf("objective needs owner")
	}
	state := strings.TrimSpace(o.state)
	if state == "" {
		state = "active"
	}
	if state != "active" && state != "retired" {
		return nil, fmt.Errorf("objective state must be active or retired")
	}
	out := map[string]any{
		"text":      goal,
		"goal":      goal,
		"done_when": strings.TrimSpace(o.doneWhen),
		"owner":     strings.TrimSpace(o.owner),
		"state":     state,
	}
	if id := strings.TrimSpace(o.amends); id != "" {
		out["amends"] = id
	}
	return out, nil
}

func assessmentContent(text string, o postOptions) (map[string]any, error) {
	claim := strings.TrimSpace(o.claim)
	if claim == "" {
		claim = strings.TrimSpace(text)
	}
	need := []struct {
		name, val string
	}{
		{"objective", o.objective},
		{"claim", claim},
		{"evidence", o.evidence},
		{"mark", o.mark},
		{"evidence_kind", o.evidenceKind},
		{"not_checked", o.notChecked},
		{"who_said", o.whoSaid},
		{"who_may", o.whoMay},
		{"judge", o.judge},
	}
	for _, f := range need {
		if strings.TrimSpace(f.val) == "" {
			return nil, fmt.Errorf("assessment needs %s", f.name)
		}
	}
	mark := strings.TrimSpace(o.mark)
	switch mark {
	case "verified", "reported", "attested":
	default:
		return nil, fmt.Errorf("assessment mark must be verified, reported or attested")
	}
	kind := strings.TrimSpace(o.evidenceKind)
	switch kind {
	case "measured", "read", "reported":
	default:
		return nil, fmt.Errorf("assessment evidence_kind must be measured, read or reported")
	}
	out := map[string]any{
		"text":          claim,
		"objective":     strings.TrimSpace(o.objective),
		"claim":         claim,
		"evidence":      strings.TrimSpace(o.evidence),
		"mark":          mark,
		"evidence_kind": kind,
		"not_checked":   strings.TrimSpace(o.notChecked),
		"who_said":      strings.TrimSpace(o.whoSaid),
		"who_may":       strings.TrimSpace(o.whoMay),
		"judge":         strings.TrimSpace(o.judge),
	}
	who, by := strings.TrimSpace(o.refusedWho), strings.TrimSpace(o.refusedBy)
	if who != "" || by != "" {
		if who == "" {
			return nil, fmt.Errorf("assessment refused needs who")
		}
		if by == "" {
			return nil, fmt.Errorf("assessment refused needs by")
		}
		out["refused"] = map[string]string{"who": who, "by": by}
	}
	return out, nil
}
