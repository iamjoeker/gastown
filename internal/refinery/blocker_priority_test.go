package refinery

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

func TestBlockerPriority(t *testing.T) {
	tests := []struct {
		name    string
		blocked int
		want    int
		why     string
	}{
		{"P0 clamps to P0", 0, 0, "P0 is the ceiling; there is no band above it"},
		{"P1 blocked gets P0", 1, 0, "must outrank the P1 it gates"},
		{"P2 blocked gets P1", 2, 1, "relative, not absolute — nowhere near P0"},
		{"P3 blocked gets P2", 3, 2, "relative"},
		{"P4 blocked gets P3", 4, 3, "relative"},
		{"out-of-range low clamps to P0", -1, 0, "nothing outranks better-than-P0"},
		{"out-of-range high clamps in-band", 9, 3, "treated as P4, so blocker is P3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BlockerPriority(tc.blocked)
			if got != tc.want {
				t.Fatalf("BlockerPriority(%d) = %d, want %d (%s)", tc.blocked, got, tc.want, tc.why)
			}
		})
	}
}

// TestBlockerPriorityNeverRanksBelowWhatItBlocks is the invariant the whole fix
// exists for: across the entire priority range, the blocker must never come out
// numerically worse than the item it gates.
func TestBlockerPriorityNeverRanksBelowWhatItBlocks(t *testing.T) {
	for blocked := HighestPriority; blocked <= LowestPriority; blocked++ {
		got := BlockerPriority(blocked)
		if got > blocked {
			t.Errorf("blocker for P%d came out at P%d — ranks BELOW what it blocks", blocked, got)
		}
		if got < HighestPriority {
			t.Errorf("blocker for P%d came out at P%d — outside the band range", blocked, got)
		}
	}
}

// TestBlockerPriorityStaysRelative guards the WATCH FOR on gt-ofb0: the fix
// must not become "inflate every refinery task to P0", which would destroy the
// priority signal the queue runs on.
func TestBlockerPriorityStaysRelative(t *testing.T) {
	for blocked := 2; blocked <= LowestPriority; blocked++ {
		if got := BlockerPriority(blocked); got == HighestPriority {
			t.Errorf("blocker for P%d was inflated to P0 — the rule must stay relative", blocked)
		}
	}
}

func TestMostUrgentPriority(t *testing.T) {
	if got := MostUrgentPriority(3, 0, 2); got != 0 {
		t.Errorf("MostUrgentPriority(3,0,2) = %d, want 0", got)
	}
	if got := MostUrgentPriority(2); got != 2 {
		t.Errorf("MostUrgentPriority(2) = %d, want 2", got)
	}
	// No inputs must not read as P0: a caller that lost its blocked items has
	// no claim on the most urgent band.
	if got := MostUrgentPriority(); got != LowestPriority {
		t.Errorf("MostUrgentPriority() = %d, want %d", got, LowestPriority)
	}
}

// --- Scheduling-order demonstration -----------------------------------------
//
// gt-ofb0 is explicit that the priority FIELD is not the thing under test: the
// failure mode is scheduling ORDER. These tests model the two ready-queue sort
// policies the beads store actually implements and assert where a generated
// blocker lands in the resulting queue.

// readyItem is one row in a modelled ready queue.
type readyItem struct {
	id        string
	priority  int
	createdAt time.Time
}

// sortHybrid orders items the way beads' DEFAULT ready-work policy does:
//
//	CASE WHEN created_at >= cutoff THEN 0 ELSE 1 END ASC,
//	CASE WHEN created_at >= cutoff THEN priority ELSE 999 END ASC,
//	created_at ASC, id ASC
//
// The tie-break inside a priority band is created_at ASC — OLDEST first. That
// is why equal-priority inheritance is not enough: a blocker is created last,
// so a tie puts it at the back of its band.
func sortHybrid(items []readyItem, now time.Time) []readyItem {
	cutoff := now.Add(-48 * time.Hour)
	out := append([]readyItem(nil), items...)
	recent := func(it readyItem) bool { return !it.createdAt.Before(cutoff) }
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if recent(a) != recent(b) {
			return recent(a)
		}
		pa, pb := a.priority, b.priority
		if !recent(a) {
			pa, pb = 999, 999
		}
		if pa != pb {
			return pa < pb
		}
		if !a.createdAt.Equal(b.createdAt) {
			return a.createdAt.Before(b.createdAt)
		}
		return a.id < b.id
	})
	return out
}

