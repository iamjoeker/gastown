package polecat

import (
	"fmt"
	"strings"
	"time"
)

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

	// WorkstateVerdictNeedsLogin is the answer for a polecat whose pane
	// demonstrably shows an auth wall: the agent has no usable credentials and
	// is waiting on a human to run /login.
	//
	// It is its own verdict because every existing one is wrong for it, and the
	// wrongest is the one it used to get. A logged-out polecat still holds a
	// hooked bead, so the bead-derived road reported WORKING — and WORKING
	// prescribes leave-alone. gastown/foundation was reported WORKING by both
	// `gt rig status` and `gt polecat check-recovery` for eighteen minutes while
	// its pane read "Not logged in · Run /login", and the same variable left the
	// Deacon logged out for 3.27 days with nothing noticing (hq-nms9g, gt-acb1).
	// Hooked-ness was being read as liveness.
	//
	// It is deliberately not NEEDS_RECOVERY: nothing is at risk in git or the
	// queue, and the recovery vocabulary would send a reader looking for lost
	// work that does not exist. It is deliberately not SAFE_TO_NUKE either — the
	// polecat is holding a slot it cannot work in, and destroying it neither
	// recovers the slot's account nor stops the next session from coming up the
	// same way.
	//
	// Restart is specifically NOT the remedy, which is why this verdict routes to
	// escalate: no restart path can supply credentials, so restarting produces
	// another logged-out session. That is exactly the loop gt-acb1 documents.
	WorkstateVerdictNeedsLogin = "NEEDS_LOGIN"

	// WorkstateVerdictSuspectStall is the answer for a polecat whose pane shows
	// a turn in flight that is consuming no tokens, with nothing visible that
	// would explain the wait.
	//
	// It exists because it is the one state a working/parked binary can NEVER
	// surface. A wedged agent still renders the busy marker, so SessionBusy
	// reads it as generating and prescribes leave-alone; its bead still says
	// working; its session is alive. Every input says healthy while the agent
	// executes nothing. The token counter is the only surface that moves, and
	// only across a window long enough to resolve it (tmux.MinLivenessWindow).
	//
	// It is deliberately NARROWER than "tokens are static". Tokens-static means
	// not thinking, which is the normal and healthy shape of an agent inside a
	// `sleep` or a long test run — the majority of blocking waits. What makes
	// this one a fault is that the pane shows no command in flight to be waiting
	// on. Escalating on tokens-static alone is the false alarm the detector was
	// asked to avoid, not the defect it was asked to catch (gt-y39t).
	//
	// It routes to escalate rather than restart: whether the agent is genuinely
	// wedged or merely between tool calls in a way the pane did not render is
	// not something this evidence settles, and restart destroys the agent's
	// context. A human or the Mayor looking at the pane settles it in seconds.
	WorkstateVerdictSuspectStall = "SUSPECT_STALL"

	// WorkstateVerdictUnverified is the answer for a caller that never gathered
	// the git and merge-queue facts (ReuseFactsMeasured false). It is not a
	// claim that anything is wrong — it is the refusal to make a claim at all.
	// Every `verdict != SAFE_TO_NUKE` guard in the tree therefore fails closed
	// on it, which is the intent (gt-49dp).
	WorkstateVerdictUnverified = "UNVERIFIED"
)

