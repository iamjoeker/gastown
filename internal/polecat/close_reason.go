package polecat

import "strings"

// closeReasonTokenMaxLen bounds how far into a close reason the leading token is
// allowed to run. A reason is free text, and prose that happens to contain a
// colon ("I checked the queue: nothing there") must not be read as a declared
// close category.
const closeReasonTokenMaxLen = 24

// mergeQueueDischargingCloseReasons are the leading close-reason tokens that
// declare a bead produced NOTHING for the merge queue.
//
// "merged" is deliberately absent, and its absence is the point: a bead closed
// because its work landed says the opposite thing about the branch, and a
// branch whose patches are in the target already measures as having no
// submittable work without any help from this table.
var mergeQueueDischargingCloseReasons = map[string]bool{
	// The form the polecat protocol documents: a polecat whose bead has nothing
	// to implement runs `bd close <id> --reason="no-changes: <why>"`.
	"no-changes": true,
	"no-change":  true,
	"nochanges":  true,

	"duplicate":      true,
	"dupe":           true,
	"superseded":     true,
	"obsolete":       true,
	"wontfix":        true,
	"wont-fix":       true,
	"won't-fix":      true,
	"invalid":        true,
	"not-applicable": true,
	"n/a":            true,
}

// CloseReasonDischargesMergeQueue reports whether a bead's close reason is an
// explicit declaration that no merge request will ever be created for it.
//
// It exists because "the bead is closed" and "the bead is closed AS PRODUCING
// NOTHING" are different facts with opposite consequences for the slot holding
// its branch, and only the second one is safe to act on. A polecat whose source
// bead is merely closed may still owe the queue a submission. A polecat whose
// source bead is closed `duplicate` or `no-changes` never will: `gt done`
// refuses to open a merge request against a closed source issue (gt-7qm), so
// nothing the polecat can do clears the requirement, and its slot is refused
// for reuse on every dispatch for the life of the registry entry (gt-xm6w).
//
// It matches on the LEADING TOKEN only — the `<category>: <explanation>` shape
// the protocol writes — never on a substring. Text ABOUT a category satisfies a
// substring search FOR that category, and close reasons are exactly where that
// bites: "Fixed the duplicate-detection bug" is a fix that landed, and a bare
// `strings.Contains(reason, "duplicate")` would release a slot still holding
// it. Anchoring on structure is what makes the two distinguishable.
//
// Silence stays conservative in both directions. An empty reason, a bare
// "Closed", or any category not listed returns false and the caller keeps
// blocking exactly as it did before.
func CloseReasonDischargesMergeQueue(reason string) bool {
	token := closeReasonToken(reason)
	return token != "" && mergeQueueDischargingCloseReasons[token]
}

// mergeLandingClaimingCloseReasons are the leading close-reason tokens that
// assert a bead's fix is already on the target branch — either landed
// directly ("Fixed: ...") or via the merge queue ("Merged in <sha>").
//
// gt-20la: a polecat closed a bead with exactly this kind of reason while the
// fix commit still lived only on the polecat's own branch — never submitted
// to the merge queue, never reached main. A close reason making this claim is
// evidence a caller should verify against git before letting the close stand,
// which is what CloseReasonClaimsMergeLanding exists to flag.
var mergeLandingClaimingCloseReasons = map[string]bool{
	"fixed":  true,
	"fix":    true,
	"merged": true,
}

// CloseReasonClaimsMergeLanding reports whether a close reason's leading
// token asserts the bead's fix is already on the target branch.
//
// It matches on the LEADING TOKEN only, for the same reason
// CloseReasonDischargesMergeQueue does: "Fixed the merge conflict detector" is
// prose about a fix, not a claim that THIS bead's work has landed, and a bare
// substring match would flag it anyway.
func CloseReasonClaimsMergeLanding(reason string) bool {
	token := closeReasonToken(reason)
	return token != "" && mergeLandingClaimingCloseReasons[token]
}

// closeReasonToken extracts the declared category from a close reason: the text
// before the first colon when there is one close to the front, otherwise the
// first word. Spaces and underscores inside the token are normalised to hyphens
// so "no changes:", "no_changes:" and "no-changes:" are one category.
func closeReasonToken(reason string) string {
	head := strings.ToLower(strings.TrimSpace(reason))
	if head == "" {
		return ""
	}
	if i := strings.IndexByte(head, ':'); i >= 0 && i <= closeReasonTokenMaxLen {
		head = head[:i]
	} else if i := strings.IndexAny(head, " \t\n"); i >= 0 {
		head = head[:i]
	}
	head = strings.Trim(head, " \t.,;\"'()[]")
	head = strings.NewReplacer(" ", "-", "_", "-").Replace(head)
	if head == "" || len(head) > closeReasonTokenMaxLen {
		return ""
	}
	return head
}
