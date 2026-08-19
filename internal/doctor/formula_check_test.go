package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/formula"
)

func TestNewFormulaCheck(t *testing.T) {
	check := NewFormulaCheck()
	if check.Name() != "formulas" {
		t.Errorf("Name() = %q, want %q", check.Name(), "formulas")
	}
	if !check.CanFix() {
		t.Error("FormulaCheck should be fixable")
	}
}

func TestFormulaCheck_Run_AllOK(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision formulas fresh
	_, err := formula.ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	check := NewFormulaCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Status = %v, want %v", result.Status, StatusOK)
	}
}

func TestFormulaCheck_Run_Missing(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision formulas
	_, err := formula.ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Delete a formula
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	formulaPath := filepath.Join(formulasDir, "mol-deacon-patrol.formula.toml")
	if err := os.Remove(formulaPath); err != nil {
		t.Fatal(err)
	}

	check := NewFormulaCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Status = %v, want %v", result.Status, StatusWarning)
	}
	if result.FixHint == "" {
		t.Error("should have FixHint")
	}
}

// TestFormulaCheck_Run_ModifiedShadowingNewerEmbedded verifies that a locally
// customized formula whose shipped default has since changed warns instead of
// reporting OK. UpdateFormulas will never touch such a file, so a fix shipped in
// the binary stays out of the town indefinitely — silently, before this (gt-0sq).
func TestFormulaCheck_Run_ModifiedShadowingNewerEmbedded(t *testing.T) {
	tmpDir := t.TempDir()

	if _, err := formula.ProvisionFormulas(tmpDir); err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	if err := os.WriteFile(filepath.Join(formulasDir, "mol-deacon-patrol.formula.toml"), []byte("# local edit\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Rewrite the install record so the embedded copy reads as newer than the
	// one this town installed.
	recordPath := filepath.Join(formulasDir, ".installed.json")
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Formulas map[string]string `json:"formulas"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record.Formulas["mol-deacon-patrol.formula.toml"] = strings.Repeat("0", 64)
	out, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, out, 0644); err != nil {
		t.Fatal(err)
	}

	result := NewFormulaCheck().Run(&CheckContext{TownRoot: tmpDir})

	if result.Status != StatusWarning {
		t.Errorf("Status = %v, want %v", result.Status, StatusWarning)
	}
	// --fix cannot repair this: UpdateFormulas skips modified files, so a hint
	// pointing at it would send the operator in a circle. Point at the command
	// that can actually reconcile instead.
	if !strings.Contains(result.FixHint, "gt formula drift") {
		t.Errorf("FixHint = %q, want it to name the reconcile command", result.FixHint)
	}
	if strings.Contains(result.FixHint, "doctor --fix") {
		t.Errorf("FixHint = %q, must not send the operator back to --fix", result.FixHint)
	}
}

// driftTownRoot provisions a town whose named formula is locally modified AND
// shadowing a newer shipped default: the file no longer matches what gt
// installed, and the install record points at a hash the embedded corpus has
// since moved past.
func driftTownRoot(t *testing.T, name string) string {
	t.Helper()

	tmpDir := t.TempDir()
	if _, err := formula.ProvisionFormulas(tmpDir); err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	if err := os.WriteFile(filepath.Join(formulasDir, name), []byte("# local edit\n"), 0644); err != nil {
		t.Fatal(err)
	}

	recordPath := filepath.Join(formulasDir, ".installed.json")
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Formulas map[string]string `json:"formulas"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record.Formulas[name] = strings.Repeat("0", 64)
	out, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, out, 0644); err != nil {
		t.Fatal(err)
	}

	return tmpDir
}

// TestFormulaCheck_Fix_ReportsWhatItRefusedToDo verifies that a --fix which
// correctly declines to overwrite a customized formula says so. The framework
// discards a Fix's return values and shows only the re-run, so a fix that
// skipped every file it was asked about used to be indistinguishable from one
// that repaired everything (gt-bxu).
func TestFormulaCheck_Fix_ReportsWhatItRefusedToDo(t *testing.T) {
	const target = "mol-deacon-patrol.formula.toml"
	tmpDir := driftTownRoot(t, target)

	check := NewFormulaCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Before the fix there is nothing to report about it.
	for _, d := range check.Run(ctx).Details {
		if strings.Contains(d, "--fix left") {
			t.Errorf("pre-fix details mention a fix that never ran: %q", d)
		}
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("after fix, Status = %v, want %v", result.Status, StatusWarning)
	}

	var sawSkip, sawDrift bool
	for _, d := range result.Details {
		if strings.Contains(d, "--fix left") && strings.Contains(d, target) {
			sawSkip = true
		}
		if strings.Contains(d, "never update") && strings.Contains(d, target) {
			sawDrift = true
		}
	}
	if !sawSkip {
		t.Errorf("details do not report the skipped formula: %v", result.Details)
	}
	if !sawDrift {
		t.Errorf("details do not report that re-running --fix cannot help: %v", result.Details)
	}
}

// TestFormulaCheck_Run_DriftAlongsideFixable verifies the hint when a town has
// both kinds at once. Handing back the bare "run gt doctor --fix" hint would be
// true of the fixable half and a dead end for the drifted half, which is exactly
// how drift disappears from view.
func TestFormulaCheck_Run_DriftAlongsideFixable(t *testing.T) {
	const target = "mol-deacon-patrol.formula.toml"
	tmpDir := driftTownRoot(t, target)

	// Add a plainly fixable problem on top of the drift.
	if err := os.Remove(filepath.Join(tmpDir, ".beads", "formulas", "mol-witness-patrol.formula.toml")); err != nil {
		t.Fatal(err)
	}

	result := NewFormulaCheck().Run(&CheckContext{TownRoot: tmpDir})

	if result.Status != StatusWarning {
		t.Errorf("Status = %v, want %v", result.Status, StatusWarning)
	}
	if !strings.Contains(result.FixHint, "--fix") {
		t.Errorf("FixHint = %q, want it to still offer the fix for the fixable half", result.FixHint)
	}
	if !strings.Contains(result.FixHint, "by hand") {
		t.Errorf("FixHint = %q, want it to also name the manual reconcile", result.FixHint)
	}
}

// TestFormulaCheck_Run_NamesMissingSections: doctor is where most operators
// first meet a pinned formula, and "reconcile by hand" does not say whether
// that merge is urgent. The detail must name what the executing copy is
// actually missing — the difference between "you are behind" and "you are
// missing the step that prevents re-dispatch" (gt-yubx).
func TestFormulaCheck_Run_NamesMissingSections(t *testing.T) {
	const target = "mol-deacon-patrol.formula.toml"
	tmpDir := driftTownRoot(t, target)

	result := NewFormulaCheck().Run(&CheckContext{TownRoot: tmpDir})

	joined := strings.Join(result.Details, "\n")
	if !strings.Contains(joined, "Missing ") || !strings.Contains(joined, "shipped section(s)") {
		t.Errorf("details do not name the missing sections:\n%s", joined)
	}
	// driftTownRoot replaces the whole file with a comment, so the shipped
	// steps are all absent and at least one must be named by id.
	if !strings.Contains(joined, "step ") {
		t.Errorf("details name no absent step:\n%s", joined)
	}
}

// TestFormulaCheck_Run_QuietWhenNothingMissing is the control for the test
// above: an unmodified town must produce no missing-section line at all, or the
// line proves nothing when it does appear.
func TestFormulaCheck_Run_QuietWhenNothingMissing(t *testing.T) {
	tmpDir := t.TempDir()
	if _, err := formula.ProvisionFormulas(tmpDir); err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	result := NewFormulaCheck().Run(&CheckContext{TownRoot: tmpDir})
	if joined := strings.Join(result.Details, "\n"); strings.Contains(joined, "shipped section(s)") {
		t.Errorf("clean town reports missing sections:\n%s", joined)
	}
}

func TestFormulaCheck_Fix(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision formulas
	_, err := formula.ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Delete a formula
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	formulaPath := filepath.Join(formulasDir, "mol-deacon-patrol.formula.toml")
	if err := os.Remove(formulaPath); err != nil {
		t.Fatal(err)
	}

	check := NewFormulaCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Run fix
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}

	// Verify formula was restored
	if _, err := os.Stat(formulaPath); os.IsNotExist(err) {
		t.Error("formula should have been restored")
	}

	// Re-run check - should be OK now
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("after fix, Status = %v, want %v", result.Status, StatusOK)
	}
}
