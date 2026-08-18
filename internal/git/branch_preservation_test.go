package git

import (
	"os"
	"path/filepath"
	"testing"
)

// forkLayoutRepo builds the clone layout every gastown polecat has: origin is the
// fork it pushes to, upstream is the canonical repo, and upstream's tracking refs
// are behind. Returns the worktree path and the main branch name.
func forkLayoutRepo(t *testing.T) (string, string) {
	t.Helper()
	localDir, _, mainBranch := initTestRepoWithRemote(t)

	upstreamDir := filepath.Join(t.TempDir(), "upstream.git")
	runGit(t, localDir, "init", "--bare", upstreamDir)
	runGit(t, localDir, "remote", "add", "upstream", upstreamDir)
	runGit(t, localDir, "push", "upstream", mainBranch)

	// Work lands on origin/main; upstream/main stays where it was.
	if err := os.WriteFile(filepath.Join(localDir, "merged.go"), []byte("package merged\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "landed on origin main")
	runGit(t, localDir, "push", "origin", mainBranch)
	runGit(t, localDir, "fetch", "upstream")

	return localDir, mainBranch
}

// gt-ykp: a polecat branch with no upstream tracking ref, sitting on a commit
// already merged into origin/main, was reported as carrying unpushed work — but
// only by the callers that pass target refs. `main` resolves to a stale
// upstream/main in a fork clone, and naming a target suppressed the origin/main
// fallback, so the stale ref became the only comparison base. That verdict is
// NEEDS_RECOVERY/ESCALATE, the loudest one there is, on a clean sandbox.
func TestBranchPreservationStatusIgnoresStaleUpstreamWhenWorkIsOnOrigin(t *testing.T) {
	localDir, mainBranch := forkLayoutRepo(t)
	g := NewGit(localDir)

	branch := "polecat/merged-work"
	runGit(t, localDir, "checkout", "-b", branch)
	if _, err := g.run("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil {
		t.Fatal("fixture branch must have no upstream tracking ref")
	}

	withoutTargets, err := g.BranchPreservationStatus(branch, "origin", nil)
	if err != nil {
		t.Fatalf("BranchPreservationStatus (no targets): %v", err)
	}
	if withoutTargets.UnpreservedPatchCount != 0 {
		t.Fatalf("no-target unpreserved = %d, want 0", withoutTargets.UnpreservedPatchCount)
	}

	withTargets, err := g.BranchPreservationStatus(branch, "origin", []string{mainBranch})
	if err != nil {
		t.Fatalf("BranchPreservationStatus (target %s): %v", mainBranch, err)
	}
	if !withTargets.Preserved || withTargets.UnpreservedPatchCount != 0 {
		t.Fatalf("HEAD merged into origin/%s reported as unpreserved: %+v", mainBranch, withTargets)
	}
	if withTargets.ComparisonBase != "origin/"+mainBranch {
		t.Fatalf("ComparisonBase = %q, want origin/%s", withTargets.ComparisonBase, mainBranch)
	}
}

// The witness refs prove durability; they must not mask work that is genuinely
// at risk, and they must not soften the target question. Pushed-but-unmerged
// work still needs its merge request.
func TestBranchPreservationStatusStillReportsWorkAtRisk(t *testing.T) {
	localDir, mainBranch := forkLayoutRepo(t)
	g := NewGit(localDir)

	branch := "polecat/local-work"
	runGit(t, localDir, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(localDir, "wip.go"), []byte("package wip\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "unpushed work")

	status, err := g.BranchPreservationStatus(branch, "origin", []string{mainBranch})
	if err != nil {
		t.Fatalf("BranchPreservationStatus: %v", err)
	}
	if status.Preserved || status.UnpreservedPatchCount == 0 {
		t.Fatalf("unpushed commit reported as preserved: %+v", status)
	}

	target, err := g.BranchTargetStatus(branch, "origin", []string{mainBranch})
	if err != nil {
		t.Fatalf("BranchTargetStatus: %v", err)
	}
	if target.Preserved || target.UnpreservedPatchCount == 0 {
		t.Fatalf("unsubmitted work reported as on target: %+v", target)
	}
}

// BranchTargetStatus answers "is this on the target branch", not "is it durable
// somewhere", so it keeps comparing against the target ref proper. A fork rig
// whose PRs go to upstream must still see submittable work when the commit has
// only reached origin.
func TestBranchTargetStatusKeepsStrictTargetSemantics(t *testing.T) {
	localDir, mainBranch := forkLayoutRepo(t)
	g := NewGit(localDir)

	branch := "polecat/merged-work"
	runGit(t, localDir, "checkout", "-b", branch)

	target, err := g.BranchTargetStatus(branch, "origin", []string{mainBranch})
	if err != nil {
		t.Fatalf("BranchTargetStatus: %v", err)
	}
	if target.ComparisonBase != "upstream/"+mainBranch {
		t.Fatalf("ComparisonBase = %q, want upstream/%s", target.ComparisonBase, mainBranch)
	}
	if target.Preserved || target.UnpreservedPatchCount == 0 {
		t.Fatalf("work absent from the target ref reported as present: %+v", target)
	}
}
