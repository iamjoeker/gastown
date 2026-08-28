package refinery

import (
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

type terminalMRCloseOptions struct {
	Reason        string
	MergeCommit   string
	AgentBeadHint string
	MissingOK     bool
	ExpectedMR    *MergeRequest
	// MergeProven says this caller has independently established, against git,
	// that the branch landed — not that it believes it did. Only a caller that
	// has run a post-merge proof may set it, and only it may rewrite an
	// already-closed record (see recordMergeOnTerminalMR).
	//
	// ExpectedMR is deliberately NOT that evidence. Manager.PostMerge fills it
	// from the very record it is about to check, so the comparison is a record
	// against itself and passes for a rejected MR as readily as a merged one.
	MergeProven bool
}

type terminalMRCloseResult struct {
	MRID                  string
	SourceIssue           string
	AgentBead             string
	Closed                bool
	AlreadyTerminal       bool
	AgentActiveMRCleared  bool
	AgentActiveMRClearErr error

	// RecordedCloseReason is the outcome the MR description already carried
	// when this close found it already terminal. Empty means the record named
	// no outcome at all, which is the shape a supersede leaves behind: the
	// supersede writes the bead's close_reason FIELD and never touches the
	// description, so the two carriers disagree and the description is silent.
	RecordedCloseReason string
	// OutcomeCorrected is true when an already-terminal record said something
	// other than merged and this call rewrote it to merged.
	OutcomeCorrected bool
	// OutcomeCorrectErr is non-fatal. The merge is a fact in git either way;
	// failing the close because its record could not be repaired would trade a
	// wrong record for a wrong outcome.
	OutcomeCorrectErr error
}

func closeTerminalMR(b *beads.Beads, mrID string, opts terminalMRCloseOptions) (*terminalMRCloseResult, error) {
	mrID = strings.TrimSpace(mrID)
	result := &terminalMRCloseResult{MRID: mrID}
	if b == nil || mrID == "" {
		return result, nil
	}

	issue, err := b.Show(mrID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) && opts.MissingOK {
			return result, nil
		}
		return result, fmt.Errorf("fetch MR for close: %w", err)
	}
	if issue == nil {
		return result, nil
	}

	fields := beads.ParseMRFields(issue)
	if fields == nil {
		fields = &beads.MRFields{}
	}
	result.SourceIssue = strings.TrimSpace(fields.SourceIssue)
	result.AgentBead = firstNonEmpty(opts.AgentBeadHint, fields.AgentBead)
	if err := validateTerminalMRCloseSnapshot(mrID, fields, opts.ExpectedMR); err != nil {
		return result, err
	}

	status := beads.IssueStatus(strings.TrimSpace(issue.Status))
	switch {
	case status == beads.StatusOpen:
		if opts.MergeCommit != "" {
			fields.MergeCommit = opts.MergeCommit
		}
		if closeReason := normalizedMRCloseReason(opts.Reason); closeReason != "" {
			fields.CloseReason = closeReason
		}
		if result.AgentBead != "" && strings.TrimSpace(fields.AgentBead) == "" {
			fields.AgentBead = result.AgentBead
		}

		newDesc := beads.SetMRFields(issue, fields)
		if err := b.Update(mrID, beads.UpdateOptions{Description: &newDesc}); err != nil {
			return result, fmt.Errorf("record MR close metadata: %w", err)
		}
		// Force-close: MR beads are pinned to protect the record from
		// `bd purge --force`, which skips pinned beads (gt-6dp). The pin is
		// meant to stop every OTHER writer, not the merge queue completing
		// its own lifecycle step, so the queue closes its own record with
		// --force rather than tripping over its own protection (gt-obth).
		if err := b.ForceCloseWithReason(opts.Reason, mrID); err != nil {
			return result, fmt.Errorf("close MR: %w", err)
		}
		result.Closed = true
	case status.IsTerminal():
		result.AlreadyTerminal = true
		result.RecordedCloseReason = strings.TrimSpace(fields.CloseReason)
		recordMergeOnTerminalMR(b, mrID, issue, fields, opts, result)
	default:
		return result, nil
	}

	if result.AgentBead != "" {
		cleared, clearErr := b.ForAgentBead().ClearAgentActiveMRIfMatches(result.AgentBead, mrID)
		result.AgentActiveMRCleared = cleared
		result.AgentActiveMRClearErr = clearErr
	}
	return result, nil
}

