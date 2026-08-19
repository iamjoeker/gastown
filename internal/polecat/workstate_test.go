package polecat

import "testing"

func TestDecideWorkstateCanonicalFields(t *testing.T) {
	tests := []struct {
		name string
		in   WorkstateInput
		want WorkstateDisposition
	}{
		{
			name: "clean idle is reusable and safe",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "main"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			name: "dirty idle needs recovery and capacity",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupUnpushed},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "cleanup-has_unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "protected active work fails closed without capacity",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveWorkBlocker: "assigned_work=gt-blocked status=blocked"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "active-work", NeedsRecovery: true, CountsTowardCapacity: false, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "active work blocker consumes capacity when requested",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveWorkBlocker: "assigned_work=gt-open status=open", ActiveWorkCountsTowardCapacity: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "active-work", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "unsubmitted branch needs mq submit",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsMQSubmit, Reason: "mq-not-submitted", NeedsRecovery: true, NeedsMQSubmit: true, MQStatus: "not_submitted", CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			// gt-46rk. This is the surface-independent half of the fix: the
			// refusal is a bead fact, so it fires even where no git or
			// merge-queue lookup ever ran (MQCheckRequired false).
			name: "recorded mr refusal needs mq submit without any queue lookup",
			in:   WorkstateInput{State: StateDone, CleanupStatus: CleanupClean, Branch: "polecat/test", MRRefused: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsMQSubmit, Reason: "mq-refused-closed-source", NeedsRecovery: true, NeedsMQSubmit: true, MQStatus: "refused_closed_source", CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"mq_status=refused_closed_source (gt done made no MR: source issue was closed)"}},
		},
		{
			name: "mr refusal is discharged by an mr that now exists",
			in:   WorkstateInput{State: StateDone, CleanupStatus: CleanupClean, Branch: "polecat/test", MRRefused: true, MRSubmitted: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-preserved"},
		},
		{
			// Proof, not silence: a surface that actually ran the check and
			// found nothing left to submit may retire the refusal.
			name: "mr refusal is discharged by a completed check with nothing to submit",
			in:   WorkstateInput{State: StateDone, CleanupStatus: CleanupClean, Branch: "polecat/test", MRRefused: true, MQCheckRequired: true, HasSubmittableWork: false},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, MQStatus: "not_required", ReuseStatus: "idle-preserved"},
		},
		{
			// ...but a check that could not complete may not. MQLookupFailed
			// already returns NEEDS_RECOVERY above; this pins the ordering so a
			// future refactor cannot let uncertainty discharge the refusal.
			name: "mr refusal survives a failed queue lookup",
			in:   WorkstateInput{State: StateDone, CleanupStatus: CleanupClean, Branch: "polecat/test", MRRefused: true, MQCheckRequired: true, MQLookupFailed: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "mq-lookup-failed", NeedsRecovery: true, MQStatus: "unknown", CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"mq_status=unknown"}},
		},
		{
			name: "mq lookup uncertainty blocks cleanup",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, MQLookupFailed: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "mq-lookup-failed", NeedsRecovery: true, MQStatus: "unknown", CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"mq_status=unknown"}},
		},
		{
			name: "open work with unpushed commits needs recovery",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", UnpushedCommits: 1},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"git_state=has_unpushed unpushed_commits=1"}},
		},
		{
			name: "mr submission makes mq submitted",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true, MRSubmitted: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, MQStatus: "submitted", ReuseStatus: "idle-preserved"},
		},
		{
			name: "terminal source alone does not prove mq submitted",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsMQSubmit, Reason: "mq-not-submitted", NeedsRecovery: true, NeedsMQSubmit: true, MQStatus: "not_submitted", CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "dirty worktree blocks terminal source",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", GitDirty: true, GitDirtyReason: "git_state=has_uncommitted uncommitted_files=1", MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-dirty", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"git_state=has_uncommitted uncommitted_files=1"}},
		},
		{
			name: "stash blocks terminal source",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", StashCount: 1, MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-stash", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"git_state=has_stash stash_count=1"}},
		},
		{
			name: "terminal source does not suppress unpreserved commits",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", UnpushedCommits: 1, MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"git_state=has_unpushed unpushed_commits=1"}},
		},
		{
			name: "push failure blocks terminal source",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", PushFailed: true, MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "push-failed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"push_failed=true"}},
		},
		{
			name: "mr failure blocks terminal source",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MRFailed: true, MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "mr-failed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"mr_failed=true"}},
		},
		{
			name: "open active mr blocks terminal source",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open", MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: "idle-pr-open", Blockers: []string{"active_mr=gt-mr-open status=open"}},
		},
		{
			name: "terminal active mr does not block when gatherer omits blocker",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-closed"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			name: "open active mr is preserved pending mr",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: "idle-pr-open"},
		},
		{
			name: "open active mr does not hide cleanup blocker",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupUnpushed, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "cleanup-has_unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"cleanup_status=has_unpushed", "active_mr=gt-mr-open status=open"}},
		},
		{
			name: "done active mr remains pending mr",
			in:   WorkstateInput{State: StateDone, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: "idle-pr-open", Blockers: []string{"active_mr=gt-mr-open status=open"}},
		},
		{
			name: "done without mr and clean cleanup is reusable and safe",
			in:   WorkstateInput{State: StateDone, CleanupStatus: CleanupClean},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			name: "done without mr blocks reuse when cleanup is dirty",
			in:   WorkstateInput{State: StateDone, CleanupStatus: CleanupUnpushed},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "cleanup-has_unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"cleanup_status=has_unpushed"}},
		},
		{
			name: "working counts as working capacity",
			in:   WorkstateInput{State: StateWorking, CleanupStatus: CleanupClean},
			want: WorkstateDisposition{Verdict: WorkstateVerdictWorking, Reason: "not-idle", NeedsRecovery: false, CountsTowardCapacity: true},
		},
		{
			// gt-5tg: the reported case. `gt done` has written agent_state=done
			// and cleared the work bead, so every bead predicate reads finished,
			// but the pane still shows the agent generating. Bead state must not
			// win — this was answering SAFE_TO_NUKE while the polecat was
			// mid-push.
			name: "busy session outranks a bead that already says done",
			in:   WorkstateInput{State: StateDone, SessionBusy: true, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true, MRSubmitted: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictWorking, Reason: "session-busy", CountsTowardCapacity: true, Blockers: []string{"session_state=busy (agent mid-turn)"}},
		},
		{
			name: "busy session outranks an idle bead with clean cleanup",
			in:   WorkstateInput{State: StateIdle, SessionBusy: true, CleanupStatus: CleanupClean, Branch: "main"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictWorking, Reason: "session-busy", CountsTowardCapacity: true, Blockers: []string{"session_state=busy (agent mid-turn)"}},
		},
		{
			name: "busy session outranks an open active mr",
			in:   WorkstateInput{State: StateDone, SessionBusy: true, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictWorking, Reason: "session-busy", CountsTowardCapacity: true, Blockers: []string{"session_state=busy (agent mid-turn)"}},
		},
		{
			// The absent-evidence direction: an unreadable or missing session
			// leaves SessionBusy false, and every existing verdict must survive
			// untouched. Anything else would stall reuse on rigs without tmux.
			name: "unknown session liveness leaves the safe verdict alone",
			in:   WorkstateInput{State: StateIdle, SessionBusy: false, CleanupStatus: CleanupClean, Branch: "main"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			name: "busy session is never reusable or safe to nuke",
			in:   WorkstateInput{State: StateIdle, SessionBusy: true, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, MRSubmitted: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictWorking, Reason: "session-busy", Reusable: false, SafeToNuke: false, CountsTowardCapacity: true},
		},
		{
			name: "stalled active work preserves blocker",
			in:   WorkstateInput{State: StateStalled, CleanupStatus: CleanupClean, ActiveWorkBlocker: "assigned_work=gt-open status=open", ActiveWorkCountsTowardCapacity: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "not-idle", NeedsRecovery: true, CountsTowardCapacity: true, Blockers: []string{"assigned_work=gt-open status=open"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Every case above asserts what a FACT-GATHERING caller concludes,
			// so the whole table runs measured. An unmeasured input never
			// reaches most of these predicates — it short-circuits at the tail
			// — and that path has its own test below rather than a silent
			// second meaning for each case here (gt-49dp).
			in := tt.in
			in.ReuseFactsMeasured = true
			got := DecideWorkstate(in)
			if got.Verdict != tt.want.Verdict || got.Reason != tt.want.Reason || got.Reusable != tt.want.Reusable || got.SafeToNuke != tt.want.SafeToNuke || got.NeedsRecovery != tt.want.NeedsRecovery || got.NeedsMQSubmit != tt.want.NeedsMQSubmit || got.MQStatus != tt.want.MQStatus || got.CountsTowardCapacity != tt.want.CountsTowardCapacity || got.ReuseStatus != tt.want.ReuseStatus {
				t.Fatalf("DecideWorkstate() = %+v, want fields %+v", got, tt.want)
			}
			if tt.want.Blockers != nil {
				if len(got.Blockers) != len(tt.want.Blockers) {
					t.Fatalf("DecideWorkstate() blockers = %v, want %v", got.Blockers, tt.want.Blockers)
				}
				for i := range tt.want.Blockers {
					if got.Blockers[i] != tt.want.Blockers[i] {
						t.Fatalf("DecideWorkstate() blockers = %v, want %v", got.Blockers, tt.want.Blockers)
					}
				}
			}
		})
	}
}

