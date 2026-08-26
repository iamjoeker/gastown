package witness

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
)

// fakeSweepGit records what it was asked and answers from a script.
type fakeSweepGit struct {
	refs    []git.RemoteRef
	refsErr error

	// status is keyed by branch name (without refs/heads/).
	status    map[string]git.BranchPreservationStatus
	statusErr map[string]error

	listedRemote string
	listedPrefix string
	targets      []string
	targetLists  [][]string
}

func (f *fakeSweepGit) ListPushRemoteRefsWithHashes(remote, prefix string) ([]git.RemoteRef, error) {
	f.listedRemote = remote
	f.listedPrefix = prefix
	return f.refs, f.refsErr
}

func (f *fakeSweepGit) PushRemoteRefTargetStatusAny(remote string, ref git.RemoteRef, targets []string) (git.BranchPreservationStatus, error) {
	f.targets = append(f.targets, targets...)
	f.targetLists = append(f.targetLists, append([]string(nil), targets...))
	branch := strings.TrimPrefix(ref.Name, "refs/heads/")
	if err := f.statusErr[branch]; err != nil {
		return git.BranchPreservationStatus{}, err
	}
	status := f.status[branch]
	if status.Preserved && status.ComparisonBase == "" && len(targets) > 0 {
		status.ComparisonBase = targets[0]
	}
	return status, nil
}

// fakeSweepBeads answers Show/ListMergeRequests/Search from maps.
type fakeSweepBeads struct {
	issues   map[string]*beads.Issue
	showErr  map[string]error
	mrs      []*beads.Issue
	mrsErr   error
	search   []*beads.Issue
	searchEr error

	searched []string
}

func (f *fakeSweepBeads) Show(id string) (*beads.Issue, error) {
	if err := f.showErr[id]; err != nil {
		return nil, err
	}
	issue, ok := f.issues[id]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

func (f *fakeSweepBeads) ListMergeRequests(opts beads.ListOptions) ([]*beads.Issue, error) {
	return f.mrs, f.mrsErr
}

func (f *fakeSweepBeads) Search(opts beads.SearchOptions) ([]*beads.Issue, error) {
	f.searched = append(f.searched, opts.Query)
	return f.search, f.searchEr
}

func remoteRef(branch, hash string) git.RemoteRef {
	return git.RemoteRef{Name: "refs/heads/" + branch, Hash: hash}
}

func mrBead(id, status, branch, sourceIssue, closeReason string) *beads.Issue {
	desc := fmt.Sprintf("branch: %s\nsource_issue: %s\n", branch, sourceIssue)
	if closeReason != "" {
		desc += "close_reason: " + closeReason + "\n"
	}
	return &beads.Issue{ID: id, Status: status, Description: desc}
}

func findingFor(t *testing.T, result *BranchSweepResult, branch string) BranchSweepFinding {
	t.Helper()
	for _, f := range result.Findings {
		if f.Branch == branch {
			return f
		}
	}
	t.Fatalf("no finding for %s in %+v", branch, result.Findings)
	return BranchSweepFinding{}
}

func TestSweepClassifiesEachRoute(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/dust/gt-k3v+aaa", "sha-k3v"),
			remoteRef("polecat/guzzle/gt-1jrl+bbb", "sha-1jrl"),
			remoteRef("polecat/crater/gt-dr6t+ccc", "sha-dr6t"),
			remoteRef("polecat/refinery/gt-aqk+ddd", "sha-aqk"),
			remoteRef("polecat/mirelurk/gt-live+eee", "sha-live"),
			remoteRef("polecat/foundation/gt-queued+fff", "sha-queued"),
		},
		status: map[string]git.BranchPreservationStatus{
			// Unmerged: real content not on the target.
			"polecat/dust/gt-k3v+aaa":          {Preserved: false, UnpreservedPatchCount: 2},
			"polecat/guzzle/gt-1jrl+bbb":       {Preserved: false, UnpreservedPatchCount: 1},
			"polecat/crater/gt-dr6t+ccc":       {Preserved: false, UnpreservedPatchCount: 3},
			"polecat/mirelurk/gt-live+eee":     {Preserved: false, UnpreservedPatchCount: 1},
			"polecat/foundation/gt-queued+fff": {Preserved: false, UnpreservedPatchCount: 1},
			// gt-aqk: NOT an ancestor, but the same patches landed via another
			// MR. Ancestry alone would report this as lost work.
			"polecat/refinery/gt-aqk+ddd": {Preserved: true, Evidence: "cherry"},
		},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{
			"gt-k3v":    {ID: "gt-k3v", Status: "closed"},
			"gt-1jrl":   {ID: "gt-1jrl", Status: "closed"},
			"gt-dr6t":   {ID: "gt-dr6t", Status: "closed"},
			"gt-aqk":    {ID: "gt-aqk", Status: "closed"},
			"gt-live":   {ID: "gt-live", Status: "hooked"},
			"gt-queued": {ID: "gt-queued", Status: "closed"},
		},
		mrs: []*beads.Issue{
			// Rejected after the bead had closed.
			mrBead("gt-wisp-mr1", "closed", "polecat/guzzle/gt-1jrl+bbb", "gt-1jrl", "rejected"),
			// Still in the queue: this is not stranded.
			mrBead("gt-wisp-mr2", "open", "polecat/foundation/gt-queued+fff", "gt-queued", ""),
		},
	}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Scanned != 6 {
		t.Fatalf("Scanned = %d, want 6", result.Scanned)
	}
	if !result.MRsMeasured {
		t.Fatalf("MRsMeasured = false with a working MR listing")
	}

	want := map[string]BranchSweepClass{
		"polecat/dust/gt-k3v+aaa":          BranchSweepCheck,  // closed bead, no MR ever
		"polecat/guzzle/gt-1jrl+bbb":       BranchSweepCheck,  // closed bead, rejected MR
		"polecat/crater/gt-dr6t+ccc":       BranchSweepCheck,  // closed bead, collateral
		"polecat/refinery/gt-aqk+ddd":      BranchSweepLanded, // superseded, patches landed
		"polecat/mirelurk/gt-live+eee":     BranchSweepActive, // bead still re-slingable
		"polecat/foundation/gt-queued+fff": BranchSweepQueued, // open MR holds it
	}
	for branch, wantClass := range want {
		if got := findingFor(t, result, branch).Class; got != wantClass {
			t.Errorf("%s classified %q, want %q", branch, got, wantClass)
		}
	}

	if got := result.AttentionCount(); got != 3 {
		t.Errorf("AttentionCount = %d, want 3", got)
	}
}

// A branch whose patches are already on the target must never reach the short
// list. This is the gt-aqk false positive, and reporting it as lost work is how
// the detector gets ignored.
func TestSweepDoesNotFlagSupersededBranch(t *testing.T) {
	for _, evidence := range []string{"ancestor", "merge_tree_noop", "cherry"} {
		t.Run(evidence, func(t *testing.T) {
			g := &fakeSweepGit{
				refs:   []git.RemoteRef{remoteRef("polecat/x/gt-aqk+ddd", "sha")},
				status: map[string]git.BranchPreservationStatus{"polecat/x/gt-aqk+ddd": {Preserved: true, Evidence: evidence}},
			}
			bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"gt-aqk": {ID: "gt-aqk", Status: "closed"}}}

			result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
			if err != nil {
				t.Fatalf("sweep: %v", err)
			}
			f := findingFor(t, result, "polecat/x/gt-aqk+ddd")
			if f.Class != BranchSweepLanded {
				t.Fatalf("class = %q, want landed (evidence %q)", f.Class, evidence)
			}
			if result.AttentionCount() != 0 {
				t.Fatalf("AttentionCount = %d, want 0", result.AttentionCount())
			}
		})
	}
}

