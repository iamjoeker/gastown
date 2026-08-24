package polecat

import "strings"

const (
	WorkstateVerdictWorking       = "WORKING"
	WorkstateVerdictSafeToNuke    = "SAFE_TO_NUKE"
	WorkstateVerdictPendingMR     = "PENDING_MR"
	WorkstateVerdictNeedsRecovery = "NEEDS_RECOVERY"
	WorkstateVerdictNeedsMQSubmit = "NEEDS_MQ_SUBMIT"

	// WorkstateVerdictNeedsStateClear is the answer for a polecat whose only
	// blocker is a deliberate paused agent_state (stuck, awaiting-gate, paused,
	// escalated). Nothing is at risk — git is clean, the hook is clear, the queue
	// is settled — but the pause is real and outlives every session restart,
	// because no restart path writes agent_state at all.
	//
	// It is deliberately NOT NeedsRecovery: there is nothing to recover, only a
	// field to clear, and routing it to "escalate" was half of what stranded the
	// slot. It is deliberately not SAFE_TO_NUKE either: reusing the slot silently
	// would discard a pause somebody set on purpose (gt-fbgq).
	WorkstateVerdictNeedsStateClear = "NEEDS_STATE_CLEAR"

	// WorkstateVerdictUnverified is the answer for a caller that never gathered
	// the git and merge-queue facts (ReuseFactsMeasured false). It is not a
	// claim that anything is wrong — it is the refusal to make a claim at all.
	// Every `verdict != SAFE_TO_NUKE` guard in the tree therefore fails closed
	// on it, which is the intent (gt-49dp).
	WorkstateVerdictUnverified = "UNVERIFIED"
)

// WorkstateInput contains the lifecycle, git, and merge-queue facts needed to
// classify a polecat consistently across list, recovery, witness, and capacity.
type WorkstateInput struct {
	State                          State
	SessionBusy                    bool
	HookBead                       string
	CleanupStatus                  CleanupStatus
	IgnoreCleanupStatus            bool
	PartialSpawnWithoutDurableHook bool
	PushFailed                     bool
	MRFailed                       bool
	Branch                         string
	GitDirty                       bool
	GitDirtyReason                 string
	StashCount                     int
	UnpushedCommits                int
	GitCheckFailed                 bool
	GitCheckFailedReason           string
	ActiveWorkBlocker              string
	ActiveWorkCountsTowardCapacity bool
	ActiveMR                       string
	ActiveMRBlocker                string
	MQCheckRequired                bool
	HasSubmittableWork             bool
	MQNotRequired                  bool
	AssignedBeadTerminal           bool
	MRSubmitted                    bool
	MQLookupFailed                 bool

	// PausedAgentState is the agent bead's agent_state when that state is a
	// deliberate pause (beads.AgentState.IsPaused: stuck, awaiting-gate, paused,
	// escalated). Empty otherwise.
	//
	// It is its own field rather than another ActiveWorkBlocker string because a
	// pause is not active work and must not be reported as such: it counts
	// against nothing, and its remedy is `gt polecat clear-state`, not recovery.
	// Only the inventory surface used to read agent_state at all, which is how
	// one polecat answered SAFE_TO_NUKE / witness_action=restart to
	// check-recovery and NEEDS_RECOVERY / agent_state=stuck to `gt polecat list`
	// in the same instant (gt-fbgq).
	PausedAgentState string

	// PushFailedRefuted records that this caller MEASURED the polecat's git state
	// and found nothing a failed push could have lost: no uncommitted work, no
	// stashes, and every commit's patch already preserved on the remote — with
	// the preservation check having actually run, rather than having returned its
	// zero value after an error.
	//
	// It exists because push_failed is not the claim its name makes. It is set
	// from the exit status of one `git push`, and a rebase makes a
	// non-fast-forward rejection there the EXPECTED outcome rather than a
	// failure. Measured on gastown/brahmin twice in 45 minutes: `gt polecat
	// git-state` said clean / 0 unpushed and the branch's commit was already an
	// ancestor of origin/main, in the same instant the flag said a push had
	// failed — and the flag won, routing a polecat with nothing at risk to
	// NEEDS_RECOVERY with escalation as its only prescribed action, whose only
	// remedy was a Mayor editing the field by hand (gt-3bzt).
	//
	// So the flag blocks only while it is unrefuted. This is deliberately not a
	// blanket exemption: an unmeasured caller leaves the field false and
	// push_failed keeps blocking, exactly as before. Only a caller that ran the
	// git checks has earned the right to contradict it, and the merge-queue tail
	// below still runs — pushed-but-unsubmitted work reaches NEEDS_MQ_SUBMIT
	// rather than being waved through.
	PushFailedRefuted bool

	// MRRefused is the agent bead's record that gt done deliberately created no
	// merge request because the source issue was already closed, leaving a
	// pushed branch outside the queue (gt-46rk). Unlike every other merge-queue
	// input here it needs no git or queue lookup, so surfaces too cheap to set
	// MQCheckRequired can still report the condition instead of answering
	// SAFE_TO_NUKE to a question they never asked.
	MRRefused bool

	// ReuseFactsMeasured records that this caller actually ran the git and
	// merge-queue checks the reuse gate runs — CurrentBranch, uncommitted work,
	// branch preservation, and the merge-request lookup — rather than answering
	// from agent-bead fields alone.
	//
	// It exists because sharing DecideWorkstate bought the APPEARANCE of a
	// single source of truth without the substance (gt-49dp). `gt polecat list`
	// and the FindIdlePolecat reuse gate do share this classifier, but they
	// build its input with two independent constructors that differ in eleven
	// fields: the list surface runs no git at all and deliberately leaves
	// MQCheckRequired false. Both then fell through to the same tail below, so
	// the same polecat read "idle-preserved / reusable" to the operator and
	// "idle-recovery-needed / mq-not-submitted" to the allocator, with nothing
	// in either output admitting one of them had never looked.
	//
	// The zero value is "not measured" on purpose. A surface that forgets to
	// set this gets UNVERIFIED — visibly useless — instead of a confident
	// answer it did not earn, which is the failure mode this field is for.
	ReuseFactsMeasured bool
}

