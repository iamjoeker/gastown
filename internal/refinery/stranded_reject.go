package refinery

import (
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// The engineer's automatic rejection closes the MR bead and leaves the source
// issue exactly as it found it. When that issue is already CLOSED, what remains
// is a branch on origin, a closed bead and a rejected MR — a state every
// structured surface reads as finished, with nothing able to re-sling it. That
// is the composition gt-rbul produces: a polecat closes its own bead, submits,
// and recheckMRSourceStillMergeable rejects the MR *because* the source issue is
// terminal.
//
// `gt mq reject` closes the same hole by reopening the issue (gt-a46b), because
// an operator running that command is asserting "this work is not done" at the
// moment they run it. The engineer cannot make that assertion. A closed bead
// under a live branch has two readings and the engineer cannot tell them apart:
//
//	(a) the work really did land, via another MR — the bead is correctly closed
//	    and reopening it resurrects finished work, at refinery cadence, in bulk;
//	(b) the bead closed prematurely underneath a live branch — the work is
//	    stranded.
//
// So the engineer reports instead of correcting: it files a durable, deduplicated
// bead naming the MR, branch, commit, issue and rejection reason, and assigns it
// to the rig's witness. That turns a silent work loss into a queued human
// decision, which is the only response correct under both readings (gt-h1cw).
//
// Two things it deliberately does NOT do:
//
// It does not classify the rejection reason. The policy reasons that would be
// wrong to act on — no_merge, review_only, merge_strategy=local — cannot reach
// here with a terminal source issue at all, because recheckMRSourceStillMergeable
// tests IsTerminal first and short-circuits. A reason classifier would be dead
// code on the exact path it exists to guard.
//
// It does not file when git ancestry PROVES reading (a). If the MR's commit is
// already an ancestor of the target, the work is in the target, the closed bead
// is right, and there is nothing for anyone to decide. The proof runs one way
// only — a non-ancestor branch may still have landed by squash or cherry-pick,
// which is precisely the shape gt-aqk turned out to have — so anything short of
// a clean, error-free "yes, merged" still files.

// strandedRejectFiler is the slice of the beads client this path needs.
type strandedRejectFiler interface {
	Show(id string) (*beads.Issue, error)
	CreateIfNoDuplicate(opts beads.CreateOptions) (*beads.Issue, bool, error)
	Update(id string, opts beads.UpdateOptions) error
}

// branchMergedProbe reports whether commit is already contained in target.
//
// It must answer (false, err) rather than (false, nil) when it cannot tell:
// a definite "no" and "I don't know" both file, but only an error-free "yes"
// suppresses, so the distinction is kept honest at the boundary.
type branchMergedProbe func(commit, target string) (bool, error)

// strandedRejectRequest describes the rejection being reported.
type strandedRejectRequest struct {
	MRID        string
	Branch      string
	Target      string
	CommitSHA   string
	Worker      string
	SourceIssue string
	Reason      string
	// Rig binds the filed bead to the rig's database (gt-7y7) and names the
	// witness it is assigned to.
	Rig string
}

// strandedRejectResult records what the report path concluded.
type strandedRejectResult struct {
	// SourceIssueID is the resolved work bead, "" when the MR has none.
	SourceIssueID string
	// SourceIssueStatus is the status that was read, for the caller's log.
	SourceIssueStatus string
	// Stranded is true when the rejection left a closed bead under an
	// unmerged branch and a bead was filed (or found already filed).
	Stranded bool
	// AlreadyMerged is true when ancestry proved the work is in the target,
	// so the closed bead is correct and nothing was filed.
	AlreadyMerged bool
	// BeadID names the filed (or pre-existing) report bead.
	BeadID string
	// Created is false when an open report for this rejection already existed.
	Created bool
	// SkipReason names why a terminal source issue was left unreported
	// (tombstoned, or not a concrete work issue).
	SkipReason string
	// Err is non-fatal: the MR was still rejected, but the stranding could not
	// be assessed or reported.
	Err error
	// RigBindErr is set when the report could not be bound to the rig's
	// database and was filed in the default one instead. The report exists;
	// it is just somewhere the rig's witness may not look.
	RigBindErr error
}

// witnessAddress is where a stranded rejection is queued for a decision.
func witnessAddress(rigName string) string {
	rigName = strings.TrimSpace(rigName)
	if rigName == "" {
		return ""
	}
	return rigName + "/witness"
}

// strandedRejectTitle is deterministic in the MR and the source issue so that
// CreateIfNoDuplicate collapses a repeated rejection onto the open report
// rather than filing a second one. It is also the string a human greps for.
//
// The constructor lives in beads because the backward half — the branch sweep
// that finds strandings already made — recognises these reports by title in
// order not to re-report them. Two paths, one string.
func strandedRejectTitle(mrID, sourceIssue string) string {
	return beads.StrandedRejectTitle(mrID, sourceIssue)
}

// reportStrandedReject files a report when an automatic rejection has left a
// closed source issue under an unmerged branch. It never rewrites the issue.
func reportStrandedReject(work strandedRejectFiler, merged branchMergedProbe, req strandedRejectRequest) strandedRejectResult {
	sourceIssue := cleanWorkBeadID(req.SourceIssue)
	result := strandedRejectResult{SourceIssueID: sourceIssue}
	if sourceIssue == "" {
		// Nothing was submitted against a work bead, so nothing can be stranded.
		return result
	}
	if work == nil {
		result.Err = errors.New("no beads client available")
		return result
	}

	issue, err := work.Show(sourceIssue)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			// A missing source issue is its own rejection reason and its own
			// problem; there is no bead here whose state could mislead anyone.
			return result
		}
		result.Err = fmt.Errorf("reading source issue: %w", err)
		return result
	}
	if issue == nil {
		return result
	}

	result.SourceIssueStatus = strings.TrimSpace(issue.Status)
	if !beads.IssueStatus(result.SourceIssueStatus).IsTerminal() {
		// Open, hooked, in_progress, blocked, deferred — all still re-slingable.
		// The rejection stranded nothing.
		return result
	}
	if skip := beads.ConcreteWorkIssueRejectReason(issue); skip != "" && skip != beads.ProtectedLabelRejectReason(issue) {
		// Wisps, formulas and internal types are not work anyone re-slings, so
		// nothing about them is stranded. A PROTECTED bead is the exception: it
		// is ordinary work that merely must not be auto-closed, and reporting
		// writes nothing to it, so protection is no reason for silence (gt-zu5n).
		result.SkipReason = skip
		return result
	}

	if merged != nil && req.CommitSHA != "" && strings.TrimSpace(req.Target) != "" {
		contained, mergeErr := merged(req.CommitSHA, req.Target)
		if mergeErr == nil && contained {
			// Reading (a), proven. The bead is closed because the work landed.
			result.AlreadyMerged = true
			return result
		}
	}

	result.Stranded = true

	opts := beads.CreateOptions{
		Title:       strandedRejectTitle(req.MRID, sourceIssue),
		Labels:      []string{"gt:bug"},
		Priority:    1,
		Description: strandedRejectDescription(req, result.SourceIssueStatus),
		Actor:       strings.TrimSpace(req.Rig) + "/refinery",
		Rig:         strings.TrimSpace(req.Rig),
	}
	report, created, err := work.CreateIfNoDuplicate(opts)
	if err != nil && opts.Rig != "" {
		// Rig binding is how the report reaches the witness who reads that
		// database (gt-7y7), but it is a routing preference, not the point. An
		// unresolvable rig alias must not turn a work-loss report into a log
		// line: retry unbound, and say where it actually landed.
		unbound := opts
		unbound.Rig = ""
		if fallback, fallbackCreated, fallbackErr := work.CreateIfNoDuplicate(unbound); fallbackErr == nil && fallback != nil {
			result.RigBindErr = err
			report, created, err = fallback, fallbackCreated, nil
		}
	}
	if err != nil {
		result.Err = fmt.Errorf("filing stranded-rejection report: %w", err)
		return result
	}
	if report == nil {
		result.Err = errors.New("filing stranded-rejection report: no bead returned")
		return result
	}
	result.BeadID = report.ID
	result.Created = created

	// Only a fresh report needs routing; re-assigning an existing one would
	// stomp whoever picked it up.
	if created {
		if witness := witnessAddress(req.Rig); witness != "" {
			if err := work.Update(report.ID, beads.UpdateOptions{Assignee: &witness}); err != nil {
				result.Err = fmt.Errorf("assigning %s to %s: %w", report.ID, witness, err)
			}
		}
	}
	return result
}

