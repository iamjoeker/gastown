package doctor

import (
	"fmt"
	"os/exec"
	"strings"
)

// sshMaxAuthTriesDefault is OpenSSH's default MaxAuthTries. Every git fetch
// or push over ssh to github.com offers the agent's loaded keys in order
// until one is accepted or this many attempts are exhausted. See gt-0ku4 /
// hq-4fb1b: a town operator whose agent held 6 keys, with the working
// github key offered sixth, had zero margin — adding one more key broke
// every fetch and push town-wide.
const sshMaxAuthTriesDefault = 6

// SSHGitHubKeyOrderCheck warns when the operator's SSH agent holds enough
// keys that github.com authentication risks exceeding OpenSSH's default
// MaxAuthTries before the working key is offered.
//
// This is a host-level, per-operator condition, not a repo problem: the fix
// (pinning github.com to a specific IdentityFile) touches the operator's
// personal ~/.ssh/config, which affects all of their ssh use, not just Gas
// Town. This check only diagnoses and reports a FixHint — it never modifies
// ~/.ssh/config (see the "NOT APPLIED" decision in hq-4fb1b).
type SSHGitHubKeyOrderCheck struct {
	BaseCheck
}

// NewSSHGitHubKeyOrderCheck creates a new SSH agent key order check.
func NewSSHGitHubKeyOrderCheck() *SSHGitHubKeyOrderCheck {
	return &SSHGitHubKeyOrderCheck{
		BaseCheck: BaseCheck{
			CheckName:        "ssh-github-key-order",
			CheckDescription: "Detect SSH agent key counts that risk exceeding MaxAuthTries against github.com",
			CheckCategory:    CategoryCore,
		},
	}
}

// Run inspects the SSH agent's loaded identities and, if github.com isn't
// already pinned to a specific IdentityFile, warns when the count is at or
// beyond OpenSSH's default MaxAuthTries.
func (c *SSHGitHubKeyOrderCheck) Run(ctx *CheckContext) *CheckResult {
	out, err := exec.Command("ssh-add", "-l").Output()
	if err != nil {
		// No agent running, or the agent reports no identities (also a
		// non-zero exit). Either way there's no key-ordering risk to warn
		// about.
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No SSH agent identities loaded",
		}
	}

	count := countAgentIdentities(string(out))
	if count < sshMaxAuthTriesDefault {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("%d SSH agent identities loaded (margin under MaxAuthTries=%d)", count, sshMaxAuthTriesDefault),
		}
	}

	gOut, gErr := exec.Command("ssh", "-G", "github.com").Output()
	if gErr == nil && githubIdentityIsPinned(string(gOut)) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("%d SSH agent identities loaded, but github.com is pinned to a specific IdentityFile", count),
		}
	}

	return &CheckResult{
		Name:   c.Name(),
		Status: StatusWarning,
		Message: fmt.Sprintf("%d SSH agent identities loaded — at or beyond OpenSSH's default MaxAuthTries=%d for github.com",
			count, sshMaxAuthTriesDefault),
		Details: []string{
			"Every git-over-ssh operation to github.com offers agent keys in order",
			"until one is accepted. With this many keys loaded and no github.com-",
			"specific IdentityFile pin, the working key may be offered past",
			"MaxAuthTries and break every fetch/push town-wide (gt-0ku4, hq-4fb1b).",
			"This is a personal SSH agent condition, not a repo problem — gt doctor",
			"will not modify your ~/.ssh/config automatically.",
		},
		FixHint: "Export the agent's github public key and pin it: " +
			"`ssh-add -L | grep -F github > ~/.ssh/github_ed25519.pub`, then add " +
			"`Host github.com` / `IdentitiesOnly yes` / `IdentityFile ~/.ssh/github_ed25519.pub` " +
			"to ~/.ssh/config (see gt-0ku4)",
	}
}

// countAgentIdentities counts the non-blank lines in `ssh-add -l` output,
// each of which represents one loaded identity.
func countAgentIdentities(output string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// githubIdentityIsPinned reports whether `ssh -G github.com` output shows
// IdentitiesOnly enabled, indicating the operator has deliberately
// restricted which identity is offered (removing the ordering dependency
// this check warns about).
func githubIdentityIsPinned(sshConfigOutput string) bool {
	for _, line := range strings.Split(sshConfigOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "identitiesonly" {
			return fields[1] == "yes"
		}
	}
	return false
}
