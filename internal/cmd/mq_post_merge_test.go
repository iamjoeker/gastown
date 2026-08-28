package cmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/refinery"
)

type fakeMQPostMergeManager struct {
	mr              *refinery.MergeRequest
	findErr         error
	postMergeErr    error
	postMergeCalled bool
	postMergeMR     *refinery.MergeRequest
}

func (m *fakeMQPostMergeManager) FindMRForPostMerge(string) (*refinery.MergeRequest, error) {
	if m.findErr != nil {
		return nil, m.findErr
	}
	return m.mr, nil
}

func (m *fakeMQPostMergeManager) PostMergeMR(mr *refinery.MergeRequest) (*refinery.PostMergeResult, error) {
	m.postMergeCalled = true
	m.postMergeMR = mr
	if m.postMergeErr != nil {
		return nil, m.postMergeErr
	}
	return &refinery.PostMergeResult{MR: m.mr, MRClosed: true, SourceIssueClosed: true, SourceIssueID: m.mr.IssueID}, nil
}

type fakeMQPostMergeGit struct {
	verifyErr    error
	verifyProof  git.MergeProof
	openPR       bool
	openPRNumber int
	prLookupErr  error
	deleteErr    error
	remoteTip    string
	localHead    string
	tipErr       error
	resolveErr   error

	verifiedCommits []string
	resolvedTargets []string
	deletedBranches []string
	deletedHeads    []string
	localDeleted    []string
}

func (g *fakeMQPostMergeGit) VerifyCommitLandedOnPushTarget(_, _, commit string) (git.MergeProof, error) {
	g.verifiedCommits = append(g.verifiedCommits, commit)
	if g.verifyErr != nil {
		return git.MergeProof{}, g.verifyErr
	}
	if g.verifyProof.Method == "" {
		return git.MergeProof{Method: git.MergeProofSHA}, nil
	}
	return g.verifyProof, nil
}

func (g *fakeMQPostMergeGit) CheckOpenPullRequest(git.PullRequestRef) git.PullRequestProtection {
	if g.prLookupErr != nil {
		return git.PullRequestProtection{LookupFailed: true, Err: g.prLookupErr}
	}
	if g.openPR {
		return git.PullRequestProtection{Open: true, PR: &git.PullRequestInfo{Number: g.openPRNumber, State: "OPEN"}}
	}
	return git.PullRequestProtection{}
}

// ResolveMergedBranchDeleteHead mirrors the real resolver's contract: the
// current remote tip wins over the recorded sha, "" means the branch is gone,
// and an error means the tip could not be proved contained in the target.
func (g *fakeMQPostMergeGit) ResolveMergedBranchDeleteHead(_, _, target, recordedHead string) (string, error) {
	g.resolvedTargets = append(g.resolvedTargets, target)
	if g.tipErr != nil {
		return "", g.tipErr
	}
	if g.remoteTip == "" {
		return "", nil
	}
	if g.remoteTip != recordedHead && g.resolveErr != nil {
		return "", g.resolveErr
	}
	return g.remoteTip, nil
}

func (g *fakeMQPostMergeGit) Rev(string) (string, error) {
	return g.localHead, nil
}

func (g *fakeMQPostMergeGit) DeleteRemoteBranchIfAt(_, branch, expectedHash string) error {
	g.deletedBranches = append(g.deletedBranches, branch)
	g.deletedHeads = append(g.deletedHeads, expectedHash)
	return g.deleteErr
}

func (g *fakeMQPostMergeGit) DeleteBranch(branch string, _ bool) error {
	g.localDeleted = append(g.localDeleted, branch)
	return nil
}

func testMQPostMergeMR() *refinery.MergeRequest {
	return &refinery.MergeRequest{
		ID:           "gt-mr-proof",
		Branch:       "polecat/test/gt-proof",
		Worker:       "polecats/test",
		IssueID:      "gt-proof",
		TargetBranch: "main",
		CommitSHA:    "abc123def456",
	}
}

