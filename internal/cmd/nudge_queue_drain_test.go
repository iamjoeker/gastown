package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/tmux"
)

// gt-1t0v instance 10: `gt nudge --mode=queue` against a PARKED agent printed
// "✓ Nudged <target> (queue)" and exited 0, and nothing happened — no interrupt
// after 12s, none after 72s. --mode=immediate to the same target resumed it in
// 20s.
//
// The mechanism is in the command's own help: queue mode means "agent picks up
// via hook at next turn boundary", and a parked agent HAS no next turn boundary
// — that is what parked means. So a queued nudge to a parked agent with no
// poller is not unlucky and not racy, it is structurally undeliverable, and the
// command reported success anyway.
//
// These tests pin the decision, not the plumbing: after Enqueue succeeds,
// something must be established that will drain the queue, and if nothing can
// be, the command must exit non-zero rather than print a check mark.

// stubQueueDrain replaces ensureQueueDrain's two side-effecting calls for the
// duration of a test and reports how many times a poller start was attempted.
func stubQueueDrain(t *testing.T, alive bool, startErr error) *int {
	t.Helper()
	origAlive := queueDrainPollerAlive
	origStart := queueDrainStartPoller
	t.Cleanup(func() {
		queueDrainPollerAlive = origAlive
		queueDrainStartPoller = origStart
	})

	starts := 0
	queueDrainPollerAlive = func(townRoot, session string) (int, bool) {
		if alive {
			return 4242, true
		}
		return 0, false
	}
	queueDrainStartPoller = func(townRoot, session string) (int, error) {
		starts++
		if startErr != nil {
			return 0, startErr
		}
		return 4243, nil
	}
	return &starts
}

func TestEnsureQueueDrain_ExistingPollerIsAcceptedWithoutStartingAnother(t *testing.T) {
	starts := stubQueueDrain(t, true, nil)

	if err := ensureQueueDrain("/town", "gt-fixture", false); err != nil {
		t.Fatalf("ensureQueueDrain with a live poller = %v, want nil", err)
	}
	if *starts != 0 {
		t.Errorf("started %d poller(s) when one was already alive, want 0", *starts)
	}
}

func TestEnsureQueueDrain_StartsPollerWhenNoneIsRunning(t *testing.T) {
	starts := stubQueueDrain(t, false, nil)

	if err := ensureQueueDrain("/town", "gt-fixture", false); err != nil {
		t.Fatalf("ensureQueueDrain = %v, want nil once a poller starts", err)
	}
	if *starts != 1 {
		t.Errorf("started %d poller(s), want exactly 1", *starts)
	}
}

// The load-bearing case. Before the fix there was no failure mode at all here:
// Enqueue returned nil, deliverNudge returned nil, and runNudge printed the
// check mark.
func TestEnsureQueueDrain_ErrorsWhenNoDrainCanBeEstablished(t *testing.T) {
	stubQueueDrain(t, false, errors.New("no such session"))

	err := ensureQueueDrain("/town", "gt-fixture", false)
	if err == nil {
		t.Fatal("ensureQueueDrain with no poller and no way to start one = nil; a queued nudge nothing drains must not report success")
	}

	// The error has to name the case, not just fail (gt-1t0v ask 3). Check for
	// the two things a caller acts on: that the payload survives, and what to do
	// instead. Match on content phrases rather than headline wording — those are
	// the parts that survive rephrasing.
	for _, want := range []string{"still in the queue", "--mode=immediate", "turn boundary"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q; got: %v", want, err)
		}
	}
	// The underlying failure must not be swallowed.
	if !strings.Contains(err.Error(), "no such session") {
		t.Errorf("error drops the poller-start cause; got: %v", err)
	}
}

// ACP sessions have no tmux pane to inject into; their queue is drained by the
// ACP propulsion loop, which is not turn-boundary-bound. Starting a tmux poller
// for one would be both useless and wrong.
func TestEnsureQueueDrain_ACPSessionNeedsNoPoller(t *testing.T) {
	starts := stubQueueDrain(t, false, errors.New("would have failed"))

	if err := ensureQueueDrain("/town", "acp-fixture", true); err != nil {
		t.Fatalf("ensureQueueDrain for an ACP session = %v, want nil", err)
	}
	if *starts != 0 {
		t.Errorf("started %d poller(s) for an ACP session, want 0", *starts)
	}
}

