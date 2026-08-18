package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
)

// mrBead builds a merge-request bead the way gt mq submit writes one: the
// structured fields live as "key: value" lines in the description.
func mrBead(id, status, branch, commitSHA, sourceIssue, closeReason string) *beads.Issue {
	var b strings.Builder
	b.WriteString("branch: " + branch + "\n")
	b.WriteString("target: main\n")
	b.WriteString("source_issue: " + sourceIssue + "\n")
	if commitSHA != "" {
		b.WriteString("commit_sha: " + commitSHA + "\n")
	}
	if closeReason != "" {
		b.WriteString("close_reason: " + closeReason + "\n")
	}
	return &beads.Issue{ID: id, Status: status, Description: b.String()}
}

func TestSelectPriorAttemptPrefersOpenMR(t *testing.T) {
	mrs := []*beads.Issue{
		mrBead("gt-wisp-old", "closed", "polecat/foundation/gt-wlco+aaa", "aaa111", "gt-wlco", "rejected"),
		mrBead("gt-wisp-new", "open", "polecat/chrome/gt-wlco+bbb", "bbb222", "gt-wlco", ""),
		mrBead("gt-wisp-later", "closed", "polecat/nux/gt-wlco+ccc", "ccc333", "gt-wlco", "superseded"),
	}

	got := selectPriorAttempt(mrs)
	if got == nil {
		t.Fatal("expected a prior attempt, got nil")
	}
	if got.MRID != "gt-wisp-new" {
		t.Errorf("MRID = %q, want gt-wisp-new (open MR must win over later closed ones)", got.MRID)
	}
	if !got.Open() {
		t.Error("Open() = false, want true")
	}
}

// The regression that motivated gt-79li: gt-wlco's only MR was closed as
// rejected, so an open-only lookup reported "no prior attempt" and the next
// polecat would have redone 717 lines of committed work.
func TestSelectPriorAttemptFindsClosedMR(t *testing.T) {
	mrs := []*beads.Issue{
		mrBead("gt-wisp-kurv", "closed", "polecat/foundation/gt-wlco+msz8v9e1", "db30de5b", "gt-wlco", "rejected"),
	}

	got := selectPriorAttempt(mrs)
	if got == nil {
		t.Fatal("expected the rejected MR to count as prior work, got nil")
	}
	if got.Branch != "polecat/foundation/gt-wlco+msz8v9e1" {
		t.Errorf("Branch = %q, want the rejected MR's branch", got.Branch)
	}
	if got.Open() {
		t.Error("Open() = true, want false for a closed MR")
	}

	vars := priorAttemptVars(got)
	assertHasVar(t, vars, "prior_branch=polecat/foundation/gt-wlco+msz8v9e1")
	assertHasVar(t, vars, "prior_commit=db30de5b")
	assertHasVar(t, vars, "prior_failure=rejected")
	assertHasVar(t, vars, "prior_status=closed:rejected")
}

func TestSelectPriorAttemptTakesLatestWithinClass(t *testing.T) {
	mrs := []*beads.Issue{
		mrBead("gt-wisp-a", "closed", "polecat/a/gt-x+1", "", "gt-x", "conflict"),
		mrBead("gt-wisp-b", "closed", "polecat/b/gt-x+2", "", "gt-x", "rejected"),
	}
	got := selectPriorAttempt(mrs)
	if got == nil || got.MRID != "gt-wisp-b" {
		t.Fatalf("selectPriorAttempt = %+v, want the last closed MR (gt-wisp-b)", got)
	}
}

func TestSelectPriorAttemptSkipsBranchlessMRs(t *testing.T) {
	mrs := []*beads.Issue{
		{ID: "gt-wisp-empty", Status: "open", Description: "source_issue: gt-x\n"},
	}
	if got := selectPriorAttempt(mrs); got != nil {
		t.Errorf("selectPriorAttempt = %+v, want nil: an MR with no branch carries no recoverable work", got)
	}
	if got := selectPriorAttempt(nil); got != nil {
		t.Errorf("selectPriorAttempt(nil) = %+v, want nil", got)
	}
}

// Merged work is not prior work to build on — it is already in the target
// branch, and handing the polecat that branch would send it to rewrite history
// that has landed.
func TestPriorAttemptVarsSkipsMergedWork(t *testing.T) {
	merged := selectPriorAttempt([]*beads.Issue{
		mrBead("gt-wisp-done", "closed", "polecat/a/gt-x+1", "abc123", "gt-x", "merged"),
	})
	if merged == nil {
		t.Fatal("expected the merged MR to be selected")
	}
	if !merged.Merged() {
		t.Error("Merged() = false, want true")
	}
	if vars := priorAttemptVars(merged); vars != nil {
		t.Errorf("priorAttemptVars = %v, want nil for merged work", vars)
	}
	notice := describePriorAttempt(merged)
	if !strings.Contains(notice, "already MERGED") {
		t.Errorf("describePriorAttempt = %q, want it to flag the merge", notice)
	}
}

func TestPriorAttemptVarsForQueuedWork(t *testing.T) {
	queued := selectPriorAttempt([]*beads.Issue{
		mrBead("gt-wisp-q", "open", "polecat/a/gt-x+1", "abc123", "gt-x", ""),
	})
	vars := priorAttemptVars(queued)
	assertHasVar(t, vars, "prior_status=queued")
	for _, v := range vars {
		if strings.HasPrefix(v, "prior_failure=") {
			t.Errorf("unexpected %q: an open MR has not failed", v)
		}
	}
}

