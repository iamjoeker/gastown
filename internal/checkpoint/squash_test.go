package checkpoint

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// initTestRepo creates a fresh git repo with an initial commit and returns its path.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args[1:], err, out)
		}
	}

	// Create initial commit on main
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", "initial commit"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args[1:], err, out)
		}
	}

	return dir
}

// createBranch creates a branch from current HEAD and switches to it.
func createBranch(t *testing.T, dir, branch string) {
	t.Helper()
	cmd := exec.Command("git", "checkout", "-b", branch)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout -b %s failed: %v\n%s", branch, err, out)
	}
}

// addCommit adds a file and commits with the given message.
func addCommit(t *testing.T, dir, filename, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", filename},
		{"git", "commit", "-m", msg},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args[1:], err, out)
		}
	}
}

// addCommitAs adds a file and commits it under an explicit author identity.
func addCommitAs(t *testing.T, dir, filename, content, msg, name, email string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", filename},
		{"git", "commit", "-m", msg},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		// Env, not -c user.name: the agent environment already exports
		// GIT_AUTHOR_NAME, and that beats git config.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME="+name,
			"GIT_AUTHOR_EMAIL="+email,
			"GIT_COMMITTER_NAME="+name,
			"GIT_COMMITTER_EMAIL="+email,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args[1:], err, out)
		}
	}
}

// gitLogFormat returns `git log -1 --format=<format> <rev>` for the repo.
func gitLogFormat(t *testing.T, dir, format, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format="+format, rev)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log %s failed: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

// revParse resolves a revision to a SHA.
func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", rev)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s failed: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

func headSHA(t *testing.T, dir string) string { return revParse(t, dir, "HEAD") }

func headTree(t *testing.T, dir string) string { return revParse(t, dir, "HEAD^{tree}") }

// getCommitSubjects returns the commit subjects on the branch since main.
func getCommitSubjects(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--format=%s", "main..HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

func TestCountWIPCommits_NoWIP(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", "add feature A")
	addCommit(t, dir, "b.go", "package b", "add feature B")

	count, err := CountWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0 WIP commits, got %d", count)
	}
}

func TestCountWIPCommits_AllWIP(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", WIPCommitPrefix)
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)

	count, err := CountWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2 WIP commits, got %d", count)
	}
}

func TestCountWIPCommits_Mixed(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", "real work")
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)
	addCommit(t, dir, "c.go", "package c", "more real work")

	count, err := CountWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 WIP commit, got %d", count)
	}
}

func TestSquashWIPCommits_NoWIP(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", "real work")

	wipCount, err := SquashWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if wipCount != 0 {
		t.Errorf("expected 0, got %d", wipCount)
	}

	// Verify commit is untouched
	subjects := getCommitSubjects(t, dir)
	if len(subjects) != 1 || subjects[0] != "real work" {
		t.Errorf("expected [real work], got %v", subjects)
	}
}

// An all-checkpoint branch has no authored commit to fold into, so history is
// reported back untouched for the caller to deal with.
func TestSquashWIPCommits_AllWIP(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", WIPCommitPrefix)
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)
	before := headSHA(t, dir)

	wipCount, err := SquashWIPCommits(dir, "main")
	if !errors.Is(err, ErrOnlyWIPCommits) {
		t.Fatalf("expected ErrOnlyWIPCommits, got %v", err)
	}
	if wipCount != 2 {
		t.Errorf("expected 2, got %d", wipCount)
	}
	if after := headSHA(t, dir); after != before {
		t.Errorf("history was rewritten: %s -> %s", before, after)
	}
}

// The authored commits survive with their own messages; only the checkpoints
// disappear, absorbed by the authored commit that follows them.
func TestSquashWIPCommits_Mixed(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", "implement auth handler")
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)
	addCommit(t, dir, "c.go", "package c", "add auth tests")
	addCommit(t, dir, "d.go", "package d", WIPCommitPrefix)
	treeBefore := headTree(t, dir)

	wipCount, err := SquashWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if wipCount != 2 {
		t.Errorf("expected 2, got %d", wipCount)
	}

	subjects := getCommitSubjects(t, dir)
	want := []string{"add auth tests", "implement auth handler"} // git log order: newest first
	if !reflect.DeepEqual(subjects, want) {
		t.Errorf("expected %v, got %v", want, subjects)
	}

	// Content is untouched — the checkpoints' changes moved into the authored
	// commits, they were not dropped.
	if after := headTree(t, dir); after != treeBefore {
		t.Errorf("HEAD tree changed: %s -> %s", treeBefore, after)
	}
	for _, f := range []string{"a.go", "b.go", "c.go", "d.go"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s to exist after squash", f)
		}
	}
}

