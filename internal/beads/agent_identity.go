package beads

import (
	"os"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// Canonical role-agent identity resolution.
//
// Two properties of a role agent are derived here, and only here:
//
//  1. Which beads database holds its beads. Town-level role directories
//     (<town>/deacon, <town>/mayor, <town>/daemon) have no .beads of their own;
//     their beads live in the town database. Deriving the database from the
//     agent's working directory — or from a sling target, which for a role
//     target IS that directory — yields a path that does not exist, and bd
//     fails with "no beads database found".
//
//  2. Which assignee address form its beads carry. Town-level singleton roles
//     are canonically addressed with a trailing slash ("deacon/"), matching
//     resolveSelfTarget and the sling target resolver. Writers have not all
//     converged on that form yet, so readers must match every historical form
//     or hooked work goes silently invisible and the agent idles.
//
// Both defects were previously handled by scattered ad-hoc fixups (three
// independent strings.TrimSuffix sites, one "only when empty" work-dir
// fallback), which is why some code paths agreed and others did not.

// townLevelSlashRoles are the town-level singleton role agents whose canonical
// assignee address carries a trailing slash. Named town-level agents
// ("deacon/boot", "deacon/dogs/<name>") are already path-form and are excluded.
var townLevelSlashRoles = []string{constants.RoleMayor, constants.RoleDeacon}

// BareAgentAddress returns an agent address with any trailing slash removed.
// This is the form used to build agent bead IDs and to parse an address into
// role components; it is NOT the form beads are assigned under.
func BareAgentAddress(addr string) string {
	return strings.TrimSuffix(strings.TrimSpace(addr), "/")
}

// IsTownLevelSlashRole reports whether addr names a town-level singleton role
// agent (mayor or deacon), in either address form.
func IsTownLevelSlashRole(addr string) bool {
	bare := BareAgentAddress(addr)
	for _, role := range townLevelSlashRoles {
		if bare == role {
			return true
		}
	}
	return false
}

// CanonicalAgentAddress returns the canonical assignee address for an agent.
// Town-level singleton roles get the trailing slash ("deacon" -> "deacon/");
// every other address is returned unchanged.
//
// Writers must use this so that readers converge on one form over time.
func CanonicalAgentAddress(addr string) string {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return ""
	}
	if IsTownLevelSlashRole(trimmed) {
		return BareAgentAddress(trimmed) + "/"
	}
	return trimmed
}

// AgentAddressForms returns every address form an agent's beads may carry,
// canonical form first. For town-level singleton roles that is both the
// slash form and the bare form; for everything else it is the single address.
//
// This is the QUERY inventory, and it is deliberately narrower than
// AgentAddressKey's comparison rule. A query costs a bd subprocess per form, so
// only the trailing-slash dimension is enumerated: that is the one dimension
// where both forms are demonstrably written into the same column for the same
// agent, still today (hq.wisps holds 339 rows assigned "deacon" and 125 assigned
// "deacon/", the newest of each less than half an hour apart). The nested-path
// dimension does not need enumerating, because no bead is ever written to a
// polecat in the collapsed form in a status any of these queries ask for.
//
// Readers must query all returned forms. Beads already written with a
// non-canonical form otherwise stay invisible forever — this town has at least
// one such bead in its history (gt patrol report wrote assignee "deacon" while
// gt hook queried "deacon/").
//
// Compare with SameAgentAddress, never by walking this list: a comparison is
// free, so it must accept every form, not just the ones worth a query.
func AgentAddressForms(addr string) []string {
	canonical := CanonicalAgentAddress(addr)
	if canonical == "" {
		return nil
	}
	bare := BareAgentAddress(canonical)
	if bare == canonical || bare == "" {
		return []string{canonical}
	}
	return []string{canonical, bare}
}

