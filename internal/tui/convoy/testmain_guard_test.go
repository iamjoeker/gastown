package convoy

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain points this package's tests at a dead Dolt port so that nothing
// here can reach the production server on :3307. See
// testenv.GuardProductionDolt for why every test package needs this.
func TestMain(m *testing.M) {
	testenv.GuardProductionDolt()
	os.Exit(m.Run())
}