// WorkstateDisposition is the canonical polecat lifecycle decision. It is pure
// policy: callers gather facts, this classifier decides how every subsystem
// should present and count the polecat.
type WorkstateDisposition struct {
	Verdict              string   `json:"verdict"`
	Reason               string   `json:"reason,omitempty"`
	Reusable             bool     `json:"reusable"`
	SafeToNuke           bool     `json:"safe_to_nuke"`
	NeedsRecovery        bool     `json:"needs_recovery"`
	NeedsMQSubmit        bool     `json:"needs_mq_submit"`
	NeedsStateClear      bool     `json:"needs_state_clear,omitempty"`
	MQStatus             string   `json:"mq_status,omitempty"`
	CountsTowardCapacity bool     `json:"counts_toward_capacity"`
	ReuseStatus          string   `json:"reuse_status,omitempty"`
	Blockers             []string `json:"blockers,omitempty"`
}

// DecideWorkstate returns the canonical disposition for a polecat.
func DecideWorkstate(in WorkstateInput) WorkstateDisposition {
	// Session liveness outranks every bead-derived fact below (gt-5tg).
	//
	// Every other input here is read from the agent bead and from git, and both
	// are written EARLY in the completion sequence: `gt done` records agent_state
	// and clears the work bead before it pushes, submits the MR, and exits. For
	// the one-to-two minutes between those writes and the session actually
	// ending, the bead says the polecat is finished while the pane still shows
	// the agent mid-turn — so the predicates below all pass and the verdict comes
	// out SAFE_TO_NUKE. That was reproduced 3/3 on two polecats inside seven
	// minutes, and it is the window in which a polecat's output exists in the
	// fewest places: it is still closing beads, writing notes, and pushing.
	//
	// SessionBusy carries positive evidence (Tmux.IsBusy) that the agent is
	// generating right now. When it is set, no bead-state disposition is
	// meaningful, so report WORKING — the same disposition StateWorking gets
	// below — and let the caller re-check once the pane goes quiet. An unknown
	// or unreadable session leaves this false and behavior is unchanged.
	if in.SessionBusy {
		return WorkstateDisposition{
			Verdict:              WorkstateVerdictWorking,
			Reason:               "session-busy",
			CountsTowardCapacity: true,
			Blockers:             []string{"session_state=busy (agent mid-turn)"},
		}
	}

	if in.ActiveMRBlocker != "" && !in.PushFailed && !in.MRFailed && in.State == StateDone {
		return WorkstateDisposition{
			Verdict:     WorkstateVerdictPendingMR,
			Reason:      "active-mr-open",
			ReuseStatus: "idle-pr-open",
			Blockers:    []string{in.ActiveMRBlocker},
		}
	}

	// StateDone (agent_state=done, seen before a polecat's own idle transition
	// lands) falls through to the real predicate checks below instead of
	// bailing out here — otherwise a merged/clean polecat gets NEEDS_RECOVERY
	// with no blockers, disagreeing with git-state for no reason (gt-check-recovery-bug).
	if in.State != StateIdle && in.State != StateDone {
		verdict := WorkstateVerdictNeedsRecovery
		needsRecovery := true
		if in.State == StateWorking {
			verdict = WorkstateVerdictWorking
			needsRecovery = false
		}
		d := WorkstateDisposition{
			Verdict:              verdict,
			Reason:               "not-idle",
			NeedsRecovery:        needsRecovery,
			CountsTowardCapacity: true,
		}
		if in.ActiveWorkBlocker != "" {
			d.Blockers = append(d.Blockers, in.ActiveWorkBlocker)
		}
		return d
	}

	d := WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke}
	capacityBlocked := false
	block := func(reason, blocker string, countsTowardCapacity bool) {
		if d.Reason == "" {
			d.Reason = reason
		}
		if blocker != "" {
			d.Blockers = append(d.Blockers, blocker)
		}
		capacityBlocked = capacityBlocked || countsTowardCapacity
	}

	if in.HookBead != "" && !in.PartialSpawnWithoutDurableHook {
		block("hook-still-set", "has work on hook ("+in.HookBead+")", true)
	}
	// Refuted only by measurement, never by silence: see PushFailedRefuted.
	if in.PushFailed && !in.PushFailedRefuted {
		block("push-failed", "push_failed=true", true)
	}
	if in.MRFailed {
		block("mr-failed", "mr_failed=true", true)
	}
	if in.ActiveWorkBlocker != "" {
		block("active-work", in.ActiveWorkBlocker, in.ActiveWorkCountsTowardCapacity)
	}
	if !in.IgnoreCleanupStatus && !in.CleanupStatus.IsSafe() {
		reason := "cleanup-" + string(in.CleanupStatus)
		blocker := "cleanup_status=" + string(in.CleanupStatus)
		if in.CleanupStatus == "" {
			reason = "cleanup-unknown"
			blocker = "cleanup_status=<missing>"
		} else if in.CleanupStatus == CleanupUnknown {
			reason = "cleanup-unknown"
		}
		block(reason, blocker, true)
	}
	if in.GitCheckFailed {
		blocker := in.GitCheckFailedReason
		if blocker == "" {
			blocker = "git_state=unknown"
		}
		block("git-check-failed", blocker, true)
	}
	if in.GitDirty {
		blocker := in.GitDirtyReason
		if blocker == "" {
			blocker = "git_state=has_uncommitted"
		}
		block("git-dirty", blocker, true)
	}
	if in.StashCount > 0 {
		block("git-stash", "git_state=has_stash stash_count="+itoa(in.StashCount), true)
	}
	if in.UnpushedCommits > 0 {
		block("git-unpushed", "git_state=has_unpushed unpushed_commits="+itoa(in.UnpushedCommits), true)
	}
	activeMRBlocks := in.ActiveMRBlocker != ""
	if activeMRBlocks {
		block("active-mr-open", in.ActiveMRBlocker, false)
	}
	// A deliberate pause is the LOWEST-priority blocker, so it is not fed through
	// block() with the rest: work at risk outranks it, an open MR outranks it,
	// and — the part that matters — the merge-queue tail below outranks it too.
	// Blocking here would return before that tail ever ran, and a stuck polecat
	// with work still outside the queue would be reported as a field to clear
	// rather than as work to rescue. It is reported alongside whatever does
	// block, and decides the verdict only when nothing else does.
	pausedBlocker := ""
	if in.PausedAgentState != "" {
		pausedBlocker = "agent_state=" + in.PausedAgentState
	}

	if len(d.Blockers) > 0 {
		// Counted before the pause is appended: the pause must not turn a
		// leave-alone PENDING_MR into a NEEDS_RECOVERY escalation.
		mrOnly := activeMRBlocks && len(d.Blockers) == 1
		if pausedBlocker != "" {
			d.Blockers = append(d.Blockers, pausedBlocker)
		}
		if mrOnly {
			d.Verdict = WorkstateVerdictPendingMR
			d.ReuseStatus = "idle-pr-open"
			return d
		}
		d.Verdict = WorkstateVerdictNeedsRecovery
		d.NeedsRecovery = true
		d.CountsTowardCapacity = capacityBlocked
		d.ReuseStatus = "idle-recovery-needed"
		return d
	}

	if in.MQCheckRequired {
		if in.MQLookupFailed {
			d.Verdict = WorkstateVerdictNeedsRecovery
			d.Reason = "mq-lookup-failed"
			d.NeedsRecovery = true
			d.MQStatus = "unknown"
			d.CountsTowardCapacity = true
			d.ReuseStatus = "idle-recovery-needed"
			d.Blockers = append(d.Blockers, "mq_status=unknown")
			return d
		} else if !in.HasSubmittableWork || in.MQNotRequired {
			d.MQStatus = "not_required"
		} else if in.MRSubmitted {
			d.MQStatus = "submitted"
		} else {
			d.Verdict = WorkstateVerdictNeedsMQSubmit
			d.Reason = "mq-not-submitted"
			d.NeedsRecovery = true
			d.NeedsMQSubmit = true
			d.MQStatus = "not_submitted"
			d.CountsTowardCapacity = true
			d.ReuseStatus = "idle-recovery-needed"
			d.Blockers = append(d.Blockers, "mq_status=not_submitted")
			return d
		}
	}

	// A recorded MR refusal outranks the absence of merge-queue facts. gt done
	// takes this path on purpose (gt-7qm: no MR against a closed source issue),
	// and on purpose leaves MRFailed false so the hook clears and the session
	// retires — so nothing above this line has any reason to block, and the
	// polecat reads SAFE_TO_NUKE while its pushed branch sits outside the queue.
	// Recycling force-deletes branches, so that verdict is the destructive one.
	//
	// Suppressed only by proof, never by silence: an MR that now exists for the
	// branch, or a surface that actually ran the merge-queue check and found
	// nothing left to submit. A surface that never looked does not get to
	// conclude the work is safe (gt-46rk).
	if in.MRRefused && !in.MRSubmitted {
		checked := in.MQCheckRequired && !in.MQLookupFailed
		resolved := checked && (!in.HasSubmittableWork || in.MQNotRequired)
		if !resolved {
			d.Verdict = WorkstateVerdictNeedsMQSubmit
			d.Reason = "mq-refused-closed-source"
			d.NeedsRecovery = true
			d.NeedsMQSubmit = true
			d.MQStatus = "refused_closed_source"
			d.CountsTowardCapacity = true
			d.ReuseStatus = "idle-recovery-needed"
			d.Blockers = append(d.Blockers, "mq_status=refused_closed_source (gt done made no MR: source issue was closed)")
			return d
		}
	}

	// Nothing at risk and nothing queued — so the pause is genuinely all that
	// stands between this polecat and reuse. Say so, and name the one action that
	// changes it. `gt session restart` is not that action: no restart path writes
	// agent_state, so prescribing it here produced a remedy that provably could
	// not work and a slot whose disposition never moved (gt-fbgq).
	//
	// Gated on ReuseFactsMeasured for the same reason SAFE_TO_NUKE is: the pause
	// itself is a bead fact any surface can read, but "nothing else blocks" is a
	// claim only a caller that ran git and the merge-queue lookup has earned. An
	// unmeasured caller falls through to UNVERIFIED below, carrying the pause in
	// its blockers so the fact still surfaces.
	if pausedBlocker != "" && in.ReuseFactsMeasured {
		d.Verdict = WorkstateVerdictNeedsStateClear
		d.Reason = "agent-state-paused"
		d.NeedsStateClear = true
		d.ReuseStatus = "idle-state-paused"
		d.Blockers = append(d.Blockers, pausedBlocker)
		return d
	}

	// Nothing above blocked. That is only an answer if the blockers above were
	// ever evaluated against gathered facts: eleven of this input's fields are
	// git and merge-queue facts, and a bead-only surface leaves every one of
	// them at the zero value, which reads here as "no blocker found" and is
	// indistinguishable from "looked and found none" (gt-49dp).
	//
	// So the tail splits. A caller that measured gets the disposition it earned;
	// a caller that did not gets UNVERIFIED and says so in its blockers, instead
	// of printing the operator the same "idle-preserved" string the reuse gate
	// prints for a polecat it has actually cleared. The refusal to answer is not
	// a recovery condition and is not counted against capacity — it is a caller
	// that must go measure, not a polecat that must be repaired.
	if !in.ReuseFactsMeasured {
		d.Verdict = WorkstateVerdictUnverified
		d.Reason = "reuse-facts-unmeasured"
		d.ReuseStatus = "idle-unverified"
		if pausedBlocker != "" {
			d.Blockers = append(d.Blockers, pausedBlocker)
		}
		d.Blockers = append(d.Blockers, "reuse_facts=unmeasured (no git or merge-queue check was run for this polecat)")
		return d
	}

	d.Reusable = true
	d.SafeToNuke = true
	d.Reason = "reusable"
	if strings.HasPrefix(in.Branch, "polecat/") {
		d.ReuseStatus = "idle-preserved"
	} else {
		d.ReuseStatus = "idle-clean"
	}
	return d
}

