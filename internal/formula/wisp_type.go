package formula

import "github.com/BurntSushi/toml"

// WispTypeVar is the formula variable that declares how the molecule wisp
// spawned from a formula should be classified for TTL-based compaction.
//
// The patrol formulas have declared it since they were written
// (`[vars.wisp_type] default = "patrol"`), but until gt-fqd5 nothing read it:
// the value was parsed into Formula.Vars and dropped, so every molecule wisp
// reached the wisps table with an empty wisp_type and gt compact's per-type TTL
// policy had nothing to key on.
const WispTypeVar = "wisp_type"

// DeclaredWispType returns the wisp_type a formula declares for the molecule
// wisps spawned from it, resolving the formula through the usual
// rig > town > embedded precedence. It returns "" when the formula declares no
// wisp_type, cannot be resolved, or cannot be parsed — an unclassified wisp is
// the status quo, so a lookup failure must degrade to it rather than guess a
// TTL bucket.
//
// Callers should treat the result as advisory and validate it against the bd
// vocabulary (beads.IsValidWispType) before writing: the value comes from a
// TOML file an operator may have edited in the rig or town tier.
func DeclaredWispType(name, townRoot, rigName string) string {
	content, err := ResolveFormulaContent(name, townRoot, rigName)
	if err != nil {
		return ""
	}

	// Decode only the vars table rather than going through Parse. Parse runs
	// full structural validation, so an unrelated defect elsewhere in the file
	// — a step missing a field, a bad aspect — would drop a perfectly good
	// wisp_type declaration on the floor. The classification should not depend
	// on the rest of the formula being well-formed.
	var declared struct {
		Vars map[string]Var `toml:"vars"`
	}
	if _, err := toml.Decode(string(content), &declared); err != nil {
		return ""
	}
	return declared.Vars[WispTypeVar].Default
}
