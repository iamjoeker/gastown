package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
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

// --- MR half: open MRs whose merge landed but whose completion never ran ---

// fakeLandedGit answers the two questions the MR sweep asks, keyed so a test can
// make each one succeed, fail, or error independently.
type fakeLandedGit struct {
	// ancestors[sha] is whether sha is contained in the target ref.
	ancestors map[string]bool
	// ancestorErrs[sha] makes the containment check unanswerable for that sha,
	// which is what an unfetched commit does in a real clone.
	ancestorErrs map[string]error
	// commits[token] are the target commits naming that source issue.
	commits    map[string][]git.CommitRef
	commitsErr error
	// searched records every (ref, token) pair, so a test can prove the sweep
	// asked about the target it claims to have measured.
	searched []string
}

func (f *fakeLandedGit) IsAncestor(ancestor, _ string) (bool, error) {
	if err, ok := f.ancestorErrs[ancestor]; ok {
		return false, err
	}
	return f.ancestors[ancestor], nil
}

func (f *fakeLandedGit) CommitsWithSubjectToken(ref, token string, _ int) ([]git.CommitRef, error) {
	f.searched = append(f.searched, ref+" "+token)
	if f.commitsErr != nil {
		return nil, f.commitsErr
	}
	return f.commits[token], nil
}

func boolPtr(v bool) *bool { return &v }

// mrWisp builds an open MR bead the way gt done writes one.
func mrWisp(id, branch, target, sourceIssue, rig, commitSHA string) *beads.Issue {
	return &beads.Issue{
		ID:     id,
		Status: "open",
		Description: fmt.Sprintf("branch: %s\ntarget: %s\nsource_issue: %s\nrig: %s\ncommit_sha: %s\nworker: slit",
			branch, target, sourceIssue, rig, commitSHA),
	}
}

// Each check alone has a benign reading that produces identical evidence, so
// only both together confirm. The false containment answer is decisive on its
// own: post-merging an MR that still carries unmerged commits would close its
// bead and delete the branch out from under them.
func TestClassifyLandedMR(t *testing.T) {
	merged := []git.CommitRef{{SHA: "be2b70ed", Subject: "Merge polecat/slit/bd-6n5+msxu2o7f into main"}}

	tests := []struct {
		name        string
		commitSHA   string
		landed      *bool
		commits     []git.CommitRef
		wantVerdict mrLandedVerdict
		wantIn      string
	}{
		{
			name:        "both checks agree the work is in",
			commitSHA:   "5fa7adefb32361d6",
			landed:      boolPtr(true),
			commits:     merged,
			wantVerdict: mrLandedConfirmed,
			wantIn:      "is in the target and 1 commit(s)",
		},
		{
			name:        "containment alone cannot tell a merge from an empty branch",
			commitSHA:   "5fa7adefb32361d6",
			landed:      boolPtr(true),
			wantVerdict: mrLandedUnconfirmed,
			wantIn:      "never carried work reads exactly the same way",
		},
		{
			name:        "naming commits alone with no commit_sha to check",
			landed:      nil,
			commits:     merged,
			wantVerdict: mrLandedUnconfirmed,
			wantIn:      "no commit_sha",
		},
		{
			name:        "not contained is pending work even when commits name the issue",
			commitSHA:   "aaaaaaaabbbbbbbb",
			landed:      boolPtr(false),
			commits:     merged,
			wantVerdict: mrLandedNone,
			wantIn:      "later round",
		},
		{
			name:        "no evidence at all is not a finding",
			commitSHA:   "aaaaaaaabbbbbbbb",
			landed:      boolPtr(false),
			wantVerdict: mrLandedNone,
		},
		{
			name:        "unanswerable containment with nothing naming the issue is not a finding",
			commitSHA:   "aaaaaaaabbbbbbbb",
			landed:      nil,
			wantVerdict: mrLandedNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict, evidence := classifyLandedMR(tt.commitSHA, tt.landed, tt.commits)
			if verdict != tt.wantVerdict {
				t.Errorf("verdict = %q, want %q (evidence: %s)", verdict, tt.wantVerdict, evidence)
			}
			if tt.wantIn != "" && !strings.Contains(evidence, tt.wantIn) {
				t.Errorf("evidence = %q, want it to contain %q", evidence, tt.wantIn)
			}
		})
	}
}

