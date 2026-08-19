package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	gitpkg "github.com/steveyegge/gastown/internal/git"
)

// mrIssue builds an open, unblocked merge-request bead pointing at a branch.
func mrIssue(id, branch, target string, priority int, createdAt time.Time) *beads.Issue {
	return &beads.Issue{
		ID:          id,
		Status:      "open",
		Priority:    priority,
		Description: fmt.Sprintf("branch: %s\ntarget: %s\nsource_issue: %s\nrig: gastown", branch, target, id),
		CreatedAt:   createdAt.Format(time.RFC3339),
	}
}

func TestSelectNextMRSkipsEmptyBranches(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	// The empty MR is older and so scores higher: without the content check it
	// is exactly what `gt mq next` hands the refinery.
	empty := mrIssue("gt-empty", "polecat/slit/bd-6jp", "main", 2, now.Add(-6*time.Hour))
	real := mrIssue("gt-real", "polecat/Nux/gt-abc", "main", 2, now.Add(-1*time.Hour))

	client := &mockBranchVerifier{
		localBranches: map[string]bool{
			"polecat/slit/bd-6jp": true,
			"polecat/Nux/gt-abc":  true,
			"main":                true,
		},
		ahead: map[string]int{
			"main..polecat/slit/bd-6jp": 0,
			"main..polecat/Nux/gt-abc":  2,
		},
	}

	// Control: with verification off, the empty MR is still the pick — the bug
	// this test guards is not an artifact of the fixture ordering.
	unverified, _, unverifiedSkipped := selectNextMR([]*beads.Issue{empty, real}, false, nil, "priority", now)
	if unverified == nil || unverified.issue.ID != "gt-empty" {
		t.Fatalf("control (verify off): pick = %v, want gt-empty", pickID(unverified))
	}
	if len(unverifiedSkipped) != 0 {
		t.Fatalf("control (verify off): skipped %d MRs, want 0", len(unverifiedSkipped))
	}

	pick, others, skipped := selectNextMR([]*beads.Issue{empty, real}, true, client, "priority", now)
	if pick == nil || pick.issue.ID != "gt-real" {
		t.Fatalf("pick = %v, want gt-real", pickID(pick))
	}
	if pick.state != mrBranchStateOK || pick.ahead != 2 {
		t.Errorf("pick state = %q ahead = %d, want OK/2", pick.state, pick.ahead)
	}
	if others != 0 {
		t.Errorf("others = %d, want 0 (the empty MR is not queued behind the pick)", others)
	}
	if len(skipped) != 1 || skipped[0].issue.ID != "gt-empty" {
		t.Fatalf("skipped = %v, want [gt-empty]", skippedEmptyIDs(skipped))
	}
}

func TestSelectNextMRSkipsEmptyUnderFIFO(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	empty := mrIssue("gt-empty", "polecat/slit/bd-6jp", "main", 2, now.Add(-6*time.Hour))
	real := mrIssue("gt-real", "polecat/Nux/gt-abc", "main", 2, now.Add(-1*time.Hour))

	client := &mockBranchVerifier{
		localBranches: map[string]bool{
			"polecat/slit/bd-6jp": true,
			"polecat/Nux/gt-abc":  true,
			"main":                true,
		},
		ahead: map[string]int{
			"main..polecat/slit/bd-6jp": 0,
			"main..polecat/Nux/gt-abc":  1,
		},
	}

	pick, _, skipped := selectNextMR([]*beads.Issue{empty, real}, true, client, "fifo", now)
	if pick == nil || pick.issue.ID != "gt-real" {
		t.Fatalf("fifo pick = %v, want gt-real", pickID(pick))
	}
	if len(skipped) != 1 {
		t.Fatalf("fifo skipped = %v, want [gt-empty]", skippedEmptyIDs(skipped))
	}
}

