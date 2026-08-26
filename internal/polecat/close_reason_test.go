package polecat

import "testing"

func TestCloseReasonDischargesMergeQueue(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   bool
	}{
		// The reason gt-xm6w was measured on, verbatim from bd-byk.
		{"duplicate with explanation", "duplicate: same bug as bd-8ob", true},
		// The form the polecat protocol documents in CLAUDE.md.
		{"documented no-changes form", `no-changes: bead already fixed on main`, true},
		{"no-changes with spaces", "no changes: nothing to implement", true},
		{"no-changes with underscores", "no_changes: nothing to implement", true},
		{"superseded without a colon", "superseded by gt-k3h (same two defects)", true},
		{"wontfix", "wontfix", true},
		{"not applicable", "not-applicable: wrong rig", true},
		{"case is not the discriminator", "DUPLICATE: same bug as bd-8ob", true},

		// THE CONTROL THAT MATTERS. A bare substring search for "duplicate"
		// returns true for every one of these, and each would release a slot
		// still holding work nobody declared unwanted. Text ABOUT a category is
		// not a closure IN that category.
		{"a fix that mentions duplicates", "Fixed the duplicate-detection bug", false},
		{"prose containing a category word", "closing this; superseded work was already handled elsewhere", false},
		{"a colon far into prose", "I checked the queue and found nothing outstanding: duplicate", false},

		// "merged" is the opposite claim and must never discharge: it says the
		// work LANDED, which is a fact about the target, not about wanting.
		{"merged", "Merged in gt-wisp-dvns2", false},

		// Silence stays conservative. These are the two commonest values in the
		// store and neither carries a decision.
		{"empty", "", false},
		{"whitespace", "   ", false},
		{"bare Closed", "Closed", false},

		{"unrelated category", "routing probe", false},
		{"re-filed elsewhere is not a declared category", "RE-FILED as beads bd-724 — not fixed, MOVED", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CloseReasonDischargesMergeQueue(tt.reason); got != tt.want {
				t.Fatalf("CloseReasonDischargesMergeQueue(%q) = %v, want %v", tt.reason, got, tt.want)
			}
		})
	}
}
