package refinery

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// mergeRecordStub stands up a `bd` whose `show` returns one MR in the given
// status with the given description, and which logs every `update` invocation
// so the test can read the description that was actually written.
//
// It logs the raw argv AND stdin. beads.Update passes the description through
// `--body-file=-`, so an argv-only log shows `update <id> --body-file=-` and
// nothing else — which reads exactly like a write that never carried a
// description. Measured: the argv-only form failed all three content
// assertions on an update that had in fact written every one of them.
func mergeRecordStub(t *testing.T, mrID, status, description string) (b *beads.Beads, updateLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses a Unix shell script stub for bd")
	}

	stubDir := t.TempDir()
	updateLog = filepath.Join(stubDir, "update-args.log")

	showJSON := `[{"id":"` + mrID + `","title":"Merge: gt-src","status":"` + status +
		`","priority":2,"issue_type":"task","labels":["gt:merge-request"],"description":"` +
		strings.ReplaceAll(description, "\n", `\n`) + `"}]`

	stubScript := `#!/bin/sh
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  show)
    cat <<'JSONEOF'
` + showJSON + `
JSONEOF
    ;;
  update)
    printf '%s\n' "$*" >> "` + updateLog + `"
    cat >> "` + updateLog + `"
    printf '\n' >> "` + updateLog + `"
    ;;
esac
exit 0
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	return beads.New(workDir), updateLog
}

func readUpdateLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("reading update log: %v", err)
	}
	return string(data)
}

// The gt-fe1e shape, exactly: a supersede closed the MR before its merge
// finished, so the bead's close_reason FIELD says "superseded by X" and the
// description carries no outcome at all. The refinery then merged the branch
// and, finding the record terminal, wrote nothing — leaving a merge on main
// with no merged-record behind it. It must record the merge instead.
func TestCloseTerminalMRRecordsMergeOnSupersededMR(t *testing.T) {
	b, updateLog := mergeRecordStub(t, "bd-wisp-u2oy", "closed",
		"branch: polecat/capable/bd-4xn+mt9iznfy\nsource_issue: bd-4xn\ntarget: main\ncommit_sha: abc123\n")

	result, err := closeTerminalMR(b, "bd-wisp-u2oy", terminalMRCloseOptions{
		Reason:      string(CloseReasonMerged),
		MergeCommit: "deadbee",
		MergeProven: true,
		ExpectedMR: &MergeRequest{
			ID:           "bd-wisp-u2oy",
			Branch:       "polecat/capable/bd-4xn+mt9iznfy",
			IssueID:      "bd-4xn",
			TargetBranch: "main",
			CommitSHA:    "abc123",
		},
	})
	if err != nil {
		t.Fatalf("closeTerminalMR: %v", err)
	}
	if !result.AlreadyTerminal {
		t.Fatalf("result = %+v, want AlreadyTerminal", result)
	}
	if result.OutcomeCorrectErr != nil {
		t.Fatalf("OutcomeCorrectErr = %v", result.OutcomeCorrectErr)
	}
	if !result.OutcomeCorrected {
		t.Fatal("OutcomeCorrected = false, want the merge recorded on the already-closed MR")
	}
	if result.RecordedCloseReason != "" {
		t.Errorf("RecordedCloseReason = %q, want empty: this record's description named no outcome",
			result.RecordedCloseReason)
	}

	written := readUpdateLog(t, updateLog)
	if !strings.Contains(written, "close_reason: merged") {
		t.Errorf("bd update did not write close_reason: merged:\n%s", written)
	}
	if !strings.Contains(written, "merge_commit: deadbee") {
		t.Errorf("bd update did not write the merge commit:\n%s", written)
	}
	// The branch identity must survive the rewrite — this is the record for
	// the branch that landed, not a fresh one.
	if !strings.Contains(written, "polecat/capable/bd-4xn+mt9iznfy") {
		t.Errorf("bd update dropped the branch:\n%s", written)
	}
}

// The proof gate, and the reason this repair is not simply "reason == merged".
//
// `gt mq post-merge <id>` reaches closeTerminalMR with Reason=merged on an
// operator's say-so alone, and its ExpectedMR is filled from the very record it
// is checking — a record compared against itself, which passes for a rejected
// MR as readily as a merged one. Without MergeProven that call would stamp
// "merged" over a rejection, inverting the defect instead of fixing it.
// Manager.PostMerge's own refusal (TestManager_PostMerge_AlreadyClosedMR) is
// what caught this; it is pinned here at the boundary that decides it.
func TestCloseTerminalMRDoesNotRepairWithoutMergeProof(t *testing.T) {
	b, updateLog := mergeRecordStub(t, "gt-wisp-unproven", "closed",
		"branch: polecat/test/unproven\nsource_issue: gt-src\ntarget: main\nclose_reason: rejected\n")

	result, err := closeTerminalMR(b, "gt-wisp-unproven", terminalMRCloseOptions{
		Reason:      string(CloseReasonMerged),
		MergeCommit: "deadbee",
		// MergeProven deliberately unset: this caller has no evidence from git.
		ExpectedMR: &MergeRequest{
			ID:           "gt-wisp-unproven",
			Branch:       "polecat/test/unproven",
			IssueID:      "gt-src",
			TargetBranch: "main",
		},
	})
	if err != nil {
		t.Fatalf("closeTerminalMR: %v", err)
	}
	if result.OutcomeCorrected {
		t.Error("OutcomeCorrected = true without a merge proof — an unproven claim must not rewrite a record")
	}
	if written := readUpdateLog(t, updateLog); written != "" {
		t.Errorf("bd update ran without a merge proof:\n%s", written)
	}
	// The prior reason is still REPORTED. Refusing to repair must not also mean
	// refusing to say what the record holds; that is the silence gt-fe1e is
	// made of.
	if result.RecordedCloseReason != string(CloseReasonRejected) {
		t.Errorf("RecordedCloseReason = %q, want %q", result.RecordedCloseReason, CloseReasonRejected)
	}
}

// A description that DOES name the supersede is the other carrier shape.
// Reported as the prior reason, so the correction can be audited.
func TestCloseTerminalMRReportsPriorReasonItReplaced(t *testing.T) {
	b, _ := mergeRecordStub(t, "bd-wisp-gjyc", "closed",
		"branch: polecat/dementus/bd-4xn+mt9jkqgx\nsource_issue: bd-4xn\ntarget: main\nclose_reason: superseded\n")

	result, err := closeTerminalMR(b, "bd-wisp-gjyc", terminalMRCloseOptions{
		Reason:      string(CloseReasonMerged),
		MergeCommit: "ac37cc3",
		MergeProven: true,
	})
	if err != nil {
		t.Fatalf("closeTerminalMR: %v", err)
	}
	if !result.OutcomeCorrected {
		t.Fatal("OutcomeCorrected = false, want the merge recorded over the supersede")
	}
	if result.RecordedCloseReason != string(CloseReasonSuperseded) {
		t.Errorf("RecordedCloseReason = %q, want %q so the correction names what it replaced",
			result.RecordedCloseReason, CloseReasonSuperseded)
	}
}

// The idempotence control. A post-merge retry on a record that already reads
// merged must write nothing — otherwise every retry churns the record and the
// "corrected" signal means nothing.
func TestCloseTerminalMRLeavesAlreadyMergedRecordAlone(t *testing.T) {
	b, updateLog := mergeRecordStub(t, "gt-wisp-done", "closed",
		"branch: polecat/test/done\nsource_issue: gt-src\ntarget: main\nclose_reason: merged\nmerge_commit: cafe123\n")

	result, err := closeTerminalMR(b, "gt-wisp-done", terminalMRCloseOptions{
		Reason:      string(CloseReasonMerged),
		MergeCommit: "cafe123",
		MergeProven: true,
	})
	if err != nil {
		t.Fatalf("closeTerminalMR: %v", err)
	}
	if result.OutcomeCorrected {
		t.Error("OutcomeCorrected = true on a record that already reads merged")
	}
	if written := readUpdateLog(t, updateLog); written != "" {
		t.Errorf("bd update ran on an already-merged record:\n%s", written)
	}
}

// The direction control, and the one that matters most. A merge is a physical
// fact in git; a rejection is a decision. Recording a rejection over a merged
// record would invert the defect rather than fix it, so the repair runs one way
// only.
func TestCloseTerminalMRDoesNotRecordRejectionOverMergedRecord(t *testing.T) {
	b, updateLog := mergeRecordStub(t, "gt-wisp-landed", "closed",
		"branch: polecat/test/landed\nsource_issue: gt-src\ntarget: main\nclose_reason: merged\n")

	result, err := closeTerminalMR(b, "gt-wisp-landed", terminalMRCloseOptions{
		Reason: "rejected: stale",
	})
	if err != nil {
		t.Fatalf("closeTerminalMR: %v", err)
	}
	if result.OutcomeCorrected {
		t.Error("OutcomeCorrected = true for a rejection — a decision must not overwrite a proven merge")
	}
	if written := readUpdateLog(t, updateLog); written != "" {
		t.Errorf("bd update ran for a rejection over a merged record:\n%s", written)
	}
}

// A rejection over a record naming NO outcome is still not recorded. Only
// merged is a fact this path has proof of.
func TestCloseTerminalMRDoesNotRecordRejectionOverSilentRecord(t *testing.T) {
	b, updateLog := mergeRecordStub(t, "gt-wisp-silent", "closed",
		"branch: polecat/test/silent\nsource_issue: gt-src\ntarget: main\n")

	result, err := closeTerminalMR(b, "gt-wisp-silent", terminalMRCloseOptions{
		Reason: "rejected: stale",
	})
	if err != nil {
		t.Fatalf("closeTerminalMR: %v", err)
	}
	if result.OutcomeCorrected {
		t.Error("OutcomeCorrected = true for a rejection")
	}
	if written := readUpdateLog(t, updateLog); written != "" {
		t.Errorf("bd update ran for a rejection:\n%s", written)
	}
}

// The repair is bound to the merge that was verified. If the record drifted
// from the snapshot the caller proved, closeTerminalMR refuses before reaching
// the repair — writing "merged" onto a neighbouring record is the same class of
// damage as the defect.
func TestCloseTerminalMRRefusesRepairOnSnapshotDrift(t *testing.T) {
	b, updateLog := mergeRecordStub(t, "gt-wisp-drift", "closed",
		"branch: polecat/test/other\nsource_issue: gt-src\ntarget: main\ncommit_sha: aaa\n")

	_, err := closeTerminalMR(b, "gt-wisp-drift", terminalMRCloseOptions{
		Reason:      string(CloseReasonMerged),
		MergeCommit: "deadbee",
		MergeProven: true,
		ExpectedMR: &MergeRequest{
			ID:           "gt-wisp-drift",
			Branch:       "polecat/test/verified",
			IssueID:      "gt-src",
			TargetBranch: "main",
			CommitSHA:    "aaa",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed after merge proof") {
		t.Fatalf("err = %v, want the snapshot drift refusal", err)
	}
	if written := readUpdateLog(t, updateLog); written != "" {
		t.Errorf("bd update ran despite snapshot drift:\n%s", written)
	}
}

// An OPEN MR is closed by the existing path, not repaired by this one. The
// repair must not fire where the normal close already writes the outcome.
func TestCloseTerminalMRDoesNotReportCorrectionOnOpenMR(t *testing.T) {
	b, _ := mergeRecordStub(t, "gt-wisp-open", "open",
		"branch: polecat/test/open\nsource_issue: gt-src\ntarget: main\n")

	result, err := closeTerminalMR(b, "gt-wisp-open", terminalMRCloseOptions{
		Reason:      string(CloseReasonMerged),
		MergeCommit: "deadbee",
		MergeProven: true,
	})
	if err != nil {
		t.Fatalf("closeTerminalMR: %v", err)
	}
	if result.AlreadyTerminal || result.OutcomeCorrected {
		t.Errorf("result = %+v, want a plain close on an open MR", result)
	}
}
