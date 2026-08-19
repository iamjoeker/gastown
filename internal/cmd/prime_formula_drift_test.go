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

// pinTownFormula writes a tier-2 copy of formulaName that is BOTH different
// from the embedded default and recorded in .installed.json under a third,
// unrelated hash. That is the pinned case: gt skips modified files, so this
// copy shadows the shipped default forever (gt-0wm7).
func pinTownFormula(t *testing.T, townRoot, formulaName, content string) string {
	t.Helper()
	filename := formulaName + ".formula.toml"
	dir := filepath.Join(townRoot, ".beads", "formulas")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	record := formula.InstalledRecord{Formulas: map[string]string{
		filename: "0000000000000000000000000000000000000000000000000000000000000000",
	}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".installed.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile installed record: %v", err)
	}
	return path
}

const driftedPatrolFormula = `description = "stale local copy"
formula = "mol-deacon-patrol"
version = 1

[[steps]]
id = "only-step"
title = "Do the stale thing"
description = "This copy predates the shipped fix."
`

// TestRenderFormulaStepsFull_WarnsOnPinnedDrift is the regression test for
// gt-0wm7: an agent priming on a formula whose executing copy shadows a newer
// shipped default must be told so in the same output as the steps. Before this,
// prime rendered the stale steps silently and a merged P1 fix sat inert for two
// days looking done.
func TestRenderFormulaStepsFull_WarnsOnPinnedDrift(t *testing.T) {
	townRoot := t.TempDir()
	path := pinTownFormula(t, townRoot, constants.MolDeaconPatrol, driftedPatrolFormula)

	out, err := renderFormulaStepsFull(constants.MolDeaconPatrol, townRoot, "")
	if err != nil {
		t.Fatalf("renderFormulaStepsFull: %v", err)
	}

	for _, want := range []string{
		"FORMULA DRIFT",
		constants.MolDeaconPatrol,
		path,
		"gt formula drift " + constants.MolDeaconPatrol,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime output missing %q:\n%s", want, out)
		}
	}

	// The steps themselves must still render — the warning informs, it does not
	// replace the checklist.
	if !strings.Contains(out, "Do the stale thing") {
		t.Errorf("prime output dropped the formula steps:\n%s", out)
	}
}

// TestRenderFormulaRootAndStepsFull_WarnsOnPinnedDrift covers the other prime
// rendering entry point.
func TestRenderFormulaRootAndStepsFull_WarnsOnPinnedDrift(t *testing.T) {
	townRoot := t.TempDir()
	pinTownFormula(t, townRoot, constants.MolDeaconPatrol, driftedPatrolFormula)

	out, err := renderFormulaRootAndStepsFull(constants.MolDeaconPatrol, townRoot, "")
	if err != nil {
		t.Fatalf("renderFormulaRootAndStepsFull: %v", err)
	}
	if !strings.Contains(out, "FORMULA DRIFT") {
		t.Errorf("prime output missing the drift warning:\n%s", out)
	}
	if !strings.Contains(out, "Do the stale thing") {
		t.Errorf("prime output dropped the formula steps:\n%s", out)
	}
}

// TestRenderFormulaStepsFull_QuietWhenNoDrift keeps the warning off the normal
// path: an unmodified town whose copies match the shipped defaults must render
// exactly as before.
func TestRenderFormulaStepsFull_QuietWhenNoDrift(t *testing.T) {
	townRoot := t.TempDir()
	if _, err := formula.ProvisionFormulas(townRoot); err != nil {
		t.Fatalf("ProvisionFormulas: %v", err)
	}

	out, err := renderFormulaStepsFull(constants.MolDeaconPatrol, townRoot, "")
	if err != nil {
		t.Fatalf("renderFormulaStepsFull: %v", err)
	}
	if strings.Contains(out, "FORMULA DRIFT") {
		t.Errorf("freshly provisioned town must not warn:\n%s", out)
	}
}

// TestRenderFormulaStepsFull_QuietOnDeliberateCustomization verifies the filter
// that keeps the warning credible: a local edit whose shipped default has NOT
// moved since is not hiding anything, so it must stay silent.
func TestRenderFormulaStepsFull_QuietOnDeliberateCustomization(t *testing.T) {
	townRoot := t.TempDir()
	if _, err := formula.ProvisionFormulas(townRoot); err != nil {
		t.Fatalf("ProvisionFormulas: %v", err)
	}

	// Overwrite the provisioned copy but leave .installed.json pointing at the
	// embedded hash, which is what ProvisionFormulas recorded.
	path := filepath.Join(townRoot, ".beads", "formulas", constants.MolDeaconPatrol+".formula.toml")
	if err := os.WriteFile(path, []byte(driftedPatrolFormula), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out, err := renderFormulaStepsFull(constants.MolDeaconPatrol, townRoot, "")
	if err != nil {
		t.Fatalf("renderFormulaStepsFull: %v", err)
	}
	if strings.Contains(out, "FORMULA DRIFT") {
		t.Errorf("a customization with no newer shipped default must not warn:\n%s", out)
	}
}
