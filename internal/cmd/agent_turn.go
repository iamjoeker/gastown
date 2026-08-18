package cmd

import (
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
)

// A live tmux session is not the same thing as a running patrol loop. A patrol
// agent executes only inside a turn, and its await-signal/await-event call is a
// child of that turn — so when the turn ends, the session stays up, the process
// tree stays intact, and every status surface keeps reporting "running" while
// the loop is stopped. Witnesses and refineries have sat that way for over an
// hour with nothing in the bead layer or the status commands showing it.
//
// These helpers put the turn state next to the session state so the two are not
// confused. Waking a stopped agent is the daemon's job (see
// wakeStoppedPatrolAgents); this is only about not reporting green.

// agentTurn reports the turn state of a live agent session as a short machine
// value for JSON output: "active", "ended", "stranded", or "unknown".
func agentTurn(t *tmux.Tmux, sessionName string) string {
	if t == nil || sessionName == "" {
		return tmux.TurnUnknown.String()
	}
	return t.TurnState(sessionName).String()
}

// renderAgentTurn renders the turn state for human output, or "" when there is
// nothing worth saying (an unreadable pane is not a finding).
func renderAgentTurn(turn string) string {
	switch turn {
	case tmux.TurnActive.String():
		return style.Success.Render("● in flight")
	case tmux.TurnEnded.String():
		return style.Warning.Render("○ ended") +
			style.Dim.Render(" — the patrol loop is stopped at an empty prompt")
	case tmux.TurnStranded.String():
		return style.Warning.Render("⚠ ended") +
			style.Dim.Render(" — unsent text is sitting in the composer")
	default:
		return ""
	}
}

// annotateRunning appends a turn annotation to a "running" status word, so a
// one-line summary cannot read as healthy while the loop is stopped.
func annotateRunning(status, turn string) string {
	if status != "running" {
		return status
	}
	switch turn {
	case tmux.TurnEnded.String(), tmux.TurnStranded.String():
		return "running (turn ended)"
	default:
		return status
	}
}
