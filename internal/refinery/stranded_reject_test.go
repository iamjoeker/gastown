package refinery

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

type fakeStrandedStore struct {
	issues  map[string]*beads.Issue
	created []beads.CreateOptions
	updates map[string][]beads.UpdateOptions
	nextID  int

	showErr    error
	createErr  error
	updateErr  error
	rigBindErr error
}

func newFakeStrandedStore() *fakeStrandedStore {
	return &fakeStrandedStore{
		issues:  map[string]*beads.Issue{},
		updates: map[string][]beads.UpdateOptions{},
	}
}

func (f *fakeStrandedStore) add(issue *beads.Issue) {
	f.issues[issue.ID] = issue
}

func (f *fakeStrandedStore) Show(id string) (*beads.Issue, error) {
	if f.showErr != nil {
		return nil, f.showErr
	}
	issue, ok := f.issues[id]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

// CreateIfNoDuplicate mirrors the real one closely enough to exercise dedup:
// an OPEN gt:bug with the same title wins, anything else creates.
func (f *fakeStrandedStore) CreateIfNoDuplicate(opts beads.CreateOptions) (*beads.Issue, bool, error) {
	if f.createErr != nil {
		return nil, false, f.createErr
	}
	if f.rigBindErr != nil && opts.Rig != "" {
		return nil, false, f.rigBindErr
	}
	for _, issue := range f.issues {
		if issue.Title == opts.Title && beads.IssueStatus(issue.Status) == beads.StatusOpen {
			return issue, false, nil
		}
	}
	f.nextID++
	issue := &beads.Issue{
		ID:          "gt-report" + string(rune('0'+f.nextID)),
		Title:       opts.Title,
		Status:      string(beads.StatusOpen),
		Labels:      opts.Labels,
		Priority:    opts.Priority,
		Description: opts.Description,
	}
	f.issues[issue.ID] = issue
	f.created = append(f.created, opts)
	return issue, true, nil
}

func (f *fakeStrandedStore) Update(id string, opts beads.UpdateOptions) error {
	f.updates[id] = append(f.updates[id], opts)
	return f.updateErr
}

func (f *fakeStrandedStore) assignee(t *testing.T, id string) string {
	t.Helper()
	for _, up := range f.updates[id] {
		if up.Assignee != nil {
			return *up.Assignee
		}
	}
	return ""
}

func closedWorkIssue(id string) *beads.Issue {
	return &beads.Issue{ID: id, Title: "Implement feature X", Type: "bug", Status: string(beads.StatusClosed)}
}

func strandedReq() strandedRejectRequest {
	return strandedRejectRequest{
		MRID:        "gt-wisp-0an54",
		Branch:      "polecat/brahmin/gt-aqk+abc",
		Target:      "main",
		CommitSHA:   "b61cadd3b61cadd3b61cadd3b61cadd3b61cadd3",
		Worker:      "gastown/polecats/brahmin",
		SourceIssue: "gt-aqk",
		Reason:      "source_issue gt-aqk status is closed",
		Rig:         "gastown",
	}
}

// notMerged is the probe answer for a branch that is not in the target.
func notMerged(string, string) (bool, error) { return false, nil }

// The defect: the refinery rejects an MR *because* the source issue is
// terminal, closes the MR bead, and leaves a closed bead over an unmerged
// branch that nothing re-slings (gt-h1cw). The engineer must not correct that
// silently, but it must not stay silent either.
func TestReportStrandedReject_ClosedIssueUnmergedBranchIsReported(t *testing.T) {
	store := newFakeStrandedStore()
	store.add(closedWorkIssue("gt-aqk"))

	result := reportStrandedReject(store, notMerged, strandedReq())

	if !result.Stranded {
		t.Fatalf("Stranded = false, want true (err: %v, skip: %q, merged: %v)",
			result.Err, result.SkipReason, result.AlreadyMerged)
	}
	if !result.Created {
		t.Errorf("Created = false, want true")
	}
	if result.Err != nil {
		t.Errorf("Err = %v, want nil", result.Err)
	}
	if len(store.created) != 1 {
		t.Fatalf("filed %d beads, want 1", len(store.created))
	}

	opts := store.created[0]
	if opts.Priority != 1 {
		t.Errorf("Priority = %d, want 1", opts.Priority)
	}
	if len(opts.Labels) != 1 || opts.Labels[0] != "gt:bug" {
		t.Errorf("Labels = %v, want [gt:bug] (CreateIfNoDuplicate only dedups against gt:bug)", opts.Labels)
	}
	if opts.Rig != "gastown" {
		t.Errorf("Rig = %q, want gastown — the report must land in the rig's database (gt-7y7)", opts.Rig)
	}
	if got := store.assignee(t, result.BeadID); got != "gastown/witness" {
		t.Errorf("assignee = %q, want gastown/witness", got)
	}

	// Every fact needed to decide between the two readings has to be in the
	// bead itself: the report outlives the refinery log it was printed beside.
	for _, want := range []string{
		"gt-wisp-0an54",
		"polecat/brahmin/gt-aqk+abc",
		"b61cadd3b61cadd3b61cadd3b61cadd3b61cadd3",
		"gt-aqk",
		"source_issue gt-aqk status is closed",
		"gastown/polecats/brahmin",
		"merge-base --is-ancestor",
	} {
		if !strings.Contains(opts.Description, want) {
			t.Errorf("description is missing %q:\n%s", want, opts.Description)
		}
	}
}

// The control that has to be able to fail on its own: an OPEN source issue is
// re-slingable, so its rejection stranded nothing and there is nothing to file.
// Without this, a report path that fired on every rejection would pass the
// test above.
func TestReportStrandedReject_OpenIssueIsNotReported(t *testing.T) {
	for _, status := range []string{
		string(beads.StatusOpen),
		string(beads.StatusInProgress),
		string(beads.IssueStatusHooked),
		string(beads.StatusBlocked),
		string(beads.StatusDeferred),
	} {
		t.Run(status, func(t *testing.T) {
			store := newFakeStrandedStore()
			issue := closedWorkIssue("gt-aqk")
			issue.Status = status
			store.add(issue)

			result := reportStrandedReject(store, notMerged, strandedReq())

			if result.Stranded {
				t.Errorf("Stranded = true for a %s source issue", status)
			}
			if len(store.created) != 0 {
				t.Errorf("filed %d beads for a %s source issue, want 0", len(store.created), status)
			}
			if result.SourceIssueStatus != status {
				t.Errorf("SourceIssueStatus = %q, want %q", result.SourceIssueStatus, status)
			}
		})
	}
}

// Ancestry proves reading (a): the work is in the target, so the closed bead is
// correct and there is nothing for a human to decide.
func TestReportStrandedReject_MergedBranchIsNotReported(t *testing.T) {
	store := newFakeStrandedStore()
	store.add(closedWorkIssue("gt-aqk"))

	var probedCommit, probedTarget string
	merged := func(commit, target string) (bool, error) {
		probedCommit, probedTarget = commit, target
		return true, nil
	}

	result := reportStrandedReject(store, merged, strandedReq())

	if !result.AlreadyMerged {
		t.Errorf("AlreadyMerged = false, want true")
	}
	if result.Stranded {
		t.Errorf("Stranded = true, want false — the commit is in the target")
	}
	if len(store.created) != 0 {
		t.Errorf("filed %d beads for work that is already merged, want 0", len(store.created))
	}
	if probedCommit != strandedReq().CommitSHA || probedTarget != "main" {
		t.Errorf("probed (%q, %q), want (%q, main)", probedCommit, probedTarget, strandedReq().CommitSHA)
	}
}

// Only an error-free "yes" suppresses. Ancestry proves containment one way
// only, so an unknown commit, a missing ref or a squash-landed branch must all
// still reach a human — silent work loss is the defect, not the noise.
func TestReportStrandedReject_UncertainAncestryStillReports(t *testing.T) {
	cases := map[string]branchMergedProbe{
		"probe errors":        func(string, string) (bool, error) { return false, errors.New("unknown revision") },
		"probe says no":       notMerged,
		"probe true with err": func(string, string) (bool, error) { return true, errors.New("bad object") },
		"no probe":            nil,
	}
	for name, probe := range cases {
		t.Run(name, func(t *testing.T) {
			store := newFakeStrandedStore()
			store.add(closedWorkIssue("gt-aqk"))

			result := reportStrandedReject(store, probe, strandedReq())

			if !result.Stranded {
				t.Errorf("Stranded = false, want true (%s must not suppress the report)", name)
			}
			if len(store.created) != 1 {
				t.Errorf("filed %d beads, want 1", len(store.created))
			}
		})
	}
}

// A missing commit_sha is itself a rejection reason. There is nothing to probe,
// so the report is filed rather than suppressed.
func TestReportStrandedReject_NoCommitSHAStillReports(t *testing.T) {
	store := newFakeStrandedStore()
	store.add(closedWorkIssue("gt-aqk"))

	probed := false
	req := strandedReq()
	req.CommitSHA = ""
	result := reportStrandedReject(store, func(string, string) (bool, error) {
		probed = true
		return true, nil
	}, req)

	if probed {
		t.Errorf("probed ancestry with no commit_sha")
	}
	if !result.Stranded || len(store.created) != 1 {
		t.Errorf("Stranded = %v with %d beads filed, want true with 1", result.Stranded, len(store.created))
	}
}

// The whole point of choosing report-over-correct: the engineer cannot assert
// "this work is not done", so it must never rewrite the source issue.
// `gt mq reject` reopens; this path does not (gt-a46b, gt-h1cw).
func TestReportStrandedReject_NeverRewritesSourceIssue(t *testing.T) {
	store := newFakeStrandedStore()
	store.add(closedWorkIssue("gt-aqk"))

	reportStrandedReject(store, notMerged, strandedReq())

	if ups := store.updates["gt-aqk"]; len(ups) != 0 {
		t.Fatalf("source issue was updated %d times: %+v — the unattended path must not reopen", len(ups), ups)
	}
	if got := store.issues["gt-aqk"].Status; got != string(beads.StatusClosed) {
		t.Errorf("source issue status = %q, want closed (untouched)", got)
	}
}

// A repeated rejection of the same MR must collapse onto the open report, not
// file a second one, and must not re-assign a report someone already picked up.
func TestReportStrandedReject_RepeatRejectionDoesNotFileTwice(t *testing.T) {
	store := newFakeStrandedStore()
	store.add(closedWorkIssue("gt-aqk"))

	first := reportStrandedReject(store, notMerged, strandedReq())
	if !first.Created {
		t.Fatalf("first report was not created")
	}
	// Someone has picked it up.
	other := "gastown/polecats/nux"
	if err := store.Update(first.BeadID, beads.UpdateOptions{Assignee: &other}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	second := reportStrandedReject(store, notMerged, strandedReq())

	if second.Created {
		t.Errorf("Created = true on the second rejection, want false")
	}
	if !second.Stranded {
		t.Errorf("Stranded = false on the second rejection, want true")
	}
	if second.BeadID != first.BeadID {
		t.Errorf("BeadID = %q, want the existing report %q", second.BeadID, first.BeadID)
	}
	if len(store.created) != 1 {
		t.Errorf("filed %d beads across two rejections, want 1", len(store.created))
	}
	// The re-assign guard: the last assignee written must still be the one the
	// test set, not the witness.
	ups := store.updates[first.BeadID]
	if last := *ups[len(ups)-1].Assignee; last != other {
		t.Errorf("assignee = %q, want %q — a repeat report must not stomp the current owner", last, other)
	}
}

// Wisps, formulas and internal types are not work anyone re-slings, so a
// terminal one strands nothing.
func TestReportStrandedReject_NonWorkSourceIsSkipped(t *testing.T) {
	cases := map[string]*beads.Issue{
		"wisp id":       {ID: "gt-wisp-abc", Title: "wisp", Status: string(beads.StatusClosed)},
		"formula id":    {ID: "mol-polecat-work", Title: "formula", Status: string(beads.StatusClosed)},
		"ephemeral":     {ID: "gt-eph", Title: "eph", Status: string(beads.StatusClosed), Ephemeral: true},
		"internal type": {ID: "gt-agent", Title: "agent", Type: "agent", Status: string(beads.StatusClosed)},
	}
	for name, issue := range cases {
		t.Run(name, func(t *testing.T) {
			store := newFakeStrandedStore()
			store.add(issue)
			req := strandedReq()
			req.SourceIssue = issue.ID

			result := reportStrandedReject(store, notMerged, req)

			if result.SkipReason == "" {
				t.Errorf("SkipReason = %q, want a reason", result.SkipReason)
			}
			if result.Stranded || len(store.created) != 0 {
				t.Errorf("filed a report for a %s source issue", name)
			}
		})
	}
}

// A protected bead is ordinary work that merely must not be auto-closed. This
// path closes nothing, so protection is no reason to leave its stranding
// unreported (gt-zu5n).
func TestReportStrandedReject_ProtectedSourceIsStillReported(t *testing.T) {
	const protectedLabel = "gt:keep"
	if !beads.ProtectedIssueLabel(protectedLabel) {
		t.Fatalf("%q is no longer a protected label — this test is pinned to the wrong vocabulary", protectedLabel)
	}
	protectedIssue := closedWorkIssue("gt-aqk")
	protectedIssue.Labels = []string{protectedLabel}
	// The guard only means anything if the blanket check would have skipped it.
	if beads.ConcreteWorkIssueRejectReason(protectedIssue) == "" {
		t.Fatalf("a %s bead is no longer rejected as non-concrete — the carve-out has nothing to carve", protectedLabel)
	}

	store := newFakeStrandedStore()
	store.add(protectedIssue)

	result := reportStrandedReject(store, notMerged, strandedReq())

	if !result.Stranded {
		t.Fatalf("Stranded = false for a protected work bead (skip: %q)", result.SkipReason)
	}
	if len(store.created) != 1 {
		t.Errorf("filed %d beads, want 1", len(store.created))
	}
}

// An MR with no source issue, or one whose source issue no longer exists, has
// no bead whose state could mislead anyone.
func TestReportStrandedReject_NoResolvableSourceIssue(t *testing.T) {
	for name, sourceIssue := range map[string]string{
		"empty":   "",
		"null":    "null",
		"missing": "gt-gone",
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStrandedStore()
			req := strandedReq()
			req.SourceIssue = sourceIssue

			result := reportStrandedReject(store, notMerged, req)

			if result.Err != nil {
				t.Errorf("Err = %v, want nil", result.Err)
			}
			if result.Stranded || len(store.created) != 0 {
				t.Errorf("filed a report for a %s source issue", name)
			}
		})
	}
}

