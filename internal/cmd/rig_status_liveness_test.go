package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/tmux"
)

// TestRigStatusLivenessNote pins what `gt rig status` says beside a polecat's
// lifecycle state once it has read the pane.
//
// The rule under test is "report disagreement only". A surface that annotated
// every polecat would bury the two lines that matter under a wall of agreeing
// ones, and a surface that annotated none is the surface gt-y39t is about —
// hooked-ness rendered as liveness, with nothing to contradict it.
func TestRigStatusLivenessNote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		live     tmux.Liveness
		state    polecat.State
		wantSub  string
		wantNone bool
	}{
		{
			// The line this whole change exists to produce. Bead says working,
			// session is alive, and the agent is sitting at an empty prompt.
			name:    "bead says working, pane is parked",
			live:    tmux.LivenessParked,
			state:   polecat.StateWorking,
			wantSub: "parked",
		},
		{
			name:     "bead says idle and the pane agrees",
			live:     tmux.LivenessParked,
			state:    polecat.StateIdle,
			wantNone: true,
		},
		{
			name:     "bead says working and the pane shows a turn in flight",
			live:     tmux.LivenessTurnInFlight,
			state:    polecat.StateWorking,
			wantNone: true,
		},
		{
			// A polecat the bead calls finished whose agent is still generating.
			// This is the gt-5tg window: gt done writes its beads a minute or
			// two before the session actually ends.
			name:    "bead says done, pane is still running",
			live:    tmux.LivenessTurnInFlight,
			state:   polecat.StateDone,
			wantSub: "turn in flight",
		},
		{
			// Always reported, agreement or not: no lifecycle state predicts it,
			// so there is nothing for it to agree WITH.
			name:    "blocking-wait is reported even while the bead says working",
			live:    tmux.LivenessBlockingWait,
			state:   polecat.StateWorking,
			wantSub: "blocking-wait",
		},
		{
			// The value an unreadable pane, a missing session, and a host with
			// no tmux all produce. Printing it would put "pane: unknown" beside
			// every polecat in those environments, turning a silent absence of
			// evidence into something that reads like a fault.
			name:     "unknown never renders",
			live:     tmux.LivenessUnknown,
			state:    polecat.StateWorking,
			wantNone: true,
		},
		{
			// Handled by its own branch upstream, which prints the remedy. If it
			// also produced a note the line would carry the auth wall twice.
			name:     "logged-out is left to the caller's own arm",
			live:     tmux.LivenessLoggedOut,
			state:    polecat.StateWorking,
			wantNone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := rigStatusLivenessNote(tt.live, tt.state)
			if tt.wantNone {
				if got != "" {
					t.Fatalf("rigStatusLivenessNote(%v, %v) = %q, want no note", tt.live, tt.state, got)
				}
				return
			}
			if !strings.Contains(got, tt.wantSub) {
				t.Fatalf("rigStatusLivenessNote(%v, %v) = %q, want it to mention %q",
					tt.live, tt.state, got, tt.wantSub)
			}
			if !strings.Contains(got, "pane:") {
				// The note sits beside a bead-derived state on the same line.
				// Without naming its source a reader cannot tell which of the
				// two words on that line came from where — the confusion that
				// made two polecat surfaces unreconcilable in gt-mkpm.
				t.Errorf("note %q does not say the claim came from the pane", got)
			}
		})
	}
}
