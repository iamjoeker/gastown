package witness

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// fakeLandedGit is a remote that never exists, so the decision logic can be
// exercised without a rig, a clone or a network.
type fakeLandedGit struct {
	refs    []git.RemoteRef
	listErr error

	// preserved maps a ref NAME to the target that contains it.
	preserved map[string]string
	// evidence maps a ref NAME to how containment was proved.
	evidence map[string]string
	// statusErr maps a ref NAME to a comparison that could not run.
	statusErr map[string]error

	exists        map[string]bool
	defaultBranch string
	clean         string
	behind        map[string]string

	fetchErr   map[string]error
	fetched    []string
	compared   []string
	fetchCalls int
}

func (f *fakeLandedGit) RemoteDefaultBranch() string { return f.defaultBranch }

func (f *fakeLandedGit) CleanBaseRef(remote, defaultBranch, target string) string { return f.clean }

func (f *fakeLandedGit) RefExists(ref string) (bool, error) { return f.exists[ref], nil }

func (f *fakeLandedGit) IsAncestor(ancestor, descendant string) (bool, error) {
	return f.behind[ancestor] == descendant, nil
}

func (f *fakeLandedGit) ListPushRemoteRefsWithHashes(remote, prefix string) ([]git.RemoteRef, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []git.RemoteRef
	for _, ref := range f.refs {
		if strings.HasPrefix(ref.Name, prefix) {
			out = append(out, ref)
		}
	}
	return out, nil
}

func (f *fakeLandedGit) PushRemoteRefTargetStatusAny(remote string, ref git.RemoteRef, targets []string) (git.BranchPreservationStatus, error) {
	f.compared = append(f.compared, ref.Name)
	if err := f.statusErr[ref.Name]; err != nil {
		return git.BranchPreservationStatus{}, err
	}
	base, ok := f.preserved[ref.Name]
	if !ok {
		return git.BranchPreservationStatus{Preserved: false, ComparisonBase: targets[0]}, nil
	}
	return git.BranchPreservationStatus{
		Preserved:      true,
		ComparisonBase: base,
		Evidence:       f.evidence[ref.Name],
	}, nil
}

func (f *fakeLandedGit) Fetch(remote string) error {
	f.fetchCalls++
	f.fetched = append(f.fetched, remote)
	return f.fetchErr[remote]
}

// singleTrunk is the ordinary rig: one origin, one main.
func singleTrunk(refs ...git.RemoteRef) *fakeLandedGit {
	return &fakeLandedGit{
		refs:          refs,
		defaultBranch: "main",
		exists:        map[string]bool{"origin/main": true},
		preserved:     map[string]string{},
		evidence:      map[string]string{},
		statusErr:     map[string]error{},
		fetchErr:      map[string]error{},
	}
}

func ref(branch, hash string) git.RemoteRef {
	return git.RemoteRef{Name: "refs/heads/" + branch, Hash: hash}
}

// The defect this file exists for (gt-e7dd): the work is on the trunk, the
// polecat's worktree is gone, and the answer must still be "landed".
func TestLandedFromPushedBranchesFindsSquashMergedWork(t *testing.T) {
	t.Parallel()

	g := singleTrunk(ref("polecat/dust/gt-e7dd+abc123", "aaa111"))
	// Squash merge: not an ancestor, but the patches are in the trunk. An
	// ancestor-only check would call this unlanded and re-dispatch the bead.
	g.preserved["refs/heads/polecat/dust/gt-e7dd+abc123"] = "origin/main"
	g.evidence["refs/heads/polecat/dust/gt-e7dd+abc123"] = "cherry"

	got, err := landedFromPushedBranches(g, "origin", "dust", "gt-e7dd")
	if err != nil {
		t.Fatalf("landedFromPushedBranches: %v", err)
	}
	if !got.Landed {
		t.Fatalf("Landed = false, want true (%s)", got.Reason)
	}
	if got.Branch != "polecat/dust/gt-e7dd+abc123" {
		t.Errorf("Branch = %q", got.Branch)
	}
	if got.ContainedIn != "origin/main" {
		t.Errorf("ContainedIn = %q, want origin/main", got.ContainedIn)
	}
	if got.CommitSHA != "aaa111" {
		t.Errorf("CommitSHA = %q, want the hash the remote listed", got.CommitSHA)
	}
	// The reason a bead was closed must survive the session that closed it.
	reason := got.CloseReason()
	for _, want := range []string{"polecat/dust/gt-e7dd+abc123", "origin/main"} {
		if !strings.Contains(reason, want) {
			t.Errorf("CloseReason() = %q, want it to name %q", reason, want)
		}
	}
}

