package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestReconcileSkipReason(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
		want  string
	}{
		{
			name:  "ordinary open bug is checked",
			issue: &beads.Issue{ID: "gt-602", Status: "open", Type: "bug"},
			want:  "",
		},
		{
			name:  "in_progress is checked",
			issue: &beads.Issue{ID: "gt-602", Status: "in_progress", Type: "bug"},
			want:  "",
		},
		{
			name:  "closed bead is not a finding",
			issue: &beads.Issue{ID: "gt-602", Status: "closed", Type: "bug"},
			want:  "terminal",
		},
		{
			name:  "MR beads are runtime state, not work",
			issue: &beads.Issue{ID: "gt-mr-1", Status: "open", Type: "merge-request"},
			want:  "internal-type:merge-request",
		},
		{
			name:  "agent beads are runtime state",
			issue: &beads.Issue{ID: "gt-gastown-polecat-settler", Status: "open", Type: "agent"},
			want:  "internal-type:agent",
		},
		{
			name:  "wisps are ephemeral",
			issue: &beads.Issue{ID: "gt-wisp-gdnc", Status: "open", Type: "bug"},
			want:  "wisp-id",
		},
		{
			// gt:rig identity beads must never be reported as closable work.
			name:  "protected labels are skipped",
			issue: &beads.Issue{ID: "gt-rig-gastown", Status: "open", Type: "task", Labels: []string{"gt:rig"}},
			want:  "protected-label:gt:rig",
		},
		{
			name:  "no_merge beads never produce a merge",
			issue: &beads.Issue{ID: "gt-602", Status: "open", Type: "bug", Description: "no_merge: true"},
			want:  "no_merge",
		},
		{
			name:  "review_only beads never produce a merge",
			issue: &beads.Issue{ID: "gt-602", Status: "open", Type: "bug", Description: "review_only: true"},
			want:  "review_only",
		},
		{
			name:  "local merge strategy lands outside the queue",
			issue: &beads.Issue{ID: "gt-602", Status: "open", Type: "bug", Description: "merge_strategy: local"},
			want:  "merge_strategy:local",
		},
		{
			name:  "an attached molecule alone does not skip the bead",
			issue: &beads.Issue{ID: "gt-602", Status: "open", Type: "bug", Description: "attached_formula: mol-polecat-work"},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reconcileSkipReason(tt.issue); got != tt.want {
				t.Errorf("reconcileSkipReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Pinned beads are permanent reference and must never be reported as closable;
// terminal beads are already closed. Hooked beads must be scanned — a hooked
// bead with work already on the target is the re-dispatch case (gt-2uqy).
func TestReconcileStatusScope(t *testing.T) {
	if len(reconcileStatuses) == 0 {
		t.Fatal("reconcileStatuses is empty; the sweep would scan nothing and report clean")
	}
	var hooked bool
	for _, status := range reconcileStatuses {
		switch status {
		case beads.IssueStatusPinned:
			t.Errorf("reconcileStatuses includes %q, which must never be closed", status)
		case beads.StatusClosed, beads.StatusTombstone:
			t.Errorf("reconcileStatuses includes terminal status %q", status)
		case beads.IssueStatusHooked:
			hooked = true
		}
	}
	if !hooked {
		t.Error("reconcileStatuses omits hooked; a polecat re-dispatched onto merged work would be invisible")
	}
	if !strings.Contains(mqReconcileCmd.Long, "Hooked beads are reported separately") {
		t.Error("mq reconcile help must explain that hooked findings are not the same claim")
	}
}

func TestTruncateReconcileTitle(t *testing.T) {
	short := "gt sling never reuses idle polecats"
	if got := truncateReconcileTitle(short); got != short {
		t.Errorf("truncateReconcileTitle(short) = %q, want it unchanged", got)
	}

	long := strings.Repeat("x", 200)
	got := truncateReconcileTitle(long)
	if len([]rune(got)) != 80 {
		t.Errorf("truncateReconcileTitle(long) length = %d runes, want 80", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncateReconcileTitle(long) = %q, want an ellipsis suffix", got)
	}
}

// The reconcile command reports; it must not close. Auto-closing on "a commit
// names this bead" would recreate the false completions done_ledger.go refuses,
// because a naming commit can be a partial fix by another bead's worker.
func TestReconcileDoesNotClose(t *testing.T) {
	cmd := mqReconcileCmd
	if cmd.Flags().Lookup("close") != nil || cmd.Flags().Lookup("fix") != nil {
		t.Error("mq reconcile grew a closing flag; closing must stay a human judgment call")
	}
	if !strings.Contains(cmd.Long, "reports only") {
		t.Error("mq reconcile help must state that it reports only")
	}
}

// The fetch is the load-bearing part: gt-602's own retraction traces to a
// grep run against an unfetched clone, where merged work returned zero.
func TestReconcileFetchIsDefaultOn(t *testing.T) {
	flag := mqReconcileCmd.Flags().Lookup("no-fetch")
	if flag == nil {
		t.Fatal("mq reconcile has no --no-fetch flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("--no-fetch default = %q, want false so the sweep fetches by default", flag.DefValue)
	}
}
