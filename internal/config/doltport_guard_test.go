package config

import (
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestResolveDoltPortIsGuardedInTests checks the guard end to end, through the
// resolver the rest of the tree actually calls.
//
// The unit tests in internal/testenv prove GuardProductionDolt writes the
// environment variables it means to. This proves the variables it writes are
// the ones ResolveDoltPort reads — the step that decides whether a test
// process ends up on the live server. An empty town root is the shape that
// used to be dangerous: nothing to resolve from, so resolution falls all the
// way through to doltserver.DefaultPort, which is production.
func TestResolveDoltPortIsGuardedInTests(t *testing.T) {
	if testenv.ProductionDoltAllowed() {
		t.Skip("process opted in to the production Dolt server")
	}
	got := ResolveDoltPort("")
	if got == testenv.ProductionDoltPort {
		t.Fatalf("ResolveDoltPort(\"\") = %d, the production port; the TestMain guard is not reaching this resolver", got)
	}
	if got != testenv.GuardedDoltPort {
		t.Errorf("ResolveDoltPort(\"\") = %d, want the guarded port %d", got, testenv.GuardedDoltPort)
	}
}