// Reason strings. They are named because callers RENDER prose off them, and the
// two WORKING reasons rest on completely different evidence: session-busy read
// the pane, not-idle read the agent bead. `gt polecat check-recovery` printed
// "The agent's pane shows it mid-turn" for both, which is a claim about a pane
// that the not-idle road never looked at — twice measured wrong on a parked
// session, once measured right on a live one, with nothing in the output telling
// the two apart (gt-mkpm).
const (
	// WorkstateReasonSessionBusy means Tmux.IsBusy returned positive evidence
	// that the agent is generating right now. This one HAS read the pane.
	WorkstateReasonSessionBusy = "session-busy"

	// WorkstateReasonNotIdle means the lifecycle state carried in the input was
	// something other than idle/done/handed-off. It is a bead-derived fact and
	// says nothing about what the pane is doing.
	WorkstateReasonNotIdle = "not-idle"

	// WorkstateReasonSessionLoggedOut means Tmux.IsLoggedOut returned positive
	// evidence that the agent is sitting at an auth wall. Like session-busy this
	// one HAS read the pane; unlike it, the state it names does not clear on its
	// own.
	WorkstateReasonSessionLoggedOut = "session-logged-out"

	// WorkstateReasonSessionSuspectStall means the pane was sampled TWICE, at
	// least tmux.MinLivenessWindow apart, and the agent's clock climbed while
	// its token counter did not — with no command in flight to explain the wait.
	//
	// It is the only reason string here that rests on a measurement over time
	// rather than on a single reading, which is why it names the window in the
	// blocker it produces. A reader who cannot see the window cannot tell this
	// apart from a snap judgement, and a snap judgement on this signal is wrong
	// most of the time (gt-y39t).
	WorkstateReasonSessionSuspectStall = "session-suspect-stall"

	// WorkstateReasonStalledPendingMR means `tmux has-session` returned NO
	// SESSION for a polecat that is still holding work, while a merge request
	// for its branch is open. The MR is real; the hand-off it implies is not.
	//
	// It is its own reason because the two roads into a pending MR look
	// identical in every bead fact and mean opposite things. A polecat that ran
	// `gt done` clears its hook on the way out, so the open MR is the last trace
	// of a session that ENDED IN COMPLETION — leave-alone is right. A polecat
	// that DIED still holds its hook, and an MR submitted on its behalf (the
	// standing remedy for a convoy deadlock) produces the same open MR over a
	// session that ended in death — leave-alone is wrong, and it is wrong for as
	// long as the MR is in flight, which is exactly the window a witness is most
	// likely to be looking in (gt-9f67).
	WorkstateReasonStalledPendingMR = "stalled-session-pending-mr"
)

// SessionPresence is what a direct `tmux has-session` on the polecat's session
// returned: does a session EXIST at all.
//
// It is deliberately named apart from SessionLiveness, which asks what a
// session that exists is DOING (working, blocking-wait, logged-out, parked).
// Every one of those readings presupposes a pane to read, and that function
// returns its zero value for both "no session" and "could not read the pane".
// The two questions had one word between them, and one of them had no field.
//
// It is a TRI-STATE on purpose. The other session inputs here (SessionBusy,
// SessionLoggedOut, SessionSuspectStall) are bools carrying positive evidence
// only, so "no session", "nobody asked" and "tmux errored" all arrive as false
// and are indistinguishable — which is fine for those, whose whole contract is
// "believe me only when I say yes", and fatal for this one, whose entire value
// is in the NEGATIVE answer. A bool would make an absent session look exactly
// like an unmeasured one, which is the shape of the defect this field fixes.
//
// Only SessionAbsent decides anything below. Unknown never does: a surface that
// did not look, or looked and could not tell, must not have its silence read as
// proof the agent is gone.
type SessionPresence string

const (
	// SessionPresenceUnknown is the zero value: nobody ran the check, or the
	// check itself failed. It is not evidence in either direction.
	SessionPresenceUnknown SessionPresence = ""

	// SessionPresent means the session exists right now.
	SessionPresent SessionPresence = "present"

	// SessionAbsent means the check RAN and the session does not exist.
	SessionAbsent SessionPresence = "absent"
)

// Reuse-status strings — the vocabulary `gt polecat list` prints as
// "reuse: <status>" and the reuse gate records.
//
// They are named here because two of them mean "nobody looked" and the rest
// mean "somebody looked and found this", and for a while the two categories
// shared a string. idle-recovery-needed was the value for BOTH "measured and
// blocked" and "a recorded MR refusal reached a surface that never consults the
// queue" — a verdict-shaped phrase naming a remedy that, on the second road,
// is not needed and cannot be performed. Two witnesses spent a night treating
// it as a stuck state, measured four remedies "failing" to clear it, and
// scoped a P1 on the misreading (gt-mkpm).
const (
	// ReuseStatusRecoveryNeeded: a blocker was found. Something must be repaired.
	ReuseStatusRecoveryNeeded = "idle-recovery-needed"

	// ReuseStatusMQUnchecked: gt done recorded that it made no merge request,
	// and the caller never consulted the merge queue to find out whether the
	// branch still holds work that needs submitting. Conservative like
	// idle-recovery-needed — the verdict and every flag are identical — but it
	// names the caller's gap rather than a defect in the polecat, so it cannot
	// be quoted out of a listing as a finding about the polecat.
	ReuseStatusMQUnchecked = "idle-mq-unchecked"

	// ReuseStatusUnverified: no git or merge-queue facts were gathered at all.
	ReuseStatusUnverified = "idle-unverified"

	// ReuseStatusPROpen: work is in the merge queue. Preserve until it lands.
	ReuseStatusPROpen = "idle-pr-open"

	// ReuseStatusStatePaused: only a deliberate agent_state pause stands.
	ReuseStatusStatePaused = "idle-state-paused"

	// ReuseStatusPreserved / ReuseStatusClean: measured, nothing blocking.
	ReuseStatusPreserved = "idle-preserved"
	ReuseStatusClean     = "idle-clean"
)

