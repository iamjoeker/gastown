package reaper

import (
	"errors"
	"testing"
)

var errFakeClearerInjected = errors.New("injected clearer failure")

// fakeActiveMRClearer stands in for a beads.Beads pointed at hq. Recording
// calls lets tests assert exactly which (agentBead, mr) pairs the sweep
// tried, not just an aggregate count.
type fakeActiveMRClearer struct {
	// activeMR maps agent bead id -> the MR it currently thinks is active.
	// Mutated by ClearAgentActiveMRIfMatches on a match, mirroring the real
	// compare-and-clear.
	activeMR map[string]string
	calls    []struct{ id, expectedMR string }
	errOn    string
}

func (f *fakeActiveMRClearer) ClearAgentActiveMRIfMatches(id string, expectedMR string) (bool, error) {
	f.calls = append(f.calls, struct{ id, expectedMR string }{id, expectedMR})
	if f.errOn != "" && id == f.errOn {
		return false, errFakeClearerInjected
	}
	if f.activeMR[id] != expectedMR {
		return false, nil
	}
	delete(f.activeMR, id)
	return true, nil
}

func mrWispRow(id, agentBead string) wispRow {
	return wispRow{
		id:       id,
		title:    "MR " + id,
		status:   "closed",
		wispType: "mr",
		description: "source_issue: gt-xxxx\n" +
			"agent_bead: " + agentBead + "\n" +
			"branch: polecat/ghoul/gt-xxxx\n",
	}
}

// TestClearDanglingMRActiveRefsClearsMatchingAgentBead is the acceptance test
// for gt-p88i: a sweep-closed MR wisp that still names an agent_bead whose
// active_mr points back at it must have that pointer cleared.
//
// The negative control is an open MR wisp naming the same agent bead — the
// sweep must never touch an MR that has not gone terminal, so leaving its
// candidate untouched is not "the pass silently clears everything" but the
// guard actually discriminating on status.
func TestClearDanglingMRActiveRefsClearsMatchingAgentBead(t *testing.T) {
	f := newFixture(t, "dangling_mr_clear")
	f.insertWisps(t,
		mrWispRow("gastown-mr-0001", "gastown/polecats/ghoul"),
	)

	clearer := &fakeActiveMRClearer{
		activeMR: map[string]string{
			"gastown/polecats/ghoul": "gastown-mr-0001",
		},
	}

	result, err := ClearDanglingMRActiveRefs(f.db, f.dbName, clearer)
	if err != nil {
		t.Fatalf("ClearDanglingMRActiveRefs: %v", err)
	}

	if result.Scanned != 1 {
		t.Errorf("Scanned = %d, want 1", result.Scanned)
	}
	if result.Cleared != 1 {
		t.Errorf("Cleared = %d, want 1 — the dangling active_mr was not cleared", result.Cleared)
	}
	if got := clearer.activeMR["gastown/polecats/ghoul"]; got != "" {
		t.Errorf("agent bead still shows active_mr=%q after clearing", got)
	}
}

// TestClearDanglingMRActiveRefsSkipsOpenMR is the negative control: an MR
// wisp still open must never be treated as a dangling reference, even though
// it names the same agent_bead shape a closed one would.
func TestClearDanglingMRActiveRefsSkipsOpenMR(t *testing.T) {
	f := newFixture(t, "dangling_mr_skip_open")
	open := mrWispRow("gastown-mr-0002", "gastown/polecats/slit")
	open.status = "open"
	f.insertWisps(t, open)

	clearer := &fakeActiveMRClearer{
		activeMR: map[string]string{
			"gastown/polecats/slit": "gastown-mr-0002",
		},
	}

	result, err := ClearDanglingMRActiveRefs(f.db, f.dbName, clearer)
	if err != nil {
		t.Fatalf("ClearDanglingMRActiveRefs: %v", err)
	}
	if result.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0 — an open MR must not be a candidate", result.Scanned)
	}
	if len(clearer.calls) != 0 {
		t.Errorf("clearer was called %d times for an open MR, want 0", len(clearer.calls))
	}
	if got := clearer.activeMR["gastown/polecats/slit"]; got != "gastown-mr-0002" {
		t.Errorf("active_mr changed for an open MR: got %q", got)
	}
}

