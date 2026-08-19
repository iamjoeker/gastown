package mail

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
	"github.com/steveyegge/gastown/internal/testutil"
)

func TestMain(m *testing.M) {
	// Point this package at a dead Dolt port before anything else runs, so a
	// test that reaches for Dolt without arranging a server of its own cannot
	// land on the production one. See testenv.GuardProductionDolt.
	testenv.GuardProductionDolt()

	code := m.Run()
	testutil.TerminateDoltContainer()
	os.Exit(code)
}
