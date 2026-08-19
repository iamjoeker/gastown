package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain sets up a dedicated tmux server for the package's integration tests.
// All tests that call newTestTmux() share this isolated server, which is torn
// down after all tests complete. This prevents test sessions from appearing on
// the user's interactive tmux and avoids socket conflicts with other packages.
func TestMain(m *testing.M) {
	// Point this package at a dead Dolt port before anything else runs, so a
	// test that reaches for Dolt without arranging a server of its own cannot
	// land on the production one. See testenv.GuardProductionDolt.
	testenv.GuardProductionDolt()

	socket := fmt.Sprintf("gt-test-%d", os.Getpid())

	// Set defaultSocket so NewTmux() connects to the test server, not the
	// user's personal server or the sentinel that indicates "no town context".
	SetDefaultSocket(socket)

	// This package's tests are the ones that must exercise real delivery, so
	// authorize it — for this socket only. Naming the socket is what makes the
	// authorization safe: it covers the isolated server started below and
	// nothing else, so a test that somehow resolves the live town socket is
	// still refused. See guardTestNudge.
	if err := os.Setenv(AllowTestNudgeEnv, socket); err != nil {
		fmt.Fprintf(os.Stderr, "setenv %s: %v\n", AllowTestNudgeEnv, err)
		os.Exit(1)
	}

	// Start a sentinel session to keep the server alive for the entire test run.
	// Without this, tests that kill their last session inadvertently take down
	// the server, leaving a stale socket that prevents subsequent new-session
	// calls from restarting it (tmux sees the socket file but no listener).
	// The sentinel uses a name no individual test touches, so it outlives all
	// per-test sessions. TestMain kills the whole server at the end.
	if _, err := exec.LookPath("tmux"); err == nil {
		_ = exec.Command("tmux", "-u", "-L", socket, "new-session", "-d", "-s", "gt-test-sentinel").Run()
	}

	code := m.Run()

	// Kill the test tmux server and restore the original socket state.
	_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	SetDefaultSocket("")

	os.Exit(code)
}
