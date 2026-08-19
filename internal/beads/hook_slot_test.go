package beads

import "testing"

func TestHookSlotHeldBy(t *testing.T) {
	const assignee = "gastown/polecats/synth"

	tests := []struct {
		name  string
		issue *Issue
		want  bool
	}{
		{name: "nil issue", issue: nil},
		{name: "exact form", issue: &Issue{Assignee: assignee}, want: true},
		{name: "normalized form", issue: &Issue{Assignee: "gastown/synth"}, want: true},
		{name: "case insensitive", issue: &Issue{Assignee: "Gastown/Polecats/Synth"}, want: true},
		{name: "padded", issue: &Issue{Assignee: "  gastown/polecats/synth  "}, want: true},
		{name: "another polecat", issue: &Issue{Assignee: "gastown/polecats/nitro"}},
		{name: "same name other rig", issue: &Issue{Assignee: "greenplace/polecats/synth"}},
		{name: "unassigned", issue: &Issue{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HookSlotHeldBy(tt.issue, assignee); got != tt.want {
				t.Fatalf("HookSlotHeldBy() = %v, want %v", got, tt.want)
			}
		})
	}

	if HookSlotHeldBy(&Issue{Assignee: assignee}, "") {
		t.Fatal("HookSlotHeldBy() with no agent = true, want false")
	}
}

func TestHookSlotReleased(t *testing.T) {
	const assignee = "gastown/polecats/synth"

	tests := []struct {
		name  string
		issue *Issue
		want  bool
	}{
		{
			// The shape the unstranding remedy writes, and the shape gt unsling
			// writes: nobody holds this bead any more.
			name:  "open and unassigned",
			issue: &Issue{Status: "open"},
			want:  true,
		},
		{name: "assigned elsewhere", issue: &Issue{Status: "hooked", Assignee: "gastown/polecats/nitro"}, want: true},
		{name: "in_progress elsewhere", issue: &Issue{Status: "in_progress", Assignee: "gastown/polecats/nitro"}, want: true},
		{name: "blocked and unassigned", issue: &Issue{Status: "blocked"}, want: true},

		{name: "held here", issue: &Issue{Status: "hooked", Assignee: assignee}},
		{name: "held here in normalized form", issue: &Issue{Status: "hooked", Assignee: "gastown/synth"}},
		// Ambiguous, not released: the status says somebody holds it while the
		// assignee names nobody.
		{name: "hooked with no assignee", issue: &Issue{Status: "hooked"}},
		{name: "in_progress with no assignee", issue: &Issue{Status: "in_progress"}},
		{name: "nil issue", issue: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HookSlotReleased(tt.issue, assignee); got != tt.want {
				t.Fatalf("HookSlotReleased() = %v, want %v", got, tt.want)
			}
		})
	}

	// A terminal bead is finished work, not reassigned work. Callers check
	// IsTerminal separately and the two mean different things downstream.
	if HookSlotReleased(&Issue{Status: "closed", Assignee: assignee}, assignee) {
		t.Fatal("HookSlotReleased() on a closed bead held here = true, want false")
	}
}

func TestNormalizedAgentPathAddress(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "gastown/polecats/synth", want: "gastown/synth"},
		{in: "gastown/crew/max", want: "gastown/max"},
		{in: "gastown/witness", want: "gastown/witness"},
		{in: "deacon/", want: "deacon/"},
		{in: "deacon/dogs/alpha", want: "deacon/dogs/alpha"},
		{in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := NormalizedAgentPathAddress(tt.in); got != tt.want {
				t.Fatalf("NormalizedAgentPathAddress(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
