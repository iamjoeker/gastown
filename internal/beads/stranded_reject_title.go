package beads

import (
	"fmt"
	"strings"
)

// A stranded rejection is reported from two directions and the two must agree
// on one string.
//
// Forward (internal/refinery/stranded_reject.go): when the engineer rejects an
// MR whose source issue is already closed, it files a report bead titled by
// StrandedRejectTitle. That catches strandings at the moment they are created.
//
// Backward (the branch sweep in internal/witness): it walks the polecat
// branches on origin that are not contained in the target and asks which ones
// need a human decision. A branch the forward path already reported is not a
// new finding, so the sweep recognises those reports by their title and says so
// instead of presenting them as fresh.
//
// The dedup is by title because that is the only key the two paths share: the
// sweep starts from a branch, the report from an MR, and no field links them
// except the source issue named in the title. Keeping the constructor here
// means the two cannot drift out of agreement without the compiler noticing.

// StrandedRejectTitlePrefix opens every stranded-rejection report title. It is
// also the string a human greps for.
const StrandedRejectTitlePrefix = "Stranded by rejection: "

// StrandedRejectTitle is deterministic in the MR and the source issue so that
// duplicate detection collapses a repeated rejection onto the open report
// rather than filing a second one.
func StrandedRejectTitle(mrID, sourceIssue string) string {
	return fmt.Sprintf("%sMR %s rejected, source issue %s left closed", StrandedRejectTitlePrefix, mrID, sourceIssue)
}

// IsStrandedRejectTitleFor reports whether title is a stranded-rejection report
// naming sourceIssue.
//
// It matches on the prefix and the issue ID rather than reconstructing the full
// title, because the sweep knows the issue but not which MR was rejected — and
// an issue can be rejected under more than one MR. The issue ID is bounded by
// spaces on both sides so gt-abc does not match a report about gt-abcdef.
func IsStrandedRejectTitleFor(title, sourceIssue string) bool {
	title = strings.TrimSpace(title)
	sourceIssue = strings.TrimSpace(sourceIssue)
	if title == "" || sourceIssue == "" {
		return false
	}
	if !strings.HasPrefix(title, StrandedRejectTitlePrefix) {
		return false
	}
	return strings.Contains(title, " "+sourceIssue+" ")
}
