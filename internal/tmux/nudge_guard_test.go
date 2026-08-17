package tmux

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGuardTestNudge_RefusesWithoutAuthorization covers the case that produced
// the incident: a test binary reaches a delivery path having arranged nothing.
func TestGuardTestNudge_RefusesWithoutAuthorization(t *testing.T) {
	t.Setenv(AllowTestNudgeEnv, "")
	if err := os.Unsetenv(AllowTestNudgeEnv); err != nil {
		t.Fatalf("unset %s: %v", AllowTestNudgeEnv, err)
	}

	tm := NewTmuxWithSocket("gt-guard-unauthorized")
	handled, err := tm.guardTestNudge("hq-mayor", "synthetic")
	if !handled {
		t.Fatal("guardTestNudge() handled = false, want true: an unauthorized test must not reach delivery")
	}
	if !errors.Is(err, ErrTestNudgeRefused) {
		t.Fatalf("guardTestNudge() err = %v, want ErrTestNudgeRefused", err)
	}
}

// TestGuardTestNudge_AuthorizationIsScopedToItsSocket is the property that makes
// the authorization structural rather than a remembered flag. A test may only
// authorize the socket it owns, so a stale or careless opt-in cannot green-light
// delivery on the live town socket.
func TestGuardTestNudge_AuthorizationIsScopedToItsSocket(t *testing.T) {
	const owned = "gt-guard-owned-socket"
	t.Setenv(AllowTestNudgeEnv, owned)

	if handled, err := NewTmuxWithSocket(owned).guardTestNudge("gt-test-session", "msg"); handled || err != nil {
		t.Fatalf("guardTestNudge() on the authorized socket = (%v, %v), want (false, nil)", handled, err)
	}

	// The town socket is not the socket the test named, so it stays refused even
	// though an opt-in is present in the environment.
	handled, err := NewTmuxWithSocket("gt-6be2f4").guardTestNudge("hq-mayor", "msg")
	if !handled || !errors.Is(err, ErrTestNudgeRefused) {
		t.Fatalf("guardTestNudge() on an unnamed socket = (%v, %v), want (true, ErrTestNudgeRefused)", handled, err)
	}

	// A boolean-shaped opt-in authorizes nothing, because it names no socket.
	// This is the likely mistake: someone reads "opt in" and sets it to 1.
	t.Setenv(AllowTestNudgeEnv, "1")
	for _, socket := range []string{"", owned} {
		handled, err := NewTmuxWithSocket(socket).guardTestNudge("hq-mayor", "msg")
		if !handled || !errors.Is(err, ErrTestNudgeRefused) {
			t.Errorf("guardTestNudge() with %s=1 on socket %s = (%v, %v), want (true, ErrTestNudgeRefused)",
				AllowTestNudgeEnv, describeSocket(socket), handled, err)
		}
	}

	// The empty socket name is the user's own default tmux server, where every
	// real agent session the developer has open lives. It is never a correct
	// target for a test, so it must not be authorizable at all.
	t.Setenv(AllowTestNudgeEnv, "")
	handled, err = NewTmuxWithSocket("").guardTestNudge("hq-mayor", "msg")
	if !handled || !errors.Is(err, ErrTestNudgeRefused) {
		t.Fatalf("guardTestNudge() on the default server = (%v, %v), want (true, ErrTestNudgeRefused)", handled, err)
	}
}

// TestGuardTestNudge_LogHookRecordsInsteadOfDelivering checks both the recording
// behavior and the empty-value case that let the original defect through.
func TestGuardTestNudge_LogHookRecordsInsteadOfDelivering(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv(TestNudgeLogEnv, logPath)
	// Authorization must not override the log hook: a test that asked for
	// recording gets recording.
	t.Setenv(AllowTestNudgeEnv, "gt-guard-recorded")

	tm := NewTmuxWithSocket("gt-guard-recorded")
	handled, err := tm.guardTestNudge("hq-mayor", "synthetic")
	if !handled || err != nil {
		t.Fatalf("guardTestNudge() = (%v, %v), want (true, nil)", handled, err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading nudge log: %v", err)
	}
	if want := "nudge:hq-mayor:synthetic"; !strings.Contains(string(data), want) {
		t.Fatalf("nudge log = %q, want containing %q", data, want)
	}

	// Presence is the signal, not the value. A blanked variable is still a test.
	t.Setenv(TestNudgeLogEnv, "")
	handled, err = tm.guardTestNudge("hq-mayor", "synthetic")
	if !handled || err != nil {
		t.Fatalf("guardTestNudge() with a blank log path = (%v, %v), want (true, nil)", handled, err)
	}
}

// TestNudgeSessionWithOpts_UnauthorizedTestReachesNothing is the end-to-end
// proof, and it asserts the outcome that matters. An error return alone would
// not distinguish "refused before delivery" from "delivered, then failed": this
// nudges a real session on a real tmux server and requires the pane to be
// untouched afterwards.
func TestNudgeSessionWithOpts_UnauthorizedTestReachesNothing(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := fmt.Sprintf("gt-test-guard-nodeliver-%d", time.Now().UnixNano()%100000)

	if err := tm.NewSession(sessionName, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()
	time.Sleep(200 * time.Millisecond)

	// Withdraw this package's authorization by pointing it at some other socket,
	// leaving tm bound to the (now unauthorized) test server.
	t.Setenv(AllowTestNudgeEnv, "gt-guard-some-other-socket")

	marker := fmt.Sprintf("GT_GUARD_MUST_NOT_APPEAR_%d", time.Now().UnixNano()%100000)
	if err := tm.NudgeSession(sessionName, "echo "+marker); !errors.Is(err, ErrTestNudgeRefused) {
		t.Fatalf("NudgeSession() = %v, want ErrTestNudgeRefused", err)
	}

	time.Sleep(300 * time.Millisecond)
	after, err := tm.CapturePane(sessionName+":0.0", 80)
	if err != nil {
		t.Fatalf("CapturePane after: %v", err)
	}
	// The marker is the assertion. It appears if any part of delivery ran: the
	// text lands on the input line under send-keys -l whether or not the Enter
	// that follows it succeeds. The pane is not compared byte-for-byte because an
	// interactive shell repaints its prompt on its own schedule, which would make
	// the test flaky without testing anything about nudges.
	if strings.Contains(after, marker) {
		t.Errorf("refused nudge still reached the pane; marker %q found in:\n%s", marker, after)
	}
}