// ExitTypeCompleted is the exit_type `gt done` records on a polecat's agent bead
// when its completion sequence ran all the way through. Duplicated from the
// command layer rather than imported because internal/cmd imports this package.
const ExitTypeCompleted = "COMPLETED"

// CompletionRecord is the completion metadata `gt done` writes to the polecat's
// own agent bead at the end of its run: exit_type, mr_id, branch,
// last_source_issue, completion_time (gt-x7t9).
//
// It is the only durable, POSITIVE evidence anywhere that a session ended in
// completion rather than in death. Nothing else writes it, and
// ResetAgentBeadForReuse clears it when the slot is recycled.
type CompletionRecord struct {
	ExitType        string
	MRID            string
	LastSourceIssue string
	CompletionTime  string
}

// CompletionCoverage reports whether the polecat's own completion record covers
// the work still attached to it and the merge request about to be cited against
// it, and returns the evidence for saying so. Empty means it does not — which is
// the answer for every caller that cannot read the record at all.
//
// citedMR is the MR this verdict is about to name. heldWork is every bead ID the
// calling surface believes the polecat still holds; pass the issue-store and
// hook lookups, NEVER anything derived from the record itself, or this compares
// a field against its own source and passes for free (gt-eygw's shape).
//
// All three bindings are required, and each closes a different road:
//
//   - exit_type=COMPLETED, because ESCALATED and DEFERRED are exits too and
//     neither means the work reached the queue.
//   - mr_id equals the cited MR, because the callers find that MR BY BRANCH.
//     A record left over from an earlier episode names that episode's MR, and no
//     branch lookup on the current branch can return it.
//   - last_source_issue equals every held bead, because a record that completed
//     some OTHER bead says nothing about the one still attached now.
//
// At least one held bead is required. Returning coverage with nothing to cover
// would waive the precondition on a polecat whose held work the surface could
// not name, and "I could not name it" is not "there is none".
func CompletionCoverage(rec CompletionRecord, citedMR string, heldWork ...string) string {
	if !strings.EqualFold(strings.TrimSpace(rec.ExitType), ExitTypeCompleted) {
		return ""
	}
	mrID := strings.TrimSpace(rec.MRID)
	if mrID == "" || mrID != strings.TrimSpace(citedMR) {
		return ""
	}
	source := strings.TrimSpace(rec.LastSourceIssue)
	if source == "" {
		return ""
	}
	covered := false
	for _, held := range heldWork {
		held = strings.TrimSpace(held)
		if held == "" {
			continue
		}
		if held != source {
			return ""
		}
		covered = true
	}
	if !covered {
		return ""
	}
	evidence := fmt.Sprintf("completion_record=exit_type=%s mr_id=%s last_source_issue=%s",
		ExitTypeCompleted, mrID, source)
	if at := strings.TrimSpace(rec.CompletionTime); at != "" {
		evidence += " completion_time=" + at
	}
	return evidence
}

