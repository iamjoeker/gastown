package cmd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

type fakeStrandingProbe struct {
	ancestor    bool
	ancestorErr error
	ahead       int
	aheadErr    error

	ancestorCalls [][2]string
	aheadCalls    [][2]string
}

func (f *fakeStrandingProbe) IsAncestor(ancestor, descendant string) (bool, error) {
	f.ancestorCalls = append(f.ancestorCalls, [2]string{ancestor, descendant})
	return f.ancestor, f.ancestorErr
}

func (f *fakeStrandingProbe) CommitsAhead(base, branch string) (int, error) {
	f.aheadCalls = append(f.aheadCalls, [2]string{base, branch})
	return f.ahead, f.aheadErr
}

// The suppression rule is the whole safety property: only an error-free "yes,
// contained" may quiet the report. Every other answer — a definite no, a probe
// error, a missing SHA, a missing target, no git client at all — strands, because
// a false alarm costs a witness one merge-base and silence costs the work.
func TestAssessDoneStrandingOnlyProvenContainmentSuppresses(t *testing.T) {
	tests := []struct {
		name          string
		probe         *fakeStrandingProbe
		baseRef       string
		commitSHA     string
		wantStranded  bool
		wantMerged    bool
		wantProbeErr  bool
		wantAhead     int
		wantAheadOK   bool
		wantNoProbing bool
	}{
		{
			name:       "contained proves the closed bead right",
			probe:      &fakeStrandingProbe{ancestor: true, ahead: 3},
			baseRef:    "origin/main",
			commitSHA:  "abc123",
			wantMerged: true,
		},
		{
			name:         "not contained strands",
			probe:        &fakeStrandingProbe{ancestor: false, ahead: 1},
			baseRef:      "origin/main",
			commitSHA:    "abc123",
			wantStranded: true,
			wantAhead:    1,
			wantAheadOK:  true,
		},
		{
			name:         "probe error strands rather than staying quiet",
			probe:        &fakeStrandingProbe{ancestorErr: errors.New("bad object")},
			baseRef:      "origin/main",
			commitSHA:    "abc123",
			wantStranded: true,
			wantProbeErr: true,
		},
		{
			name:          "missing commit sha strands",
			probe:         &fakeStrandingProbe{ancestor: true},
			baseRef:       "origin/main",
			commitSHA:     "  ",
			wantStranded:  true,
			wantProbeErr:  true,
			wantNoProbing: true,
		},
		{
			name:          "missing target ref strands",
			probe:         &fakeStrandingProbe{ancestor: true},
			baseRef:       "",
			commitSHA:     "abc123",
			wantStranded:  true,
			wantProbeErr:  true,
			wantNoProbing: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := assessDoneStranding(tt.probe, tt.baseRef, "polecat/dust/gt-k3v", tt.commitSHA)
			if st.Stranded != tt.wantStranded {
				t.Errorf("Stranded = %v, want %v", st.Stranded, tt.wantStranded)
			}
			if st.AlreadyMerged != tt.wantMerged {
				t.Errorf("AlreadyMerged = %v, want %v", st.AlreadyMerged, tt.wantMerged)
			}
			if (st.ProbeErr != nil) != tt.wantProbeErr {
				t.Errorf("ProbeErr = %v, want error=%v", st.ProbeErr, tt.wantProbeErr)
			}
			if st.AheadKnown != tt.wantAheadOK || st.Ahead != tt.wantAhead {
				t.Errorf("ahead = %d (known=%v), want %d (known=%v)", st.Ahead, st.AheadKnown, tt.wantAhead, tt.wantAheadOK)
			}
			if tt.wantNoProbing && len(tt.probe.ancestorCalls) != 0 {
				t.Errorf("probed git despite missing inputs: %v", tt.probe.ancestorCalls)
			}
			if st.AlreadyMerged && st.Stranded {
				t.Error("AlreadyMerged and Stranded are mutually exclusive")
			}
		})
	}
}

func TestAssessDoneStrandingNilProbeStrands(t *testing.T) {
	st := assessDoneStranding(nil, "origin/main", "polecat/dust/gt-k3v", "abc123")
	if !st.Stranded {
		t.Fatal("nil git client did not strand — an unanswerable probe must never suppress the report")
	}
	if st.ProbeErr == nil {
		t.Error("nil git client left ProbeErr nil, so the warning cannot say why")
	}
}

