package refinery

// Priority band bounds. Beads priorities run P0 (most urgent) through P4
// (backlog); score.go clamps anything outside that range when scoring.
const (
	// HighestPriority is the most urgent band. Nothing outranks it.
	HighestPriority = 0
	// LowestPriority is the least urgent band.
	LowestPriority = 4
)

// BlockerPriority returns the priority a task must carry when it BLOCKS work
// that sits at blockedPriority.
//
// The rule is one band more urgent than what is blocked, clamped at P0.
//
// Why not a fixed priority (gt-ofb0): the refinery used to file
// conflict-resolution and resubmit tasks at a hardcoded P1 while the MRs they
// unblock were P0. A P1 blocker queues behind every P0 in the rig — including
// the very P0 it is holding up — so it is never scheduled and the MR can never
// merge. That deadlocks by construction, not by accident: measured 3 of 3 open
// MRs on the gastown rig on 2026-08-22.
//
// Why not plain inheritance (equal priority): the ready queue's default sort
// policy is hybrid, whose tie-break inside a priority band is created_at ASC —
// oldest first. A blocker is created strictly AFTER the work it blocks and
// after everything already queued in that band, so at equal priority it lands
// at the BACK of the band, behind the whole backlog it was meant to jump. One
// band better is the smallest derivation that puts it in front of that backlog
// regardless of tie-break.
//
// The rule is deliberately RELATIVE, not absolute: a P3 MR's blocker gets P2,
// not P0. Inflating every refinery-generated task to P0 would destroy the
// priority signal the queue runs on.
//
// A blocker that gates several items must be derived from the most urgent of
// them — see MostUrgentPriority.
func BlockerPriority(blockedPriority int) int {
	if blockedPriority <= HighestPriority {
		// P0 is the ceiling: "one better than P0" is still P0. This is the one
		// case where a blocker merely ties with what it blocks, and it is safe
		// because a blocked item is excluded from the ready queue entirely
		// (is_blocked = 1), so the tie is never actually contested.
		return HighestPriority
	}
	if blockedPriority > LowestPriority {
		blockedPriority = LowestPriority
	}
	return blockedPriority - 1
}

// MostUrgentPriority returns the most urgent (numerically lowest) priority
// among the given ones, which is the input BlockerPriority needs when one task
// blocks more than one item. Returns LowestPriority when given nothing, so a
// caller that lost its inputs files at the back of the queue rather than
// silently claiming P0.
func MostUrgentPriority(priorities ...int) int {
	if len(priorities) == 0 {
		return LowestPriority
	}
	most := priorities[0]
	for _, p := range priorities[1:] {
		if p < most {
			most = p
		}
	}
	return most
}
