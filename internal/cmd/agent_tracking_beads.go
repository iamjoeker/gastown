package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// findCwdBeadsWorkDir finds the nearest .beads directory by walking up from CWD.
// It intentionally ignores BEADS_DIR for callers whose target is implied by
// the current rig worktree rather than inherited session environment.
func findCwdBeadsWorkDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	path := cwd
	for {
		if _, err := os.Stat(filepath.Join(path, ".beads")); err == nil {
			return path, nil
		}

		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}

	return "", fmt.Errorf("no .beads directory found")
}

// resolveAgentTrackingBeadsDir resolves the bead database used for agent state.
// Agent tracking follows the agent's current rig, so cwd-local redirects must
// win over an inherited town-level BEADS_DIR. The env-first resolver remains a
// fallback for contexts that do not have a cwd-local .beads directory.
func resolveAgentTrackingBeadsDir() (string, error) {
	workDir, err := findCwdBeadsWorkDir()
	if err != nil {
		workDir, err = findLocalBeadsDir()
	}
	if err != nil {
		return "", err
	}

	beadsDir := beads.ResolveBeadsDir(workDir)
	if beadsDir == "" {
		return "", fmt.Errorf("not in a beads workspace")
	}
	return beadsDir, nil
}

// resolveAgentStateBeadsDir resolves the bead database that actually holds
// agentBead, which is where its operational state (idle:N, backoff-until,
// last_activity) must be read and written.
//
// The rig-local database wins whenever it holds the bead, preserving per-rig
// agent tracking. When it does not, the town database is used: agent identity
// beads are town-scoped in every town provisioned so far, and pinning state ops
// to the rig database there silently drops every read and every write — idle
// backoff freezes at the base interval and heartbeats never advance, so a dead
// patrol agent looks identical to a healthy one.
//
// An empty agentBead means the caller has no bead to track, so the rig-local
// database is returned without probing.
func resolveAgentStateBeadsDir(agentBead string) (string, error) {
	rigBeadsDir, err := resolveAgentTrackingBeadsDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(agentBead) == "" || agentBeadExistsIn(agentBead, rigBeadsDir) {
		return rigBeadsDir, nil
	}

	townBeadsDir := townAgentBeadsDir()
	if townBeadsDir == "" || filepath.Clean(townBeadsDir) == filepath.Clean(rigBeadsDir) {
		return rigBeadsDir, nil
	}
	if agentBeadExistsIn(agentBead, townBeadsDir) {
		return townBeadsDir, nil
	}

	// The bead is in neither database. Return the rig directory so callers
	// surface the same "agent bead not found" diagnostic they always have.
	return rigBeadsDir, nil
}

// townAgentBeadsDir returns the town-level beads directory, or "" when the
// current working directory is not inside a town.
func townAgentBeadsDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	townRoot := beads.FindTownRoot(cwd)
	if townRoot == "" {
		return ""
	}
	return beads.ResolveBeadsDir(beads.GetTownBeadsPath(townRoot))
}

// agentBeadExistsIn reports whether agentBead is readable from beadsDir.
func agentBeadExistsIn(agentBead, beadsDir string) bool {
	_, err := getAllAgentLabels(agentBead, beadsDir)
	return err == nil
}
