package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/util"
)

// WIPCommitPrefix is the commit message prefix used by checkpoint_dog auto-commits.
const WIPCommitPrefix = "WIP: checkpoint (auto)"

// ErrOnlyWIPCommits reports that the range consists entirely of checkpoint
// auto-commits, so there is no authored commit to fold them into.
// SquashWIPCommits leaves history untouched in that case; the caller decides
// what to do, since only the caller knows the issue this work belongs to
// (see SquashAll).
var ErrOnlyWIPCommits = errors.New("branch carries only checkpoint auto-commits")

// rangeCommit is one commit in mergeBase..HEAD, oldest first.
type rangeCommit struct {
	sha     string
	subject string
}

func (c rangeCommit) isWIP() bool { return strings.HasPrefix(c.subject, WIPCommitPrefix) }

// CountWIPCommits returns the number of WIP checkpoint commits between
// the merge-base of baseRef and HEAD.
func CountWIPCommits(workDir, baseRef string) (int, error) {
	_, commits, err := rangeSinceMergeBase(workDir, baseRef)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, c := range commits {
		if c.isWIP() {
			count++
		}
	}
	return count, nil
}

// SquashWIPCommits folds checkpoint auto-commits into the authored commits of
// mergeBase..HEAD and returns the number folded away.
//
// Each WIP commit is absorbed by the next authored commit; WIP commits after
// the last authored commit are absorbed by it. Authored commits keep their own
// message, author and order, so `git log` and `git blame` still land on a
// commit that names its issue. The tree at HEAD is unchanged, and nothing at or
// below the merge-base is rewritten.
//
// If the range holds WIP commits but no authored commit, history is left alone
// and ErrOnlyWIPCommits is returned alongside the WIP count.
func SquashWIPCommits(workDir, baseRef string) (int, error) {
	mergeBase, commits, err := rangeSinceMergeBase(workDir, baseRef)
	if err != nil {
		return 0, err
	}

	var wipCount int
	var authored []rangeCommit
	for _, c := range commits {
		if c.isWIP() {
			wipCount++
		} else {
			authored = append(authored, c)
		}
	}

	if wipCount == 0 {
		return 0, nil
	}
	if len(authored) == 0 {
		return wipCount, ErrOnlyWIPCommits
	}

	headTree, err := treeOf(workDir, "HEAD")
	if err != nil {
		return 0, err
	}

	// Replay the authored commits onto the merge-base. Every commit keeps its
	// own tree except the last, which takes HEAD's tree so the trailing
	// checkpoints' content survives. That makes each authored commit absorb the
	// checkpoints that preceded it.
	parent := mergeBase
	for i, c := range authored {
		tree, err := treeOf(workDir, c.sha)
		if err != nil {
			return 0, err
		}
		if i == len(authored)-1 {
			tree = headTree
		}
		parent, err = recommit(workDir, tree, parent, c.sha)
		if err != nil {
			return 0, err
		}
	}

	// --soft leaves the index and working tree alone. The rewritten tip carries
	// HEAD's tree, so nothing in the working tree moves.
	if _, err := gitOutput(workDir, "reset", "--soft", parent); err != nil {
		return 0, fmt.Errorf("moving branch to rewritten history: %w", err)
	}

	return wipCount, nil
}

// SquashAll collapses every commit in mergeBase..HEAD into a single commit
// carrying the given message, and returns the number of commits collapsed. A
// range of one commit is reworded rather than left alone — a lone
// "WIP: checkpoint (auto)" is the very thing the caller is trying to keep out
// of the target.
//
// This throws away commit messages, so it is only for ranges that have none
// worth keeping — an all-checkpoint branch. Prefer SquashWIPCommits.
func SquashAll(workDir, baseRef, message string) (int, error) {
	if strings.TrimSpace(message) == "" {
		return 0, errors.New("squash message is empty")
	}

	mergeBase, commits, err := rangeSinceMergeBase(workDir, baseRef)
	if err != nil {
		return 0, err
	}
	if len(commits) == 0 {
		return 0, nil
	}

	headTree, err := treeOf(workDir, "HEAD")
	if err != nil {
		return 0, err
	}

	newHead, err := gitRun(workDir, nil, message+"\n", "commit-tree", headTree, "-p", mergeBase)
	if err != nil {
		return 0, fmt.Errorf("writing squash commit: %w", err)
	}
	if _, err := gitOutput(workDir, "reset", "--soft", newHead); err != nil {
		return 0, fmt.Errorf("moving branch to squash commit: %w", err)
	}

	return len(commits), nil
}

// rangeSinceMergeBase returns the merge-base of baseRef and HEAD along with the
// commits reachable from HEAD but not from it, oldest first.
func rangeSinceMergeBase(workDir, baseRef string) (string, []rangeCommit, error) {
	mergeBase, err := gitOutput(workDir, "merge-base", baseRef, "HEAD")
	if err != nil {
		return "", nil, fmt.Errorf("finding merge-base: %w", err)
	}

	logOut, err := gitOutput(workDir, "log", "--reverse", "--format=%H %s", mergeBase+"..HEAD")
	if err != nil {
		return "", nil, fmt.Errorf("listing commits: %w", err)
	}
	if logOut == "" {
		return mergeBase, nil, nil
	}

	var commits []rangeCommit
	for _, line := range strings.Split(logOut, "\n") {
		sha, subject, _ := strings.Cut(line, " ")
		if sha == "" {
			continue
		}
		commits = append(commits, rangeCommit{sha: sha, subject: subject})
	}
	return mergeBase, commits, nil
}

func treeOf(workDir, rev string) (string, error) {
	tree, err := gitOutput(workDir, "rev-parse", rev+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("reading tree of %s: %w", rev, err)
	}
	return tree, nil
}

// recommit writes a new commit with the given tree and parent, carrying the
// message, author and committer identity of src.
func recommit(workDir, tree, parent, src string) (string, error) {
	// %x00 makes git emit the NUL; it cannot be passed literally in argv.
	idents, err := gitOutput(workDir, "log", "-1", "--format=%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI", src)
	if err != nil {
		return "", fmt.Errorf("reading identity of %s: %w", src, err)
	}
	fields := strings.Split(idents, "\x00")
	if len(fields) != 6 {
		return "", fmt.Errorf("reading identity of %s: got %d fields, want 6", src, len(fields))
	}

	msg, err := gitOutput(workDir, "log", "-1", "--format=%B", src)
	if err != nil {
		return "", fmt.Errorf("reading message of %s: %w", src, err)
	}

	env := append(os.Environ(),
		"GIT_AUTHOR_NAME="+fields[0],
		"GIT_AUTHOR_EMAIL="+fields[1],
		"GIT_AUTHOR_DATE="+fields[2],
		"GIT_COMMITTER_NAME="+fields[3],
		"GIT_COMMITTER_EMAIL="+fields[4],
		"GIT_COMMITTER_DATE="+fields[5],
	)

	sha, err := gitRun(workDir, env, msg+"\n", "commit-tree", tree, "-p", parent)
	if err != nil {
		return "", fmt.Errorf("rewriting %s: %w", src, err)
	}
	return sha, nil
}

// gitOutput runs a git command and returns trimmed stdout.
func gitOutput(workDir string, args ...string) (string, error) {
	return gitRun(workDir, nil, "", args...)
}

// gitRun runs a git command with an optional environment and stdin, and
// returns trimmed stdout.
func gitRun(workDir string, env []string, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	cmd.Env = env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	util.SetDetachedProcessGroup(cmd)

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if stderr != "" {
				return "", fmt.Errorf("%s: %s", err, stderr)
			}
		}
		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}
