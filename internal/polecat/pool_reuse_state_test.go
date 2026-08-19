package polecat

import "testing"

// TestStateEligibleForPoolReuse pins the set of states the pool may consider.
//
// This exists because gt-2uqy was one state missing from one condition:
// FindIdlePolecat matched StateIdle only, while a real pool is almost entirely
// StateDone (measured 2026-08-18: 26 of 26). Nothing errored — sling just quietly
// spawned a fresh worktree every time until the per-rig directory cap filled and
// blocked dispatch. A regression here looks like normal operation, so assert the
// membership directly rather than relying on the reuse path to fail loudly.
func TestStateEligibleForPoolReuse(t *testing.T) {
	tests := []struct {
		state State
		want  bool
		why   string
	}{
		{StateIdle, true, "never assigned, or already transitioned back to idle"},
		{StateDone, true, "finished work, idle transition not landed yet — the common case"},
		{StateWorking, false, "actively holds work"},
		{StateStalled, false, "session died mid-work; needs recovery"},
		{StateReviewNeeded, false, "live session with no work; needs a human verdict"},
		{State("nuked"), false, "unknown states must not be eligible by default"},
		{State(""), false, "unset state must not be eligible by default"},
	}

	for _, tt := range tests {
		if got := StateEligibleForPoolReuse(tt.state); got != tt.want {
			t.Errorf("StateEligibleForPoolReuse(%q) = %v, want %v (%s)", tt.state, got, tt.want, tt.why)
		}
	}
}

// TestStateEligibleForPoolReuseDecidesEligibilityNotSafety documents the split the
// predicate depends on: eligible states still have to clear DecideWorkstate before
// anything destructive happens. If eligibility ever started implying safety, a
// StateDone polecat with unpushed work would be reusable.
func TestStateEligibleForPoolReuseDecidesEligibilityNotSafety(t *testing.T) {
	for _, state := range []State{StateIdle, StateDone} {
		if !StateEligibleForPoolReuse(state) {
			t.Fatalf("precondition: %q should be eligible", state)
		}
		d := DecideWorkstate(WorkstateInput{
			State:           state,
			CleanupStatus:   CleanupClean,
			Branch:          "polecat/toast/gt-abc",
			UnpushedCommits: 3,
		})
		if d.Reusable {
			t.Errorf("state %q with 3 unpushed commits: Reusable = true, want false (reason=%q)", state, d.Reason)
		}
	}
}