func TestRunVerifiedMQPostMerge_ProofFailurePreservesRecordsAndBranch(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{verifyErr: errors.New("not reachable")}

	_, _, _, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err == nil || !strings.Contains(err.Error(), "merge proof failed") {
		t.Fatalf("runVerifiedMQPostMerge error = %v, want merge proof failure", err)
	}
	if !strings.Contains(err.Error(), mgr.mr.CommitSHA) {
		t.Fatalf("proof error %q does not mention submitted head %s", err, mgr.mr.CommitSHA)
	}
	if mgr.postMergeCalled {
		t.Fatal("PostMerge called after failed proof")
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("remote branch deleted after failed proof: %v", rigGit.deletedBranches)
	}
	if len(rigGit.localDeleted) != 0 {
		t.Fatalf("local branch deleted after failed proof: %v", rigGit.localDeleted)
	}
}

func TestRunVerifiedMQPostMerge_VerifiedHeadClosesAndLeaseDeletes(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{remoteTip: mgr.mr.CommitSHA, localHead: mgr.mr.CommitSHA}

	_, _, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called after successful proof")
	}
	if mgr.postMergeMR != mgr.mr {
		t.Fatal("PostMerge did not use the verified MR snapshot")
	}
	if len(rigGit.verifiedCommits) != 1 || rigGit.verifiedCommits[0] != mgr.mr.CommitSHA {
		t.Fatalf("verified commits = %v, want [%s]", rigGit.verifiedCommits, mgr.mr.CommitSHA)
	}
	if !cleanup.RemoteDeleted || len(rigGit.deletedBranches) != 1 || rigGit.deletedBranches[0] != mgr.mr.Branch {
		t.Fatalf("remote delete = cleanup=%+v branches=%v", cleanup, rigGit.deletedBranches)
	}
	if len(rigGit.deletedHeads) != 1 || rigGit.deletedHeads[0] != mgr.mr.CommitSHA {
		t.Fatalf("deleted heads = %v, want [%s]", rigGit.deletedHeads, mgr.mr.CommitSHA)
	}
	if !cleanup.LocalDeleted || len(rigGit.localDeleted) != 1 || rigGit.localDeleted[0] != mgr.mr.Branch {
		t.Fatalf("local delete = cleanup=%+v local=%v", cleanup, rigGit.localDeleted)
	}
}

func TestRunVerifiedMQPostMerge_SkipBranchDeleteStillRequiresProof(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{}

	_, _, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, true)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called after successful proof")
	}
	if len(rigGit.verifiedCommits) != 1 || rigGit.verifiedCommits[0] != mgr.mr.CommitSHA {
		t.Fatalf("verified commits = %v, want [%s]", rigGit.verifiedCommits, mgr.mr.CommitSHA)
	}
	if !cleanup.Skipped {
		t.Fatalf("cleanup.Skipped = false, cleanup=%+v", cleanup)
	}
	if len(rigGit.deletedBranches) != 0 || len(rigGit.localDeleted) != 0 {
		t.Fatalf("branch deleted despite skip: remote=%v local=%v", rigGit.deletedBranches, rigGit.localDeleted)
	}
}

func TestRunVerifiedMQPostMerge_OpenPRSkipsRemoteDeleteAfterProof(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{openPR: true, localHead: mgr.mr.CommitSHA}

	_, _, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called after successful proof")
	}
	if !cleanup.OpenPR {
		t.Fatalf("cleanup.OpenPR = false, cleanup=%+v", cleanup)
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("remote branch deleted despite open PR: %v", rigGit.deletedBranches)
	}
	if len(rigGit.localDeleted) != 1 || rigGit.localDeleted[0] != mgr.mr.Branch {
		t.Fatalf("local branch cleanup = %v, want [%s]", rigGit.localDeleted, mgr.mr.Branch)
	}
}

func TestRunVerifiedMQPostMerge_LeaseDeleteFailureReturnsAfterPostMerge(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{remoteTip: mgr.mr.CommitSHA, deleteErr: errors.New("stale info")}

	_, _, _, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err == nil || !strings.Contains(err.Error(), "remote branch delete") {
		t.Fatalf("runVerifiedMQPostMerge error = %v, want remote branch delete failure", err)
	}
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called after successful proof")
	}
	if len(rigGit.deletedBranches) != 1 || rigGit.deletedBranches[0] != mgr.mr.Branch {
		t.Fatalf("remote delete attempts = %v, want [%s]", rigGit.deletedBranches, mgr.mr.Branch)
	}
	if len(rigGit.deletedHeads) != 1 || rigGit.deletedHeads[0] != mgr.mr.CommitSHA {
		t.Fatalf("delete lease heads = %v, want [%s]", rigGit.deletedHeads, mgr.mr.CommitSHA)
	}
	if len(rigGit.localDeleted) != 0 {
		t.Fatalf("local branch deleted after remote lease failure: %v", rigGit.localDeleted)
	}
}