// sessionAbsentHoldingWork reports the gt-9f67 signature: `tmux has-session`
// RAN and found no session, and the polecat is nonetheless still holding work.
//
// Both halves are required, and each is doing a distinct job.
//
// Absent-and-measured, because Unknown is the answer a caller gets when it never
// looked or when tmux itself failed, and treating either as "the agent is gone"
// would route healthy polecats to escalation on no evidence — the same defect
// this fixes, pointed the other way.
//
// Still-holding-work, because a dead session on its own is the NORMAL end state
// of every polecat that ever finished. `gt done` pushes, submits, and exits, so
// "no session + open MR + no hook" is what success looks like and must keep
// reading PENDING_MR.
//
// The callers carry that work in THREE different fields — `gt polecat list` in
// ActiveWorkBlocker, check-recovery in HookBead, and the issue store's own
// hooked-bead lookup in AssignedWorkBead — so all three are checked. Reading
// fewer is not a smaller version of this fix, it is a fix that does not fire:
// the polecat this was measured on (gastown/deathclaw, gt-y39t hooked, session
// gone) had only the third one set.
//
// STILL-HOLDING-WORK IS NOT PROOF THE SEQUENCE WAS CUT SHORT, and this predicate
// was written believing it was: "gt done clears it". gt done clears the AGENT
// BEAD's hook_bead, and the third field is not that. AssignedWorkBead is the
// ISSUE STORE's hooked bead, and `gt done` leaves that one open ON PURPOSE when
// it submits an MR — "the refinery closes it on merge" (gt-429i). So the normal
// post-completion window is: session gone, hook_bead cleared, issue store still
// hooked, MR queued. The predicate fires on every polecat that succeeds, for the
// whole time its MR is in flight, and routes it to escalate (gt-n3jq).
//
// It was validated on that very case. gastown/deathclaw held gt-y39t hooked with
// its session gone, and deathclaw had run `gt done --pre-verified` and AUTHORED
// the commit that landed as 4392fa4db — the flip to NEEDS_RECOVERY/escalate
// recorded as evidence the fix worked was a false alarm on a healthy polecat.
//
// So the missing half is supplied where it actually lives, and only as positive
// evidence: the polecat's own completion record, matched to THIS episode's MR
// and THIS episode's bead (see CompletionCoverage). A polecat that wrote one is
// a polecat that reached the end of its own completion sequence — the thing
// gt-9f67 wanted the hook to stand in for, read from the field that means it.
//
// Everything gt-9f67 catches, it still catches. A polecat that DIED holding work
// wrote no completion record for that work, so it has no coverage, so the
// precondition stands and it still escalates. Coverage is empty for every caller
// that cannot read the record, which is the conservative direction.
func sessionAbsentHoldingWork(in WorkstateInput) bool {
	if in.SessionPresence != SessionAbsent {
		return false
	}
	if in.CompletionCoverage != "" {
		return false
	}
	holdsHook := in.HookBead != "" && !in.PartialSpawnWithoutDurableHook
	return holdsHook || in.ActiveWorkBlocker != "" || in.AssignedWorkBead != ""
}

// DispositionUnmeasured reports whether this disposition was reached WITHOUT the
// facts that would settle it, as opposed to by finding something. Both roads
// present as a blocking verdict, and a reader cannot tell them apart from the
// verdict alone — which is what this predicate exists to let surfaces say out
// loud instead of leaving to the reader (gt-mkpm).
func DispositionUnmeasured(d WorkstateDisposition) bool {
	return d.ReuseStatus == ReuseStatusUnverified || d.ReuseStatus == ReuseStatusMQUnchecked
}

