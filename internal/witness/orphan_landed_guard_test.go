package witness

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gt-e7dd. The #2036 guard — "the work is already on main, close the bead
// instead of re-dispatching it" — was implemented as a lookup in the polecat's
// WORKTREE. Both callers that reach resetAbandonedBead get there only after
// os.Stat says that directory is gone, so on the orphan paths the guard could
// never fire. These tests pin the guard to state that outlives the nuke.

// townWithRig builds the minimum a town needs for workspace.Find to resolve it.
func townWithRig(t *testing.T, rigName string) string {
	t.Helper()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatalf("creating mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("writing town marker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, rigName, "polecats"), 0o755); err != nil {
		t.Fatalf("creating rig dir: %v", err)
	}
	return townRoot
}

// The CONTROL for the whole defect: with the polecat's worktree gone — which is
// the precondition every caller of resetAbandonedBead has already proved — the
// worktree check cannot return true. If this ever starts passing on its own, the
// durable fallback is no longer the only thing standing between an orphaned bead
// and a duplicate polecat, and this test should be the one that says so.
func TestVerifyCommitOnMainCannotAnswerForADeletedWorktree(t *testing.T) {
	t.Parallel()

	townRoot := townWithRig(t, "testrig")
	polecatDir := filepath.Join(townRoot, "testrig", "polecats", "dust")
	if _, err := os.Stat(polecatDir); !os.IsNotExist(err) {
		t.Fatalf("fixture is wrong: %s should not exist (%v)", polecatDir, err)
	}

	onMain, err := _verifyCommitOnMain(townRoot, "testrig", "dust")
	if onMain {
		t.Fatal("_verifyCommitOnMain returned true for a polecat with no worktree")
	}
	if err == nil {
		t.Fatal("_verifyCommitOnMain returned (false, nil) for a missing worktree — the guard's caller cannot distinguish that from a measured 'not merged'")
	}
}

// The fix: the worktree lookup fails, the durable check says the work landed,
// and the bead is closed rather than sent to a second polecat.
func TestResetAbandonedBeadClosesWhenTheWorktreeIsGoneButWorkLanded(t *testing.T) {
	// Not parallel: overrides package-level vars.
	stubWorktreeCheckAsDeleted(t)
	stubDurableCheck(t, LandedEvidence{
		Landed:      true,
		Branch:      "polecat/dust/gt-work123+abc",
		Polecat:     "dust",
		ContainedIn: "origin/main",
		Evidence:    "cherry",
	}, nil)

	bd, mock := hookedBeadMock()

	if resetAbandonedBead(bd, t.TempDir(), "testrig", "gt-work123", "dust", nil) {
		t.Errorf("resetAbandonedBead returned true for work that is already in the trunk: %v", mock.calls)
	}

	var closed, reset bool
	var closeCall string
	for _, call := range mock.calls {
		if strings.Contains(call, "close gt-work123") {
			closed = true
			closeCall = call
		}
		if strings.Contains(call, "update") && strings.Contains(call, "--status=open") {
			reset = true
		}
	}
	if !closed {
		t.Fatalf("expected bd close, got calls: %v", mock.calls)
	}
	if reset {
		t.Errorf("bead was reset for re-dispatch despite landed work: %v", mock.calls)
	}
	// The close reason is the only durable trace of why the guard fired.
	for _, want := range []string{"polecat/dust/gt-work123+abc", "origin/main"} {
		if !strings.Contains(closeCall, want) {
			t.Errorf("close reason %q does not name %q", closeCall, want)
		}
	}
}

// Control: a measured "not landed" must still recover the bead. Without this,
// the test above would pass just as well against a guard that closes everything.
func TestResetAbandonedBeadStillRecoversWhenNothingLanded(t *testing.T) {
	// Not parallel: overrides package-level vars.
	stubWorktreeCheckAsDeleted(t)
	stubDurableCheck(t, LandedEvidence{Reason: "no branch on origin names gt-work123"}, nil)

	bd, mock := hookedBeadMock()

	if !resetAbandonedBead(bd, t.TempDir(), "testrig", "gt-work123", "dust", nil) {
		t.Fatalf("resetAbandonedBead returned false with nothing landed: %v", mock.calls)
	}
	for _, call := range mock.calls {
		if strings.Contains(call, "close gt-work123") {
			t.Errorf("bead was closed on a measured 'not landed': %v", mock.calls)
		}
	}
}