func TestRunVerifiedMQPostMerge_MissingRemoteBranchIsIdempotentAfterProof(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{localHead: mgr.mr.CommitSHA}

	_, _, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called after successful proof")
	}
	if !cleanup.AlreadyGone {
		t.Fatalf("cleanup.AlreadyGone = false, cleanup=%+v", cleanup)
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("remote branch delete attempted for missing branch: %v", rigGit.deletedBranches)
	}
}

// A branch refreshed after MR creation — the normal conflict-resolution path,
// where the resolver merges the target INTO the branch — must still be deleted.
// Leasing against the recorded commit_sha made git reject the delete as "stale
// info" and stranded the branch. (gt-yog2)
func TestRunVerifiedMQPostMerge_RefreshedBranchDeletesAtCurrentHead(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	refreshedHead := "8d1adabb99887766"
	rigGit := &fakeMQPostMergeGit{remoteTip: refreshedHead, localHead: refreshedHead}

	_, _, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	if len(rigGit.deletedHeads) != 1 || rigGit.deletedHeads[0] != refreshedHead {
		t.Fatalf("delete lease heads = %v, want [%s] (not the recorded %s)", rigGit.deletedHeads, refreshedHead, mgr.mr.CommitSHA)
	}
	if !cleanup.RemoteDeleted || cleanup.RefreshedHead != refreshedHead {
		t.Fatalf("cleanup = %+v, want RemoteDeleted with RefreshedHead %s", cleanup, refreshedHead)
	}
	if len(rigGit.resolvedTargets) != 1 || rigGit.resolvedTargets[0] != mgr.mr.TargetBranch {
		t.Fatalf("resolve targets = %v, want [%s]", rigGit.resolvedTargets, mgr.mr.TargetBranch)
	}
	if !cleanup.LocalDeleted || len(rigGit.localDeleted) != 1 {
		t.Fatalf("local delete = cleanup=%+v local=%v", cleanup, rigGit.localDeleted)
	}
}

// The ancestry check is the safety property the recorded sha stood in for, so a
// refreshed head that is NOT contained in the target keeps its branch.
func TestRunVerifiedMQPostMerge_RefreshedHeadNotContainedFailsClosed(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{
		remoteTip:  "8d1adabb99887766",
		localHead:  "8d1adabb99887766",
		resolveErr: errors.New("not contained in main"),
	}

	_, _, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err == nil || !strings.Contains(err.Error(), "remote branch delete") {
		t.Fatalf("runVerifiedMQPostMerge error = %v, want remote branch delete failure", err)
	}
	if !strings.Contains(err.Error(), "not contained in main") {
		t.Fatalf("error %q does not name the ancestry failure", err)
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("remote branch deleted despite unverified head: %v", rigGit.deletedBranches)
	}
	if len(rigGit.localDeleted) != 0 {
		t.Fatalf("local branch deleted despite unverified head: %v", rigGit.localDeleted)
	}
	if !cleanup.Attempted {
		t.Fatalf("cleanup.Attempted = false, cleanup=%+v", cleanup)
	}
}

