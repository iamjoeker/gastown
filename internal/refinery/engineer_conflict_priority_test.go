package refinery

import (
	"fmt"
	"testing"
)

// TestConflictTaskCreateOptionsOutranksItsMR checks the priority that actually
// reaches the create call, not the rule in isolation.
//
// gt-ofb0 was a wiring defect: the derivation existed as an idea and a literal
// went out on the wire. A unit test on BlockerPriority alone stays green
// through that, so the boundary is where this has to be asserted.
func TestConflictTaskCreateOptionsOutranksItsMR(t *testing.T) {
	for _, tc := range []struct {
		mrPriority   int
		wantPriority int
	}{
		{mrPriority: 0, wantPriority: 0},
		{mrPriority: 1, wantPriority: 0},
		{mrPriority: 2, wantPriority: 1},
		{mrPriority: 3, wantPriority: 2},
		{mrPriority: 4, wantPriority: 3},
	} {
		tc := tc
		t.Run(fmt.Sprintf("MR at P%d", tc.mrPriority), func(t *testing.T) {
			mr := &MRInfo{
				ID:          "gt-wisp-faoi",
				Branch:      "polecat/chrome/gt-hv3p",
				Target:      "main",
				SourceIssue: "gt-hv3p",
				Priority:    tc.mrPriority,
			}

			opts := conflictTaskCreateOptions(mr, "gastown", "Resolve merge conflicts: x", "body")

			if opts.Priority != tc.wantPriority {
				t.Fatalf("conflict task for a P%d MR would be created at P%d, want P%d",
					tc.mrPriority, opts.Priority, tc.wantPriority)
			}
			// The invariant stated against the MR rather than a constant: a
			// blocker must never rank below what it blocks.
			if opts.Priority > mr.Priority {
				t.Fatalf("conflict task P%d ranks BELOW the P%d MR it blocks",
					opts.Priority, mr.Priority)
			}
			// The exact shape of the reported defect.
			if tc.mrPriority == 0 && opts.Priority == 1 {
				t.Fatal("conflict task for a P0 MR came out at the old hardcoded P1")
			}
			// Guard the rest of the wiring while we are here: a task that lands
			// in the wrong database is unschedulable for a different reason.
			if opts.Rig != "gastown" {
				t.Errorf("Rig = %q, want %q — task would land outside the rig's database", opts.Rig, "gastown")
			}
		})
	}
}

// TestConflictTaskPriorityIsDerivedNotLiteral is a regression guard on the
// specific mistake: the create options must respond to the MR's priority. If a
// future edit reintroduces a constant, every MR priority maps to one value and
// this fails.
func TestConflictTaskPriorityIsDerivedNotLiteral(t *testing.T) {
	seen := map[int]bool{}
	for p := HighestPriority; p <= LowestPriority; p++ {
		opts := conflictTaskCreateOptions(&MRInfo{Priority: p}, "gastown", "t", "d")
		seen[opts.Priority] = true
	}
	if len(seen) < 2 {
		t.Fatalf("conflict-task priority took %d distinct value(s) across P0-P4 — it is a literal, not a derivation", len(seen))
	}
}
