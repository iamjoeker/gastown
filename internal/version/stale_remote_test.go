package version

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newClonedRepo builds the shape a Gas Town actually has: a remote that merges
// land on, and a working clone that nothing fast-forwards. It returns
// (remoteDir, cloneDir); the clone is on main with origin/main tracking.
func newClonedRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	remote := newGitRepo(t)
	gitCommit(t, remote, "a.go", "1")
	gitRun(t, remote, "branch", "-M", "main")

	clone := filepath.Join(t.TempDir(), "clone")
	gitRun(t, remote, "clone", "-q", remote, clone)
	gitRun(t, clone, "config", "commit.gpgsign", "false")
	return remote, clone
}

// TestCheckStaleBinary_RemoteMovesWhileNoLocalRefDoes is the test gt-ympl asked
// for: land a commit on the remote's main, touch no local checkout, and assert
// the binary reports stale.
//
// It also pins the defect in the same run. The local-only check — every ref it
// can see is a cache with no invalidation — answers "fresh" here, which is the
// wrong answer and the whole reason the rebuild loop could not self-heal. That
// assertion is what makes the refreshed one mean something: without it, a
// refreshed check passing proves only that the fixture is not broken.
func TestCheckStaleBinary_RemoteMovesWhileNoLocalRefDoes(t *testing.T) {
	remote, clone := newClonedRepo(t)

	binaryCommit := gitRun(t, clone, "rev-parse", "HEAD")
	setBinaryCommit(t, binaryCommit)

	// A merge lands on the remote. Nothing pulls, nothing fetches.
	landed := gitCommit(t, remote, "b.go", "2")

	local := CheckStaleBinary(clone)
	if local.Error != nil {
		t.Fatalf("local check errored: %v", local.Error)
	}
	if local.IsStale {
		t.Fatalf("fixture is not reproducing the defect: the local-only check saw the remote move")
	}
	if local.Refreshed {
		t.Errorf("local-only check must not claim it read the remote")
	}

	refreshed := CheckStaleBinaryWithOptions(clone, StaleOptions{RefreshRemote: true})
	if refreshed.Error != nil {
		t.Fatalf("refreshed check errored: %v", refreshed.Error)
	}
	if refreshed.Skipped {
		t.Fatalf("refreshed check skipped: %s", refreshed.SkipReason)
	}
	if !refreshed.IsStale {
		t.Fatalf("binary built at %s must be stale after %s landed on the remote", ShortCommit(binaryCommit), ShortCommit(landed))
	}
	if !refreshed.Refreshed {
		t.Errorf("Refreshed should be true when the tip came from the remote")
	}
	if refreshed.RepoCommit != landed {
		t.Errorf("RepoCommit = %q, want the remote tip %q", refreshed.RepoCommit, landed)
	}
	if refreshed.CompareRef != "origin/main" {
		t.Errorf("CompareRef = %q, want origin/main", refreshed.CompareRef)
	}
	if refreshed.CommitsBehind != 1 {
		t.Errorf("CommitsBehind = %d, want 1", refreshed.CommitsBehind)
	}
	if !refreshed.IsForward {
		t.Errorf("IsForward should be true: the landed commit descends from the binary's")
	}
	if !refreshed.OnMainBranch {
		t.Errorf("OnMainBranch should be true: the clone is on main")
	}
}

// TestCheckStaleBinary_RefreshedFreshIsStillFresh guards the other direction:
// reading the remote must not invent staleness when there is none.
func TestCheckStaleBinary_RefreshedFreshIsStillFresh(t *testing.T) {
	_, clone := newClonedRepo(t)
	setBinaryCommit(t, gitRun(t, clone, "rev-parse", "HEAD"))

	info := CheckStaleBinaryWithOptions(clone, StaleOptions{RefreshRemote: true})
	if info.Error != nil {
		t.Fatalf("unexpected error: %v", info.Error)
	}
	if info.Skipped {
		t.Fatalf("unexpected skip: %s", info.SkipReason)
	}
	if info.IsStale {
		t.Errorf("binary at the remote tip must not be stale")
	}
	if !info.Refreshed {
		t.Errorf("Refreshed should be true")
	}
}

// TestCheckStaleBinary_UnreachableRemoteDoesNotReportFresh: when the remote
// cannot be read, a binary that is level with the local ref must report
// Skipped, not fresh. Reporting fresh is the exact failure this whole change
// exists to remove, and an unreachable remote is the one path that could
// reintroduce it.
func TestCheckStaleBinary_UnreachableRemoteDoesNotReportFresh(t *testing.T) {
	_, clone := newClonedRepo(t)
	setBinaryCommit(t, gitRun(t, clone, "rev-parse", "HEAD"))
	gitRun(t, clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "no-such-repo"))

	info := CheckStaleBinaryWithOptions(clone, StaleOptions{RefreshRemote: true})
	if info.Error != nil {
		t.Fatalf("unexpected error: %v", info.Error)
	}
	if info.IsStale {
		t.Errorf("must not invent staleness from a failed remote read")
	}
	if info.Refreshed {
		t.Errorf("Refreshed must be false when the remote read failed")
	}
	if !info.Skipped {
		t.Fatalf("an unverified 'fresh' must be reported as Skipped, got IsStale=%v Skipped=false", info.IsStale)
	}
	if !strings.Contains(info.SkipReason, "cannot prove the binary is fresh") {
		t.Errorf("SkipReason = %q, want it to say freshness is unproven", info.SkipReason)
	}
	if info.RefreshError == "" {
		t.Errorf("RefreshError should carry why the remote read failed")
	}
}