func TestApplyBranchMRToWorkstateInput(t *testing.T) {
	// An MR found by branch when the agent bead recorded nothing: this is the
	// rescue-submit case gt-46rk left invisible. The MR must both prove
	// submission and, while open, block the polecat as PENDING_MR — recycling
	// force-deletes the branch the MR points at.
	var in WorkstateInput
	ApplyBranchMRToWorkstateInput(&in, "bd-wisp-4v7m", true)
	if !in.MRSubmitted || in.ActiveMR != "bd-wisp-4v7m" || in.ActiveMRBlocker == "" {
		t.Fatalf("open branch MR = %+v", in)
	}

	// A closed MR still proves submission, but is no longer a reason to preserve.
	closed := WorkstateInput{}
	ApplyBranchMRToWorkstateInput(&closed, "bd-wisp-old", false)
	if !closed.MRSubmitted {
		t.Fatal("a closed MR still proves the branch reached the queue")
	}
	if closed.ActiveMR != "" || closed.ActiveMRBlocker != "" {
		t.Fatalf("closed MR must not block: %+v", closed)
	}

	// A stored active_mr wins: the caller has already assessed it, and that
	// assessment carries provenance a branch lookup cannot reconstruct.
	stored := WorkstateInput{ActiveMR: "bd-wisp-stored", ActiveMRBlocker: "active_mr=bd-wisp-stored status=open"}
	ApplyBranchMRToWorkstateInput(&stored, "bd-wisp-4v7m", true)
	if stored.ActiveMR != "bd-wisp-stored" || stored.ActiveMRBlocker != "active_mr=bd-wisp-stored status=open" {
		t.Fatalf("stored active_mr must win: %+v", stored)
	}
	if !stored.MRSubmitted {
		t.Fatal("submission is still proven when a stored active_mr wins")
	}

	// No MR found is not a fact about the queue — it must write nothing.
	var none WorkstateInput
	ApplyBranchMRToWorkstateInput(&none, "", true)
	if none.MRSubmitted || none.ActiveMR != "" || none.ActiveMRBlocker != "" {
		t.Fatalf("empty MR id must be a no-op: %+v", none)
	}
}