// The forward half already files a report for some of these. Re-listing them as
// fresh findings double-counts a decision that is already queued.
func TestSweepDedupsAgainstExistingStrandedReport(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/guzzle/gt-1jrl+bbb", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/guzzle/gt-1jrl+bbb": {Preserved: false, UnpreservedPatchCount: 1}},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{"gt-1jrl": {ID: "gt-1jrl", Status: "closed"}},
		search: []*beads.Issue{
			{ID: "gt-noise", Title: "Something else entirely about gt-1jrl"},
			{ID: "gt-report", Title: beads.StrandedRejectTitle("gt-wisp-mr1", "gt-1jrl")},
		},
	}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f := findingFor(t, result, "polecat/guzzle/gt-1jrl+bbb")
	if f.Class != BranchSweepReported {
		t.Fatalf("class = %q, want reported", f.Class)
	}
	if f.ReportBead != "gt-report" {
		t.Fatalf("ReportBead = %q, want gt-report", f.ReportBead)
	}
	if result.AttentionCount() != 0 {
		t.Fatalf("AttentionCount = %d, want 0 — an already-reported branch is not a new finding", result.AttentionCount())
	}
}

// A report naming a DIFFERENT bead must not suppress this one. The dedup key is
// the source issue, and gt-abc must not be silenced by a report about gt-abcdef.
func TestSweepReportDedupDoesNotMatchOtherBeads(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/x/gt-abc+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/x/gt-abc+aaa": {Preserved: false, UnpreservedPatchCount: 1}},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{"gt-abc": {ID: "gt-abc", Status: "closed"}},
		search: []*beads.Issue{{ID: "gt-other-report", Title: beads.StrandedRejectTitle("gt-wisp-mr9", "gt-abcdef")}},
	}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := findingFor(t, result, "polecat/x/gt-abc+aaa").Class; got != BranchSweepCheck {
		t.Fatalf("class = %q, want check", got)
	}
}

// "I could not tell" must never render as "nothing to see".
func TestSweepReportsUnknownRatherThanClean(t *testing.T) {
	t.Run("git comparison fails", func(t *testing.T) {
		g := &fakeSweepGit{
			refs:      []git.RemoteRef{remoteRef("polecat/x/gt-a+aaa", "sha")},
			statusErr: map[string]error{"polecat/x/gt-a+aaa": errors.New("fetch refused")},
		}
		result, err := SweepUnmergedPolecatBranches(g, &fakeSweepBeads{}, BranchSweepOptions{Targets: []string{"origin/main"}})
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		f := findingFor(t, result, "polecat/x/gt-a+aaa")
		if f.Class != BranchSweepUnknown {
			t.Fatalf("class = %q, want unknown", f.Class)
		}
		if f.Err == "" {
			t.Fatalf("unknown finding carries no error text")
		}
		if result.AttentionCount() != 1 {
			t.Fatalf("AttentionCount = %d, want 1 — unknown is a question, not an all-clear", result.AttentionCount())
		}
	})

	t.Run("bead read fails", func(t *testing.T) {
		g := &fakeSweepGit{
			refs:   []git.RemoteRef{remoteRef("polecat/x/gt-a+aaa", "sha")},
			status: map[string]git.BranchPreservationStatus{"polecat/x/gt-a+aaa": {Preserved: false}},
		}
		bd := &fakeSweepBeads{showErr: map[string]error{"gt-a": errors.New("dolt: connection refused")}}
		result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if got := findingFor(t, result, "polecat/x/gt-a+aaa").Class; got != BranchSweepUnknown {
			t.Fatalf("class = %q, want unknown", got)
		}
	})

	t.Run("no beads handle", func(t *testing.T) {
		g := &fakeSweepGit{
			refs:   []git.RemoteRef{remoteRef("polecat/x/gt-a+aaa", "sha")},
			status: map[string]git.BranchPreservationStatus{"polecat/x/gt-a+aaa": {Preserved: false}},
		}
		result, err := SweepUnmergedPolecatBranches(g, nil, BranchSweepOptions{Targets: []string{"origin/main"}})
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if got := findingFor(t, result, "polecat/x/gt-a+aaa").Class; got != BranchSweepUnknown {
			t.Fatalf("class = %q, want unknown", got)
		}
		if result.MRsMeasured {
			t.Fatalf("MRsMeasured = true without a beads handle")
		}
	})
}

// Every route into `unknown` shares one class, and the human table shows the NOTE
// and not Err — so a note that says only "could not compare" leaves a fetch-level
// fault looking like a property of the branch. That is the whole of gt-880s: a
// clobbered FETCH_HEAD was reported as a branch nobody could classify, and the
// count moved run to run with nothing about the branches changing. The reason has
// to reach the column a reader actually sees.
func TestSweepUnknownNoteCarriesTheReason(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{remoteRef("polecat/x/gt-a+aaa", "sha")},
		statusErr: map[string]error{
			"polecat/x/gt-a+aaa": errors.New("remote tip for refs/heads/polecat/x/gt-a+aaa moved between listing and fetch: listed 89db0051, fetched dd7e98ec (re-run to classify it)"),
		},
	}
	result, err := SweepUnmergedPolecatBranches(g, &fakeSweepBeads{}, BranchSweepOptions{Targets: []string{"origin/main", "upstream/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f := findingFor(t, result, "polecat/x/gt-a+aaa")
	if f.Class != BranchSweepUnknown {
		t.Fatalf("class = %q, want unknown", f.Class)
	}
	for _, want := range []string{"origin/main or upstream/main", "moved between listing and fetch", "89db0051"} {
		if !strings.Contains(f.Note, want) {
			t.Errorf("note %q does not contain %q", f.Note, want)
		}
	}
}

// A missing bead is a finding; an unreadable store is not. The two must not
// collapse into the same answer.
func TestSweepFlagsMissingBeadAsCheck(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/x/gt-gone+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/x/gt-gone+aaa": {Preserved: false}},
	}
	result, err := SweepUnmergedPolecatBranches(g, &fakeSweepBeads{}, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f := findingFor(t, result, "polecat/x/gt-gone+aaa")
	if f.Class != BranchSweepCheck {
		t.Fatalf("class = %q, want check", f.Class)
	}
	if f.Err != "" {
		t.Fatalf("a missing bead is not an error condition, got %q", f.Err)
	}
}

// A failed MR listing must be announced, because every "no MR" under it is
// unmeasured rather than measured.
func TestSweepMarksMRsUnmeasuredWhenListingFails(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/x/gt-a+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/x/gt-a+aaa": {Preserved: false}},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{"gt-a": {ID: "gt-a", Status: "closed"}},
		mrsErr: errors.New("wisps table unreachable"),
	}
	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.MRsMeasured {
		t.Fatalf("MRsMeasured = true after the listing failed")
	}
	if len(result.Errors) == 0 {
		t.Fatalf("a failed MR listing was not reported")
	}
	f := findingFor(t, result, "polecat/x/gt-a+aaa")
	if !strings.Contains(f.Note, "UNMEASURED") {
		t.Fatalf("note %q does not say the MR state was unmeasured", f.Note)
	}
}

// Closed MRs are indexed too: "rejected" and "never submitted" are different
// faults with different fixes, and an open-only view reports the first as the
// second.
func TestSweepDistinguishesRejectedMRFromNoMR(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/a/gt-rej+aaa", "sha1"),
			remoteRef("polecat/b/gt-none+bbb", "sha2"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/a/gt-rej+aaa":  {Preserved: false},
			"polecat/b/gt-none+bbb": {Preserved: false},
		},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{
			"gt-rej":  {ID: "gt-rej", Status: "closed"},
			"gt-none": {ID: "gt-none", Status: "closed"},
		},
		mrs: []*beads.Issue{mrBead("gt-wisp-mr1", "closed", "polecat/a/gt-rej+aaa", "gt-rej", "rejected")},
	}
	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	rejected := findingFor(t, result, "polecat/a/gt-rej+aaa")
	if rejected.MRID != "gt-wisp-mr1" || rejected.MRCloseReason != "rejected" {
		t.Fatalf("rejected MR not carried through: %+v", rejected)
	}
	none := findingFor(t, result, "polecat/b/gt-none+bbb")
	if none.MRID != "" {
		t.Fatalf("branch with no MR reported MR %q", none.MRID)
	}
	if !strings.Contains(none.Note, "no MR was ever created") {
		t.Fatalf("note %q does not distinguish 'never submitted'", none.Note)
	}
}

