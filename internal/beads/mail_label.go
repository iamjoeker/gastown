package beads

import "strings"

// Mail beads: durable messages, not filed work.
//
// Mail is stored as an ordinary bead on purpose. A message has to survive the
// recipient's session death and wisps do not, so `gt mail send` writes into the
// same issues table that the work queue reads. The side effect is that unread
// mail is indistinguishable from filed work to every query that does not know
// to look at the label, and the ready query is one of those: measured on the hq
// store 2026-08-25, `bd ready -n 0` returned 605 rows and 358 of them (59%)
// carried gt:message. All eleven that reached `gt ready`'s town section were
// P0, so mail held eleven of the seventeen slots at the top of the queue.
//
// Excluding mail from the work queue is a display change and nothing else.
// No bead is closed, relabelled, moved or made less durable; `gt mail` reads
// exactly the same beads through exactly the same label.
const (
	// MessageLabel is the canonical marker `gt mail` puts on a message bead.
	MessageLabel = "gt:message"

	// MessageIssueType is the legacy issue type for a message. Beads' own
	// infra-type filter keys on this rather than the label, which is why that
	// filter does not hide Gas Town mail: gt writes messages as type=task.
	MessageIssueType = "message"
)

// IsMailBead reports whether an issue is a message rather than filed work.
//
// Both the label and the legacy type are checked, matching IsAgentBead.
//
// Callers must not rely on this alone to keep mail out of a listing built from
// `bd ready --json` or `bd list --skip-labels`: those surfaces omit the labels
// field entirely, so every row arrives label-free and this returns false for
// mail. It is a second line of defence behind a query-level exclusion, not a
// substitute for one.
func IsMailBead(issue *Issue) bool {
	if issue == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(issue.Type), MessageIssueType) {
		return true
	}
	for _, label := range issue.Labels {
		if strings.EqualFold(strings.TrimSpace(label), MessageLabel) {
			return true
		}
	}
	return false
}
