package doctor

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain strips the ambient Gas Town environment before any test runs.
//
// The doctor checks exist to inspect a town, and their tests hand them a
// fixture town under t.TempDir(). But the Dolt endpoint resolution they reach
// through (config.ResolveDoltPort) consults GT_DOLT_PORT ahead of the town's
// .dolt-data/config.yaml, so on a machine with the live town's port exported
// the checks reported on the live server rather than the fixture.
//
// TestGetServerAddr_UsesConfigYAMLPort caught this: it writes a config.yaml
// with port 13527 and asserts getServerAddr returns it, but with
// GT_DOLT_PORT=3307 in the environment it got the live port back. Tests that
// need a value still set it with t.Setenv.
func TestMain(m *testing.M) {
	testenv.UnsetAmbientTownEnv()
	os.Exit(m.Run())
}
