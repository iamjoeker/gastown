package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/deps"
)

// ClaudeOnboardingVersionCheck detects the "parked at first-run screen" hazard
// (gt-lbhl): onboarding state lives in ONE file shared by every agent on the
// machine (~/.claude.json). When Claude Code upgrades, that file's
// lastOnboardingVersion/lastReleaseNotesSeen go stale relative to the
// installed binary, and the next agent to spawn renders "Press Enter to
// continue" instead of running its start prompt — parked, session alive,
// process alive, doing nothing, until a human or agent dismisses the screen.
//
// This check is detection-only: it flags the divergence (the precondition for
// the hazard), it does not dismiss the screen or spawn a canary agent.
type ClaudeOnboardingVersionCheck struct {
	BaseCheck
}

// NewClaudeOnboardingVersionCheck creates a new onboarding-version-drift check.
func NewClaudeOnboardingVersionCheck() *ClaudeOnboardingVersionCheck {
	return &ClaudeOnboardingVersionCheck{
		BaseCheck: BaseCheck{
			CheckName:        "claude-onboarding-version",
			CheckDescription: "Detect Claude Code onboarding state drift that parks newly-spawned agents at the first-run screen",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// claudeOnboardingState mirrors the subset of ~/.claude.json this check reads.
type claudeOnboardingState struct {
	LastOnboardingVersion string `json:"lastOnboardingVersion"`
	LastReleaseNotesSeen  string `json:"lastReleaseNotesSeen"`
}

// Run compares the installed Claude Code version against the onboarding
// version fields recorded in ~/.claude.json.
func (c *ClaudeOnboardingVersionCheck) Run(ctx *CheckContext) *CheckResult {
	_, installedVersion := deps.CheckClaudeCode()
	if installedVersion == "" {
		// claude not found, exec failed, or version unparseable — the
		// claude-binary check already reports on that. Nothing to compare here.
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "claude version unavailable, skipping onboarding-state comparison",
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "could not resolve home directory, skipping onboarding-state comparison",
		}
	}

	claudeJSONPath := filepath.Join(home, ".claude.json")
	data, err := os.ReadFile(claudeJSONPath)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "no ~/.claude.json found, nothing to compare",
		}
	}

	var state claudeOnboardingState
	if err := json.Unmarshal(data, &state); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "~/.claude.json unparseable, skipping onboarding-state comparison",
		}
	}

	var stale []string
	if state.LastOnboardingVersion != "" && state.LastOnboardingVersion != installedVersion {
		stale = append(stale, fmt.Sprintf("lastOnboardingVersion=%s", state.LastOnboardingVersion))
	}
	if state.LastReleaseNotesSeen != "" && state.LastReleaseNotesSeen != installedVersion {
		stale = append(stale, fmt.Sprintf("lastReleaseNotesSeen=%s", state.LastReleaseNotesSeen))
	}

	if len(stale) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("claude %s matches onboarding state", installedVersion),
		}
	}

	return &CheckResult{
		Name:   c.Name(),
		Status: StatusWarning,
		Message: fmt.Sprintf("claude %s installed but %s in ~/.claude.json — the next spawned agent may park at the first-run screen",
			installedVersion, joinStale(stale)),
		Details: []string{
			"Onboarding state (~/.claude.json) is shared by every agent on this machine.",
			"Until dismissed, a newly-spawned agent renders 'Press Enter to continue' instead of running its start prompt: session alive, process alive, doing nothing.",
			"Every liveness signal (session status, heartbeat, a delivered nudge) reads healthy through this — only prime OUTPUT reveals it.",
		},
		FixHint: "Spawn one canary agent and confirm it reaches prime output before slinging more work — that single spawn dismisses the screen for everyone. If a pane is already parked, dismiss it manually.",
	}
}

func joinStale(stale []string) string {
	out := stale[0]
	for _, s := range stale[1:] {
		out += ", " + s
	}
	return out
}