// An open MR beats a closed one for the same branch: a resubmission supersedes
// the rejection, and reporting the rejection would call queued work stranded.
func TestSweepPrefersOpenMRForBranch(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/a/gt-x+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/a/gt-x+aaa": {Preserved: false}},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{"gt-x": {ID: "gt-x", Status: "closed"}},
		mrs: []*beads.Issue{
			mrBead("gt-wisp-old", "closed", "polecat/a/gt-x+aaa", "gt-x", "rejected"),
			mrBead("gt-wisp-new", "open", "polecat/a/gt-x+aaa", "gt-x", ""),
		},
	}
	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f := findingFor(t, result, "polecat/a/gt-x+aaa")
	if f.Class != BranchSweepQueued || f.MRID != "gt-wisp-new" {
		t.Fatalf("got %+v, want queued on gt-wisp-new", f)
	}
}

// Order matters even when the closed MR is listed second.
func TestSweepPrefersOpenMRRegardlessOfOrder(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/a/gt-x+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/a/gt-x+aaa": {Preserved: false}},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{"gt-x": {ID: "gt-x", Status: "closed"}},
		mrs: []*beads.Issue{
			mrBead("gt-wisp-new", "open", "polecat/a/gt-x+aaa", "gt-x", ""),
			mrBead("gt-wisp-old", "closed", "polecat/a/gt-x+aaa", "gt-x", "rejected"),
		},
	}
	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if f := findingFor(t, result, "polecat/a/gt-x+aaa"); f.MRID != "gt-wisp-new" {
		t.Fatalf("MRID = %q, want gt-wisp-new", f.MRID)
	}
}

// A branch whose name encodes no bead still gets on the short list, via the MR
// if there is one. Silently skipping unparseable branches would hide exactly
// the hand-made cases nobody is watching.
func TestSweepHandlesBranchWithoutParseableBead(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/loose-abc123", "sha1"),
			remoteRef("polecat/viamr-abc123", "sha2"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/loose-abc123": {Preserved: false},
			"polecat/viamr-abc123": {Preserved: false},
		},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{"gt-viamr": {ID: "gt-viamr", Status: "closed"}},
		mrs:    []*beads.Issue{mrBead("gt-wisp-mr1", "closed", "polecat/viamr-abc123", "gt-viamr", "rejected")},
	}
	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	loose := findingFor(t, result, "polecat/loose-abc123")
	if loose.Class != BranchSweepCheck {
		t.Fatalf("class = %q, want check", loose.Class)
	}
	if loose.IssueID != "" {
		t.Fatalf("IssueID = %q, want empty", loose.IssueID)
	}

	viaMR := findingFor(t, result, "polecat/viamr-abc123")
	if viaMR.IssueID != "gt-viamr" {
		t.Fatalf("IssueID = %q, want gt-viamr recovered from the MR", viaMR.IssueID)
	}
	if viaMR.IssueStatus != "closed" {
		t.Fatalf("IssueStatus = %q, want closed", viaMR.IssueStatus)
	}
}

// Split fetch/push remotes are configured on these rigs. Listing the fetch side
// gives a false clean, because the branch was written to the push side.
func TestSweepListsThePushRemote(t *testing.T) {
	g := &fakeSweepGit{}
	if _, err := SweepUnmergedPolecatBranches(g, &fakeSweepBeads{}, BranchSweepOptions{Targets: []string{"origin/main"}}); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if g.listedRemote != "origin" {
		t.Fatalf("listed remote %q, want origin", g.listedRemote)
	}
	if g.listedPrefix != "refs/heads/polecat/" {
		t.Fatalf("listed prefix %q, want refs/heads/polecat/", g.listedPrefix)
	}
}

// A bare branch name resolves ambiguously on a fork-backed rig — to the STALE
// upstream — which reports unmerged branches as landed. Refuse rather than
// guess.
func TestSweepRequiresResolvedTarget(t *testing.T) {
	_, err := SweepUnmergedPolecatBranches(&fakeSweepGit{}, &fakeSweepBeads{}, BranchSweepOptions{})
	if err == nil {
		t.Fatalf("sweep accepted an empty target")
	}
	if !strings.Contains(err.Error(), "target") {
		t.Fatalf("error %q does not name the missing target", err)
	}
}

// A failed listing is not an empty rig. Returning a zero-finding result would
// read as "no unmerged branches".
func TestSweepFailsLoudlyWhenListingFails(t *testing.T) {
	g := &fakeSweepGit{refsErr: errors.New("remote hung up")}
	result, err := SweepUnmergedPolecatBranches(g, &fakeSweepBeads{}, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err == nil {
		t.Fatalf("sweep reported success on a failed listing: %+v", result)
	}
}

// The short list sorts first so a long --all listing does not bury it.
func TestSweepSortsAttentionFirst(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/a/gt-landed+aaa", "sha1"),
			remoteRef("polecat/b/gt-check+bbb", "sha2"),
			remoteRef("polecat/c/gt-queued+ccc", "sha3"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/a/gt-landed+aaa": {Preserved: true, Evidence: "ancestor"},
			"polecat/b/gt-check+bbb":  {Preserved: false},
			"polecat/c/gt-queued+ccc": {Preserved: false},
		},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{
			"gt-landed": {ID: "gt-landed", Status: "closed"},
			"gt-check":  {ID: "gt-check", Status: "closed"},
			"gt-queued": {ID: "gt-queued", Status: "closed"},
		},
		mrs: []*beads.Issue{mrBead("gt-wisp-mr1", "open", "polecat/c/gt-queued+ccc", "gt-queued", "")},
	}
	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Findings[0].Class != BranchSweepCheck {
		t.Fatalf("first finding is %q, want check first: %+v", result.Findings[0].Class, result.Findings)
	}
	if result.Findings[len(result.Findings)-1].Class != BranchSweepLanded {
		t.Fatalf("last finding is %q, want landed last", result.Findings[len(result.Findings)-1].Class)
	}
}

// The comparison must use the ref the caller resolved, not a rewritten one.
func TestSweepComparesAgainstTheGivenTarget(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/a/gt-x+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/a/gt-x+aaa": {Preserved: false}},
	}
	if _, err := SweepUnmergedPolecatBranches(g, &fakeSweepBeads{}, BranchSweepOptions{Targets: []string{"upstream/main"}}); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(g.targets) != 1 || g.targets[0] != "upstream/main" {
		t.Fatalf("compared against %v, want [upstream/main]", g.targets)
	}
}

