package refinery

import (
	"errors"
	"strings"
	"testing"
)

// gt-sfcl: the notification half of a rejection reported success from the
// --notify flag alone. These tests pin the outcome to what the channels
// actually did.
func TestNotifyRejectedWorker(t *testing.T) {
	nudgeFailed := errors.New(`exit status 1: Error: session "gt-chrome" not found`)
	mailFailed := errors.New("exit status 1: Error: beads unreachable")

	tests := []struct {
		name        string
		nudgeErr    error
		mailErr     error
		wantOutcome NotifyOutcome
		wantMailed  bool // mail must have been attempted
	}{
		{
			name:        "live session takes the nudge and mail is not sent",
			wantOutcome: NotifyNudged,
		},
		{
			name:        "dead session falls back to durable mail",
			nudgeErr:    nudgeFailed,
			wantOutcome: NotifyMailed,
			wantMailed:  true,
		},
		{
			name:        "both channels failing is reported as failure",
			nudgeErr:    nudgeFailed,
			mailErr:     mailFailed,
			wantOutcome: NotifyFailed,
			wantMailed:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var nudged, mailed bool
			var gotNudgeMsg, gotMailBody string
			n := rejectNotifier{
				nudge: func(_, msg string) error {
					nudged = true
					gotNudgeMsg = msg
					return tt.nudgeErr
				},
				mail: func(_, _, body string) error {
					mailed = true
					gotMailBody = body
					return tt.mailErr
				},
			}

			got := notifyRejectedWorker(n, "gastown/chrome", "polecat/chrome/gt-z5h7", "gt-z5h7", "gates failed on rebase")

			if got.Outcome != tt.wantOutcome {
				t.Errorf("Outcome = %q, want %q", got.Outcome, tt.wantOutcome)
			}
			if !nudged {
				t.Error("nudge was never attempted; the live-session channel must always be tried first")
			}
			if mailed != tt.wantMailed {
				t.Errorf("mail attempted = %v, want %v", mailed, tt.wantMailed)
			}
			if got.Target != "gastown/chrome" {
				t.Errorf("Target = %q, want %q", got.Target, "gastown/chrome")
			}
			// The reason is the whole point of the notice — a rejection whose
			// diagnosis is dropped tells the worker nothing actionable.
			if !strings.Contains(gotNudgeMsg, "gates failed on rebase") {
				t.Errorf("nudge message %q dropped the rejection reason", gotNudgeMsg)
			}
			if tt.wantMailed && !strings.Contains(gotMailBody, "gates failed on rebase") {
				t.Errorf("mail body %q dropped the rejection reason", gotMailBody)
			}
		})
	}
}

// A nudge failure is the fact the operator needs even when mail rescued the
// notice: it says the worker's session is gone, so nothing will read the mail
// until something respawns it.
func TestNotifyRejectedWorkerKeepsNudgeErrorAfterMailFallback(t *testing.T) {
	nudgeErr := errors.New(`exit status 1: Error: session "gt-chrome" not found`)
	got := notifyRejectedWorker(rejectNotifier{
		nudge: func(string, string) error { return nudgeErr },
		mail:  func(string, string, string) error { return nil },
	}, "gastown/chrome", "polecat/chrome", "gt-z5h7", "no")

	if got.Outcome != NotifyMailed {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, NotifyMailed)
	}
	if got.NudgeErr == nil {
		t.Fatal("NudgeErr = nil after a mail fallback; the reason the live channel failed was discarded")
	}
	if !strings.Contains(got.NudgeErr.Error(), "not found") {
		t.Errorf("NudgeErr = %v, want it to carry the nudge's own message", got.NudgeErr)
	}
	if got.MailErr != nil {
		t.Errorf("MailErr = %v, want nil when mail succeeded", got.MailErr)
	}
}

// The zero value must not read as "notified". A NotifyReport that was never
// filled in is the exact shape --notify=false produces, and the old bug was
// precisely a success line printed for an attempt that never happened.
func TestNotifyReportZeroValueIsNotRequested(t *testing.T) {
	var report NotifyReport
	if report.Outcome != NotifyNotRequested {
		t.Errorf("zero Outcome = %q, want %q", report.Outcome, NotifyNotRequested)
	}
	if report.Outcome == NotifyNudged || report.Outcome == NotifyMailed {
		t.Error("zero NotifyReport reads as delivered")
	}
}

func TestRejectNotifyTarget(t *testing.T) {
	tests := []struct {
		rig    string
		worker string
		want   string
	}{
		{"gastown", "polecats/chrome", "gastown/chrome"},
		{"gastown", "chrome", "gastown/chrome"},
		{"foundation", "polecats/deathclaw", "foundation/deathclaw"},
	}
	for _, tt := range tests {
		if got := rejectNotifyTarget(tt.rig, tt.worker); got != tt.want {
			t.Errorf("rejectNotifyTarget(%q, %q) = %q, want %q", tt.rig, tt.worker, got, tt.want)
		}
	}
}

// errorLine exists so a failing gt command's diagnosis survives into the
// summary block instead of a usage banner or a progress warning.
//
// The inputs below are transcribed from real gt output, not paraphrased. The
// first draft of this helper took the LAST non-empty line on the assumption
// that cobra prints usage above the error. It prints usage BELOW it, so on a
// real `gt mail send` failure that draft would have reported
// "--wisp Send as wisp (ephemeral, ...)" as the reason the worker was not
// notified.
func TestErrorLine(t *testing.T) {
	// Verbatim from: gt mail send gastown/nosuchpolecat -s x -m y --permanent
	realMailFailure := `Error: unknown recipient: gastown/nosuchpolecat (no matching agent or workspace found)
Usage:
  gt mail send <address> [flags]

Flags:
      --allow-empty       Send even when the body is empty (subject-only message)
      --wisp              Send as wisp (ephemeral, age-GC reclaimable, not synced to git)`

	// Verbatim from: gt nudge gastown/polecats/nosuchpolecat "probe"
	realNudgeFailure := `Error: session "gt-nosuchpolecat" not found (cannot queue nudge for nonexistent session)`

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "real mail failure: usage banner BELOW the error",
			in:   realMailFailure,
			want: "Error: unknown recipient: gastown/nosuchpolecat (no matching agent or workspace found)",
		},
		{
			name: "real nudge failure: single line",
			in:   realNudgeFailure,
			want: `Error: session "gt-nosuchpolecat" not found (cannot queue nudge for nonexistent session)`,
		},
		{
			name: "progress output above the error is skipped",
			in:   "Watching gt-chrome for idle (up to 1m0s)...\nError: pane is gone",
			want: "Error: pane is gone",
		},
		{
			name: "output with no Error: line falls back to the first line",
			in:   "\n  something broke\ntrailing noise\n",
			want: "something broke",
		},
		{name: "empty", in: "", want: ""},
		{name: "only blanks", in: "\n \n", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errorLine(tt.in)
			if got != tt.want {
				t.Errorf("errorLine() = %q, want %q", got, tt.want)
			}
			if strings.HasPrefix(got, "--") {
				t.Errorf("errorLine() = %q, which is a flag description from the usage banner", got)
			}
		})
	}
}

func TestErrorLineTruncatesRunawayOutput(t *testing.T) {
	long := "Error: " + strings.Repeat("x", 500)
	got := errorLine(long)
	if len(got) > errorLineMaxLen+3 {
		t.Errorf("errorLine() returned %d bytes, want at most %d", len(got), errorLineMaxLen+3)
	}
	if !strings.HasPrefix(got, "Error: xxx") {
		t.Errorf("errorLine() = %q, want the head of the diagnosis kept", got)
	}
}
