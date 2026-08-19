//go:build integration

package cmd

import (
	"flag"
	"fmt"
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

	// Force sequential test execution to avoid bd file locks on Windows.
	_ = flag.Set("test.parallel", "1")
	flag.Parse()

	// Start an ephemeral Dolt container for this package's integration tests.
	// Tests like TestAgentWorktreesStayClean and TestBeadsRoutingFromTownRoot
	// spawn gt/bd subprocesses that create databases (e.g., "tr", "hq").
	// By routing to an isolated container (via GT_DOLT_PORT), those databases
	// are destroyed when the container is terminated at cleanup —
	// preventing orphan accumulation in the shared production Dolt data dir.
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "integration TestMain: dolt setup: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	// Clean up the shared Dolt container.
	testutil.TerminateDoltContainer()
	os.Exit(code)
}