func TestPriorAttemptVarsNilSafe(t *testing.T) {
	if vars := priorAttemptVars(nil); vars != nil {
		t.Errorf("priorAttemptVars(nil) = %v, want nil", vars)
	}
	if notice := describePriorAttempt(nil); notice != "" {
		t.Errorf("describePriorAttempt(nil) = %q, want empty", notice)
	}
}

// The refusal has to be actionable: it must name the MR, the branch, and the
// two ways out, or the reader just reaches for --force.
func TestDuplicateDispatchMessageIsActionable(t *testing.T) {
	p := &priorAttempt{
		MRID:      "gt-wisp-kurv",
		MRStatus:  "open",
		Branch:    "polecat/foundation/gt-wlco+msz8v9e1",
		CommitSHA: "db30de5b652116554722155c00906147580d172d",
	}
	msg := duplicateDispatchMessage("gt-wlco", p)
	for _, want := range []string{
		"gt-wlco",
		"gt-wisp-kurv",
		"polecat/foundation/gt-wlco+msz8v9e1",
		"db30de5b652116554722155c00906147580d172d",
		"gt mq status gt-wisp-kurv",
		"--force",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("duplicateDispatchMessage missing %q:\n%s", want, msg)
		}
	}
}

// The branch fallback: MR wisps get reaped, so "no MR bead" is not evidence
// that nothing was pushed. gt-wlco's branch outlived both its bead close and
// its MR rejection.
func TestSelectPriorBranchMatchesIssue(t *testing.T) {
	refs := []git.RemoteRef{
		{Hash: "aaa", Name: "refs/heads/polecat/chrome/gt-p2e7+msz92bvc"},
		{Hash: "db30de5b652116554722155c00906147580d172d", Name: "refs/heads/polecat/foundation/gt-wlco+msz8v9e1"},
		{Hash: "ccc", Name: "refs/heads/polecat/nux/gt-other+xyz"},
	}

	got := selectPriorBranch(refs, "gt-wlco")
	if got == nil {
		t.Fatal("expected the pushed branch to be found, got nil")
	}
	if got.Branch != "polecat/foundation/gt-wlco+msz8v9e1" {
		t.Errorf("Branch = %q, want polecat/foundation/gt-wlco+msz8v9e1", got.Branch)
	}
	if got.CommitSHA != "db30de5b652116554722155c00906147580d172d" {
		t.Errorf("CommitSHA = %q, want the ref hash", got.CommitSHA)
	}
	if got.Source != priorSourceBranch {
		t.Errorf("Source = %q, want %q", got.Source, priorSourceBranch)
	}
}

// A bare branch must never block dispatch: nothing is holding a merge slot for
// it, so refusing would strand the bead permanently.
func TestPriorBranchDoesNotBlockDispatch(t *testing.T) {
	p := selectPriorBranch(
		[]git.RemoteRef{{Hash: "abc123", Name: "refs/heads/polecat/foundation/gt-wlco+msz8v9e1"}},
		"gt-wlco",
	)
	if p.Open() {
		t.Error("Open() = true for a branch with no MR; that would refuse dispatch and strand the bead")
	}
	if label := priorAttemptStatusLabel(p); label != "unqueued-branch" {
		t.Errorf("priorAttemptStatusLabel = %q, want unqueued-branch", label)
	}
	assertHasVar(t, priorAttemptVars(p), "prior_branch=polecat/foundation/gt-wlco+msz8v9e1")
	assertHasVar(t, priorAttemptVars(p), "prior_status=unqueued-branch")
	if notice := describePriorAttempt(p); !strings.Contains(notice, "no merge request") {
		t.Errorf("describePriorAttempt = %q, want it to say there is no merge request", notice)
	}
}

func TestSelectPriorBranchIgnoresOtherIssues(t *testing.T) {
	refs := []git.RemoteRef{
		{Hash: "aaa", Name: "refs/heads/polecat/chrome/gt-p2e7+msz92bvc"},
		{Hash: "bbb", Name: "refs/heads/main"},
		{Hash: "ccc", Name: "refs/heads/polecat/nux-abc123"}, // no issue encoded
	}
	if got := selectPriorBranch(refs, "gt-wlco"); got != nil {
		t.Errorf("selectPriorBranch = %+v, want nil", got)
	}
}

// "gt-abc" must not match a branch for "gt-abcdef" — the same partial-ID trap
// MatchesMRSourceIssue guards against on the MR side.
func TestSelectPriorBranchRejectsPartialIDMatch(t *testing.T) {
	refs := []git.RemoteRef{
		{Hash: "aaa", Name: "refs/heads/polecat/nux/gt-abcdef+xyz"},
	}
	if got := selectPriorBranch(refs, "gt-abc"); got != nil {
		t.Errorf("selectPriorBranch matched a partial ID: %+v", got)
	}
}

func assertHasVar(t *testing.T, vars []string, want string) {
	t.Helper()
	for _, v := range vars {
		if v == want {
			return
		}
	}
	t.Errorf("vars %v missing %q", vars, want)
}
