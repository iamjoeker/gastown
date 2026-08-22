package witness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The unit tests above drive landedFromPushedBranches through a fake remote, so
// they prove the DECISION but not the PLUMBING: that *git.Git satisfies the
// interface behaviourally, that ls-remote reaches the push url from a bare rig
// clone, and that a SQUASH merge — how the refinery lands nearly everything — is
// actually recognised end to end. A fake that answers "preserved" cannot fail
// the way real git fails, so this builds a real remote and a real rig clone.

// landedFixture is a real remote, a real rig clone laid out the way a Gas Town
// rig is, and a work clone to push from.
type landedFixture struct {
	townRoot string
	remote   string
	work     string
}

func newLandedFixture(t *testing.T) *landedFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}

	root := t.TempDir()
	f := &landedFixture{
		townRoot: filepath.Join(root, "town"),
		remote:   filepath.Join(root, "remote.git"),
		work:     filepath.Join(root, "work"),
	}

	mustMkdir(t, filepath.Join(f.townRoot, "mayor"))
	mustWrite(t, filepath.Join(f.townRoot, "mayor", "town.json"), "{}")

	f.gitAt(t, root, "init", "--bare", "--initial-branch=main", f.remote)

	// Seed main through a work clone.
	f.gitAt(t, root, "clone", f.remote, f.work)
	f.configureIdentity(t, f.work)
	mustWrite(t, filepath.Join(f.work, "README.md"), "seed\n")
	f.git(t, "add", "README.md")
	f.git(t, "commit", "-m", "seed")
	f.git(t, "push", "origin", "main")

	// The rig's durable clone, configured exactly as `gt rig` leaves it: a bare
	// repo whose origin fetches into refs/remotes/origin/*.
	bare := filepath.Join(f.townRoot, "testrig", ".repo.git")
	mustMkdir(t, filepath.Dir(bare))
	f.gitAt(t, root, "init", "--bare", "--initial-branch=main", bare)
	f.gitAt(t, root, "--git-dir="+bare, "remote", "add", "origin", f.remote)
	f.gitAt(t, root, "--git-dir="+bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	f.gitAt(t, root, "--git-dir="+bare, "fetch", "origin")

	return f
}

func (f *landedFixture) configureIdentity(t *testing.T, dir string) {
	t.Helper()
	f.gitAt(t, dir, "config", "user.email", "witness@example.test")
	f.gitAt(t, dir, "config", "user.name", "Witness Test")
}

func (f *landedFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	return f.gitAt(t, f.work, args...)
}

func (f *landedFixture) gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Witness Test", "GIT_AUTHOR_EMAIL=witness@example.test",
		"GIT_COMMITTER_NAME=Witness Test", "GIT_COMMITTER_EMAIL=witness@example.test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// pushPolecatBranch creates a polecat branch carrying one file and pushes it.
func (f *landedFixture) pushPolecatBranch(t *testing.T, branch, file, content string) {
	t.Helper()
	f.git(t, "checkout", "-b", branch, "main")
	mustWrite(t, filepath.Join(f.work, file), content)
	f.git(t, "add", file)
	f.git(t, "commit", "-m", "work on "+branch)
	f.git(t, "push", "origin", branch)
	f.git(t, "checkout", "main")
}

// squashMergeToMain lands a branch the way the refinery does: the content
// arrives on main under a NEW sha, so the branch tip is not an ancestor of main
// and an ancestry-only check calls it unlanded.
func (f *landedFixture) squashMergeToMain(t *testing.T, branch string) {
	t.Helper()
	f.git(t, "checkout", "main")
	f.git(t, "merge", "--squash", branch)
	f.git(t, "commit", "-m", "squash "+branch)
	f.git(t, "push", "origin", "main")
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The end-to-end shape of gt-e7dd: the polecat's worktree never exists, its work
// was squash-merged, and the durable check must still say landed.
func TestVerifyWorkLandedFromDurableStateEndToEnd(t *testing.T) {
	t.Parallel()

	f := newLandedFixture(t)
	f.pushPolecatBranch(t, "polecat/dust/gt-int1+aaa", "landed.txt", "landed\n")
	f.squashMergeToMain(t, "polecat/dust/gt-int1+aaa")

	// The polecat's sandbox is gone — it was never created. That is exactly the
	// state both orphan callers guarantee before they get here.
	if _, err := os.Stat(filepath.Join(f.townRoot, "testrig", "polecats", "dust")); !os.IsNotExist(err) {
		t.Fatalf("fixture is wrong: the polecat worktree should not exist (%v)", err)
	}

	got, err := _verifyWorkLandedFromDurableState(f.townRoot, "testrig", "dust", "gt-int1")
	if err != nil {
		t.Fatalf("_verifyWorkLandedFromDurableState: %v", err)
	}
	if !got.Landed {
		t.Fatalf("Landed = false for squash-merged work: %s", got.Reason)
	}
	if got.Branch != "polecat/dust/gt-int1+aaa" {
		t.Errorf("Branch = %q", got.Branch)
	}
	if got.Evidence == "ancestor" {
		t.Errorf("Evidence = ancestor for a squash merge — the fixture is not testing the squash path")
	}
	if !strings.Contains(got.ContainedIn, "main") {
		t.Errorf("ContainedIn = %q, want the trunk", got.ContainedIn)
	}
}

// The control: a branch that was pushed and never merged must come back as a
// measured "not landed", so the bead is still recovered. Without this, the test
// above passes equally well against a check that says landed unconditionally.
func TestVerifyWorkLandedFromDurableStateEndToEndUnmerged(t *testing.T) {
	t.Parallel()

	f := newLandedFixture(t)
	f.pushPolecatBranch(t, "polecat/dust/gt-int2+bbb", "pending.txt", "pending\n")

	got, err := _verifyWorkLandedFromDurableState(f.townRoot, "testrig", "dust", "gt-int2")
	if err != nil {
		t.Fatalf("_verifyWorkLandedFromDurableState: %v", err)
	}
	if got.Landed {
		t.Fatalf("Landed = true for a branch that was never merged: %+v", got)
	}
	if !strings.Contains(got.Reason, "polecat/dust/gt-int2+bbb") {
		t.Errorf("Reason = %q, want it to name the branch it compared", got.Reason)
	}
}

// A polecat that died before pushing leaves nothing on the remote. That must
// read as "nothing to land", not as an error and not as landed.
func TestVerifyWorkLandedFromDurableStateEndToEndNothingPushed(t *testing.T) {
	t.Parallel()

	f := newLandedFixture(t)

	got, err := _verifyWorkLandedFromDurableState(f.townRoot, "testrig", "dust", "gt-int3")
	if err != nil {
		t.Fatalf("_verifyWorkLandedFromDurableState: %v", err)
	}
	if got.Landed {
		t.Fatalf("Landed = true with no branch on the remote: %+v", got)
	}
	if !strings.Contains(got.Reason, "gt-int3") {
		t.Errorf("Reason = %q, want it to name the bead", got.Reason)
	}
}

// A rig with no durable clone cannot answer, and must say so rather than
// returning a clean "not landed" that would look measured.
func TestVerifyWorkLandedFromDurableStateWithNoRigClone(t *testing.T) {
	t.Parallel()

	townRoot := townWithRig(t, "testrig")
	if _, err := _verifyWorkLandedFromDurableState(townRoot, "testrig", "dust", "gt-int4"); err == nil {
		t.Fatal("want an error when the rig has no durable repository")
	}
}