// Checkpoints taken before the first authored commit are absorbed by it, so the
// branch does not keep a leading WIP commit.
func TestSquashWIPCommits_LeadingWIP(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", WIPCommitPrefix)
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)
	addCommit(t, dir, "c.go", "package c", "fix: the real work (gt-eob)")

	wipCount, err := SquashWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if wipCount != 2 {
		t.Errorf("expected 2, got %d", wipCount)
	}

	subjects := getCommitSubjects(t, dir)
	want := []string{"fix: the real work (gt-eob)"}
	if !reflect.DeepEqual(subjects, want) {
		t.Errorf("expected %v, got %v", want, subjects)
	}
	for _, f := range []string{"a.go", "b.go", "c.go"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected %s to exist after squash", f)
		}
	}
}

// Nothing at or below the merge-base may be rewritten, and commits that main
// gained since the branch forked must not be disturbed.
func TestSquashWIPCommits_LeavesBaseUntouched(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", "authored work")
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)

	mergeBaseBefore := revParse(t, dir, "main")
	if _, err := SquashWIPCommits(dir, "main"); err != nil {
		t.Fatal(err)
	}

	if after := revParse(t, dir, "main"); after != mergeBaseBefore {
		t.Errorf("base branch moved: %s -> %s", mergeBaseBefore, after)
	}
	if parent := revParse(t, dir, "HEAD~1"); parent != mergeBaseBefore {
		t.Errorf("rewritten history is not parented on the merge-base: %s", parent)
	}
}

// The authored commit's identity and dates survive the rewrite.
func TestSquashWIPCommits_PreservesAuthor(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommitAs(t, dir, "a.go", "package a", "authored work", "Polecat Foundation", "foundation@gastown.test")
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)

	if _, err := SquashWIPCommits(dir, "main"); err != nil {
		t.Fatal(err)
	}

	got := gitLogFormat(t, dir, "%an <%ae>", "HEAD")
	want := "Polecat Foundation <foundation@gastown.test>"
	if got != want {
		t.Errorf("expected author %q, got %q", want, got)
	}
}

func TestSquashWIPCommits_NoCommits(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")

	wipCount, err := SquashWIPCommits(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if wipCount != 0 {
		t.Errorf("expected 0 for no commits, got %d", wipCount)
	}
}

func TestSquashAll(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", WIPCommitPrefix)
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)
	treeBefore := headTree(t, dir)

	count, err := SquashAll(dir, "main", "chore: checkpointed work (gt-eob)")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2 collapsed commits, got %d", count)
	}

	subjects := getCommitSubjects(t, dir)
	want := []string{"chore: checkpointed work (gt-eob)"}
	if !reflect.DeepEqual(subjects, want) {
		t.Errorf("expected %v, got %v", want, subjects)
	}
	if after := headTree(t, dir); after != treeBefore {
		t.Errorf("HEAD tree changed: %s -> %s", treeBefore, after)
	}
}

// A branch whose only commit is a checkpoint gets reworded — leaving it alone
// would let "WIP: checkpoint (auto)" reach the target, which is the whole point.
func TestSquashAll_SingleCommitIsReworded(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", WIPCommitPrefix)
	treeBefore := headTree(t, dir)

	count, err := SquashAll(dir, "main", "chore: checkpointed work (gt-eob)")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1, got %d", count)
	}

	subjects := getCommitSubjects(t, dir)
	want := []string{"chore: checkpointed work (gt-eob)"}
	if !reflect.DeepEqual(subjects, want) {
		t.Errorf("expected %v, got %v", want, subjects)
	}
	if after := headTree(t, dir); after != treeBefore {
		t.Errorf("HEAD tree changed: %s -> %s", treeBefore, after)
	}
}

func TestSquashAll_NoCommits(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	before := headSHA(t, dir)

	count, err := SquashAll(dir, "main", "chore: checkpointed work (gt-eob)")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
	if after := headSHA(t, dir); after != before {
		t.Errorf("history was rewritten: %s -> %s", before, after)
	}
}

func TestSquashAll_EmptyMessageRejected(t *testing.T) {
	dir := initTestRepo(t)
	createBranch(t, dir, "feature")
	addCommit(t, dir, "a.go", "package a", WIPCommitPrefix)
	addCommit(t, dir, "b.go", "package b", WIPCommitPrefix)

	if _, err := SquashAll(dir, "main", "  "); err == nil {
		t.Error("expected an error for an empty squash message")
	}
}