// AgentAddressKey reduces an agent address to the one string every convention
// in this system agrees on, so that two addresses naming the same agent compare
// equal no matter which writer produced them.
//
// It collapses all three ways gt disagrees with itself about an address:
//
//   - trailing slash — "deacon" vs "deacon/" (gt writes the slashed form, the
//     deacon's own patrol workaround rewrites it bare on every cycle)
//   - nested path — "gastown/polecats/toast" vs "gastown/toast" (sling keeps the
//     container segment, mail's AddressToIdentity strips it, and both forms are
//     live in the same store for the same agent)
//   - letter case — polecat names are written capitalized in some surfaces
//
// The key is for comparison only. Never store it, never query with it, and
// never show it to anyone: it is lossy on purpose, and a collapsed address
// cannot be expanded back without guessing between "crew" and "polecats".
func AgentAddressKey(addr string) string {
	bare := strings.ToLower(BareAgentAddress(addr))
	if bare == "" {
		return ""
	}
	parts := strings.Split(bare, "/")
	if len(parts) == 3 && isAgentPathContainer(parts[1]) && !isRigLevelRoleName(parts[2]) {
		return parts[0] + "/" + parts[2]
	}
	return bare
}

// isAgentPathContainer reports whether seg is a container segment that some
// writers include between a rig and an agent name and others omit.
//
// "dogs" is deliberately absent: dogs are only ever addressed
// "deacon/dogs/<name>", so collapsing that segment would make every dog compare
// equal to a hypothetical "deacon/<name>" agent instead of fixing anything.
func isAgentPathContainer(seg string) bool {
	switch seg {
	case "polecats", "polecat", constants.RoleCrew:
		return true
	}
	return false
}

// isRigLevelRoleName reports whether seg names a rig-level role rather than an
// individual agent.
//
// It blocks the one collapse that would be worse than the disagreement it
// fixes: "gastown/polecats/witness" is a polecat that happens to be named
// witness, and collapsing it to "gastown/witness" would hand it the rig
// witness's beads. Two agents comparing equal is a far more expensive mistake
// than one agent's two spellings comparing unequal, so the ambiguous case keeps
// its full path on both sides.
func isRigLevelRoleName(seg string) bool {
	switch seg {
	case constants.RoleWitness, constants.RoleRefinery, constants.RoleMayor, constants.RoleDeacon:
		return true
	}
	return false
}

// SameAgentAddress reports whether two addresses name the same agent.
//
// This is the single comparison boundary the address-form mess is resolved at.
// Every ownership, hook-slot and hook-verification decision must ask it rather
// than compare raw strings: the two sides of such a comparison routinely come
// from different writers, and a raw == is how "assignee is \"mayor/\", actor is
// \"mayor\"" became a refusal to close one's own mail.
//
// Empty is nobody, and nobody is not the same agent as anybody — including
// another nobody. Callers that mean "unassigned" must test for that explicitly.
func SameAgentAddress(a, b string) bool {
	keyA := AgentAddressKey(a)
	if keyA == "" {
		return false
	}
	return keyA == AgentAddressKey(b)
}

// ListAcrossAgentAddressForms runs List once per address form the assignee's
// beads may carry and merges the results, deduplicated by ID and preserving the
// order the forms were queried in.
//
// Any listing that filters on an agent's assignee must go through this rather
// than call List directly. A single-form listing is a silent false-empty in the
// one direction that costs the most: it reports "this agent holds nothing" for
// an agent that holds something, and the caller acts on the absence.
//
// With no assignee filter, or with an assignee that has only one form, this is
// exactly List and costs no extra query.
func (b *Beads) ListAcrossAgentAddressForms(opts ListOptions) ([]*Issue, error) {
	forms := AgentAddressForms(opts.Assignee)
	if len(forms) <= 1 {
		return b.List(opts)
	}

	limit := opts.Limit
	seen := make(map[string]bool)
	var merged []*Issue
	for _, form := range forms {
		formOpts := opts
		formOpts.Assignee = form
		formOpts.Limit = 0
		found, err := b.List(formOpts)
		if err != nil {
			return nil, err
		}
		for _, issue := range found {
			if issue == nil || seen[issue.ID] {
				continue
			}
			seen[issue.ID] = true
			merged = append(merged, issue)
		}
	}
	if limit > 0 && len(merged) > limit {
		return merged[:limit], nil
	}
	return merged, nil
}