// A pushed branch that is genuinely not in the trunk is a measured "no", and
// recovery must proceed exactly as it did before.
func TestLandedFromPushedBranchesReportsUnmergedWork(t *testing.T) {
	t.Parallel()

	g := singleTrunk(ref("polecat/dust/gt-e7dd+abc123", "aaa111"))

	got, err := landedFromPushedBranches(g, "origin", "dust", "gt-e7dd")
	if err != nil {
		t.Fatalf("landedFromPushedBranches: %v", err)
	}
	if got.Landed {
		t.Fatal("Landed = true for a branch that is not contained in the trunk")
	}
	if !strings.Contains(got.Reason, "gt-e7dd") || !strings.Contains(got.Reason, "origin/main") {
		t.Errorf("Reason = %q, want it to name the bead and what it was measured against", got.Reason)
	}
}

// A polecat that died before pushing has left nothing to land. That is a "no"
// with no comparison at all — and must not cost a fetch.
func TestLandedFromPushedBranchesWithNoBranchForTheBead(t *testing.T) {
	t.Parallel()

	g := singleTrunk(ref("polecat/dust/gt-other+abc123", "aaa111"))
	g.preserved["refs/heads/polecat/dust/gt-other+abc123"] = "origin/main"

	got, err := landedFromPushedBranches(g, "origin", "dust", "gt-e7dd")
	if err != nil {
		t.Fatalf("landedFromPushedBranches: %v", err)
	}
	if got.Landed {
		t.Fatalf("another bead's landed branch was credited to gt-e7dd: %+v", got)
	}
	if len(g.compared) != 0 {
		t.Errorf("compared %v, want no comparison when no branch names the bead", g.compared)
	}
	if g.fetchCalls != 0 {
		t.Errorf("fetched %d time(s), want none when there is nothing to compare", g.fetchCalls)
	}
}

// "Could not compare" and "compared, not there" share a boolean, and only the
// second may ever close a bead. Every candidate failing must surface as an
// error, not as a clean no.
func TestLandedFromPushedBranchesErrorsWhenNothingCouldBeCompared(t *testing.T) {
	t.Parallel()

	g := singleTrunk(ref("polecat/dust/gt-e7dd+abc123", "aaa111"))
	g.statusErr["refs/heads/polecat/dust/gt-e7dd+abc123"] = errors.New("remote hung up")

	got, err := landedFromPushedBranches(g, "origin", "dust", "gt-e7dd")
	if err == nil {
		t.Fatalf("want an error when no branch could be compared, got %+v", got)
	}
	if got.Landed {
		t.Error("Landed = true alongside an error")
	}
}

// One branch failing to compare while another compares cleanly is a partial
// measurement: a "no" is allowed, but it has to say what it could not see.
func TestLandedFromPushedBranchesRecordsPartialComparison(t *testing.T) {
	t.Parallel()

	g := singleTrunk(
		ref("polecat/dust/gt-e7dd+aaa", "aaa111"),
		ref("polecat/rictus/gt-e7dd+bbb", "bbb222"),
	)
	g.statusErr["refs/heads/polecat/rictus/gt-e7dd+bbb"] = errors.New("object missing")

	got, err := landedFromPushedBranches(g, "origin", "dust", "gt-e7dd")
	if err != nil {
		t.Fatalf("landedFromPushedBranches: %v", err)
	}
	if got.Landed {
		t.Fatal("Landed = true with no preserved branch")
	}
	if !strings.Contains(got.Reason, "could not be compared") {
		t.Errorf("Reason = %q, want it to admit the branch it could not compare", got.Reason)
	}
}

// A trunk that could not be refreshed may be stale, and a stale trunk is how
// landed work reads as unlanded. The "no" has to say so.
func TestLandedFromPushedBranchesRecordsAStaleTrunk(t *testing.T) {
	t.Parallel()

	g := singleTrunk(ref("polecat/dust/gt-e7dd+abc123", "aaa111"))
	g.fetchErr["origin"] = errors.New("network unreachable")

	got, err := landedFromPushedBranches(g, "origin", "dust", "gt-e7dd")
	if err != nil {
		t.Fatalf("landedFromPushedBranches: %v", err)
	}
	if !strings.Contains(got.Reason, "may be stale") {
		t.Errorf("Reason = %q, want it to flag the unrefreshed trunk", got.Reason)
	}
}

// Containment in EITHER trunk counts on a fork-backed rig, and the finding names
// which one held it.
func TestLandedFromPushedBranchesAcceptsTheUpstreamTrunk(t *testing.T) {
	t.Parallel()

	g := singleTrunk(ref("polecat/dust/gt-e7dd+abc123", "aaa111"))
	g.exists["upstream/main"] = true
	g.preserved["refs/heads/polecat/dust/gt-e7dd+abc123"] = "upstream/main"
	g.evidence["refs/heads/polecat/dust/gt-e7dd+abc123"] = "ancestor"

	got, err := landedFromPushedBranches(g, "origin", "dust", "gt-e7dd")
	if err != nil {
		t.Fatalf("landedFromPushedBranches: %v", err)
	}
	if !got.Landed || got.ContainedIn != "upstream/main" {
		t.Fatalf("got %+v, want landed in upstream/main", got)
	}
	if len(g.fetched) != 2 {
		t.Errorf("fetched %v, want both trunks refreshed before comparing", g.fetched)
	}
}

