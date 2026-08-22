package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FETCH_HEAD is ONE FILE PER REPOSITORY, and a rig's .repo.git is shared by every
// polecat worktree, the deacon's dogs and the refinery. So "fetch, then read
// FETCH_HEAD" is not a sequence — it is a read of state any other process in the
// town may have rewritten in between (gt-880s). These tests hold the fetch-and-
// compare paths to a private destination ref instead.

// fetchHeadPath resolves the FETCH_HEAD git honours for this repo, rather than
// assuming .git/FETCH_HEAD: a worktree resolves it somewhere else, and a test
// that wrote to the wrong file would pass by never being read.
func fetchHeadPath(t *testing.T, g *Git) string {
	t.Helper()
	out, err := g.run("rev-parse", "--git-path", "FETCH_HEAD")
	if err != nil {
		t.Fatalf("rev-parse --git-path FETCH_HEAD: %v", err)
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(g.WorkDir(), path)
	}
	return path
}

// pushedPolecatBranch commits one file on a polecat branch, pushes it, and
// returns the branch name and its pushed sha with main checked out again.
func pushedPolecatBranch(t *testing.T, g *Git, localDir, mainBranch, branch, file string) (string, string) {
	t.Helper()
	if err := g.CreateBranch(branch); err != nil {
		t.Fatalf("CreateBranch %s: %v", branch, err)
	}
	if err := g.Checkout(branch); err != nil {
		t.Fatalf("Checkout %s: %v", branch, err)
	}
	if err := os.WriteFile(filepath.Join(localDir, file), []byte("work\n"), 0644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	if err := g.Add(file); err != nil {
		t.Fatalf("Add %s: %v", file, err)
	}
	if err := g.Commit("work on " + branch); err != nil {
		t.Fatalf("Commit %s: %v", branch, err)
	}
	sha, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev HEAD: %v", err)
	}
	runGit(t, localDir, "push", "origin", branch)
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout %s: %v", mainBranch, err)
	}
	return branch, strings.TrimSpace(sha)
}

// TestFetchHeadIsClobberedByTheNextFetch is the premise the fix rests on, proved
// rather than assumed, and it is the control for the tests below: if a future git
// ever made FETCH_HEAD safe to read after an unrelated fetch, this test is what
// says so, and the reasoning in fetchPushRemoteRefToPrivateRef would need
// revisiting.
//
// It is deterministic: the two fetches are sequenced by hand. Concurrently — the
// production case — the second one lands at an arbitrary moment, which is why the
// symptom was a count that varied per run against an unchanged remote.
func TestFetchHeadIsClobberedByTheNextFetch(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)
	branch, branchSHA := pushedPolecatBranch(t, g, localDir, mainBranch, "polecat/fetch-head-race", "race.txt")

	// What the old code did: fetch the candidate with no destination refspec.
	if _, err := g.run("fetch", "--no-tags", "origin", "refs/heads/"+branch); err != nil {
		t.Fatalf("fetch candidate: %v", err)
	}
	before, err := g.Rev("FETCH_HEAD")
	if err != nil {
		t.Fatalf("Rev FETCH_HEAD: %v", err)
	}
	if strings.TrimSpace(before) != branchSHA {
		t.Fatalf("FETCH_HEAD after candidate fetch = %s, want %s", strings.TrimSpace(before), branchSHA)
	}

	// A neighbour in the same gitdir fetches something else. Nothing about our
	// candidate changed; the remote did not move.
	if _, err := g.run("fetch", "--no-tags", "origin", "refs/heads/"+mainBranch); err != nil {
		t.Fatalf("neighbour fetch: %v", err)
	}
	after, err := g.Rev("FETCH_HEAD")
	if err != nil {
		t.Fatalf("Rev FETCH_HEAD after neighbour: %v", err)
	}
	if strings.TrimSpace(after) == branchSHA {
		t.Fatal("FETCH_HEAD survived an unrelated fetch: the shared-state premise no longer holds, so re-read the comment on fetchPushRemoteRefToPrivateRef")
	}
	mainSHA, err := g.Rev(mainBranch)
	if err != nil {
		t.Fatalf("Rev main: %v", err)
	}
	if strings.TrimSpace(after) != strings.TrimSpace(mainSHA) {
		t.Fatalf("FETCH_HEAD = %s after fetching %s, want %s", strings.TrimSpace(after), mainBranch, strings.TrimSpace(mainSHA))
	}
}

