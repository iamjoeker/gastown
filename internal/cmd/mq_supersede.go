package cmd

// Supersede-on-resubmission retires the record a new submission replaces. That
// is right when the SAME branch is submitted again: the old row names the same
// work at an older SHA, the refinery must not merge it, and closing it loses
// nothing.
//
// The queue was doing it for every other case too. FindOpenMRsForIssue keys on
// the SOURCE ISSUE alone, so two polecats holding one bead with complementary
// branches produced "superseded by X" on a branch that then LANDED — four times
// in rig beads, one of them the chain u2oy -> fdo4 -> gjyc -> gbj0 of which
// three branches merged to main and exactly one record says merged (gt-fe1e).
//
// The damage is to the record, not the code: every one of those branches is on
// main. It compounds because closeTerminalMR writes the merge outcome onto an
// OPEN MR only, so a record closed as superseded before its merge completes
// keeps "superseded" forever and the merge has no record behind it at all. The
// other half of the fix makes that path repair what it finds; this half stops
// manufacturing it.
//
// "Still mergeable" is deliberately NOT probed here. `gt mq submit` and
// `gt done` run in a polecat's shallow clone, whose remote-tracking refs cover
// its own branch and little else — asking "does this branch still carry work"
// there answers MISSING for live branches, and would supersede precisely the
// records this exists to protect. The refinery holds the full refs and already
// has an honest disposition for a stale MR it dequeues (verify, then reject,
// then a stranded-reject report to the witness), so leaving one open costs a
// queue cycle and yields a true record, while closing one wrongly yields a
// false record and no way back to the truth.

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// keptMR is an open MR a new submission left alone, and the branch that is the
// reason. Carried out of the decision so the caller can say what it did NOT do;
// a supersede loop that silently skips is indistinguishable from one that found
// nothing.
type keptMR struct {
	ID     string
	Branch string
}

// supersedePlan is the partition of the other open MRs on a source issue.
type supersedePlan struct {
	// Supersede holds the MR IDs this submission replaces.
	Supersede []string
	// Keep holds the MRs left open because they name a different branch.
	Keep []keptMR
}

// planSupersede decides which of an issue's other open MRs a new submission
// retires.
//
// newMRID is skipped (it is the row we just created). An MR is superseded when
// it records the same branch as the new submission, or records no branch at
// all: a row with no branch names no work that could land, so it can never be
// the record behind a merge, and leaving it open jams the queue forever.
//
// Everything else is kept. A different branch is a different piece of work that
// may still land, and the submit-time caller cannot prove otherwise.
func planSupersede(oldMRs []*beads.Issue, newMRID, newBranch string) supersedePlan {
	var plan supersedePlan
	newBranch = strings.TrimSpace(newBranch)

	for _, old := range oldMRs {
		if old == nil || old.ID == "" || old.ID == newMRID {
			continue
		}

		oldBranch := ""
		if fields := beads.ParseMRFields(old); fields != nil {
			oldBranch = strings.TrimSpace(fields.Branch)
		}

		if oldBranch == "" || (newBranch != "" && oldBranch == newBranch) {
			plan.Supersede = append(plan.Supersede, old.ID)
			continue
		}
		plan.Keep = append(plan.Keep, keptMR{ID: old.ID, Branch: oldBranch})
	}

	return plan
}

// supersedeKeptNotice renders what the submission left open and why. Returns ""
// when nothing was kept, so a caller can print it unconditionally.
func supersedeKeptNotice(plan supersedePlan, newBranch string) string {
	if len(plan.Keep) == 0 {
		return ""
	}

	var b strings.Builder
	for _, kept := range plan.Keep {
		fmt.Fprintf(&b, "  %s Left open: %s (%s)\n",
			style.Dim.Render("○"), kept.ID, kept.Branch)
	}
	fmt.Fprintf(&b, "  %s\n", style.Dim.Render(fmt.Sprintf(
		"%d open MR(s) on this issue name a branch other than %s. They are not "+
			"superseded: a different branch may still land, and closing its record "+
			"would leave that merge with no record behind it (gt-fe1e). The refinery "+
			"verifies and rejects the ones that are genuinely stale.",
		len(plan.Keep), newBranch)))
	return b.String()
}