// TestDecideWorkstateUnmeasuredSurfaceCannotClaimReusable is the guard for the
// eleven-field gap between the two WorkstateInput constructors (gt-49dp).
//
// `gt polecat list` and the FindIdlePolecat reuse gate share DecideWorkstate but
// build its input separately, and the list constructor gathers no git and no
// merge-queue facts at all. Both then fell through to the same tail, so the same
// polecat read "idle-preserved / reusable" to the operator while the gate
// refused it for mq-not-submitted — and nothing in either output revealed that
// one surface had never looked. Sharing the decision function was never enough;
// the inputs have to agree, or the unmeasured one has to say so.
//
// The existing tests could not catch this because each exercises exactly one
// constructor. This one runs both shapes of input through the classifier
// together and asserts they never both answer confidently.
func TestDecideWorkstateUnmeasuredSurfaceCannotClaimReusable(t *testing.T) {
	// The reported polecat: done, clean cleanup, a preserved branch, commits
	// still outside the merge queue. This is what the two constructors see.
	listView := WorkstateInput{
		State:         StateDone,
		CleanupStatus: CleanupClean,
		Branch:        "polecat/chrome",
		// No git fields, no MQCheckRequired, no ReuseFactsMeasured: exactly what
		// buildPolecatInventoryItemFromEvidence produces.
	}
	gateView := WorkstateInput{
		State:              StateDone,
		CleanupStatus:      CleanupClean,
		Branch:             "polecat/chrome",
		MQCheckRequired:    true,
		HasSubmittableWork: true,
		ReuseFactsMeasured: true,
	}

	list := DecideWorkstate(listView)
	gate := DecideWorkstate(gateView)

	if gate.Reusable {
		t.Fatalf("fixture is wrong: the gate must refuse this polecat, got %+v", gate)
	}
	if gate.Reason != "mq-not-submitted" {
		t.Fatalf("gate reason = %q, want mq-not-submitted", gate.Reason)
	}
	// The bug, stated directly: the surface that never looked must not answer in
	// the same words as the surface that did.
	if list.ReuseStatus == "idle-preserved" {
		t.Fatalf("unmeasured list surface claimed %q while the gate refused with %q", list.ReuseStatus, gate.Reason)
	}
	if list.Reusable {
		t.Fatalf("unmeasured surface must not advertise reuse: %+v", list)
	}
	if list.SafeToNuke {
		t.Fatalf("unmeasured surface must not advertise safe-to-nuke: %+v", list)
	}
	if list.Verdict != WorkstateVerdictUnverified || list.ReuseStatus != "idle-unverified" {
		t.Fatalf("unmeasured surface = %+v, want UNVERIFIED/idle-unverified", list)
	}
	// Not a recovery condition and not capacity: nothing is known to be wrong
	// with the polecat, only with the question that was asked about it.
	if list.NeedsRecovery || list.NeedsMQSubmit || list.CountsTowardCapacity {
		t.Fatalf("unmeasured surface must not fabricate a recovery state: %+v", list)
	}
	if len(list.Blockers) == 0 {
		t.Fatal("unmeasured surface must name what it did not check")
	}

	// Measuring is the only thing that changes the answer: same facts plus the
	// checks the gate runs, and the two surfaces agree.
	measuredList := listView
	measuredList.ReuseFactsMeasured = true
	measuredList.MQCheckRequired = true
	measuredList.HasSubmittableWork = true
	if agreed := DecideWorkstate(measuredList); agreed.Reason != gate.Reason || agreed.ReuseStatus != gate.ReuseStatus {
		t.Fatalf("measured list view = %+v, want agreement with gate %+v", agreed, gate)
	}

	// The other direction: a polecat the gate genuinely clears still reads
	// idle-preserved, so this guard cannot be satisfied by refusing everyone.
	cleared := gateView
	cleared.MRSubmitted = true
	if d := DecideWorkstate(cleared); !d.Reusable || d.ReuseStatus != "idle-preserved" {
		t.Fatalf("a measured, cleared polecat must still be reusable: %+v", d)
	}
}
