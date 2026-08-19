package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestReconcileSkipReason(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
		want  string
	}{
		{
			name:  "ordinary open bug is checked",
			issue: &beads.Issue{ID: "gt-602", Status: "open", Type: "bug"},
			want:  "",
		},
		{
			name:  "in_progress is checked",
			issue: &beads.Issue{ID: "gt-602", Status: "in_progress", Type: "bug"},
			want:  "",
		},
		{
			name:  "closed bead is not a finding",
			issue: &beads.Issue{ID: "gt-602", Status: "closed", Type: "bug"},
			want:  "terminal",
		},
		{
			name:  "MR beads are runtime state, not work",
			issue: &beads.Issue{ID: "gt-mr-1", Status: "open", Type: "merge-request"},
			want:  "internal-type:merge-request",
		},
		{
			name:  "agent beads are runtime state",
			issue: &beads.Issue{ID: "gt-gastown-polecat-settler", Status: "open", Type: "agent"},
			want:  "internal-type:agent",
		},
		{
			name:  "wisps are ephemeral",
			issue: &beads.Issue{ID: "gt-wisp-gdnc", Status: "open", Type: "bug"},
			want:  "wisp-id",
		},
		{
			// gt:rig identity beads must never be reported as closable work.
			name:  "protected labels are skipped",
			issue: &beads.Issue{ID: "gt-rig-gastown", Status: "open", Type: "task", Labels: []string{"gt:rig"}},
			want:  "protected-label:gt:rig",
		},
		{
			name:  "no_merge beads never produce a merge",
			issue: &beads.Issue{ID: "gt-602", Status: "open", Type: "bug", Description: "no_merge: true"},
			want:  "no_merge",
		},
		{
			name:  "review_only beads never produce a merge",
			issue: &beads.Issue{ID: "gt-602", Status: "open", Type: "bug", Description: "review_only: true"},
			want:  "review_only",
		},
		{
			name:  "local merge strategy lands outside the queue",
			issue: &beads.Issue{ID: "gt-602", Status: "open", Type: "bug", Description: "merge_strategy: local"},
			want:  "merge_strategy:local",
		},
		{
			name:  "an attached molecule alone does not skip the bead",
			issue: &beads.Issue{ID: "gt-602", Status: "open", Type: "bug", Description: "attached_formula: mol-polecat-work"},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reconcileSkipReason(tt.issue); got != tt.want {
				t.Errorf("reconcileSkipReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Pinned beads are permanent reference and must never be reported as closable;
// terminal beads are already closed. Hooked beads must be scanned — a hooked
// bead with work already on the target is the re-dispatch case (gt-2uqy).
func TestReconcileStatusScope(t *testing.T) {
	if len(reconcileStatuses) == 0 {
		t.Fatal("reconcileStatuses is empty; the sweep would scan nothing and report clean")
	}
	var hooked bool
	for _, status := range reconcileStatuses {
		switch status {
		case beads.IssueStatusPinned:
			t.Errorf("reconcileStatuses includes %q, which must never be closed", status)
		case beads.StatusClosed, beads.StatusTombstone:
			t.Errorf("reconcileStatuses includes terminal status %q", status)
		case beads.IssueStatusHooked:
			hooked = true
		}
	}
	if !hooked {
		t.Error("reconcileStatuses omits hooked; a polecat re-dispatched onto merged work would be invisible")
	}
	if !strings.Contains(mqReconcileCmd.Long, "Hooked beads are reported separately") {
		t.Error("mq reconcile help must explain that hooked findings are not the same claim")
	}
}

func TestTruncateReconcileTitle(t *testing.T) {
	short := "gt sling never reuses idle polecats"
	if got := truncateReconcileTitle(short); got != short {
		t.Errorf("truncateReconcileTitle(short) = %q, want it unchanged", got)
	}

	long := strings.Repeat("x", 200)
	got := truncateReconcileTitle(long)
	if len([]rune(got)) != 80 {
		t.Errorf("truncateReconcileTitle(long) length = %d runes, want 80", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncateReconcileTitle(long) = %q, want an ellipsis suffix", got)
	}
}

// The reconcile command reports; it must not close. Auto-closing on "a commit
// names this bead" would recreate the false completions done_ledger.go refuses,
// because a naming commit can be a partial fix by another bead's worker.
func TestReconcileDoesNotClose(t *testing.T) {
	cmd := mqReconcileCmd
	if cmd.Flags().Lookup("close") != nil || cmd.Flags().Lookup("fix") != nil {
		t.Error("mq reconcile grew a closing flag; closing must stay a human judgment call")
	}
	if !strings.Contains(cmd.Long, "reports only") {
		t.Error("mq reconcile help must state that it reports only")
	}
}

// captureReconcileOutput runs print with stdout redirected and returns what it
// wrote.
func captureReconcileOutput(t *testing.T, asJSON bool, report reconcileReport) string {
	t.Helper()
	oldJSON := mqReconcileJSON
	mqReconcileJSON = asJSON
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = oldStdout
		mqReconcileJSON = oldJSON
	})

	printErr := printReconcileReport(report)
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	os.Stdout = oldStdout
	mqReconcileJSON = oldJSON
	if printErr != nil {
		t.Fatalf("printReconcileReport: %v", printErr)
	}
	return buf.String()
}

func reconcileReportWithSkips(skips ...reconcileSkip) reconcileReport {
	report := reconcileReport{
		Rig:          "gastown",
		Ref:          "origin/main",
		Fetched:      true,
		Scanned:      50,
		SkippedBeads: append([]reconcileSkip{}, skips...),
		MissedCloses: []reconcileFinding{},
		InFlight:     []reconcileFinding{},
	}
	report.finalize()
	return report
}

// A skipped bead that is only counted is indistinguishable from a bead that was
// searched and found clean — the same reasoning the Unsearchable path already
// applies. Every exclusion is correct, but a bead wrongly labelled no_merge
// skips for a wrong reason and is the missed close this command exists to find.
func TestReconcileNamesSkippedBeadsInHumanOutput(t *testing.T) {
	output := captureReconcileOutput(t, false, reconcileReportWithSkips(
		reconcileSkip{IssueID: "gt-mr-1", Reason: "internal-type:merge-request"},
		reconcileSkip{IssueID: "gt-602", Reason: "no_merge"},
		reconcileSkip{IssueID: "gt-2uqy", Reason: "no_merge"},
	))

	if !strings.Contains(output, "3 skipped") {
		t.Errorf("summary line lost the skipped count:\n%s", output)
	}
	for _, want := range []string{"gt-602", "gt-2uqy", "gt-mr-1", "no_merge", "internal-type:merge-request"} {
		if !strings.Contains(output, want) {
			t.Errorf("skipped beads output does not name %q:\n%s", want, output)
		}
	}
	// Grouped by reason, so a reason with two beads is stated once.
	if got := strings.Count(output, "no_merge"); got != 1 {
		t.Errorf("no_merge appears %d times, want 1 grouped line:\n%s", got, output)
	}
	if !strings.Contains(output, "no_merge: gt-2uqy, gt-602") {
		t.Errorf("skipped beads are not grouped and sorted under their reason:\n%s", output)
	}
}

// A clean sweep must not grow a section about beads that do not exist.
func TestReconcileNoSkipsPrintsNoSkipSection(t *testing.T) {
	output := captureReconcileOutput(t, false, reconcileReportWithSkips())
	if strings.Contains(output, "skipped before") {
		t.Errorf("empty skip list still printed a section:\n%s", output)
	}
	if !strings.Contains(output, "0 skipped") {
		t.Errorf("summary line lost the skipped count:\n%s", output)
	}
}

// The patrol reads this command's JSON. skipped_beads carries the attribution
// the count cannot, and is an empty list rather than null on a clean rig so a
// consumer can read .skipped_beads[] unconditionally.
func TestReconcileJSONNamesSkippedBeads(t *testing.T) {
	output := captureReconcileOutput(t, true, reconcileReportWithSkips(
		reconcileSkip{IssueID: "gt-602", Reason: "no_merge"},
	))

	var decoded reconcileReport
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", output, err)
	}
	if len(decoded.SkippedBeads) != 1 {
		t.Fatalf("skipped_beads = %+v, want the one skipped bead", decoded.SkippedBeads)
	}
	if decoded.SkippedBeads[0].IssueID != "gt-602" || decoded.SkippedBeads[0].Reason != "no_merge" {
		t.Errorf("skipped_beads[0] = %+v, want gt-602/no_merge", decoded.SkippedBeads[0])
	}
	if decoded.Skipped != len(decoded.SkippedBeads) {
		t.Errorf("skipped = %d but skipped_beads has %d entries; the count and the names disagree",
			decoded.Skipped, len(decoded.SkippedBeads))
	}
	if !strings.Contains(output, `"skipped_beads":[]`) {
		clean := captureReconcileOutput(t, true, reconcileReportWithSkips())
		if !strings.Contains(clean, `"skipped_beads":[]`) {
			t.Errorf("clean rig emits null rather than [] for skipped_beads:\n%s", clean)
		}
	}
}

// finalize derives the count from the named list rather than trusting a tally
// maintained alongside it, and orders the list so two runs of the same sweep
// produce the same report.
func TestReconcileFinalizeDerivesSkipCount(t *testing.T) {
	report := reconcileReport{
		Skipped: 99,
		SkippedBeads: []reconcileSkip{
			{IssueID: "gt-zzz", Reason: "terminal"},
			{IssueID: "gt-aaa", Reason: "no_merge"},
		},
	}
	report.finalize()

	if report.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2 derived from the named beads", report.Skipped)
	}
	if report.SkippedBeads[0].IssueID != "gt-aaa" {
		t.Errorf("SkippedBeads not sorted by ID: %+v", report.SkippedBeads)
	}
}

// The fetch is the load-bearing part: gt-602's own retraction traces to a
// grep run against an unfetched clone, where merged work returned zero.
func TestReconcileFetchIsDefaultOn(t *testing.T) {
	flag := mqReconcileCmd.Flags().Lookup("no-fetch")
	if flag == nil {
		t.Fatal("mq reconcile has no --no-fetch flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("--no-fetch default = %q, want false so the sweep fetches by default", flag.DefValue)
	}
}