// TestCheckStaleBinary_UnreachableRemoteStillReportsStale: staleness detection
// is one-sided. A local ref can only LAG the remote, never lead it, so a binary
// measured as behind that ref is behind the remote too — that verdict survives
// a failed remote read and must not be downgraded to Skipped, or an offline
// town could never rebuild at all.
func TestCheckStaleBinary_UnreachableRemoteStillReportsStale(t *testing.T) {
	_, clone := newClonedRepo(t)
	old := gitRun(t, clone, "rev-parse", "HEAD")
	setBinaryCommit(t, old)
	gitCommit(t, clone, "b.go", "2")
	gitRun(t, clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "no-such-repo"))

	info := CheckStaleBinaryWithOptions(clone, StaleOptions{RefreshRemote: true})
	if info.Error != nil {
		t.Fatalf("unexpected error: %v", info.Error)
	}
	if !info.IsStale {
		t.Fatalf("behind the local ref proves behind the remote; must stay stale")
	}
	if info.Skipped {
		t.Errorf("a positive staleness verdict must not be downgraded to Skipped: %s", info.SkipReason)
	}
	if info.Refreshed {
		t.Errorf("Refreshed must be false when the remote read failed")
	}
}

// TestCheckStaleBinary_RefreshFromFeatureBranch: the rig is not always parked
// on main. From a feature branch the compare ref is resolved from the
// candidate list, and the refresh must re-read THAT branch from ITS remote
// rather than defaulting to something else.
func TestCheckStaleBinary_RefreshFromFeatureBranch(t *testing.T) {
	remote, clone := newClonedRepo(t)
	binaryCommit := gitRun(t, clone, "rev-parse", "HEAD")
	setBinaryCommit(t, binaryCommit)

	gitRun(t, clone, "checkout", "-q", "-b", "feat/x")
	gitCommit(t, clone, "feature.go", "unmerged work")
	landed := gitCommit(t, remote, "b.go", "2")

	info := CheckStaleBinaryWithOptions(clone, StaleOptions{RefreshRemote: true})
	if info.Error != nil {
		t.Fatalf("unexpected error: %v", info.Error)
	}
	if info.Skipped {
		t.Fatalf("unexpected skip: %s", info.SkipReason)
	}
	if !info.IsStale {
		t.Fatalf("binary must be stale: %s landed on the remote's main", ShortCommit(landed))
	}
	if info.RepoCommit != landed {
		t.Errorf("RepoCommit = %q, want the remote main tip %q (not the feature HEAD)", info.RepoCommit, landed)
	}
	if info.OnMainBranch {
		t.Errorf("OnMainBranch must be false on feat/x")
	}
}

func TestBranchRemote(t *testing.T) {
	_, clone := newClonedRepo(t)

	if got := branchRemote(clone, "main"); got != "origin" {
		t.Errorf("branchRemote(main) = %q, want origin", got)
	}
	// A branch tracking nothing falls back rather than returning empty, which
	// would make the fetch below it fail on an unnamed remote.
	gitRun(t, clone, "branch", "untracked-branch")
	if got := branchRemote(clone, "untracked-branch"); got != defaultRemote {
		t.Errorf("branchRemote(untracked-branch) = %q, want %q", got, defaultRemote)
	}
	if got := branchRemote(clone, ""); got != defaultRemote {
		t.Errorf("branchRemote(\"\") = %q, want %q", got, defaultRemote)
	}

	gitRun(t, clone, "config", "branch.upstream-tracked.remote", "upstream")
	if got := branchRemote(clone, "upstream-tracked"); got != "upstream" {
		t.Errorf("branchRemote(upstream-tracked) = %q, want upstream", got)
	}
}

// TestSingleBranchRefSplitsRemoteAndBranch pins the split that decides WHICH
// remote and WHICH branch a refresh re-reads. Getting it wrong is silent: a
// fetch of "origin/carry/operational" from origin asks for a branch that does
// not exist, the refresh fails, and the check falls back to the local ref —
// the original bug wearing a fix.
func TestSingleBranchRefSplitsRemoteAndBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("remote-tracking carry", func(t *testing.T) {
		dir := newGitRepo(t)
		c1 := gitCommit(t, dir, "a.go", "1")
		gitRun(t, dir, "update-ref", "refs/remotes/origin/carry/operational", c1)

		ref, ok := singleBranchRef(dir, "refs/remotes/origin/carry/")
		if !ok {
			t.Fatal("expected the single remote carry ref to resolve")
		}
		if ref.remote != "origin" || ref.branch != "carry/operational" {
			t.Errorf("got remote=%q branch=%q, want origin / carry/operational", ref.remote, ref.branch)
		}
	})

	t.Run("local carry has no remote in its name", func(t *testing.T) {
		dir := newGitRepo(t)
		gitCommit(t, dir, "a.go", "1")
		gitRun(t, dir, "branch", "-M", "main")
		gitRun(t, dir, "branch", "carry/operational")

		ref, ok := singleBranchRef(dir, "refs/heads/carry/")
		if !ok {
			t.Fatal("expected the single local carry ref to resolve")
		}
		if ref.remote != "" || ref.branch != "carry/operational" {
			t.Errorf("got remote=%q branch=%q, want \"\" / carry/operational", ref.remote, ref.branch)
		}
	})
}