// Every class carries the bead status, not just the ones whose decision needed
// it. A blank status on a queued or landed row reads as "the bead is gone",
// which is a different and much worse claim than "we did not look".
func TestSweepCarriesBeadStatusForEveryClass(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/a/gt-landed+aaa", "sha1"),
			remoteRef("polecat/b/gt-queued+bbb", "sha2"),
			remoteRef("polecat/c/gt-check+ccc", "sha3"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/a/gt-landed+aaa": {Preserved: true, Evidence: "ancestor"},
			"polecat/b/gt-queued+bbb": {Preserved: false},
			"polecat/c/gt-check+ccc":  {Preserved: false},
		},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{
			"gt-landed": {ID: "gt-landed", Status: "closed"},
			"gt-queued": {ID: "gt-queued", Status: "hooked"},
			"gt-check":  {ID: "gt-check", Status: "closed"},
		},
		mrs: []*beads.Issue{mrBead("gt-wisp-mr1", "open", "polecat/b/gt-queued+bbb", "gt-queued", "")},
	}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	want := map[string]string{
		"polecat/a/gt-landed+aaa": "closed",
		"polecat/b/gt-queued+bbb": "hooked",
		"polecat/c/gt-check+ccc":  "closed",
	}
	for branch, wantStatus := range want {
		f := findingFor(t, result, branch)
		if f.IssueStatus != wantStatus {
			t.Errorf("%s (%s) IssueStatus = %q, want %q", branch, f.Class, f.IssueStatus, wantStatus)
		}
	}
}

// Containment in ANY trunk counts. A rig whose origin is a fork carries two
// refs that are honestly the trunk, and on gastown they differ by 289 commits —
// so checking only one of them puts landed work on the short list.
func TestSweepAcceptsContainmentInAnyTarget(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{remoteRef("polecat/a/gt-x+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{
			// The fake answers for the whole target list at once; the real
			// implementation returns the first target that contains the ref.
			"polecat/a/gt-x+aaa": {Preserved: true, Evidence: "ancestor", ComparisonBase: "origin/main"},
		},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"gt-x": {ID: "gt-x", Status: "closed"}}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets: []string{"upstream/main", "origin/main"},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(g.targetLists) != 1 || len(g.targetLists[0]) != 2 {
		t.Fatalf("git was not asked about both targets: %v", g.targetLists)
	}
	f := findingFor(t, result, "polecat/a/gt-x+aaa")
	if f.Class != BranchSweepLanded {
		t.Fatalf("class = %q, want landed", f.Class)
	}
	if f.ContainedIn != "origin/main" {
		t.Fatalf("ContainedIn = %q, want origin/main — 'landed' is incomplete without saying where", f.ContainedIn)
	}
	if result.Target != "upstream/main" {
		t.Fatalf("Target = %q, want the primary target", result.Target)
	}
	if len(result.Targets) != 2 {
		t.Fatalf("Targets = %v, want both recorded", result.Targets)
	}
}

