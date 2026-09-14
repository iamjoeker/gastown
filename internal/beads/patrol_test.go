package beads

import (
	"strings"
	"testing"
)

func TestIsRoleOwnedPatrolFormula(t *testing.T) {
	cases := []struct {
		name    string
		formula string
		want    bool
	}{
		{"empty", "", false},
		{"deacon patrol", "mol-deacon-patrol", true},
		{"witness patrol", "mol-witness-patrol", true},
		{"refinery patrol", "mol-refinery-patrol", true},
		{"pr feedback patrol", "mol-pr-feedback-patrol", true},
		{"case insensitive", "MOL-DEACON-PATROL", true},
		{"whitespace", "  mol-deacon-patrol  ", true},
		{"generic work", "mol-polecat-work", false},
		{"unrelated suffix", "mol-deacon-patrolling", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRoleOwnedPatrolFormula(tc.formula); got != tc.want {
				t.Errorf("IsRoleOwnedPatrolFormula(%q) = %v, want %v", tc.formula, got, tc.want)
			}
		})
	}
}

func TestRoleOwnedPatrolDispatchRefusal(t *testing.T) {
	t.Run("no attached formula", func(t *testing.T) {
		if got := RoleOwnedPatrolDispatchRefusal("gt-abc", "just some notes"); got != "" {
			t.Errorf("expected no refusal, got %q", got)
		}
	})

	t.Run("generic formula not refused", func(t *testing.T) {
		desc := "attached_formula: mol-polecat-work\nattached_at: 2026-01-01T00:00:00Z"
		if got := RoleOwnedPatrolDispatchRefusal("gt-abc", desc); got != "" {
			t.Errorf("expected no refusal for generic work formula, got %q", got)
		}
	})

	t.Run("role-owned patrol formula refused", func(t *testing.T) {
		desc := "attached_formula: mol-deacon-patrol\nattached_at: 2026-01-01T00:00:00Z"
		got := RoleOwnedPatrolDispatchRefusal("gt-abc", desc)
		if got == "" {
			t.Fatal("expected a refusal for a role-owned patrol formula, got none")
		}
		if !strings.Contains(got, "gt-abc") || !strings.Contains(got, "mol-deacon-patrol") {
			t.Errorf("refusal message missing bead id or formula name: %q", got)
		}
	})
}
