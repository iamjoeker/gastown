package nudge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/testguard"
)

// liveTownRoot returns a real, writable directory that the guard must treat as a
// town agents live in.
//
// It is a temporary directory like any other; what makes it "live" is that
// TMPDIR is moved somewhere else afterwards, so the directory is no longer under
// the disposable boundary. That inversion is what lets the test use a writable
// path: if the guard ever regresses, Enqueue really does write a file, and the
// assertions see it. A path that could not be written to would make a regression
// look like a refusal.
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

// unset removes an environment variable for the duration of the test. The
// t.Setenv first is what registers the restore: os.Unsetenv alone would leak the
// removal into every test that runs after this one.
func unset(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
}

func testNudge(message string) QueuedNudge {
	return QueuedNudge{Sender: "tester", Message: message, Priority: PriorityNormal}
}

// TestEnqueue_RefusesLiveTownRootFromTestBinary is the case that gt-vmj was
// closed against on the tmux route and left open on this one. `gt nudge` defaults
// to --mode=wait-idle, whose ordinary outcome for a busy agent is a queue write,
// so a test that reached deliverNudge without the log hook delivered by way of
// the queue while the tmux guard saw nothing.
//
// The assertion is that the queue is untouched, not merely that an error came
// back: a file written and then reported as an error is still a delivery, because
// the agent's hook drains what is on disk and never sees the error.
func TestEnqueue_RefusesLiveTownRootFromTestBinary(t *testing.T) {
	unset(t, testguard.LogEnv)
	unset(t, testguard.AllowEnv)
	live := liveTownRoot(t)

	const session = "gt-nudge-test-fixture"
	err := Enqueue(live, session, testNudge("synthetic"))
	// Not Fatalf: the disk assertions below are the ones that describe the
	// incident, and they must still run when the error assertion is the thing
	// that failed.
	if !errors.Is(err, testguard.ErrRefused) {
		t.Errorf("Enqueue() into a live town root = %v, want ErrRefused", err)
	}

	if n := QueueLen(live, session); n != 0 {
		t.Errorf("QueueLen after a refused Enqueue = %d, want 0", n)
	}
	if _, statErr := os.Stat(queueDir(live, session)); !os.IsNotExist(statErr) {
		t.Errorf("refused Enqueue created %q; the queue directory must not exist", queueDir(live, session))
	}
}

// TestEnqueue_AllowsDisposableTownRoot is the property that makes the guard
// structural rather than remembered. Every test in the repo that queues a nudge
// already builds its town with t.TempDir(); none of them declares anything, and
// none of them has to.
func TestEnqueue_AllowsDisposableTownRoot(t *testing.T) {
	unset(t, testguard.LogEnv)
	unset(t, testguard.AllowEnv)

	townRoot := t.TempDir()
	const session = "gt-nudge-test-fixture"
	if err := Enqueue(townRoot, session, testNudge("hello")); err != nil {
		t.Fatalf("Enqueue() into a temporary town root = %v, want nil", err)
	}
	if n := QueueLen(townRoot, session); n != 1 {
		t.Errorf("QueueLen = %d, want 1", n)
	}
}

// TestEnqueue_AuthorizationIsScopedToItsTownRoot mirrors the tmux guard's
// property: an opt-in names the boundary it covers, so it cannot green-light a
// town the test does not own. A boolean-shaped value names nothing and therefore
// authorizes nothing — the likely mistake, since "opt in" reads like a flag.
func TestEnqueue_AuthorizationIsScopedToItsTownRoot(t *testing.T) {
	unset(t, testguard.LogEnv)
	live := liveTownRoot(t)
	const session = "gt-nudge-test-fixture"

	t.Setenv(testguard.AllowEnv, live)
	if err := Enqueue(live, session, testNudge("authorized")); err != nil {
		t.Fatalf("Enqueue() into the authorized town root = %v, want nil", err)
	}

	other := filepath.Join(filepath.Dir(live), "some-other-town")
	if err := Enqueue(other, session, testNudge("not authorized")); !errors.Is(err, testguard.ErrRefused) {
		t.Errorf("Enqueue() into an unnamed town root = %v, want ErrRefused", err)
	}

	t.Setenv(testguard.AllowEnv, "1")
	if err := Enqueue(live, session, testNudge("bare flag")); !errors.Is(err, testguard.ErrRefused) {
		t.Errorf("Enqueue() with %s=1 = %v, want ErrRefused", testguard.AllowEnv, err)
	}

	// The empty town root is what workspace lookup returns when it found no
	// town. Joining it yields a relative path in whatever directory the test is
	// running from, which is a worse place to write than the live town.
	t.Setenv(testguard.AllowEnv, "")
	if err := Enqueue("", session, testNudge("no town")); !errors.Is(err, testguard.ErrRefused) {
		t.Errorf("Enqueue() with an empty town root = %v, want ErrRefused", err)
	}
}

// TestEnqueue_LogHookRecordsInsteadOfQueueing covers the recording behavior and
// the empty-value case that let the original defect through (gt-5my).
func TestEnqueue_LogHookRecordsInsteadOfQueueing(t *testing.T) {
	unset(t, testguard.AllowEnv)
	live := liveTownRoot(t)
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv(testguard.LogEnv, logPath)

	const session = "gt-nudge-test-fixture"
	if err := Enqueue(live, session, testNudge("recorded")); err != nil {
		t.Fatalf("Enqueue() under the log hook = %v, want nil", err)
	}
	if n := QueueLen(live, session); n != 0 {
		t.Errorf("QueueLen under the log hook = %d, want 0: recording must not also queue", n)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading nudge log: %v", err)
	}
	if want := "queue:" + session + ":recorded"; !strings.Contains(string(data), want) {
		t.Errorf("nudge log = %q, want containing %q", data, want)
	}

	// Presence is the signal, not the value. A blanked variable is still a test.
	t.Setenv(testguard.LogEnv, "")
	if err := Enqueue(live, session, testNudge("blanked")); err != nil {
		t.Fatalf("Enqueue() with a blank log path = %v, want nil", err)
	}
	if n := QueueLen(live, session); n != 0 {
		t.Errorf("QueueLen with a blank log path = %d, want 0", n)
	}
}