// WorkstateInput contains the lifecycle, git, and merge-queue facts needed to
// classify a polecat consistently across list, recovery, witness, and capacity.
type WorkstateInput struct {
	State            State
	SessionBusy      bool
	SessionLoggedOut bool
	SessionPresence  SessionPresence

	// SessionSuspectStall is the two-sample pane measurement described at
	// WorkstateVerdictSuspectStall: the agent's clock climbed while its token
	// counter did not, and no command was in flight.
	//
	// It is a separate field from SessionBusy rather than a refinement of it
	// because a caller that did not sample twice must leave it false and get
	// exactly the old behaviour. Only tmux.LivenessReading.SuspectStall should
	// set it; that predicate is already false for a single-sample read, so a
	// cheap caller cannot accidentally arm this.
	SessionSuspectStall bool

	// SessionStallWindow is how far apart the two samples behind
	// SessionSuspectStall were taken. Reported in the blocker so the claim can
	// be read as the measurement it is (gt-y39t).
	SessionStallWindow time.Duration

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

	// AssignedWorkBead is the ISSUE STORE's answer to "what hooked, non-terminal
	// work does this polecat hold" — the bead found by querying for hooked issues
	// assigned to it, not a field copied off its agent bead.
	//
	// It exists because the two are not the same fact and can disagree, and the
	// disagreement is silent. gastown/deathclaw held gt-y39t at status HOOKED,
	// assigned to gastown/polecats/deathclaw, with its session gone — and
	// check-recovery reported one blocker, the open MR, because the only hook
	// surface it fed into this classifier was the agent bead's hook_bead field,
	// which was empty. The lifecycle detection HAD read the issue store (that is
	// where its "stalled" came from, and the whole reason an MR could promote it
	// to "handed-off"), and then the answer went nowhere (gt-9f67).
	//
	// It is consumed by exactly one predicate — sessionAbsentHoldingWork — and
	// deliberately blocks nothing on its own. A polecat holding hooked work with
	// a LIVE session is a working polecat, which the State road already handles;
	// promoting this to a general blocker would refuse cleanup on a surface that
	// currently allows it, which is a much larger change than the one this bead
	// asks for.
	AssignedWorkBead string

	// CompletionCoverage is the evidence, in the polecat's OWN completion record,
	// that the session ending was completion and that the record covers the work
	// still attached and the merge request about to be cited. Empty means no such
	// evidence — including for every caller that never read the record.
	//
	// Produce it with CompletionCoverage(); do not assemble the string by hand.
	// The point of routing it through one constructor is that a surface cannot
	// claim coverage without having matched the record to this episode's MR and
	// this episode's bead, and cannot claim it without saying what it matched.
	//
	// Consumed by exactly one predicate — sessionAbsentHoldingWork — where it
	// supplies the half gt-9f67 inferred from a cleared hook and gt-429i had
	// already made untrue. It is also REPORTED wherever it decides, because a
	// waived precondition that leaves no trace is the same reading problem as an
	// unexplained one.
	CompletionCoverage string

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
	NeedsLogin           bool     `json:"needs_login,omitempty"`
	MQStatus             string   `json:"mq_status,omitempty"`
	CountsTowardCapacity bool     `json:"counts_toward_capacity"`
	ReuseStatus          string   `json:"reuse_status,omitempty"`
	Blockers             []string `json:"blockers,omitempty"`
}

