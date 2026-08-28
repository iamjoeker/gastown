package cmd

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/style"
)

// raiseEscalationFn is the seam that lets the gt done escalation path be tested
// without a bd subprocess or a live town. Production always uses
// raiseEscalation.
var raiseEscalationFn = raiseEscalation

// doneRebaseEscalationFingerprint identifies repeat attempts at the same stuck
// rebase so a polecat that reruns gt done does not file a new escalation each
// time. The branch is the identity: a different branch is a different blockage.
func doneRebaseEscalationFingerprint(branch, base string) string {
	return escalationFingerprintLabel(fmt.Sprintf("gt-done-rebase:%s:%s", branch, base))
}

// doneRebaseEscalationDescription is the escalation title. It names gt done, the
// branch and the base, because the base being wrong is the failure this exists
// to surface (gt-lj2n).
func doneRebaseEscalationDescription(branch, base string) string {
	return fmt.Sprintf("gt done blocked: auto-rebase of %s onto %s failed", branch, base)
}

// escalateDoneRebaseFailure makes a failed auto-rebase LOUD. gt done aborts the
// rebase and leaves HEAD intact, so the branch looks healthy and the polecat
// looks merely idle: nothing was written to the merge queue and nothing reached
// anyone. That silence is what turned gt-lj2n into three burned polecat sessions
// instead of one, so the failure now raises a routed escalation and nudges the
// Witness before the error is returned.
//
// It never converts the failure into a success: the returned error always wraps
// rebaseErr, so gt done still exits non-zero whether or not the escalation
// could be filed. Escalation problems are reported, not substituted.
func escalateDoneRebaseFailure(townRoot, rigName, sender, issueID, branch, base string, behind int, rebaseErr error) error {
	// The Witness owns this polecat and is not on any escalation route, so it is
	// nudged directly — first, and unconditionally. It needs no town root and no
	// bd, so it is the one signal a Dolt outage cannot swallow. Nudges are
	// ephemeral by design; the escalation below is the durable half.
	nudgeWitness(rigName, strings.Join(strings.Fields(
		fmt.Sprintf("REBASE_BLOCKED %s onto %s failed - no MR created for %s", branch, base, issueID)), " "))

	if townRoot == "" {
		style.PrintWarning("could not escalate the rebase failure: no town root resolved")
		return rebaseErr
	}

	escalationConfig, cfgErr := config.LoadOrCreateEscalationConfig(config.EscalationConfigPath(townRoot))
	if cfgErr != nil {
		style.PrintWarning("could not escalate the rebase failure: loading escalation config: %v", cfgErr)
		return rebaseErr
	}

	agentID := sender
	if agentID == "" {
		agentID = "unknown"
	}
	description := doneRebaseEscalationDescription(branch, base)
	reason := fmt.Sprintf("gt done could not rebase %s onto %s (%d commits behind) and created no MR.\n"+
		"Issue: %s\nRebase error: %v\n"+
		"The rebase was aborted and HEAD is intact, so the branch looks healthy while the work is unsubmitted.\n"+
		"If %s is not this rig's merge target, the base itself is wrong — see gt-lj2n.",
		branch, base, behind, issueID, rebaseErr, base)

	outcome, escErr := raiseEscalationFn(escalationRequest{
		TownRoot:    townRoot,
		AgentID:     agentID,
		Description: description,
		Severity:    config.SeverityHigh,
		Reason:      reason,
		Source:      "gt done",
		RelatedBead: issueID,
		Fingerprint: doneRebaseEscalationFingerprint(branch, base),
		Config:      escalationConfig,
	})

	switch {
	case escErr != nil:
		style.PrintWarning("could not escalate the rebase failure: %v", escErr)
	case outcome.Duplicate:
		fmt.Printf("%s Rebase failure already escalated: %s\n", style.Bold.Render("⚠"), outcome.RecordID)
	case !outcome.Delivered:
		// A record nobody received is an ephemeral wisp that will be
		// garbage-collected unread, so it must not be reported as escalated.
		style.PrintWarning("%v", undeliveredEscalationError(townRoot, outcome.RecordID, config.SeverityHigh, outcome.Actions))
	default:
		fmt.Printf("%s Escalated: %s\n", style.Bold.Render("⚠"), doneRebaseEscalationRecordLine(outcome))
	}

	return rebaseErr
}

// doneRebaseEscalationRecordLine names the bead that outlives the ephemeral
// record, because that is the one `gt escalate list` renders — reporting only
// the record id points at something that will be garbage-collected.
func doneRebaseEscalationRecordLine(outcome *escalationOutcome) string {
	if outcome.DurableBeadID != "" && outcome.DurableBeadID != outcome.RecordID {
		return fmt.Sprintf("%s (recorded as %s)", outcome.RecordID, outcome.DurableBeadID)
	}
	return outcome.RecordID
}
