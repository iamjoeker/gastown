package polecat

import "testing"

// gt-3bzt: push_failed is set from the exit status of one `git push`, and a
// rebase makes a non-fast-forward rejection there the expected outcome. These
// cases pin down when the flag still decides and when a measurement is allowed
// to contradict it.

func TestGitFactsRefutePushFailed(t *testing.T) {
	tests := []struct {
		name            string
		gitMeasured     bool
		gitCheckFailed  bool
		gitDirty        bool
		stashCount      int
		unpushedCommits int
		want            bool
	}{
		{name: "measured and clean refutes", gitMeasured: true, want: true},
		// The false zero this predicate exists to not act on: an unmeasured
		// caller's zeros are indistinguishable from a clean worktree's.
		{name: "unmeasured never refutes", want: false},
		{name: "failed git check never refutes", gitMeasured: true, gitCheckFailed: true, want: false},
		{name: "dirty tree never refutes", gitMeasured: true, gitDirty: true, want: false},
		{name: "stash never refutes", gitMeasured: true, stashCount: 1, want: false},
		{name: "unpreserved patches never refute", gitMeasured: true, unpushedCommits: 1, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GitFactsRefutePushFailed(tt.gitMeasured, tt.gitCheckFailed, tt.gitDirty, tt.stashCount, tt.unpushedCommits)
			if got != tt.want {
				t.Errorf("GitFactsRefutePushFailed = %v, want %v", got, tt.want)
			}
		})
	}
}

// The unrefuted flag must keep behaving exactly as it did. This is the control
// for the test below: without it, a change that simply stopped reading
// push_failed at all would look like the fix.
func TestDecideWorkstate_UnrefutedPushFailedStillBlocks(t *testing.T) {
	got := DecideWorkstate(WorkstateInput{
		State:              StateIdle,
		CleanupStatus:      CleanupClean,
		PushFailed:         true,
		ReuseFactsMeasured: true,
	})
	if got.Verdict != WorkstateVerdictNeedsRecovery {
		t.Fatalf("verdict = %s, want %s", got.Verdict, WorkstateVerdictNeedsRecovery)
	}
	if got.Reason != "push-failed" {
		t.Errorf("reason = %q, want %q", got.Reason, "push-failed")
	}
	if len(got.Blockers) != 1 || got.Blockers[0] != "push_failed=true" {
		t.Errorf("blockers = %v, want [push_failed=true]", got.Blockers)
	}
	if !got.CountsTowardCapacity {
		t.Error("an unrefuted push_failed must still hold the slot")
	}
}

// The reported case: git says clean / 0 unpushed in the same instant the flag
// says a push failed. The measurement wins, and the polecat stops being routed
// to an escalation whose only remedy was a Mayor editing the field by hand.
func TestDecideWorkstate_RefutedPushFailedDoesNotStrand(t *testing.T) {
	got := DecideWorkstate(WorkstateInput{
		State:              StateIdle,
		CleanupStatus:      CleanupClean,
		Branch:             "polecat/dust",
		PushFailed:         true,
		PushFailedRefuted:  true,
		ReuseFactsMeasured: true,
	})
	if got.Verdict != WorkstateVerdictSafeToNuke {
		t.Fatalf("verdict = %s (blockers %v), want %s", got.Verdict, got.Blockers, WorkstateVerdictSafeToNuke)
	}
	if got.NeedsRecovery {
		t.Error("a refuted push_failed must not report needs_recovery")
	}
	if got.CountsTowardCapacity {
		t.Error("a refuted push_failed must not hold the slot")
	}
}

// Refuting the flag must not wave the work through the merge queue. A branch
// whose commits reached the remote but were never submitted still needs
// submitting — and NEEDS_MQ_SUBMIT is a verdict the witness can act on, which is
// the whole complaint about the NEEDS_RECOVERY/escalate pair it replaces.
func TestDecideWorkstate_RefutedPushFailedStillReachesMQTail(t *testing.T) {
	got := DecideWorkstate(WorkstateInput{
		State:              StateIdle,
		CleanupStatus:      CleanupClean,
		Branch:             "polecat/dust",
		PushFailed:         true,
		PushFailedRefuted:  true,
		MQCheckRequired:    true,
		HasSubmittableWork: true,
		ReuseFactsMeasured: true,
	})
	if got.Verdict != WorkstateVerdictNeedsMQSubmit {
		t.Fatalf("verdict = %s (blockers %v), want %s", got.Verdict, got.Blockers, WorkstateVerdictNeedsMQSubmit)
	}
	if got.MQStatus != "not_submitted" {
		t.Errorf("mq_status = %q, want %q", got.MQStatus, "not_submitted")
	}
}

// The reuse gate and check-recovery must not reach opposite conclusions about
// the same polecat from the same measurement, which is what carrying the
// refutation through SlotReuseInput is for.
func TestDecideSlotReuse_CarriesPushFailedRefutation(t *testing.T) {
	base := SlotReuseInput{State: StateIdle, CleanupStatus: CleanupClean, PushFailed: true, ReuseFactsMeasured: true}

	if got := DecideSlotReuse(base); got.Reusable || got.Reason != "push-failed" {
		t.Fatalf("unrefuted push_failed = %+v, want not reusable with reason push-failed", got)
	}

	refuted := base
	refuted.PushFailedRefuted = true
	if got := DecideSlotReuse(refuted); !got.Reusable {
		t.Errorf("refuted push_failed = %+v, want reusable", got)
	}
}

// Refuting push_failed refutes push_failed and nothing else. Any other blocker
// present at the same time must still be reported and must still block.
func TestDecideWorkstate_RefutedPushFailedLeavesOtherBlockers(t *testing.T) {
	got := DecideWorkstate(WorkstateInput{
		State:              StateIdle,
		CleanupStatus:      CleanupClean,
		Branch:             "polecat/dust",
		PushFailed:         true,
		PushFailedRefuted:  true,
		MRFailed:           true,
		ReuseFactsMeasured: true,
	})
	if got.Verdict != WorkstateVerdictNeedsRecovery {
		t.Fatalf("verdict = %s, want %s", got.Verdict, WorkstateVerdictNeedsRecovery)
	}
	if got.Reason != "mr-failed" {
		t.Errorf("reason = %q, want %q", got.Reason, "mr-failed")
	}
	if len(got.Blockers) != 1 || got.Blockers[0] != "mr_failed=true" {
		t.Errorf("blockers = %v, want [mr_failed=true]", got.Blockers)
	}
}