// DecideWorkstate returns the canonical disposition for a polecat.
//
// EVERY PREDICATE THAT CAN PRODUCE ReuseStatus "idle-recovery-needed", because
// that one display string is a projection over thirteen of them and the
// projection hides which one is set. Two rigs reached it by different roads and
// a fix validated on one looked like a fix for both (gt-uapr). This function is
// the only producer of the string — grep it and you will find these five sites,
// no more:
//
//	(a) the general blocker tail, when at least one of these blocked and the
//	    blockers are not an open MR alone:
//	      hook-still-set      HookBead set and not a partial spawn
//	      push-failed         PushFailed && !PushFailedRefuted
//	      mr-failed           MRFailed
//	      active-work         ActiveWorkBlocker set
//	      cleanup-<status>    CleanupStatus unsafe and not ignored
//	      git-check-failed    GitCheckFailed
//	      git-dirty           GitDirty
//	      git-stash           StashCount > 0
//	      git-unpushed        UnpushedCommits > 0
//	(b) mq-lookup-failed          MQCheckRequired && MQLookupFailed
//	(c) mq-not-submitted          MQCheckRequired, submittable work, no MR
//	(d) mq-refused-closed-source  MRRefused && !MRSubmitted, unresolved
//	(e) stalled-session-pending-mr  an open MR over a MEASURED-absent session that
//	    is still holding work — see sessionAbsentHoldingWork
//
// PausedAgentState is deliberately NOT on that list: it produces
// "idle-state-paused" / NEEDS_STATE_CLEAR, and folding it in here is what sent a
// polecat needing one field cleared down the escalation road (gt-fbgq).
//
// Only a caller that sets ReuseFactsMeasured reaches any of this as a claim; an
// unmeasured one gets "idle-unverified", which is a refusal to answer rather
// than a recovery condition (gt-49dp).
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
	// Checked BEFORE SessionBusy, which is the whole reason it can fire at all.
	//
	// A wedged agent renders the busy marker exactly like a generating one — the
	// marker says a turn is open, not that anything is happening inside it — so
	// SessionBusy is TRUE for every case this catches. Ordering this second
	// would make it unreachable, and the resulting verdict (WORKING /
	// leave-alone) is precisely the one that let a stuck agent sit unnoticed.
	//
	// The two checks are not in tension despite looking like it. SessionBusy
	// answers "is a turn open" from one snapshot; this answers "is anything
	// moving inside it" from two, a minute or more apart. The second question
	// strictly refines the first, so where they disagree the refinement is the
	// one carrying more evidence — and it is the only one of the two that could
	// have been armed by a caller that paid for the measurement.
	// The !SessionLoggedOut guard is here rather than expressed as ordering
	// because the auth wall must beat this one while still LOSING to
	// SessionBusy, and no single ordering of three arms gives both.
	//
	// The two cannot co-occur in one reading — a logged-out pane has no turn
	// open, so it classifies as logged-out and never as blocking-wait. They can
	// co-occur across two, because check-recovery reads the auth wall and the
	// token delta from different captures taken a minute or more apart, which is
	// exactly long enough for a session to hit its auth wall in between. When
	// they disagree the auth wall wins: it names a remedy (a human at a browser)
	// while this verdict only says to go look, and "go look" at a pane that
	// already says "Please run /login" wastes the one reading that was clear.
	if in.SessionSuspectStall && !in.SessionLoggedOut {
		return WorkstateDisposition{
			Verdict:              WorkstateVerdictSuspectStall,
			Reason:               WorkstateReasonSessionSuspectStall,
			CountsTowardCapacity: true,
			Blockers: []string{fmt.Sprintf(
				"session_state=blocking-wait (turn open, token counter static across %s, no command in flight)",
				in.SessionStallWindow)},
		}
	}

	if in.SessionBusy {
		return WorkstateDisposition{
			Verdict:              WorkstateVerdictWorking,
			Reason:               WorkstateReasonSessionBusy,
			CountsTowardCapacity: true,
			Blockers:             []string{"session_state=busy (agent mid-turn)"},
		}
	}

	// The auth wall is read from the pane too, and it is checked here — after
	// busy, before every bead-derived fact — because it must beat exactly one
	// road and lose to exactly one other.
	//
	// It loses to SessionBusy on purpose. Both are pane reads of the same
	// snapshot and they should be mutually exclusive (an agent at an auth wall is
	// not generating), so reaching both means one of the two markers was matched
	// in text rather than in the status bar. Busy is the safer of the two to
	// believe, because its prescription is leave-alone.
	//
	// It beats the State road below, which is the whole point. A logged-out
	// polecat keeps whatever lifecycle state it had when the credentials expired
	// — usually working, since it was dispatched — so the bead-derived road
	// reports WORKING and prescribes leave-alone, and the pane it never consulted
	// is the only place the truth is written. That is how eighteen minutes and
	// 3.27 days of a dead agent both read as healthy (gt-acb1, hq-nms9g).
	if in.SessionLoggedOut {
		return WorkstateDisposition{
			Verdict:              WorkstateVerdictNeedsLogin,
			Reason:               WorkstateReasonSessionLoggedOut,
			NeedsLogin:           true,
			CountsTowardCapacity: true,
			Blockers:             []string{"session_state=logged-out (agent has no credentials; needs a human /login)"},
		}
	}

	// Liveness is a PRECONDITION of the leave-alone verdict below, not another
	// fact weighed against it.
	//
	// Everything from here down is bead-derived and queue-derived, and an open
	// merge request is the strongest of those facts: it promotes a detected
	// "stalled" to "handed-off" and takes the whole road to PENDING_MR /
	// leave-alone. That promotion's only evidence is the MR. It never asks
	// whether the polecat is alive — and gastown/chrome oscillated
	// stalled -> handed-off -> SAFE_TO_NUKE across three readings in which
	// NOTHING ABOUT THE POLECAT CHANGED. `tmux has-session` said DEAD at all
	// three (control: a live polecat, which the same probe called ALIVE), while
	// the verdict tracked the MR and the hook (gt-9f67).
	//
	// The signature is narrow on purpose, because the rule it guards is a GOOD
	// rule: restarting a polecat whose work is sitting in the queue is exactly
	// the wrong move, and PENDING_MR prevents it. So this does not weaken it. It
	// requires the one thing "handed off" claims and an MR cannot show — that
	// the session ended in COMPLETION. A polecat that ran `gt done` clears its
	// hook on the way out, so it reaches PENDING_MR here unchanged. A polecat
	// still HOLDING WORK with NO SESSION did not complete; it died, and the MR
	// belongs to whoever submitted on its behalf.
	//
	// That remedy is the one this most matters for. Submitting for a dead
	// polecat is the standing fix for a convoy deadlock and should continue —
	// but until now it blinded the witness to that polecat for as long as the MR
	// was in flight, so the remedy for one defect triggered the other.
	//
	// ESCALATE, not restart and not leave-alone: the work is in the queue, so
	// there is nothing to restart into, and the session is gone, so there is
	// nobody to leave alone.
	//
	// It is computed here and consumed in two places — the leave-alone arm
	// immediately below and the general blocker tail — rather than returning a
	// verdict of its own, because returning here would DROP every other blocker
	// the caller gathered. A polecat can be dead, holding a hook, carrying an
	// open MR and carrying push_failed all at once; the reader needs all four.
	// So this suppresses leave-alone and adds one blocker; it never replaces the
	// others.
	//
	// An open MR is part of the signature, not incidental to it. Without one
	// there is no leave-alone road to guard — a dead session holding work is
	// StateStalled and already escalates — and a reason named
	// "stalled-session-pending-mr" over a polecat with no pending MR would be a
	// confident sentence about a thing that is not there.
	stalledUnderPendingMR := in.ActiveMRBlocker != "" && sessionAbsentHoldingWork(in)

	if in.ActiveMRBlocker != "" && !in.PushFailed && !in.MRFailed && !stalledUnderPendingMR && (in.State == StateDone || in.State == StateHandedOff) {
		d := WorkstateDisposition{
			Verdict:     WorkstateVerdictPendingMR,
			Reason:      "active-mr-open",
			ReuseStatus: ReuseStatusPROpen,
			Blockers:    []string{in.ActiveMRBlocker},
		}
		// The verdict is decided by the MR, but a hook or an assigned work bead
		// is still a fact about this polecat and the caller gathered it. Dropping
		// it left the reader with an MR pointer and no hint that work was still
		// attached — exactly the shape gastown/chrome was in, which is what made
		// "stalled" look plausible in the first place. The two surfaces carry it
		// in different fields (the list in ActiveWorkBlocker, check-recovery in
		// HookBead), so report both or they disagree again (gt-mkpm).
		if in.ActiveWorkBlocker != "" {
			d.Blockers = append(d.Blockers, in.ActiveWorkBlocker)
		}
		if in.HookBead != "" && !in.PartialSpawnWithoutDurableHook {
			d.Blockers = append(d.Blockers, "has work on hook ("+in.HookBead+")")
		}
		// Say out loud that the session is gone and why that is not being read as
		// a stall. Without this line the reader sees leave-alone over a polecat
		// with no session and no account of it — which is the reading gt-9f67 was
		// filed about, and it stays wrong even when the verdict is right.
		if in.SessionPresence == SessionAbsent && in.CompletionCoverage != "" {
			d.Blockers = append(d.Blockers,
				"session_presence=absent (tmux has-session found no session) — not a stall: "+in.CompletionCoverage)
		}
		return d
	}

	// StateDone (agent_state=done, seen before a polecat's own idle transition
	// lands) falls through to the real predicate checks below instead of
	// bailing out here — otherwise a merged/clean polecat gets NEEDS_RECOVERY
	// with no blockers, disagreeing with git-state for no reason (gt-check-recovery-bug).
	//
	// StateHandedOff falls through for the same reason and with a stronger claim
	// behind it: callers only assign it where an open MR for this polecat's
	// branch was positively found, so the session ending was completion, not
	// death. Bailing out here is what rendered a polecat whose work was in the
	// queue as "stalled" — the word for the failure case — for the whole
	// in-flight window (gt-mkpm).
	if in.State != StateIdle && in.State != StateDone && in.State != StateHandedOff {
		verdict := WorkstateVerdictNeedsRecovery
		needsRecovery := true
		if in.State == StateWorking {
			verdict = WorkstateVerdictWorking
			needsRecovery = false
		}
		d := WorkstateDisposition{
			Verdict:              verdict,
			Reason:               WorkstateReasonNotIdle,
			NeedsRecovery:        needsRecovery,
			CountsTowardCapacity: true,
		}
		if in.ActiveWorkBlocker != "" {
			d.Blockers = append(d.Blockers, in.ActiveWorkBlocker)
		} else {
			// The state IS the evidence here, so name it. With no blocker at all
			// this verdict reached `gt polecat check-recovery`'s empty-blockers
			// arm and printed "Cleanup refused by an unknown recovery predicate"
			// — a refusal that cannot say what it refused on is unactionable by
			// construction, and the predicate was never unknown to the code
			// (hq-qm7bt, gt-mkpm).
			d.Blockers = append(d.Blockers, "polecat_state="+string(in.State)+" (lifecycle state is not idle; no other blocker was recorded)")
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

	// FIRST, ahead of the hook it always accompanies, because block() takes the
	// reason from whoever calls it first and this is the one that names the
	// finding. "hook-still-set" is true of a healthy in-flight polecat too; the
	// dead session is what makes this one a stall, and it is the fact a reader
	// has to see to know a restart is not the remedy (gt-9f67).
	//
	// It also breaks the mrOnly test below by construction — two blockers, not
	// one — which is the second road to PENDING_MR and had to be closed with the
	// first, or the leave-alone verdict simply reappears a few lines further
	// down.
	if stalledUnderPendingMR {
		blocker := "session_presence=absent (tmux has-session found no session) while work is still attached"
		// Named here only when nothing else will name it. The hook and the
		// active-work blockers below both carry a bead ID; AssignedWorkBead does
		// not block on its own, so on the road where it is the ONLY evidence the
		// reader would otherwise get a verdict about attached work with nothing
		// saying WHICH work.
		if in.HookBead == "" && in.ActiveWorkBlocker == "" && in.AssignedWorkBead != "" {
			blocker += " (issue store holds " + in.AssignedWorkBead + " hooked to this polecat)"
		}
		block(WorkstateReasonStalledPendingMR, blocker, true)
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
			d.ReuseStatus = ReuseStatusPROpen
			return d
		}
		d.Verdict = WorkstateVerdictNeedsRecovery
		d.NeedsRecovery = true
		d.CountsTowardCapacity = capacityBlocked
		d.ReuseStatus = ReuseStatusRecoveryNeeded
		return d
	}

	if in.MQCheckRequired {
		if in.MQLookupFailed {
			d.Verdict = WorkstateVerdictNeedsRecovery
			d.Reason = "mq-lookup-failed"
			d.NeedsRecovery = true
			d.MQStatus = "unknown"
			d.CountsTowardCapacity = true
			d.ReuseStatus = ReuseStatusRecoveryNeeded
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
			d.ReuseStatus = ReuseStatusRecoveryNeeded
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
			d.NeedsRecovery = true
			d.NeedsMQSubmit = true
			d.CountsTowardCapacity = true
			// The two roads out of here are conservative in the same direction
			// and for opposite reasons, and until gt-mkpm they rendered as the
			// same string.
			//
			// checked: the queue WAS consulted and the branch still holds work
			// nothing has taken. That is a finding about the polecat.
			//
			// !checked: nobody consulted the queue. The refusal is a bead fact
			// that fires on surfaces too cheap to look (MQCheckRequired false),
			// which is deliberate — a surface that never looked does not get to
			// conclude the work is safe (gt-46rk) — but it is a statement about
			// the CALLER's gap, and printing "idle-recovery-needed" for it named
			// a remedy that on this road is not needed and cannot be performed.
			// Everything that decides stays identical; only the words change.
			//
			// The checked arm is currently unreachable: every road on which the
			// queue WAS consulted returns from the MQCheckRequired block above
			// (not_submitted, lookup_failed) or discharges the refusal via
			// `resolved`. It is written out anyway rather than collapsed into an
			// assumption, because the assumption is exactly what would go stale
			// if that ordering ever moved — and a stale assumption here renders
			// as a confident wrong string, which is the defect this splits.
			if checked {
				d.Reason = "mq-refused-closed-source"
				d.MQStatus = "refused_closed_source"
				d.ReuseStatus = ReuseStatusRecoveryNeeded
				d.Blockers = append(d.Blockers, "mq_status=refused_closed_source (gt done made no MR: source issue was closed)")
			} else {
				d.Reason = "mq-refused-unchecked"
				d.MQStatus = "refused_closed_source_unchecked"
				d.ReuseStatus = ReuseStatusMQUnchecked
				d.Blockers = append(d.Blockers, "mq_status=refused_closed_source_unchecked (gt done made no MR: source issue was closed; no merge-queue check was run here, so whether the branch still holds unsubmitted work is UNKNOWN)")
			}
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
		d.ReuseStatus = ReuseStatusStatePaused
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
		d.ReuseStatus = ReuseStatusUnverified
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
		d.ReuseStatus = ReuseStatusPreserved
	} else {
		d.ReuseStatus = ReuseStatusClean
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
