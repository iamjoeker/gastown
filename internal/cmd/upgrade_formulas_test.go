package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/formula"
)

// driftedFormulaTown provisions a town whose named formula is locally modified
// AND shadowing a newer shipped default: the file no longer matches what gt
// installed, and the install record points at a hash the embedded corpus has
// since moved past. `gt upgrade` will correctly refuse to overwrite it, and that
// refusal is permanent.
func driftedFormulaTown(t *testing.T, name string) string {
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

// TestUpgradeFormulas_ReportsDrift covers the upgrade-side half of the same
// defect the doctor check had: "N skipped (modified)" reads like a no-op on a
// file nobody needed to touch. For the drifted subset it is not — the shipped
// default moved and no future `gt upgrade` will deliver it either (gt-bxu).
func TestUpgradeFormulas_ReportsDrift(t *testing.T) {
	const target = "mol-deacon-patrol.formula.toml"

	t.Run("live run names the drifted formula", func(t *testing.T) {
		tmpDir := driftedFormulaTown(t, target)

		var result upgradeResult
		out := captureStdout(t, func() { result = upgradeFormulas(tmpDir) })

		if result.skipped != 1 {
			t.Errorf("skipped = %d, want 1", result.skipped)
		}
		if !strings.Contains(out, target) {
			t.Errorf("output does not name the drifted formula:\n%s", out)
		}
		if !strings.Contains(out, "shadow a newer shipped default") {
			t.Errorf("output does not report the drift:\n%s", out)
		}
		if !strings.Contains(strings.Join(result.details, "; "), "newer shipped default") {
			t.Errorf("details = %v, want the drift recorded", result.details)
		}

		// The refusal must be correct as well as audible.
		content, err := os.ReadFile(filepath.Join(tmpDir, ".beads", "formulas", target))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "# local edit\n" {
			t.Error("upgrade overwrote a locally modified formula")
		}
	})

	// Drift is the only thing wrong here, so there is nothing upgrade would
	// change — which the dry run used to render as a plain "up-to-date" tick.
	t.Run("dry run names the drifted formula", func(t *testing.T) {
		tmpDir := driftedFormulaTown(t, target)

		upgradeDryRun = true
		defer func() { upgradeDryRun = false }()

		out := captureStdout(t, func() { upgradeFormulas(tmpDir) })

		if !strings.Contains(out, "shadow a newer shipped default") {
			t.Errorf("dry-run output does not report the drift:\n%s", out)
		}
		if !strings.Contains(out, target) {
			t.Errorf("dry-run output does not name the drifted formula:\n%s", out)
		}
	})

	// Control: an untouched town must still get its clean bill of health.
	t.Run("dry run on a clean town reports up-to-date", func(t *testing.T) {
		tmpDir := t.TempDir()
		if _, err := formula.ProvisionFormulas(tmpDir); err != nil {
			t.Fatalf("ProvisionFormulas() error: %v", err)
		}

		upgradeDryRun = true
		defer func() { upgradeDryRun = false }()

		out := captureStdout(t, func() { upgradeFormulas(tmpDir) })

		if !strings.Contains(out, "up-to-date") || strings.Contains(out, "cannot reach") {
			t.Errorf("clean town did not report up-to-date:\n%s", out)
		}
	})

	// Control: a local edit made against the CURRENT embedded copy is skipped
	// too, but nothing is being shadowed, so there is no drift to report.
	t.Run("modified without drift stays quiet", func(t *testing.T) {
		tmpDir := t.TempDir()
		if _, err := formula.ProvisionFormulas(tmpDir); err != nil {
			t.Fatalf("ProvisionFormulas() error: %v", err)
		}
		path := filepath.Join(tmpDir, ".beads", "formulas", target)
		if err := os.WriteFile(path, []byte("# local edit\n"), 0644); err != nil {
			t.Fatal(err)
		}

		var result upgradeResult
		out := captureStdout(t, func() { result = upgradeFormulas(tmpDir) })

		if result.skipped != 1 {
			t.Errorf("skipped = %d, want 1", result.skipped)
		}
		if strings.Contains(out, "shadow a newer shipped default") {
			t.Errorf("reported drift for a formula that is not drifted:\n%s", out)
		}
	})
}
