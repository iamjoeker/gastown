package witness

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
)

// PushFailureAlarm is the operator-facing rendering of a polecat that reported
// its work never reached origin.
type PushFailureAlarm struct {
	MayorMessage    string // Nudged to the Mayor session
	Action          string // HandlerResult.Action
	DiscoveryAction string // CompletionDiscovery.Action (no polecat name — the record carries it)
	Unnamed         bool   // The polecat could not name its own branch
}

// pushFailureAlarm renders the alarm for a polecat whose push did not reach
// origin, keeping two conditions apart that used to share one message (gt-e45).
//
// A branch of "HEAD" is what `git rev-parse --abbrev-ref HEAD` prints for a
// detached worktree, and polecat worktrees are routinely detached. It is not a
// branch: no remote carries refs/heads/HEAD, so a push of that name cannot
// succeed and a remote check of that name cannot pass. Reporting it as
// PUSH_FAILED tells an operator to push a branch that cannot exist, and makes a
// polecat that merely could not name its branch indistinguishable from one whose
// push genuinely failed. Either way the signal is destroyed, so the unnamed case
// gets its own alarm and says plainly that no push result was measured.
func pushFailureAlarm(polecatName, branch, issueID string) PushFailureAlarm {
	if isUnresolvableBranchName(branch) {
		return PushFailureAlarm{
			Unnamed: true,
			MayorMessage: fmt.Sprintf("POLECAT_DETACHED: polecat=%s branch=unresolvable issue=%s — polecat could not name its branch (detached HEAD); no push result was measured, work may be local only",
				polecatName, issueID),
			Action: fmt.Sprintf("branch-unresolvable for %s (issue=%s) — detached HEAD, no push result measured; work may be local only",
				polecatName, issueID),
			DiscoveryAction: fmt.Sprintf("branch-unresolvable (issue=%s) — detached HEAD, no push result measured; work may be local only",
				issueID),
		}
	}

	return PushFailureAlarm{
		MayorMessage: fmt.Sprintf("PUSH_FAILED: polecat=%s branch=%s issue=%s — branch not on origin, possible work loss",
			polecatName, branch, issueID),
		Action: fmt.Sprintf("push-failed-recovery-needed for %s (branch=%s issue=%s) — branch not on origin, worktree may be at risk",
			polecatName, branch, issueID),
		DiscoveryAction: fmt.Sprintf("push-failed-recovery-needed (branch=%s issue=%s) — branch not on origin, worktree may be at risk",
			branch, issueID),
	}
}

// isUnresolvableBranchName reports whether a recorded branch name can never
// identify a pushable branch — either the polecat recorded nothing, or it
// recorded the detached-HEAD placeholder.
func isUnresolvableBranchName(branch string) bool {
	branch = strings.TrimSpace(branch)
	return branch == "" || branch == git.DetachedHeadName
}
