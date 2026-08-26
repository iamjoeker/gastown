package polecat

import (
	"strings"
	"testing"
)

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
			//
			// gt-mkpm: and it says so. Everything that DECIDES is identical to
			// the checked case below — same verdict, same flags, same capacity —
			// but the words name the caller's gap instead of naming a remedy the
			// caller is in no position to prescribe.
			name: "recorded mr refusal needs mq submit without any queue lookup",
			in:   WorkstateInput{State: StateDone, CleanupStatus: CleanupClean, Branch: "polecat/test", MRRefused: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsMQSubmit, Reason: "mq-refused-unchecked", NeedsRecovery: true, NeedsMQSubmit: true, MQStatus: "refused_closed_source_unchecked", CountsTowardCapacity: true, ReuseStatus: ReuseStatusMQUnchecked, Blockers: []string{"mq_status=refused_closed_source_unchecked (gt done made no MR: source issue was closed; no merge-queue check was run here, so whether the branch still holds unsubmitted work is UNKNOWN)"}},
		},
		{
			// The consulted road, pinned because the STRING above depends on it:
			// a refusal whose queue WAS consulted and still has work outstanding
			// must not come out wearing the unchecked vocabulary. It reaches the
			// MQCheckRequired block first and is a finding about the polecat, so
			// it gets the verdict-shaped string and mq_status=not_submitted.
			name: "consulted mr refusal with work outstanding keeps the measured vocabulary",
			in:   WorkstateInput{State: StateDone, CleanupStatus: CleanupClean, Branch: "polecat/test", MRRefused: true, MQCheckRequired: true, HasSubmittableWork: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsMQSubmit, Reason: "mq-not-submitted", NeedsRecovery: true, NeedsMQSubmit: true, MQStatus: "not_submitted", CountsTowardCapacity: true, ReuseStatus: ReuseStatusRecoveryNeeded, Blockers: []string{"mq_status=not_submitted"}},
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
			// gt-acb1: the reported case. A `gt session restart` brought the
			// session up without CLAUDE_CONFIG_DIR, so the agent is logged out —
			// but it still holds its hooked bead and its lifecycle state is still
			// working, so every bead-derived fact says it is fine. This used to
			// come out WORKING / leave-alone, which is why it ran for eighteen
			// minutes with two health surfaces calling it healthy.
			name: "logged-out session outranks a bead that still says working",
			in:   WorkstateInput{State: StateWorking, SessionLoggedOut: true, CleanupStatus: CleanupClean, HookBead: "gt-hooked"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsLogin, Reason: "session-logged-out", NeedsLogin: true, CountsTowardCapacity: true, Blockers: []string{"session_state=logged-out (agent has no credentials; needs a human /login)"}},
		},
		{
			// The auth wall is not a work-at-risk state, so it must not be
			// reusable or safe to nuke either: the polecat holds a slot it cannot
			// work in, and destroying it neither supplies credentials nor stops
			// the next session coming up the same way.
			name: "logged-out session is never reusable or safe to nuke",
			in:   WorkstateInput{State: StateIdle, SessionLoggedOut: true, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, MRSubmitted: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsLogin, Reason: "session-logged-out", NeedsLogin: true, Reusable: false, SafeToNuke: false, CountsTowardCapacity: true},
		},
		{
			// Busy wins. Both are pane reads of the same snapshot and should be
			// mutually exclusive on a real pane, so reaching both means one
			// marker matched text rather than the status bar — and busy is the
			// safer of the two to believe, because it prescribes leave-alone.
			name: "a busy session outranks a logged-out reading of the same pane",
			in:   WorkstateInput{State: StateWorking, SessionBusy: true, SessionLoggedOut: true, CleanupStatus: CleanupClean},
			want: WorkstateDisposition{Verdict: WorkstateVerdictWorking, Reason: "session-busy", CountsTowardCapacity: true, Blockers: []string{"session_state=busy (agent mid-turn)"}},
		},
		{
			// The absent-evidence direction, same contract as SessionBusy: an
			// unreadable or missing pane leaves the flag false and every existing
			// verdict must survive untouched.
			name: "unknown login state leaves the safe verdict alone",
			in:   WorkstateInput{State: StateIdle, SessionLoggedOut: false, CleanupStatus: CleanupClean, Branch: "main"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			name: "stalled active work preserves blocker",
			in:   WorkstateInput{State: StateStalled, CleanupStatus: CleanupClean, ActiveWorkBlocker: "assigned_work=gt-open status=open", ActiveWorkCountsTowardCapacity: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "not-idle", NeedsRecovery: true, CountsTowardCapacity: true, Blockers: []string{"assigned_work=gt-open status=open"}},
		},
		{
			// gt-mkpm / hq-qm7bt. This is the input that produced "Cleanup
			// refused by an unknown recovery predicate": NEEDS_RECOVERY with an
			// empty blocker list, which the renderer had nothing to print. The
			// predicate was never unknown — the state IS the predicate — so it
			// must appear in the blockers.
			name: "not-idle with no other blocker names the state it refused on",
			in:   WorkstateInput{State: StateStalled, CleanupStatus: CleanupClean},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "not-idle", NeedsRecovery: true, CountsTowardCapacity: true, Blockers: []string{"polecat_state=stalled (lifecycle state is not idle; no other blocker was recorded)"}},
		},
		{
			// The handed-off window: the session is gone and an open MR for the
			// branch was found. Before gt-mkpm this bailed out at the not-idle
			// arm as NEEDS_RECOVERY with no blockers, and the list surface
			// printed "stalled" — the word for the failure case — over a polecat
			// that had SUCCEEDED, for exactly as long as the MR was in flight.
			name: "handed off with an open mr is pending, not stalled",
			in:   WorkstateInput{State: StateHandedOff, CleanupStatus: CleanupClean, ActiveMR: "gt-wisp-1cmci", ActiveMRBlocker: "active_mr=gt-wisp-1cmci status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: ReuseStatusPROpen, Blockers: []string{"active_mr=gt-wisp-1cmci status=open"}},
		},
		{
			// The two surfaces record still-attached work in DIFFERENT fields:
			// the list surface in ActiveWorkBlocker, check-recovery in HookBead.
			// Both must survive into a PENDING_MR verdict, or the two disagree
			// again about the same polecat — which is the bead's title.
			//
			// SessionPresence is deliberately unset here, and that is now what
			// this case is about as much as the hook is. This is the UNMEASURED
			// road: nobody ran `tmux has-session`, so nothing has been shown
			// about whether the agent is alive, and leave-alone — the
			// non-destructive answer — stays right. The measured-dead variant of
			// this exact input is the gt-9f67 case below and answers differently.
			name: "pending mr reports the hook check-recovery gathered",
			in:   WorkstateInput{State: StateHandedOff, CleanupStatus: CleanupClean, HookBead: "gt-0g5r", ActiveMR: "gt-wisp-1cmci", ActiveMRBlocker: "active_mr=gt-wisp-1cmci status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: ReuseStatusPROpen, Blockers: []string{"active_mr=gt-wisp-1cmci status=open", "has work on hook (gt-0g5r)"}},
		},
		{
			// Handed-off is not a waiver. It routes like StateDone — through the
			// real predicates — so work genuinely at risk still surfaces, and the
			// still-hooked bead that chrome carried is NAMED rather than being
			// flattened into a bare "stalled".
			name: "handed off still reports work at risk alongside the mr",
			in:   WorkstateInput{State: StateHandedOff, CleanupStatus: CleanupClean, ActiveMR: "gt-wisp-1cmci", ActiveMRBlocker: "active_mr=gt-wisp-1cmci status=open", ActiveWorkBlocker: "assigned_work=gt-0g5r status=hooked", ActiveWorkCountsTowardCapacity: true, PushFailed: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "push-failed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: ReuseStatusRecoveryNeeded, Blockers: []string{"push_failed=true", "assigned_work=gt-0g5r status=hooked", "active_mr=gt-wisp-1cmci status=open"}},
		},
		{
			// THE gt-9f67 CASE. Byte-for-byte the "pending mr reports the hook"
			// input above plus one field: `tmux has-session` was actually run and
			// said the session is gone. That one fact turns the answer from
			// leave-alone into escalate, which is the whole finding — the verdict
			// tracked the MR and the hook and never consulted liveness at all.
			//
			// gastown/chrome went stalled -> handed-off -> SAFE_TO_NUKE across
			// three readings with the session dead at every one of them and
			// nothing about the polecat changing. This is the middle reading, the
			// one that told a witness to leave a dead polecat alone indefinitely.
			name: "measured-dead session holding work refuses leave-alone",
			in:   WorkstateInput{State: StateHandedOff, CleanupStatus: CleanupClean, SessionPresence: SessionAbsent, HookBead: "gt-0g5r", ActiveMR: "gt-wisp-1cmci", ActiveMRBlocker: "active_mr=gt-wisp-1cmci status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: WorkstateReasonStalledPendingMR, NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: ReuseStatusRecoveryNeeded, Blockers: []string{"session_presence=absent (tmux has-session found no session) while work is still attached", "has work on hook (gt-0g5r)", "active_mr=gt-wisp-1cmci status=open"}},
		},
		{
			// The same signature as the list surface records it. The two surfaces
			// put still-attached work in different fields and a guard that read
			// only one of them would fix check-recovery and leave `gt polecat
			// list` saying the opposite about the same polecat (gt-mkpm).
			name: "measured-dead session holding assigned work refuses leave-alone",
			in:   WorkstateInput{State: StateHandedOff, CleanupStatus: CleanupClean, SessionPresence: SessionAbsent, ActiveWorkBlocker: "assigned_work=gt-0g5r status=hooked", ActiveWorkCountsTowardCapacity: true, ActiveMR: "gt-wisp-1cmci", ActiveMRBlocker: "active_mr=gt-wisp-1cmci status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: WorkstateReasonStalledPendingMR, NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: ReuseStatusRecoveryNeeded, Blockers: []string{"session_presence=absent (tmux has-session found no session) while work is still attached", "assigned_work=gt-0g5r status=hooked", "active_mr=gt-wisp-1cmci status=open"}},
		},
		{
			// THE ROAD THE LIVE CASE ACTUALLY TOOK, and the one a narrower guard
			// would have missed entirely.
			//
			// gastown/deathclaw: gt-y39t at status HOOKED, assigned to it in the
			// issue store, session gone, MR gt-wisp-67mp open. Its agent bead's
			// hook_bead field was EMPTY, so check-recovery fed no hook into the
			// classifier and reported exactly one blocker — the MR. A guard
			// reading HookBead and ActiveWorkBlocker alone would have passed its
			// own tests and changed nothing about the polecat it was written for.
			//
			// The bead must be named in the blockers on this road too: it is the
			// only one carrying an ID here, so without it the verdict says work is
			// attached and nothing says which work.
			name: "measured-dead session holding issue-store work refuses leave-alone",
			in:   WorkstateInput{State: StateHandedOff, CleanupStatus: CleanupClean, SessionPresence: SessionAbsent, AssignedWorkBead: "gt-y39t", ActiveMR: "gt-wisp-67mp", ActiveMRBlocker: "active_mr=gt-wisp-67mp status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: WorkstateReasonStalledPendingMR, NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: ReuseStatusRecoveryNeeded, Blockers: []string{"session_presence=absent (tmux has-session found no session) while work is still attached (issue store holds gt-y39t hooked to this polecat)", "active_mr=gt-wisp-67mp status=open"}},
		},
		{
			// AssignedWorkBead blocks NOTHING on its own. It is consumed by the
			// dead-session precondition and by nothing else, so a polecat whose
			// issue store holds work but whose session was never measured keeps
			// whatever verdict it had. Widening it into a general blocker would
			// refuse cleanup on surfaces that currently allow it — a much larger
			// change than this bead asks for, and one nobody has measured.
			name: "issue-store work alone does not block an unmeasured session",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, AssignedWorkBead: "gt-y39t", Branch: "polecat/deathclaw/gt-y39t"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: ReuseStatusPreserved},
		},
		{
			// THE CONTROL THAT MATTERS MOST, because getting it wrong jams the
			// whole pool: a dead session is the NORMAL end state of every polecat
			// that ever succeeded. `gt done` pushes, submits, clears the hook and
			// exits, so "no session + open MR + no work attached" is what
			// completion looks like. It must still read PENDING_MR / leave-alone.
			//
			// This is also the discriminator, stated as a test: what separates the
			// case above from this one is not the dead session — both are dead —
			// but the work still attached to it.
			name: "measured-dead session with no work attached is still a hand-off",
			in:   WorkstateInput{State: StateHandedOff, CleanupStatus: CleanupClean, SessionPresence: SessionAbsent, ActiveMR: "gt-wisp-1cmci", ActiveMRBlocker: "active_mr=gt-wisp-1cmci status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: ReuseStatusPROpen, Blockers: []string{"active_mr=gt-wisp-1cmci status=open"}},
		},
		{
			// A live session is not the signature either, and this arm is here so
			// the guard cannot be satisfied by "not alive" — which is what a bool
			// would have collapsed Alive, Unknown and Dead into.
			name: "live session holding work under an open mr is left alone",
			in:   WorkstateInput{State: StateHandedOff, CleanupStatus: CleanupClean, SessionPresence: SessionPresent, HookBead: "gt-0g5r", ActiveMR: "gt-wisp-1cmci", ActiveMRBlocker: "active_mr=gt-wisp-1cmci status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: ReuseStatusPROpen, Blockers: []string{"active_mr=gt-wisp-1cmci status=open", "has work on hook (gt-0g5r)"}},
		},
		{
			// The guard adds a blocker; it must never eat one. A polecat can be
			// dead, hooked, carrying an open MR and carrying push_failed at the
			// same time, and returning early on the dead session would have
			// dropped push_failed from the output — replacing one incomplete
			// picture with another.
			name: "the dead-session blocker joins the others rather than replacing them",
			in:   WorkstateInput{State: StateHandedOff, CleanupStatus: CleanupClean, SessionPresence: SessionAbsent, HookBead: "gt-0g5r", PushFailed: true, ActiveMR: "gt-wisp-1cmci", ActiveMRBlocker: "active_mr=gt-wisp-1cmci status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: WorkstateReasonStalledPendingMR, NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: ReuseStatusRecoveryNeeded, Blockers: []string{"session_presence=absent (tmux has-session found no session) while work is still attached", "has work on hook (gt-0g5r)", "push_failed=true", "active_mr=gt-wisp-1cmci status=open"}},
		},
		{
			// No MR, no leave-alone road to guard — so the reason must NOT be the
			// pending-MR one. A dead idle polecat with a hook already escalates on
			// hook-still-set, and naming a pending MR that does not exist would be
			// a confident sentence about nothing.
			name: "dead session holding work without an mr keeps its own reason",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, SessionPresence: SessionAbsent, HookBead: "gt-0g5r"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "hook-still-set", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: ReuseStatusRecoveryNeeded, Blockers: []string{"has work on hook (gt-0g5r)"}},
		},
		{
			// A dead session with nothing attached and nothing else blocking is
			// the reuse pool's steady state. If this stopped reading SAFE_TO_NUKE
			// every finished polecat would hold its slot forever.
			name: "dead session with nothing attached stays reusable",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, SessionPresence: SessionAbsent, Branch: "polecat/chrome/gt-0g5r"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: ReuseStatusPreserved},
		},
		{
			// gt-n3jq, TRANSCRIBED FROM THE LIVE POLECAT, not constructed:
			// gastown/deathclaw at 2026-08-26T14:2x, session gone, gt-sfcl still
			// HOOKED in the issue store, MR gt-wisp-ep0m open — and its agent bead
			// reading exit_type: COMPLETED / mr_id: gt-wisp-ep0m /
			// last_source_issue: gt-sfcl / completion_time: 2026-08-26T14:03:30Z.
			//
			// The row above it in this table is the SAME polecat one bead earlier
			// and asserts escalate. Both cannot be right, and the difference is the
			// completion record: gt done left gt-sfcl hooked ON PURPOSE, because
			// the refinery closes the source bead on merge (gt-429i), so "still
			// holding work" is what every successful polecat looks like for the
			// whole time its MR is in flight.
			name: "completion record covering the held work restores leave-alone",
			in: WorkstateInput{
				State: StateHandedOff, CleanupStatus: CleanupClean, SessionPresence: SessionAbsent,
				AssignedWorkBead: "gt-sfcl", ActiveMR: "gt-wisp-ep0m", ActiveMRBlocker: "active_mr=gt-wisp-ep0m status=open",
				CompletionCoverage: "completion_record=exit_type=COMPLETED mr_id=gt-wisp-ep0m last_source_issue=gt-sfcl completion_time=2026-08-26T14:03:30Z",
			},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: ReuseStatusPROpen, Blockers: []string{
				"active_mr=gt-wisp-ep0m status=open",
				"session_presence=absent (tmux has-session found no session) — not a stall: completion_record=exit_type=COMPLETED mr_id=gt-wisp-ep0m last_source_issue=gt-sfcl completion_time=2026-08-26T14:03:30Z",
			}},
		},
		{
			// The same waiver reached through the OTHER two held-work fields, so
			// this cannot be a fix that only works on the road the live case took.
			// gt-9f67 wrote three cases for exactly this reason and it applies with
			// equal force to its exemption.
			name: "completion record covering a hook and a listed blocker also restores leave-alone",
			in: WorkstateInput{
				State: StateHandedOff, CleanupStatus: CleanupClean, SessionPresence: SessionAbsent,
				HookBead: "gt-sfcl", ActiveWorkBlocker: "assigned_work=gt-sfcl status=hooked", ActiveWorkCountsTowardCapacity: true,
				ActiveMR: "gt-wisp-ep0m", ActiveMRBlocker: "active_mr=gt-wisp-ep0m status=open",
				CompletionCoverage: "completion_record=exit_type=COMPLETED mr_id=gt-wisp-ep0m last_source_issue=gt-sfcl",
			},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: ReuseStatusPROpen, Blockers: []string{
				"active_mr=gt-wisp-ep0m status=open",
				"assigned_work=gt-sfcl status=hooked",
				"has work on hook (gt-sfcl)",
				"session_presence=absent (tmux has-session found no session) — not a stall: completion_record=exit_type=COMPLETED mr_id=gt-wisp-ep0m last_source_issue=gt-sfcl",
			}},
		},
		{
			// THE CONTROL THAT KEEPS gt-9f67 ALIVE. Identical to the row above in
			// every field the verdict reads except the coverage, which is what a
			// polecat that DIED holding its work has: nothing. It must still
			// escalate, or this fix has quietly reverted the bead it builds on.
			name: "dead session holding work with no completion record still escalates",
			in: WorkstateInput{
				State: StateHandedOff, CleanupStatus: CleanupClean, SessionPresence: SessionAbsent,
				AssignedWorkBead: "gt-sfcl", ActiveMR: "gt-wisp-ep0m", ActiveMRBlocker: "active_mr=gt-wisp-ep0m status=open",
			},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: WorkstateReasonStalledPendingMR, NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: ReuseStatusRecoveryNeeded, Blockers: []string{
				"session_presence=absent (tmux has-session found no session) while work is still attached (issue store holds gt-sfcl hooked to this polecat)",
				"active_mr=gt-wisp-ep0m status=open",
			}},
		},
		{
			// Coverage waives the PRESENCE precondition and nothing else. A
			// completed polecat whose push failed still has work outside the queue,
			// and a waiver that swallowed that would turn this fix into the
			// false-success defect gt-n3jq is the mirror of.
			name: "completion record does not waive push_failed",
			in: WorkstateInput{
				State: StateHandedOff, CleanupStatus: CleanupClean, SessionPresence: SessionAbsent,
				AssignedWorkBead: "gt-sfcl", PushFailed: true, ActiveMR: "gt-wisp-ep0m", ActiveMRBlocker: "active_mr=gt-wisp-ep0m status=open",
				CompletionCoverage: "completion_record=exit_type=COMPLETED mr_id=gt-wisp-ep0m last_source_issue=gt-sfcl",
			},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "push-failed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: ReuseStatusRecoveryNeeded, Blockers: []string{
				"push_failed=true",
				"active_mr=gt-wisp-ep0m status=open",
			}},
		},
		{
			// The waiver is a fact about a DEAD session, so it must not appear over
			// a live one. Printing "not a stall" next to a polecat that is sitting
			// right there generating would be a confident sentence about a
			// condition that is not present — the shape this whole file is about.
			name: "the waiver line is not printed for a live session",
			in: WorkstateInput{
				State: StateHandedOff, CleanupStatus: CleanupClean, SessionPresence: SessionPresent,
				HookBead: "gt-sfcl", ActiveMR: "gt-wisp-ep0m", ActiveMRBlocker: "active_mr=gt-wisp-ep0m status=open",
				CompletionCoverage: "completion_record=exit_type=COMPLETED mr_id=gt-wisp-ep0m last_source_issue=gt-sfcl",
			},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: ReuseStatusPROpen, Blockers: []string{
				"active_mr=gt-wisp-ep0m status=open",
				"has work on hook (gt-sfcl)",
			}},
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

// A deliberate agent_state pause is its own verdict with its own remedy, and it
// is the LOWEST-priority blocker. Both halves matter:
//
//   - Its own verdict, because check-recovery never read agent_state at all and
//     so answered SAFE_TO_NUKE / witness_action=restart for the same polecat
//     `gt polecat list` was calling NEEDS_RECOVERY on agent_state=stuck. Restart
//     writes no agent_state, so that prescription could not move the state.
//
//   - Lowest priority, because the pause is evaluated after the merge-queue tail.
//     A stuck polecat with work still outside the queue must read NEEDS_MQ_SUBMIT.
//     "Just clear the field" would talk a witness straight past work at risk.
//
// gt-fbgq.
func TestDecideWorkstatePausedAgentState(t *testing.T) {
	base := WorkstateInput{
		State:              StateIdle,
		CleanupStatus:      CleanupClean,
		Branch:             "polecat/ace/bd-4l6",
		ReuseFactsMeasured: true,
	}

	t.Run("pause alone is its own verdict", func(t *testing.T) {
		in := base
		in.PausedAgentState = "stuck"
		d := DecideWorkstate(in)
		if d.Verdict != WorkstateVerdictNeedsStateClear || !d.NeedsStateClear {
			t.Fatalf("verdict = %+v, want NEEDS_STATE_CLEAR", d)
		}
		// Not a recovery condition: there is nothing to recover, only a field to
		// clear. Routing it to "escalate" was half of what stranded the slot.
		if d.NeedsRecovery || d.NeedsMQSubmit {
			t.Fatalf("a pause is not a recovery condition: %+v", d)
		}
		// And not a reuse either: silently reusing the slot discards a pause
		// somebody set on purpose.
		if d.Reusable || d.SafeToNuke {
			t.Fatalf("a pause must still block reuse: %+v", d)
		}
		if d.CountsTowardCapacity {
			t.Fatalf("a pause occupies no slot: %+v", d)
		}
		if !containsBlocker(d.Blockers, "agent_state=stuck") {
			t.Fatalf("blockers %q must name the state", d.Blockers)
		}
	})

	t.Run("work at risk outranks the pause", func(t *testing.T) {
		in := base
		in.PausedAgentState = "stuck"
		in.UnpushedCommits = 1
		d := DecideWorkstate(in)
		if d.Verdict != WorkstateVerdictNeedsRecovery || !d.NeedsRecovery {
			t.Fatalf("verdict = %+v, want NEEDS_RECOVERY", d)
		}
		// Reported alongside, not swallowed: the witness needs to know both that
		// there is work to rescue and that a pause is waiting behind it.
		if !containsBlocker(d.Blockers, "git_state=has_unpushed") || !containsBlocker(d.Blockers, "agent_state=stuck") {
			t.Fatalf("blockers %q must name both the work and the pause", d.Blockers)
		}
	})

	t.Run("unsubmitted merge queue work outranks the pause", func(t *testing.T) {
		// The ordering hazard in one case. The merge-queue tail runs AFTER the
		// blocker loop, so a pause that blocked in that loop would return before
		// the tail ever executed and this would read NEEDS_STATE_CLEAR.
		in := base
		in.PausedAgentState = "stuck"
		in.MQCheckRequired = true
		in.HasSubmittableWork = true
		d := DecideWorkstate(in)
		if d.Verdict != WorkstateVerdictNeedsMQSubmit || !d.NeedsMQSubmit {
			t.Fatalf("verdict = %+v, want NEEDS_MQ_SUBMIT — the pause must not preempt the queue check", d)
		}
	})

	t.Run("a refused merge request outranks the pause", func(t *testing.T) {
		in := base
		in.PausedAgentState = "stuck"
		in.MRRefused = true
		d := DecideWorkstate(in)
		if d.Verdict != WorkstateVerdictNeedsMQSubmit {
			t.Fatalf("verdict = %+v, want NEEDS_MQ_SUBMIT", d)
		}
	})

	t.Run("an open merge request stays leave-alone", func(t *testing.T) {
		// PENDING_MR means "preserve this until it lands". The pause must not
		// convert that into an escalation, so it is appended to the blockers only
		// after the single-blocker MR test has been decided.
		in := base
		in.PausedAgentState = "stuck"
		in.ActiveMRBlocker = "active_mr=gt-mr status=open"
		d := DecideWorkstate(in)
		if d.Verdict != WorkstateVerdictPendingMR {
			t.Fatalf("verdict = %+v, want PENDING_MR", d)
		}
		if !containsBlocker(d.Blockers, "agent_state=stuck") {
			t.Fatalf("blockers %q must still name the pause", d.Blockers)
		}
	})

	t.Run("a busy session outranks everything", func(t *testing.T) {
		in := base
		in.PausedAgentState = "stuck"
		in.SessionBusy = true
		if d := DecideWorkstate(in); d.Verdict != WorkstateVerdictWorking {
			t.Fatalf("verdict = %+v, want WORKING", d)
		}
	})

	t.Run("an unmeasured surface still names the pause", func(t *testing.T) {
		// The list surface runs no git, so it has not earned NEEDS_STATE_CLEAR.
		// But agent_state is a bead fact it read directly, and dropping it would
		// reproduce the original defect from the other side.
		in := base
		in.PausedAgentState = "stuck"
		in.ReuseFactsMeasured = false
		d := DecideWorkstate(in)
		if d.Verdict != WorkstateVerdictUnverified {
			t.Fatalf("verdict = %+v, want UNVERIFIED", d)
		}
		if !containsBlocker(d.Blockers, "agent_state=stuck") {
			t.Fatalf("blockers %q must name the pause even when nothing was measured", d.Blockers)
		}
	})

	t.Run("no pause is unchanged", func(t *testing.T) {
		// The control. If this ever fails, the arms above are passing because
		// everything is blocked, not because the pause is being detected.
		if d := DecideWorkstate(base); !d.Reusable || d.Verdict != WorkstateVerdictSafeToNuke {
			t.Fatalf("an unpaused clean polecat must still be reusable: %+v", d)
		}
	})
}

func containsBlocker(blockers []string, want string) bool {
	for _, blocker := range blockers {
		if strings.Contains(blocker, want) {
			return true
		}
	}
	return false
}

// TestUnmeasuredVocabularyIsDisjoint is the acceptance test for gt-mkpm: a
// reader of one surface must be able to tell a measured blocker from an
// unmeasured one, from the output alone.
//
// The bug was not a wrong verdict. Both roads block, deliberately and
// identically — "suppressed only by proof, never by silence" (gt-46rk). The bug
// was that they printed the SAME WORD, so a string meaning "this caller did not
// look" read as a finding about the polecat, and two witnesses spent a night
// measuring four remedies "failing" to clear a condition that was recomputed
// from scratch on every listing and had no stored state to clear.
func TestUnmeasuredVocabularyIsDisjoint(t *testing.T) {
	// The did-not-look road: a recorded MR refusal reaching a surface that runs
	// no merge-queue check. This is exactly what `gt polecat list` produced.
	unmeasured := DecideWorkstate(WorkstateInput{
		State: StateDone, CleanupStatus: CleanupClean, Branch: "polecat/ghoul", MRRefused: true,
	})
	// The looked-and-found road: a caller that ran the check and found work
	// outside the queue.
	measured := DecideWorkstate(WorkstateInput{
		State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/ghoul",
		MQCheckRequired: true, HasSubmittableWork: true, ReuseFactsMeasured: true,
	})

	// Both still block, and both still count. The fix is words only — if this
	// arm ever fails, the rendering fix has quietly become a policy change.
	for name, d := range map[string]WorkstateDisposition{"unmeasured": unmeasured, "measured": measured} {
		if d.Verdict != WorkstateVerdictNeedsMQSubmit || !d.NeedsRecovery || !d.NeedsMQSubmit || d.SafeToNuke || d.Reusable || !d.CountsTowardCapacity {
			t.Fatalf("%s road stopped blocking: %+v", name, d)
		}
	}

	if unmeasured.ReuseStatus == measured.ReuseStatus {
		t.Fatalf("did-not-look and looked-and-found share reuse_status %q — the whole defect", unmeasured.ReuseStatus)
	}
	if unmeasured.ReuseStatus != ReuseStatusMQUnchecked {
		t.Fatalf("unmeasured reuse_status = %q, want %q", unmeasured.ReuseStatus, ReuseStatusMQUnchecked)
	}
	if measured.ReuseStatus != ReuseStatusRecoveryNeeded {
		t.Fatalf("measured reuse_status = %q, want %q", measured.ReuseStatus, ReuseStatusRecoveryNeeded)
	}
	if unmeasured.MQStatus == measured.MQStatus || unmeasured.Reason == measured.Reason {
		t.Fatalf("mq_status/reason still collide: %+v vs %+v", unmeasured, measured)
	}

	// The blocker line is what gets quoted into a bead, so it has to carry the
	// gap by itself, without the reuse_status next to it.
	if !containsBlocker(unmeasured.Blockers, "no merge-queue check was run") {
		t.Fatalf("unmeasured blockers %q must say the check did not run", unmeasured.Blockers)
	}

	// And the predicate surfaces use to footnote a listing must agree.
	if !DispositionUnmeasured(unmeasured) {
		t.Fatal("DispositionUnmeasured must be true for the did-not-look road")
	}
	if DispositionUnmeasured(measured) {
		t.Fatal("DispositionUnmeasured must be false for a measured finding")
	}
	// The control: the OTHER unmeasured road must also be caught, or the
	// predicate is matching one literal rather than a category.
	nothingGathered := DecideWorkstate(WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "main"})
	if nothingGathered.ReuseStatus != ReuseStatusUnverified || !DispositionUnmeasured(nothingGathered) {
		t.Fatalf("idle-unverified must count as unmeasured: %+v", nothingGathered)
	}
}