// A neighbour's fetch must not be able to change our verdict, and the way to be
// sure is that our verdict never consults the file a neighbour writes. A sentinel
// FETCH_HEAD that is still there afterwards proves both halves: we did not read
// it, and — because a destination refspec alone would still rewrite it — we did
// not write it either, so we are not the neighbour who breaks somebody else.
func TestPushRemoteRefTargetStatusDoesNotTouchFetchHead(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)
	branch, branchSHA := pushedPolecatBranch(t, g, localDir, mainBranch, "polecat/fetch-head-untouched", "untouched.txt")

	mainSHA, err := g.Rev(mainBranch)
	if err != nil {
		t.Fatalf("Rev main: %v", err)
	}
	// The hostile value is the one the real failure carried: the target's own tip,
	// which is what a plain `git fetch` in the same gitdir leaves behind. It is a
	// commit that exists and resolves, so nothing downstream errors — it just
	// answers about the wrong branch.
	sentinel := strings.TrimSpace(mainSHA) + "\t\tbranch '" + mainBranch + "' of a neighbour's fetch\n"
	fetchHead := fetchHeadPath(t, g)
	if err := os.WriteFile(fetchHead, []byte(sentinel), 0644); err != nil {
		t.Fatalf("write sentinel FETCH_HEAD: %v", err)
	}

	ref := mustPushRemoteRef(t, g, branch)
	if ref.Hash != branchSHA {
		t.Fatalf("listed hash = %s, want %s", ref.Hash, branchSHA)
	}
	status, err := g.PushRemoteRefTargetStatusAny("origin", ref, []string{"origin/" + mainBranch})
	if err != nil {
		t.Fatalf("PushRemoteRefTargetStatusAny: %v", err)
	}
	// The branch has a commit main does not: unpreserved. Read through a
	// FETCH_HEAD pointing at main it would look preserved, which is the wrong
	// answer in the dangerous direction — a stranded branch called landed.
	if status.Preserved {
		t.Fatalf("status = %+v, want unpreserved (the branch's commit is not on %s)", status, mainBranch)
	}
	if status.UnpreservedPatchCount == 0 {
		t.Fatalf("status = %+v, want at least one unpreserved patch", status)
	}

	got, err := os.ReadFile(fetchHead)
	if err != nil {
		t.Fatalf("read FETCH_HEAD: %v", err)
	}
	if string(got) != sentinel {
		t.Fatalf("FETCH_HEAD was rewritten:\n got %q\nwant %q\nthe fetch must not touch it — writing it makes this process the neighbour that breaks another", string(got), sentinel)
	}
}

// The private ref exists for the duration of one comparison. Left behind, it
// would accumulate one ref per branch per sweep in a repository every agent in
// the town shares, and hold their objects reachable for good.
func TestPushRemoteRefTargetStatusRemovesItsPrivateRef(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)
	branch, _ := pushedPolecatBranch(t, g, localDir, mainBranch, "polecat/private-ref-cleanup", "cleanup.txt")

	ref := mustPushRemoteRef(t, g, branch)
	if _, err := g.PushRemoteRefTargetStatusAny("origin", ref, []string{"origin/" + mainBranch}); err != nil {
		t.Fatalf("PushRemoteRefTargetStatusAny: %v", err)
	}

	out, err := g.run("for-each-ref", "--format=%(refname)", privateFetchRefNamespace)
	if err != nil {
		t.Fatalf("for-each-ref: %v", err)
	}
	if left := strings.TrimSpace(out); left != "" {
		t.Fatalf("private fetch refs left behind:\n%s", left)
	}
}

