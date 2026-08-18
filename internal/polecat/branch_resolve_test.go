package polecat

import (
	"errors"
	"strings"
	"testing"
)

type fakeBranchLookup struct {
	current      string
	currentErr   error
	pointingAt   []string
	pointingErr  error
	pointingCall int
}

func (f *fakeBranchLookup) CurrentBranch() (string, error) { return f.current, f.currentErr }

func (f *fakeBranchLookup) BranchesPointingAt(rev string) ([]string, error) {
	f.pointingCall++
	return f.pointingAt, f.pointingErr
}

func TestResolveWorkingBranchAttached(t *testing.T) {
	g := &fakeBranchLookup{current: "polecat/furiosa/bd-791+abc"}

	res, err := ResolveWorkingBranch(g, "furiosa", "main")
	if err != nil {
		t.Fatalf("ResolveWorkingBranch: %v", err)
	}
	if res.Branch != "polecat/furiosa/bd-791+abc" {
		t.Errorf("Branch = %q, want the checked-out branch", res.Branch)
	}
	if res.Detached || res.Recovered {
		t.Errorf("Detached=%v Recovered=%v, want both false for an attached HEAD", res.Detached, res.Recovered)
	}
	if g.pointingCall != 0 {
		t.Errorf("BranchesPointingAt called %d times for an attached HEAD, want 0", g.pointingCall)
	}
}

// The whole point of the resolver: a detached worktree must never be named "HEAD",
// because no remote carries refs/heads/HEAD (gt-e45).
func TestResolveWorkingBranchNeverReturnsHEAD(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pointingAt []string
	}{
		{"recoverable", []string{"polecat/furiosa/bd-791+abc"}},
		{"unrecoverable", nil},
		{"ambiguous", []string{"polecat/furiosa/bd-1+a", "polecat/furiosa/bd-2+b"}},
		{"default branch only", []string{"main"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &fakeBranchLookup{current: "HEAD", pointingAt: tc.pointingAt}
			res, err := ResolveWorkingBranch(g, "furiosa", "main")
			if err != nil {
				t.Fatalf("ResolveWorkingBranch: %v", err)
			}
			if res.Branch == "HEAD" {
				t.Fatal("Branch = \"HEAD\": a branch by that name cannot exist on a remote")
			}
			if !res.Detached {
				t.Error("Detached = false, want true")
			}
			if res.Unresolvable() && res.Reason == "" {
				t.Error("Reason is empty for an unresolvable branch — the alarm has nothing to say")
			}
		})
	}
}

// The observed gt-e45 incident: furiosa was detached at the tip of its own
// branch, so the branch name was recoverable and the push should have happened.
func TestResolveWorkingBranchRecoversFromBranchAtHead(t *testing.T) {
	g := &fakeBranchLookup{
		current:    "HEAD",
		pointingAt: []string{"main", "polecat/furiosa/bd-791+abc"},
	}

	res, err := ResolveWorkingBranch(g, "furiosa", "main")
	if err != nil {
		t.Fatalf("ResolveWorkingBranch: %v", err)
	}
	if res.Branch != "polecat/furiosa/bd-791+abc" {
		t.Errorf("Branch = %q, want the polecat's own branch", res.Branch)
	}
	if !res.Recovered {
		t.Error("Recovered = false, want true")
	}
	if res.Unresolvable() {
		t.Error("Unresolvable() = true for a recoverable detached HEAD")
	}
}

func TestResolveWorkingBranchPrefersOwnBranch(t *testing.T) {
	g := &fakeBranchLookup{
		current:    "HEAD",
		pointingAt: []string{"polecat/nux/bd-100+a", "polecat/furiosa/bd-791+abc", "polecat/slit/bd-200+b"},
	}

	res, err := ResolveWorkingBranch(g, "furiosa", "main")
	if err != nil {
		t.Fatalf("ResolveWorkingBranch: %v", err)
	}
	if res.Branch != "polecat/furiosa/bd-791+abc" {
		t.Errorf("Branch = %q, want this polecat's branch over the others", res.Branch)
	}
}

