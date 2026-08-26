package cmd

import (
	"strings"
	"testing"
)

// gt-ewip. A polecat whose task produced findings rather than code has one
// sanctioned way to say so: --cleanup-status=clean. The zero-commit guard in
// runDone honours it by name; the ledger block that follows did not, and on a
// fork-backed rig that block refused every no-MR code-bead close outright. The
// polecat could not submit (nothing to submit) and could not close (this
// refusal), and the refusal's own advice — "use the fork PR flow instead" —
// needs a commit to open a PR with.
//
// The fixture is gt-7k3q's, deliberately: same fork-backed rig, same code bead,
// same branch sitting at the target tip with nothing of its own, and nothing on
// the target that names the bead. The ONLY difference between the two tests
// below is --cleanup-status, which is exactly the discrimination the fix adds.
// Sharing the fixture is what makes that provable rather than asserted.
func TestRunDoneClosesReportOnlyCodeBeadOnForkBackedRig(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupSupersededGitRepo(t, workDir, false, true)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetDoneFlagsForTest(t)
	primeSupersededDoneEnv(t, workDir, routedSourceTestTownRoot(workDir))
	doneCleanupStatus = "clean"

	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone on a report-only close: %v\nthis is gt-ewip: a report-only polecat on a fork-backed rig can neither submit nor close", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	if !strings.Contains(log, "close bd-source") {
		t.Fatalf("bd log has no close of bd-source, so the bead is left HOOKED against a nuked polecat:\n%s", log)
	}
	// The close must say what it is. A report-only close that recorded a SHA
	// would be recording the base ref — a stranger's commit — as proof of work,
	// which is the gt-r5p defect this arm exists to avoid, not to reproduce.
	for _, want := range []string{"report_only: true", "commit_sha: none"} {
		if !strings.Contains(log, want) {
			t.Errorf("close reason is missing %q, so the close cannot be told from one backed by a commit:\n%s", want, log)
		}
	}
	// Nothing to submit means nothing submitted.
	assertBDLogNotContains(t, log, currentBeadsDir, "--labels=gt:merge-request")
}

// The other half, and the reason the fix is a new arm rather than a relaxed
// refusal: with --cleanup-status anything but clean, the same fork-backed rig
// with the same absence of evidence must still refuse. gt-7k3q's own test
// covers this too; it is repeated here against the report-only fixture so that
// a future change which widens the new arm — dropping the cleanup-status
// condition, say — fails in the file that introduced it.
func TestRunDoneStillRefusesForkBackedCloseWithoutTheReportOnlyFlag(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupSupersededGitRepo(t, workDir, false, true)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetDoneFlagsForTest(t)
	primeSupersededDoneEnv(t, workDir, routedSourceTestTownRoot(workDir))
	doneCleanupStatus = "unpushed"

	err := runDone(nil, nil)
	if err == nil {
		t.Fatal("runDone closed a fork-backed code bead with no work, no landed commit, and no report-only flag")
	}
	if !strings.Contains(err.Error(), "fork/upstream mode") {
		t.Errorf("refusal %q no longer names the fork/upstream condition it turns on", err.Error())
	}
	if log := readSubmitSourceBDLog(t, logPath); strings.Contains(log, "close bd-source") {
		t.Errorf("a refused completion must not close the bead:\n%s", log)
	}
}
