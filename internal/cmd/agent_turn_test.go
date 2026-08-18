package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/tmux"
)

// The point of the annotation is that "running" alone can be true of a stopped
// patrol loop. A turn state that says the loop is stopped must change what the
// one-line summary says; anything else must leave it exactly as it was.
func TestAnnotateRunning(t *testing.T) {
	tests := []struct {
		status string
		turn   string
		want   string
	}{
		{"running", tmux.TurnEnded.String(), "running (turn ended)"},
		{"running", tmux.TurnStranded.String(), "running (turn ended)"},
		{"running", tmux.TurnActive.String(), "running"},
		{"running", tmux.TurnUnknown.String(), "running"},
		{"running", "", "running"},
		{"stopped", tmux.TurnEnded.String(), "stopped"},
	}

	for _, tt := range tests {
		if got := annotateRunning(tt.status, tt.turn); got != tt.want {
			t.Errorf("annotateRunning(%q, %q) = %q, want %q", tt.status, tt.turn, got, tt.want)
		}
	}
}

// An unreadable pane is not a finding: reporting "unknown" next to a live
// session would train readers to ignore the line, which is how the green they
// already could not trust got ignored in the first place.
func TestRenderAgentTurn(t *testing.T) {
	if got := renderAgentTurn(tmux.TurnUnknown.String()); got != "" {
		t.Errorf("renderAgentTurn(unknown) = %q, want empty", got)
	}
	if got := renderAgentTurn(""); got != "" {
		t.Errorf("renderAgentTurn(\"\") = %q, want empty", got)
	}
	for _, turn := range []string{tmux.TurnActive.String(), tmux.TurnEnded.String(), tmux.TurnStranded.String()} {
		if got := renderAgentTurn(turn); strings.TrimSpace(got) == "" {
			t.Errorf("renderAgentTurn(%q) = %q, want a rendered label", turn, got)
		}
	}
}

// agentTurn must not panic or invent a state when it has nothing to read.
func TestAgentTurnWithoutSession(t *testing.T) {
	if got := agentTurn(nil, "gt-witness"); got != tmux.TurnUnknown.String() {
		t.Errorf("agentTurn(nil, _) = %q, want %q", got, tmux.TurnUnknown.String())
	}
	if got := agentTurn(tmux.NewTmux(), ""); got != tmux.TurnUnknown.String() {
		t.Errorf("agentTurn(_, \"\") = %q, want %q", got, tmux.TurnUnknown.String())
	}
}
