package polecat

import (
	"strings"
	"testing"
	"time"
)

// TestSuspectStallOutranksSessionBusy is the ordering test, and the ordering is
// the whole feature.
//
// A wedged agent renders the busy marker exactly like a generating one — the
// marker says a turn is OPEN, not that anything is happening inside it — so
// SessionBusy is true for every case SUSPECT_STALL exists to catch. Checked in
// the other order, this verdict is unreachable and the caller gets WORKING /
// leave-alone, which is the verdict that let a stuck agent sit unnoticed
// (gt-y39t).
func TestSuspectStallOutranksSessionBusy(t *testing.T) {
	t.Parallel()

	d := DecideWorkstate(WorkstateInput{
		State:               StateWorking,
		SessionBusy:         true,
		SessionSuspectStall: true,
		SessionStallWindow:  90 * time.Second,
	})

	if d.Verdict != WorkstateVerdictSuspectStall {
		t.Fatalf("verdict = %q, want %q — a busy marker is present in every stall this catches",
			d.Verdict, WorkstateVerdictSuspectStall)
	}
	if d.Reason != WorkstateReasonSessionSuspectStall {
		t.Errorf("reason = %q, want %q", d.Reason, WorkstateReasonSessionSuspectStall)
	}
	if !d.CountsTowardCapacity {
		t.Error("a suspected stall released the polecat's capacity slot; it is still holding it")
	}
	if len(d.Blockers) != 1 || !strings.Contains(d.Blockers[0], "1m30s") {
		// The window is the difference between a measurement and a snap
		// judgement, and a snap judgement on this signal is wrong most of the
		// time. A reader who cannot see the window cannot tell which they got.
		t.Errorf("blockers = %v, want one naming the sampling window", d.Blockers)
	}
}

// TestSuspectStallLosesToLoggedOut keeps the two pane-derived verdicts ordered
// by remedy. A logged-out agent consumes no tokens either, so it can satisfy
// both predicates at once — and NEEDS_LOGIN names the actual remedy (a human at
// a browser) while SUSPECT_STALL only says to go look.
func TestSuspectStallLosesToLoggedOut(t *testing.T) {
	t.Parallel()

	d := DecideWorkstate(WorkstateInput{
		State:               StateWorking,
		SessionLoggedOut:    true,
		SessionSuspectStall: true,
		SessionStallWindow:  90 * time.Second,
	})

	if d.Verdict != WorkstateVerdictNeedsLogin {
		t.Fatalf("verdict = %q, want %q", d.Verdict, WorkstateVerdictNeedsLogin)
	}
}

// TestUnsampledCallerGetsOldBehaviour pins the guarantee that makes this safe to
// land: a caller that did not pay for the two-sample measurement leaves
// SessionSuspectStall false and gets exactly the verdict it got before.
func TestUnsampledCallerGetsOldBehaviour(t *testing.T) {
	t.Parallel()

	d := DecideWorkstate(WorkstateInput{State: StateWorking, SessionBusy: true})
	if d.Verdict != WorkstateVerdictWorking || d.Reason != WorkstateReasonSessionBusy {
		t.Fatalf("verdict/reason = %q/%q, want %q/%q",
			d.Verdict, d.Reason, WorkstateVerdictWorking, WorkstateReasonSessionBusy)
	}
}