// The comparison must be made against the fully qualified target ref, not the
// bare branch name: a bare "main" resolves to a stale local ref in a polecat
// worktree and would report unmerged work as landed.
func TestAssessDoneStrandingComparesAgainstQualifiedRef(t *testing.T) {
	probe := &fakeStrandingProbe{ancestor: false, ahead: 2}
	st := assessDoneStranding(probe, "upstream/main", "polecat/dust/gt-k3v", "abc123")

	if len(probe.ancestorCalls) != 1 {
		t.Fatalf("ancestor probes = %d, want 1", len(probe.ancestorCalls))
	}
	if got := probe.ancestorCalls[0]; got[0] != "abc123" || got[1] != "upstream/main" {
		t.Errorf("IsAncestor(%q, %q), want IsAncestor(\"abc123\", \"upstream/main\")", got[0], got[1])
	}
	if len(probe.aheadCalls) != 1 || probe.aheadCalls[0][0] != "upstream/main" {
		t.Errorf("CommitsAhead calls = %v, want base upstream/main", probe.aheadCalls)
	}
	if st.BaseRef != "upstream/main" {
		t.Errorf("BaseRef = %q, want upstream/main", st.BaseRef)
	}
}

// An unknown commit count must not print as "0 commits ahead" — that reads as
// "no work", the exact conclusion the branch contradicts. A count of zero beside
// a not-contained branch is a contradiction, so it is discarded at the source.
func TestAheadPhraseNeverReportsAnUnknownCountAsZero(t *testing.T) {
	if got := aheadPhrase(doneStranding{AheadKnown: false}); strings.Contains(got, "0") {
		t.Errorf("aheadPhrase with unknown count = %q, must not name a count", got)
	}
	zeroCount := assessDoneStranding(&fakeStrandingProbe{ancestor: false, ahead: 0}, "origin/main", "polecat/dust/gt-k3v", "abc123")
	if zeroCount.AheadKnown {
		t.Errorf("a zero count beside a not-contained branch was kept: %+v", zeroCount)
	}
	if got := aheadPhrase(zeroCount); strings.Contains(got, "0") {
		t.Errorf("aheadPhrase = %q, must not report zero commits for stranded work", got)
	}
	if got := aheadPhrase(doneStranding{Ahead: 1, AheadKnown: true}); got != "1 commit ahead" {
		t.Errorf("aheadPhrase(1) = %q, want %q", got, "1 commit ahead")
	}
	if got := aheadPhrase(doneStranding{Ahead: 4, AheadKnown: true}); got != "4 commits ahead" {
		t.Errorf("aheadPhrase(4) = %q, want %q", got, "4 commits ahead")
	}
}

type fakeStrandedFiler struct {
	created   []beads.CreateOptions
	assigned  map[string]string
	nextID    string
	duplicate *beads.Issue
	// rigErr fails any create bound to a rig, exercising the unbound retry.
	rigErr error
	// createErr fails every create.
	createErr error
	updateErr error
}

func (f *fakeStrandedFiler) CreateIfNoDuplicate(opts beads.CreateOptions) (*beads.Issue, bool, error) {
	if f.createErr != nil {
		return nil, false, f.createErr
	}
	if f.rigErr != nil && strings.TrimSpace(opts.Rig) != "" {
		return nil, false, f.rigErr
	}
	f.created = append(f.created, opts)
	if f.duplicate != nil {
		return f.duplicate, false, nil
	}
	id := f.nextID
	if id == "" {
		id = "gt-report"
	}
	return &beads.Issue{ID: id, Title: opts.Title}, true, nil
}

func (f *fakeStrandedFiler) Update(id string, opts beads.UpdateOptions) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if f.assigned == nil {
		f.assigned = map[string]string{}
	}
	if opts.Assignee != nil {
		f.assigned[id] = *opts.Assignee
	}
	return nil
}

func sampleStrandedRequest() strandedDoneRequest {
	return strandedDoneRequest{
		IssueID:   "gt-k3v",
		Branch:    "polecat/dust/gt-k3v+mszjfjql",
		BaseRef:   "origin/main",
		CommitSHA: "76693419aaaabbbbccccdddd",
		Worker:    "gastown/polecats/dust",
		Rig:       "gastown",
		Refusal:   "source issue gt-k3v is closed — a merge request needs open work to carry",
	}
}

