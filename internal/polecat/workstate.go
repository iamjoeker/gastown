package polecat

import "strings"

const (
	WorkstateVerdictWorking       = "WORKING"
	WorkstateVerdictSafeToNuke    = "SAFE_TO_NUKE"
	WorkstateVerdictPendingMR     = "PENDING_MR"
	WorkstateVerdictNeedsRecovery = "NEEDS_RECOVERY"
	WorkstateVerdictNeedsMQSubmit = "NEEDS_MQ_SUBMIT"

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
	if in.PushFailed {
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

	if len(d.Blockers) > 0 {
		if activeMRBlocks && len(d.Blockers) == 1 {
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
