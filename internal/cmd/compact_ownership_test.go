package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// These tests cover gt-98hh: `gt compact` deleted molecule steps on age alone,
// with nothing anywhere asking whether the molecule was still running.
//
// The shape every test here keeps is a PAIR. A guard that holds is worth
// nothing on its own — "held 4411" and "there was nothing to do" print the same
// on a run that holds everything — so each case that expects a hold is written
// next to a control that must still be deleted through the same call.

// finishedOwnership is an index that was built successfully and contains no
// live molecule. It is the release case: a closed wisp with no parent passes
// straight through it.
//
// Used as the neutral argument by the pre-existing pin and label tests, so
// those keep testing what they were written to test rather than accidentally
// passing on the new guard.
func finishedOwnership() *wispOwnership {
	return &wispOwnership{nodes: map[string]wispOwnerRow{}}
}

// ownershipOf builds an index from id -> (status, parent) pairs.
func ownershipOf(rows ...wispOwnerRow) *wispOwnership {
	own := &wispOwnership{nodes: make(map[string]wispOwnerRow, len(rows))}
	for _, r := range rows {
		own.nodes[r.ID] = r
	}
	return own
}

// TestDecideWispDeletesLiveMoleculeSteps is not a test of desired behaviour. It
// pins the branch that makes the guard necessary, so that if the policy is ever
// changed at its source the guard's justification is re-read rather than
// silently outlived.
//
// decideWisp handles closed wisps in the case above this one, so the only wisp
// that reaches "molecule step past TTL" is a step that is still open, hooked,
// blocked or in_progress — an agent's current work.
func TestDecideWispDeletesLiveMoleculeSteps(t *testing.T) {
	ttls := map[string]time.Duration{"gc_report": 24 * time.Hour, "default": 24 * time.Hour}

	for _, status := range []string{"open", "in_progress", "hooked", "blocked"} {
		step := &compactIssue{
			Issue:    beads.Issue{ID: "w-step", Status: status, Parent: "w-root"},
			WispType: "gc_report",
		}
		v := decideWisp(step, 48*time.Hour, ttls)
		if v.action != actionDelete {
			t.Fatalf("decideWisp(step status=%s, 48h past a 24h TTL) = %v, want actionDelete. "+
				"If this branch no longer deletes live steps, gt-98hh's ownership guard "+
				"needs its justification re-read, not just this test updated.", status, v.action)
		}
	}
}

// TestOwnershipHoldsStepOfLiveMolecule is the central case: a CLOSED step, past
// its TTL, whose molecule root is still hooked. Age says delete; ownership says
// an agent is working this molecule right now.
func TestOwnershipHoldsStepOfLiveMolecule(t *testing.T) {
	own := ownershipOf(
		wispOwnerRow{ID: "w-live-root", Status: "hooked"},
		wispOwnerRow{ID: "w-dead-root", Status: "closed"},
		wispOwnerRow{ID: "w-live-step", Status: "closed", Parent: "w-live-root"},
		wispOwnerRow{ID: "w-dead-step", Status: "closed", Parent: "w-dead-root"},
	)

	live := &compactIssue{Issue: beads.Issue{ID: "w-live-step", Status: "closed", Parent: "w-live-root"}}
	dead := &compactIssue{Issue: beads.Issue{ID: "w-dead-step", Status: "closed", Parent: "w-dead-root"}}

	guard := own.guard(live)
	if guard == "" {
		t.Errorf("a closed step of a hooked molecule was released for deletion; " +
			"that is gt-98hh — TTL is a statement about age, not about whether " +
			"anyone is still using the record")
	}
	if !strings.Contains(guard, "w-live-root") {
		t.Errorf("guard = %q, want it to name the live molecule so an operator "+
			"reading Protected can tell which one held it", guard)
	}

	// Control. Same shape, same call, molecule finished: this MUST be released,
	// or the test above passes against a guard that holds everything.
	if g := own.guard(dead); g != "" {
		t.Errorf("guard(step of a closed molecule) = %q, want \"\" — a guard with no "+
			"release arm proves nothing", g)
	}
}

// TestOwnershipHoldsUnfinishedWispItself covers the branch decideWisp actually
// reaches: the step is not closed. It is held on its own status, without the
// index needing to know anything about it.
func TestOwnershipHoldsUnfinishedWispItself(t *testing.T) {
	own := ownershipOf(wispOwnerRow{ID: "w-root", Status: "closed"})

	for _, status := range []string{"open", "in_progress", "hooked", "blocked", "deferred"} {
		w := &compactIssue{Issue: beads.Issue{ID: "w-step", Status: status, Parent: "w-root"}}
		if own.guard(w) == "" {
			t.Errorf("guard(step with status %q) = \"\", want a hold — an unfinished wisp "+
				"is live whatever its age", status)
		}
	}

	// Control: the same wisp once closed, under the same closed root.
	closed := &compactIssue{Issue: beads.Issue{ID: "w-step", Status: "closed", Parent: "w-root"}}
	if g := own.guard(closed); g != "" {
		t.Errorf("guard(closed step of closed root) = %q, want \"\"", g)
	}
}

