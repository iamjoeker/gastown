package cmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	gitpkg "github.com/steveyegge/gastown/internal/git"
)

// mockBranchVerifier implements branchVerifier for testing.
type mockBranchVerifier struct {
	localBranches  map[string]bool
	remoteBranches map[string]bool
	ahead          map[string]int // "base..branch" -> commit count
	localErr       error
	remoteErr      error
	aheadErr       error
}

func (m *mockBranchVerifier) BranchExists(branch string) (bool, error) {
	if m.localErr != nil {
		return false, m.localErr
	}
	return m.localBranches[branch], nil
}

func (m *mockBranchVerifier) RemoteTrackingBranchExists(remote, branch string) (bool, error) {
	if m.remoteErr != nil {
		return false, m.remoteErr
	}
	key := remote + "/" + branch
	return m.remoteBranches[key], nil
}

func (m *mockBranchVerifier) CommitsAhead(base, branch string) (int, error) {
	if m.aheadErr != nil {
		return 0, m.aheadErr
	}
	return m.ahead[base+".."+branch], nil
}

func TestVerifyBranch(t *testing.T) {
	tests := []struct {
		name      string
		verify    bool
		client    branchVerifier
		fields    *beads.MRFields
		wantState mrBranchState
		wantAhead int
	}{
		{
			name:      "verify disabled",
			verify:    false,
			client:    &mockBranchVerifier{},
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc"},
			wantState: mrBranchStateSkipped,
		},
		{
			name:      "nil client",
			verify:    true,
			client:    nil,
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc"},
			wantState: mrBranchStateSkipped,
		},
		{
			name:      "empty branch",
			verify:    true,
			client:    &mockBranchVerifier{},
			fields:    &beads.MRFields{Branch: ""},
			wantState: mrBranchStateSkipped,
		},
		{
			name:   "local branch with commits",
			verify: true,
			client: &mockBranchVerifier{
				localBranches: map[string]bool{"polecat/Nux/gt-abc": true, "main": true},
				ahead:         map[string]int{"main..polecat/Nux/gt-abc": 3},
			},
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc", Target: "main"},
			wantState: mrBranchStatePresent,
			wantAhead: 3,
		},
		{
			name:   "remote-only branch with commits",
			verify: true,
			client: &mockBranchVerifier{
				localBranches: map[string]bool{},
				remoteBranches: map[string]bool{
					"origin/polecat/Nux/gt-abc": true,
					"origin/main":               true,
				},
				ahead: map[string]int{"origin/main..origin/polecat/Nux/gt-abc": 1},
			},
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc", Target: "main"},
			wantState: mrBranchStatePresent,
			wantAhead: 1,
		},
		{
			name:   "remote ref preferred over stale local ref",
			verify: true,
			client: &mockBranchVerifier{
				localBranches: map[string]bool{"polecat/Nux/gt-abc": true, "main": true},
				remoteBranches: map[string]bool{
					"origin/polecat/Nux/gt-abc": true,
					"origin/main":               true,
				},
				// The stale local ref would claim work exists; the pushed ref is empty.
				ahead: map[string]int{
					"main..polecat/Nux/gt-abc":               4,
					"origin/main..origin/polecat/Nux/gt-abc": 0,
				},
			},
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc", Target: "main"},
			wantState: mrBranchStateEmpty,
		},
		{
			name:   "branch exists but carries no commits",
			verify: true,
			client: &mockBranchVerifier{
				localBranches: map[string]bool{"polecat/slit/bd-6jp": true, "main": true},
				ahead:         map[string]int{"main..polecat/slit/bd-6jp": 0},
			},
			fields:    &beads.MRFields{Branch: "polecat/slit/bd-6jp", Target: "main"},
			wantState: mrBranchStateEmpty,
		},
		{
			name:   "unset target defaults to main",
			verify: true,
			client: &mockBranchVerifier{
				localBranches: map[string]bool{"polecat/Nux/gt-abc": true, "main": true},
				ahead:         map[string]int{"main..polecat/Nux/gt-abc": 0},
			},
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc"},
			wantState: mrBranchStateEmpty,
		},
		{
			name:   "integration target branch",
			verify: true,
			client: &mockBranchVerifier{
				localBranches: map[string]bool{
					"polecat/Nux/gt-abc":    true,
					"integration/auth-epic": true,
				},
				ahead: map[string]int{"integration/auth-epic..polecat/Nux/gt-abc": 2},
			},
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc", Target: "integration/auth-epic"},
			wantState: mrBranchStatePresent,
			wantAhead: 2,
		},
		{
			name:   "both refs missing",
			verify: true,
			client: &mockBranchVerifier{
				localBranches:  map[string]bool{},
				remoteBranches: map[string]bool{},
			},
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc", Target: "main"},
			wantState: mrBranchStateMissing,
		},
		{
			name:   "unresolvable target is not reported as OK",
			verify: true,
			client: &mockBranchVerifier{
				localBranches: map[string]bool{"polecat/Nux/gt-abc": true},
			},
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc", Target: "integration/gone"},
			wantState: mrBranchStateErr,
		},
		{
			name:   "local check errors",
			verify: true,
			client: &mockBranchVerifier{
				localErr: errors.New("permission denied"),
			},
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc", Target: "main"},
			wantState: mrBranchStateErr,
		},
		{
			name:   "remote check errors",
			verify: true,
			client: &mockBranchVerifier{
				localBranches: map[string]bool{},
				remoteErr:     errors.New("corrupt repo"),
			},
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc", Target: "main"},
			wantState: mrBranchStateErr,
		},
		{
			name:   "commit count errors",
			verify: true,
			client: &mockBranchVerifier{
				localBranches: map[string]bool{"polecat/Nux/gt-abc": true, "main": true},
				aheadErr:      errors.New("bad revision"),
			},
			fields:    &beads.MRFields{Branch: "polecat/Nux/gt-abc", Target: "main"},
			wantState: mrBranchStateErr,
		},
		{
			name:      "nil fields",
			verify:    true,
			client:    &mockBranchVerifier{},
			fields:    nil,
			wantState: mrBranchStateSkipped,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotState, gotAhead := verifyBranch(tt.verify, tt.client, tt.fields)
			if gotState != tt.wantState {
				t.Errorf("verifyBranch() state = %q, want %q", gotState, tt.wantState)
			}
			if gotAhead != tt.wantAhead {
				t.Errorf("verifyBranch() commitsAhead = %d, want %d", gotAhead, tt.wantAhead)
			}
		})
	}
}