// sortByPriorityPolicy orders items the way beads' explicit priority policy
// does: ORDER BY priority ASC, created_at DESC, id ASC.
func sortByPriorityPolicy(items []readyItem) []readyItem {
	out := append([]readyItem(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.priority != b.priority {
			return a.priority < b.priority
		}
		if !a.createdAt.Equal(b.createdAt) {
			return a.createdAt.After(b.createdAt)
		}
		return a.id < b.id
	})
	return out
}

func positionOf(queue []readyItem, id string) int {
	for i, it := range queue {
		if it.id == id {
			return i
		}
	}
	return -1
}

// backlogScenario builds the situation measured on 2026-08-22: a P0 MR is
// blocked, and the rig already holds a backlog of open work the blocker has to
// get in front of. The blocked MR itself is NOT in the ready set (is_blocked=1),
// which is exactly why the blocker's competition is the backlog, not the MR.
func backlogScenario(now time.Time, blockerPriority int) []readyItem {
	return []readyItem{
		{id: "backlog-p0-old", priority: 0, createdAt: now.Add(-6 * time.Hour)},
		{id: "backlog-p0-older", priority: 0, createdAt: now.Add(-12 * time.Hour)},
		{id: "backlog-p1-old", priority: 1, createdAt: now.Add(-8 * time.Hour)},
		{id: "backlog-p1-older", priority: 1, createdAt: now.Add(-20 * time.Hour)},
		// The blocker is always the newest row: it is created in response to
		// the merge failure, after everything else already in the queue.
		{id: "blocker", priority: blockerPriority, createdAt: now},
	}
}

// TestDerivedBlockerIsNeverOutrankedInTheReadyQueue is the first half of the
// gt-ofb0 validation, for a P0 MR and for a P1 MR under BOTH sort policies.
//
// The claim under test is NOT "the blocker is first in line" — under the
// default hybrid policy the in-band tie-break is created_at ASC, so a blocker
// still sits behind OLDER items of its own band. That is fine: a same-band
// backlog is a fixed set that drains FIFO. The deadlock came from being behind
// a strictly BETTER band, which is refilled indefinitely and therefore never
// drains.
//
// So the assertion is: after the fix, nothing in the queue outranks the
// blocker. The control is the old fixed-P1 rule, which must show items that do.
func TestDerivedBlockerIsNeverOutrankedInTheReadyQueue(t *testing.T) {
	now := time.Date(2026, 8, 22, 21, 0, 0, 0, time.UTC)

	policies := []struct {
		name string
		sort func([]readyItem) []readyItem
	}{
		{"hybrid (beads default)", func(items []readyItem) []readyItem { return sortHybrid(items, now) }},
		{"priority policy", sortByPriorityPolicy},
	}

	// outrankers reports the items that beat the blocker on priority alone.
	outrankers := func(items []readyItem, blockerPriority int) []string {
		var worse []string
		for _, it := range items {
			if it.id != "blocker" && it.priority < blockerPriority {
				worse = append(worse, it.id)
			}
		}
		return worse
	}

	const oldFixedPriority = 1 // the hardcoded value that produced the deadlock

	for _, blockedPriority := range []int{0, 1} {
		blockedPriority := blockedPriority
		t.Run(fmt.Sprintf("blocked MR at P%d", blockedPriority), func(t *testing.T) {
			derived := BlockerPriority(blockedPriority)

			// CONTROL: the old rule must actually be outranked here. Without
			// this the probe could pass over a scenario too weak to expose the
			// bug, and a green result would certify nothing.
			control := backlogScenario(now, oldFixedPriority)
			if beaten := outrankers(control, oldFixedPriority); len(beaten) == 0 {
				t.Fatalf("control did not reproduce the defect: a fixed-P%d blocker was outranked "+
					"by nothing in %v — this scenario cannot detect the bug it tests for",
					oldFixedPriority, ids(control))
			}

			// THE FIX: nothing in the queue outranks the derived blocker.
			fixed := backlogScenario(now, derived)
			if beaten := outrankers(fixed, derived); len(beaten) > 0 {
				t.Fatalf("blocked P%d -> blocker P%d is still outranked by %v in %v",
					blockedPriority, derived, beaten, ids(fixed))
			}

			// And it never ranks below the MR it gates.
			if derived > blockedPriority {
				t.Fatalf("blocker P%d ranks below the P%d MR it blocks", derived, blockedPriority)
			}

			// Position must strictly improve under every policy.
			for _, policy := range policies {
				t.Run(policy.name, func(t *testing.T) {
					before := positionOf(policy.sort(backlogScenario(now, oldFixedPriority)), "blocker")
					after := positionOf(policy.sort(backlogScenario(now, derived)), "blocker")
					if after > before {
						t.Fatalf("blocked P%d: blocker moved BACKWARDS, position %d -> %d",
							blockedPriority, before, after)
					}
				})
			}
		})
	}
}