// ApplyBranchMRToWorkstateInput folds a merge-request bead found by looking up
// the polecat's BRANCH into the input. Callers pass the MR's ID and whether it
// is still open.
//
// The branch is the join key that survives; the agent bead's active_mr field is
// not. gt done writes that field, and nothing else does — so an MR someone else
// submitted for a stranded branch leaves it empty, and the polecat reports
// SAFE_TO_NUKE while recycling stands ready to force-delete the only branch the
// open MR points at (gt-46rk). A stored active_mr still wins when present: it
// carries provenance this lookup cannot, and the caller has already assessed it.
func ApplyBranchMRToWorkstateInput(in *WorkstateInput, mrID string, mrOpen bool) {
	if in == nil || mrID == "" {
		return
	}
	in.MRSubmitted = true
	if in.ActiveMR != "" || !mrOpen {
		return
	}
	in.ActiveMR = mrID
	in.ActiveMRBlocker = "active_mr=" + mrID + " status=open source=branch-lookup"
}

// GitFactsRefutePushFailed reports whether measured git facts leave nothing a
// failed push could have lost, and is the single definition of that bar for
// every caller that fills WorkstateInput.PushFailedRefuted.
//
// gitMeasured is the caller's own statement that all four inputs come from
// checks that RAN. It is separate from the values because a check that errored
// returns the same zeros as a clean worktree, and "0 unpreserved patches"
// arrived at that way is the false zero this predicate exists to not act on.
func GitFactsRefutePushFailed(gitMeasured, gitCheckFailed, gitDirty bool, stashCount, unpushedCommits int) bool {
	return gitMeasured && !gitCheckFailed && !gitDirty && stashCount == 0 && unpushedCommits == 0
}

// CanIgnoreStaleCleanupStatus returns true when a dirty persisted
// cleanup_status is older than the direct predicates proving no work is at risk.
// The status remains unsafe globally; callers must opt into this reconciliation
// path only after gathering live git, hook, work, and active-MR facts.
func CanIgnoreStaleCleanupStatus(status CleanupStatus, workTerminal, hookSafe, activeMRSafe, gitSafe bool) bool {
	if !workTerminal || !hookSafe || !activeMRSafe || !gitSafe {
		return false
	}
	switch status {
	case CleanupUncommitted, CleanupStash, CleanupUnpushed:
		return true
	default:
		return false
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
