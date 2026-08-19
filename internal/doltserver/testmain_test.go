package doltserver

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain strips the ambient Gas Town environment before any test runs.
//
// Almost every test in this package builds a fixture town under t.TempDir()
// and then calls production code that resolves the Dolt endpoint through
// config.ResolveDoltPort / ResolveDoltHost. Those resolvers consult
// GT_DOLT_PORT and GT_DOLT_HOST *before* the town's own config.yaml, so on a
// developer or agent machine — where the shell exports GT_DOLT_PORT=3307 for
// the live town server — the fixture's port was ignored and the tests talked
// to the live server instead.
//
// TestWaitForCatalog_NoServer is the clearest casualty: it writes a
// config.yaml pointing at an unused port precisely so the connection is
// refused, then asserts the error is non-retryable. With GT_DOLT_PORT=3307
// leaking in, the live server answered and returned a retryable "database not
// found" instead. Tests that need a specific value still set it with
// t.Setenv, which now restores to unset rather than to the host's value.
func TestMain(m *testing.M) {
	testenv.UnsetAmbientTownEnv()

	// After the strip, not before: UnsetAmbientTownEnv removes GT_* and
	// BEADS_* wholesale, which would take the guarded port with it and leave
	// resolution falling through to the 3307 default again.
	testenv.GuardProductionDolt()

	os.Exit(m.Run())
}
