package refinery

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/testutil"
)

// setupStrandedRejectEngineer builds an engineer over a real beads store with a
// CLOSED source issue and an open MR bead pointing at it — the exact state
// recheckMRSourceStillMergeable rejects on.
func setupStrandedRejectEngineer(t *testing.T, sourceStatus beads.IssueStatus) (*Engineer, *beads.Beads, *MRInfo, *beads.Issue, *bytes.Buffer) {
	t.Helper()
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	rigPath := t.TempDir()
	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init(uniqueBeadsPrefix(t)); err != nil {
		t.Skipf("bd init unavailable: %v", err)
	}

	srcIssue, err := b.Create(beads.CreateOptions{Title: "Implement feature X", Labels: []string{"gt:task"}})
	if err != nil {
		t.Fatalf("create source issue: %v", err)
	}
	if sourceStatus == beads.StatusClosed {
		if err := b.Close(srcIssue.ID); err != nil {
			t.Fatalf("close source issue: %v", err)
		}
	}

	mrIssue, err := b.Create(beads.CreateOptions{
		Title: "MR for feature X",
		Labels: []string{
			"gt:merge-request",
		},
		Description: "branch: polecat/nux/gt-xyz\nsource_issue: " + srcIssue.ID +
			"\nworker: testrig/polecats/nux\ntarget: main\ncommit_sha: b61cadd3b61cadd3b61cadd3b61cadd3b61cadd3",
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}

	out := &bytes.Buffer{}
	e := &Engineer{
		rig:    &rig.Rig{Name: "testrig", Path: rigPath},
		beads:  b,
		output: out,
	}
	mr := &MRInfo{
		ID:          mrIssue.ID,
		Branch:      "polecat/nux/gt-xyz",
		Target:      "main",
		SourceIssue: srcIssue.ID,
		Worker:      "testrig/polecats/nux",
		Rig:         "testrig",
		CommitSHA:   "b61cadd3b61cadd3b61cadd3b61cadd3b61cadd3",
	}
	return e, b, mr, srcIssue, out
}

func findReportBead(t *testing.T, b *beads.Beads, title string) *beads.Issue {
	t.Helper()
	issues, err := b.Search(beads.SearchOptions{Query: title, Status: "all", Label: "gt:bug", Limit: 20})
	if err != nil {
		t.Fatalf("search for report bead: %v", err)
	}
	for _, issue := range issues {
		if issue.Title == title {
			return issue
		}
	}
	return nil
}

// End to end on the path that runs unattended: the refinery rejects an MR whose
// source issue is closed, and the rejection must no longer be silent (gt-h1cw).
// Deleting the reportStrandedRejection call from rejectMRBeforeMerge makes this
// fail with "no report bead filed", so it can detect the defect it exists for.
func TestRejectMRBeforeMerge_FilesStrandedReportForClosedSourceIssue(t *testing.T) {
	e, b, mr, srcIssue, out := setupStrandedRejectEngineer(t, beads.StatusClosed)

	result := e.rejectMRBeforeMerge(mr, "source_issue "+srcIssue.ID+" status is closed")

	if result.Success || !result.NoMerge {
		t.Fatalf("reject result = %+v, want an ineligible (NoMerge) result", result)
	}

	report := findReportBead(t, b, strandedRejectTitle(mr.ID, srcIssue.ID))
	if report == nil {
		t.Fatalf("no report bead filed for a rejection that stranded %s\nengineer output:\n%s", srcIssue.ID, out.String())
	}
	if report.Priority != 1 {
		t.Errorf("report priority = %d, want 1", report.Priority)
	}
	for _, want := range []string{mr.ID, mr.Branch, srcIssue.ID, mr.CommitSHA, "merge-base --is-ancestor"} {
		if !strings.Contains(report.Description, want) {
			t.Errorf("report description is missing %q:\n%s", want, report.Description)
		}
	}
	// The rig alias does not resolve in a bare temp dir, so the report falls
	// back to the default database — and says so rather than vanishing.
	if !strings.Contains(out.String(), "filed") {
		t.Errorf("engineer said nothing about the report it filed:\n%s", out.String())
	}

	// The whole reason the unattended path reports instead of correcting.
	after, err := b.Show(srcIssue.ID)
	if err != nil {
		t.Fatalf("show source issue: %v", err)
	}
	if beads.IssueStatus(strings.TrimSpace(after.Status)) != beads.StatusClosed {
		t.Errorf("source issue status = %q, want closed — the engineer must not reopen it", after.Status)
	}
}

// The control: an OPEN source issue is still re-slingable, so its rejection
// stranded nothing and no report is filed. Without this, a report path that
// fired on every rejection would pass the test above.
func TestRejectMRBeforeMerge_NoReportWhenSourceIssueIsOpen(t *testing.T) {
	e, b, mr, srcIssue, out := setupStrandedRejectEngineer(t, beads.StatusOpen)

	e.rejectMRBeforeMerge(mr, "MR has missing commit_sha")

	if report := findReportBead(t, b, strandedRejectTitle(mr.ID, srcIssue.ID)); report != nil {
		t.Fatalf("filed report %s for an open source issue\nengineer output:\n%s", report.ID, out.String())
	}
}

// A repeated rejection of the same MR collapses onto the open report.
func TestRejectMRBeforeMerge_RepeatRejectionFilesOneReport(t *testing.T) {
	e, b, mr, srcIssue, _ := setupStrandedRejectEngineer(t, beads.StatusClosed)

	reason := "source_issue " + srcIssue.ID + " status is closed"
	e.rejectMRBeforeMerge(mr, reason)
	e.rejectMRBeforeMerge(mr, reason)

	title := strandedRejectTitle(mr.ID, srcIssue.ID)
	issues, err := b.Search(beads.SearchOptions{Query: title, Status: "all", Label: "gt:bug", Limit: 20})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	count := 0
	for _, issue := range issues {
		if issue.Title == title {
			count++
		}
	}
	if count != 1 {
		t.Errorf("found %d report beads after two rejections, want 1", count)
	}
}