// Every target must be reported, not just the primary: a reader cannot judge a
// short list without knowing what it was measured against.
func TestSweepRecordsEveryTarget(t *testing.T) {
	g := &fakeSweepGit{}
	result, err := SweepUnmergedPolecatBranches(g, &fakeSweepBeads{}, BranchSweepOptions{
		Targets: []string{"origin/main", "upstream/main"},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(result.Targets) != 2 || result.Targets[0] != "origin/main" || result.Targets[1] != "upstream/main" {
		t.Fatalf("Targets = %v, want both in order", result.Targets)
	}
}

// A landed branch with a live MR is worth saying out loud: the queue is holding
// a slot for content that is already in the target.
func TestSweepNotesOpenMROnLandedBranch(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/a/gt-x+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/a/gt-x+aaa": {Preserved: true, Evidence: "ancestor"}},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{"gt-x": {ID: "gt-x", Status: "closed"}},
		mrs:    []*beads.Issue{mrBead("gt-wisp-mr1", "open", "polecat/a/gt-x+aaa", "gt-x", "")},
	}
	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f := findingFor(t, result, "polecat/a/gt-x+aaa")
	if f.Class != BranchSweepLanded {
		t.Fatalf("class = %q, want landed", f.Class)
	}
	if !strings.Contains(f.Note, "gt-wisp-mr1") {
		t.Fatalf("note %q does not mention the stale open MR", f.Note)
	}
}

// Nothing in the sweep may say work is lost. It cannot tell superseded from
// stranded, and a detector that overclaims gets ignored within a day.
func TestSweepNeverClaimsWorkIsLost(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/a/gt-x+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/a/gt-x+aaa": {Preserved: false, UnpreservedPatchCount: 4}},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"gt-x": {ID: "gt-x", Status: "closed"}}}
	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, f := range result.Findings {
		lower := strings.ToLower(f.Note)
		for _, banned := range []string{"lost", "stranded work", "work is gone"} {
			if strings.Contains(lower, banned) {
				t.Fatalf("note %q overclaims with %q", f.Note, banned)
			}
		}
	}
}

// The sweep must read only. The fakes have no write methods at all, so this
// test documents the constraint at the interface: if a write is ever added to
// BranchSweepBeads or BranchSweepGit, this stops compiling.
func TestSweepInterfacesAreReadOnly(t *testing.T) {
	var _ BranchSweepBeads = (*fakeSweepBeads)(nil)
	var _ BranchSweepGit = (*fakeSweepGit)(nil)

	// The production types must still satisfy them.
	var _ BranchSweepBeads = (*beads.Beads)(nil)
	var _ BranchSweepGit = (*git.Git)(nil)
}

// The two halves of "landed" route to different places, so the sweep must tell
// them apart on the finding itself. Branch hygiene deletes by ancestry alone;
// containment proved any other way is contained AND uncollectable (gt-l65a).
func TestSweepMarksLandedNonAncestorsAsHygieneUnreachable(t *testing.T) {
	cases := []struct {
		evidence        string
		wantUnreachable bool
	}{
		{"ancestor", false},
		{"merge_tree_noop", true},
		{"cherry", true},
		// An evidence string this build does not recognise cannot be assumed to
		// be ancestry. Naming the branch costs a line; assuming hygiene has it
		// is how the row goes uncollected forever.
		{"some_future_proof", true},
		{"", true},
	}
	for _, tc := range cases {
		t.Run("evidence="+tc.evidence, func(t *testing.T) {
			g := &fakeSweepGit{
				refs:   []git.RemoteRef{remoteRef("polecat/x/gt-wz3y+ddd", "sha")},
				status: map[string]git.BranchPreservationStatus{"polecat/x/gt-wz3y+ddd": {Preserved: true, Evidence: tc.evidence}},
			}
			bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"gt-wz3y": {ID: "gt-wz3y", Status: "closed"}}}

			result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
			if err != nil {
				t.Fatalf("sweep: %v", err)
			}
			f := findingFor(t, result, "polecat/x/gt-wz3y+ddd")
			if f.Class != BranchSweepLanded {
				t.Fatalf("class = %q, want landed", f.Class)
			}
			if f.HygieneUnreachable != tc.wantUnreachable {
				t.Fatalf("HygieneUnreachable = %v, want %v (evidence %q)", f.HygieneUnreachable, tc.wantUnreachable, tc.evidence)
			}
			if got := result.HygieneUnreachableCount(); got != boolToInt(tc.wantUnreachable) {
				t.Fatalf("HygieneUnreachableCount = %d, want %d", got, boolToInt(tc.wantUnreachable))
			}
			// The note must carry the consequence, not only the evidence: a
			// reader who sees "landed" and stops reading is the failure mode.
			mentions := strings.Contains(f.Note, "branch hygiene cannot delete it")
			if mentions != tc.wantUnreachable {
				t.Fatalf("note %q mentions hygiene = %v, want %v", f.Note, mentions, tc.wantUnreachable)
			}
		})
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// An unmerged branch is not deletable on any evidence, so the flag must never
// be set outside the landed class. A check row rendered as safe-to-delete would
// destroy the one thing this sweep exists to protect.
func TestSweepNeverMarksUnlandedBranchesDeletable(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/a/gt-check+aaa", "sha-a"),
			remoteRef("polecat/b/gt-live+bbb", "sha-b"),
			remoteRef("polecat/c/gt-broken+ccc", "sha-c"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/a/gt-check+aaa": {Preserved: false, UnpreservedPatchCount: 2},
			"polecat/b/gt-live+bbb":  {Preserved: false, UnpreservedPatchCount: 1},
		},
		statusErr: map[string]error{"polecat/c/gt-broken+ccc": errors.New("tip moved mid-sweep")},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{
		"gt-check": {ID: "gt-check", Status: "closed"},
		"gt-live":  {ID: "gt-live", Status: "hooked"},
	}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, f := range result.Findings {
		if f.HygieneUnreachable {
			t.Fatalf("%s (class %q) marked deletable while not landed", f.Branch, f.Class)
		}
	}
	if got := result.HygieneUnreachableCount(); got != 0 {
		t.Fatalf("HygieneUnreachableCount = %d, want 0", got)
	}
}

// The two totals answer different questions — a decision versus a deletion —
// and folding either into the other loses the branches this bead is about.
func TestHygieneUnreachableCountIsSeparateFromAttention(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/a/gt-check+aaa", "sha-a"),
			remoteRef("polecat/b/gt-anc+bbb", "sha-b"),
			remoteRef("polecat/c/gt-wz3y+ccc", "sha-c"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/a/gt-check+aaa": {Preserved: false, UnpreservedPatchCount: 3},
			"polecat/b/gt-anc+bbb":   {Preserved: true, Evidence: "ancestor"},
			"polecat/c/gt-wz3y+ccc":  {Preserved: true, Evidence: "cherry"},
		},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{
		"gt-check": {ID: "gt-check", Status: "closed"},
		"gt-anc":   {ID: "gt-anc", Status: "closed"},
		"gt-wz3y":  {ID: "gt-wz3y", Status: "closed"},
	}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := result.AttentionCount(); got != 1 {
		t.Fatalf("AttentionCount = %d, want 1 (the check row only)", got)
	}
	if got := result.HygieneUnreachableCount(); got != 1 {
		t.Fatalf("HygieneUnreachableCount = %d, want 1 (the cherry row only)", got)
	}
	if got := result.CountByClass()[BranchSweepLanded]; got != 2 {
		t.Fatalf("landed = %d, want 2 — both halves are still landed", got)
	}
}

// A nil result must answer zero rather than panic: the count is read from
// render paths that also run on a failed sweep.
func TestHygieneUnreachableCountOnNilResult(t *testing.T) {
	var result *BranchSweepResult
	if got := result.HygieneUnreachableCount(); got != 0 {
		t.Fatalf("HygieneUnreachableCount on nil = %d, want 0", got)
	}
}

// --- superseded markers (gt-8xcg) ---------------------------------------
//
// The sweep re-derived the same verdict on the same branches on every cycle
// because a correct answer had nowhere to live: 21 of 36 branches on gastown
// came back unchanged across cycles 1, 6 and 16 of one witness session. These
// tests pin the two halves of the remedy — a marker settles a branch, and a
// marker stops applying the moment the branch moves under it.

func supersededMark(branch, commit, reason string) git.SupersededMark {
	return git.SupersededMark{
		Branch:   branch,
		Commit:   commit,
		Reason:   reason,
		MarkedBy: "gastown/witness",
		MarkedAt: "2026-08-26T06:00:00Z",
	}
}

// The acceptance case from the bead: a check row and a landed-but-not-an-
// ancestor row, both settled, leave a short list of zero and a --deletable list
// of zero — without either branch being deleted or reclassified by guesswork.
func TestSweepSettlesMarkedBranchesAndStopsReportingThem(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/dust/gt-k3v+aaa", "sha-k3v"),
			remoteRef("polecat/refinery/gt-aqk+ddd", "sha-aqk"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/dust/gt-k3v+aaa":     {Preserved: false, UnpreservedPatchCount: 2},
			"polecat/refinery/gt-aqk+ddd": {Preserved: true, Evidence: "cherry"},
		},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{
		"gt-k3v": {ID: "gt-k3v", Status: "closed"},
		"gt-aqk": {ID: "gt-aqk", Status: "closed"},
	}}

	// Measure the same rig with no markers first. Without this the assertions
	// below prove only that two branches are quiet, not that the MARKER is what
	// quieted them — and "quiet" is what a broken sweep produces too.
	before, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("baseline sweep: %v", err)
	}
	if before.AttentionCount() != 1 || before.HygieneUnreachableCount() != 1 {
		t.Fatalf("baseline is not the state this test is about: %d to check, %d unreachable",
			before.AttentionCount(), before.HygieneUnreachableCount())
	}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets: []string{"origin/main"},
		Superseded: map[string]git.SupersededMark{
			"polecat/dust/gt-k3v+aaa":     supersededMark("polecat/dust/gt-k3v+aaa", "sha-k3v", "residual is 2 test files, both since deleted"),
			"polecat/refinery/gt-aqk+ddd": supersededMark("polecat/refinery/gt-aqk+ddd", "sha-aqk", "superseded by gt-u5c; net contribution zero"),
		},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if got := result.AttentionCount(); got != 0 {
		t.Errorf("short list is %d, want 0 — a settled branch is still being reported", got)
	}
	if got := result.HygieneUnreachableCount(); got != 0 {
		t.Errorf("--deletable list is %d, want 0 — a settled branch is still asking to be deleted", got)
	}
	if got := result.SupersededCount(); got != 2 {
		t.Errorf("superseded tally is %d, want 2 — suppressed rows must still be counted", got)
	}
	if got := result.Scanned; got != 2 {
		t.Errorf("scanned %d, want 2 — suppression must not drop branches from the scan", got)
	}
	if got := result.StaleMarkCount(); got != 0 {
		t.Errorf("stale marker tally is %d, want 0", got)
	}

	// The measurement behind each suppressed row survives, so an audit can tell
	// a settled CHECK from a settled landed one.
	check := findingFor(t, result, "polecat/dust/gt-k3v+aaa")
	if check.Class != BranchSweepSuperseded {
		t.Errorf("marked branch is %s, want superseded", check.Class)
	}
	if check.UnderlyingClass != BranchSweepCheck {
		t.Errorf("underlying class is %q, want check — the measurement was destroyed, not routed", check.UnderlyingClass)
	}
	if check.Superseded == nil || !strings.Contains(check.Superseded.Reason, "2 test files") {
		t.Errorf("the derivation did not travel with the finding: %+v", check.Superseded)
	}
	if !strings.Contains(check.Note, "2 test files") {
		t.Errorf("note does not carry the reason, so a reader still cannot see why:\n%s", check.Note)
	}

	landed := findingFor(t, result, "polecat/refinery/gt-aqk+ddd")
	if landed.UnderlyingClass != BranchSweepLanded {
		t.Errorf("underlying class is %q, want landed", landed.UnderlyingClass)
	}
	// The row keeps the measurement even though the count excludes it: hygiene
	// still cannot delete this branch, and that remains true after settling.
	if !landed.HygieneUnreachable {
		t.Error("settling a branch erased the measurement that hygiene cannot reach it")
	}
}