// The gt-3jx0 case end to end: bd-wisp-6td's branch merged as be2b70ed0 while
// the MR stayed open, because the post-merge call never ran.
func TestSweepLandedMRsFindsMergedButOpenMR(t *testing.T) {
	g := &fakeLandedGit{
		ancestors: map[string]bool{"5fa7adefb32361d6dadafae25164b2377d2b3bfb": true},
		commits: map[string][]git.CommitRef{
			"bd-6n5": {{SHA: "be2b70ed09dbf504", Date: "2026-08-18T00:09:13Z", Subject: "Merge polecat/slit/bd-6n5+msxu2o7f into main"}},
		},
	}
	mrs := []*beads.Issue{
		mrWisp("bd-wisp-6td", "polecat/slit/bd-6n5+msxu2o7f", "main", "bd-6n5", "beads", "5fa7adefb32361d6dadafae25164b2377d2b3bfb"),
	}

	findings, skips, scanned, err := sweepLandedMRs(mrs, "beads", "main", g, 5)
	if err != nil {
		t.Fatalf("sweepLandedMRs: %v", err)
	}
	if scanned != 1 {
		t.Errorf("scanned = %d, want 1", scanned)
	}
	if len(skips) != 0 {
		t.Errorf("skips = %+v, want none", skips)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want the one merged-but-open MR", findings)
	}
	finding := findings[0]
	if finding.MRID != "bd-wisp-6td" || finding.Verdict != mrLandedConfirmed {
		t.Errorf("finding = %+v, want bd-wisp-6td confirmed", finding)
	}
	if finding.SourceIssue != "bd-6n5" || finding.Target != "main" || finding.Worker != "slit" {
		t.Errorf("finding lost MR fields: %+v", finding)
	}
	if finding.CommitLanded == nil || !*finding.CommitLanded {
		t.Errorf("commit_landed = %v, want true", finding.CommitLanded)
	}
	if len(g.searched) != 1 || g.searched[0] != "origin/main bd-6n5" {
		t.Errorf("searched = %v, want the MR's own target ref", g.searched)
	}
}

// An MR that still carries commits the target lacks is pending work, and must
// never be reported as landed however many commits name its bead.
func TestSweepLandedMRsIgnoresPendingWork(t *testing.T) {
	g := &fakeLandedGit{
		ancestors: map[string]bool{"deadbeefdeadbeef": false},
		commits: map[string][]git.CommitRef{
			"gt-602": {{SHA: "11111111", Subject: "fix: earlier round (gt-602)"}},
		},
	}
	mrs := []*beads.Issue{mrWisp("gt-wisp-1", "polecat/chrome/gt-602", "main", "gt-602", "gastown", "deadbeefdeadbeef")}

	findings, _, scanned, err := sweepLandedMRs(mrs, "gastown", "main", g, 5)
	if err != nil {
		t.Fatalf("sweepLandedMRs: %v", err)
	}
	if scanned != 1 {
		t.Errorf("scanned = %d, want 1 — a pending MR is examined, not skipped", scanned)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none: the MR carries commits the target lacks", findings)
	}
}