func TestHandedOffState(t *testing.T) {
	if got := HandedOffState(StateStalled, true); got != StateHandedOff {
		t.Fatalf("stalled + proven open MR = %q, want %q", got, StateHandedOff)
	}
	// Proof, not absence of doubt. An unconsulted queue leaves the polecat
	// stalled: claiming handed-off from silence is the same defect pointing the
	// other way, and this direction is the one that talks a witness out of
	// looking at a polecat that really did die.
	if got := HandedOffState(StateStalled, false); got != StateStalled {
		t.Fatalf("stalled + no proof = %q, want %q", got, StateStalled)
	}
	// Only stalled is promoted. Nothing else is a candidate — a live working
	// session with an open MR is still working.
	for _, state := range []State{StateWorking, StateIdle, StateDone, StateZombie, StateReviewNeeded, StateStuck} {
		if got := HandedOffState(state, true); got != state {
			t.Fatalf("HandedOffState(%q, true) = %q, want it unchanged", state, got)
		}
	}
}

func TestCompletionCoverage(t *testing.T) {
	// The live record, copied field-for-field off gastown/deathclaw's agent bead
	// while the polecat was reading NEEDS_RECOVERY / escalate (gt-n3jq).
	live := CompletionRecord{
		ExitType:        "COMPLETED",
		MRID:            "gt-wisp-ep0m",
		LastSourceIssue: "gt-sfcl",
		CompletionTime:  "2026-08-26T14:03:30Z",
	}

	// The positive control runs FIRST and its result is required below, so a
	// predicate that can only ever return "" cannot pass this test by refusing
	// everything — which is what a coverage check that never fires would do, and
	// it would look exactly like a careful one.
	got := CompletionCoverage(live, "gt-wisp-ep0m", "gt-sfcl")
	if got == "" {
		t.Fatal("the measured record covering its own MR and its own bead returned no coverage")
	}
	// The evidence has to name what was matched. A bare bool would let the
	// waiver decide a verdict and leave the reader no way to audit it.
	for _, want := range []string{"COMPLETED", "gt-wisp-ep0m", "gt-sfcl", "2026-08-26T14:03:30Z"} {
		if !strings.Contains(got, want) {
			t.Fatalf("coverage %q omits %q", got, want)
		}
	}

	refusals := []struct {
		name     string
		rec      CompletionRecord
		citedMR  string
		heldWork []string
	}{
		// The polecat DIED holding its work: no record at all. This is gt-9f67's
		// case and it must keep escalating.
		{"no record", CompletionRecord{}, "gt-wisp-ep0m", []string{"gt-sfcl"}},
		// Exits that are not completions. ESCALATED and DEFERRED both end a
		// session and neither means the work reached the queue.
		{"escalated", CompletionRecord{ExitType: "ESCALATED", MRID: "gt-wisp-ep0m", LastSourceIssue: "gt-sfcl"}, "gt-wisp-ep0m", []string{"gt-sfcl"}},
		{"deferred", CompletionRecord{ExitType: "DEFERRED", MRID: "gt-wisp-ep0m", LastSourceIssue: "gt-sfcl"}, "gt-wisp-ep0m", []string{"gt-sfcl"}},
		// A completion that made no MR covers no MR. gt done takes this road on
		// purpose against a closed source issue (gt-7qm/gt-46rk), and that is the
		// stranded-branch signature, not a hand-off.
		{"completed with no mr", CompletionRecord{ExitType: "COMPLETED", LastSourceIssue: "gt-sfcl"}, "gt-wisp-ep0m", []string{"gt-sfcl"}},
		// A record from an EARLIER episode. The callers find the cited MR by
		// branch, so a stale mr_id cannot match — this is what stops a reused
		// slot from inheriting the last occupant's completion.
		{"stale mr", live, "gt-wisp-newer", []string{"gt-sfcl"}},
		// Completed some other bead. Says nothing about the one attached now.
		{"covers a different bead", live, "gt-wisp-ep0m", []string{"gt-9tpw"}},
		// Covers one of two attached beads. Partial coverage is not coverage:
		// the uncovered one is exactly the work nobody has accounted for.
		{"covers only one of two held beads", live, "gt-wisp-ep0m", []string{"gt-sfcl", "gt-9tpw"}},
		// Nothing to cover. A surface that could not name the held work does not
		// get to waive a precondition about it — "I could not name it" is not
		// "there is none", which is the reading this whole area keeps producing.
		{"no held work named", live, "gt-wisp-ep0m", nil},
		{"held work named only as empty strings", live, "gt-wisp-ep0m", []string{"", "  "}},
		// No MR is being cited, so there is no leave-alone road to waive onto.
		{"no cited mr", live, "", []string{"gt-sfcl"}},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompletionCoverage(tt.rec, tt.citedMR, tt.heldWork...); got != "" {
				t.Fatalf("CompletionCoverage() = %q, want no coverage", got)
			}
		})
	}
}
