package townlog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/testguard"
)

// liveTownRoot returns a real, writable town root that the guard must treat as
// live.
//
// It is a temporary directory like any other; what makes it live is that TMPDIR
// is moved elsewhere afterwards, so the directory is no longer under the
// disposable boundary. Using a writable path is the point: if the guard ever
// regresses the log file really is written, and the assertions see it. An
// unwritable path would make a regression look like a refusal.
func liveTownRoot(t *testing.T) string {
	t.Helper()
	live := t.TempDir()
	elsewhere := t.TempDir()
	t.Setenv("TMPDIR", elsewhere)
	if got := os.TempDir(); got != elsewhere {
		t.Skipf("os.TempDir() = %q, not the TMPDIR just set; cannot stage a live town root on this platform", got)
	}
	return live
}

func unset(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
}

// TestLogEvent_RefusesLiveTownFromTestBinary covers the route the gt-vmj count
// was read out of. The bead's headline evidence was 252 lines in the live
// logs/town.log naming six production agents, at 42 apiece; those lines are
// written here, by cmd.runNudge, after a nudge that the test hook had already
// intercepted. Delivery was stopped and the log still said it happened.
func TestLogEvent_RefusesLiveTownFromTestBinary(t *testing.T) {
	unset(t, testguard.AllowEnv)
	// The nudge hook is deliberately left as-is: it does not authorize anything
	// here, and a test that sets it must still not stamp the live town log —
	// that combination is exactly what produced the reported entries.
	live := liveTownRoot(t)

	err := NewLogger(live).Log(EventNudge, "hq-mayor", "test")
	if !errors.Is(err, testguard.ErrRefused) {
		t.Errorf("Log() into a live town = %v, want ErrRefused", err)
	}

	if _, statErr := os.Stat(logPath(live)); !os.IsNotExist(statErr) {
		data, _ := os.ReadFile(logPath(live))
		t.Errorf("refused Log() wrote %s:\n%s", logPath(live), data)
	}
}

// TestLogEvent_AllowsDisposableTown is what keeps the guard from costing anything:
// the package's own tests, and every other test that builds a town with
// t.TempDir(), declare nothing and keep working.
func TestLogEvent_AllowsDisposableTown(t *testing.T) {
	unset(t, testguard.AllowEnv)

	townRoot := t.TempDir()
	if err := NewLogger(townRoot).Log(EventSpawn, "gastown/polecats/fixture", "gt-fixture"); err != nil {
		t.Fatalf("Log() into a temporary town = %v, want nil", err)
	}
	if _, err := os.Stat(logPath(townRoot)); err != nil {
		t.Fatalf("Log() into a temporary town wrote nothing: %v", err)
	}
}

// TestLogEvent_AuthorizationIsScopedToItsTown mirrors the other two routes: an
// opt-in names the town it covers, and a boolean-shaped value names nothing.
func TestLogEvent_AuthorizationIsScopedToItsTown(t *testing.T) {
	live := liveTownRoot(t)
	other := filepath.Join(filepath.Dir(live), "some-other-town")

	t.Setenv(testguard.AllowEnv, live)
	if err := NewLogger(live).Log(EventNudge, "gt-nudge-test-fixture", "authorized"); err != nil {
		t.Fatalf("Log() into the authorized town = %v, want nil", err)
	}

	if err := NewLogger(other).Log(EventNudge, "gt-nudge-test-fixture", "not authorized"); !errors.Is(err, testguard.ErrRefused) {
		t.Errorf("Log() into an unnamed town = %v, want ErrRefused", err)
	}

	t.Setenv(testguard.AllowEnv, "1")
	if err := NewLogger(live).Log(EventNudge, "gt-nudge-test-fixture", "bare flag"); !errors.Is(err, testguard.ErrRefused) {
		t.Errorf("Log() with %s=1 = %v, want ErrRefused", testguard.AllowEnv, err)
	}
}