// Wisps are shared across every rig on the Dolt server, and an MR that cannot
// be examined must be named rather than silently dropped.
func TestSweepLandedMRsScopeAndSkips(t *testing.T) {
	g := &fakeLandedGit{
		ancestors: map[string]bool{"cafecafecafecafe": true},
		commits:   map[string][]git.CommitRef{"bd-6n5": {{SHA: "22222222", Subject: "fix (bd-6n5)"}}},
	}
	mrs := []*beads.Issue{
		mrWisp("bd-wisp-other", "polecat/slit/bd-6n5", "main", "bd-6n5", "beads", "cafecafecafecafe"),
		mrWisp("gt-wisp-nosource", "polecat/chrome/gt-1", "main", "", "gastown", "cafecafecafecafe"),
		mrWisp("gt-wisp-pending", "polecat/chrome/gt-2", "main", "gt-2", "gastown", "0123456789abcdef"),
		{ID: "gt-wisp-empty", Status: "open"},
		{ID: "gt-wisp-closed", Status: "closed", Description: "branch: b\ntarget: main\nsource_issue: gt-9\nrig: gastown\ncommit_sha: cafecafecafecafe"},
	}

	findings, skips, scanned, err := sweepLandedMRs(mrs, "gastown", "main", g, 5)
	if err != nil {
		t.Fatalf("sweepLandedMRs: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none — the only landed MR belongs to another rig", findings)
	}
	if scanned != 1 {
		t.Errorf("scanned = %d, want 1 — only gt-wisp-pending is both in scope and examinable", scanned)
	}
	got := map[string]string{}
	for _, skip := range skips {
		got[skip.IssueID] = skip.Reason
	}
	if got["gt-wisp-nosource"] != "no-source-issue" {
		t.Errorf("skips = %+v, want gt-wisp-nosource named with its reason", skips)
	}
	if got["gt-wisp-empty"] != "unparseable-mr" {
		t.Errorf("skips = %+v, want gt-wisp-empty named with its reason", skips)
	}
	if _, ok := got["gt-wisp-closed"]; ok {
		t.Errorf("closed MR reported as unexaminable: %+v", skips)
	}
}

// A git failure invalidates the sweep — it must not be reported as a clean rig.
func TestSweepLandedMRsGitFailureAborts(t *testing.T) {
	g := &fakeLandedGit{commitsErr: errors.New("bad object")}
	mrs := []*beads.Issue{mrWisp("gt-wisp-1", "polecat/chrome/gt-602", "main", "gt-602", "gastown", "abcdef1234567890")}

	if _, _, _, err := sweepLandedMRs(mrs, "gastown", "main", g, 5); err == nil {
		t.Fatal("sweepLandedMRs returned nil error on a git failure; a failed search would read as a clean rig")
	}
}

// An unfetched commit makes containment unanswerable. Unknown must not become
// "not contained": the MR still gets reported on the other check's evidence.
func TestSweepLandedMRsUnansweredContainment(t *testing.T) {
	g := &fakeLandedGit{
		ancestorErrs: map[string]error{"abcdef1234567890": errors.New("unknown revision")},
		commits:      map[string][]git.CommitRef{"gt-602": {{SHA: "33333333", Subject: "Merge polecat/chrome/gt-602 into main"}}},
	}
	mrs := []*beads.Issue{mrWisp("gt-wisp-1", "polecat/chrome/gt-602", "main", "gt-602", "gastown", "abcdef1234567890")}

	findings, _, _, err := sweepLandedMRs(mrs, "gastown", "main", g, 5)
	if err != nil {
		t.Fatalf("sweepLandedMRs: %v", err)
	}
	if len(findings) != 1 || findings[0].Verdict != mrLandedUnconfirmed {
		t.Fatalf("findings = %+v, want one unconfirmed finding", findings)
	}
	if findings[0].CommitLanded != nil {
		t.Errorf("commit_landed = %v, want nil — git could not answer, which is not a no", *findings[0].CommitLanded)
	}
}

func landedMRReport(findings ...mrLandedFinding) reconcileReport {
	report := reconcileReport{
		Rig:          "beads",
		Ref:          "origin/main",
		Fetched:      true,
		SkippedBeads: []reconcileSkip{},
		MissedCloses: []reconcileFinding{},
		InFlight:     []reconcileFinding{},
		SkippedMRs:   []reconcileSkip{},
		LandedMRs:    append([]mrLandedFinding{}, findings...),
		ScannedMRs:   len(findings),
	}
	report.finalize()
	return report
}

func confirmedLandedMR() mrLandedFinding {
	return mrLandedFinding{
		MRID:         "bd-wisp-6td",
		Verdict:      mrLandedConfirmed,
		Branch:       "polecat/slit/bd-6n5+msxu2o7f",
		Target:       "main",
		SourceIssue:  "bd-6n5",
		Worker:       "slit",
		CommitSHA:    "5fa7adefb32361d6dadafae25164b2377d2b3bfb",
		CommitLanded: boolPtr(true),
		Commits:      []git.CommitRef{{SHA: "be2b70ed09dbf504", Date: "2026-08-18T00:09:13Z", Subject: "Merge polecat/slit/bd-6n5+msxu2o7f into main"}},
		Evidence:     "commit 5fa7adef is in the target and 1 commit(s) there name the issue",
	}
}

