package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// setupSupersededGitRepo builds the state gt-7k3q describes: a sibling landed
// the work on the target under a commit that names the bead, and this polecat's
// branch sits at that same commit with nothing of its own to submit.
//
// Two knobs, and they are the two variables the fix turns on:
//
//	landsTheBead  whether the commit on the target names the bead. With it, the
//	              work is provably in the target; without it, zero commits ahead
//	              means the polecat wrote nothing. The two must not reach the
//	              same outcome.
//	forkBacked    whether the rig fetches from an upstream it cannot push to.
//	              This is the configuration dementus hit, and the one where
//	              gt done had no exit at all: the no-MR code-bead close is
//	              refused outright in fork/upstream mode.
func setupSupersededGitRepo(t *testing.T, workDir string, landsTheBead, forkBacked bool) string {
	t.Helper()
	remote := t.TempDir()
	runGitForMQSubmitTest(t, remote, "init", "--bare")
	runGitForMQSubmitTest(t, workDir, "init")
	runGitForMQSubmitTest(t, workDir, "config", "user.email", "test@example.com")
	runGitForMQSubmitTest(t, workDir, "config", "user.name", "Test User")
	runGitForMQSubmitTest(t, workDir, "remote", "add", "origin", remote)
	writeMQSubmitTestFile(t, workDir, ".gitignore", ".beads/\n.runtime/\n")
	writeMQSubmitTestFile(t, workDir, "file.txt", "main\n")
	runGitForMQSubmitTest(t, workDir, "add", ".gitignore", "file.txt")
	runGitForMQSubmitTest(t, workDir, "commit", "-m", "main")
	runGitForMQSubmitTest(t, workDir, "branch", "-M", "main")

	// The sibling's landing commit.
	writeMQSubmitTestFile(t, workDir, "sibling.txt", "landed by another polecat\n")
	runGitForMQSubmitTest(t, workDir, "add", "sibling.txt")
	subject := "fix(testenv): guard the production server (bd-source)"
	if !landsTheBead {
		subject = "chore: unrelated maintenance"
	}
	runGitForMQSubmitTest(t, workDir, "commit", "-m", subject)
	runGitForMQSubmitTest(t, workDir, "push", "-u", "origin", "main")

	if forkBacked {
		upstream := t.TempDir()
		runGitForMQSubmitTest(t, upstream, "init", "--bare")
		runGitForMQSubmitTest(t, workDir, "remote", "add", "upstream", upstream)
		runGitForMQSubmitTest(t, workDir, "push", "upstream", "main")
		runGitForMQSubmitTest(t, workDir, "fetch", "upstream")
	}

	// The polecat's branch, created at the target's tip: zero commits ahead.
	branch := "polecat/refuge/bd-source"
	runGitForMQSubmitTest(t, workDir, "checkout", "-b", branch)
	return branch
}

func primeSupersededDoneEnv(t *testing.T, workDir, townRoot string) {
	t.Helper()
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)
	t.Setenv("GT_ROLE", "gastown/polecats/refuge")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "refuge")
	t.Setenv("BD_ACTOR", "gastown/polecats/refuge")
	t.Chdir(workDir)

	doneIssue = "bd-source"
	doneCleanupStatus = "unpushed"
	updateAgentStateOnDoneFn = func(cwd, townRoot, exitType, issueID, mrID string) error { return nil }
}

// The acceptance criterion for gt-7k3q, in the configuration that had no exit:
// a fork-backed rig, a code bead, and a branch zero commits ahead of a target
// that already carries the work. Every path refused it — the no-MR close ("use
// the fork PR flow instead"), --skip-verify (code beads are refused), and
// DEFERRED (simply false; the work is finished) — so the polecat closed the
// bead itself and orphaned a P0 merge request.
func TestRunDoneClosesSupersededCodeBeadOnForkBackedRig(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupSupersededGitRepo(t, workDir, true, true)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetDoneFlagsForTest(t)
	primeSupersededDoneEnv(t, workDir, routedSourceTestTownRoot(workDir))

	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone on superseded work: %v\nthis is the dead end gt-7k3q is about — every exit refused", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	if !strings.Contains(log, "close bd-source") {
		t.Fatalf("bd log has no close of bd-source; the bead is left for the polecat to close by hand:\n%s", log)
	}
	// The close must record the commit it relied on. It is the only durable
	// evidence that this bead was closed over somebody else's work.
	for _, want := range []string{"superseded: true", "fix(testenv): guard the production server (bd-source)"} {
		if !strings.Contains(log, want) {
			t.Errorf("close reason is missing %q, so the close cannot be audited against git:\n%s", want, log)
		}
	}
	// Nothing to submit means nothing submitted. A superseded close that also
	// filed a merge request would put a byte-identical duplicate in the queue.
	assertBDLogNotContains(t, log, currentBeadsDir, "--labels=gt:merge-request")
}

