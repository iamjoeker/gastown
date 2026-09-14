package polecat

import (
	"strings"
	"testing"
)

// TestParkedMismatchCatchesUnmeasuredNotIdle is the core case gt-3veeb is
// about: the bead says working and nothing else contradicts it, but the pane
// was read and positively found parked — no turn in flight, no auth wall.
// Before this, the not-idle road never consulted the pane at all and reported
// WORKING/leave-alone, which is what let a polecat sit on an unanswered
// interactive menu for 42+ minutes (gastown/brahmin) and the Mayor for
// ~1h50m (hq-79f59) with every automated surface reporting healthy.
func TestParkedMismatchCatchesUnmeasuredNotIdle(t *testing.T) {
	t.Parallel()

	d := DecideWorkstate(WorkstateInput{
		State:         StateWorking,
		SessionParked: true,
	})

	if d.Verdict != WorkstateVerdictParkedMismatch {
		t.Fatalf("verdict = %q, want %q — the bead's not-idle claim went unchecked against the pane",
			d.Verdict, WorkstateVerdictParkedMismatch)
	}
	if d.Reason != WorkstateReasonSessionParkedMismatch {
		t.Errorf("reason = %q, want %q", d.Reason, WorkstateReasonSessionParkedMismatch)
	}
	if !d.CountsTowardCapacity {
		t.Error("a parked-mismatch polecat released its capacity slot; it is still holding it")
	}
	if d.NeedsRecovery {
		t.Error("NeedsRecovery should be false: nothing is proven lost, only unanswered")
	}
	if len(d.Blockers) != 1 || !strings.Contains(d.Blockers[0], "parked") {
		t.Errorf("blockers = %v, want one naming the parked pane", d.Blockers)
	}
}

// TestSessionBusyOutranksParkedMismatch: SessionBusy and SessionParked cannot
// both be positive evidence about the same live capture (busy means a turn is
// open; parked means one is not), but a caller could still set both by
// mistake, and the busier signal must win — it is checked first in
// DecideWorkstate and is a genuine generating agent.
func TestSessionBusyOutranksParkedMismatch(t *testing.T) {
	t.Parallel()

	d := DecideWorkstate(WorkstateInput{
		State:         StateWorking,
		SessionBusy:   true,
		SessionParked: true,
	})

	if d.Verdict != WorkstateVerdictWorking || d.Reason != WorkstateReasonSessionBusy {
		t.Fatalf("verdict/reason = %q/%q, want %q/%q — SessionBusy is positive evidence of a live turn",
			d.Verdict, d.Reason, WorkstateVerdictWorking, WorkstateReasonSessionBusy)
	}
}

// TestLoggedOutOutranksParkedMismatch: an auth wall names an actual remedy (a
// human running /login); parked-mismatch only says "go look". The auth wall
// must win the same way it wins over SUSPECT_STALL.
func TestLoggedOutOutranksParkedMismatch(t *testing.T) {
	t.Parallel()

	d := DecideWorkstate(WorkstateInput{
		State:            StateWorking,
		SessionLoggedOut: true,
		SessionParked:    true,
	})

	if d.Verdict != WorkstateVerdictNeedsLogin {
		t.Fatalf("verdict = %q, want %q", d.Verdict, WorkstateVerdictNeedsLogin)
	}
}

// TestParkedMismatchRequiresStateWorking: SessionParked alone, without the
// bead claiming State==StateWorking, must not fire this verdict — an idle or
// done polecat sitting at its prompt is the ordinary end state, not a
// mismatch. Nothing here contradicts the bead, so the call falls through to
// the ordinary SAFE_TO_NUKE / reuse-gate predicates.
func TestParkedMismatchRequiresStateWorking(t *testing.T) {
	t.Parallel()

	d := DecideWorkstate(WorkstateInput{
		State:              StateIdle,
		SessionParked:      true,
		ReuseFactsMeasured: true,
	})

	if d.Verdict == WorkstateVerdictParkedMismatch {
		t.Fatalf("verdict = %q, an idle polecat parked at its prompt is not a mismatch", d.Verdict)
	}
}

// TestUnmeasuredCallerKeepsOldNotIdleBehaviour pins the guarantee that makes
// this safe to land: a caller that never reads the pane leaves SessionParked
// false and gets exactly the not-idle verdict it got before this field
// existed.
func TestUnmeasuredCallerKeepsOldNotIdleBehaviour(t *testing.T) {
	t.Parallel()

	d := DecideWorkstate(WorkstateInput{State: StateWorking})
	if d.Verdict != WorkstateVerdictWorking || d.Reason != WorkstateReasonNotIdle {
		t.Fatalf("verdict/reason = %q/%q, want %q/%q",
			d.Verdict, d.Reason, WorkstateVerdictWorking, WorkstateReasonNotIdle)
	}
}
