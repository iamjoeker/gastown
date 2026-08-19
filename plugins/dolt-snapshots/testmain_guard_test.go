package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// This file is internal/testenv's Dolt-port guard, reimplemented for a module
// that cannot import it.
//
// plugins/dolt-snapshots is a separate Go module: run.sh builds it with
// `go build .` inside this directory and the Makefile ships it into the town
// runtime with `gt plugin sync`, so it deliberately requires exactly one
// dependency. Its go.mod does not require gastown, and the guard lives at
// gastown's internal/testenv, which is unimportable from outside that module
// even if the requirement were added. Neither option that keeps a single
// implementation is free: requiring gastown here would pull its whole
// dependency tree into a standalone plugin binary for a test-only import, and
// folding this directory into the main module would remove the standalone
// build that run.sh and plugin sync depend on.
//
// So the logic is duplicated, and the duplication is held in place rather than
// trusted: TestNestedModuleGuardsMatchThisPackage in internal/testenv fails if
// the two port constants below drift from testenv's, and
// TestEveryTestPackageGuardsProductionDolt now walks into this module, so a new
// package here that omits its own TestMain fails the gate in the main module.
//
// The one thing that is not duplicated is the variable list. See
// doltPortEnvVars.

// ProductionDoltPort is the port the live Gas Town Dolt server listens on. It
// is the value resolvePort falls back to when nothing else is configured, which
// is the whole reason this file exists.
const ProductionDoltPort = 3307

// GuardedDoltPort is the dead port a guarded test process is pointed at
// instead. Nothing listens there, so a test that reaches for Dolt without
// arranging its own server fails with "connection refused" rather than
// silently creating a database on the live server.
const GuardedDoltPort = 63307

// AllowProductionDoltEnv opts this process back in to the production server,
// for an operator running a smoke check against the real thing by hand. Its
// value must name the boundary being crossed — the production port — so a bare
// "1" authorizes nothing.
//
// The name is testguard.AllowDoltEnv's value, spelled out here because
// testguard is also unreachable across the module boundary.
const AllowProductionDoltEnv = "GT_ALLOW_TEST_DOLT"

// doltPortEnvVars are the variables an endpoint reaches this module's code
// through, and it is deliberately NOT testenv's list.
//
// resolvePort consults GT_DOLT_PORT and then DOLT_PORT. Nothing in the main
// gastown module reads DOLT_PORT, so testenv does not guard it; and this
// module spawns no bd/gt/dolt subprocess and reads no BEADS_* variable, so the
// two BEADS_ names testenv guards have no route in here. Guarding the list
// this module's own resolver reads is the property that matters — copying
// testenv's list verbatim would leave DOLT_PORT open, which is the one route
// unique to this binary.
var doltPortEnvVars = []string{"GT_DOLT_PORT", "DOLT_PORT"}

// ProductionDoltAllowed reports whether this process has explicitly opted in to
// using the production Dolt server.
func ProductionDoltAllowed() bool {
	allowed, ok := os.LookupEnv(AllowProductionDoltEnv)
	return ok && allowed == strconv.Itoa(ProductionDoltPort)
}

// GuardProductionDolt points the current process at GuardedDoltPort so its
// tests cannot write to the production Dolt server.
//
// A variable already holding a deliberate non-production port is left alone, so
// a developer pointing GT_DOLT_PORT at their own scratch server keeps it.
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
// It exists for the tests whose subject *is* the unconfigured fallback — "with
// no flag and no environment, which port does resolvePort return?" Without it
// such a test has to unset the variables itself, and the version of
// TestResolvePort that did so left them unset for every test that ran after it,
// undoing the guard for the rest of the binary.
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
// production server. Empty qualifies: an unset variable is exactly how
// resolvePort ends up at the 3307 default.
func needsGuarding(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		// resolvePort does not parse, so an unparseable value is passed
		// straight into the DSN rather than falling back. Guard it anyway:
		// a test that produced garbage here did not choose a boundary.
		return true
	}
	return port == ProductionDoltPort
}

// TestMain points this module's tests at a dead Dolt port so that nothing here
// can reach the production server on :3307.
//
// Nothing in this module opens a connection from a test today — every test is a
// pure-function test. The guard is here because nothing stopped the next one:
// main.go calls sql.Open in two places against a DSN built from resolvePort,
// whose default is the live server, and `go test ./...` from the repo root
// never reaches this module, so the failure would have been invisible to the
// suite that exists to catch it.
func TestMain(m *testing.M) {
	GuardProductionDolt()
	os.Exit(m.Run())
}
