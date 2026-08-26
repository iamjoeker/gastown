package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mergeProofGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeNumberedFile writes a file whose lines are individually addressable, so a
// test can change one line and leave the rest as the context that patch-id
// hashes.
func writeNumberedFile(t *testing.T, dir, name string, edits map[int]string) {
	t.Helper()
	lines := make([]string, 0, 24)
	for i := 1; i <= 24; i++ {
		if text, ok := edits[i]; ok {
			lines = append(lines, text)
			continue
		}
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestVerifyCommitLandedOnPushTargetProvesRebasedLanding reproduces gt-umq0: two
// MRs submitted against the same main, the first lands by fast-forward and the
// second has to be rebased because the first got there first. Sha containment
// can only prove the first. The second is as good as the first in every respect
// that is about the work.
//
// The adjacent change is deliberate. It puts the fixture in the case the naive
// patch-id rule cannot do either — a rebase across a nearby edit rewrites the
// context lines patch-id hashes — and the test asserts that `git cherry` really
// does fail here, so the merge-tree proof is not being credited for work a
// simpler check would have done.
func TestVerifyCommitLandedOnPushTargetProvesRebasedLanding(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)
	const branch = "polecat/test/gt-rebased"

	writeNumberedFile(t, localDir, "app.txt", nil)
	mergeProofGit(t, localDir, "add", "app.txt")
	mergeProofGit(t, localDir, "commit", "-m", "base")
	mergeProofGit(t, localDir, "push", "origin", mainBranch)

	// The polecat branches from that main and submits one commit.
	mergeProofGit(t, localDir, "checkout", "-b", branch)
	writeNumberedFile(t, localDir, "app.txt", map[int]string{20: "line 20 — polecat work"})
	mergeProofGit(t, localDir, "commit", "-am", "polecat work")
	submitted := mergeProofGit(t, localDir, "rev-parse", "HEAD")
	mergeProofGit(t, localDir, "push", "origin", branch)

	// Another MR lands first, so main moves under the submission.
	mergeProofGit(t, localDir, "checkout", mainBranch)
	writeNumberedFile(t, localDir, "app.txt", map[int]string{18: "line 18 — the other MR"})
	mergeProofGit(t, localDir, "commit", "-am", "the other MR")
	mergeProofGit(t, localDir, "push", "origin", mainBranch)

	// The refinery does what its brief prescribes: rebase, then land.
	mergeProofGit(t, localDir, "checkout", branch)
	mergeProofGit(t, localDir, "rebase", mainBranch)
	rebased := mergeProofGit(t, localDir, "rev-parse", "HEAD")
	mergeProofGit(t, localDir, "checkout", mainBranch)
	mergeProofGit(t, localDir, "merge", "--ff-only", branch)
	mergeProofGit(t, localDir, "push", "origin", mainBranch)

	if rebased == submitted {
		t.Fatal("fixture did not rebase: the submitted sha survived, so the case under test never arose")
	}

	// Control: the scenario really is the one that used to be refused.
	if err := g.VerifyPushedCommitReachableFromPushTarget("origin", mainBranch, submitted); err == nil {
		t.Fatal("sha containment proved a rebased landing; the fixture is not exercising gt-umq0")
	}

	// Control: patch-id alone cannot prove it either, because the adjacent edit
	// rewrote the context this commit's hunk carries.
	cherry, err := g.Cherry(mainBranch, submitted)
	if err != nil {
		t.Fatalf("Cherry: %v", err)
	}
	if CountCherryUnmergedCommits(cherry) == 0 {
		t.Fatalf("patch-id already matches (%q), so this fixture no longer covers the context-drift caveat", cherry)
	}

	proof, err := g.VerifyCommitLandedOnPushTarget("origin", mainBranch, submitted)
	if err != nil {
		t.Fatalf("VerifyCommitLandedOnPushTarget on a clean rebased landing: %v", err)
	}
	if proof.Method != MergeProofContent {
		t.Fatalf("proof method = %q, want %q", proof.Method, MergeProofContent)
	}
	if proof.Evidence != "merge_tree_noop" {
		t.Fatalf("proof evidence = %q, want merge_tree_noop", proof.Evidence)
	}
	if proof.TargetTip != rebased {
		t.Fatalf("proof target tip = %s, want the landed head %s", proof.TargetTip, rebased)
	}
}

// A rebase that did NOT touch anything near the polecat's work leaves patch-id
// intact, which is the case the bead's proposed rule covers. It must still be
// proven, and by content rather than by sha.
func TestVerifyCommitLandedOnPushTargetProvesRebaseWithIntactPatchID(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)
	const branch = "polecat/test/gt-far"

	writeNumberedFile(t, localDir, "app.txt", nil)
	mergeProofGit(t, localDir, "add", "app.txt")
	mergeProofGit(t, localDir, "commit", "-m", "base")
	mergeProofGit(t, localDir, "push", "origin", mainBranch)

	mergeProofGit(t, localDir, "checkout", "-b", branch)
	writeNumberedFile(t, localDir, "app.txt", map[int]string{24: "line 24 — polecat work"})
	mergeProofGit(t, localDir, "commit", "-am", "polecat work")
	submitted := mergeProofGit(t, localDir, "rev-parse", "HEAD")

	// The other MR lands in a different file entirely.
	mergeProofGit(t, localDir, "checkout", mainBranch)
	if err := os.WriteFile(filepath.Join(localDir, "other.txt"), []byte("elsewhere\n"), 0644); err != nil {
		t.Fatalf("write other.txt: %v", err)
	}
	mergeProofGit(t, localDir, "add", "other.txt")
	mergeProofGit(t, localDir, "commit", "-m", "the other MR")
	mergeProofGit(t, localDir, "push", "origin", mainBranch)

	mergeProofGit(t, localDir, "checkout", branch)
	mergeProofGit(t, localDir, "rebase", mainBranch)
	mergeProofGit(t, localDir, "checkout", mainBranch)
	mergeProofGit(t, localDir, "merge", "--ff-only", branch)
	mergeProofGit(t, localDir, "push", "origin", mainBranch)

	if err := g.VerifyPushedCommitReachableFromPushTarget("origin", mainBranch, submitted); err == nil {
		t.Fatal("sha containment proved a rebased landing; the fixture is not exercising gt-umq0")
	}
	proof, err := g.VerifyCommitLandedOnPushTarget("origin", mainBranch, submitted)
	if err != nil {
		t.Fatalf("VerifyCommitLandedOnPushTarget: %v", err)
	}
	if proof.Method != MergeProofContent {
		t.Fatalf("proof method = %q, want %q", proof.Method, MergeProofContent)
	}
}

