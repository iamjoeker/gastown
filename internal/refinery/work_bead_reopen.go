package refinery

import (
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// workBeadReopener is the slice of the beads client the reject path needs.
type workBeadReopener interface {
	Show(id string) (*beads.Issue, error)
	Reopen(id, reason string) error
}

// rejectedWorkBeadResult records what happened to a rejected MR's source issue.
//
// Status is the issue's status AFTER the attempt, so callers can report the
// bead's real state instead of describing reject's own behaviour.
type rejectedWorkBeadResult struct {
	WorkBeadID string
	Status     string
	Reopened   bool
	// SkipReason is set when the bead was terminal but deliberately left alone
	// (tombstoned, or not a concrete work issue).
	SkipReason string
	// Err is non-fatal: the rejection still happened, but the source issue's
	// state could not be read or corrected and needs a human.
	Err error
}

// reopenRejectedWorkBead reopens the source issue of a rejected MR.
//
// A rejection means the work is not done, which is precisely the condition a
// closed bead denies: leaving it closed strands the branch on origin with
// nothing able to re-sling it. Anything but a plain closed bead is reported
// rather than rewritten.
func reopenRejectedWorkBead(work workBeadReopener, workBeadID, mrID, reason string) rejectedWorkBeadResult {
	workBeadID = cleanWorkBeadID(workBeadID)
	result := rejectedWorkBeadResult{WorkBeadID: workBeadID}
	if workBeadID == "" {
		return result
	}
	if work == nil {
		result.Err = errors.New("no beads client available")
		return result
	}

	issue, err := work.Show(workBeadID)
	if err != nil {
		result.Err = fmt.Errorf("reading issue: %w", err)
		return result
	}
	if issue == nil {
		result.Err = errors.New("issue not found")
		return result
	}

	result.Status = strings.TrimSpace(issue.Status)
	if beads.IssueStatus(result.Status) != beads.StatusClosed {
		// Open, hooked, in_progress, blocked, deferred — all re-slingable, and
		// tombstones are not ours to resurrect.
		if beads.IssueStatus(result.Status).IsTerminal() {
			result.SkipReason = "terminal:" + result.Status
		}
		return result
	}
	if skip := beads.ConcreteWorkIssueRejectReason(issue); skip != "" {
		result.SkipReason = skip
		return result
	}

	reopenReason := fmt.Sprintf("MR %s rejected: %s", mrID, reason)
	if err := work.Reopen(workBeadID, reopenReason); err != nil {
		result.Err = fmt.Errorf("reopening issue: %w", err)
		return result
	}

	result.Reopened = true
	result.Status = string(beads.StatusOpen)
	return result
}