// recordMergeOnTerminalMR stamps the merge outcome onto an MR that was already
// closed by the time the merge completed.
//
// Without it, an MR closed as superseded mid-flight keeps "superseded" forever:
// the open-status branch above is the only writer of the description's
// close_reason, so a real merge on main ends up with no merged-record behind
// it. Four branches on beads/main are in exactly that state (gt-fe1e), and one
// of them carries no description close_reason at all — the record does not even
// say it was superseded, it says nothing.
//
// Three things bound the rewrite, because overwriting a settled record is not a
// small act:
//
//   - Only toward merged. A merge is a physical fact in git that this caller
//     has already proved. A rejection is a DECISION, and a later "rejected"
//     must never erase an earlier "merged" — that would invert the defect
//     rather than fix it.
//   - Only when the record disagrees. An already-merged record is left byte-
//     identical, so a post-merge retry is still idempotent.
//   - Only for a caller holding a post-merge PROOF. `gt mq post-merge <id>` is
//     run by hand and takes the operator's word that a merge happened; letting
//     it repair would let a typo stamp "merged" onto a rejected record, which
//     is the defect inverted rather than fixed. Manager.PostMerge therefore
//     keeps its refusal, and only the engineer's landing path — downstream of
//     verifyMRInfoPostMergeProof — sets MergeProven.
//
// validateTerminalMRCloseSnapshot has also already run by this point, binding
// branch, source_issue and commit_sha to the merge that was verified, so the
// rewrite lands on the record for the branch that actually merged and not on a
// neighbour.
//
// The bead's own close_reason FIELD still reads "superseded by X" afterwards.
// It cannot be amended from here, and it is the honest history of what the
// queue did; the description carrier is what states the OUTCOME, and an audit
// that reads both now sees a merge with a merged-record and the supersede that
// nearly hid it.
func recordMergeOnTerminalMR(
	b *beads.Beads,
	mrID string,
	issue *beads.Issue,
	fields *beads.MRFields,
	opts terminalMRCloseOptions,
	result *terminalMRCloseResult,
) {
	if !opts.MergeProven {
		return
	}
	if normalizedMRCloseReason(opts.Reason) != string(CloseReasonMerged) {
		return
	}
	if result.RecordedCloseReason == string(CloseReasonMerged) {
		return
	}

	fields.CloseReason = string(CloseReasonMerged)
	if opts.MergeCommit != "" {
		fields.MergeCommit = opts.MergeCommit
	}
	newDesc := beads.SetMRFields(issue, fields)
	if err := b.Update(mrID, beads.UpdateOptions{Description: &newDesc}); err != nil {
		result.OutcomeCorrectErr = fmt.Errorf("recording merge on already-closed MR %s: %w", mrID, err)
		return
	}
	result.OutcomeCorrected = true
}

func validateTerminalMRCloseSnapshot(mrID string, fields *beads.MRFields, expected *MergeRequest) error {
	if expected == nil || fields == nil {
		return nil
	}
	checks := []struct {
		name string
		got  string
		want string
	}{
		{name: "branch", got: fields.Branch, want: expected.Branch},
		{name: "source_issue", got: fields.SourceIssue, want: expected.IssueID},
		{name: "commit_sha", got: fields.CommitSHA, want: expected.CommitSHA},
	}
	if strings.TrimSpace(expected.TargetBranch) != "" {
		checks = append(checks, struct {
			name string
			got  string
			want string
		}{name: "target", got: fields.Target, want: expected.TargetBranch})
	}
	for _, check := range checks {
		got := strings.TrimSpace(check.got)
		want := strings.TrimSpace(check.want)
		if want != "" && got != want {
			return fmt.Errorf("MR %s changed after merge proof: %s=%q, verified %q", mrID, check.name, got, want)
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
