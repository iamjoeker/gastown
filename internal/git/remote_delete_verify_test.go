package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// swallowDeletePushes puts a git shim on PATH that reports a branch delete as
// "[deleted]" and exits 0 without performing it, for the first swallowCount
// delete pushes. Everything else — including the ls-remote the verification
// runs — goes to the real git untouched.
//
// This is the behaviour that was observed live: a delete printed its success
// line and exited 0 while ls-remote still returned the old sha immediately
// afterwards, with the branch's polecat already gone and nothing to re-push it.
// A shim is the only way to hold that behaviour still. (gt-wkcz)
func swallowDeletePushes(t *testing.T, swallowCount int) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate git: %v", err)
	}
	dir := t.TempDir()
	countFile := filepath.Join(dir, "swallowed")
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    :refs/heads/*)
      swallowed=$(cat %[1]q 2>/dev/null || echo 0)
      if [ "$swallowed" -lt %[2]d ]; then
        echo $((swallowed + 1)) > %[1]q
        echo " - [deleted]         ${arg#:refs/heads/}"
        exit 0
      fi
      ;;
  esac
done
exec %[3]q "$@"
`, countFile, swallowCount, realGit)
	shim := filepath.Join(dir, "git")
	if err := os.WriteFile(shim, []byte(script), 0755); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return countFile
}

func pushedBranchFixture(t *testing.T) (string, string, string) {
	t.Helper()
	localDir, _, _ := initTestRepoWithRemote(t)
	branch := "polecat/deleteme"
	runGit(t, localDir, "checkout", "-b", branch)
	writeRepoFile(t, localDir, "done.go", "package done\n")
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "done: merged work")
	runGit(t, localDir, "push", "origin", branch)
	return localDir, branch, revParse(t, localDir, "HEAD")
}

// The "[deleted]" line is not proof of deletion, so the delete verifies and
// re-issues rather than trusting the push report.
func TestDeleteRemoteBranchIfAtRetriesAPushThatOnlyReportedSuccess(t *testing.T) {
	localDir, branch, head := pushedBranchFixture(t)
	countFile := swallowDeletePushes(t, 1)
	g := NewGit(localDir)

	if err := g.DeleteRemoteBranchIfAt("origin", branch, head); err != nil {
		t.Fatalf("DeleteRemoteBranchIfAt: %v", err)
	}
	if swallowed, err := os.ReadFile(countFile); err != nil || strings.TrimSpace(string(swallowed)) != "1" {
		t.Fatalf("shim swallow count = %q (err %v), want 1: the lying push never happened", swallowed, err)
	}
	tip, err := g.RemoteBranchTip("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}
	if strings.TrimSpace(tip) != "" {
		t.Fatalf("branch still at %s after DeleteRemoteBranchIfAt reported success", tip)
	}
}

// A delete that never takes must fail loudly. Reporting success here is what
// leaves a live ref behind that no later sweep can collect.
func TestDeleteRemoteBranchIfAtFailsWhenTheBranchSurvives(t *testing.T) {
	localDir, branch, head := pushedBranchFixture(t)
	swallowDeletePushes(t, 2)
	g := NewGit(localDir)

	err := g.DeleteRemoteBranchIfAt("origin", branch, head)
	if err == nil {
		t.Fatal("DeleteRemoteBranchIfAt returned nil for a branch that is still on the remote")
	}
	if !strings.Contains(err.Error(), "still at") {
		t.Fatalf("error %q does not say the branch survived", err)
	}
	tip, tipErr := g.RemoteBranchTip("origin", branch)
	if tipErr != nil {
		t.Fatalf("RemoteBranchTip: %v", tipErr)
	}
	if strings.TrimSpace(tip) != head {
		t.Fatalf("fixture branch tip = %q, want the undeleted %s", tip, head)
	}
}

// The ordinary case still works, and still costs exactly one push.
func TestDeleteRemoteBranchIfAtDeletesOnTheFirstPush(t *testing.T) {
	localDir, branch, head := pushedBranchFixture(t)
	g := NewGit(localDir)

	if err := g.DeleteRemoteBranchIfAt("origin", branch, head); err != nil {
		t.Fatalf("DeleteRemoteBranchIfAt: %v", err)
	}
	tip, err := g.RemoteBranchTip("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}
	if strings.TrimSpace(tip) != "" {
		t.Fatalf("branch still at %s after a successful delete", tip)
	}
}
