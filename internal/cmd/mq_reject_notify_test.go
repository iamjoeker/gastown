package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/refinery"
)

// gt-sfcl: "Worker notified via mail" was printed from the --notify flag alone,
// so a rejection whose notification failed — including one aimed at a worker
// with no tmux session at all — reported success as its final line. Three
// rejections in one night read as notified when nothing had been sent.
func TestRejectNotifyReportFor(t *testing.T) {
	noSession := errors.New(`exit status 1: Error: session "gt-chrome" not found`)
	mailDown := errors.New("exit status 1: Error: beads unreachable")

	tests := []struct {
		name       string
		notify     refinery.NotifyReport
		wantNote   string // substring
		wantFailed bool
		wantAction string // substring, "" means no action line
	}{
		{
			name:     "no notification requested prints nothing",
			notify:   refinery.NotifyReport{},
			wantNote: "",
		},
		{
			name: "nudge landed",
			notify: refinery.NotifyReport{
				Outcome: refinery.NotifyNudged,
				Target:  "gastown/chrome",
			},
			wantNote: "Worker notified: nudged gastown/chrome",
		},
		{
			name: "mail fallback names the channel that carried it and why",
			notify: refinery.NotifyReport{
				Outcome:  refinery.NotifyMailed,
				Target:   "gastown/chrome",
				NudgeErr: noSession,
			},
			wantNote: "Worker notified by mail (no live session:",
		},
		{
			name: "both channels failed says NOT notified and how to fix it by hand",
			notify: refinery.NotifyReport{
				Outcome:  refinery.NotifyFailed,
				Target:   "gastown/chrome",
				NudgeErr: noSession,
				MailErr:  mailDown,
			},
			wantNote:   "Worker NOT notified",
			wantFailed: true,
			wantAction: "gt mail send gastown/chrome",
		},
		{
			name: "skipped notification is a failure to notify, not a success",
			notify: refinery.NotifyReport{
				Outcome:    refinery.NotifySkipped,
				Target:     "gastown/chrome",
				SkipReason: "MR was already closed before this rejection",
			},
			wantNote:   "Worker NOT notified: MR was already closed",
			wantFailed: true,
			wantAction: "gt mail send gastown/chrome",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &refinery.RejectResult{
				MR:     &refinery.MergeRequest{IssueID: "gt-z5h7"},
				Notify: tt.notify,
			}
			got := rejectNotifyReportFor(result)

			if tt.wantNote == "" {
				if got.Note != "" {
					t.Fatalf("Note = %q, want none", got.Note)
				}
				return
			}
			if !strings.Contains(got.Note, tt.wantNote) {
				t.Errorf("Note = %q, want it to contain %q", got.Note, tt.wantNote)
			}
			if got.Failed != tt.wantFailed {
				t.Errorf("Failed = %v, want %v", got.Failed, tt.wantFailed)
			}
			if tt.wantAction == "" && got.Action != "" {
				t.Errorf("Action = %q, want none", got.Action)
			}
			if tt.wantAction != "" && !strings.Contains(got.Action, tt.wantAction) {
				t.Errorf("Action = %q, want it to contain %q", got.Action, tt.wantAction)
			}

			// The original defect in one assertion: no undelivered outcome may
			// produce a line a reader can mistake for delivery.
			if tt.wantFailed && !strings.Contains(got.Note, "NOT notified") {
				t.Errorf("Note = %q does not say the worker was NOT notified", got.Note)
			}
		})
	}
}

// The literal string that was the bug must not survive anywhere in the report:
// it claimed mail on a path that only ever nudged.
func TestRejectNotifyReportNeverClaimsMailForANudge(t *testing.T) {
	got := rejectNotifyReportFor(&refinery.RejectResult{
		MR: &refinery.MergeRequest{IssueID: "gt-z5h7"},
		Notify: refinery.NotifyReport{
			Outcome: refinery.NotifyNudged,
			Target:  "gastown/chrome",
		},
	})
	if strings.Contains(got.Note, "via mail") {
		t.Errorf("Note = %q claims mail for a notice the nudge channel carried", got.Note)
	}
}