// IsTownScopedAgent reports whether an agent's beads live in the town database
// rather than in a rig's. That is the town-level roles and everything nested
// under them: "mayor/", "deacon/", "deacon/boot", "deacon/dogs/<name>".
//
// This is the identity-side answer to "which database?", and it is the one to
// use whenever the agent is known. Directory probing (see
// ResolveBeadsDirWithTownFallback) answers the same question for commands that
// only have a cwd, but it cannot distinguish "this agent has no database" from
// "this agent's database has not been created yet" — a freshly spawned polecat
// clone is beads-less for a moment, and silently rerouting its work to the town
// database would put its wisp in the wrong place instead of failing loudly.
func IsTownScopedAgent(addr string) bool {
	first, _, _ := strings.Cut(BareAgentAddress(addr), "/")
	for _, role := range townLevelSlashRoles {
		if first == role {
			return true
		}
	}
	return false
}

// AgentBeadsWorkDir returns the directory a bd subprocess must run in to reach
// agentAddr's beads.
//
// Role agents (deacon, mayor, and everything under them) get the town root:
// their own directories hold no .beads, so deriving the database from the
// agent's working directory — or from a sling target, which for a role target
// IS that directory — yields a path that does not exist and bd dies with
// "no beads database found".
//
// Every other agent keeps its own working directory, including rig polecats,
// whose worktrees do have .beads and whose slings always worked.
func AgentBeadsWorkDir(agentAddr, workDir, townRoot string) string {
	if townRoot != "" && (workDir == "" || IsTownScopedAgent(agentAddr)) {
		return townRoot
	}
	return workDir
}

// ResolveBeadsDirWithTownFallback returns the beads directory reachable from
// workDir, falling back to the town database when workDir has none.
//
// It is for commands rooted at a cwd rather than at an agent: run from a role
// directory (<town>/deacon, <town>/mayor, <town>/daemon) they would otherwise
// resolve to a .beads that does not exist and fail, with cd'ing to the town
// root as the only workaround.
//
// townRoot may be empty, in which case it is located by walking up from
// workDir. When no usable directory can be found, the workDir-derived path is
// returned unchanged so callers keep reporting the diagnostic they always have.
func ResolveBeadsDirWithTownFallback(workDir, townRoot string) string {
	fallback := ""
	if workDir != "" {
		fallback = ResolveBeadsDir(workDir)
		if beadsDirExists(fallback) {
			return fallback
		}
	}

	if townRoot == "" && workDir != "" {
		townRoot = FindTownRoot(workDir)
	}
	if townRoot == "" {
		return fallback
	}

	townBeadsDir := ResolveBeadsDir(GetTownBeadsPath(townRoot))
	if beadsDirExists(townBeadsDir) {
		return townBeadsDir
	}
	if fallback == "" {
		return townBeadsDir
	}
	return fallback
}

// BeadsWorkDirWithTownFallback is the work-dir form of
// ResolveBeadsDirWithTownFallback, for callers that hand a work dir to
// beads.New rather than pinning BEADS_DIR directly.
//
// It returns workDir untouched whenever workDir already resolves to a real
// beads directory — including through a redirect, so rig worktrees keep their
// existing cwd and routing.
func BeadsWorkDirWithTownFallback(workDir, townRoot string) string {
	if workDir != "" && beadsDirExists(ResolveBeadsDir(workDir)) {
		return workDir
	}
	if townRoot == "" && workDir != "" {
		townRoot = FindTownRoot(workDir)
	}
	if townRoot == "" {
		return workDir
	}
	return townRoot
}

// beadsDirExists reports whether path is an existing directory.
func beadsDirExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
