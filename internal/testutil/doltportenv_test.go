package testutil

import (
	"os"
	"slices"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestDoltPortEnvVarsMatchGuard pins testutil's copy of the Dolt port variable
// list against testenv's, which is the one GuardProductionDolt sets to the
// dead guarded port.
//
// A variable added to the guard but not here is the bug this whole file exists
// for, and it is invisible without this test: the container still starts, the
// helper still returns a port, and the affected test takes a t.Skip path while
// the package reports ok. Two integration tests in internal/doltserver sat
// skipped that way (gt-bbh0).
func TestDoltPortEnvVarsMatchGuard(t *testing.T) {
	guarded := testenv.DoltPortEnvVars()
	if !slices.Equal(doltPortEnvVars, guarded) {
		t.Errorf("testutil.doltPortEnvVars = %v, testenv.DoltPortEnvVars() = %v\n"+
			"the container helpers must set every variable the guard points at a dead port",
			doltPortEnvVars, guarded)
	}
}

// TestSetDoltPortEnvForTestCoversGuardedVars checks the t.Setenv path leaves
// no guarded variable behind, and that cleanup restores each one.
//
// The control is the surrounding process state: this test asserts the values
// differ from what it set only after the subtest has finished, so a helper
// that quietly set nothing would fail the inner check rather than pass the
// outer one by accident.
func TestSetDoltPortEnvForTestCoversGuardedVars(t *testing.T) {
	before := map[string]string{}
	for _, name := range doltPortEnvVars {
		t.Setenv(name, "1"+name[:1]) // distinct per-variable sentinel
		before[name] = os.Getenv(name)
	}

	t.Run("scoped", func(t *testing.T) {
		setDoltPortEnvForTest(t, "45678")
		for _, name := range doltPortEnvVars {
			if got := os.Getenv(name); got != "45678" {
				t.Errorf("%s = %q, want %q", name, got, "45678")
			}
		}
	})

	for _, name := range doltPortEnvVars {
		if got := os.Getenv(name); got != before[name] {
			t.Errorf("after cleanup %s = %q, want %q restored", name, got, before[name])
		}
	}
}
