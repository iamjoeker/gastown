package beads

import (
	"fmt"
	"strconv"
)

// beadsRoleKey is the git-config key bd reads to decide whether a repo's beads
// belong to the project or to the caller's personal planning store.
const beadsRoleKey = "beads.role"

// beadsRoleMaintainer routes a repo's beads to the project database.
const beadsRoleMaintainer = "maintainer"

// withProjectBeadsRole pins beads.role=maintainer for one bd subprocess, so bd's
// role-based routing cannot divert a pinned command away from the project
// database.
//
// bd routes a repo's beads by role. With beads.role=contributor — set
// explicitly, or inferred by bd from the origin URL — create and list are
// silently redirected to the caller's contributor planning store instead of the
// project database. bd still exits 0 and still returns a real bead ID, so
// nothing looks wrong, but the bead never lands in the project's tables.
//
// For a rig that is fatal. A rig's beads ARE the project's beads: the MR beads
// `gt done` submits, and the wisps `gt mq list` and the refinery read, all live
// in the rig database. Under contributor routing every MR a polecat submitted
// was written to a personal store nobody else reads, so the merge queue looked
// empty while work piled up invisibly and was eventually lost (gt-2ta).
//
// This is applied only where gt has already pinned BEADS_DIR — which is gt
// asserting a specific project database — and so extends the guarantee
// BuildPinnedBDEnv already makes: the pinned .beads directory is the target, and
// inherited state must not redirect the command somewhere else.
//
// The override travels as GIT_CONFIG_* on the subprocess environment, which git
// applies as if it were config without reading or writing any config file. The
// repo's own beads.role is never inspected and never rewritten, so what a human
// or another tool sees in that clone is unchanged — only where bd files the
// beads for this one gt-issued command. Which store is authoritative for a
// developer's own `bd list` stays a repo-owner decision.
func withProjectBeadsRole(env []string) []string {
	// git reads overrides from GIT_CONFIG_KEY_<n>/VALUE_<n> for n in [0, COUNT).
	// Append after any the caller already set rather than clobbering them.
	n := 0
	if existing := envValue(env, "GIT_CONFIG_COUNT"); existing != "" {
		parsed, err := strconv.Atoi(existing)
		// A malformed count makes git reject every override, so treat it as
		// absent and write a well-formed one.
		if err == nil && parsed > 0 {
			n = parsed
		}
	}

	env = StripEnvKey(env, "GIT_CONFIG_COUNT")
	env = append(env,
		fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n, beadsRoleKey),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, beadsRoleMaintainer),
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", n+1),
	)
	return env
}
