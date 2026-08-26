package cmd

import (
	"errors"
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
	verifyErr  error
	openPR     bool
	deleteErr  error
	remoteTip  string
	localHead  string
	tipErr     error
	resolveErr error

	verifiedCommits []string
	resolvedTargets []string
	deletedBranches []string
	deletedHeads    []string
	localDeleted    []string
}

func (g *fakeMQPostMergeGit) VerifyPushedCommitReachableFromPushTarget(_, _, commit string) error {
	g.verifiedCommits = append(g.verifiedCommits, commit)
	return g.verifyErr
}

func (g *fakeMQPostMergeGit) HasOpenPullRequest(git.PullRequestRef) bool {
	return g.openPR
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

	_, _, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
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

	_, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
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

	_, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, true)
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

	_, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
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

	_, _, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
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

	_, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
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

	_, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
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

	_, cleanup, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
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
	err := reportMQPostMerge(&out, result, cleanup, errors.New("stale info"))
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
	err := reportMQPostMerge(&out, result, mqPostMergeBranchCleanup{}, errors.New("close failed"))
	if err == nil {
		t.Fatal("reportMQPostMerge returned nil for a failed post-merge")
	}
	if strings.Contains(err.Error(), "outstanding") || strings.Contains(out.String(), "Outstanding") {
		t.Fatalf("pre-cleanup failure reported branch as outstanding: err=%v out=%s", err, out.String())
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
	if err := reportMQPostMerge(&out, result, cleanup, nil); err != nil {
		t.Fatalf("reportMQPostMerge: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "Deleted remote branch") || !strings.Contains(text, "refreshed") {
		t.Fatalf("report output does not report the refreshed head:\n%s", text)
	}
}

func TestRunVerifiedMQPostMerge_MissingSubmittedHeadFailsClosed(t *testing.T) {
	mr := testMQPostMergeMR()
	mr.CommitSHA = ""
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{}

	_, _, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
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

	_, _, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false)
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
