package refinery

import (
	"fmt"
	"strings"
	"testing"
)

// fakePreVerifyGit answers the two questions the fast path asks. Reads are
// counted so a test can tell "git agreed" from "git was never consulted" —
// the whole defect being that the decision was taken without asking.
type fakePreVerifyGit struct {
	head      string
	base      string
	revErr    error
	baseErr   error
	revCalls  int
	baseCalls int
	baseArgs  [2]string
}

func (f *fakePreVerifyGit) Rev(ref string) (string, error) {
	f.revCalls++
	if f.revErr != nil {
		return "", f.revErr
	}
	return f.head, nil
}

func (f *fakePreVerifyGit) MergeBase(a, b string) (string, error) {
	f.baseCalls++
	f.baseArgs = [2]string{a, b}
	if f.baseErr != nil {
		return "", f.baseErr
	}
	return f.base, nil
}

const (
	shaTargetHead = "3431f90f7aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaRealParent = "3116d331abbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaSubmitted  = "099efc257cccccccccccccccccccccccccccccccc"
)

// TestFastPathRefusesAClaimGitContradicts is gt-eygw's own numbers: the MR
// claimed pre_verified_base 3431f90f7 while the branch's real parent was
// 3116d331a. Under the old rule the claim equalled the target head, so gates
// were skipped — and the equality proved nothing, because gt done had copied
// the head into the field it was being compared against.
func TestFastPathRefusesAClaimGitContradicts(t *testing.T) {
	g := &fakePreVerifyGit{head: shaTargetHead, base: shaRealParent}
	mr := &MRInfo{
		ID:              "gt-wisp-7v47c",
		Target:          "main",
		Branch:          "polecat/nux",
		CommitSHA:       shaSubmitted,
		PreVerified:     true,
		PreVerifiedBase: shaTargetHead,
	}

	got := decidePreVerifiedFastPath(g, mr)
	if got.SkipGates {
		t.Error("skipped gates for a branch git says is not on the target head")
	}
	if g.baseCalls == 0 {
		t.Error("decided without asking git where the branch is built")
	}
	if g.baseArgs != [2]string{"origin/main", shaSubmitted} {
		t.Errorf("merge-base args = %v, want origin/main and the submitted sha", g.baseArgs)
	}
	// The wrong self-report is a defect in whatever wrote it; it must not be
	// absorbed silently just because the outcome happens to be safe.
	if !strings.Contains(got.Log, "claims pre-verified base") {
		t.Errorf("log does not report the contradicted claim: %q", got.Log)
	}
	if !strings.Contains(got.Log, "stale") {
		t.Errorf("log does not state the decision: %q", got.Log)
	}
}

// TestFastPathFiresWhenGitConfirmsTheRebase is the control. Without it, a rule
// that simply never skips would satisfy every other case here while silently
// deleting the fast path the flag exists for.
func TestFastPathFiresWhenGitConfirmsTheRebase(t *testing.T) {
	g := &fakePreVerifyGit{head: shaTargetHead, base: shaTargetHead}
	mr := &MRInfo{
		ID:              "gt-wisp-ok",
		Target:          "main",
		Branch:          "polecat/nux",
		CommitSHA:       shaSubmitted,
		PreVerified:     true,
		PreVerifiedBase: shaTargetHead,
	}

	got := decidePreVerifiedFastPath(g, mr)
	if !got.SkipGates {
		t.Fatalf("did not take the fast path for a genuinely rebased branch: %q", got.Log)
	}
	if strings.Contains(got.Log, "claims pre-verified base") {
		t.Errorf("reported a claim mismatch where the claim was right: %q", got.Log)
	}
	if !strings.Contains(got.Log, "fast-path") {
		t.Errorf("log does not state the decision: %q", got.Log)
	}
}

// TestFastPathFiresOnAWrongClaimGitVindicates is the other half of "git
// decides": an understated claim — an older gt, a hand-edited bead — should not
// cost the fast path when the branch really is on the target head. It also
// pins that the mismatch is reported in both directions rather than only when
// it changes the outcome.
func TestFastPathFiresOnAWrongClaimGitVindicates(t *testing.T) {
	g := &fakePreVerifyGit{head: shaTargetHead, base: shaTargetHead}
	mr := &MRInfo{
		ID:              "gt-wisp-understated",
		Target:          "main",
		Branch:          "polecat/nux",
		CommitSHA:       shaSubmitted,
		PreVerified:     true,
		PreVerifiedBase: shaRealParent,
	}

	got := decidePreVerifiedFastPath(g, mr)
	if !got.SkipGates {
		t.Errorf("refused a branch git places on the target head: %q", got.Log)
	}
	if !strings.Contains(got.Log, "claims pre-verified base") {
		t.Errorf("did not report the contradicted claim: %q", got.Log)
	}
}

