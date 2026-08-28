package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scenario these tests reproduce is the one gt-3bzt reported: a polecat
// finishes, rebases its branch onto current origin/main as step 7 of
// mol-polecat-work tells it to, and the push that follows is refused for being a
// non-fast-forward. The refusal is what the rebase guarantees; it was being read
// as a failed push, and the flag that read set stranded the polecat.

// rebasedBranchRepo builds a local repo with a published polecat branch whose
// history is then rewritten, exactly as a rebase would. It returns the repo path
// and the branch name.
func rebasedBranchRepo(t *testing.T) (string, string) {
	t.Helper()
	localDir, _, _ := initTestRepoWithRemote(t)
	const branch = "polecat/dust"

	runGit(t, localDir, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(localDir, "work.txt"), []byte("first\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "polecat work")
	runGit(t, localDir, "push", "origin", branch+":"+branch)

	// Rewrite the published history. An amend is the cheapest faithful stand-in
	// for a rebase: same tree, different sha, so the remote tip stops being an
	// ancestor of HEAD — which is the whole of what makes the next push a
	// non-fast-forward.
	runGit(t, localDir, "commit", "--amend", "-m", "polecat work (rebased)")

	return localDir, branch
}

func TestIsNonFastForwardPushError_ClassifiesRebasedBranchPush(t *testing.T) {
	localDir, branch := rebasedBranchRepo(t)
	g := NewGit(localDir)

	err := g.Push("origin", branch+":"+branch, false)
	if err == nil {
		// Without this the whole test is vacuous: it would be asserting a
		// classification of an error that never occurred.
		t.Fatal("plain push of a rewritten branch succeeded; the non-fast-forward this test classifies never happened")
	}
	if !IsNonFastForwardPushError(err) {
		t.Errorf("IsNonFastForwardPushError = false for a real non-fast-forward rejection: %v", err)
	}
}

// The control for the test above: a push that fails for a reason that is NOT a
// non-fast-forward must not classify as one, or the predicate would wave through
// every push failure there is.
func TestIsNonFastForwardPushError_RejectsOtherPushFailures(t *testing.T) {
	localDir := initTestRepo(t)
	g := NewGit(localDir)

	err := g.Push("no-such-remote", "main:main", false)
	if err == nil {
		t.Fatal("push to a nonexistent remote succeeded")
	}
	if IsNonFastForwardPushError(err) {
		t.Errorf("IsNonFastForwardPushError = true for an unrelated push failure: %v", err)
	}

	if IsNonFastForwardPushError(nil) {
		t.Error("IsNonFastForwardPushError(nil) = true")
	}
}

func TestPushWithLease_RepublishesRebasedBranch(t *testing.T) {
	localDir, branch := rebasedBranchRepo(t)
	g := NewGit(localDir)

	if err := g.Push("origin", branch+":"+branch, false); err == nil {
		t.Fatal("plain push succeeded; nothing here needed a lease")
	}

	if err := g.PushWithLease("origin", branch, branch); err != nil {
		t.Fatalf("PushWithLease: %v", err)
	}

	head, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev HEAD: %v", err)
	}
	tip, err := g.RemoteBranchTip("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}
	if tip != head {
		t.Errorf("origin/%s = %s after lease push, want HEAD %s", branch, tip, head)
	}
}

// The property that makes a force push safe here is not the lease — a lease read
// immediately before the write would happily overwrite what it just read. It is
// that the remote's content must survive in the branch replacing it. A commit
// only the remote has must stop this push.
func TestPushWithLease_RefusesToDropRemoteOnlyCommit(t *testing.T) {
	localDir, branch := rebasedBranchRepo(t)
	g := NewGit(localDir)

	// A second clone stands in for the other actor, and adds a commit this repo
	// has never seen.
	tmp := t.TempDir()
	otherDir := filepath.Join(tmp, "other")
	remoteURL, err := g.RemoteURL("origin")
	if err != nil {
		t.Fatalf("RemoteURL: %v", err)
	}
	runGit(t, tmp, "clone", remoteURL, otherDir)
	runGit(t, otherDir, "config", "user.email", "other@test.com")
	runGit(t, otherDir, "config", "user.name", "Other User")
	runGit(t, otherDir, "checkout", branch)
	if err := os.WriteFile(filepath.Join(otherDir, "other.txt"), []byte("theirs\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, otherDir, "add", ".")
	runGit(t, otherDir, "commit", "-m", "someone else's commit")
	runGit(t, otherDir, "push", "origin", branch)

	before, err := g.RemoteBranchTip("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}

	leaseErr := g.PushWithLease("origin", branch, branch)
	if leaseErr == nil {
		t.Fatal("PushWithLease overwrote a remote commit that exists nowhere in the local branch")
	}
	// Named, not merely non-nil: a refusal for an unrelated reason (an
	// unreachable remote, a fetch that failed) would leave this test green while
	// the guard it is about never ran.
	if !strings.Contains(leaseErr.Error(), "not preserved in") {
		t.Errorf("PushWithLease refused for the wrong reason: %v", leaseErr)
	}

	after, err := g.RemoteBranchTip("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}
	if after != before {
		t.Errorf("origin/%s moved from %s to %s despite the refusal", branch, before, after)
	}
}

// gt done only ever leases a polecat's own feature branch. The default branch is
// refused outright, so no future caller can reach a rewrite of main through here.
func TestPushWithLease_RefusesDefaultBranch(t *testing.T) {
	localDir, _, _ := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	defaultBranch := g.RemoteDefaultBranch()
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	err := g.PushWithLease("origin", defaultBranch, defaultBranch)
	if err == nil {
		t.Fatalf("PushWithLease over the default branch %s was allowed", defaultBranch)
	}
	if !strings.Contains(err.Error(), "default branch") {
		t.Errorf("PushWithLease refused for the wrong reason: %v", err)
	}
}
