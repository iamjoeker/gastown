package cmd

import (
	"fmt"
	"path/filepath"
	"testing"
)

// gt-eygw: `gt done --pre-verified` recorded the target's current tip as the
// base its gates ran against. On the path that matters it had already measured
// the opposite a few hundred lines earlier — the contamination preflight finds
// the branch behind the target and skips the auto-rebase precisely because
// --pre-verified is set — so the field asserted a rebase the same command had
// just declined to perform.

// TestResolvePreVerifiedBaseRecordsTheGatedBaseNotTheTip is the stale case: the
// branch is behind the target, which is where a --pre-verified submission is
// left on a busy queue. The recorded base must be the commit the gates saw.
func TestResolvePreVerifiedBaseRecordsTheGatedBaseNotTheTip(t *testing.T) {
	g, _, branch, defaultBranch := preVerifiedRepo(t)
	repo := g.WorkDir()

	gatedOn, err := g.Rev("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	writeRecoveryFile(t, filepath.Join(repo, "work.txt"), "polecat work")
	runGit(t, repo, "add", "work.txt")
	runGit(t, repo, "commit", "-m", "polecat work")
	submitted, err := g.Rev("HEAD")
	if err != nil {
		t.Fatal(err)
	}

	movePushTarget(t, repo, branch, defaultBranch, "the other MR")

	tip, err := g.Rev("origin/" + defaultBranch)
	if err != nil {
		t.Fatal(err)
	}
	if tip == gatedOn {
		t.Fatalf("fixture did not move %s: still %s", defaultBranch, tip)
	}

	pvb, err := resolvePreVerifiedBase(g, "origin/"+defaultBranch, submitted)
	if err != nil {
		t.Fatalf("resolvePreVerifiedBase: %v", err)
	}
	if pvb.Base != gatedOn {
		t.Errorf("recorded base = %s, want the commit the gates ran against (%s)", pvb.Base, gatedOn)
	}
	if pvb.Base == tip {
		t.Errorf("recorded base is the target tip %s — this is the gt-eygw defect", tip)
	}
	if pvb.TargetTip != tip {
		t.Errorf("TargetTip = %s, want %s", pvb.TargetTip, tip)
	}
	if pvb.OnTargetTip() {
		t.Error("OnTargetTip() = true for a branch that was never rebased onto the tip")
	}
}

// TestResolvePreVerifiedBaseAcceptsAGenuineRebase is the control. Without it,
// a resolver that always returned some commit other than the tip would satisfy
// the test above while refusing every fast path the flag exists to enable —
// the recorded base has to be the tip here, and only here.
func TestResolvePreVerifiedBaseAcceptsAGenuineRebase(t *testing.T) {
	g, _, branch, defaultBranch := preVerifiedRepo(t)
	repo := g.WorkDir()

	writeRecoveryFile(t, filepath.Join(repo, "work.txt"), "polecat work")
	runGit(t, repo, "add", "work.txt")
	runGit(t, repo, "commit", "-m", "polecat work")

	movePushTarget(t, repo, branch, defaultBranch, "the other MR")

	// The polecat does what --pre-verified attests to: rebase, then gate.
	runGit(t, repo, "rebase", "origin/"+defaultBranch)
	submitted, err := g.Rev("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tip, err := g.Rev("origin/" + defaultBranch)
	if err != nil {
		t.Fatal(err)
	}

	pvb, err := resolvePreVerifiedBase(g, "origin/"+defaultBranch, submitted)
	if err != nil {
		t.Fatalf("resolvePreVerifiedBase: %v", err)
	}
	if pvb.Base != tip {
		t.Errorf("recorded base = %s, want the target tip %s for a rebased branch", pvb.Base, tip)
	}
	if !pvb.OnTargetTip() {
		t.Error("OnTargetTip() = false after a genuine rebase; the fast path would never fire")
	}
}

// TestResolvePreVerifiedBaseDefaultsToHead covers the empty-submitted-sha case:
// gt done resolves commit_sha from HEAD moments earlier and tolerates failure,
// so the recorder must still measure something rather than silently record the
// zero value.
func TestResolvePreVerifiedBaseDefaultsToHead(t *testing.T) {
	g, _, _, defaultBranch := preVerifiedRepo(t)
	repo := g.WorkDir()

	gatedOn, err := g.Rev("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	writeRecoveryFile(t, filepath.Join(repo, "work.txt"), "polecat work")
	runGit(t, repo, "add", "work.txt")
	runGit(t, repo, "commit", "-m", "polecat work")

	pvb, err := resolvePreVerifiedBase(g, "origin/"+defaultBranch, "")
	if err != nil {
		t.Fatalf("resolvePreVerifiedBase: %v", err)
	}
	if pvb.Base != gatedOn {
		t.Errorf("base = %s, want %s", pvb.Base, gatedOn)
	}
}

// TestResolvePreVerifiedBaseErrorsRatherThanGuesses: an unresolvable base ref
// used to warn and simply omit the field. That is still the outcome, but the
// resolver must report it as an error rather than hand back a usable-looking
// zero — an empty base compares unequal to every target head, which happens to
// be the safe direction and would keep a real breakage quiet.
func TestResolvePreVerifiedBaseErrorsRatherThanGuesses(t *testing.T) {
	g, _, _, _ := preVerifiedRepo(t)

	if _, err := resolvePreVerifiedBase(g, "origin/nonexistent-branch", "HEAD"); err == nil {
		t.Error("resolvePreVerifiedBase on an unresolvable base ref returned no error")
	}
	if _, err := resolvePreVerifiedBase(g, "  ", "HEAD"); err == nil {
		t.Error("resolvePreVerifiedBase with a blank base ref returned no error")
	}
}

// preVerifiedRepo returns a repo on a polecat branch cut from the pushed
// default branch, plus its origin.
func preVerifiedRepo(t *testing.T) (g preVerifiedTestGit, townRoot, branch, defaultBranch string) {
	t.Helper()
	realGit, townRoot, _, branch, defaultBranch := classifyPushRepo(t)
	return realGit, townRoot, branch, defaultBranch
}

// preVerifiedTestGit is what the fixture hands back: the resolver's dependency
// plus the accessors the tests use to set up and read the repo.
type preVerifiedTestGit interface {
	preVerifiedBaseGit
	WorkDir() string
}

// movePushTarget lands an unrelated commit on the target and returns to branch,
// leaving the branch behind its target exactly as a queue that merged something
// else first would.
func movePushTarget(t *testing.T, repo, branch, defaultBranch, message string) {
	t.Helper()
	runGit(t, repo, "checkout", defaultBranch)
	writeRecoveryFile(t, filepath.Join(repo, "other.txt"), message)
	runGit(t, repo, "add", "other.txt")
	runGit(t, repo, "commit", "-m", message)
	runGit(t, repo, "push", "origin", fmt.Sprintf("%s:%s", defaultBranch, defaultBranch))
	runGit(t, repo, "checkout", branch)
}