// TestVerifyBranchAgainstRealGit reproduces the live case from gt-d5u: a pushed
// branch that points at the target's own head. Branch existence alone reports it
// as mergeable; the content check must call it EMPTY.
func TestVerifyBranchAgainstRealGit(t *testing.T) {
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

	// A polecat that correctly made no commits: the branch exists and is pushed,
	// but points at main's head.
	runGitForMQSubmitTest(t, repo, "checkout", "-b", "polecat/slit/bd-6jp")
	runGitForMQSubmitTest(t, repo, "push", "origin", "polecat/slit/bd-6jp")

	// A polecat with real work.
	runGitForMQSubmitTest(t, repo, "checkout", "-b", "polecat/Nux/gt-abc", "main")
	writeMQSubmitTestFile(t, repo, "file.txt", "work\n")
	runGitForMQSubmitTest(t, repo, "commit", "-am", "work")
	runGitForMQSubmitTest(t, repo, "push", "origin", "polecat/Nux/gt-abc")

	g := gitpkg.NewGit(repo)

	state, ahead := verifyBranch(true, g, &beads.MRFields{Branch: "polecat/slit/bd-6jp", Target: "main"})
	if state != mrBranchStateEmpty {
		t.Errorf("zero-commit branch: state = %q, want %q", state, mrBranchStateEmpty)
	}
	if ahead != 0 {
		t.Errorf("zero-commit branch: commitsAhead = %d, want 0", ahead)
	}

	state, ahead = verifyBranch(true, g, &beads.MRFields{Branch: "polecat/Nux/gt-abc", Target: "main"})
	if state != mrBranchStatePresent {
		t.Errorf("branch with work: state = %q, want %q", state, mrBranchStatePresent)
	}
	if ahead != 1 {
		t.Errorf("branch with work: commitsAhead = %d, want 1", ahead)
	}

	state, _ = verifyBranch(true, g, &beads.MRFields{Branch: "polecat/gone/gt-xyz", Target: "main"})
	if state != mrBranchStateMissing {
		t.Errorf("deleted branch: state = %q, want %q", state, mrBranchStateMissing)
	}
}

