package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/checkpoint"
)

// fakeAheadCounter records the recount squashWIPCheckpoints does after a
// history rewrite, so a test can tell "recounted" from "left stale".
type fakeAheadCounter struct {
	n     int
	err   error
	calls int
}

func (f *fakeAheadCounter) CommitsAhead(base, ref string) (int, error) {
	f.calls++
	return f.n, f.err
}

func squashTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	squashGit(t, dir, "init", "-b", "main")
	squashGit(t, dir, "config", "user.email", "test@test.com")
	squashGit(t, dir, "config", "user.name", "Test")
	writeAndCommit(t, dir, "README.md", "# Test\n", "initial commit")
	squashGit(t, dir, "checkout", "-b", "feature")
	return dir
}

// squashGit is a returning variant of this package's runGit helper.
func squashGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeAndCommit(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	squashGit(t, dir, "add", name)
	squashGit(t, dir, "commit", "-m", msg)
}

func branchSubjects(t *testing.T, dir string) []string {
	t.Helper()
	out := squashGit(t, dir, "log", "--format=%s", "main..HEAD")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func TestSquashWIPCheckpoints_FoldsIntoAuthoredCommits(t *testing.T) {
	dir := squashTestRepo(t)
	writeAndCommit(t, dir, "a.go", "package a", "fix: real work (gt-eob)")
	writeAndCommit(t, dir, "b.go", "package b", checkpoint.WIPCommitPrefix)

	ahead := 2
	counter := &fakeAheadCounter{n: 1}
	squashWIPCheckpoints(dir, "main", "gt-eob", false, &ahead, counter)

	want := []string{"fix: real work (gt-eob)"}
	if got := branchSubjects(t, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("expected %v, got %v", want, got)
	}
	if ahead != 1 {
		t.Errorf("expected aheadCount recounted to 1, got %d", ahead)
	}
	if counter.calls != 1 {
		t.Errorf("expected 1 recount, got %d", counter.calls)
	}
	// The checkpoint's content must survive the fold.
	if _, err := os.Stat(filepath.Join(dir, "b.go")); err != nil {
		t.Errorf("expected b.go to survive the squash: %v", err)
	}
}

// A branch of nothing but checkpoints still gets submitted, but under a subject
// that names the issue — the point of gt-eob is that no auto-commit reaches the
// target unattributable.
func TestSquashWIPCheckpoints_AllWIPGetsIssueNamedMessage(t *testing.T) {
	dir := squashTestRepo(t)
	writeAndCommit(t, dir, "a.go", "package a", checkpoint.WIPCommitPrefix)
	writeAndCommit(t, dir, "b.go", "package b", checkpoint.WIPCommitPrefix)

	ahead := 2
	squashWIPCheckpoints(dir, "main", "gt-eob", false, &ahead, &fakeAheadCounter{n: 1})

	want := []string{"chore: squash checkpoint auto-commits (gt-eob)"}
	if got := branchSubjects(t, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("expected %v, got %v", want, got)
	}
	if ahead != 1 {
		t.Errorf("expected aheadCount recounted to 1, got %d", ahead)
	}
	for _, f := range []string{"a.go", "b.go"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s to survive the squash: %v", f, err)
		}
	}
}

func TestSquashWIPCheckpoints_NoWIPLeavesHistoryAndCountAlone(t *testing.T) {
	dir := squashTestRepo(t)
	writeAndCommit(t, dir, "a.go", "package a", "fix: one (gt-eob)")
	writeAndCommit(t, dir, "b.go", "package b", "fix: two (gt-eob)")
	before := squashGit(t, dir, "rev-parse", "HEAD")

	ahead := 2
	counter := &fakeAheadCounter{n: 99}
	squashWIPCheckpoints(dir, "main", "gt-eob", false, &ahead, counter)

	if after := squashGit(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("history was rewritten: %s -> %s", before, after)
	}
	if ahead != 2 {
		t.Errorf("expected aheadCount left at 2, got %d", ahead)
	}
	if counter.calls != 0 {
		t.Errorf("expected no recount when nothing moved, got %d", counter.calls)
	}
}

// A squash failure must not stop gt done: blocking here would strand pushed-up
// work and zombie the polecat.
func TestSquashWIPCheckpoints_FailureIsNotFatal(t *testing.T) {
	dir := squashTestRepo(t)
	writeAndCommit(t, dir, "a.go", "package a", "fix: real work (gt-eob)")

	ahead := 1
	counter := &fakeAheadCounter{n: 7}
	squashWIPCheckpoints(dir, "no-such-ref", "gt-eob", false, &ahead, counter)

	if ahead != 1 {
		t.Errorf("expected aheadCount left at 1, got %d", ahead)
	}
	if counter.calls != 0 {
		t.Errorf("expected no recount after a failed squash, got %d", counter.calls)
	}
}

// Rewriting an already-pushed branch would need a force-push, and on the resume
// path gt done skips the push entirely — the rewrite would just be discarded.
func TestSquashWIPCheckpoints_AlreadyPushedLeavesHistoryAlone(t *testing.T) {
	dir := squashTestRepo(t)
	writeAndCommit(t, dir, "a.go", "package a", "fix: real work (gt-eob)")
	writeAndCommit(t, dir, "b.go", "package b", checkpoint.WIPCommitPrefix)
	before := squashGit(t, dir, "rev-parse", "HEAD")

	ahead := 2
	counter := &fakeAheadCounter{n: 1}
	squashWIPCheckpoints(dir, "main", "gt-eob", true, &ahead, counter)

	if after := squashGit(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("history was rewritten on an already-pushed branch: %s -> %s", before, after)
	}
	if ahead != 2 {
		t.Errorf("expected aheadCount left at 2, got %d", ahead)
	}
	if counter.calls != 0 {
		t.Errorf("expected no recount, got %d", counter.calls)
	}
}

func TestWipSquashMessage(t *testing.T) {
	if got, want := wipSquashMessage("gt-eob"), "chore: squash checkpoint auto-commits (gt-eob)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := wipSquashMessage(""), "chore: squash checkpoint auto-commits"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