// A queue whose only candidate is empty must report nothing to do rather than
// hand the empty one over — and must say why.
func TestSelectNextMRAllEmpty(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	empty := mrIssue("gt-empty", "polecat/slit/bd-6jp", "main", 2, now.Add(-time.Hour))

	client := &mockBranchVerifier{
		localBranches: map[string]bool{"polecat/slit/bd-6jp": true, "main": true},
		ahead:         map[string]int{"main..polecat/slit/bd-6jp": 0},
	}

	pick, others, skipped := selectNextMR([]*beads.Issue{empty}, true, client, "priority", now)
	if pick != nil {
		t.Fatalf("pick = %v, want none", pickID(pick))
	}
	if others != 0 {
		t.Errorf("others = %d, want 0", others)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want [gt-empty]", skippedEmptyIDs(skipped))
	}
}

// MISSING and ERR are reported but never exclude: both are reachable from a
// stale or unreachable refinery worktree, and excluding on them would let a bad
// local checkout drain the queue.
func TestSelectNextMRDoesNotExcludeUnprovenStates(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		client    branchVerifier
		wantState mrBranchState
	}{
		{
			name:      "missing branch is still selectable",
			client:    &mockBranchVerifier{},
			wantState: mrBranchStateMissing,
		},
		{
			name: "unresolvable target is still selectable",
			client: &mockBranchVerifier{
				localBranches: map[string]bool{"polecat/Nux/gt-abc": true},
			},
			wantState: mrBranchStateErr,
		},
		{
			name: "git failure is still selectable",
			client: &mockBranchVerifier{
				localBranches: map[string]bool{"polecat/Nux/gt-abc": true, "main": true},
				aheadErr:      fmt.Errorf("bad revision"),
			},
			wantState: mrBranchStateErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := mrIssue("gt-abc", "polecat/Nux/gt-abc", "main", 2, now.Add(-time.Hour))
			pick, _, skipped := selectNextMR([]*beads.Issue{issue}, true, tt.client, "priority", now)
			if pick == nil || pick.issue.ID != "gt-abc" {
				t.Fatalf("pick = %v, want gt-abc", pickID(pick))
			}
			if pick.state != tt.wantState {
				t.Errorf("state = %q, want %q", pick.state, tt.wantState)
			}
			if len(skipped) != 0 {
				t.Errorf("skipped = %v, want none", skippedEmptyIDs(skipped))
			}
		})
	}
}

// Bead-level unreadiness (blocked, closed, no MR fields) still excludes, and
// those exclusions are not miscounted as empty-branch skips.
func TestSelectNextMRKeepsBeadLevelFiltering(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	blocked := mrIssue("gt-blocked", "polecat/a/gt-1", "main", 0, now.Add(-9*time.Hour))
	blocked.Dependencies = []beads.IssueDep{{ID: "gt-blocker", Status: "open", DependencyType: "blocks"}}
	closed := mrIssue("gt-closed", "polecat/b/gt-2", "main", 0, now.Add(-8*time.Hour))
	closed.Status = "closed"
	fieldless := &beads.Issue{ID: "gt-bare", Status: "open", CreatedAt: now.Add(-7 * time.Hour).Format(time.RFC3339)}
	real := mrIssue("gt-real", "polecat/c/gt-3", "main", 2, now.Add(-time.Hour))

	client := &mockBranchVerifier{
		localBranches: map[string]bool{"polecat/c/gt-3": true, "main": true},
		ahead:         map[string]int{"main..polecat/c/gt-3": 1},
	}

	pick, others, skipped := selectNextMR([]*beads.Issue{blocked, closed, fieldless, real}, true, client, "priority", now)
	if pick == nil || pick.issue.ID != "gt-real" {
		t.Fatalf("pick = %v, want gt-real", pickID(pick))
	}
	if others != 0 {
		t.Errorf("others = %d, want 0", others)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want none (bead-level exclusions are not empty branches)", skippedEmptyIDs(skipped))
	}
}