// strandedRejectDescription names every fact a reader needs to decide between
// the two readings, plus the one command that separates them.
func strandedRejectDescription(req strandedRejectRequest, status string) string {
	target := strings.TrimSpace(req.Target)
	if target == "" {
		target = "main"
	}
	commit := strings.TrimSpace(req.CommitSHA)
	if commit == "" {
		commit = "(none recorded)"
	}
	worker := strings.TrimSpace(req.Worker)
	if worker == "" {
		worker = "(unknown)"
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = "(none recorded)"
	}

	return fmt.Sprintf(`The refinery rejected an MR whose source issue was already %s.

mr: %s
branch: %s
commit_sha: %s
target: %s
worker: %s
source_issue: %s
source_issue_status: %s
rejection_reason: %s

The rejection closed the MR bead and left the source issue closed, which is the
state that reads as finished on every surface while the branch sits unmerged on
origin. Nothing re-slings a closed bead, so this needs a decision the refinery
cannot make for itself (gt-h1cw).

TWO READINGS, and the command that separates them:

    git merge-base --is-ancestor %s origin/%s && echo MERGED || echo NOT-AN-ANCESTOR
    git cherry origin/%s %s        # '-' prefixed lines already landed by squash/cherry-pick

(a) The work landed by another route. The bead is correctly closed and the
    branch is redundant. Close this report; delete the branch if you like.
(b) The bead closed prematurely underneath live work. Reopen %s and re-sling it:

        bd update %s --status=open

The refinery did NOT reopen the issue itself: it cannot tell (a) from (b), and
reopening under (a) resurrects finished work in bulk. `+"`gt mq reject`"+` does reopen,
because an operator running it is asserting the work is not done (gt-a46b).`,
		status,
		req.MRID, branch, commit, target, worker, req.SourceIssue, status, req.Reason,
		commit, target,
		target, branch,
		req.SourceIssue,
		req.SourceIssue,
	)
}
