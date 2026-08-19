package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitWithMessage adds one commit carrying message to the repo at dir.
func commitWithMessage(t *testing.T, dir, file, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", message)
}

func TestCommitsWithSubjectToken(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Test User")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@test.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test User")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@test.com")

	dir := initTestRepo(t)
	commitWithMessage(t, dir, "a.txt", "fix(polecat): reuse done polecats (gt-602)")
	// The discriminator: gt-602 is a prefix of gt-6021, so a substring search
	// would report this longer bead's commit as gt-602's work.
	commitWithMessage(t, dir, "b.txt", "feat: unrelated longer id (gt-6021)")
	commitWithMessage(t, dir, "c.txt", "docs: no bead here at all")
	// A body cross-reference is not attribution: this is another bead's commit
	// mentioning gt-602 as related work.
	commitWithMessage(t, dir, "d.txt", "fix(dolt): unrelated fix (gt-xvwu)\n\nFollow-up filed as gt-602.")
	commitWithMessage(t, dir, "e.txt", "Merge polecat/settler/gt-602+mszje5tl into main")

	g := NewGit(dir)

	got, err := g.CommitsWithSubjectToken("HEAD", "gt-602", 0)
	if err != nil {
		t.Fatalf("CommitsWithSubjectToken: %v", err)
	}
	if len(got) != 2 {
		var subjects []string
		for _, c := range got {
			subjects = append(subjects, c.Subject)
		}
		t.Fatalf("CommitsWithSubjectToken(gt-602) returned %d commits (%v), want 2", len(got), subjects)
	}
	// Newest first.
	if !strings.HasPrefix(got[0].Subject, "Merge polecat/settler/gt-602") {
		t.Errorf("first subject = %q, want the merge commit", got[0].Subject)
	}
	for _, c := range got {
		if strings.Contains(c.Subject, "gt-6021") {
			t.Errorf("gt-6021 commit leaked into gt-602 results: %q", c.Subject)
		}
		if strings.Contains(c.Subject, "gt-xvwu") {
			t.Errorf("body-only cross-reference counted as attribution: %q", c.Subject)
		}
		if len(c.SHA) != 40 {
			t.Errorf("SHA = %q, want a 40-char hash", c.SHA)
		}
		if c.Date == "" {
			t.Errorf("Date is empty for %s", c.SHA)
		}
	}

	// The longer ID must still find its own commit — the boundary rule must
	// not be so strict that it matches nothing.
	longer, err := g.CommitsWithSubjectToken("HEAD", "gt-6021", 0)
	if err != nil {
		t.Fatalf("CommitsWithSubjectToken(gt-6021): %v", err)
	}
	if len(longer) != 1 {
		t.Fatalf("CommitsWithSubjectToken(gt-6021) returned %d commits, want 1", len(longer))
	}
}

// The limit must be applied after the subject filter. Applying it to the git
// prefilter would let body mentions crowd out the bead's own commit.
func TestCommitsWithSubjectTokenLimitCountsSubjectMatches(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Test User")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@test.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test User")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@test.com")

	dir := initTestRepo(t)
	commitWithMessage(t, dir, "a.txt", "one (gt-602)")
	commitWithMessage(t, dir, "b.txt", "two (gt-602)")
	commitWithMessage(t, dir, "c.txt", "three (gt-602)")
	for i, name := range []string{"d.txt", "e.txt", "f.txt"} {
		commitWithMessage(t, dir, name, "noise "+string(rune('a'+i))+" (gt-other)\n\nsee gt-602")
	}

	g := NewGit(dir)
	got, err := g.CommitsWithSubjectToken("HEAD", "gt-602", 2)
	if err != nil {
		t.Fatalf("CommitsWithSubjectToken: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("CommitsWithSubjectToken with limit 2 returned %d commits, want 2", len(got))
	}
	for _, c := range got {
		if !strings.Contains(c.Subject, "gt-602") {
			t.Errorf("limited result %q is not a subject match", c.Subject)
		}
	}
}

func TestCommitsWithSubjectTokenNoMatchIsEmptyNotError(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	got, err := g.CommitsWithSubjectToken("HEAD", "gt-nothing", 0)
	if err != nil {
		t.Fatalf("CommitsWithSubjectToken: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("CommitsWithSubjectToken returned %d commits, want 0", len(got))
	}
}

func TestCommitsWithSubjectTokenRejectsUnsupportedTokens(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	for _, token := range []string{"", "   ", "gt-.*", "gt-602 OR gt-603", "--output=/tmp/x", "gt-602|gt-603", "-602"} {
		if _, err := g.CommitsWithSubjectToken("HEAD", token, 0); err == nil {
			t.Errorf("CommitsWithSubjectToken(%q) = nil error, want a refusal", token)
		}
	}
}

func TestCommitsWithSubjectTokenUnknownRef(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	if _, err := g.CommitsWithSubjectToken("refs/heads/does-not-exist", "gt-602", 0); err == nil {
		t.Error("expected an error for an unknown ref, got nil")
	}
	if _, err := g.CommitsWithSubjectToken("", "gt-602", 0); err == nil {
		t.Error("expected an error for an empty ref, got nil")
	}
}