// TestOwnershipWalksTransitively covers a step of a step. Liveness is a property
// of the whole chain, and a guard that only looked at the immediate parent would
// pass every test above while releasing a grandchild of a running molecule.
func TestOwnershipWalksTransitively(t *testing.T) {
	own := ownershipOf(
		wispOwnerRow{ID: "w-root", Status: "hooked"},
		wispOwnerRow{ID: "w-mid", Status: "closed", Parent: "w-root"},
		wispOwnerRow{ID: "w-leaf", Status: "closed", Parent: "w-mid"},
	)

	leaf := &compactIssue{Issue: beads.Issue{ID: "w-leaf", Status: "closed", Parent: "w-mid"}}
	guard := own.guard(leaf)
	if guard == "" {
		t.Fatalf("guard(grandchild of a hooked molecule) = \"\", want a hold")
	}
	if !strings.Contains(guard, "w-root") {
		t.Errorf("guard = %q, want it to name w-root — the immediate parent w-mid is "+
			"closed, so a one-level check would have released this wisp", guard)
	}
}

// TestOwnershipReleasesOrphan is the release arm that keeps the guard from
// meaning "never delete". A wisp whose ancestor row is gone belongs to a
// molecule that no longer exists, so it cannot be live.
func TestOwnershipReleasesOrphan(t *testing.T) {
	own := ownershipOf(wispOwnerRow{ID: "w-orphan", Status: "closed", Parent: "w-gone"})
	orphan := &compactIssue{Issue: beads.Issue{ID: "w-orphan", Status: "closed", Parent: "w-gone"}}

	if g := own.guard(orphan); g != "" {
		t.Errorf("guard(step whose parent row is absent) = %q, want \"\" — the molecule "+
			"that owned it has already been swept", g)
	}
}

// TestOwnershipHoldsWhenIndexUnavailable covers the two ways the answer is
// unknown. Unknown must mean HOLD: a wisp not deleted today is deleted tomorrow,
// a wisp deleted today without an answer is gone (the wisp tables are
// dolt-ignored — no history to read AS OF, no backup).
func TestOwnershipHoldsWhenIndexUnavailable(t *testing.T) {
	w := &compactIssue{Issue: beads.Issue{ID: "w-any", Status: "closed"}}

	// A nil index is a call site that has none to pass. It must not read as
	// "no policy applies".
	var missing *wispOwnership
	if g := missing.guard(w); g == "" {
		t.Errorf("guard on a nil ownership index released the wisp; a call site added " +
			"later with nothing to pass would silently disable the guard")
	}

	failed := &wispOwnership{nodes: map[string]wispOwnerRow{}, err: errFake{}}
	g := failed.guard(w)
	if g == "" {
		t.Fatalf("guard on a failed index released the wisp")
	}
	if !strings.Contains(g, "fake query failure") {
		t.Errorf("guard = %q, want it to carry the underlying error — an operator "+
			"reading a large Protected count needs to know it was a failed query "+
			"and not that many live molecules", g)
	}

	// Control: the same wisp through an index that built cleanly.
	if g := finishedOwnership().guard(w); g != "" {
		t.Errorf("guard on a healthy empty index = %q, want \"\"", g)
	}
}

type errFake struct{}

func (errFake) Error() string { return "fake query failure" }

// TestOwnershipHoldsOnParentCycle covers corrupt ancestry. A cycle is not a
// finished molecule; the walk cannot show the chain ends, so it holds rather
// than running forever or falling out of the loop into a release.
func TestOwnershipHoldsOnParentCycle(t *testing.T) {
	own := ownershipOf(
		wispOwnerRow{ID: "w-a", Status: "closed", Parent: "w-b"},
		wispOwnerRow{ID: "w-b", Status: "closed", Parent: "w-a"},
	)
	w := &compactIssue{Issue: beads.Issue{ID: "w-a", Status: "closed", Parent: "w-b"}}

	if g := own.guard(w); g == "" {
		t.Errorf("guard(wisp in a parent cycle) = \"\", want a hold")
	}
}