// A branch-delete failure lands after the MR and source issue are already
// closed. The report must say which steps landed and name the one that did not,
// because re-running reports "already closed" and says nothing about the branch.
func TestReportMQPostMerge_PartialCleanupNamesOutstandingStep(t *testing.T) {
	mr := testMQPostMergeMR()
	result := &refinery.PostMergeResult{MR: mr, MRClosed: true, SourceIssueClosed: true, SourceIssueID: mr.IssueID}
	cleanup := mqPostMergeBranchCleanup{Attempted: true, Branch: mr.Branch}

	var out strings.Builder
	err := reportMQPostMerge(&out, result, git.MergeProof{Method: git.MergeProofSHA}, cleanup, errors.New("stale info"))
	if err == nil {
		t.Fatal("reportMQPostMerge returned nil for a failed branch delete")
	}
	for _, want := range []string{mr.ID, mr.Branch, "outstanding"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	text := out.String()
	for _, want := range []string{"MR closed", "Source issue closed", "NOT deleted", "Outstanding", mr.Branch} {
		if !strings.Contains(text, want) {
			t.Errorf("report output missing %q:\n%s", want, text)
		}
	}
}

// A failure BEFORE branch cleanup ran leaves the branch untouched by design, so
// the report must not report it as outstanding work.
func TestReportMQPostMerge_PreCleanupFailureClaimsNoOutstandingBranch(t *testing.T) {
	mr := testMQPostMergeMR()
	result := &refinery.PostMergeResult{MR: mr}

	var out strings.Builder
	err := reportMQPostMerge(&out, result, git.MergeProof{Method: git.MergeProofSHA}, mqPostMergeBranchCleanup{}, errors.New("close failed"))
	if err == nil {
		t.Fatal("reportMQPostMerge returned nil for a failed post-merge")
	}
	if strings.Contains(err.Error(), "outstanding") || strings.Contains(out.String(), "Outstanding") {
		t.Fatalf("pre-cleanup failure reported branch as outstanding: err=%v out=%s", err, out.String())
	}
}

// A PR lookup that never completed must not be reported as a PR that was found.
// The two share a decision — skip the delete — and nothing else. An operator
// told "open PR exists" leaves the branch alone believing a PR is holding it;
// the truth was an HTTP 401 and nothing about the branch was measured. The old
// line also rendered a hardcoded bead reference, "(gas-fk4)", exactly where a
// reader expects a PR number (gt-wbvx).
func TestRunVerifiedMQPostMerge_FailedPRLookupIsNotReportedAsAnOpenPR(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{
		prLookupErr: errors.New("gh pr list head polecat/test/gt-proof failed: HTTP 401: Requires authentication"),
		localHead:   mgr.mr.CommitSHA,
	}

	result, proof, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	if cleanup.OpenPR {
		t.Fatalf("cleanup.OpenPR = true for a lookup that never completed: %+v", cleanup)
	}
	if !cleanup.PRLookupFailed {
		t.Fatalf("cleanup.PRLookupFailed = false, cleanup=%+v", cleanup)
	}
	// Fail-closed is the behaviour that was always correct and must not change.
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("remote branch deleted after an unreadable PR lookup: %v", rigGit.deletedBranches)
	}

	var out strings.Builder
	if err := reportMQPostMerge(&out, result, proof, cleanup, nil); err != nil {
		t.Fatalf("reportMQPostMerge: %v", err)
	}
	text := out.String()
	for _, want := range []string{"PR lookup FAILED", "401", "nothing was verified"} {
		if !strings.Contains(text, want) {
			t.Errorf("report output missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"open PR exists", "gas-fk4"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("report output still claims %q:\n%s", unwanted, text)
		}
	}
}

// The control for the test above, and the reason it is not simply asserting the
// absence of a string: a genuinely open PR must still be reported as one, and
// now names the PR the lookup actually found instead of a bead reference.
func TestRunVerifiedMQPostMerge_OpenPRIsNamedByNumber(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{openPR: true, openPRNumber: 7331, localHead: mgr.mr.CommitSHA}

	result, proof, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	if !cleanup.OpenPR || cleanup.PRLookupFailed {
		t.Fatalf("unexpected cleanup for a found open PR: %+v", cleanup)
	}

	var out strings.Builder
	if err := reportMQPostMerge(&out, result, proof, cleanup, nil); err != nil {
		t.Fatalf("reportMQPostMerge: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "open PR #7331") {
		t.Errorf("report output does not name the open PR:\n%s", text)
	}
	if strings.Contains(text, "gas-fk4") {
		t.Errorf("report output still renders a bead reference where a PR number belongs:\n%s", text)
	}
}

func TestReportMQPostMerge_RefreshedDeleteIsReported(t *testing.T) {
	mr := testMQPostMergeMR()
	result := &refinery.PostMergeResult{MR: mr, MRClosed: true}
	cleanup := mqPostMergeBranchCleanup{
		Attempted:     true,
		Branch:        mr.Branch,
		RemoteDeleted: true,
		RefreshedHead: "8d1adabb99887766",
	}

	var out strings.Builder
	if err := reportMQPostMerge(&out, result, git.MergeProof{Method: git.MergeProofSHA}, cleanup, nil); err != nil {
		t.Fatalf("reportMQPostMerge: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "Deleted remote branch") || !strings.Contains(text, "refreshed") {
		t.Fatalf("report output does not report the refreshed head:\n%s", text)
	}
}

// A rebased landing is proven by content under a rewritten sha. The record has
// to say so: "verified" alone cannot be told apart from plain containment later,
// and the whole reason this path exists is that the two differ (gt-umq0).
func TestRunVerifiedMQPostMerge_RebasedLandingIsProvenAndRecorded(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{
		verifyProof: git.MergeProof{Method: git.MergeProofContent, Evidence: "merge_tree_noop", TargetTip: "9f8e7d6c5b4a3210"},
		remoteTip:   mgr.mr.CommitSHA,
	}

	result, proof, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge on a rebased landing: %v", err)
	}
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called for a content-proven landing")
	}
	if proof.Method != git.MergeProofContent {
		t.Fatalf("proof method = %q, want %q", proof.Method, git.MergeProofContent)
	}

	var out strings.Builder
	if err := reportMQPostMerge(&out, result, proof, cleanup, nil); err != nil {
		t.Fatalf("reportMQPostMerge: %v", err)
	}
	text := out.String()
	for _, want := range []string{"rewritten sha", "rebased landing", "merge_tree_noop", "9f8e7d6c"} {
		if !strings.Contains(text, want) {
			t.Errorf("report output missing %q:\n%s", want, text)
		}
	}
}

// "I could not tell" must not be reported as "this MR is bad": the two want
// different operator responses, and the MR may be perfectly fine (gt-umq0).
func TestRunVerifiedMQPostMerge_UnprovableProofDoesNotBlameTheMR(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{
		verifyErr: fmt.Errorf("%w: commit abc123de is not readable here", git.ErrMergeProofUnprovable),
	}

	_, _, _, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err == nil {
		t.Fatal("runVerifiedMQPostMerge accepted an unprovable landing")
	}
	if !errors.Is(err, git.ErrMergeProofUnprovable) {
		t.Fatalf("error = %v, want it to wrap ErrMergeProofUnprovable", err)
	}
	if strings.Contains(err.Error(), "merge proof failed") {
		t.Fatalf("an unprovable landing was reported as a failed proof: %v", err)
	}
	if !strings.Contains(err.Error(), "could not be established") {
		t.Fatalf("error %q does not say the proof could not be established", err)
	}
	if mgr.postMergeCalled {
		t.Fatal("PostMerge ran despite an unprovable landing")
	}
	if len(rigGit.deletedBranches) != 0 || len(rigGit.localDeleted) != 0 {
		t.Fatalf("branch deleted despite an unprovable landing: remote=%v local=%v", rigGit.deletedBranches, rigGit.localDeleted)
	}
}

func TestRunVerifiedMQPostMerge_MissingSubmittedHeadFailsClosed(t *testing.T) {
	mr := testMQPostMergeMR()
	mr.CommitSHA = ""
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{}

	_, _, _, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err == nil || !strings.Contains(err.Error(), "missing submitted commit_sha") {
		t.Fatalf("runVerifiedMQPostMerge error = %v, want missing submitted head", err)
	}
	if mgr.postMergeCalled {
		t.Fatal("PostMerge called with missing submitted head")
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("branch deleted with missing submitted head: %v", rigGit.deletedBranches)
	}
}

func TestRunVerifiedMQPostMerge_SourceTargetBranchFailsClosed(t *testing.T) {
	mr := testMQPostMergeMR()
	mr.Branch = "main"
	mr.TargetBranch = "main"
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{}

	_, _, _, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
	if err == nil || !strings.Contains(err.Error(), "matches target branch") {
		t.Fatalf("runVerifiedMQPostMerge error = %v, want source/target failure", err)
	}
	if mgr.postMergeCalled {
		t.Fatal("PostMerge called when source branch matched target")
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("branch deleted when source matched target: %v", rigGit.deletedBranches)
	}
}