// TestClearDanglingMRActiveRefsLeavesReusedActiveMRAlone covers the safety
// property the bead calls out explicitly: a polecat that has since reused
// active_mr for a NEWER MR must not have that newer pointer erased just
// because an older MR naming the same agent_bead was swept closed.
func TestClearDanglingMRActiveRefsLeavesReusedActiveMRAlone(t *testing.T) {
	f := newFixture(t, "dangling_mr_reused")
	f.insertWisps(t, mrWispRow("gastown-mr-old", "gastown/polecats/ghoul"))

	clearer := &fakeActiveMRClearer{
		activeMR: map[string]string{
			// The agent bead has already moved on to a different MR.
			"gastown/polecats/ghoul": "gastown-mr-new",
		},
	}

	result, err := ClearDanglingMRActiveRefs(f.db, f.dbName, clearer)
	if err != nil {
		t.Fatalf("ClearDanglingMRActiveRefs: %v", err)
	}
	if result.Cleared != 0 {
		t.Errorf("Cleared = %d, want 0 — a reused active_mr must be left alone", result.Cleared)
	}
	if got := clearer.activeMR["gastown/polecats/ghoul"]; got != "gastown-mr-new" {
		t.Errorf("active_mr = %q, want unchanged gastown-mr-new", got)
	}
}

// TestClearDanglingMRActiveRefsSkipsWispsWithNoAgentBead ensures an MR wisp
// with no agent_bead field (never had one, or it was stripped) is not a
// candidate at all — there is nothing to clear it against.
func TestClearDanglingMRActiveRefsSkipsWispsWithNoAgentBead(t *testing.T) {
	f := newFixture(t, "dangling_mr_no_agent_bead")
	f.insertWisps(t, wispRow{
		id:          "gastown-mr-0003",
		title:       "MR without agent_bead",
		status:      "closed",
		wispType:    "mr",
		description: "source_issue: gt-xxxx\nbranch: polecat/ghoul/gt-xxxx\n",
	})

	clearer := &fakeActiveMRClearer{activeMR: map[string]string{}}

	result, err := ClearDanglingMRActiveRefs(f.db, f.dbName, clearer)
	if err != nil {
		t.Fatalf("ClearDanglingMRActiveRefs: %v", err)
	}
	if result.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0", result.Scanned)
	}
	if len(clearer.calls) != 0 {
		t.Errorf("clearer was called %d times with no agent_bead present, want 0", len(clearer.calls))
	}
}

// TestClearDanglingMRActiveRefsCountsErrorsWithoutStopping asserts one
// unreachable/malformed agent bead does not stop the sweep from checking the
// remaining candidates.
func TestClearDanglingMRActiveRefsCountsErrorsWithoutStopping(t *testing.T) {
	f := newFixture(t, "dangling_mr_errors")
	f.insertWisps(t,
		mrWispRow("gastown-mr-bad", "gastown/polecats/broken"),
		mrWispRow("gastown-mr-good", "gastown/polecats/ghoul"),
	)

	clearer := &fakeActiveMRClearer{
		activeMR: map[string]string{
			"gastown/polecats/broken": "gastown-mr-bad",
			"gastown/polecats/ghoul":  "gastown-mr-good",
		},
		errOn: "gastown/polecats/broken",
	}

	result, err := ClearDanglingMRActiveRefs(f.db, f.dbName, clearer)
	if err != nil {
		t.Fatalf("ClearDanglingMRActiveRefs: %v", err)
	}
	if result.Scanned != 2 {
		t.Errorf("Scanned = %d, want 2", result.Scanned)
	}
	if result.Errors != 1 {
		t.Errorf("Errors = %d, want 1", result.Errors)
	}
	if result.Cleared != 1 {
		t.Errorf("Cleared = %d, want 1 — the good candidate must still be cleared despite the bad one erroring", result.Cleared)
	}
}

// TestClearDanglingMRActiveRefsNilClearer covers the "town root not
// resolvable" case: it must scan (so the count is still meaningful) but
// never panic on the nil interface.
func TestClearDanglingMRActiveRefsNilClearer(t *testing.T) {
	f := newFixture(t, "dangling_mr_nil_clearer")
	f.insertWisps(t, mrWispRow("gastown-mr-0004", "gastown/polecats/ghoul"))

	result, err := ClearDanglingMRActiveRefs(f.db, f.dbName, nil)
	if err != nil {
		t.Fatalf("ClearDanglingMRActiveRefs: %v", err)
	}
	if result.Scanned != 1 {
		t.Errorf("Scanned = %d, want 1", result.Scanned)
	}
	if result.Cleared != 0 {
		t.Errorf("Cleared = %d, want 0 with no clearer configured", result.Cleared)
	}
}
