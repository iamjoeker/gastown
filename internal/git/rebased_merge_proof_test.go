package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rebasedMergeRepo builds the state the refinery is in on every MR after the
// first of a cycle: the branch was submitted at one sha, the target moved, the
// branch was rebased onto the new baseline, and the rebased commit landed. The
// submitted sha is then reachable from nothing on the target.
//
// Returns the worktree, the main branch name, the submitted (pre-rebase) sha and
// the sha that actually landed.
func rebasedMergeRepo(t *testing.T) (string, string, string, string) {
	t.Helper()
	localDir, _, mainBranch := initTestRepoWithRemote(t)

	runGit(t, localDir, "checkout", "-b", "polecat/rebased")
	writeRepoFile(t, localDir, "worker.go", "package worker\n")
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "worker: the submitted work")
	submitted := revParse(t, localDir, "HEAD")
	runGit(t, localDir, "push", "origin", "polecat/rebased")

	// The target moves while the MR sits in the queue.
	runGit(t, localDir, "checkout", mainBranch)
	writeRepoFile(t, localDir, "other.go", "package other\n")
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "another MR landed first")
	runGit(t, localDir, "push", "origin", mainBranch)

	// The refinery's documented path: rebase onto the new baseline, then land.
	runGit(t, localDir, "checkout", "polecat/rebased")
	runGit(t, localDir, "rebase", mainBranch)
	landedBranchHead := revParse(t, localDir, "HEAD")
	runGit(t, localDir, "checkout", mainBranch)
	runGit(t, localDir, "merge", "--ff-only", "polecat/rebased")
	runGit(t, localDir, "push", "origin", mainBranch)

	return localDir, mainBranch, submitted, landedBranchHead
}

func writeRepoFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := NewGit(dir).Rev(ref)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(out)
}

// The bug: sequential rebasing is the refinery's REQUIRED path, so a proof that
// asks for the submitted sha by ancestry rejects a correct merge on every MR
// after the first in a cycle. The ancestry check must still say no here — that
// is what makes the patch check the thing doing the work. (gt-wkcz)
func TestVerifyWorkLandedOnPushTargetAcceptsARebasedMerge(t *testing.T) {
	localDir, mainBranch, submitted, landed := rebasedMergeRepo(t)
	g := NewGit(localDir)

	if submitted == landed {
		t.Fatal("fixture did not rewrite the commit: the rebase case is not being tested")
	}
	if err := g.VerifyPushedCommitReachableFromPushTarget("origin", mainBranch, submitted); err == nil {
		t.Fatal("ancestry check passed on a rebased sha; the fixture no longer reproduces the bug")
	}

	proof, err := g.VerifyWorkLandedOnPushTarget("origin", mainBranch, submitted)
	if err != nil {
		t.Fatalf("VerifyWorkLandedOnPushTarget on a rebased merge: %v", err)
	}
	if !proof.Rebased {
		t.Fatal("proof.Rebased = false, want true: the submitted sha is not the landed sha")
	}
}

// The un-rebased MR — the first of a cycle — still proves by identity, and must
// not be reported as rebased.
func TestVerifyWorkLandedOnPushTargetReportsAnUnrebasedMergeAsExact(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	runGit(t, localDir, "checkout", "-b", "polecat/direct")
	writeRepoFile(t, localDir, "direct.go", "package direct\n")
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "direct: submitted work")
	submitted := revParse(t, localDir, "HEAD")
	runGit(t, localDir, "checkout", mainBranch)
	runGit(t, localDir, "merge", "--ff-only", "polecat/direct")
	runGit(t, localDir, "push", "origin", mainBranch)

	proof, err := g.VerifyWorkLandedOnPushTarget("origin", mainBranch, submitted)
	if err != nil {
		t.Fatalf("VerifyWorkLandedOnPushTarget on a fast-forward merge: %v", err)
	}
	if proof.Rebased {
		t.Fatal("proof.Rebased = true for a sha that is on the target unchanged")
	}
}

// Accepting a rebase must not become accepting anything: work that never landed
// still fails, and the failure says how many patches are missing rather than
// only that a sha is absent.
func TestVerifyWorkLandedOnPushTargetRejectsWorkThatNeverLanded(t *testing.T) {
	localDir, mainBranch, _, _ := rebasedMergeRepo(t)
	g := NewGit(localDir)

	runGit(t, localDir, "checkout", "-b", "polecat/unlanded")
	writeRepoFile(t, localDir, "unlanded.go", "package unlanded\n")
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "unlanded: never merged")
	unlanded := revParse(t, localDir, "HEAD")

	proof, err := g.VerifyWorkLandedOnPushTarget("origin", mainBranch, unlanded)
	if err == nil {
		t.Fatal("VerifyWorkLandedOnPushTarget passed for work that is not on the target")
	}
	if proof.UnmergedPatches != 1 {
		t.Fatalf("proof.UnmergedPatches = %d, want 1", proof.UnmergedPatches)
	}
	if !strings.Contains(err.Error(), "no patch-equivalent") {
		t.Fatalf("error %q does not name the patch check as the reason", err)
	}
}

// A sha this repository has never seen is an unanswerable question, not a
// negative answer: reporting "not landed" for it would send a caller looking for
// lost work that may be perfectly safe.
func TestVerifyWorkLandedOnPushTargetSeparatesAnAbsentObjectFromAbsentWork(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	_, err := g.VerifyWorkLandedOnPushTarget("origin", mainBranch, "0123456789abcdef0123456789abcdef01234567")
	if err == nil {
		t.Fatal("VerifyWorkLandedOnPushTarget passed for a sha not in the repository")
	}
	if !strings.Contains(err.Error(), "not in this repository") {
		t.Fatalf("error %q does not say the commit is missing locally", err)
	}
}