// The empty-branch case end to end against real git, mirroring gt-d5u's live
// case: a pushed branch that points at main's head.
func TestSelectNextMRAgainstRealGit(t *testing.T) {
	repo := t.TempDir()
	remote := t.TempDir()
	runGitForMQSubmitTest(t, remote, "init", "--bare")

	runGitForMQSubmitTest(t, repo, "init")
	runGitForMQSubmitTest(t, repo, "config", "user.email", "test@example.com")
	runGitForMQSubmitTest(t, repo, "config", "user.name", "Test User")
	runGitForMQSubmitTest(t, repo, "remote", "add", "origin", remote)

	writeMQSubmitTestFile(t, repo, "file.txt", "main\n")
	runGitForMQSubmitTest(t, repo, "add", "file.txt")
	runGitForMQSubmitTest(t, repo, "commit", "-m", "main")
	runGitForMQSubmitTest(t, repo, "branch", "-M", "main")
	runGitForMQSubmitTest(t, repo, "push", "-u", "origin", "main")

	// Pushed but never committed to.
	runGitForMQSubmitTest(t, repo, "checkout", "-b", "polecat/slit/bd-6jp")
	runGitForMQSubmitTest(t, repo, "push", "origin", "polecat/slit/bd-6jp")

	// Real work.
	runGitForMQSubmitTest(t, repo, "checkout", "-b", "polecat/Nux/gt-abc", "main")
	writeMQSubmitTestFile(t, repo, "file.txt", "work\n")
	runGitForMQSubmitTest(t, repo, "commit", "-am", "work")
	runGitForMQSubmitTest(t, repo, "push", "origin", "polecat/Nux/gt-abc")

	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	// P0 and oldest: the empty MR wins every ordering it is allowed into.
	empty := mrIssue("gt-empty", "polecat/slit/bd-6jp", "main", 0, now.Add(-24*time.Hour))
	real := mrIssue("gt-real", "polecat/Nux/gt-abc", "main", 3, now.Add(-time.Minute))

	pick, _, skipped := selectNextMR([]*beads.Issue{empty, real}, true, gitpkg.NewGit(repo), "priority", now)
	if pick == nil || pick.issue.ID != "gt-real" {
		t.Fatalf("pick = %v, want gt-real", pickID(pick))
	}
	if len(skipped) != 1 || skipped[0].issue.ID != "gt-empty" {
		t.Fatalf("skipped = %v, want [gt-empty]", skippedEmptyIDs(skipped))
	}
}

// A skip that says nothing trades a no-op merge for an MR that never gets
// picked and never gets explained. The report must name every skipped ID.
func TestReportSkippedEmptyMRs(t *testing.T) {
	var buf bytes.Buffer
	reportSkippedEmptyMRs(&buf, "gastown", nil)
	if buf.Len() != 0 {
		t.Fatalf("nothing skipped: output = %q, want empty", buf.String())
	}

	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	skipped := []mqNextCandidate{
		{issue: mrIssue("gt-empty1", "polecat/slit/bd-6jp", "main", 2, now), fields: &beads.MRFields{Branch: "polecat/slit/bd-6jp"}},
		{issue: mrIssue("gt-empty2", "polecat/dust/bd-7kq", "main", 2, now), fields: &beads.MRFields{Branch: "polecat/dust/bd-7kq"}},
	}
	reportSkippedEmptyMRs(&buf, "gastown", skipped)
	out := buf.String()
	for _, want := range []string{"gt-empty1", "gt-empty2", "polecat/slit/bd-6jp", "polecat/dust/bd-7kq", "gt mq reject gastown"} {
		if !strings.Contains(out, want) {
			t.Errorf("skip report missing %q:\n%s", want, out)
		}
	}
}

func TestDescribeMQNextGitState(t *testing.T) {
	tests := []struct {
		name      string
		candidate mqNextCandidate
		want      string // substring; "" means no line at all
	}{
		{name: "unverified prints nothing", candidate: mqNextCandidate{state: mrBranchStateSkipped}},
		{name: "ok reports commit count", candidate: mqNextCandidate{state: mrBranchStateOK, ahead: 3}, want: "3 commit"},
		{name: "missing is named", candidate: mqNextCandidate{state: mrBranchStateMissing}, want: "MISSING"},
		{name: "err is named", candidate: mqNextCandidate{state: mrBranchStateErr}, want: "ERR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeMQNextGitState(&tt.candidate)
			if tt.want == "" {
				if got != "" {
					t.Fatalf("got %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("got %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

func pickID(c *mqNextCandidate) string {
	if c == nil {
		return "<none>"
	}
	return c.issue.ID
}
