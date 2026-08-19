package doltserver

import (
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestProductionPortMatchesGuard ties testenv's copy of the production port to
// the real one.
//
// testenv.ProductionDoltPort has to be a literal rather than a reference:
// testenv is called from TestMain in every package in the tree, including this
// one, so it cannot import anything. That leaves two constants free to drift,
// and drift here is silent in the worst direction — if DefaultPort moved and
// testenv did not follow, every guarded test process would keep redirecting
// away from a port nothing uses while writing happily to the new production
// one.
func TestProductionPortMatchesGuard(t *testing.T) {
	if testenv.ProductionDoltPort != DefaultPort {
		t.Fatalf("testenv.ProductionDoltPort = %d, doltserver.DefaultPort = %d; "+
			"update testenv.ProductionDoltPort so the test guard still points at production",
			testenv.ProductionDoltPort, DefaultPort)
	}
}
