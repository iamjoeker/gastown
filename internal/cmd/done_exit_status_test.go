package cmd

import (
	"errors"
	"strings"
	"testing"
)

// gt-mqmh: a POLECAT_DONE carried "exit=COMPLETED" and the non-fast-forward
// push failure that had stopped the branch from ever reaching origin, in the
// same line. COMPLETED is what the run ASKED for; it must not also be what the
// witness and the completion metadata are told happened.
func TestDoneReportedExit(t *testing.T) {
	tests := []struct {
		name       string
		exitType   string
		pushFailed bool
		want       string
	}{
		{"a completed run that pushed stays completed", ExitCompleted, false, ExitCompleted},
		{"a completed run that could not push escalates", ExitCompleted, true, ExitEscalated},
		// The requested status is not overwritten when it already says the work
		// did not land — these carry their own meaning to the witness.
		{"a deferred run is left alone", ExitDeferred, true, ExitDeferred},
		{"an escalated run is left alone", ExitEscalated, true, ExitEscalated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := doneReportedExit(tt.exitType, tt.pushFailed); got != tt.want {
				t.Errorf("doneReportedExit(%q, %v) = %q, want %q", tt.exitType, tt.pushFailed, got, tt.want)
			}
		})
	}
}

// gt done reported push failures, MR failures and refused merge requests to the
// witness and then returned nil, so the process exited 0 (gt-7k3q). A caller
// checking exit status could not tell a completion that landed from one that
// pushed nothing.
func TestDoneExitError(t *testing.T) {
	tests := []struct {
		name     string
		outcome  doneOutcome
		wantErr  bool
		contains []string
	}{
		{
			name:    "clean completion exits zero",
			outcome: doneOutcome{},
		},
		{
			name: "a benign no-MR completion exits zero",
			// The superseded and already-merged paths record a reason for the
			// witness without failing. Reasons alone must not decide the exit
			// status, or every one of them becomes a false alarm.
			outcome: doneOutcome{Reasons: []string{"push failed for branch 'x' (content already merged into main)"}},
		},
		{
			name:     "push failure exits non-zero",
			outcome:  doneOutcome{PushFailed: true, Reasons: []string{"push failed for branch 'polecat/x'"}},
			wantErr:  true,
			contains: []string{"the branch was not pushed", "push failed for branch 'polecat/x'"},
		},
		{
			name:     "MR failure exits non-zero",
			outcome:  doneOutcome{MRFailed: true, Reasons: []string{"MR bead creation failed: dolt timeout"}},
			wantErr:  true,
			contains: []string{"no merge request reached the queue", "dolt timeout"},
		},
		{
			name:     "a refused MR exits non-zero",
			outcome:  doneOutcome{MRRefused: true},
			wantErr:  true,
			contains: []string{"merge request creation was refused"},
		},
		{
			name:     "a withheld close exits non-zero",
			outcome:  doneOutcome{LedgerNoteErr: errors.New("bd update failed")},
			wantErr:  true,
			contains: []string{"ledger annotation was not recorded"},
		},
		{
			name:     "several failures are all named",
			outcome:  doneOutcome{PushFailed: true, MRFailed: true},
			wantErr:  true,
			contains: []string{"the branch was not pushed", "no merge request reached the queue"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := doneExitError(tt.outcome)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("doneExitError() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("doneExitError() = nil, want an error so the process exits non-zero")
			}
			for _, want := range tt.contains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q is missing %q", err.Error(), want)
				}
			}
			// The failure that reaches a human has to say what NOT to do next.
			// Hand-closing the bead over an unlanded branch is the exact move
			// that orphaned a P0 merge request.
			if !strings.Contains(err.Error(), "do NOT close the bead by hand") {
				t.Errorf("error %q does not warn against hand-closing the bead", err.Error())
			}
		})
	}
}
