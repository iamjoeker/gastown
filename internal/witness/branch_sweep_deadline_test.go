package witness

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// A per-call timeout, on its own, does not fix a hang in a sweep — it divides
// it (gt-i9wz).
//
// Comparison fetches a candidate ref per branch, so the deadline that ends the
// stall is charged once per branch: nine polecat branches against a remote that
// has stopped answering cost nine full deadlines, and the witness is parked for
// nearly as long as it was before the fix. Worse, it is parked with a green
// changelog behind it. These tests hold the sweep to reading the first deadline
// kill as a fact about the REMOTE.

// unresponsiveErr is what the git layer actually hands back: the sentinel,
// wrapped the way fetchPushRemoteRefExactly wraps it. Constructing it by hand
// rather than asserting on a bare sentinel keeps the test honest about the
// chain errors.Is has to walk in production.
func unresponsiveErr(branch string) error {
	return fmt.Errorf("fetching candidate refs/heads/%s: %w", branch,
		fmt.Errorf("git fetch timed out after 2m0s and its process group was killed: %w", git.ErrRemoteUnresponsive))
}

func TestSweepStopsComparingOnceTheRemoteStopsAnswering(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/aardvark-gt-1", "aaa1"),
			remoteRef("polecat/badger-gt-2", "bbb2"),
			remoteRef("polecat/coyote-gt-3", "ccc3"),
		},
		statusErr: map[string]error{
			"polecat/aardvark-gt-1": unresponsiveErr("polecat/aardvark-gt-1"),
		},
		status: map[string]git.BranchPreservationStatus{
			// Answers that must never be reached. If the sweep asks anyway,
			// these turn into `landed` rows — a clean verdict manufactured
			// out of a remote that is not answering, which is the worst
			// possible outcome and the reason they are scripted this way.
			"polecat/badger-gt-2": {Preserved: true, Evidence: "ancestor"},
			"polecat/coyote-gt-3": {Preserved: true, Evidence: "ancestor"},
		},
	}

	result, err := SweepUnmergedPolecatBranches(g, nil, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("SweepUnmergedPolecatBranches: %v", err)
	}

	if got := len(g.targetLists); got != 1 {
		t.Fatalf("the sweep made %d comparison calls, want 1: after the first deadline kill it must stop touching the network, or the timeout has relocated the hang rather than removed it", got)
	}
	if result.Scanned != 3 {
		t.Fatalf("Scanned = %d, want 3: declining to compare must not shrink the count of branches SEEN", result.Scanned)
	}

	for _, branch := range []string{"polecat/badger-gt-2", "polecat/coyote-gt-3"} {
		f := findingFor(t, result, branch)
		if f.Class != BranchSweepUnknown {
			t.Fatalf("%s classified %s, want unknown: skipping the network must produce a smaller bill, never a verdict", branch, f.Class)
		}
		if !strings.Contains(f.Note, "NOT COMPARED") {
			t.Fatalf("%s note = %q, want it to say the branch was not compared", branch, f.Note)
		}
		if f.Evidence != "" || f.ContainedIn != "" {
			t.Fatalf("%s carries containment evidence (%q / %q) for a comparison that never ran", branch, f.Evidence, f.ContainedIn)
		}
	}

	// The row that DID hit the deadline keeps its own reason, not the
	// second-hand one: the reader's next move differs between "this branch
	// timed out" and "an earlier branch did".
	first := findingFor(t, result, "polecat/aardvark-gt-1")
	if first.Class != BranchSweepUnknown {
		t.Fatalf("the timed-out branch classified %s, want unknown", first.Class)
	}
	if strings.Contains(first.Note, "NOT COMPARED") {
		t.Fatalf("the timed-out branch reports someone else's failure: %q", first.Note)
	}

	if result.AttentionCount() != 3 {
		t.Fatalf("AttentionCount = %d, want 3: an unmeasured branch is a question and belongs on the short list", result.AttentionCount())
	}
}

// TestSweepSaysItWentPartial keeps a truncated sweep from reading like a
// complete one. A short list is only interpretable next to what it was measured
// against, and here most of it was not measured at all.
func TestSweepSaysItWentPartial(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/aardvark-gt-1", "aaa1"),
			remoteRef("polecat/badger-gt-2", "bbb2"),
		},
		statusErr: map[string]error{"polecat/aardvark-gt-1": unresponsiveErr("polecat/aardvark-gt-1")},
	}

	result, err := SweepUnmergedPolecatBranches(g, nil, BranchSweepOptions{Remote: "origin", Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("SweepUnmergedPolecatBranches: %v", err)
	}

	joined := strings.Join(result.Errors, "\n")
	if !strings.Contains(joined, "stopped responding") {
		t.Fatalf("sweep errors = %q, want one naming the unresponsive remote", joined)
	}
	if !strings.Contains(joined, "partial measurement") {
		t.Fatalf("sweep errors = %q, want it to say the sweep is partial", joined)
	}
}

// TestSweepKeepsComparingAfterAnOrdinaryFailure is the control, and it is the
// one that can fail independently: without it, a short-circuit keyed on ANY
// error would satisfy both tests above while silently truncating every sweep in
// which one branch was merely odd — a moved tip, an unreadable bead, a bad ref.
// Those are facts about a branch, not about the remote.
func TestSweepKeepsComparingAfterAnOrdinaryFailure(t *testing.T) {
	g := &fakeSweepGit{
		refs: []git.RemoteRef{
			remoteRef("polecat/aardvark-gt-1", "aaa1"),
			remoteRef("polecat/badger-gt-2", "bbb2"),
			remoteRef("polecat/coyote-gt-3", "ccc3"),
		},
		statusErr: map[string]error{
			"polecat/aardvark-gt-1": fmt.Errorf("remote tip for refs/heads/polecat/aardvark-gt-1 moved between listing and fetch"),
		},
		status: map[string]git.BranchPreservationStatus{
			"polecat/badger-gt-2": {Preserved: true, Evidence: "ancestor"},
			"polecat/coyote-gt-3": {Preserved: true, Evidence: "ancestor"},
		},
	}

	result, err := SweepUnmergedPolecatBranches(g, nil, BranchSweepOptions{Targets: []string{"origin/main"}})
	if err != nil {
		t.Fatalf("SweepUnmergedPolecatBranches: %v", err)
	}
	if got := len(g.targetLists); got != 3 {
		t.Fatalf("the sweep made %d comparison calls, want 3: one branch failing for its own reasons must not blind the sweep to the rest", got)
	}
	for _, branch := range []string{"polecat/badger-gt-2", "polecat/coyote-gt-3"} {
		if f := findingFor(t, result, branch); f.Class != BranchSweepLanded {
			t.Fatalf("%s classified %s, want landed", branch, f.Class)
		}
	}
	if joined := strings.Join(result.Errors, "\n"); strings.Contains(joined, "stopped responding") {
		t.Fatalf("an ordinary per-branch failure was reported as an unresponsive remote: %q", joined)
	}
}