// TestDerivedBlockerIsNotStarvedByArrivingWork is the second half of the
// gt-ofb0 validation and the one that actually shows SCHEDULING ORDER rather
// than a field value.
//
// It runs the queue: each round the head item is dispatched and removed, and a
// fresh P0 arrives — which is what a live rig looks like. The old fixed-P1
// blocker never reaches the head no matter how many rounds run; the derived
// blocker does, within a bounded number.
func TestDerivedBlockerIsNotStarvedByArrivingWork(t *testing.T) {
	now := time.Date(2026, 8, 22, 21, 0, 0, 0, time.UTC)

	// Modelled under the hybrid policy only — the one beads uses by default and
	// the one gastown runs on (no sort_policy is configured anywhere in the
	// town). Its in-band tie-break is created_at ASC, so a same-band backlog
	// drains FIFO and an existing item cannot be pushed back by new arrivals.
	// The explicit priority policy breaks in-band ties LIFO (created_at DESC),
	// under which a steady arrival rate starves any in-band item regardless of
	// what this fix does; that is a policy property, not a blocker-priority one.
	const rounds = 50

	// runQueue dispatches the head each round, injecting one new P0 arrival,
	// and returns the round the blocker was dispatched on (-1 if never).
	runQueue := func(blockerPriority int) int {
		queue := backlogScenario(now, blockerPriority)
		for round := 1; round <= rounds; round++ {
			sorted := sortHybrid(queue, now.Add(time.Duration(round)*time.Minute))
			head := sorted[0]
			if head.id == "blocker" {
				return round
			}
			// Dispatch the head and let a fresh P0 arrive in its place.
			queue = nil
			for _, it := range sorted[1:] {
				queue = append(queue, it)
			}
			queue = append(queue, readyItem{
				id:        fmt.Sprintf("arrival-p0-%d", round),
				priority:  0,
				createdAt: now.Add(time.Duration(round) * time.Minute),
			})
		}
		return -1
	}

	// CONTROL: the old fixed-P1 blocker under a P0 MR must never be dispatched.
	// This is the reported defect; if the control were dispatched, the model
	// would not reproduce the bug and the result below would mean nothing.
	if got := runQueue(1); got != -1 {
		t.Fatalf("control did not reproduce the deadlock: fixed-P1 blocker was dispatched "+
			"on round %d of %d — this model cannot detect the bug it tests for", got, rounds)
	}

	// THE FIX: a blocker derived from a P0 MR and from a P1 MR both get out.
	for _, blockedPriority := range []int{0, 1} {
		derived := BlockerPriority(blockedPriority)
		got := runQueue(derived)
		if got == -1 {
			t.Errorf("blocked P%d -> blocker P%d was never dispatched in %d rounds — still starved",
				blockedPriority, derived, rounds)
			continue
		}
		t.Logf("blocked P%d -> blocker P%d dispatched on round %d (control: never in %d)",
			blockedPriority, derived, got, rounds)
	}
}

func ids(items []readyItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = fmt.Sprintf("%s(P%d)", it.id, it.priority)
	}
	return out
}