func TestReportStrandedDoneFilesForTheWitness(t *testing.T) {
	filer := &fakeStrandedFiler{nextID: "gt-strand1"}
	report := reportStrandedDone(filer, sampleStrandedRequest())

	if report.Err != nil {
		t.Fatalf("reportStrandedDone: %v", report.Err)
	}
	if report.BeadID != "gt-strand1" || !report.Created {
		t.Fatalf("report = %+v, want a freshly created gt-strand1", report)
	}
	if len(filer.created) != 1 {
		t.Fatalf("creates = %d, want 1", len(filer.created))
	}
	opts := filer.created[0]
	if opts.Rig != "gastown" {
		t.Errorf("Rig = %q, want gastown — an unbound report never reaches the rig's witness", opts.Rig)
	}
	if opts.Priority != 1 {
		t.Errorf("Priority = %d, want 1", opts.Priority)
	}
	if opts.Ephemeral {
		t.Error("report was filed ephemeral; a wisp is reaped and the stranding goes quiet again")
	}
	if filer.assigned["gt-strand1"] != "gastown/witness" {
		t.Errorf("assignee = %q, want gastown/witness", filer.assigned["gt-strand1"])
	}

	// The description has to stand alone: the polecat's sandbox is gone by the
	// time anyone reads it.
	for _, want := range []string{
		"gt-k3v",
		"polecat/dust/gt-k3v+mszjfjql",
		"76693419aaaabbbbccccdddd",
		"origin/main",
		"merge-base --is-ancestor",
		"git cherry",
		"bd update gt-k3v --status=open",
		"--allow-closed-issue",
	} {
		if !strings.Contains(opts.Description, want) {
			t.Errorf("description missing %q:\n%s", want, opts.Description)
		}
	}
}

// A repeated gt done must collapse onto the open report rather than filing a
// second one, and must not re-assign it — whoever picked it up keeps it.
func TestReportStrandedDoneDedupesAndDoesNotStompAssignee(t *testing.T) {
	filer := &fakeStrandedFiler{duplicate: &beads.Issue{ID: "gt-existing"}}
	report := reportStrandedDone(filer, sampleStrandedRequest())

	if report.Err != nil {
		t.Fatalf("reportStrandedDone: %v", report.Err)
	}
	if report.BeadID != "gt-existing" {
		t.Errorf("BeadID = %q, want gt-existing", report.BeadID)
	}
	if report.Created {
		t.Error("Created = true on a duplicate")
	}
	if len(filer.assigned) != 0 {
		t.Errorf("re-assigned an existing report: %v", filer.assigned)
	}
}

// The title is the dedup key, so it must be stable in the issue and branch and
// must not carry anything that varies between runs.
func TestStrandedDoneTitleIsStable(t *testing.T) {
	a := strandedDoneTitle("gt-k3v", "polecat/dust/gt-k3v+mszjfjql")
	b := strandedDoneTitle("gt-k3v", "polecat/dust/gt-k3v+mszjfjql")
	if a != b {
		t.Fatalf("title is not deterministic: %q vs %q", a, b)
	}
	if c := strandedDoneTitle("gt-other", "polecat/dust/gt-k3v+mszjfjql"); c == a {
		t.Error("titles for different issues collide, so two strandings would dedup into one")
	}
}

// An unresolvable rig alias must not turn a work-loss report into a log line.
func TestReportStrandedDoneFallsBackToAnUnboundReport(t *testing.T) {
	filer := &fakeStrandedFiler{nextID: "gt-unbound", rigErr: errors.New("unknown rig")}
	report := reportStrandedDone(filer, sampleStrandedRequest())

	if report.Err != nil {
		t.Fatalf("reportStrandedDone: %v", report.Err)
	}
	if report.BeadID != "gt-unbound" {
		t.Fatalf("BeadID = %q, want the unbound report", report.BeadID)
	}
	if report.RigBindErr == nil {
		t.Error("RigBindErr is nil, so nothing tells the reader the report is in the wrong database")
	}
	if len(filer.created) != 1 || filer.created[0].Rig != "" {
		t.Errorf("retry was not unbound: %+v", filer.created)
	}
}

