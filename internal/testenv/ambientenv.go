// Package testenv isolates tests from the ambient Gas Town installation.
//
// It deliberately has no dependencies on the rest of the tree so that any
// package — including the ones internal/testutil itself depends on — can call
// it from TestMain without creating an import cycle.
package testenv

import (
	"os"
	"strings"
)

// ambientEnvPrefixes are the environment-variable namespaces through which a
// developer's live Gas Town installation reaches into a test process:
//
//	GT_*     town root, Dolt host/port, rig and role identity
//	BD_*     bd CLI overrides
//	BEADS_*  beads library overrides (BEADS_DIR, BEADS_DOLT_PORT, ...)
//
// A test that builds a fixture town under t.TempDir() and then calls into
// code that consults these variables does not test its fixture — it tests
// whatever town happens to be installed on the machine running it. The
// failure mode is silent in both directions: the test can go red because the
// live server answered, or green because the live server answered the way the
// fixture would have.
var ambientEnvPrefixes = []string{"GT_", "BD_", "BEADS_"}

// UnsetAmbientTownEnv removes the ambient Gas Town environment (see
// ambientEnvPrefixes) from the current process, so that packages whose tests
// are meant to be fixture-driven start from a clean slate.
//
// Call it at the top of TestMain, before m.Run:
//
//	func TestMain(m *testing.M) {
//	    testutil.UnsetAmbientTownEnv()
//	    os.Exit(m.Run())
//	}
//
// Individual tests that need one of these variables set their own value with
// t.Setenv, which restores the (now empty) ambient value on cleanup. Helpers
// that start a Dolt container per test — StartIsolatedDoltContainer — also use
// t.Setenv and are unaffected. Helpers that set process-wide state for the
// whole run — EnsureDoltContainerForTestMain — must be called after this.
//
// It returns the variables it removed, which is useful for a TestMain that
// wants to log what the host was contributing.
func UnsetAmbientTownEnv() []string {
	var removed []string
	for _, e := range os.Environ() {
		name, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		for _, prefix := range ambientEnvPrefixes {
			if strings.HasPrefix(name, prefix) {
				_ = os.Unsetenv(name)
				removed = append(removed, name)
				break
			}
		}
	}
	return removed
}
