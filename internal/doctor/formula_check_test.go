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
	// pointing at it would send the operator in a circle.
	if !strings.Contains(result.FixHint, "by hand") {
		t.Errorf("FixHint = %q, want a manual-reconciliation hint", result.FixHint)
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
