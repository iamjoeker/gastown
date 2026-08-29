package cmd

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

type submitSourceIssue struct {
	ID              string
	Issue           *beads.Issue
	BD              *beads.Beads
	CurrentBeadsDir string
	RoutedBeadsDir  string
}

func routedIssueBeads(cwd, issueID string) (*beads.Beads, string, string) {
	currentBeadsDir := beads.ResolveBeadsDir(cwd)
	routedBeadsDir := beads.ResolveBeadsDirForID(currentBeadsDir, issueID)
	return beads.NewWithBeadsDir(cwd, routedBeadsDir), currentBeadsDir, routedBeadsDir
}

func sourceRouteContext(currentBeadsDir, routedBeadsDir string) string {
	return fmt.Sprintf("current_db=%s routed_db=%s", currentBeadsDir, routedBeadsDir)
}

// lookupSubmitSourceIssue resolves issueID to its routed bead without judging
// whether that bead is a usable merge-request source.
//
// gt-zu5n: concreteness is a precondition of *creating an MR*, not of reading
// the source issue. Validating it at resolution time made every completion path
// pay the check, including the ones that submit nothing — so hardening a bead
// with gt:keep (the sanctioned reaper exemption) turned the polecat working it
// into a zombie with no exit. Callers that will submit must follow this with
// validateConcreteSourceIssue once they know an MR is needed.
func lookupSubmitSourceIssue(cwd, issueID string) (*submitSourceIssue, error) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, fmt.Errorf("source_issue is required")
	}

	sourceBD, currentBeadsDir, routedBeadsDir := routedIssueBeads(cwd, issueID)
	issue, err := sourceBD.Show(issueID)
	if err != nil {
		return nil, fmt.Errorf("source_issue %s could not be resolved (%s): %w", issueID, sourceRouteContext(currentBeadsDir, routedBeadsDir), err)
	}
	return &submitSourceIssue{
		ID:              issueID,
		Issue:           issue,
		BD:              sourceBD,
		CurrentBeadsDir: currentBeadsDir,
		RoutedBeadsDir:  routedBeadsDir,
	}, nil
}

// resolveSubmitSourceIssue is lookupSubmitSourceIssue for callers that are
// definitely creating a merge request, and so must reject a non-concrete source
// up front.
func resolveSubmitSourceIssue(cwd, issueID string) (*submitSourceIssue, error) {
	source, err := lookupSubmitSourceIssue(cwd, issueID)
	if err != nil {
		return nil, err
	}
	if err := validateConcreteSourceIssue(source.ID, source.Issue); err != nil {
		return nil, err
	}
	return source, nil
}

func validateConcreteSourceIssue(issueID string, issue *beads.Issue) error {
	if reason := beads.ConcreteWorkIssueRejectReason(issue); reason != "" {
		return fmt.Errorf("source_issue %s is not concrete (%s)", issueID, reason)
	}
	return nil
}

// closedSourceIssueRefusal explains why no merge request may be created against
// issueID. Empty means the source issue still holds live work.
//
// gt-7qm: MR creation had no open-source-issue precondition. A polecat
// respawned onto an already-closed bead, correctly found nothing to do in the
// repo, submitted anyway, the refinery rejected the empty MR, and the cycle
// repeated across polecat deaths. Rejecting downstream answers the symptom and
// the answer regenerates the question — the precondition belongs at the
// producer, where it terminates the loop.
func closedSourceIssueRefusal(issueID string, issue *beads.Issue) string {
	if issue == nil {
		return "" // missing sources are validateConcreteSourceIssue's business
	}
	status := beads.IssueStatus(strings.ToLower(strings.TrimSpace(issue.Status)))
	if !status.IsTerminal() {
		return ""
	}
	return fmt.Sprintf("source issue %s is %s — a merge request needs open work to carry", issueID, status)
}

// validateOpenSourceIssueForMR is the erroring form of closedSourceIssueRefusal,
// for callers that abort rather than complete without an MR.
func validateOpenSourceIssueForMR(issueID string, issue *beads.Issue) error {
	refusal := closedSourceIssueRefusal(issueID, issue)
	if refusal == "" {
		return nil
	}
	return fmt.Errorf("%s.\n"+
		"A closed issue means the work is done or abandoned; either way there is nothing to merge.\n"+
		"If work genuinely remains: bd update %s --status=open, then resubmit.\n"+
		"Operator override (use only when the close was itself the mistake): --allow-closed-issue",
		refusal, issueID)
}

// noOpMRRefusal explains why no merge request may be created for commitSHA
// against target, given the outcome of asking whether that content already
// reached target (git.Git.VerifyCommitLandedOnPushTarget).
//
// gt-2fgq: a polecat respawned onto a branch whose commit content had already
// landed on target — the fix shipped elsewhere, or the branch was rebased
// away from real work — and submitted anyway. The refinery merges a no-op MR
// silently, so like gt-7qm's closed-issue gate, the precondition belongs at
// the producer.
//
// landedErr is nil when the landing check found proof the content is already
// on target: that is the failure case here. A non-nil landedErr means either
// real new work was found (git.ErrCommitNotLanded) or the question could not
// be answered (git.ErrMergeProofUnprovable, offline remote, etc.) — both fail
// open, mirroring close.go's verifyMergeLandingClaim: absence of proof is not
// proof of absence, and refusing here would block submits that have nothing
// to do with the defect being guarded against.
func noOpMRRefusal(target, commitSHA string, landedErr error) string {
	if landedErr != nil {
		return ""
	}
	return fmt.Sprintf("commit %s is already on %s (by sha or by content) — there is no new work to merge",
		shortSHA(commitSHA), target)
}

// validateNonEmptyMRSource is the erroring form of noOpMRRefusal, for callers
// that abort rather than create an MR that carries no work.
func validateNonEmptyMRSource(target, commitSHA string, landedErr error) error {
	refusal := noOpMRRefusal(target, commitSHA, landedErr)
	if refusal == "" {
		return nil
	}
	return fmt.Errorf("%s.\n"+
		"Merging this branch would be a no-op: %s already carries everything it has.\n"+
		"If real work remains, rebase onto the latest %s and verify your changes are present.\n"+
		"Operator override (use only when this check is wrong): --allow-noop",
		refusal, target, target)
}

func validateMergeRequestSource(mr *beads.Issue, expectedIssueID string, expectedIssue *beads.Issue) error {
	if mr == nil {
		return fmt.Errorf("merge request is missing")
	}
	fields := beads.ParseMRFields(mr)
	if fields == nil || strings.TrimSpace(fields.SourceIssue) == "" {
		return fmt.Errorf("merge request %s has missing source_issue", mr.ID)
	}
	sourceIssueID := strings.TrimSpace(fields.SourceIssue)
	if sourceIssueID != strings.TrimSpace(expectedIssueID) {
		return fmt.Errorf("merge request %s source_issue %s does not match expected %s", mr.ID, sourceIssueID, expectedIssueID)
	}
	if expectedIssue == nil {
		return fmt.Errorf("source_issue %s was not pre-resolved for merge request validation", sourceIssueID)
	}
	if resolvedID := strings.TrimSpace(expectedIssue.ID); resolvedID != "" && resolvedID != sourceIssueID {
		return fmt.Errorf("pre-resolved source_issue %s does not match merge request source_issue %s", resolvedID, sourceIssueID)
	}
	return validateConcreteSourceIssue(sourceIssueID, expectedIssue)
}
