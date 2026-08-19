package testutil

import (
	"os"
	"testing"
)

// doltPortEnvVars are the variables that route a Dolt endpoint, both for the
// in-process resolvers and for any bd/gt/dolt subprocess a test spawns.
//
// It duplicates testenv.doltPortEnvVars by value rather than importing that
// package: testutil is imported by the test files of the packages it helps, so
// every import it takes on is a cycle waiting for one of those packages to
// want a helper back (see importcycle_test.go). TestDoltPortEnvVarsMatchGuard
// fails if the two lists ever drift apart — that test file may import testenv
// freely, since test files are compiled into this package's own binary.
//
// Setting all of them is not belt-and-braces. testenv.GuardProductionDolt sets
// every one to the dead guarded port, so a container helper that publishes its
// port through only one leaves the others still pointing at the guard: bd
// reads BEADS_DOLT_SERVER_PORT and BEADS_DOLT_PORT ahead of GT_DOLT_PORT and
// fails to reach a container that is running perfectly well.
var doltPortEnvVars = []string{"GT_DOLT_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_SERVER_PORT"}

// setDoltPortEnv points every Dolt port variable at port for the rest of the
// process. Used by the shared-container helpers, which run from TestMain where
// there is no *testing.T to scope the change to.
func setDoltPortEnv(port string) {
	for _, name := range doltPortEnvVars {
		os.Setenv(name, port) //nolint:tenv // intentional process-wide env: called from TestMain
	}
}

// setDoltPortEnvForTest points every Dolt port variable at port for the
// duration of t, restoring the guarded values when it finishes.
func setDoltPortEnvForTest(t *testing.T, port string) {
	t.Helper()
	for _, name := range doltPortEnvVars {
		t.Setenv(name, port)
	}
}