// TestDeleteWispConsultsOwnership proves the guard is wired into the deletion
// path and not merely into a function nothing calls. gt-6dp's post-mortem names
// exactly this: each caller looked reasonable, and the check was in none of
// them.
func TestDeleteWispConsultsOwnership(t *testing.T) {
	oldDryRun, oldVerbose, oldJSON := compactDryRun, compactVerbose, compactJSON
	compactDryRun, compactVerbose, compactJSON = true, false, true
	t.Cleanup(func() { compactDryRun, compactVerbose, compactJSON = oldDryRun, oldVerbose, oldJSON })

	own := ownershipOf(
		wispOwnerRow{ID: "w-live-root", Status: "hooked"},
		wispOwnerRow{ID: "w-dead-root", Status: "closed"},
	)
	held := &compactIssue{Issue: beads.Issue{ID: "w-held", Status: "closed", Parent: "w-live-root"}}
	gone := &compactIssue{Issue: beads.Issue{ID: "w-gone", Status: "closed", Parent: "w-dead-root"}}

	result := &compactResult{}
	deleteWisp(nil, held, "molecule step past TTL", result, own)
	deleteWisp(nil, gone, "molecule step past TTL", result, own)

	if len(result.Protected) != 1 || result.Protected[0].ID != "w-held" {
		t.Errorf("Protected = %+v, want exactly [w-held]", result.Protected)
	}
	if len(result.Deleted) != 1 || result.Deleted[0].ID != "w-gone" {
		t.Errorf("Deleted = %+v, want exactly [w-gone] — the control must stay deletable, "+
			"or this test passes against a compact that deletes nothing", result.Deleted)
	}
	if len(result.Protected) == 1 && !strings.Contains(result.Protected[0].Reason, "molecule step past TTL") {
		t.Errorf("Protected reason = %q, want it to record the action the wisp would "+
			"otherwise have received", result.Protected[0].Reason)
	}
}

// TestWispProtectionOrdersReasons pins that the reported reason is the most
// specific one. A pinned wisp under a live molecule is held because somebody
// pinned it; telling the responder "live molecule" instead would hide the fact
// they are looking for.
func TestWispProtectionOrdersReasons(t *testing.T) {
	own := ownershipOf(wispOwnerRow{ID: "w-live-root", Status: "hooked"})

	pinned := &compactIssue{
		Issue:  beads.Issue{ID: "w-both", Status: "closed", Parent: "w-live-root"},
		Pinned: true,
	}
	if got := wispProtection(pinned, own); got != "pinned" {
		t.Errorf("wispProtection(pinned wisp under a live molecule) = %q, want \"pinned\"", got)
	}

	// Control: drop the pin and the ownership guard is what answers, so the
	// ordering above is a choice between two live guards rather than a case
	// where only one ever fires.
	unpinned := &compactIssue{Issue: beads.Issue{ID: "w-both", Status: "closed", Parent: "w-live-root"}}
	if got := wispProtection(unpinned, own); !strings.Contains(got, "w-live-root") {
		t.Errorf("wispProtection(unpinned wisp under a live molecule) = %q, want the "+
			"ownership guard", got)
	}
}

// TestWispOwnershipQueryScope guards the two properties the index depends on.
//
// The infra exclusion is the subtle one. mutableWispWhere is right about what
// compaction may MUTATE and wrong about what it may LOOK AT: an infra-typed
// wisp compaction must never touch can still be the parent of a step it may.
// Excluding it here would make that parent invisible, the walk would find no
// row, and the step would be released as an orphan by the exact fact that makes
// it live.
func TestWispOwnershipQueryScope(t *testing.T) {
	q := wispOwnershipQuery

	if strings.Contains(q, "issue_type NOT IN") {
		t.Errorf("ownership query excludes infra types; an infra-typed parent would be "+
			"invisible and its live children released as orphans:\n%s", q)
	}
	for _, want := range []string{"FROM wisps w", "w.status", "wisp_dependencies", "'parent-child'"} {
		if !strings.Contains(q, want) {
			t.Errorf("ownership query is missing %q:\n%s", want, q)
		}
	}
	// gt-g60l: a correlated subquery here is one execution per wisp, and at
	// hq's row count that is the 60s bd subprocess timeout.
	for _, sub := range parenthesizedSubqueries(q) {
		if strings.Contains(sub, "w.") {
			t.Errorf("ownership query has a correlated subquery %q; at hq's row count "+
				"that times out:\n%s", sub, q)
		}
	}
}

// TestLoadWispOwnershipFailureIsCarriedNotDropped covers the contract that makes
// the guard hard to disable: a failure to build the index is not returned to a
// caller who may forget to check it, it is carried inside the value and turns
// every candidate into a hold.
func TestLoadWispOwnershipFailureIsCarriedNotDropped(t *testing.T) {
	own := loadWispOwnership(nil)
	if own == nil {
		t.Fatalf("loadWispOwnership returned nil; a nil index is a footgun even though " +
			"guard holds on it")
	}
	if own.err == nil {
		t.Fatalf("loadWispOwnership(nil bd) recorded no error, so guard would release " +
			"every wisp against an empty index")
	}
	if g := own.guard(&compactIssue{Issue: beads.Issue{ID: "w", Status: "closed"}}); g == "" {
		t.Errorf("guard on an index that failed to build released the wisp")
	}
}
