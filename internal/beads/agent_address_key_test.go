package beads

import "testing"

// Tests for the single comparison boundary (gt-gbv4).
//
// Before it existed there were two half-inventories: AgentAddressForms knew the
// trailing-slash dimension and NormalizedAgentPathAddress knew the nested-path
// dimension, and exactly one surface in the tree combined them. Every other
// comparison got half the answer and refused, or went false-empty, whenever the
// two sides had been written by different commands.

func TestAgentAddressKeyCollapsesEveryDimension(t *testing.T) {
	cases := map[string]string{
		// trailing slash — the deacon patrol stall
		"deacon":  "deacon",
		"deacon/": "deacon",
		"mayor/":  "mayor",
		" mayor ": "mayor",

		// nested path — sling keeps the container, mail's AddressToIdentity strips it
		"gastown/polecats/toast": "gastown/toast",
		"gastown/polecat/toast":  "gastown/toast",
		"gastown/crew/joe":       "gastown/joe",
		"gastown/toast":          "gastown/toast",

		// case — polecat names are capitalized by some surfaces
		"gastown/polecats/Toast": "gastown/toast",
		"Deacon/":                "deacon",

		// left alone
		"gastown/witness":   "gastown/witness",
		"deacon/boot":       "deacon/boot",
		"deacon/dogs/alpha": "deacon/dogs/alpha",
		"overseer":          "overseer",
		"":                  "",
	}
	for in, want := range cases {
		if got := AgentAddressKey(in); got != want {
			t.Errorf("AgentAddressKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAgentAddressKeyKeepsDogsDistinct guards the one container segment that
// must NOT collapse. Dogs are only ever addressed "deacon/dogs/<name>";
// collapsing that segment would make every dog compare equal to a
// "deacon/<name>" agent instead of fixing anything.
func TestAgentAddressKeyKeepsDogsDistinct(t *testing.T) {
	if SameAgentAddress("deacon/dogs/alpha", "deacon/alpha") {
		t.Error(`SameAgentAddress("deacon/dogs/alpha", "deacon/alpha") = true, want false — "dogs" is not a collapsible container`)
	}
	if !SameAgentAddress("deacon/dogs/alpha", "deacon/dogs/alpha") {
		t.Error("a dog must still equal itself")
	}
}

func TestSameAgentAddressAcceptsEveryPairOfConventions(t *testing.T) {
	// Each group is one agent as five different writers spell it. Every pair
	// within a group must compare equal, in both directions.
	groups := [][]string{
		{"deacon", "deacon/", "Deacon/", " deacon "},
		{"mayor", "mayor/"},
		{"gastown/polecats/toast", "gastown/toast", "gastown/polecats/Toast", "gastown/polecat/toast"},
		{"gastown/crew/joe", "gastown/joe"},
		{"gastown/witness"},
		{"deacon/dogs/alpha"},
	}
	for _, group := range groups {
		for _, a := range group {
			for _, b := range group {
				if !SameAgentAddress(a, b) {
					t.Errorf("SameAgentAddress(%q, %q) = false, want true", a, b)
				}
			}
		}
	}

	// Cross-group must never match, or the predicate is just "true".
	for i, groupA := range groups {
		for j, groupB := range groups {
			if i == j {
				continue
			}
			if SameAgentAddress(groupA[0], groupB[0]) {
				t.Errorf("SameAgentAddress(%q, %q) = true, want false — distinct agents", groupA[0], groupB[0])
			}
		}
	}
}

// TestSameAgentAddressTreatsEmptyAsNobody pins the one case where "equal
// strings" is the wrong answer. Two unassigned beads are not held by the same
// agent; they are held by nobody, and an ownership check that says otherwise
// hands a bead to whoever asked first.
func TestSameAgentAddressTreatsEmptyAsNobody(t *testing.T) {
	for _, pair := range [][2]string{{"", ""}, {"", "deacon"}, {"deacon", ""}, {"  ", ""}} {
		if SameAgentAddress(pair[0], pair[1]) {
			t.Errorf("SameAgentAddress(%q, %q) = true, want false", pair[0], pair[1])
		}
	}
}

// TestHookSlotHeldByMatchesCollapsedAssignee is the direction the old
// hand-combined predicate could not do.
//
// It normalized the QUERY side only: NormalizedAgentPathAddress(form) collapsed
// "gastown/polecats/toast" down to "gastown/toast", so a full-path bead was
// found by a collapsed query. The reverse — a bead written collapsed (which is
// what mail's AddressToIdentity writes, and there are live rows in this town's
// hq store) checked against the agent's full role path — matched nothing.
func TestHookSlotHeldByMatchesCollapsedAssignee(t *testing.T) {
	cases := []struct {
		beadAssignee string
		agent        string
	}{
		{"gastown/toast", "gastown/polecats/toast"}, // mail wrote it, sling asks
		{"gastown/polecats/toast", "gastown/toast"}, // sling wrote it, mail asks
		{"gastown/joe", "gastown/crew/joe"},
		{"deacon", "deacon/"},
		{"deacon/", "deacon"},
		{"gastown/polecats/Toast", "gastown/polecats/toast"},
	}
	for _, tc := range cases {
		issue := &Issue{Assignee: tc.beadAssignee, Status: StatusHooked}
		if !HookSlotHeldBy(issue, tc.agent) {
			t.Errorf("HookSlotHeldBy(assignee=%q, agent=%q) = false, want true", tc.beadAssignee, tc.agent)
		}
		if HookSlotReleased(issue, tc.agent) {
			t.Errorf("HookSlotReleased(assignee=%q, agent=%q) = true — a held bead must not read as released", tc.beadAssignee, tc.agent)
		}
	}
}

// TestHookSlotHeldByStillRejectsOtherAgents is the control for the test above.
// Widening a match predicate is only a fix if it still says no; a predicate that
// returns true for everything would pass every assertion in this file's
// positive direction.
func TestHookSlotHeldByStillRejectsOtherAgents(t *testing.T) {
	cases := []struct {
		beadAssignee string
		agent        string
	}{
		{"gastown/toast", "gastown/polecats/furiosa"},
		{"gastown/toast", "beads/polecats/toast"},
		{"deacon", "mayor/"},
		{"deacon/dogs/alpha", "deacon/"},
		{"", "gastown/polecats/toast"},
		{"gastown/witness", "gastown/polecats/witness"},
	}
	for _, tc := range cases {
		issue := &Issue{Assignee: tc.beadAssignee, Status: StatusHooked}
		if HookSlotHeldBy(issue, tc.agent) {
			t.Errorf("HookSlotHeldBy(assignee=%q, agent=%q) = true, want false", tc.beadAssignee, tc.agent)
		}
	}
}
