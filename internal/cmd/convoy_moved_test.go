package cmd

import "testing"

// gt-ju7k: gt-ygb7 taught the scheduler's queue reader to follow a moved
// bead's live row instead of its prefix-routed (closed) source copy, but the
// convoy completion path reads tracked issues through the same prefix-routed
// lookup and was never taught the same lesson. A convoy tracking a bead that
// moved to another rig then auto-closed as "1/1 completed" the moment the
// source copy closed, even though the live row — the actual work — was still
// open and had never been dispatched.

// The reproduction: dn-cqu is closed in duly_noted (source, where it was
// filed and where the convoy's dep-list points by prefix) and open in
// gastown (the rig it moved to, holding the real work).
func TestAdoptMovedTrackedDepsFollowsTheLiveRow(t *testing.T) {
	townRoot := newOwnerTown(t)
	stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Title: "source copy", Status: "closed"},
		"gastown":    {Title: "the real work", Status: "open", Labels: []string{"gt:p1"}},
	})

	// What the prefix-routed batch lookup (getIssueDetailsBatch) produced
	// before this call: the closed source copy.
	deps := []trackedDependency{
		{ID: "dn-cqu", DependencyType: "tracks", Status: "closed", Title: "source copy"},
	}
	adoptMovedTrackedDeps(townRoot, deps)

	got := deps[0]
	if got.Status != "open" {
		t.Errorf("Status = %q, want %q — closeConvoyIfComplete would wrongly treat this convoy as done", got.Status, "open")
	}
	if got.Title != "the real work" {
		t.Errorf("Title = %q, want the live row's title", got.Title)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "gt:p1" {
		t.Errorf("Labels = %v, want the live row's labels", got.Labels)
	}
}

// A bead that is genuinely closed everywhere — never moved — must stay
// closed. adoptMovedTrackedDeps only adopts a row when a LIVE copy exists
// elsewhere; it must not paper over real completion.
func TestAdoptMovedTrackedDepsLeavesGenuinelyClosedBeadsAlone(t *testing.T) {
	townRoot := newOwnerTown(t)
	stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Title: "finished work", Status: "closed"},
	})

	deps := []trackedDependency{
		{ID: "dn-cqu", DependencyType: "tracks", Status: "closed", Title: "finished work"},
	}
	adoptMovedTrackedDeps(townRoot, deps)

	got := deps[0]
	if got.Status != "closed" {
		t.Errorf("Status = %q, want %q — no live row exists elsewhere, so this must stay closed", got.Status, "closed")
	}
}

// An open tracked dep is already live and must never trigger the extra
// lookup — adoptMovedTrackedDeps only pays for beads that read as dead.
func TestAdoptMovedTrackedDepsSkipsLiveDeps(t *testing.T) {
	townRoot := newOwnerTown(t)
	scans := stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Title: "in progress", Status: "open"},
	})

	deps := []trackedDependency{
		{ID: "dn-cqu", DependencyType: "tracks", Status: "open", Title: "in progress"},
	}
	adoptMovedTrackedDeps(townRoot, deps)

	if *scans != 0 {
		t.Errorf("expected no extra store scans for an already-live dep, got %d", *scans)
	}
	if deps[0].Status != "open" {
		t.Errorf("Status changed unexpectedly to %q", deps[0].Status)
	}
}
