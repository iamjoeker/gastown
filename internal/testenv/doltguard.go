package testenv

import (
	"os"
	"strconv"
	"strings"
	"testing"

	// testguard imports nothing outside the standard library, which is what
	// keeps this package callable from TestMain everywhere (see the package
	// comment in ambientenv.go). TestTestguardStaysDependencyFree holds it there.
	"github.com/steveyegge/gastown/internal/testguard"
)

// ProductionDoltPort is the port the live Gas Town Dolt server listens on.
//
// It duplicates doltserver.DefaultPort by value rather than importing it:
// testenv is called from TestMain in every package in the tree — including
// doltserver's own — so it has to stay dependency-free. TestGuardedPortsMatch
// in internal/doltserver fails if the two ever drift apart.
const ProductionDoltPort = 3307

// GuardedDoltPort is the port a guarded test process is pointed at instead of
// the production server. Nothing listens there, so a test that reaches for
// Dolt without arranging its own server fails with "connection refused"
// instead of silently creating a database on the live server.
//
// The value is deliberately unregistered and outside the ephemeral range used
// by testcontainers, so it cannot collide with a Dolt container this suite
// started for itself.
//
// "Nothing listens there" holds for every package but one. internal/doltserver
// starts servers on whatever port resolution produces, so a test there can bind
// the guarded port and a sibling test that expected connection-refused gets a
// live server instead — as a retryable "database not found" rather than a
// refusal, which is a green-looking wrong answer. Tests whose subject is
// resolution itself call WithoutDoltPortGuard for this reason.
const GuardedDoltPort = 63307

// AllowProductionDoltEnv opts a test process back in to the production Dolt
// server. It exists for the handful of checks that genuinely have to talk to
// the live server (operational smoke tests run by hand); it is never set in
// CI or in an agent sandbox.
//
// Its value must name the boundary being crossed — the production port — not a
// bare "1". That rule, and this variable's name, come from testguard, which
// holds the vocabulary shared by every guard that keeps a test binary off live
// town state; the Dolt port is the fifth such route.
const AllowProductionDoltEnv = testguard.AllowDoltEnv

// doltPortEnvVars are the variables through which a Dolt endpoint reaches
// both the in-process resolvers (config.ResolveDoltPort,
// beads.EnsureDoltConfigValue) and any bd/gt/dolt subprocess a test spawns,
// which inherits them from os.Environ.
//
// All three matter: config.ResolveDoltPort reads only GT_DOLT_PORT, while
// beads reads BEADS_DOLT_SERVER_PORT and BEADS_DOLT_PORT ahead of it. Leaving
// any one of them unset leaves a path back to the 3307 default.
var doltPortEnvVars = []string{"GT_DOLT_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_SERVER_PORT"}

// DoltPortEnvVars returns the variables GuardProductionDolt points at the
// guarded port.
//
// A helper that starts a Dolt server for a test has to overwrite every one of
// them, not just the one its own caller reads: whatever it leaves untouched
// still holds the dead guarded port, and a bd/gt subprocess reads that in
// preference to the helper's. testutil's container helpers are the callers
// this exists for; they keep their own copy of the list rather than importing
// this package (testutil is imported by the test files of packages testenv
// might one day want a helper from), and TestDoltPortEnvVarsMatchGuard there
// fails if the two drift.
func DoltPortEnvVars() []string {
	out := make([]string, len(doltPortEnvVars))
	copy(out, doltPortEnvVars)
	return out
}

// ProductionDoltAllowed reports whether this process has explicitly opted in
// to using the production Dolt server.
//
// The opt-in has to name the port it is reaching for. An operator running a
// smoke check against the real server knows which one that is; a stray export
// of "1" does not, and authorizes nothing.
func ProductionDoltAllowed() bool {
	return testguard.AuthorizedBy(AllowProductionDoltEnv, strconv.Itoa(ProductionDoltPort))
}

// GuardProductionDolt points the current process at GuardedDoltPort so its
// tests cannot write to the production Dolt server.
//
// Call it as the first statement of TestMain, before any helper that starts a
// server of its own:
//
//	func TestMain(m *testing.M) {
//	    testenv.GuardProductionDolt()
//	    os.Exit(m.Run())
//	}
//
// Why this is needed at all: every Dolt port resolver in the tree ends in the
// same fallback — doltserver.DefaultPort, which is 3307, the live server. A
// test that builds a fixture town under t.TempDir() and then calls production
// code inherits that fallback, so `go test` from a developer or agent sandbox
// creates real databases on the live server. Six were created in 34 seconds on
// 2026-08-18 that way (forkrig, pc1, pc2, pc3, testrig, testrip). Setting the
// port variables to a dead port removes the fallback: resolution finds the
// guarded port and never reaches DefaultPort.
//
// A variable already holding a deliberate non-production port is left alone,
// so a developer pointing GT_DOLT_PORT at their own scratch server keeps it.
// Helpers that start a container for the whole run —
// testutil.EnsureDoltContainerForTestMain — set these variables themselves and
// must therefore be called after this, not before.
//
// Individual tests that need a specific value still use t.Setenv, which
// restores the guarded value rather than the host's on cleanup.
func GuardProductionDolt() {
	if ProductionDoltAllowed() {
		return
	}
	guarded := strconv.Itoa(GuardedDoltPort)
	for _, name := range doltPortEnvVars {
		if needsGuarding(os.Getenv(name)) {
			_ = os.Setenv(name, guarded)
		}
	}
}

// WithoutDoltPortGuard clears the guarded port variables for the duration of
// one test and restores them when it finishes.
//
// It exists for the handful of tests whose subject *is* the unconfigured
// fallback — "with no metadata, no config.yaml and no environment, which port
// does this resolve to?" GuardProductionDolt sets those variables precisely so
// that fallback is never reached, which would otherwise leave these tests
// asserting the guarded port and no longer checking the real default at all.
//
// This is safe only because such tests resolve a port or build a connection
// string without opening one. Do not use it to let a test talk to Dolt; that
// is what AllowProductionDoltEnv is for, and it is not set in CI.
//
// Like t.Setenv, it must not be combined with t.Parallel: the port variables
// are process-wide.
func WithoutDoltPortGuard(t testing.TB) {
	t.Helper()
	for _, name := range doltPortEnvVars {
		prev, had := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unsetenv %s: %v", name, err)
		}
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(name, prev)
				return
			}
			_ = os.Unsetenv(name)
		})
	}
}

// needsGuarding reports whether an environment value leaves a path back to the
// production server.
//
// Empty qualifies: an unset variable is exactly how the resolvers end up at
// the 3307 default. So does an unparseable value, which the resolvers skip for
// the same fallback. A deliberate value for some other port does not.
func needsGuarding(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return true
	}
	return port == ProductionDoltPort
}