// The other half of the same discrimination. Same fork-backed rig, same zero
// commits ahead — but nothing on the target names the bead, so there is no
// evidence the work landed and the close stays refused. The refusal must exit
// non-zero and must name what was checked and what not to do next; describing
// only the state is what left gt-7k3q's polecat with no correct action.
func TestRunDoneRefusesForkBackedCloseWhenNothingLanded(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupSupersededGitRepo(t, workDir, false, true)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetDoneFlagsForTest(t)
	primeSupersededDoneEnv(t, workDir, routedSourceTestTownRoot(workDir))

	err := runDone(nil, nil)
	if err == nil {
		t.Fatal("runDone closed a fork-backed code bead with no work and nothing on the target")
	}
	for _, want := range []string{
		"fork/upstream mode",
		"no evidence the work landed",
		"Do NOT close the bead by hand",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q is missing %q", err.Error(), want)
		}
	}
	if log := readSubmitSourceBDLog(t, logPath); strings.Contains(log, "close bd-source") {
		t.Errorf("a refused completion must not close the bead:\n%s", log)
	}
}

// The same superseded close on an ordinary (non-fork) rig. This path already
// closed the bead, but on the strength of HEAD being reachable from
// origin/main — and on this path HEAD *is* the base ref, so the ledger recorded
// an unrelated commit as proof of work. The evidence recorded now is the commit
// that actually carries the bead.
func TestRunDoneRecordsTheLandingCommitOnSupersededClose(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupSupersededGitRepo(t, workDir, true, false)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetDoneFlagsForTest(t)
	primeSupersededDoneEnv(t, workDir, routedSourceTestTownRoot(workDir))

	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	if !strings.Contains(log, "superseded: true") {
		t.Errorf("close reason does not record the superseding commit:\n%s", log)
	}
	if strings.Contains(log, "Completed with no code changes") {
		t.Errorf("close still uses the generic no-code-changes reason, which names no evidence:\n%s", log)
	}
}

// gt-gubw, at the level the pure guard cannot reach: that runDone actually
// consults the bead's status before refusing.
//
// This is TestRunDoneRefusesForkBackedCloseWhenNothingLanded with one variable
// changed — the source bead is closed rather than open — and it is the control
// for this one. Same fork-backed rig, same empty unpushed branch, same absence
// of any landing commit naming the bead; that test must keep refusing, or the
// exemption here is a blanket pass rather than a status check.
//
// The state is what a polecat assigned to fix ANOTHER agent's branch ends up
// in: the work lands over there under a commit naming the branch owner's bead,
// the refinery closes this one, and this branch is empty by design.
func TestRunDoneExitsWhenTheSourceBeadIsAlreadyClosed(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupSupersededGitRepo(t, workDir, false, true)
	logPath := installSubmitSourceBDRecorderWithStatus(t, currentBeadsDir, ownerBeadsDir, "closed")
	resetDoneFlagsForTest(t)
	primeSupersededDoneEnv(t, workDir, routedSourceTestTownRoot(workDir))

	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone on an already-closed bead: %v\nthe bead is closed, so there is no completion to record and nothing left to refuse", err)
	}

	// Exempting the guard must not turn into closing the bead a second time,
	// which would overwrite the refinery's proof of work with a generic reason.
	if log := readSubmitSourceBDLog(t, logPath); strings.Contains(log, "close bd-source") {
		t.Errorf("an already-closed bead must not be closed again:\n%s", log)
	}
}