// mockMergeRehearser implements mergeRehearser for testing.
type mockMergeRehearser struct {
	conflicts map[string][]string // "base..branch" -> conflicted paths
	err       error
	calls     int
}

func (m *mockMergeRehearser) MergeConflicts(base, branch string) ([]string, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.conflicts[base+".."+branch], nil
}

func TestRehearseMerge(t *testing.T) {
	present := mrVerification{
		state:        mrBranchStatePresent,
		commitsAhead: 700,
		branchRef:    "origin/polecat/deathclaw/gt-x",
		targetRef:    "origin/main",
	}

	tests := []struct {
		name          string
		check         bool
		client        mergeRehearser
		verification  mrVerification
		wantState     mrMergeState
		wantConflicts int
		wantCalls     int
	}{
		{
			name:         "merge check disabled",
			check:        false,
			client:       &mockMergeRehearser{},
			verification: present,
			wantState:    mrMergeStateSkipped,
		},
		{
			name:         "nil client",
			check:        true,
			client:       nil,
			verification: present,
			wantState:    mrMergeStateSkipped,
		},
		{
			name:          "clean merge",
			check:         true,
			client:        &mockMergeRehearser{},
			verification:  present,
			wantState:     mrMergeStateClean,
			wantConflicts: 0,
			wantCalls:     1,
		},
		{
			// The measured case from gt-0w2l: PRESENT, 700 commits ahead, and
			// unmergeable. Existence and mergeability disagreeing is the whole
			// point of this check, so the fixture must exhibit it.
			name:  "present branch that conflicts",
			check: true,
			client: &mockMergeRehearser{conflicts: map[string][]string{
				"origin/main..origin/polecat/deathclaw/gt-x": conflictedPathsFixture(17),
			}},
			verification:  present,
			wantState:     mrMergeStateConflicts,
			wantConflicts: 17,
			wantCalls:     1,
		},
		{
			name:          "git failure is ERR, never CLEAN",
			check:         true,
			client:        &mockMergeRehearser{err: errors.New("bad object")},
			verification:  present,
			wantState:     mrMergeStateErr,
			wantConflicts: 0,
			wantCalls:     1,
		},
		{
			name:         "empty branch is not rehearsed",
			check:        true,
			client:       &mockMergeRehearser{},
			verification: mrVerification{state: mrBranchStateEmpty, branchRef: "origin/b", targetRef: "origin/main"},
			wantState:    mrMergeStateSkipped,
		},
		{
			name:         "missing branch is not rehearsed",
			check:        true,
			client:       &mockMergeRehearser{},
			verification: mrVerification{state: mrBranchStateMissing},
			wantState:    mrMergeStateSkipped,
		},
		{
			name:         "unresolved refs are not rehearsed",
			check:        true,
			client:       &mockMergeRehearser{},
			verification: mrVerification{state: mrBranchStatePresent, commitsAhead: 2},
			wantState:    mrMergeStateSkipped,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotState, gotConflicts := rehearseMerge(tt.check, tt.client, tt.verification)
			if gotState != tt.wantState {
				t.Errorf("rehearseMerge() state = %q, want %q", gotState, tt.wantState)
			}
			if gotConflicts != tt.wantConflicts {
				t.Errorf("rehearseMerge() conflicts = %d, want %d", gotConflicts, tt.wantConflicts)
			}
			if m, ok := tt.client.(*mockMergeRehearser); ok && m.calls != tt.wantCalls {
				t.Errorf("rehearseMerge() called git %d time(s), want %d", m.calls, tt.wantCalls)
			}
		})
	}
}

