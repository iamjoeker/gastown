package formula

import (
	"strings"
	"testing"
)

// The refinery patrol is the only thing that runs on the merge queue's cadence,
// so it is where the ledger reconcile has to live: a command nobody runs does
// not surface a bead whose merge landed and whose close did not (gt-602).
func TestRefineryPatrolReconcilesTheLedger(t *testing.T) {
	f := loadRefineryPatrolFormula(t)
	cleanup := requireFormulaStep(t, f, "patrol-cleanup")

	if !strings.Contains(cleanup.Description, "gt mq reconcile <rig>") {
		t.Fatal("patrol-cleanup does not run gt mq reconcile; merged-but-open beads stay invisible")
	}

	// The report is evidence, not a verdict. Bulk-closing it would manufacture
	// exactly the false completions the ledger guards refuse.
	if !strings.Contains(cleanup.Description, "Do NOT bulk-close") {
		t.Error("patrol-cleanup must forbid bulk-closing the reconcile report")
	}
	if !strings.Contains(cleanup.Description, "bd show <id>") {
		t.Error("patrol-cleanup must require reading each bead against its commits before closing")
	}
}