// The failure mode that would make this feature worse than the noise it
// removes: a branch settled once, then pushed to, going on being suppressed
// while genuinely unmerged work sits on it.
func TestSweepDoesNotSettleABranchThatMovedAfterItWasMarked(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{remoteRef("polecat/dust/gt-k3v+aaa", "sha-NEW")},
		status: map[string]git.BranchPreservationStatus{
			"polecat/dust/gt-k3v+aaa": {Preserved: false, UnpreservedPatchCount: 3},
		},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"gt-k3v": {ID: "gt-k3v", Status: "closed"}}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets: []string{"origin/main"},
		Superseded: map[string]git.SupersededMark{
			"polecat/dust/gt-k3v+aaa": supersededMark("polecat/dust/gt-k3v+aaa", "sha-OLD", "settled at the old tip"),
		},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	finding := findingFor(t, result, "polecat/dust/gt-k3v+aaa")
	if finding.Class != BranchSweepCheck {
		t.Fatalf("branch is %s, want check — a stale marker suppressed live work", finding.Class)
	}
	if result.AttentionCount() != 1 {
		t.Errorf("short list is %d, want 1", result.AttentionCount())
	}
	if result.SupersededCount() != 0 {
		t.Errorf("a stale marker was counted as a settlement")
	}
	if !finding.SupersededStale || result.StaleMarkCount() != 1 {
		t.Errorf("the stale marker is invisible: SupersededStale=%v, tally=%d", finding.SupersededStale, result.StaleMarkCount())
	}
	// A marked branch that is still on the list reads as a marker being ignored
	// unless the row says which commit was settled and which is now the tip.
	for _, want := range []string{"sha-OLD", "sha-NEW", "pushed to after it was settled"} {
		if !strings.Contains(finding.Note, want) {
			t.Errorf("note does not explain why the marker did not apply (missing %q):\n%s", want, finding.Note)
		}
	}
	if finding.Superseded != nil {
		t.Error("a stale marker was attached as though it applied")
	}
}

// A marker for a branch nobody else touched must change nothing about any other
// branch — the suppression is per-row, not a mode the sweep enters.
func TestSweepLeavesUnmarkedBranchesAlone(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/dust/gt-k3v+aaa", "sha-k3v"),
			remoteRef("polecat/crater/gt-dr6t+ccc", "sha-dr6t"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/dust/gt-k3v+aaa":    {Preserved: false, UnpreservedPatchCount: 2},
			"polecat/crater/gt-dr6t+ccc": {Preserved: false, UnpreservedPatchCount: 3},
		},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{
		"gt-k3v":  {ID: "gt-k3v", Status: "closed"},
		"gt-dr6t": {ID: "gt-dr6t", Status: "closed"},
	}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets: []string{"origin/main"},
		Superseded: map[string]git.SupersededMark{
			"polecat/dust/gt-k3v+aaa": supersededMark("polecat/dust/gt-k3v+aaa", "sha-k3v", "settled"),
		},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	other := findingFor(t, result, "polecat/crater/gt-dr6t+ccc")
	if other.Class != BranchSweepCheck {
		t.Errorf("an unmarked branch is %s, want check", other.Class)
	}
	if other.Superseded != nil || other.SupersededStale || other.UnderlyingClass != "" {
		t.Errorf("an unmarked branch picked up marker state: %+v", other)
	}
	if result.AttentionCount() != 1 {
		t.Errorf("short list is %d, want 1 — exactly the unmarked branch", result.AttentionCount())
	}
}

// A settled branch sorts last, because it is the row nobody needs to read again.
func TestSweepSortsSettledBranchesLast(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/aaa/gt-1+aaa", "sha-1"),
			remoteRef("polecat/zzz/gt-2+bbb", "sha-2"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/aaa/gt-1+aaa": {Preserved: false, UnpreservedPatchCount: 1},
			"polecat/zzz/gt-2+bbb": {Preserved: false, UnpreservedPatchCount: 1},
		},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{
		"gt-1": {ID: "gt-1", Status: "closed"},
		"gt-2": {ID: "gt-2", Status: "closed"},
	}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets: []string{"origin/main"},
		Superseded: map[string]git.SupersededMark{
			"polecat/aaa/gt-1+aaa": supersededMark("polecat/aaa/gt-1+aaa", "sha-1", "settled"),
		},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Findings[0].Branch != "polecat/zzz/gt-2+bbb" {
		t.Errorf("settled branch did not sort last: %v", []string{result.Findings[0].Branch, result.Findings[1].Branch})
	}
}

// fakeSweepSessions answers session presence from a map, and records who was
// asked so a test can prove the probe RAN rather than inferring it from a
// verdict that several roads produce.
type fakeSweepSessions struct {
	presence map[string]polecat.SessionPresence
	asked    []string
}

func (f *fakeSweepSessions) SessionPresenceFor(name string) polecat.SessionPresence {
	f.asked = append(f.asked, name)
	return f.presence[name]
}

// The gt-6i5d instance, reproduced in its exact shape: dead session, hooked
// bead, pushed tip, no MR. It read "active — still re-slingable" for 23 hours.
func TestSweepStalledOnDeadSessionHoldingHookedBead(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/ace/bd-uzt+mt7w4a5q", "83d8994f2")},
		status: map[string]git.BranchPreservationStatus{"polecat/ace/bd-uzt+mt7w4a5q": {Preserved: false, UnpreservedPatchCount: 67}},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"bd-uzt": {ID: "bd-uzt", Status: "hooked"}}}
	sessions := &fakeSweepSessions{presence: map[string]polecat.SessionPresence{"ace": polecat.SessionAbsent}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets:  []string{"origin/main"},
		Sessions: sessions,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !result.SessionsMeasured {
		t.Fatalf("SessionsMeasured = false with a working probe")
	}
	f := findingFor(t, result, "polecat/ace/bd-uzt+mt7w4a5q")
	if f.Class != BranchSweepStalled {
		t.Fatalf("class = %q, want %q — this is the class the sweep exists to catch", f.Class, BranchSweepStalled)
	}
	if !f.Class.NeedsAttention() {
		t.Fatalf("stalled is not on the short list; the whole finding was that it never showed up")
	}
	if result.AttentionCount() != 1 {
		t.Fatalf("AttentionCount = %d, want 1", result.AttentionCount())
	}
	if f.SessionPresence != branchSessionAbsent {
		t.Fatalf("SessionPresence = %q, want %q", f.SessionPresence, branchSessionAbsent)
	}
	if len(sessions.asked) != 1 || sessions.asked[0] != "ace" {
		t.Fatalf("probe asked %v, want exactly [ace]", sessions.asked)
	}
	for _, want := range []string{"bd-uzt", "hooked", "ace", "GONE", "no MR was ever created", "re-dispatched"} {
		if !strings.Contains(f.Note, want) {
			t.Errorf("note %q is missing %q", f.Note, want)
		}
	}
}

