package witness

import (
	"strings"
	"testing"
)

// mrQueryOutput builds the wisps-table listing the MR query returns.
func mrQueryOutput(entries ...string) string {
	return "[" + strings.Join(entries, ",") + "]"
}

func mrEntry(id, sourceIssue, branch string) string {
	return `{"id":"` + id + `","description":"branch: ` + branch + `\ntarget: main\nsource_issue: ` + sourceIssue + `\nrig: testrig"}`
}

func TestFindOpenMRForSourceIssue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		issueID string
		output  string
		execErr error
		want    string
	}{
		{
			name:    "matches the MR naming this issue",
			issueID: "gt-work123",
			output:  mrQueryOutput(mrEntry("gt-wisp-other", "gt-other", "polecat/beta/gt-other"), mrEntry("gt-wisp-mr1", "gt-work123", "polecat/alpha/gt-work123")),
			want:    "gt-wisp-mr1",
		},
		{
			name:    "does not match an ID this one is a prefix of",
			issueID: "gt-work",
			output:  mrQueryOutput(mrEntry("gt-wisp-mr1", "gt-work123", "polecat/alpha/gt-work123")),
			want:    "",
		},
		{
			name:    "no MRs queued",
			issueID: "gt-work123",
			output:  "[]",
			want:    "",
		},
		{
			name:    "null listing",
			issueID: "gt-work123",
			output:  "null",
			want:    "",
		},
		{
			name:    "unparseable listing reads as unqueued",
			issueID: "gt-work123",
			output:  "not json",
			want:    "",
		},
		{
			name:    "empty issue ID never matches",
			issueID: "",
			output:  mrQueryOutput(mrEntry("gt-wisp-mr1", "gt-work123", "polecat/alpha/gt-work123")),
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bd, _ := mockBd(
				func(args []string) (string, error) {
					if len(args) >= 1 && args[0] == "query" {
						return tt.output, tt.execErr
					}
					return "", nil
				},
				func(args []string) error { return nil },
			)
			if got := findOpenMRForSourceIssue(bd, t.TempDir(), tt.issueID); got != tt.want {
				t.Errorf("findOpenMRForSourceIssue(%q) = %q, want %q", tt.issueID, got, tt.want)
			}
		})
	}
}

// TestResetAbandonedBeadSkipsQueuedWork is the re-dispatch half of gt-429i.
// gt done leaves the source bead open once it has queued an MR, so the witness
// must not read that bead as abandoned work and send a second polecat after it.
func TestResetAbandonedBeadSkipsQueuedWork(t *testing.T) {
	// Not parallel: overrides package-level verifyCommitOnMain.
	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return false, nil // not merged yet — the whole point of the guard
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	bd, mock := mockBd(
		func(args []string) (string, error) {
			switch {
			case len(args) >= 1 && args[0] == "show":
				return `[{"status":"hooked"}]`, nil
			case len(args) >= 1 && args[0] == "query":
				return mrQueryOutput(mrEntry("gt-wisp-mr1", "gt-work123", "polecat/alpha/gt-work123")), nil
			}
			return "", nil
		},
		func(args []string) error { return nil },
	)

	if resetAbandonedBead(bd, t.TempDir(), "testrig", "gt-work123", "alpha", nil) {
		t.Error("resetAbandonedBead returned true for a bead whose MR is still in the queue")
	}
	for _, call := range mock.calls {
		if strings.Contains(call, "update") && strings.Contains(call, "--status=open") {
			t.Errorf("bead was reset for re-dispatch despite a queued MR: %v", mock.calls)
		}
		if strings.Contains(call, "close") {
			t.Errorf("bead was closed instead of left for the refinery: %v", mock.calls)
		}
	}
}

// TestResetAbandonedBeadResetsWhenMRIsGone is the control for the guard above:
// a rejected or reaped MR stops matching, and recovery resumes.
func TestResetAbandonedBeadResetsWhenMRIsGone(t *testing.T) {
	// Not parallel: overrides package-level verifyCommitOnMain.
	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	bd, mock := mockBd(
		func(args []string) (string, error) {
			switch {
			case len(args) >= 1 && args[0] == "show":
				return `[{"status":"hooked"}]`, nil
			case len(args) >= 1 && args[0] == "query":
				// Only another issue's MR is queued.
				return mrQueryOutput(mrEntry("gt-wisp-mr9", "gt-other", "polecat/beta/gt-other")), nil
			}
			return "", nil
		},
		func(args []string) error { return nil },
	)

	if !resetAbandonedBead(bd, t.TempDir(), "testrig", "gt-work123", "alpha", nil) {
		t.Fatalf("resetAbandonedBead returned false with no MR for the bead: %v", mock.calls)
	}
	var reset bool
	for _, call := range mock.calls {
		if strings.Contains(call, "update") && strings.Contains(call, "--status=open") {
			reset = true
		}
	}
	if !reset {
		t.Errorf("expected bd update --status=open, got calls: %v", mock.calls)
	}
}
