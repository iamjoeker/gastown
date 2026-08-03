package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// validMRDescription is a well-formed merge-request body: it has both the
// source branch and the target branch, so the MR is actionable by the refinery.
const validMRDescription = "branch: polecat/dust/gt-2ta\ntarget: main\nsource_issue: gt-2ta\nrig: gastown"

func TestIsMergeRequestReadyForSelection(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
		want  bool
	}{
		{
			name:  "open without blockers is ready",
			issue: &beads.Issue{Status: "open", Description: validMRDescription},
			want:  true,
		},
		{
			name: "nil issue is not ready",
		},
		{
			name:  "closed issue is not ready",
			issue: &beads.Issue{Status: "closed", Description: validMRDescription},
		},
		{
			name: "open issue with blocking dependency is not ready",
			issue: &beads.Issue{
				Status:       "open",
				Description:  validMRDescription,
				Dependencies: []beads.IssueDep{{ID: "gt-blocker", Status: "open", DependencyType: "blocks"}},
			},
		},
		{
			name:  "open issue with unhydrated dependency count is not ready",
			issue: &beads.Issue{Status: "open", Description: validMRDescription, DependencyCount: 1},
		},
		{
			name: "closed dependency overrides stale blocked count",
			issue: &beads.Issue{
				Status:         "open",
				Description:    validMRDescription,
				BlockedByCount: 1,
				Dependencies:   []beads.IssueDep{{ID: "gt-closed", Status: "closed", DependencyType: "blocks"}},
			},
			want: true,
		},
		{
			name: "unmerged merge-block remains not ready",
			issue: &beads.Issue{
				Status:       "open",
				Description:  validMRDescription,
				Dependencies: []beads.IssueDep{{ID: "gt-closed-only", Status: "closed", DependencyType: "merge-blocks"}},
			},
		},

		// gt-2ta: an MR the refinery would reject must never rank ready.
		{
			name:  "no MR fields at all is not ready",
			issue: &beads.Issue{Status: "open"},
		},
		{
			name:  "prose-only description is not ready",
			issue: &beads.Issue{Status: "open", Description: "probe"},
		},
		{
			name:  "missing branch is not ready",
			issue: &beads.Issue{Status: "open", Description: "target: main\nsource_issue: gt-2ta"},
		},
		{
			name:  "missing target is not ready",
			issue: &beads.Issue{Status: "open", Description: "branch: polecat/dust/gt-2ta\nsource_issue: gt-2ta"},
		},
		{
			name:  "blank branch value is not ready",
			issue: &beads.Issue{Status: "open", Description: "branch:   \ntarget: main"},
		},
		{
			name:  "blank target value is not ready",
			issue: &beads.Issue{Status: "open", Description: "branch: polecat/dust/gt-2ta\ntarget:   "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMergeRequestReadyForSelection(tt.issue); got != tt.want {
				t.Fatalf("isMergeRequestReadyForSelection() = %v, want %v", got, tt.want)
			}
		})
	}
}