// A read failure is not a licence to stay quiet about the rejection, but it is
// also not something this path can report as a stranding. It surfaces as an
// error the caller logs, and files nothing it cannot substantiate.
func TestReportStrandedReject_ShowFailureIsReportedAsError(t *testing.T) {
	store := newFakeStrandedStore()
	store.showErr = errors.New("dolt: connection refused")

	result := reportStrandedReject(store, notMerged, strandedReq())

	if result.Err == nil {
		t.Fatalf("Err = nil, want the read failure")
	}
	if result.Stranded || len(store.created) != 0 {
		t.Errorf("filed a report without reading the source issue")
	}
}

// A failed assignment must not lose the report: the bead is filed, and the
// routing failure is surfaced for the caller to log.
func TestReportStrandedReject_AssignFailureKeepsTheBead(t *testing.T) {
	store := newFakeStrandedStore()
	store.add(closedWorkIssue("gt-aqk"))
	store.updateErr = errors.New("bd update: no such assignee")

	result := reportStrandedReject(store, notMerged, strandedReq())

	if result.BeadID == "" || !result.Created {
		t.Fatalf("report was not filed: %+v", result)
	}
	if result.Err == nil {
		t.Errorf("Err = nil, want the assignment failure")
	}
}

// Rig binding is how the report reaches the right witness, not what makes it
// worth having. An unresolvable rig alias must degrade to a report in the
// default database, never to silence — silence is the defect being fixed.
func TestReportStrandedReject_UnresolvableRigStillFiles(t *testing.T) {
	store := newFakeStrandedStore()
	store.add(closedWorkIssue("gt-aqk"))
	store.rigBindErr = errors.New(`unknown repo/rig alias "gastown"`)

	result := reportStrandedReject(store, notMerged, strandedReq())

	if !result.Stranded || result.BeadID == "" {
		t.Fatalf("nothing filed when the rig alias would not resolve: %+v", result)
	}
	if result.Err != nil {
		t.Errorf("Err = %v, want nil — the fallback succeeded", result.Err)
	}
	if result.RigBindErr == nil {
		t.Errorf("RigBindErr = nil, want the binding failure so the caller can say where it landed")
	}
	if len(store.created) != 1 {
		t.Fatalf("filed %d beads, want 1", len(store.created))
	}
	if store.created[0].Rig != "" {
		t.Errorf("fallback create still bound Rig = %q", store.created[0].Rig)
	}
}

