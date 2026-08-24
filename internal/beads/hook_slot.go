package beads

import "strings"

// Reconciling an agent bead's hook slot against the work bead it names.
//
// The slot is a copy. Nothing rewrites it when the work bead moves on, so the
// standard unstranding remedy — reopen the bead and clear its assignee so
// anyone can pick it up — leaves a dead agent's slot pointing at a bead the
// issue store says nobody holds. Three surfaces blocked cleanup on that slot
// alone (check-recovery, the nuke safety check, and the witness's slot-reuse
// decision), so a polecat could be refused cleanup forever over a bead that was
// simultaneously sitting in `bd ready`, and every unstranding manufactured
// another one (gt-dh3d).
//
// The work bead is authoritative about who holds it. The rule lives here, once,
// because a predicate fixed on two of three surfaces is not fixed.

// NormalizedAgentPathAddress collapses a nested agent path to the short form
// some writers use: "rig/crew/name" and "rig/polecats/name" both become
// "rig/name". Other addresses are returned unchanged.
//
// This preserves the input's case and trailing slash, so it is the right tool
// for producing an address to hand onward. To decide whether two addresses name
// the same agent, use SameAgentAddress instead — this function knows only one
// of the three dimensions gt disagrees on.
func NormalizedAgentPathAddress(addr string) string {
	parts := strings.Split(strings.TrimSpace(addr), "/")
	if len(parts) == 3 && (parts[1] == "crew" || parts[1] == "polecats") {
		return parts[0] + "/" + parts[2]
	}
	return addr
}

// HookSlotHeldBy reports whether a work bead's own assignee agrees that the
// named agent holds it.
//
// Every address form the agent's beads may carry counts as a match. A form this
// misses would drop a real blocker, which is the expensive direction to be
// wrong in — which is why the rule is SameAgentAddress and not a walk over
// AgentAddressForms. This predicate used to combine the two form helpers by
// hand, and it was the only surface in the tree that knew both dimensions.
func HookSlotHeldBy(issue *Issue, assignee string) bool {
	if issue == nil {
		return false
	}
	return SameAgentAddress(issue.Assignee, assignee)
}

// HookSlotReleased reports whether the issue store positively contradicts an
// agent bead's hook slot — that is, whether the work bead it names is held by
// somebody other than this agent, or by nobody at all.
//
// Release requires positive evidence: an assignee naming somebody else, or the
// exact shape `gt unsling` writes (status open, assignee cleared), which is
// also what reopening a bead to unstrand it produces. An empty assignee on a
// bead still marked hooked or in_progress is ambiguous — the status says
// somebody holds it while the assignee names nobody — and ambiguity keeps
// blocking.
//
// A terminal work bead is not "released" here; callers check IsTerminal
// separately, because a finished bead and a reassigned one mean different
// things to the rest of a cleanup decision.
func HookSlotReleased(issue *Issue, assignee string) bool {
	if issue == nil || HookSlotHeldBy(issue, assignee) {
		return false
	}
	if strings.TrimSpace(issue.Assignee) != "" {
		return true
	}
	return !IssueStatus(issue.Status).IsAssigned()
}
