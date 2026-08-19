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

// The wrappers in agent_keys.go exist only to carry guardTestNudge, so what
// these tests assert is that each one reaches it. guard behavior itself —
// socket scoping, the boolean-shaped opt-in, the log hook's empty value — is
// covered once in nudge_guard_test.go and not restated here.

// unauthorize points AllowTestNudgeEnv at a socket this package does not own,
// withdrawing the authorization TestMain grants for the duration of a test.
func unauthorize(t *testing.T) {
	t.Helper()
	t.Setenv(AllowTestNudgeEnv, "gt-agent-keys-some-other-socket")
}

func TestSendKeysToAgent_RefusedFromUnauthorizedTest(t *testing.T) {
	unauthorize(t)

	tm := NewTmuxWithSocket("gt-agent-keys-unauthorized")
	if err := tm.SendKeysToAgent("hq-mayor", "synthetic"); !errors.Is(err, ErrTestNudgeRefused) {
		t.Errorf("SendKeysToAgent() = %v, want ErrTestNudgeRefused", err)
	}
	if err := tm.SendKeysToAgentDebounced("hq-mayor", "synthetic", 10); !errors.Is(err, ErrTestNudgeRefused) {
		t.Errorf("SendKeysToAgentDebounced() = %v, want ErrTestNudgeRefused", err)
	}
}

func TestInterruptAgent_RefusedFromUnauthorizedTest(t *testing.T) {
	unauthorize(t)

	tm := NewTmuxWithSocket("gt-agent-keys-unauthorized")
	for _, key := range []string{KeyEscape, KeyCtrlC} {
		if err := tm.InterruptAgent("hq-mayor", key); !errors.Is(err, ErrTestNudgeRefused) {
			t.Errorf("InterruptAgent(%q) = %v, want ErrTestNudgeRefused", key, err)
		}
	}
}

// TestSendNotificationBanner_RefusedFromUnauthorizedTest covers the one guarded
// wrapper with a production caller that is not a shutdown path: mail routing
// prints a banner into whichever session the recipient is running in
// (internal/mail.router), so a test that exercises delivery reaches a live pane.
func TestSendNotificationBanner_RefusedFromUnauthorizedTest(t *testing.T) {
	unauthorize(t)

	tm := NewTmuxWithSocket("gt-agent-keys-unauthorized")
	err := tm.SendNotificationBanner("hq-mayor", "gastown/witness", "synthetic")
	if !errors.Is(err, ErrTestNudgeRefused) {
		t.Errorf("SendNotificationBanner() = %v, want ErrTestNudgeRefused", err)
	}
}

// TestAgentKeys_LogHookRecordsInsteadOfDelivering checks that the recording
// route works for interrupts too. Interrupts are the entries most worth having
// in the log: unlike a message they leave no text behind, so a test that fired
// one at a live agent would otherwise be invisible in the record.
func TestAgentKeys_LogHookRecordsInsteadOfDelivering(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv(TestNudgeLogEnv, logPath)

	tm := NewTmuxWithSocket("gt-agent-keys-recorded")
	if err := tm.InterruptAgent("hq-mayor", KeyCtrlC); err != nil {
		t.Fatalf("InterruptAgent() = %v, want nil (recorded)", err)
	}
	if err := tm.SendKeysToAgent("hq-mayor", "synthetic"); err != nil {
		t.Fatalf("SendKeysToAgent() = %v, want nil (recorded)", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading nudge log: %v", err)
	}
	for _, want := range []string{"nudge:hq-mayor:interrupt C-c", "nudge:hq-mayor:synthetic"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("nudge log = %q, want containing %q", data, want)
		}
	}
}

// TestSendKeysToAgent_UnauthorizedTestReachesNothing is the end-to-end proof for
// the keystroke wrapper, and it asserts the outcome rather than the error: a
// returned error alone would not distinguish "refused before delivery" from
// "delivered, then failed". Modeled on the NudgeSession equivalent in
// nudge_guard_test.go.
func TestSendKeysToAgent_UnauthorizedTestReachesNothing(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := fmt.Sprintf("gt-test-agentkeys-nodeliver-%d", time.Now().UnixNano()%100000)

	if err := tm.NewSession(sessionName, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()
	time.Sleep(200 * time.Millisecond)

	// Withdraw this package's authorization, leaving tm bound to the (now
	// unauthorized) test server.
	unauthorize(t)

	marker := fmt.Sprintf("GT_AGENTKEYS_MUST_NOT_APPEAR_%d", time.Now().UnixNano()%100000)
	if err := tm.SendKeysToAgent(sessionName, "echo "+marker); !errors.Is(err, ErrTestNudgeRefused) {
		t.Fatalf("SendKeysToAgent() = %v, want ErrTestNudgeRefused", err)
	}

	time.Sleep(300 * time.Millisecond)
	after, err := tm.CapturePane(sessionName+":0.0", 80)
	if err != nil {
		t.Fatalf("CapturePane after: %v", err)
	}
	// The marker appears if any part of delivery ran: send-keys -l puts the text
	// on the input line whether or not the Enter that follows it succeeds. The
	// pane is not compared byte-for-byte because an interactive shell repaints
	// its prompt on its own schedule.
	if strings.Contains(after, marker) {
		t.Errorf("refused send still reached the pane; marker %q found in:\n%s", marker, after)
	}
}

// TestSendKeys_StaysUnguarded records the deliberate limit of this change.
// SendKeys is how a session is bootstrapped — `gt start` launches the agent
// process with it, and this package's own fixtures build shells with it — so it
// must keep working from a test binary that has arranged nothing. Guarding it
// would refuse the act of creating the thing the guard protects. If someone
// later moves the guard down onto the primitive, this test fails and points at
// the call sites that will break.
func TestSendKeys_StaysUnguarded(t *testing.T) {
	unauthorize(t)

	tm := NewTmuxWithSocket("gt-agent-keys-nonexistent-socket")
	// The session does not exist, so tmux itself errors — but it must be tmux's
	// error, not a refusal from the guard.
	if err := tm.SendKeysRaw("gt-agent-keys-no-such-session", KeyCtrlC); errors.Is(err, ErrTestNudgeRefused) {
		t.Error("SendKeysRaw() was refused by the nudge guard; the guard belongs on the agent-facing wrappers, not the primitive")
	}
	if err := tm.SendKeys("gt-agent-keys-no-such-session", "echo hi"); errors.Is(err, ErrTestNudgeRefused) {
		t.Error("SendKeys() was refused by the nudge guard; the guard belongs on the agent-facing wrappers, not the primitive")
	}
}