// End-to-end through deliverNudge: the seam that produced the false check mark.
// The queue write really happens (a disposable town root is what
// nudge.guardTestEnqueue keys on), so a regression that drops the drain check
// fails here with the file on disk and a nil error — exactly the observed bug.
func TestDeliverNudge_QueueModeFailsWhenNothingWillDrainTheQueue(t *testing.T) {
	origMode := nudgeModeFlag
	origPriority := nudgePriorityFlag
	t.Cleanup(func() {
		nudgeModeFlag = origMode
		nudgePriorityFlag = origPriority
	})

	// No test hook and no tmux allowance: deliverNudge must run its real queue
	// route, not the recording shortcut.
	t.Setenv(testNudgeHookEnv, "")
	if err := os.Unsetenv(testNudgeHookEnv); err != nil {
		t.Fatalf("unset %s: %v", testNudgeHookEnv, err)
	}
	t.Setenv(tmux.AllowTestNudgeEnv, "")
	if err := os.Unsetenv(tmux.AllowTestNudgeEnv); err != nil {
		t.Fatalf("unset %s: %v", tmux.AllowTestNudgeEnv, err)
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("creating town marker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("writing town marker: %v", err)
	}
	t.Chdir(townRoot)

	stubQueueDrain(t, false, errors.New("session gone"))

	nudgeModeFlag = NudgeModeQueue
	nudgePriorityFlag = nudge.PriorityNormal

	const sessionName = "gt-nudge-test-fixture"
	err := deliverNudge(nil, sessionName, "synthetic", "tester")
	if err == nil {
		t.Fatal("deliverNudge(--mode=queue) with no drain = nil; runNudge would print \"✓ Nudged ... (queue)\" and exit 0 over an undeliverable nudge")
	}

	// The payload is undelivered, NOT lost — the error says so, and the queue
	// must actually still hold it.
	if n := nudge.QueueLen(townRoot, sessionName); n != 1 {
		t.Errorf("QueueLen = %d, want 1: the error promises the message is still queued", n)
	}
}

// The success path must still be a success: a target that already has a live
// poller is drainable, and queue mode has to stay usable for it.
func TestDeliverNudge_QueueModeSucceedsWhenAPollerIsAlive(t *testing.T) {
	origMode := nudgeModeFlag
	origPriority := nudgePriorityFlag
	t.Cleanup(func() {
		nudgeModeFlag = origMode
		nudgePriorityFlag = origPriority
	})

	t.Setenv(testNudgeHookEnv, "")
	if err := os.Unsetenv(testNudgeHookEnv); err != nil {
		t.Fatalf("unset %s: %v", testNudgeHookEnv, err)
	}
	t.Setenv(tmux.AllowTestNudgeEnv, "")
	if err := os.Unsetenv(tmux.AllowTestNudgeEnv); err != nil {
		t.Fatalf("unset %s: %v", tmux.AllowTestNudgeEnv, err)
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("creating town marker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("writing town marker: %v", err)
	}
	t.Chdir(townRoot)

	stubQueueDrain(t, true, nil)

	nudgeModeFlag = NudgeModeQueue
	nudgePriorityFlag = nudge.PriorityNormal

	const sessionName = "gt-nudge-test-fixture"
	if err := deliverNudge(nil, sessionName, "synthetic", "tester"); err != nil {
		t.Fatalf("deliverNudge(--mode=queue) to a drainable target = %v, want nil", err)
	}
	if n := nudge.QueueLen(townRoot, sessionName); n != 1 {
		t.Errorf("QueueLen = %d, want 1", n)
	}
}

// The help text is the surface that told the witness queue mode was safe here.
// It must no longer promise delivery it does not check, and must say what
// happens when no drain exists.
func TestNudgeHelpDocumentsQueueDrainRequirement(t *testing.T) {
	for _, want := range []string{"parked", "nudge-poller", "exits non-zero"} {
		if !strings.Contains(nudgeCmd.Long, want) {
			t.Errorf("gt nudge help does not mention %q; queue mode's precondition has to be visible where the mode is chosen:\n%s", want, nudgeCmd.Long)
		}
	}
}