func TestReportStrandedDoneSurfacesFilingFailure(t *testing.T) {
	filer := &fakeStrandedFiler{createErr: errors.New("dolt down")}
	report := reportStrandedDone(filer, sampleStrandedRequest())
	if report.Err == nil {
		t.Fatal("a failed filing reported no error, so the console would claim a report exists")
	}
	if report.BeadID != "" {
		t.Errorf("BeadID = %q on a failed filing", report.BeadID)
	}

	if r := reportStrandedDone(nil, sampleStrandedRequest()); r.Err == nil {
		t.Error("nil filer reported no error")
	}
}

// The composition is the fix. The individual pieces were all reachable before
// gt-rbul too; what was missing is that the refusal path never asked the second
// question, so these two cases pin probe -> report -> warn end to end.
func TestHandleClosedSourceRefusalReportsAndWarnsOnStrandedWork(t *testing.T) {
	probe := &fakeStrandingProbe{ancestor: false, ahead: 1}
	filer := &fakeStrandedFiler{nextID: "gt-strand1"}
	var out strings.Builder

	note := handleClosedSourceRefusal(probe, filer, sampleStrandedRequest(), &out)

	if len(filer.created) != 1 {
		t.Fatalf("report beads filed = %d, want 1", len(filer.created))
	}
	if filer.assigned["gt-strand1"] != "gastown/witness" {
		t.Errorf("report was not routed to the witness: %v", filer.assigned)
	}
	console := out.String()
	for _, want := range []string{"WARNING", "1 commit ahead", "gt-strand1", "--allow-closed-issue"} {
		if !strings.Contains(console, want) {
			t.Errorf("console missing %q:\n%s", want, console)
		}
	}
	if !strings.Contains(note, "STRANDED WORK") {
		t.Errorf("mail note does not flag the stranding:\n%s", note)
	}
}

func TestHandleClosedSourceRefusalFilesNothingWhenTheWorkLanded(t *testing.T) {
	probe := &fakeStrandingProbe{ancestor: true}
	filer := &fakeStrandedFiler{nextID: "gt-strand1"}
	var out strings.Builder

	note := handleClosedSourceRefusal(probe, filer, sampleStrandedRequest(), &out)

	if len(filer.created) != 0 {
		t.Errorf("filed a report for work that is already in the target: %+v", filer.created)
	}
	if console := out.String(); strings.Contains(console, "WARNING") {
		t.Errorf("warned about a merged branch:\n%s", console)
	}
	if strings.Contains(note, "STRANDED WORK") {
		t.Errorf("mail note alarms on a merged branch:\n%s", note)
	}
}

// The console output is the last thing the polecat sees before its sandbox is
// nuked, so it must carry the recovery commands itself.
func TestDoneStrandingConsoleLinesWarnAndOfferRecovery(t *testing.T) {
	req := sampleStrandedRequest()
	st := doneStranding{BaseRef: "origin/main", Stranded: true, Ahead: 1, AheadKnown: true}
	report := strandedDoneReport{BeadID: "gt-strand1", Created: true}

	out := strings.Join(doneStrandingConsoleLines(req, st, report), "\n")
	for _, want := range []string{
		"WARNING",
		"1 commit ahead",
		"origin/main",
		"will not land",
		"gt-strand1",
		"gastown/witness",
		"bd update gt-k3v --status=open",
		"gt mq submit --branch polecat/dust/gt-k3v+mszjfjql --issue gt-k3v",
		"--allow-closed-issue",
		"git cherry",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("console output missing %q:\n%s", want, out)
		}
	}
}

// The reassuring wording is only earned when containment was proven. Emitting it
// on an unmerged branch is the original bug.
func TestDoneStrandingConsoleLinesStayQuietOnlyWhenMerged(t *testing.T) {
	req := sampleStrandedRequest()
	merged := strings.Join(doneStrandingConsoleLines(req, doneStranding{BaseRef: "origin/main", AlreadyMerged: true}, strandedDoneReport{}), "\n")

	if strings.Contains(merged, "WARNING") {
		t.Errorf("merged branch produced a warning:\n%s", merged)
	}
	if !strings.Contains(merged, "Nothing is stranded") {
		t.Errorf("merged branch did not say the work landed:\n%s", merged)
	}

	stranded := strings.Join(doneStrandingConsoleLines(req, doneStranding{BaseRef: "origin/main", Stranded: true}, strandedDoneReport{BeadID: "gt-x", Created: true}), "\n")
	if strings.Contains(stranded, "Nothing is stranded") {
		t.Errorf("unmerged branch was reported as landed:\n%s", stranded)
	}
}