func conflictedPathsFixture(n int) []string {
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		paths = append(paths, fmt.Sprintf("internal/pkg/file%d.go", i))
	}
	return paths
}

// TestRehearseMergeAgainstRealGit is the control this whole change exists for:
// a branch that --verify calls PRESENT, with commits over its target, that
// still cannot merge. If the rehearsal ever agrees with --verify on this
// fixture, it has stopped measuring anything (gt-0w2l).
func TestRehearseMergeAgainstRealGit(t *testing.T) {
	repo := t.TempDir()
	runGitForMQSubmitTest(t, repo, "init")
	runGitForMQSubmitTest(t, repo, "config", "user.email", "test@example.com")
	runGitForMQSubmitTest(t, repo, "config", "user.name", "Test User")

	writeMQSubmitTestFile(t, repo, "a.txt", "base\n")
	writeMQSubmitTestFile(t, repo, "b.txt", "base\n")
	runGitForMQSubmitTest(t, repo, "add", ".")
	runGitForMQSubmitTest(t, repo, "commit", "-m", "base")
	runGitForMQSubmitTest(t, repo, "branch", "-M", "main")

	// A branch that edits two files one way...
	runGitForMQSubmitTest(t, repo, "checkout", "-b", "polecat/deathclaw/gt-x")
	writeMQSubmitTestFile(t, repo, "a.txt", "deathclaw\n")
	writeMQSubmitTestFile(t, repo, "b.txt", "deathclaw\n")
	runGitForMQSubmitTest(t, repo, "commit", "-am", "deathclaw")

	// ...and a branch that touches a file main never moved.
	runGitForMQSubmitTest(t, repo, "checkout", "-b", "polecat/nux/gt-y", "main")
	writeMQSubmitTestFile(t, repo, "c.txt", "nux\n")
	runGitForMQSubmitTest(t, repo, "add", ".")
	runGitForMQSubmitTest(t, repo, "commit", "-m", "nux")

	// main moves the same two lines the other way, so the merge base is behind
	// both tips — exactly the shape that produced 17 conflicted files in the
	// live case.
	runGitForMQSubmitTest(t, repo, "checkout", "main")
	writeMQSubmitTestFile(t, repo, "a.txt", "main\n")
	writeMQSubmitTestFile(t, repo, "b.txt", "main\n")
	runGitForMQSubmitTest(t, repo, "commit", "-am", "main moved on")

	g := gitpkg.NewGit(repo)

	conflicting := &beads.MRFields{Branch: "polecat/deathclaw/gt-x", Target: "main"}
	v := verifyBranchRefs(true, g, conflicting)
	if v.state != mrBranchStatePresent {
		t.Fatalf("conflicting branch: verify state = %q, want %q", v.state, mrBranchStatePresent)
	}
	state, files := rehearseMerge(true, g, v)
	if state != mrMergeStateConflicts {
		t.Errorf("conflicting branch: merge state = %q, want %q", state, mrMergeStateConflicts)
	}
	if files != 2 {
		t.Errorf("conflicting branch: conflicted files = %d, want 2", files)
	}

	clean := &beads.MRFields{Branch: "polecat/nux/gt-y", Target: "main"}
	v = verifyBranchRefs(true, g, clean)
	if v.state != mrBranchStatePresent {
		t.Fatalf("clean branch: verify state = %q, want %q", v.state, mrBranchStatePresent)
	}
	state, files = rehearseMerge(true, g, v)
	if state != mrMergeStateClean {
		t.Errorf("clean branch: merge state = %q, want %q", state, mrMergeStateClean)
	}
	if files != 0 {
		t.Errorf("clean branch: conflicted files = %d, want 0", files)
	}
}