// The fallback is a fallback: a rig that resolves must not be silently unbound.
func TestReportStrandedReject_ResolvableRigIsNotUnbound(t *testing.T) {
	store := newFakeStrandedStore()
	store.add(closedWorkIssue("gt-aqk"))

	result := reportStrandedReject(store, notMerged, strandedReq())

	if result.RigBindErr != nil {
		t.Errorf("RigBindErr = %v, want nil", result.RigBindErr)
	}
	if len(store.created) != 1 || store.created[0].Rig != "gastown" {
		t.Errorf("created %+v, want a single create bound to gastown", store.created)
	}
}

func TestStrandedRejectTitleIsStableAndNamesBothIDs(t *testing.T) {
	title := strandedRejectTitle("gt-wisp-0an54", "gt-aqk")
	if title != strandedRejectTitle("gt-wisp-0an54", "gt-aqk") {
		t.Errorf("title is not stable across calls — dedup keys on it")
	}
	if !strings.Contains(title, "gt-wisp-0an54") || !strings.Contains(title, "gt-aqk") {
		t.Errorf("title = %q, want it to name both the MR and the source issue", title)
	}
	if strandedRejectTitle("gt-wisp-0an54", "gt-aqk") == strandedRejectTitle("gt-wisp-0an54", "gt-other") {
		t.Errorf("titles collide across source issues — two strandings would dedup into one")
	}
}

func TestWitnessAddress(t *testing.T) {
	if got := witnessAddress("gastown"); got != "gastown/witness" {
		t.Errorf("witnessAddress(gastown) = %q, want gastown/witness", got)
	}
	if got := witnessAddress("  "); got != "" {
		t.Errorf("witnessAddress(blank) = %q, want empty", got)
	}
}
