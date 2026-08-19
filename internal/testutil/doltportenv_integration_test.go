//go:build integration && !windows

package testutil

import (
	"os"
	"strconv"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestStartIsolatedDoltContainerLeavesNoGuardedPort checks the end of the chain
// this package's port handling exists for: after the helper returns, no Dolt
// port variable still holds the guarded port.
//
// The default-tag TestDoltPortEnvVarsMatchGuard pins the two lists against each
// other; this one pins the helper against a container that is actually running,
// which is what the skipping tests in internal/doltserver needed and did not
// get (gt-bbh0).
//
// The control is the assertion before the call: this package's TestMain runs
// GuardProductionDolt, so every variable must read as the guarded port first.
// Without that check a helper that set nothing at all could pass here on a
// machine whose shell happened to export the right value.
func TestStartIsolatedDoltContainerLeavesNoGuardedPort(t *testing.T) {
	guardedPort := strconv.Itoa(testenv.GuardedDoltPort)
	for _, name := range testenv.DoltPortEnvVars() {
		if got := os.Getenv(name); got != guardedPort {
			t.Fatalf("control failed: %s = %q before the helper runs, want the guarded %q — "+
				"this test cannot tell a working helper from a no-op", name, got, guardedPort)
		}
	}

	port := StartIsolatedDoltContainer(t)
	if port == "" {
		t.Fatal("StartIsolatedDoltContainer returned an empty port")
	}
	if port == guardedPort {
		t.Fatalf("container reported the guarded port %q", port)
	}

	for _, name := range testenv.DoltPortEnvVars() {
		got := os.Getenv(name)
		if got == guardedPort {
			t.Errorf("%s still holds the guarded port %q — a bd subprocess reading it "+
				"cannot reach the container on %q and will take a skip path", name, got, port)
			continue
		}
		if got != port {
			t.Errorf("%s = %q, want the container port %q", name, got, port)
		}
	}
}
