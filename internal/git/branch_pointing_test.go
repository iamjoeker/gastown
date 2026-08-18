package git

import (
	"os/exec"
	"testing"
)

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A detached worktree is what makes gt-e45 happen at all: CurrentBranch reports
// the literal "HEAD", which is not a branch anyone can push.
func TestCurrentBranchReportsHEADWhenDetached(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	gitCmd(t, dir, "checkout", "--detach", "HEAD")

	branch, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != DetachedHeadName {
		t.Fatalf("CurrentBranch = %q, want %q — the premise of the branch resolver", branch, DetachedHeadName)
	}
}

func TestBranchesPointingAt(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	gitCmd(t, dir, "branch", "polecat/furiosa/bd-791+abc")
	gitCmd(t, dir, "checkout", "--detach", "HEAD")

	branches, err := g.BranchesPointingAt("HEAD")
	if err != nil {
		t.Fatalf("BranchesPointingAt: %v", err)
	}

	found := false
	for _, b := range branches {
		if b == "polecat/furiosa/bd-791+abc" {
			found = true
		}
		if b == DetachedHeadName {
			t.Errorf("BranchesPointingAt returned %q — refs/heads/HEAD is not a branch", b)
		}
	}
	if !found {
		t.Errorf("BranchesPointingAt = %v, want the branch at the detached commit", branches)
	}
}

func TestBranchesPointingAtSkipsBranchesElsewhere(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	gitCmd(t, dir, "checkout", "-b", "polecat/furiosa/bd-791+abc")
	gitCmd(t, dir, "commit", "--allow-empty", "-m", "work")

	branches, err := g.BranchesPointingAt("HEAD~1")
	if err != nil {
		t.Fatalf("BranchesPointingAt: %v", err)
	}
	for _, b := range branches {
		if b == "polecat/furiosa/bd-791+abc" {
			t.Errorf("BranchesPointingAt(HEAD~1) returned %q, which points at HEAD", b)
		}
	}
}