func TestResolveWorkingBranchRefusesAmbiguousOwnBranches(t *testing.T) {
	g := &fakeBranchLookup{
		current:    "HEAD",
		pointingAt: []string{"polecat/furiosa/bd-791+a", "polecat/furiosa/bd-792+b"},
	}

	res, err := ResolveWorkingBranch(g, "furiosa", "main")
	if err != nil {
		t.Fatalf("ResolveWorkingBranch: %v", err)
	}
	if !res.Unresolvable() {
		t.Fatalf("Branch = %q, want a refusal: pushing the wrong name mis-attributes the work", res.Branch)
	}
	if !strings.Contains(res.Reason, "bd-791") || !strings.Contains(res.Reason, "bd-792") {
		t.Errorf("Reason = %q, want it to name the ambiguous candidates", res.Reason)
	}
}

func TestResolveWorkingBranchIgnoresDefaultBranch(t *testing.T) {
	// A rig whose default branch is not "main" must still not have it chosen.
	g := &fakeBranchLookup{current: "HEAD", pointingAt: []string{"trunk"}}

	res, err := ResolveWorkingBranch(g, "furiosa", "trunk")
	if err != nil {
		t.Fatalf("ResolveWorkingBranch: %v", err)
	}
	if !res.Unresolvable() {
		t.Errorf("Branch = %q, want the default branch rejected as a work branch", res.Branch)
	}
}

func TestResolveWorkingBranchSingleNonPolecatBranch(t *testing.T) {
	g := &fakeBranchLookup{current: "HEAD", pointingAt: []string{"main", "fix/hand-made"}}

	res, err := ResolveWorkingBranch(g, "furiosa", "main")
	if err != nil {
		t.Fatalf("ResolveWorkingBranch: %v", err)
	}
	if res.Branch != "fix/hand-made" {
		t.Errorf("Branch = %q, want the sole remaining candidate", res.Branch)
	}
	if !res.Recovered {
		t.Error("Recovered = false, want true")
	}
}

func TestResolveWorkingBranchNoBranchPointsAtHead(t *testing.T) {
	g := &fakeBranchLookup{current: "HEAD"}

	res, err := ResolveWorkingBranch(g, "furiosa", "main")
	if err != nil {
		t.Fatalf("ResolveWorkingBranch: %v", err)
	}
	if !res.Unresolvable() {
		t.Fatalf("Branch = %q, want unresolvable", res.Branch)
	}
	if !strings.Contains(res.Reason, "no local branch points at it") {
		t.Errorf("Reason = %q, want it to explain that nothing names the commit", res.Reason)
	}
}

// Listing candidates can fail; that is still "cannot name the branch", not an
// aborted gt done, and never a push result.
func TestResolveWorkingBranchListingErrorIsUnresolvable(t *testing.T) {
	g := &fakeBranchLookup{current: "HEAD", pointingErr: errors.New("boom")}

	res, err := ResolveWorkingBranch(g, "furiosa", "main")
	if err != nil {
		t.Fatalf("ResolveWorkingBranch returned an error, want an unresolvable result: %v", err)
	}
	if !res.Unresolvable() {
		t.Fatalf("Branch = %q, want unresolvable", res.Branch)
	}
	if !strings.Contains(res.Reason, "boom") {
		t.Errorf("Reason = %q, want the underlying failure preserved", res.Reason)
	}
}

func TestResolveWorkingBranchPropagatesCurrentBranchError(t *testing.T) {
	g := &fakeBranchLookup{currentErr: errors.New("not a git repository")}

	if _, err := ResolveWorkingBranch(g, "furiosa", "main"); err == nil {
		t.Fatal("ResolveWorkingBranch = nil error, want the git failure surfaced")
	}
}
