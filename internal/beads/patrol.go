package beads

import "strings"

// Role-owned patrol steps: a deacon's or witness's own patrol-cycle content
// (e.g. "Refresh heartbeat", mail-patrol steps), materialized as bd issues by
// mol-deacon-patrol / mol-witness-patrol / mol-refinery-patrol /
// mol-pr-feedback-patrol and worked in-process by the owning role's own
// session (see startDeaconSession). They must never be slung to a rig's
// general polecat pool.
//
// gt-spf25/gt-feo92 added a guard for one dispatch path. gt-pjuow found at
// least 6 different call sites that could still sling a role-owned patrol
// bead into the polecat pool because each one carried its own copy-pasted
// check (or none at all). This file is the single shared guard: every
// dispatch surface must call RoleOwnedPatrolDispatchRefusal (or
// IsRoleOwnedPatrolFormula directly) instead of re-deriving the rule.
const roleOwnedPatrolFormulaSuffix = "-patrol"

// IsRoleOwnedPatrolFormula reports whether formula is a role-owned patrol
// molecule whose steps must stay reserved for that role's own session.
func IsRoleOwnedPatrolFormula(formula string) bool {
	formula = strings.ToLower(strings.TrimSpace(formula))
	if formula == "" {
		return false
	}
	return strings.HasSuffix(formula, roleOwnedPatrolFormulaSuffix)
}

// AttachedFormulaFromDescription extracts the attached_formula field from a
// bead's raw description text, or "" if absent. A thin wrapper around
// ParseAttachmentFields for callers that only have the description string
// (e.g. a locally-decoded JSON struct) rather than a full *Issue.
func AttachedFormulaFromDescription(description string) string {
	fields := ParseAttachmentFields(&Issue{Description: description})
	if fields == nil {
		return ""
	}
	return fields.AttachedFormula
}

// RoleOwnedPatrolDispatchRefusal returns the message explaining why a
// role-owned patrol step was not dispatched to the general polecat pool, or
// "" when description carries no role-owned patrol attached_formula. Shared
// by every dispatch surface so the guidance is identical wherever the
// refusal surfaces.
func RoleOwnedPatrolDispatchRefusal(beadID, description string) string {
	formula := AttachedFormulaFromDescription(description)
	if !IsRoleOwnedPatrolFormula(formula) {
		return ""
	}
	return "refusing to sling role-owned patrol step " + beadID + ": attached_formula " + formula +
		" (belongs to that role's own patrol session, not the general polecat pool)\n" +
		"  Patrol steps are worked in-process by the owning role's own session.\n" +
		"  Leaving the bead alone lets the owning role's own patrol loop pick it back up."
}
