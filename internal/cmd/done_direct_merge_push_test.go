package cmd

import (
	"path/filepath"
	"testing"
)

// gt-myia: the two direct-merge push sites in runDone set push_failed straight
// from git's exit status, the same defect gt-3bzt fixed on the default MR path.
// A direct-merge push refspec is branch:<defaultBranch>, so a later push of the
// same (unchanged) local branch is refused as non-fast-forward once origin/
// <defaultBranch> has moved ahead of it — even though the commit the polecat
// cares about is already an ancestor of that tip and nothing is at risk.
//
// This proves the two primitives runDone's direct-merge fix now composes
// (g.Push failing, g.VerifyPushedCommitReachableFromPushTarget succeeding)
// actually occur together for a direct-merge refspec, the way
// TestClassifyFailedBranchPush_CommitAlreadyMerged proves it for the MR path.
func TestDirectMergePush_AlreadyLandedIsNotPushFailed(t *testing.T) {
	g, townRoot, _, branch, defaultBranch := classifyPushRepo(t)
	repo := g.WorkDir()

	writeRecoveryFile(t, filepath.Join(repo, "work.txt"), "polecat work")
	runGit(t, repo, "add", "work.txt")
	runGit(t, repo, "commit", "-m", "polecat work")

	head, err := g.Rev("HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// First direct-merge push lands the commit on origin/<defaultBranch>.
	if pushErr := g.Push("origin", branch+":"+defaultBranch, false); pushErr != nil {
		t.Fatalf("initial direct push: %v", pushErr)
	}

	// Someone else advances the default branch further, from a separate clone.
	other := filepath.Join(townRoot, "other")
	origin := filepath.Join(townRoot, "origin.git")
	runCmd(t, townRoot, "git", "clone", origin, other)
	runGit(t, other, "config", "user.email", "test@example.com")
	runGit(t, other, "config", "user.name", "Test User")
	writeRecoveryFile(t, filepath.Join(other, "other.txt"), "someone else's work")
	runGit(t, other, "add", "other.txt")
	runGit(t, other, "commit", "-m", "someone else's work")
	runGit(t, other, "push", "origin", defaultBranch)

	// A re-run of gt done pushes the same local branch again: a non-fast-forward,
	// since origin/<defaultBranch> is now ahead of it — but head is still an
	// ancestor, so nothing the polecat did is at risk.
	pushErr := g.Push("origin", branch+":"+defaultBranch, false)
	if pushErr == nil {
		t.Fatal("expected second direct push to be rejected as non-fast-forward")
	}
	if verifyErr := g.VerifyPushedCommitReachableFromPushTarget("origin", defaultBranch, head); verifyErr != nil {
		t.Errorf("VerifyPushedCommitReachableFromPushTarget = %v, want nil (commit already landed)", verifyErr)
	}
}