// TestFastPathRunsGatesWhenGitCannotAnswer: every unresolvable case has to land
// on running gates. Falling back to the recorded field on an error path would
// reinstate the defect exactly where the evidence is weakest.
func TestFastPathRunsGatesWhenGitCannotAnswer(t *testing.T) {
	cases := []struct {
		name string
		git  preVerifyGit
		mr   *MRInfo
	}{
		{
			name: "target head unresolvable",
			git:  &fakePreVerifyGit{revErr: fmt.Errorf("unknown revision")},
			mr:   &MRInfo{Target: "main", CommitSHA: shaSubmitted, PreVerified: true, PreVerifiedBase: shaTargetHead},
		},
		{
			name: "merge-base unresolvable",
			git:  &fakePreVerifyGit{head: shaTargetHead, baseErr: fmt.Errorf("no merge base")},
			mr:   &MRInfo{Target: "main", CommitSHA: shaSubmitted, PreVerified: true, PreVerifiedBase: shaTargetHead},
		},
		{
			name: "no git client",
			git:  nil,
			mr:   &MRInfo{Target: "main", CommitSHA: shaSubmitted, PreVerified: true, PreVerifiedBase: shaTargetHead},
		},
		{
			name: "no target",
			git:  &fakePreVerifyGit{head: shaTargetHead, base: shaTargetHead},
			mr:   &MRInfo{CommitSHA: shaSubmitted, PreVerified: true, PreVerifiedBase: shaTargetHead},
		},
		{
			name: "neither commit nor branch",
			git:  &fakePreVerifyGit{head: shaTargetHead, base: shaTargetHead},
			mr:   &MRInfo{Target: "main", PreVerified: true, PreVerifiedBase: shaTargetHead},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decidePreVerifiedFastPath(tc.git, tc.mr)
			if got.SkipGates {
				t.Error("skipped gates on evidence git could not supply")
			}
			if got.Log == "" {
				t.Error("skipped gates decision left no line in the operator log")
			}
		})
	}
}

// TestFastPathIsSilentWhenNotClaimed: an MR that never claimed
// pre-verification is the ordinary case and must not print a line about a fast
// path it never asked for, nor consult git for it.
func TestFastPathIsSilentWhenNotClaimed(t *testing.T) {
	for _, mr := range []*MRInfo{
		nil,
		{Target: "main", CommitSHA: shaSubmitted},
		{Target: "main", CommitSHA: shaSubmitted, PreVerified: true},
		{Target: "main", CommitSHA: shaSubmitted, PreVerified: true, PreVerifiedBase: "   "},
	} {
		g := &fakePreVerifyGit{head: shaTargetHead, base: shaTargetHead}
		got := decidePreVerifiedFastPath(g, mr)
		if got.SkipGates {
			t.Errorf("%+v: took the fast path without a pre-verification claim", mr)
		}
		if got.Log != "" {
			t.Errorf("%+v: logged %q for an MR that made no claim", mr, got.Log)
		}
		if g.revCalls != 0 || g.baseCalls != 0 {
			t.Errorf("%+v: consulted git for an MR that made no claim", mr)
		}
	}
}

// TestFastPathFallsBackToTheBranchName: gt done tolerates a failed HEAD
// resolution, so commit_sha can be absent on an otherwise valid MR. The branch
// name is then the only handle on the submission, and it must be used rather
// than the merge-base silently being taken against nothing.
func TestFastPathFallsBackToTheBranchName(t *testing.T) {
	g := &fakePreVerifyGit{head: shaTargetHead, base: shaTargetHead}
	mr := &MRInfo{
		Target:          "main",
		Branch:          "polecat/nux",
		PreVerified:     true,
		PreVerifiedBase: shaTargetHead,
	}

	if got := decidePreVerifiedFastPath(g, mr); !got.SkipGates {
		t.Errorf("refused an MR whose submission is identified by branch: %q", got.Log)
	}
	if g.baseArgs != [2]string{"origin/main", "polecat/nux"} {
		t.Errorf("merge-base args = %v, want the branch name as the second operand", g.baseArgs)
	}
}