// Control: "could not tell" is not "landed". An unmeasurable check must leave
// the pre-existing recovery in place — a duplicate polecat is loud and bounded,
// a wrongly closed bead silently drops the work.
func TestResetAbandonedBeadRecoversWhenLandingCannotBeMeasured(t *testing.T) {
	// Not parallel: overrides package-level vars.
	stubWorktreeCheckAsDeleted(t)
	stubDurableCheck(t, LandedEvidence{}, errors.New("could not read from remote"))

	bd, mock := hookedBeadMock()

	if !resetAbandonedBead(bd, t.TempDir(), "testrig", "gt-work123", "dust", nil) {
		t.Fatalf("resetAbandonedBead returned false when landing could not be measured: %v", mock.calls)
	}
	for _, call := range mock.calls {
		if strings.Contains(call, "close gt-work123") {
			t.Errorf("bead was closed on an unmeasured check: %v", mock.calls)
		}
	}
}

// The worktree remains the fast path where it exists — HandleMerged still runs
// with the directory in place, and it must not pay for a remote round trip.
func TestVerifyWorkAlreadyLandedPrefersTheWorktree(t *testing.T) {
	// Not parallel: overrides package-level vars.
	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) { return true, nil }
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	durableCalled := false
	oldDurable := verifyWorkLandedFromDurableState
	verifyWorkLandedFromDurableState = func(workDir, rigName, polecatName, hookBead string) (LandedEvidence, error) {
		durableCalled = true
		return LandedEvidence{}, nil
	}
	t.Cleanup(func() { verifyWorkLandedFromDurableState = oldDurable })

	got, err := _verifyWorkAlreadyLanded(t.TempDir(), "testrig", "dust", "gt-work123")
	if err != nil {
		t.Fatalf("_verifyWorkAlreadyLanded: %v", err)
	}
	if !got.Landed {
		t.Fatal("Landed = false when the worktree proved the work is on the default branch")
	}
	if durableCalled {
		t.Error("the durable check ran even though the worktree answered")
	}
}

// A worktree that cannot answer must not stop the durable check from running.
// This is the exact composition the orphan paths depend on.
func TestVerifyWorkAlreadyLandedFallsBackWhenTheWorktreeIsGone(t *testing.T) {
	// Not parallel: overrides package-level vars.
	stubWorktreeCheckAsDeleted(t)

	oldDurable := verifyWorkLandedFromDurableState
	verifyWorkLandedFromDurableState = func(workDir, rigName, polecatName, hookBead string) (LandedEvidence, error) {
		return LandedEvidence{Landed: true, Branch: "polecat/dust/gt-work123+abc", ContainedIn: "origin/main"}, nil
	}
	t.Cleanup(func() { verifyWorkLandedFromDurableState = oldDurable })

	got, err := _verifyWorkAlreadyLanded(t.TempDir(), "testrig", "dust", "gt-work123")
	if err != nil {
		t.Fatalf("_verifyWorkAlreadyLanded: %v", err)
	}
	if !got.Landed || got.Branch != "polecat/dust/gt-work123+abc" {
		t.Fatalf("got %+v, want the durable check's answer", got)
	}
}

// stubWorktreeCheckAsDeleted makes the worktree lookup fail the way a nuked
// sandbox makes it fail: an error, not a measured false.
func stubWorktreeCheckAsDeleted(t *testing.T) {
	t.Helper()
	old := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return false, errors.New("getting polecat HEAD: no such file or directory")
	}
	t.Cleanup(func() { verifyCommitOnMain = old })
}

func stubDurableCheck(t *testing.T, evidence LandedEvidence, err error) {
	t.Helper()
	old := verifyWorkLandedFromDurableState
	verifyWorkLandedFromDurableState = func(workDir, rigName, polecatName, hookBead string) (LandedEvidence, error) {
		return evidence, err
	}
	t.Cleanup(func() { verifyWorkLandedFromDurableState = old })
}

// hookedBeadMock is a bd whose bead is hooked and whose merge queue is empty:
// the state resetAbandonedBead is built for.
func hookedBeadMock() (*BdCli, *mockBdCalls) {
	return mockBd(
		func(args []string) (string, error) {
			switch {
			case len(args) >= 1 && args[0] == "show":
				return `[{"status":"hooked"}]`, nil
			case len(args) >= 1 && args[0] == "query":
				return "[]", nil
			}
			return "", nil
		},
		func(args []string) error { return nil },
	)
}
