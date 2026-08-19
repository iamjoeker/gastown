package cmd

import (
	"errors"
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
			wantState: mrBranchStateOK,
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
			wantState: mrBranchStateOK,
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
			wantState: mrBranchStateOK,
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
	if state != mrBranchStateOK {
		t.Errorf("branch with work: state = %q, want %q", state, mrBranchStateOK)
	}
	if ahead != 1 {
		t.Errorf("branch with work: commitsAhead = %d, want 1", ahead)
	}

	state, _ = verifyBranch(true, g, &beads.MRFields{Branch: "polecat/gone/gt-xyz", Target: "main"})
	if state != mrBranchStateMissing {
		t.Errorf("deleted branch: state = %q, want %q", state, mrBranchStateMissing)
	}
}