// TestVerifyBranchRefsResolvesRefsForRehearsal pins the refs the rehearsal runs
// against. Rehearsing "polecat/x" when the queue will merge "origin/polecat/x"
// answers about a different commit, and a stale local ref is the routine way
// those two come apart.
func TestVerifyBranchRefsResolvesRefsForRehearsal(t *testing.T) {
	client := &mockBranchVerifier{
		localBranches: map[string]bool{"polecat/Nux/gt-abc": true, "main": true},
		remoteBranches: map[string]bool{
			"origin/polecat/Nux/gt-abc": true,
			"origin/main":               true,
		},
		ahead: map[string]int{"origin/main..origin/polecat/Nux/gt-abc": 5},
	}
	v := verifyBranchRefs(true, client, &beads.MRFields{Branch: "polecat/Nux/gt-abc", Target: "main"})
	if v.branchRef != "origin/polecat/Nux/gt-abc" {
		t.Errorf("branchRef = %q, want %q", v.branchRef, "origin/polecat/Nux/gt-abc")
	}
	if v.targetRef != "origin/main" {
		t.Errorf("targetRef = %q, want %q", v.targetRef, "origin/main")
	}
}

// TestVerifyStatesDoNotReadAsMergeClearance guards the rename, and it is a
// behaviour test rather than a spelling one: the failure it prevents is a
// reader taking a reachability answer for a merge verdict.
//
// "OK" is banned outright. It is the string that misled — in a GIT column
// beside a "ready" status it read as clearance for two branches that conflicted
// in 17 and 12 files (gt-0w2l) — and reintroducing it anywhere in these states
// reintroduces the defect whatever else changes around it.
func TestVerifyStatesDoNotReadAsMergeClearance(t *testing.T) {
	for _, state := range []mrBranchState{
		mrBranchStatePresent, mrBranchStateMissing, mrBranchStateEmpty, mrBranchStateErr,
	} {
		if strings.EqualFold(string(state), "OK") {
			t.Errorf("branch state %q reads as merge clearance; --verify answers reachability only", state)
		}
	}
	if mrBranchStatePresent != "PRESENT" {
		t.Errorf("the reachable-and-non-empty state is %q, want PRESENT", mrBranchStatePresent)
	}

	// The help an operator reads before believing the column has to carry the
	// limit AND point at the check that does answer mergeability. Matching
	// distinctive content phrases, not headings — headings get restyled.
	help := mqListCmd.Long
	for _, want := range []string{
		"not a merge verdict",
		"PRESENT",
		"--merge-check",
		"CONFLICTS=<n>",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("gt mq list --help does not mention %q:\n%s", want, help)
		}
	}
	if strings.Contains(mqListCmd.Long, "\n  OK  ") {
		t.Error("gt mq list --help still documents an OK state")
	}
}

// TestMergeCheckFlagIsRegistered keeps the fix reachable. A verdict that only
// exists as a function is a verdict nobody can ask for.
func TestMergeCheckFlagIsRegistered(t *testing.T) {
	f := mqListCmd.Flags().Lookup("merge-check")
	if f == nil {
		t.Fatal("gt mq list has no --merge-check flag")
	}
	if !strings.Contains(f.Usage, "merge-tree") {
		t.Errorf("--merge-check usage does not say how it decides: %q", f.Usage)
	}
	v := mqListCmd.Flags().Lookup("verify")
	if v == nil {
		t.Fatal("gt mq list has no --verify flag")
	}
	if !strings.Contains(v.Usage, "not a merge verdict") {
		t.Errorf("--verify usage does not disclaim a merge verdict: %q", v.Usage)
	}
}
