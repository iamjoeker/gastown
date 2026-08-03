package git

import (
	"testing"
	"time"
)

func TestCommitMeta(t *testing.T) {
	// Agent sandboxes export GIT_AUTHOR_NAME, which overrides the repo config
	// initTestRepo sets. Pin the whole ident so the test is deterministic.
	t.Setenv("GIT_AUTHOR_NAME", "Test User")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@test.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test User")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@test.com")

	dir := initTestRepo(t)
	g := NewGit(dir)

	before := time.Now().Add(-time.Minute)

	info, err := g.CommitMeta("HEAD")
	if err != nil {
		t.Fatalf("CommitMeta: %v", err)
	}

	if len(info.SHA) != 40 {
		t.Errorf("SHA = %q, want a 40-char hash", info.SHA)
	}
	if info.AuthorName != "Test User" {
		t.Errorf("AuthorName = %q, want %q", info.AuthorName, "Test User")
	}
	if info.AuthorEmail != "test@test.com" {
		t.Errorf("AuthorEmail = %q, want %q", info.AuthorEmail, "test@test.com")
	}
	if info.CommitterName != "Test User" {
		t.Errorf("CommitterName = %q, want %q", info.CommitterName, "Test User")
	}
	if info.CommittedAt.Before(before) {
		t.Errorf("CommittedAt = %v, want a time at or after %v", info.CommittedAt, before)
	}
}

func TestCommitMeta_UnknownRef(t *testing.T) {
	// Agent sandboxes export GIT_AUTHOR_NAME, which overrides the repo config
	// initTestRepo sets. Pin the whole ident so the test is deterministic.
	t.Setenv("GIT_AUTHOR_NAME", "Test User")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@test.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test User")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@test.com")

	dir := initTestRepo(t)
	g := NewGit(dir)

	if _, err := g.CommitMeta("refs/heads/does-not-exist"); err == nil {
		t.Error("expected an error for an unknown ref, got nil")
	}
}
