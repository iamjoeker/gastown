package git

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// mergeConflictRepo builds a repo whose merge base is behind both tips — the
// shape that makes existence and mergeability disagree. Returns the worktree.
func mergeConflictRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", ".")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")

	writeMergeFixtureFile(t, dir, "a.txt", "base\n")
	writeMergeFixtureFile(t, dir, "b.txt", "base\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")
	runGit(t, dir, "branch", "-M", "main")

	runGit(t, dir, "checkout", "-b", "feature")
	writeMergeFixtureFile(t, dir, "a.txt", "feature\n")
	writeMergeFixtureFile(t, dir, "b.txt", "feature\n")
	runGit(t, dir, "commit", "-am", "feature")

	runGit(t, dir, "checkout", "-b", "orthogonal", "main")
	writeMergeFixtureFile(t, dir, "c.txt", "orthogonal\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "orthogonal")

	runGit(t, dir, "checkout", "main")
	writeMergeFixtureFile(t, dir, "a.txt", "main\n")
	writeMergeFixtureFile(t, dir, "b.txt", "main\n")
	runGit(t, dir, "commit", "-am", "main moved on")

	return dir
}

func writeMergeFixtureFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestMergeConflicts is the control for the whole check: a branch that exists,
// is pushed and carries commits over its target, and still cannot merge. Every
// cheaper probe calls it fine (gt-0w2l).
func TestMergeConflicts(t *testing.T) {
	dir := mergeConflictRepo(t)
	g := NewGit(dir)

	conflicts, err := g.MergeConflicts("main", "feature")
	if err != nil {
		t.Fatalf("MergeConflicts(main, feature): %v", err)
	}
	want := []string{"a.txt", "b.txt"}
	if !reflect.DeepEqual(conflicts, want) {
		t.Errorf("conflicting merge: got %v, want %v", conflicts, want)
	}

	// A clean merge must return no conflicts AND no error. Reporting an error
	// here would push callers to treat "could not tell" and "clean" alike,
	// which is the failure this check exists to prevent.
	conflicts, err = g.MergeConflicts("main", "orthogonal")
	if err != nil {
		t.Fatalf("MergeConflicts(main, orthogonal): %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("clean merge: got %v, want none", conflicts)
	}

	// A branch already contained in the target merges cleanly too.
	conflicts, err = g.MergeConflicts("feature", "main")
	if err != nil {
		t.Fatalf("MergeConflicts(feature, main): %v", err)
	}
	if len(conflicts) != 2 {
		t.Errorf("reversed conflicting merge: got %v, want 2 paths", conflicts)
	}
}

// TestMergeConflictsUnresolvableRefIsAnError separates "conflicts" from "could
// not answer", and it is the reason this check reads stdout instead of trusting
// the exit code.
//
// The docs say merge-tree exits 1 for a conflicted merge and >1 for a failure.
// Measured on git 2.55, an unresolvable ref exits 1 as well, with the message on
// stderr and stdout EMPTY. A caller that reads the exit code alone parses that
// empty stdout into zero conflicted files and calls the merge CLEAN — a
// non-answer rendered as a verdict, which is precisely the defect this whole
// change is fixing (gt-0w2l).
func TestMergeConflictsUnresolvableRefIsAnError(t *testing.T) {
	dir := mergeConflictRepo(t)
	g := NewGit(dir)

	conflicts, err := g.MergeConflicts("main", "no/such/branch")
	if err == nil {
		t.Fatalf("MergeConflicts on an unresolvable ref returned nil error (conflicts=%v) — an unanswered merge must never read as clean", conflicts)
	}
	if conflicts != nil {
		t.Errorf("MergeConflicts on an unresolvable ref returned %v, want nil", conflicts)
	}
}

func TestParseMergeTreeConflictNames(t *testing.T) {
	tests := []struct {
		name   string
		out    string
		want   []string
		wantOK bool
	}{
		{
			name:   "tree oid only",
			out:    "c53e5b6942abef42a99d51a175b4aabf3ce093de\n",
			wantOK: true,
		},
		{
			// What an unresolvable ref actually leaves on stdout. It must not
			// parse as "a merge with no conflicts".
			name: "empty output is not a merge result",
			out:  "",
		},
		{
			name: "an error message on stdout is not a merge result",
			out:  "merge-tree: no/such/branch - not something we can merge\n",
		},
		{
			// The informational messages after the blank line are prose, and
			// counting them as filenames would inflate every conflict count.
			name: "stops at the blank line before the messages",
			out: "c53e5b6942abef42a99d51a175b4aabf3ce093de\nf.txt\ng.txt\n\n" +
				"Auto-merging f.txt\nCONFLICT (content): Merge conflict in f.txt\n",
			want:   []string{"f.txt", "g.txt"},
			wantOK: true,
		},
		{
			name:   "crlf line endings",
			out:    "c53e5b6942abef42a99d51a175b4aabf3ce093de\r\nf.txt\r\n\r\nCONFLICT (content): Merge conflict in f.txt\r\n",
			want:   []string{"f.txt"},
			wantOK: true,
		},
		{
			name:   "paths with spaces survive intact",
			out:    "c53e5b6942abef42a99d51a175b4aabf3ce093de\ndocs/a file.md\n\nCONFLICT (content): x\n",
			want:   []string{"docs/a file.md"},
			wantOK: true,
		},
		{
			name:   "sha256 object id",
			out:    "c53e5b6942abef42a99d51a175b4aabf3ce093dec53e5b6942abef42a99d51a1\nf.txt\n",
			want:   []string{"f.txt"},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseMergeTreeConflictNames(tt.out)
			if ok != tt.wantOK {
				t.Fatalf("parseMergeTreeConflictNames() ok = %v, want %v", ok, tt.wantOK)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseMergeTreeConflictNames() = %v, want %v", got, tt.want)
			}
		})
	}
}
