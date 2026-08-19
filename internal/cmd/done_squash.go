package cmd

import (
	"errors"
	"fmt"

	"github.com/steveyegge/gastown/internal/checkpoint"
	"github.com/steveyegge/gastown/internal/style"
)

// aheadCounter is the subset of *git.Git that squashWIPCheckpoints needs, so
// the recount after a history rewrite can be observed in tests.
type aheadCounter interface {
	CommitsAhead(base, ref string) (int, error)
}

// wipSquashMessage is the subject given to a branch that carries nothing but
// checkpoint auto-commits. It names the issue, which is exactly what a
// "WIP: checkpoint (auto)" subject fails to do.
func wipSquashMessage(issueID string) string {
	if issueID == "" {
		return "chore: squash checkpoint auto-commits"
	}
	return fmt.Sprintf("chore: squash checkpoint auto-commits (%s)", issueID)
}

// squashWIPCheckpoints removes checkpoint_dog's "WIP: checkpoint (auto)" commits
// from the branch before it is pushed, and updates aheadCount if history moved.
//
// Normally each checkpoint is folded into the authored commit that follows it,
// leaving the agent's own commits and messages intact. A branch with no authored
// commit at all is collapsed under a subject naming the issue: the content is
// real work, so refusing to submit it would strand it, but it must not reach the
// target as an unattributable "WIP: checkpoint (auto)".
//
// alreadyPushed suppresses the rewrite: gt done skips the push on that path, so
// the rewrite would either be silently dropped or demand a force-push. The
// checkpoints are reported instead of removed.
//
// Failures here are reported and not fatal — an unsquashed branch is untidy,
// but blocking gt done over it would strand the work and zombie the polecat.
//
// gt-eob.
func squashWIPCheckpoints(workDir, baseRef, issueID string, alreadyPushed bool, aheadCount *int, g aheadCounter) {
	if alreadyPushed {
		if n, err := checkpoint.CountWIPCommits(workDir, baseRef); err == nil && n > 0 {
			style.PrintWarning("branch carries %d checkpoint auto-commit(s) but is already pushed; leaving history as-is", n)
		}
		return
	}

	squashed, err := checkpoint.SquashWIPCommits(workDir, baseRef)

	switch {
	case errors.Is(err, checkpoint.ErrOnlyWIPCommits):
		msg := wipSquashMessage(issueID)
		collapsed, allErr := checkpoint.SquashAll(workDir, baseRef, msg)
		if allErr != nil {
			style.PrintWarning("could not squash %d checkpoint auto-commit(s): %v (submitting history as-is)", squashed, allErr)
			return
		}
		style.PrintWarning("branch carried no authored commit; squashed %d checkpoint auto-commit(s) into %q", collapsed, msg)

	case err != nil:
		style.PrintWarning("could not squash checkpoint auto-commits: %v (submitting history as-is)", err)
		return

	case squashed == 0:
		return

	default:
		fmt.Printf("%s Squashed %d checkpoint auto-commit(s) into the authored history\n",
			style.Bold.Render("✓"), squashed)
	}

	// History was rewritten, so the earlier count is stale.
	if n, countErr := g.CommitsAhead(baseRef, "HEAD"); countErr == nil {
		*aheadCount = n
	}
}
