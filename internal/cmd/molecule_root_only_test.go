package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// A root-only molecule is a molecule root. Saying "not a molecule root?" to
// someone holding a live wisp ID is what sent gt-ba4h's investigation four
// hours in the wrong direction, so that phrase must not appear for one.
func TestNoStepsErrorOnMoleculeRootDoesNotDisputeTheRoot(t *testing.T) {
	root := &beads.Issue{ID: "gt-wisp-gwp41", Title: "mol-polecat-work", Type: "molecule"}

	got := noStepsError(root.ID, root).Error()

	if strings.Contains(got, "not a molecule root") {
		t.Errorf("root-only molecule reported as not-a-root: %s", got)
	}
	if !strings.Contains(got, "root-only") {
		t.Errorf("error does not name the root-only condition: %s", got)
	}
	if !strings.Contains(got, "mol-polecat-work") {
		t.Errorf("error does not name the formula the steps live in: %s", got)
	}
	if !strings.Contains(got, root.ID) {
		t.Errorf("error does not name the molecule: %s", got)
	}
}

// The pre-gt-pzx reading is still correct for anything that is not a molecule
// root: an empty child listing there really does suggest the wrong ID.
func TestNoStepsErrorKeepsOriginalWordingForNonMolecules(t *testing.T) {
	for _, tc := range []struct {
		name  string
		issue *beads.Issue
	}{
		{"bug bead", &beads.Issue{ID: "gt-ba4h", Title: "some bug", Type: "bug"}},
		{"task bead", &beads.Issue{ID: "gt-wisp-92lql", Title: "Implement the solution", Type: "task"}},
		{"unreadable issue", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "gt-unknown"
			if tc.issue != nil {
				id = tc.issue.ID
			}
			got := noStepsError(id, tc.issue).Error()
			want := "no steps found for " + id + " (not a molecule root?)"
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

// issue_type is what both spawn paths set, so root-only and poured molecules
// are recognized the same way — the fix must not depend on which one made it.
func TestIsMoleculeRoot(t *testing.T) {
	for _, tc := range []struct {
		issueType string
		want      bool
	}{
		{"molecule", true},
		{"Molecule", true},
		{" molecule ", true},
		{"task", false},
		{"bug", false},
		{"", false},
	} {
		if got := isMoleculeRoot(&beads.Issue{Type: tc.issueType}); got != tc.want {
			t.Errorf("isMoleculeRoot(%q) = %v, want %v", tc.issueType, got, tc.want)
		}
	}
	if isMoleculeRoot(nil) {
		t.Error("isMoleculeRoot(nil) = true, want false")
	}
}

// The formula name is taken from the title, which is only trustworthy when it
// looks like a formula. A hand-titled molecule gets no suffix rather than a
// sentence pointing at a formula that does not exist.
func TestFormulaSuffixOnlyNamesPlausibleFormulas(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  string
	}{
		{"mol-polecat-work", " (mol-polecat-work)"},
		{"mol-witness-patrol", " (mol-witness-patrol)"},
		{"", ""},
		{"Fix the thing", ""},
		{"mol- something with spaces", ""},
	} {
		if got := formulaSuffix(&beads.Issue{Title: tc.title}); got != tc.want {
			t.Errorf("formulaSuffix(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}
