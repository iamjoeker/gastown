package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// gt-3bzt: when the push command fails, push_failed must be decided by what the
// REMOTE holds, not by the exit status. These cases are the three answers the
// remote can give, and the first two are the ones that were being read as
// failures.

// classifyPushRepo builds a town-shaped layout and returns the pieces
// classifyFailedBranchPush takes.
//
// The layout is the production one, and getting it right is the whole point of
// this fixture (gt-mqmh). A town has THREE repositories, not two:
//
//	<townRoot>/origin.git          the remote — GitHub in a real town
//	<townRoot>/<rig>/.repo.git     the shared bare repo that HOSTS worktrees,
//	                               with origin.git as its own origin
//	<townRoot>/<rig>/polecats/...  the polecat worktree, added from .repo.git
//
// An earlier version of this fixture made .repo.git the origin. That collapses
// the two into one, and it silently disarms the control below: with .repo.git
// as the remote, the bare repo's refs/heads/<branch> genuinely does describe
// what the remote holds. In a real town it describes only what the polecat
// committed — `git worktree add` from a bare repo creates the branch inside it,
// and the remote's refs live under refs/remotes/origin/*. The check that read
// refs/heads as proof of a push was wrong in production and correct in the
// fixture, so three tests passed over the defect for three days.
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

	// The remote.
	origin := filepath.Join(townRoot, "origin.git")
	runCmd(t, townRoot, "git", "init", "--bare", "--initial-branch="+defaultBranch, origin)

	// Seed it with a base commit on the default branch.
	seed := filepath.Join(townRoot, "seed")
	runCmd(t, townRoot, "git", "init", "--initial-branch="+defaultBranch, seed)
	runGit(t, seed, "config", "user.email", "test@example.com")
	runGit(t, seed, "config", "user.name", "Test User")
	writeRecoveryFile(t, filepath.Join(seed, "README.md"), "base")
	runGit(t, seed, "add", "README.md")
	runGit(t, seed, "commit", "-m", "base")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "-u", "origin", defaultBranch)

	// The rig's shared bare repo: a clone of origin, NOT origin itself. Its
	// remote refs land under refs/remotes/origin/*, exactly as in a real town.
	bare := filepath.Join(rigPath, ".repo.git")
	runCmd(t, townRoot, "git", "init", "--bare", "--initial-branch="+defaultBranch, bare)
	runGit(t, bare, "remote", "add", "origin", origin)
	runGit(t, bare, "config", "user.email", "test@example.com")
	runGit(t, bare, "config", "user.name", "Test User")
	runGit(t, bare, "fetch", "origin")

	// The polecat worktree, hosted by the bare repo. Its commits update
	// <bare>/refs/heads/<branch> and reach the remote only when pushed.
	repo := filepath.Join(rigPath, "polecats", "dust")
	runGit(t, bare, "worktree", "add", "-b", branch, repo, "origin/"+defaultBranch)

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
//
// gt-mqmh: this is also the regression. The commit here is in the bare repo's
// refs/heads/<branch> — that is where a worktree commit goes — and the bare
// fallback used to read exactly that ref and call it a published push. This
// case answered pushContentOnBranch, push_failed was cleared, and gt done went
// on to create a merge request over a commit no remote had. The assertion below
// is unchanged from gt-3bzt; only the fixture's geometry changed, and that
// alone is what lets it fail.
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

	// Pin the trap this test exists to catch: the bare repo really does hold
	// this commit at refs/heads/<branch>. Without this the case could pass for
	// the wrong reason — a fixture where the ref simply is not there proves
	// nothing about a check that reads it.
	bare := git.NewGitWithDir(filepath.Join(townRoot, rigName, ".repo.git"), "")
	bareTip, bareErr := bare.Rev("refs/heads/" + branch)
	if bareErr != nil || strings.TrimSpace(bareTip) != head {
		t.Fatalf("fixture does not reproduce the trap: bare refs/heads/%s = %q (err %v), want HEAD %s",
			branch, bareTip, bareErr, head)
	}

	if got := classifyFailedBranchPush(g, townRoot, rigName, branch, defaultBranch, head); got != pushContentMissing {
		t.Errorf("classifyFailedBranchPush = %v, want pushContentMissing", got)
	}
	if err := verifyPushedCommitWithBareFallback(g, townRoot, rigName, branch, head); err == nil {
		t.Error("verifyPushedCommitWithBareFallback verified a commit that was never pushed")
	}
}

// The bare fallback still has to do its job, or the fix above would have traded
// one wrong answer for another. Here the WORKTREE cannot reach the remote —
// its origin URL is broken, which is the stale-gitdir shape the fallback was
// added for (GH #1348) — while the branch genuinely is published. The bare repo
// shares the same origin and answers correctly.
func TestVerifyPushedCommitWithBareFallback_RecoversFromBrokenWorktreeRemote(t *testing.T) {
	g, townRoot, rigName, branch, _ := classifyPushRepo(t)
	repo := g.WorkDir()

	writeRecoveryFile(t, filepath.Join(repo, "work.txt"), "polecat work")
	runGit(t, repo, "add", "work.txt")
	runGit(t, repo, "commit", "-m", "polecat work")
	runGit(t, repo, "push", "origin", branch+":"+branch)

	head, err := g.Rev("HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// Break the worktree's git context, leaving the bare repo's intact: a
	// worktree's .git is a pointer file, and a stale one is what GH #1348 was.
	// Every git invocation from the worktree now fails outright.
	writeRecoveryFile(t, filepath.Join(repo, ".git"),
		"gitdir: "+filepath.Join(townRoot, "no-such-gitdir")+"\n")
	if directErr := g.VerifyPushedCommit("origin", branch, head); directErr == nil {
		t.Fatal("fixture did not break the worktree's git context — the fallback would not be exercised")
	}

	if err := verifyPushedCommitWithBareFallback(g, townRoot, rigName, branch, head); err != nil {
		t.Errorf("verifyPushedCommitWithBareFallback = %v, want nil for a branch that is on the remote", err)
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