// The other side of the split must be unchanged, and must SAY what it looked at.
// An "active" verdict that does not carry its evidence is the defect, restated.
func TestSweepActiveCarriesLiveSessionEvidence(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/dust/gt-live+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/dust/gt-live+aaa": {Preserved: false, UnpreservedPatchCount: 1}},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"gt-live": {ID: "gt-live", Status: "hooked"}}}
	sessions := &fakeSweepSessions{presence: map[string]polecat.SessionPresence{"dust": polecat.SessionPresent}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets:  []string{"origin/main"},
		Sessions: sessions,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f := findingFor(t, result, "polecat/dust/gt-live+aaa")
	if f.Class != BranchSweepActive {
		t.Fatalf("class = %q, want active — a live session must not be raised", f.Class)
	}
	if result.AttentionCount() != 0 {
		t.Fatalf("AttentionCount = %d, want 0", result.AttentionCount())
	}
	if f.SessionPresence != branchSessionPresent {
		t.Fatalf("SessionPresence = %q, want %q", f.SessionPresence, branchSessionPresent)
	}
	if !strings.Contains(f.Note, "dust") || !strings.Contains(f.Note, "alive") {
		t.Fatalf("active note %q does not name the session it rests on", f.Note)
	}
}

// Unknown must never decide. A probe that could not run leaves the row exactly
// where it was before this fix existed — routing a live polecat to stalled on a
// tmux hiccup is the same defect pointed the other way.
func TestSweepUnknownSessionNeverStalls(t *testing.T) {
	cases := []struct {
		name     string
		sessions BranchSweepSessions
		wantNote string
	}{
		{"no probe at all", nil, "session state for ace UNMEASURED"},
		{"probe errored", &fakeSweepSessions{presence: map[string]polecat.SessionPresence{"ace": polecat.SessionPresenceUnknown}}, "session state for ace UNMEASURED"},
		{"typed nil pointer", (*fakeSweepSessions)(nil), "session state for ace UNMEASURED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &fakeSweepGit{
				refs:   []git.RemoteRef{remoteRef("polecat/ace/bd-uzt+aaa", "sha")},
				status: map[string]git.BranchPreservationStatus{"polecat/ace/bd-uzt+aaa": {Preserved: false, UnpreservedPatchCount: 1}},
			}
			bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"bd-uzt": {ID: "bd-uzt", Status: "hooked"}}}

			result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
				Targets:  []string{"origin/main"},
				Sessions: tc.sessions,
			})
			if err != nil {
				t.Fatalf("sweep: %v", err)
			}
			f := findingFor(t, result, "polecat/ace/bd-uzt+aaa")
			if f.Class != BranchSweepActive {
				t.Fatalf("class = %q, want active — unknown is not evidence the agent is gone", f.Class)
			}
			if f.SessionPresence != branchSessionUnknown {
				t.Fatalf("SessionPresence = %q, want %q", f.SessionPresence, branchSessionUnknown)
			}
			if !strings.Contains(f.Note, tc.wantNote) {
				t.Fatalf("note %q is missing %q", f.Note, tc.wantNote)
			}
		})
	}
}

// A typed nil probe must not panic. It reaches the sweep as a NON-nil interface,
// so the ordinary `!= nil` guard passes and the first method call is on nothing.
func TestSweepTypedNilSessionProbeIsUnmeasured(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/ace/bd-uzt+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/ace/bd-uzt+aaa": {Preserved: false, UnpreservedPatchCount: 1}},
	}
	result, err := SweepUnmergedPolecatBranches(g, &fakeSweepBeads{}, BranchSweepOptions{
		Targets:  []string{"origin/main"},
		Sessions: (*fakeSweepSessions)(nil),
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.SessionsMeasured {
		t.Fatalf("SessionsMeasured = true for a typed nil probe — a probe that cannot answer is not a probe")
	}
}

// A sweep with no session probe must say so. Otherwise "0 stalled" reads as an
// all-clear from a question nobody asked — which is precisely how the 23-hour
// stranding presented.
func TestSweepWithoutSessionProbeSaysSoOutLoud(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/ace/bd-uzt+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/ace/bd-uzt+aaa": {Preserved: false, UnpreservedPatchCount: 1}},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"bd-uzt": {ID: "bd-uzt", Status: "hooked"}}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.SessionsMeasured {
		t.Fatalf("SessionsMeasured = true with no probe supplied")
	}
	if result.CountByClass()[BranchSweepStalled] != 0 {
		t.Fatalf("stalled must be 0 when nothing was asked")
	}
	var said bool
	for _, e := range result.Errors {
		if strings.Contains(e, "session state is UNMEASURED") {
			said = true
		}
	}
	if !said {
		t.Fatalf("a sweep with no session probe did not report it: %v", result.Errors)
	}
}

// A bead nobody holds cannot be stranded by a death. open/blocked/deferred stay
// active whatever the session says — the original reasoning applies to them
// unchanged, and widening the split to cover them would raise every abandoned
// branch on the rig.
func TestSweepUnheldBeadStaysActiveWithDeadSession(t *testing.T) {
	for _, status := range []string{"open", "blocked", "deferred"} {
		t.Run(status, func(t *testing.T) {
			g := &fakeSweepGit{
				refs:   []git.RemoteRef{remoteRef("polecat/ace/gt-x+aaa", "sha")},
				status: map[string]git.BranchPreservationStatus{"polecat/ace/gt-x+aaa": {Preserved: false, UnpreservedPatchCount: 1}},
			}
			bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"gt-x": {ID: "gt-x", Status: status}}}
			sessions := &fakeSweepSessions{presence: map[string]polecat.SessionPresence{"ace": polecat.SessionAbsent}}

			result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
				Targets:  []string{"origin/main"},
				Sessions: sessions,
			})
			if err != nil {
				t.Fatalf("sweep: %v", err)
			}
			f := findingFor(t, result, "polecat/ace/gt-x+aaa")
			if f.Class != BranchSweepActive {
				t.Fatalf("%s bead classified %q, want active — nobody is holding it", status, f.Class)
			}
			if !strings.Contains(f.Note, "not held by it") {
				t.Fatalf("note %q does not explain why an absent session did not raise the row", f.Note)
			}
		})
	}
}

