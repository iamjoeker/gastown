package cmd

import (
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// isMergeRequestReadyForSelection reports whether an MR bead may be surfaced as
// ready to merge.
//
// Beyond status and blockers, the MR must be *actionable*: it needs parseable
// merge-request fields with both a source branch and a target branch. The
// refinery rejects an MR that is missing either one (see engineer.go —
// "MR has missing merge-request fields" / "MR has missing target"), so a row
// without them can never merge. Reporting it as ready is worse than useless:
// gt-2ta had a branchless, targetless wisp ranking ready at the top of the
// queue, which made `gt mq list` look healthy while the real MRs were missing.
func isMergeRequestReadyForSelection(issue *beads.Issue) bool {
	if issue == nil || issue.Status != "open" || beads.HasUnresolvedBlockers(issue) {
		return false
	}
	fields := beads.ParseMRFields(issue)
	if fields == nil {
		return false
	}
	return strings.TrimSpace(fields.Branch) != "" && strings.TrimSpace(fields.Target) != ""
}