// A probe that could not answer must say so rather than asserting a fact it did
// not measure.
func TestDoneStrandingConsoleLinesNameAnUnprovableProbe(t *testing.T) {
	req := sampleStrandedRequest()
	st := doneStranding{BaseRef: "origin/main", Stranded: true, ProbeErr: errors.New("bad object")}
	out := strings.Join(doneStrandingConsoleLines(req, st, strandedDoneReport{BeadID: "gt-x", Created: true}), "\n")

	if !strings.Contains(out, "bad object") {
		t.Errorf("probe failure not surfaced:\n%s", out)
	}
	if !strings.Contains(out, "treating the branch as unmerged") {
		t.Errorf("output does not say which way the unknown was resolved:\n%s", out)
	}
}

// When no report bead could be filed, the console must not claim one exists.
func TestDoneStrandingConsoleLinesReportFilingFailure(t *testing.T) {
	req := sampleStrandedRequest()
	st := doneStranding{BaseRef: "origin/main", Stranded: true, AheadKnown: true, Ahead: 2}
	out := strings.Join(doneStrandingConsoleLines(req, st, strandedDoneReport{Err: errors.New("dolt down")}), "\n")

	if !strings.Contains(out, "Could not file a stranded-work report") {
		t.Errorf("filing failure not surfaced:\n%s", out)
	}
	if strings.Contains(out, "Filed ") {
		t.Errorf("claimed a report was filed when none was:\n%s", out)
	}
}

// The witness triages by mail before it opens any bead, so the mail must
// distinguish the two outcomes on its own.
func TestDoneStrandingMailNoteDistinguishesTheOutcomes(t *testing.T) {
	req := sampleStrandedRequest()

	stranded := doneStrandingMailNote(req, doneStranding{BaseRef: "origin/main", Stranded: true, Ahead: 1, AheadKnown: true},
		strandedDoneReport{BeadID: "gt-strand1", Created: true})
	for _, want := range []string{"STRANDED WORK", "polecat/dust/gt-k3v+mszjfjql", "1 commit ahead", "origin/main", "gt-strand1"} {
		if !strings.Contains(stranded, want) {
			t.Errorf("stranded mail note missing %q:\n%s", want, stranded)
		}
	}

	merged := doneStrandingMailNote(req, doneStranding{BaseRef: "origin/main", AlreadyMerged: true}, strandedDoneReport{})
	if strings.Contains(merged, "STRANDED WORK") {
		t.Errorf("merged branch mailed a stranding alarm:\n%s", merged)
	}

	unfiled := doneStrandingMailNote(req, doneStranding{BaseRef: "origin/main", Stranded: true},
		strandedDoneReport{Err: errors.New("dolt down")})
	if !strings.Contains(unfiled, "NO REPORT BEAD WAS FILED") {
		t.Errorf("mail does not say it is the only trace:\n%s", unfiled)
	}
}

// DONE_MR_REFUSED is not one of the ephemeral protocol prefixes, so the mail is
// durable — but the report bead is what a witness can query, and it must survive
// the reaper. Belt and braces: assert the label set the reaper reads.
func TestStrandedDoneReportIsDurableWork(t *testing.T) {
	filer := &fakeStrandedFiler{nextID: "gt-strand1"}
	if r := reportStrandedDone(filer, sampleStrandedRequest()); r.Err != nil {
		t.Fatalf("reportStrandedDone: %v", r.Err)
	}
	opts := filer.created[0]
	if len(opts.Labels) == 0 {
		t.Fatal("report carries no labels")
	}
	if !slices.Contains(opts.Labels, "gt:bug") {
		t.Errorf("labels = %v, want gt:bug", opts.Labels)
	}
	issue := &beads.Issue{ID: "gt-strand1", Labels: opts.Labels, Type: "bug", Title: opts.Title}
	if reason := beads.ConcreteWorkIssueRejectReason(issue); reason != "" {
		t.Errorf("report is not a concrete work issue (%s), so nothing can be slung to fix it", reason)
	}
}