// A tip that genuinely moved between the listing and the fetch must say so. The
// old message said "changed while pruning", which named an operation the sweep
// does not perform — and because that error is what the branch sweep turns into
// its 'unknown' class, a reader had no way to tell a moving remote from a branch
// nothing could be decided about.
func TestFetchPushRemoteRefExactlyReportsAMovedTipAsMoved(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)
	branch, branchSHA := pushedPolecatBranch(t, g, localDir, mainBranch, "polecat/moved-tip", "moved.txt")

	staleSHA, err := g.Rev(branchSHA + "^")
	if err != nil {
		t.Fatalf("Rev parent: %v", err)
	}
	stale := strings.TrimSpace(staleSHA)

	_, cleanup, err := g.fetchPushRemoteRefExactly("origin", RemoteRef{Name: "refs/heads/" + branch, Hash: stale})
	cleanup()
	if err == nil {
		t.Fatal("fetchPushRemoteRefExactly accepted a hash the remote does not have")
	}
	msg := err.Error()
	for _, want := range []string{"moved between listing and fetch", shortSHA(stale), shortSHA(branchSHA), "re-run"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "pruning") {
		t.Errorf("error still blames pruning, which this path does not do: %q", msg)
	}

	out, err := g.run("for-each-ref", "--format=%(refname)", privateFetchRefNamespace)
	if err != nil {
		t.Fatalf("for-each-ref: %v", err)
	}
	if left := strings.TrimSpace(out); left != "" {
		t.Fatalf("private fetch ref left behind after a failure:\n%s", left)
	}
}

// Push verification read FETCH_HEAD too, and there the clobber can fail in the
// direction that matters: the value a neighbour leaves is usually the trunk's
// tip, and a commit that landed on the trunk IS an ancestor of it — so a push
// that never reached the branch could verify against a ref nobody asked about.
func TestVerifyPushedCommitReachableFromPushTargetDoesNotTouchFetchHead(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	branch := "polecat/verify-reachable"
	if err := g.CreateBranch(branch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout(branch); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "first.txt"), []byte("first\n"), 0644); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if err := g.Add("first.txt"); err != nil {
		t.Fatalf("Add first: %v", err)
	}
	if err := g.Commit("first"); err != nil {
		t.Fatalf("Commit first: %v", err)
	}
	firstSHA, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev first: %v", err)
	}
	first := strings.TrimSpace(firstSHA)
	runGit(t, localDir, "push", "origin", branch)

	// The branch advances on the remote, so verification cannot short-circuit on
	// an exact tip match and must take the fetch-and-compare path.
	if err := os.WriteFile(filepath.Join(localDir, "second.txt"), []byte("second\n"), 0644); err != nil {
		t.Fatalf("write second: %v", err)
	}
	if err := g.Add("second.txt"); err != nil {
		t.Fatalf("Add second: %v", err)
	}
	if err := g.Commit("second"); err != nil {
		t.Fatalf("Commit second: %v", err)
	}
	runGit(t, localDir, "push", "origin", branch)
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}

	// A commit that exists on the trunk and nowhere near the branch. This is both
	// the hostile FETCH_HEAD value and the negative case below.
	if err := os.WriteFile(filepath.Join(localDir, "trunk.txt"), []byte("trunk\n"), 0644); err != nil {
		t.Fatalf("write trunk: %v", err)
	}
	if err := g.Add("trunk.txt"); err != nil {
		t.Fatalf("Add trunk: %v", err)
	}
	if err := g.Commit("trunk only"); err != nil {
		t.Fatalf("Commit trunk: %v", err)
	}
	runGit(t, localDir, "push", "origin", mainBranch)

	mainSHA, err := g.Rev(mainBranch)
	if err != nil {
		t.Fatalf("Rev main: %v", err)
	}
	sentinel := strings.TrimSpace(mainSHA) + "\t\tbranch '" + mainBranch + "' of a neighbour's fetch\n"
	fetchHead := fetchHeadPath(t, g)
	if err := os.WriteFile(fetchHead, []byte(sentinel), 0644); err != nil {
		t.Fatalf("write sentinel FETCH_HEAD: %v", err)
	}

	if err := g.VerifyPushedCommitReachableFromPushTarget("origin", branch, first); err != nil {
		t.Fatalf("VerifyPushedCommitReachableFromPushTarget: %v", err)
	}
	got, err := os.ReadFile(fetchHead)
	if err != nil {
		t.Fatalf("read FETCH_HEAD: %v", err)
	}
	if string(got) != sentinel {
		t.Fatalf("FETCH_HEAD was rewritten by push verification:\n got %q\nwant %q", string(got), sentinel)
	}

	// A commit that is on the trunk but never on the branch must still fail, which
	// is the false pass the clobber could produce.
	if err := g.VerifyPushedCommitReachableFromPushTarget("origin", branch, strings.TrimSpace(mainSHA)); err == nil {
		t.Fatal("verification passed for a commit that is not on the branch")
	}
}
