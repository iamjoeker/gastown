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
// Readers must query all returned forms. Beads already written with a
// non-canonical form otherwise stay invisible forever — this town has at least
// one such bead in its history (gt patrol report wrote assignee "deacon" while
// gt hook queried "deacon/").
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
