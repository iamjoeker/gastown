package nudge

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/testguard"
)

// TestStartPollerRefusedInTests pins the guard that keeps a test binary from
// spawning itself.
//
// StartPoller execs os.Executable(). Under `go test` that is the test binary, so
// a test that reaches StartPoller launches a detached copy of the whole suite
// that nothing waits on and nothing reaps. That mattered little while StartPoller
// had four callers in manager code no test drives; it matters now that it sits
// behind session.StartSession, which any test of the shared lifecycle reaches
// (gt-xmq6).
func TestStartPollerRefusedInTests(t *testing.T) {
	townRoot := t.TempDir()

	pid, err := StartPoller(townRoot, "gt-fixture")
	if err == nil {
		t.Fatal("StartPoller from a test binary = nil error; it would have exec'd the test binary as a " +
			"background poller")
	}
	if !errors.Is(err, testguard.ErrRefused) {
		t.Errorf("StartPoller error = %v, want it to wrap testguard.ErrRefused", err)
	}
	if pid != 0 {
		t.Errorf("StartPoller returned pid %d alongside a refusal", pid)
	}
	if _, statErr := os.Stat(pollerPidFile(townRoot, "gt-fixture")); !os.IsNotExist(statErr) {
		t.Errorf("refused StartPoller left a pid file behind (stat: %v); a later PollerAlive would read "+
			"it and report a drain that does not exist", statErr)
	}
}

// TestStartPollerRefusesEvenADisposableTown is the difference between this guard
// and guardTestEnqueue, which treats a town under TMPDIR as safe. It is safe for
// the queue — nothing reads a temporary town — and irrelevant here, because the
// hazard is the exec, not the destination.
func TestStartPollerRefusesEvenADisposableTown(t *testing.T) {
	townRoot := t.TempDir()
	if !testguard.Disposable(townRoot) {
		t.Fatalf("fixture town %q is not disposable; this test cannot see what it is checking", townRoot)
	}

	handled, err := guardTestStartPoller(townRoot, "gt-fixture")
	if !handled {
		t.Fatal("guardTestStartPoller let a disposable town through; a temporary directory does not make " +
			"exec'ing the test binary safe")
	}
	if err == nil {
		t.Fatal("guardTestStartPoller refused without saying why")
	}
}

// TestStartPollerGuardHonorsAuthorization is the positive control: a guard that
// refuses unconditionally would pass both tests above while being broken for the
// one caller allowed through.
func TestStartPollerGuardHonorsAuthorization(t *testing.T) {
	townRoot := filepath.Join(t.TempDir(), "town")
	t.Setenv(testguard.AllowEnv, townRoot)

	if handled, err := guardTestStartPoller(townRoot, "gt-fixture"); handled {
		t.Fatalf("guardTestStartPoller refused a town root named in %s: handled=%v err=%v",
			testguard.AllowEnv, handled, err)
	}

	// A different town root is still refused — authorization names one boundary,
	// it is not a global off switch.
	if handled, _ := guardTestStartPoller(filepath.Join(t.TempDir(), "other"), "gt-fixture"); !handled {
		t.Fatal("guardTestStartPoller let an unauthorized town root through once any authorization was set")
	}
}
