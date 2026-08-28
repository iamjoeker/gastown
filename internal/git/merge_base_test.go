package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMergeBaseSeparatesBranchBaseFromTargetTip builds the fixture gt-eygw was
// filed from: a polecat branch cut from main, another MR landing on main
// underneath it, and no rebase in between — the state `gt done --pre-verified`
// deliberately leaves the branch in, because rebasing would invalidate the gate
// results the flag attests to.
//
// The point of the assertion pair is that the tip and the merge-base are
// different commits here while being the same commit in the rebased control
// below. A recorder that reaches for the tip is therefore right exactly when it
// is redundant and wrong exactly when it matters, which is why it went
// unnoticed: every passing observation of it was taken in the case that cannot
// fail.
func TestMergeBaseSeparatesBranchBaseFromTargetTip(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)
	const branch = "polecat/test/gt-eygw"

	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(localDir, name), []byte(body), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	write("app.txt", "base\n")
	mergeProofGit(t, localDir, "add", "app.txt")
	mergeProofGit(t, localDir, "commit", "-m", "base")
	mergeProofGit(t, localDir, "push", "origin", mainBranch)
	cutFrom := mergeProofGit(t, localDir, "rev-parse", "HEAD")

	// The polecat cuts its branch here and runs its gates against this commit.
	mergeProofGit(t, localDir, "checkout", "-b", branch)
	write("polecat.txt", "polecat work\n")
	mergeProofGit(t, localDir, "add", "polecat.txt")
	mergeProofGit(t, localDir, "commit", "-m", "polecat work")
	submitted := mergeProofGit(t, localDir, "rev-parse", "HEAD")

	// Another MR lands on main while the polecat is working.
	mergeProofGit(t, localDir, "checkout", mainBranch)
	write("other.txt", "the other MR\n")
	mergeProofGit(t, localDir, "add", "other.txt")
	mergeProofGit(t, localDir, "commit", "-m", "the other MR")
	mergeProofGit(t, localDir, "push", "origin", mainBranch)
	mergeProofGit(t, localDir, "checkout", branch)

	tip, err := g.Rev("origin/" + mainBranch)
	if err != nil {
		t.Fatalf("Rev(origin/%s): %v", mainBranch, err)
	}
	if tip == cutFrom {
		t.Fatalf("fixture did not move the target: tip and cut point are both %s", tip)
	}

	base, err := g.MergeBase("origin/"+mainBranch, submitted)
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if base != cutFrom {
		t.Errorf("merge-base = %s, want the commit the branch was cut from (%s)", base, cutFrom)
	}
	if base == tip {
		t.Errorf("merge-base equals the target tip (%s); the fixture is meant to hold them apart", tip)
	}

	// Control: the same branch, rebased. If the two values did not converge
	// here, a recorder switching from the tip to the merge-base would refuse
	// every fast path rather than only the ones that never earned it, and the
	// assertion above would be satisfied by a merge-base that is simply always
	// wrong.
	mergeProofGit(t, localDir, "rebase", mainBranch)
	rebased := mergeProofGit(t, localDir, "rev-parse", "HEAD")
	rebasedBase, err := g.MergeBase("origin/"+mainBranch, rebased)
	if err != nil {
		t.Fatalf("MergeBase after rebase: %v", err)
	}
	if rebasedBase != tip {
		t.Errorf("after rebase merge-base = %s, want the target tip %s", rebasedBase, tip)
	}
}

// TestMergeBaseReportsUnrelatedHistories keeps the error path honest: two roots
// have no common ancestor, and git exits non-zero with nothing on stdout. An
// empty string returned with a nil error would read as a valid base of "" and
// compare unequal to everything, which is the safe direction here by accident
// rather than by decision — callers should get the error and say so.
func TestMergeBaseReportsUnrelatedHistories(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	mergeProofGit(t, localDir, "checkout", "--orphan", "unrelated")
	mergeProofGit(t, localDir, "rm", "-rf", "--cached", ".")
	if err := os.WriteFile(filepath.Join(localDir, "orphan.txt"), []byte("orphan\n"), 0644); err != nil {
		t.Fatalf("write orphan.txt: %v", err)
	}
	mergeProofGit(t, localDir, "add", "orphan.txt")
	mergeProofGit(t, localDir, "commit", "-m", "orphan root")

	base, err := g.MergeBase(mainBranch, "unrelated")
	if err == nil {
		t.Fatalf("MergeBase across unrelated histories returned %q and no error", base)
	}
	if base != "" {
		t.Errorf("MergeBase returned %q alongside its error, want empty", base)
	}
}