// With no trunk to compare against, landed and unlanded are indistinguishable.
// Guessing one is how a bead gets closed on no evidence.
func TestLandedFromPushedBranchesErrorsWithNoComparisonRef(t *testing.T) {
	t.Parallel()

	g := singleTrunk(ref("polecat/dust/gt-e7dd+abc123", "aaa111"))
	g.exists = map[string]bool{}
	g.clean = ""

	if _, err := landedFromPushedBranches(g, "origin", "dust", "gt-e7dd"); err == nil {
		t.Fatal("want an error when no comparison ref resolves")
	}
}

// A listing that failed is not an empty listing.
func TestLandedFromPushedBranchesErrorsWhenTheRemoteCannotBeListed(t *testing.T) {
	t.Parallel()

	g := singleTrunk()
	g.listErr = errors.New("could not read from remote")

	if _, err := landedFromPushedBranches(g, "origin", "dust", "gt-e7dd"); err == nil {
		t.Fatal("want an error when the remote could not be listed")
	}
}

// Only branches that are compared may be reported on. Truncation is stated, not
// applied silently — a capped sweep that reads as exhaustive is worse than one
// that admits its cap.
func TestLandedFromPushedBranchesStatesWhatItTruncated(t *testing.T) {
	t.Parallel()

	var refs []git.RemoteRef
	for i := 0; i < maxLandedCandidateBranches+3; i++ {
		refs = append(refs, ref(fmt.Sprintf("polecat/dust/gt-e7dd+s%02d", i), fmt.Sprintf("hash%02d", i)))
	}
	g := singleTrunk(refs...)

	got, err := landedFromPushedBranches(g, "origin", "dust", "gt-e7dd")
	if err != nil {
		t.Fatalf("landedFromPushedBranches: %v", err)
	}
	if len(g.compared) != maxLandedCandidateBranches {
		t.Errorf("compared %d branches, want the cap of %d", len(g.compared), maxLandedCandidateBranches)
	}
	if !strings.Contains(got.Reason, "NOT checked") {
		t.Errorf("Reason = %q, want it to state the branches it skipped", got.Reason)
	}
}

func TestCandidateBranchesForBead(t *testing.T) {
	t.Parallel()

	refs := []git.RemoteRef{
		// Legacy form: encodes no bead, so it can never be attributed to one.
		ref("polecat/dust-abc123", "111"),
		// Another bead entirely.
		ref("polecat/dust/gt-other+abc", "222"),
		// An earlier attempt at the SAME bead by a different polecat.
		ref("polecat/rictus/gt-e7dd+xyz", "333"),
		// This polecat's own branch for the bead.
		ref("polecat/dust/gt-e7dd+abc", "444"),
		// A tagless ref with no hash is unusable.
		{Name: "refs/heads/polecat/dust/gt-e7dd+nohash", Hash: ""},
	}

	got := candidateBranchesForBead(refs, "dust", "gt-e7dd")
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(got), got)
	}
	if got[0].branch != "polecat/dust/gt-e7dd+abc" {
		t.Errorf("first candidate = %q, want the dead polecat's own branch first", got[0].branch)
	}
	if !got[0].own {
		t.Error("own = false for the dead polecat's own branch")
	}
	if got[1].branch != "polecat/rictus/gt-e7dd+xyz" {
		t.Errorf("second candidate = %q, want the other polecat's branch for the same bead", got[1].branch)
	}
	if got[1].own {
		t.Error("own = true for another polecat's branch")
	}
}

// A legacy branch names no bead. Crediting one would close a bead because some
// unrelated work by the same polecat had landed.
func TestCandidateBranchesForBeadRejectsBeadlessBranches(t *testing.T) {
	t.Parallel()

	refs := []git.RemoteRef{ref("polecat/dust-abc123", "111")}
	if got := candidateBranchesForBead(refs, "dust", "gt-e7dd"); len(got) != 0 {
		t.Fatalf("got %+v, want no candidate from a branch that names no bead", got)
	}
}

// An empty bead ID matches nothing. Without this, every beadless branch would
// match a beadless query.
func TestVerifyWorkLandedFromDurableStateWithNoBead(t *testing.T) {
	t.Parallel()

	got, err := _verifyWorkLandedFromDurableState(t.TempDir(), "testrig", "dust", "  ")
	if err != nil {
		t.Fatalf("_verifyWorkLandedFromDurableState: %v", err)
	}
	if got.Landed {
		t.Fatal("Landed = true with no bead to attribute a branch to")
	}
}
