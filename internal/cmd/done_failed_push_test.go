package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// gt-3bzt: when the push command fails, push_failed must be decided by what the
// REMOTE holds, not by the exit status. These cases are the three answers the
// remote can give, and the first two are the ones that were being read as
// failures.

// classifyPushRepo builds a town-shaped layout — <townRoot>/<rig>/.repo.git as
// the origin, and a worktree on a polecat branch — and returns the pieces
// classifyFailedBranchPush takes.
func classifyPushRepo(t *testing.T) (g *git.Git, townRoot, rigName, branch, defaultBranch string) {
	t.Helper()
	townRoot = t.TempDir()
	rigName = "testrig"
	branch = "polecat/dust"
	defaultBranch = "main"

	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(rigPath, ".repo.git")
	repo := filepath.Join(rigPath, "work")
	runCmd(t, townRoot, "git", "init", "--bare", remote)
	runCmd(t, townRoot, "git", "init", repo)
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	writeRecoveryFile(t, filepath.Join(repo, "README.md"), "base")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "base")
	runGit(t, repo, "branch", "-M", defaultBranch)
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "-u", "origin", defaultBranch)
	runGit(t, repo, "switch", "-c", branch)

	return git.NewGit(repo), townRoot, rigName, branch, defaultBranch
}

func TestClassifyFailedBranchPush_BranchAlreadyHoldsCommit(t *testing.T) {
	g, townRoot, rigName, branch, defaultBranch := classifyPushRepo(t)
	repo := g.WorkDir()

	writeRecoveryFile(t, filepath.Join(repo, "work.txt"), "polecat work")
	runGit(t, repo, "add", "work.txt")
	runGit(t, repo, "commit", "-m", "polecat work")
	runGit(t, repo, "push", "origin", branch+":"+branch)

	head, err := g.Rev("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got := classifyFailedBranchPush(g, townRoot, rigName, branch, defaultBranch, head); got != pushContentOnBranch {
		t.Errorf("classifyFailedBranchPush = %v, want pushContentOnBranch", got)
	}
}

func TestClassifyFailedBranchPush_CommitAlreadyMerged(t *testing.T) {
	g, townRoot, rigName, branch, defaultBranch := classifyPushRepo(t)
	repo := g.WorkDir()

	writeRecoveryFile(t, filepath.Join(repo, "work.txt"), "polecat work")
	runGit(t, repo, "add", "work.txt")
	runGit(t, repo, "commit", "-m", "polecat work")
	// Landed on the target branch, and the branch ref never published — the shape
	// the reported polecat was in when it was called stranded: MR already merged,
	// commit an ancestor of origin/main, flag saying the push had failed.
	runGit(t, repo, "push", "origin", branch+":"+defaultBranch)

	head, err := g.Rev("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got := classifyFailedBranchPush(g, townRoot, rigName, branch, defaultBranch, head); got != pushContentMerged {
		t.Errorf("classifyFailedBranchPush = %v, want pushContentMerged", got)
	}
}

// The control. Work that really is only local must classify as missing, or the
// two answers above would be waving through every failed push there is.
func TestClassifyFailedBranchPush_CommitOnlyLocal(t *testing.T) {
	g, townRoot, rigName, branch, defaultBranch := classifyPushRepo(t)
	repo := g.WorkDir()

	writeRecoveryFile(t, filepath.Join(repo, "work.txt"), "never pushed")
	runGit(t, repo, "add", "work.txt")
	runGit(t, repo, "commit", "-m", "polecat work")

	head, err := g.Rev("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got := classifyFailedBranchPush(g, townRoot, rigName, branch, defaultBranch, head); got != pushContentMissing {
		t.Errorf("classifyFailedBranchPush = %v, want pushContentMissing", got)
	}
}

// An unanswerable question is not evidence of safety.
func TestClassifyFailedBranchPush_EmptyCommitIsMissing(t *testing.T) {
	g, townRoot, rigName, branch, defaultBranch := classifyPushRepo(t)
	if got := classifyFailedBranchPush(g, townRoot, rigName, branch, defaultBranch, "  "); got != pushContentMissing {
		t.Errorf("classifyFailedBranchPush(empty commit) = %v, want pushContentMissing", got)
	}
	head, err := g.Rev("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got := classifyFailedBranchPush(g, townRoot, rigName, branch, "", head); got != pushContentMissing {
		t.Errorf("classifyFailedBranchPush(no default branch) = %v, want pushContentMissing", got)
	}
}
