package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/formula"
)

// resetFormulaDriftFlags restores the package-level flag vars these tests set.
// Cobra flags are process-global; leaving --json on leaks into every later test
// in this package.
func resetFormulaDriftFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		formulaDriftJSON = false
		formulaDriftEmbedded = false
		formulaDriftAcceptEmbedded = false
		formulaDriftMarkReconciled = false
	})
}

// TestFormulaDriftOne_ListsMissingSections is the operator-facing half of
// gt-yubx: `gt formula drift <name>` on a pinned formula must name the shipped
// sections the executing copy does not have. "You are behind" sent three
// operators to a hand diff in one night; two of those diffs found a landed fix
// sitting inert.
func TestFormulaDriftOne_ListsMissingSections(t *testing.T) {
	resetFormulaDriftFlags(t)
	townRoot := t.TempDir()
	pinTownFormula(t, townRoot, constants.MolDeaconPatrol, driftedPatrolFormula)

	shipped, err := formula.GetEmbeddedFormulaContent(constants.MolDeaconPatrol + ".formula.toml")
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent: %v", err)
	}

	out := captureStdout(t, func() {
		if err := formulaDriftOne(constants.MolDeaconPatrol, townRoot, ""); err != nil {
			t.Errorf("formulaDriftOne: %v", err)
		}
	})

	if !strings.Contains(out, "Shipped sections absent from this copy") {
		t.Fatalf("detail view does not list missing sections:\n%s", out)
	}
	// driftedPatrolFormula keeps exactly one step, so every other shipped step
	// must be named. Pick one from the real corpus rather than hardcoding an id
	// that a later edit could rename.
	var sampleStep string
	for _, line := range strings.Split(string(shipped), "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), `id = "`); ok {
			sampleStep = strings.TrimSuffix(id, `"`)
			break
		}
	}
	if sampleStep == "" {
		t.Fatal("shipped deacon patrol declares no steps")
	}
	if !strings.Contains(out, sampleStep) {
		t.Errorf("detail view does not name shipped step %q:\n%s", sampleStep, out)
	}
}

// TestFormulaDriftOne_SaysWhenNothingIsMissing covers the answer that bounds a
// pinned formula's risk instead of merely restating it. Silence here would be
// indistinguishable from not having compared the texts at all.
func TestFormulaDriftOne_SaysWhenNothingIsMissing(t *testing.T) {
	resetFormulaDriftFlags(t)
	townRoot := t.TempDir()
	if _, err := formula.ProvisionFormulas(townRoot); err != nil {
		t.Fatalf("ProvisionFormulas: %v", err)
	}
	// Edit the provisioned copy in a way that adds no section and removes none:
	// this is the "customized" case, and its shipped default has not moved.
	path := filepath.Join(townRoot, ".beads", "formulas", constants.MolDeaconPatrol+".formula.toml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	edited := string(content) + "\n# a local note that is not a section\n"
	if err := os.WriteFile(path, []byte(edited), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out := captureStdout(t, func() {
		if err := formulaDriftOne(constants.MolDeaconPatrol, townRoot, ""); err != nil {
			t.Errorf("formulaDriftOne: %v", err)
		}
	})
	if !strings.Contains(out, "Every section of the shipped default is present") {
		t.Errorf("no positive statement for a copy that is missing nothing:\n%s", out)
	}
	if strings.Contains(out, "Shipped sections absent") {
		t.Errorf("a comment-only local edit reported as a missing section:\n%s", out)
	}
}

// TestFormulaDriftJSON_CarriesMissingSections keeps the machine-readable shape
// in step with the human one — the witness patrol reads this with --json.
func TestFormulaDriftJSON_CarriesMissingSections(t *testing.T) {
	resetFormulaDriftFlags(t)
	townRoot := t.TempDir()
	pinTownFormula(t, townRoot, constants.MolDeaconPatrol, driftedPatrolFormula)
	formulaDriftJSON = true

	out := captureStdout(t, func() {
		if err := formulaDriftOne(constants.MolDeaconPatrol, townRoot, ""); err != nil {
			t.Errorf("formulaDriftOne: %v", err)
		}
	})

	var entries []struct {
		Name            string            `json:"name"`
		Drift           string            `json:"drift"`
		MissingSections []formula.Section `json:"missing_sections"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("Unmarshal(%q): %v", out, err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Drift != string(formula.DriftPinned) {
		t.Fatalf("drift = %q, want pinned", entries[0].Drift)
	}
	if len(entries[0].MissingSections) == 0 {
		t.Fatalf("missing_sections is empty for a copy with one step out of many:\n%s", out)
	}
	for _, s := range entries[0].MissingSections {
		if s.Kind == "" || s.Label == "" && s.ID == "" {
			t.Errorf("section serialized without kind or name: %+v", s)
		}
	}
}
