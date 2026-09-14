package formula

import (
	"regexp"
	"testing"
)

// TestPatrolShapeDiagramNamesRealSteps fences gt-attj1: the "Patrol Shape
// (Linear)" ASCII diagram in mol-witness-patrol's top-level description named
// a step ("check-swarm") that does not exist — the real step is
// "check-swarm-completion". `gt patrol report --steps` validates its input
// against the formula's own step IDs (validateStepLabels), so an agent that
// read the diagram literally and typed the name it saw got rejected. The
// diagram is the most prominent summary of the step vocabulary an agent
// reads, so it must never hand out a name its own tooling doesn't recognize.
func TestPatrolShapeDiagramNamesRealSteps(t *testing.T) {
	content, err := formulasFS.ReadFile("formulas/mol-witness-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading witness patrol formula: %v", err)
	}
	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing witness patrol formula: %v", err)
	}

	validIDs := make(map[string]bool, len(f.Steps))
	for _, s := range f.Steps {
		validIDs[s.ID] = true
	}
	if len(validIDs) == 0 {
		t.Fatalf("witness patrol formula has no steps")
	}

	start := regexp.MustCompile(`Patrol Shape \(Linear\)\n\n`+"```").FindStringIndex(f.Description)
	if start == nil {
		t.Fatalf("could not find the Patrol Shape diagram block in the formula description")
	}
	rest := f.Description[start[1]:]
	end := regexp.MustCompile("```").FindStringIndex(rest)
	if end == nil {
		t.Fatalf("Patrol Shape diagram block is not closed with ```")
	}
	diagram := rest[:end[0]]

	names := regexp.MustCompile(`[a-z][a-z0-9-]*`).FindAllString(diagram, -1)
	if len(names) == 0 {
		t.Fatalf("no step-like names found in the Patrol Shape diagram")
	}
	for _, name := range names {
		if !validIDs[name] {
			t.Errorf("Patrol Shape diagram names %q, which is not one of the formula's own step IDs", name)
		}
	}
}