// A fast-forward landing is still proven by sha, and says so.
func TestVerifyCommitLandedOnPushTargetReportsShaContainment(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	writeNumberedFile(t, localDir, "app.txt", nil)
	mergeProofGit(t, localDir, "add", "app.txt")
	mergeProofGit(t, localDir, "commit", "-m", "base")
	submitted := mergeProofGit(t, localDir, "rev-parse", "HEAD")
	mergeProofGit(t, localDir, "push", "origin", mainBranch)

	proof, err := g.VerifyCommitLandedOnPushTarget("origin", mainBranch, submitted)
	if err != nil {
		t.Fatalf("VerifyCommitLandedOnPushTarget: %v", err)
	}
	if proof.Method != MergeProofSHA {
		t.Fatalf("proof method = %q, want %q", proof.Method, MergeProofSHA)
	}
}

// Work that never landed is still refused — the point of accepting content
// equivalence is to stop refusing correct landings, not to stop refusing.
func TestVerifyCommitLandedOnPushTargetRefusesUnlandedWork(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	writeNumberedFile(t, localDir, "app.txt", nil)
	mergeProofGit(t, localDir, "add", "app.txt")
	mergeProofGit(t, localDir, "commit", "-m", "base")
	mergeProofGit(t, localDir, "push", "origin", mainBranch)

	mergeProofGit(t, localDir, "checkout", "-b", "polecat/test/gt-unlanded")
	writeNumberedFile(t, localDir, "app.txt", map[int]string{12: "line 12 — never landed"})
	mergeProofGit(t, localDir, "commit", "-am", "never landed")
	orphan := mergeProofGit(t, localDir, "rev-parse", "HEAD")

	_, err := g.VerifyCommitLandedOnPushTarget("origin", mainBranch, orphan)
	if err == nil {
		t.Fatal("VerifyCommitLandedOnPushTarget accepted work that is not on the target")
	}
	if !errors.Is(err, ErrCommitNotLanded) {
		t.Fatalf("error = %v, want ErrCommitNotLanded", err)
	}
	if errors.Is(err, ErrMergeProofUnprovable) {
		t.Fatalf("unlanded work reported as unprovable: %v", err)
	}
}

// A commit this repository cannot read is a different answer from "not landed",
// and the two want different operator responses (gt-umq0).
func TestVerifyCommitLandedOnPushTargetSeparatesUnprovableFromUnlanded(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	writeNumberedFile(t, localDir, "app.txt", nil)
	mergeProofGit(t, localDir, "add", "app.txt")
	mergeProofGit(t, localDir, "commit", "-m", "base")
	mergeProofGit(t, localDir, "push", "origin", mainBranch)

	_, err := g.VerifyCommitLandedOnPushTarget("origin", mainBranch, "0123456789012345678901234567890123456789")
	if err == nil {
		t.Fatal("VerifyCommitLandedOnPushTarget accepted an unreadable commit")
	}
	if !errors.Is(err, ErrMergeProofUnprovable) {
		t.Fatalf("error = %v, want ErrMergeProofUnprovable", err)
	}
	if errors.Is(err, ErrCommitNotLanded) {
		t.Fatalf("an unreadable commit was reported as not landed: %v", err)
	}

	if _, err := g.VerifyCommitLandedOnPushTarget("origin", mainBranch, "   "); !errors.Is(err, ErrMergeProofUnprovable) {
		t.Fatalf("empty commit error = %v, want ErrMergeProofUnprovable", err)
	}
}
