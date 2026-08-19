package tmux

import "github.com/steveyegge/gastown/internal/constants"

// Agent-facing keystrokes are the ones a live agent reads: text that lands in
// its composer, or an interrupt that cancels the turn it is in the middle of.
// They are the class gt-vmj and gt-kqf are about, and a test binary must never
// originate one without having arranged isolation.
//
// SendKeys and SendKeysRaw cannot carry that guard themselves. They are also
// how a session is brought into existence — `gt start` launches the agent
// process itself with SendKeys (internal/cmd/start.go), and this package's own
// tests build their fixture sessions the same way — so guarding the primitive
// would refuse the act of creating the thing the guard exists to protect.
//
// The distinction that matters is not the primitive but the destination:
// keystrokes aimed at an agent that is already running in a pane. The wrappers
// below are that destination. A call site picks one and thereby declares which
// kind of send it is making, and the guard follows from the choice rather than
// from remembering to write it out — which is the same reason guardTestNudge
// lives at the transport and not at the thirty-odd nudge call sites.

// Keystrokes that interrupt an agent already running in a pane.
const (
	// KeyEscape cancels the turn an agent is in the middle of. It is what
	// `gt down` sends before asking for a handoff, so the request is not typed
	// into a pane that is busy generating.
	KeyEscape = "Escape"

	// KeyCtrlC signals the process running in the pane. Teardown paths send it
	// before killing a session so the agent gets a chance to exit on its own.
	KeyCtrlC = "C-c"
)

// SendKeysToAgent is SendKeys for text a running agent will read as input.
//
// Use it instead of SendKeys whenever the recipient is a live agent rather than
// a session this process just created. The message is refused — not delivered
// and not silently dropped — when it originates in a test binary that has not
// named the socket it owns. See guardTestNudge.
//
// Messages an agent is meant to act on should go through NudgeSession instead,
// which adds per-session serialization and verifies the text actually left the
// composer. This is for the cases that are not conversational turns: a banner,
// a passthrough on the Connection abstraction.
func (t *Tmux) SendKeysToAgent(session, keys string) error {
	return t.SendKeysToAgentDebounced(session, keys, constants.DefaultDebounceMs)
}

// SendKeysToAgentDebounced is SendKeysToAgent with a caller-chosen delay between
// the paste and the Enter that submits it. See SendKeysDebounced.
func (t *Tmux) SendKeysToAgentDebounced(session, keys string, debounceMs int) error {
	if handled, err := t.guardTestNudge(session, keys); handled {
		return err
	}
	return t.SendKeysDebounced(session, keys, debounceMs)
}

// InterruptAgent sends an interrupt keystroke to a running agent's pane without
// a following Enter. Pass KeyEscape or KeyCtrlC.
//
// It carries the same guard as a nudge for the same reason: an interrupt
// injected into hq-mayor by a stray test cancels whatever the Mayor was doing,
// which is a louder failure than an unwanted message, not a quieter one.
func (t *Tmux) InterruptAgent(session, key string) error {
	if handled, err := t.guardTestNudge(session, "interrupt "+key); handled {
		return err
	}
	return t.SendKeysRaw(session, key)
}