// The remedy has to be in the output. An operator who reads only the queue sees
// this MR as EMPTY — whose documented disposition is rejection, which reopens
// the source issue and nudges the polecat to resubmit merged work.
func TestLandedMRsOutputNamesPostMergeAndWarnsOffReject(t *testing.T) {
	output := captureReconcileOutput(t, false, landedMRReport(confirmedLandedMR()))

	for _, want := range []string{
		"bd-wisp-6td",
		"polecat/slit/bd-6n5+msxu2o7f",
		"gt mq post-merge beads bd-wisp-6td",
		"Merge polecat/slit/bd-6n5+msxu2o7f into main",
		"--skip-branch-delete",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("landed-MR output does not contain %q:\n%s", want, output)
		}
	}
	if !strings.Contains(output, "Do not reject one") {
		t.Errorf("landed-MR output does not warn against rejecting:\n%s", output)
	}
	if !strings.Contains(output, "active_mr") {
		t.Errorf("landed-MR output does not say what the completion path maintains:\n%s", output)
	}
}

// An unconfirmed finding must not carry a runnable post-merge line: containment
// alone is also what an MR whose branch never carried a commit looks like, and
// post-merging that one closes a bead for work that was never done.
func TestLandedMRsUnconfirmedWithholdsTheCommand(t *testing.T) {
	unconfirmed := confirmedLandedMR()
	unconfirmed.MRID = "bd-wisp-emp"
	unconfirmed.Verdict = mrLandedUnconfirmed
	unconfirmed.Commits = nil
	unconfirmed.Evidence = "commit 5fa7adef is in the target, but no commit there names the issue"

	output := captureReconcileOutput(t, false, landedMRReport(unconfirmed))

	if strings.Contains(output, "gt mq post-merge beads bd-wisp-emp") {
		t.Errorf("unconfirmed finding printed a runnable post-merge command:\n%s", output)
	}
	if !strings.Contains(output, "UNCONFIRMED") {
		t.Errorf("unconfirmed finding is not labelled as such:\n%s", output)
	}
	if !strings.Contains(output, "never carried a commit") {
		t.Errorf("output does not state the reading that makes this ambiguous:\n%s", output)
	}
}

// Confirmed findings sort first: an unconfirmed one needs a human check before
// it can be acted on, and burying the actionable rows under it inverts the read.
func TestLandedMRsSortConfirmedFirst(t *testing.T) {
	unconfirmed := confirmedLandedMR()
	unconfirmed.MRID = "bd-wisp-aaa"
	unconfirmed.Verdict = mrLandedUnconfirmed
	report := landedMRReport(unconfirmed, confirmedLandedMR())

	if report.LandedMRs[0].Verdict != mrLandedConfirmed {
		t.Errorf("landed MRs = %+v, want the confirmed finding first", report.LandedMRs)
	}
}

// A rig with no findings at all must say so for both halves, and JSON consumers
// read .landed_mrs[] unconditionally.
func TestReconcileCleanRigCoversMRs(t *testing.T) {
	human := captureReconcileOutput(t, false, landedMRReport())
	if !strings.Contains(human, "No beads or MRs found") {
		t.Errorf("clean sweep does not state that the MR half was clean too:\n%s", human)
	}
	if !strings.Contains(human, "open MR(s) scanned") {
		t.Errorf("summary line does not scope the MR half:\n%s", human)
	}

	jsonOut := captureReconcileOutput(t, true, landedMRReport())
	if !strings.Contains(jsonOut, `"landed_mrs":[]`) {
		t.Errorf("clean rig emits null rather than [] for landed_mrs:\n%s", jsonOut)
	}
	var decoded reconcileReport
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", jsonOut, err)
	}
	if decoded.LandedMRs == nil || len(decoded.LandedMRs) != 0 {
		t.Errorf("landed_mrs = %+v, want an empty list", decoded.LandedMRs)
	}
}