// in_progress is held too. Keying only on "hooked" would be a fix that does not
// fire on half its own class.
func TestSweepStalledCoversInProgress(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/ace/gt-x+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/ace/gt-x+aaa": {Preserved: false, UnpreservedPatchCount: 1}},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"gt-x": {ID: "gt-x", Status: "in_progress"}}}
	sessions := &fakeSweepSessions{presence: map[string]polecat.SessionPresence{"ace": polecat.SessionAbsent}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets:  []string{"origin/main"},
		Sessions: sessions,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := findingFor(t, result, "polecat/ace/gt-x+aaa").Class; got != BranchSweepStalled {
		t.Fatalf("in_progress + dead session classified %q, want stalled", got)
	}
}

// An open MR beats a dead session: the work HAS a route to land, and whoever
// submitted it did so on the polecat's behalf. Ordering matters here — the
// queued test runs before the bead-status switch, and a stalled verdict over a
// queued branch would send an operator to submit an MR that already exists.
func TestSweepQueuedBeatsDeadSession(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/ace/gt-x+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/ace/gt-x+aaa": {Preserved: false, UnpreservedPatchCount: 1}},
	}
	bd := &fakeSweepBeads{
		issues: map[string]*beads.Issue{"gt-x": {ID: "gt-x", Status: "hooked"}},
		mrs:    []*beads.Issue{mrBead("gt-wisp-mr1", "open", "polecat/ace/gt-x+aaa", "gt-x", "")},
	}
	sessions := &fakeSweepSessions{presence: map[string]polecat.SessionPresence{"ace": polecat.SessionAbsent}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets:  []string{"origin/main"},
		Sessions: sessions,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := findingFor(t, result, "polecat/ace/gt-x+aaa").Class; got != BranchSweepQueued {
		t.Fatalf("class = %q, want queued — an open MR is a route to land", got)
	}
	if len(sessions.asked) != 0 {
		t.Fatalf("probe was consulted for a queued branch (%v); it cannot change the verdict there", sessions.asked)
	}
}

// Landed beats everything: content already in the target needs no agent.
func TestSweepLandedBeatsDeadSession(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/ace/gt-x+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/ace/gt-x+aaa": {Preserved: true, Evidence: "ancestor"}},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"gt-x": {ID: "gt-x", Status: "hooked"}}}
	sessions := &fakeSweepSessions{presence: map[string]polecat.SessionPresence{"ace": polecat.SessionAbsent}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets:  []string{"origin/main"},
		Sessions: sessions,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := findingFor(t, result, "polecat/ace/gt-x+aaa").Class; got != BranchSweepLanded {
		t.Fatalf("class = %q, want landed", got)
	}
}

// One probe per polecat, however many branches name it — and the same answer on
// every row. A listing that contradicts itself because a session died mid-sweep
// is worse than one that is a moment stale.
func TestSweepProbesEachPolecatOnce(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/ace/gt-a+aaa", "sha-a"),
			remoteRef("polecat/ace/gt-b+bbb", "sha-b"),
			remoteRef("polecat/ace/gt-c+ccc", "sha-c"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/ace/gt-a+aaa": {Preserved: false, UnpreservedPatchCount: 1},
			"polecat/ace/gt-b+bbb": {Preserved: false, UnpreservedPatchCount: 1},
			"polecat/ace/gt-c+ccc": {Preserved: false, UnpreservedPatchCount: 1},
		},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{
		"gt-a": {ID: "gt-a", Status: "hooked"},
		"gt-b": {ID: "gt-b", Status: "hooked"},
		"gt-c": {ID: "gt-c", Status: "hooked"},
	}}
	sessions := &fakeSweepSessions{presence: map[string]polecat.SessionPresence{"ace": polecat.SessionAbsent}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets:  []string{"origin/main"},
		Sessions: sessions,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(sessions.asked) != 1 {
		t.Fatalf("probe ran %d times for one polecat across 3 branches, want 1 (%v)", len(sessions.asked), sessions.asked)
	}
	if got := result.CountByClass()[BranchSweepStalled]; got != 3 {
		t.Fatalf("stalled = %d, want 3 — the cached answer must apply to every row", got)
	}
}

// Stalled sorts above check and unknown: it is the only short-list class that is
// already settled, and it is the one an operator can act on immediately.
func TestSweepSortsStalledFirst(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/zed/gt-chk+aaa", "sha-1"),
			remoteRef("polecat/ace/gt-hold+bbb", "sha-2"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/zed/gt-chk+aaa":  {Preserved: false, UnpreservedPatchCount: 1},
			"polecat/ace/gt-hold+bbb": {Preserved: false, UnpreservedPatchCount: 1},
		},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{
		"gt-chk":  {ID: "gt-chk", Status: "closed"},
		"gt-hold": {ID: "gt-hold", Status: "hooked"},
	}}
	sessions := &fakeSweepSessions{presence: map[string]polecat.SessionPresence{"ace": polecat.SessionAbsent}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets:  []string{"origin/main"},
		Sessions: sessions,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("Findings = %d, want 2", len(result.Findings))
	}
	if result.Findings[0].Class != BranchSweepStalled {
		t.Fatalf("first row is %q, want stalled", result.Findings[0].Class)
	}
	if result.AttentionCount() != 2 {
		t.Fatalf("AttentionCount = %d, want 2", result.AttentionCount())
	}
}

// SessionPresence must serialise as "unknown", never as an empty string. An
// empty JSON value is indistinguishable from a row written by a build that
// never looked, which is the shape of the defect this field closes.
func TestSweepSerialisesUnknownSessionExplicitly(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/ace/gt-x+aaa", "sha")},
		status: map[string]git.BranchPreservationStatus{"polecat/ace/gt-x+aaa": {Preserved: true, Evidence: "ancestor"}},
	}
	result, err := SweepUnmergedPolecatBranches(g, &fakeSweepBeads{}, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	blob, err := json.Marshal(result.Findings[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"session_presence":"unknown"`) {
		t.Fatalf("finding JSON %s does not carry an explicit unknown", blob)
	}
	if !strings.Contains(string(blob), `"session_presence"`) {
		t.Fatalf("session_presence was omitted entirely from %s", blob)
	}
}

// A marker over a STALLED branch is a composition of two features that landed
// on the same day, and it must not silently swallow the finding.
//
// The marker keeps its authority — it replays a decision somebody wrote down,
// and taking that away would break unmark as the way back — but a marker is a
// claim about the branch's CONTENT, while stalled is a claim about the AGENT
// holding the bead, and the second is not answered by the first. So the verdict
// survives in UnderlyingClass and in the note, where the next reader meets it.
func TestSupersededMarkOverAStalledBranchKeepsTheVerdict(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/ace/bd-uzt+aaa", "sha-uzt")},
		status: map[string]git.BranchPreservationStatus{"polecat/ace/bd-uzt+aaa": {Preserved: false, UnpreservedPatchCount: 1}},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"bd-uzt": {ID: "bd-uzt", Status: "hooked"}}}
	sessions := &fakeSweepSessions{presence: map[string]polecat.SessionPresence{"ace": polecat.SessionAbsent}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets:  []string{"origin/main"},
		Sessions: sessions,
		Superseded: map[string]git.SupersededMark{
			"polecat/ace/bd-uzt+aaa": supersededMark("polecat/ace/bd-uzt+aaa", "sha-uzt", "content landed out of band; residual measured at zero"),
		},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f := findingFor(t, result, "polecat/ace/bd-uzt+aaa")
	if f.Class != BranchSweepSuperseded {
		t.Fatalf("class = %q, want superseded — the marker keeps its authority", f.Class)
	}
	if f.UnderlyingClass != BranchSweepStalled {
		t.Fatalf("UnderlyingClass = %q, want stalled — the verdict must survive the suppression", f.UnderlyingClass)
	}
	if !strings.Contains(f.Note, "would otherwise be stalled") {
		t.Fatalf("note %q does not carry the suppressed verdict", f.Note)
	}
	// The session fact travels too, so a reader can tell WHY it would have been
	// stalled without re-running the probe.
	if f.SessionPresence != branchSessionAbsent {
		t.Fatalf("SessionPresence = %q, want %q", f.SessionPresence, branchSessionAbsent)
	}
}

// A STALE marker must not suppress a stalled row: the settlement was about a
// commit that is no longer the tip, so the branch was pushed to after it was
// settled and the stalled verdict stands.
func TestStaleSupersededMarkDoesNotSuppressStalled(t *testing.T) {
	g := &fakeSweepGit{
		refs:   []git.RemoteRef{remoteRef("polecat/ace/bd-uzt+aaa", "sha-new")},
		status: map[string]git.BranchPreservationStatus{"polecat/ace/bd-uzt+aaa": {Preserved: false, UnpreservedPatchCount: 1}},
	}
	bd := &fakeSweepBeads{issues: map[string]*beads.Issue{"bd-uzt": {ID: "bd-uzt", Status: "hooked"}}}
	sessions := &fakeSweepSessions{presence: map[string]polecat.SessionPresence{"ace": polecat.SessionAbsent}}

	result, err := SweepUnmergedPolecatBranches(g, bd, BranchSweepOptions{
		Targets:  []string{"origin/main"},
		Sessions: sessions,
		Superseded: map[string]git.SupersededMark{
			"polecat/ace/bd-uzt+aaa": supersededMark("polecat/ace/bd-uzt+aaa", "sha-old", "settled at the old tip"),
		},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	f := findingFor(t, result, "polecat/ace/bd-uzt+aaa")
	if f.Class != BranchSweepStalled {
		t.Fatalf("class = %q, want stalled — a stale marker does not apply", f.Class)
	}
	if !f.SupersededStale {
		t.Fatalf("SupersededStale = false; a marker that did not apply must say so")
	}
	if result.AttentionCount() != 1 {
		t.Fatalf("AttentionCount = %d, want 1", result.AttentionCount())
	}
}
