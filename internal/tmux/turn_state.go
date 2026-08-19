package tmux

import "strings"

// TurnState describes what an agent's pane says about the state of its turn.
//
// A conversational agent only executes inside a turn. Its patrol loop —
// including the await-signal/await-event call that would put it back to
// sleep — is a child of that turn, so when the turn ends nothing is left
// running and nothing re-invokes it. Distinguishing "turn in flight" from
// "turn ended at an empty prompt" is what lets an external supervisor wake a
// stopped agent without interrupting a working one.
type TurnState int

const (
	// TurnUnknown means the pane could not be read, or no composer line was
	// found in it. The state is not determined; callers must not act on it.
	TurnUnknown TurnState = iota

	// TurnActive means a turn is in flight: the footer carries the busy
	// indicator the agent TUI renders while generating or running a tool.
	// This covers an agent legitimately blocked inside await-signal just as
	// much as one computing, and both must be left alone.
	TurnActive

	// TurnEnded means no turn is in flight and the composer is empty. The
	// agent is sitting at its prompt and will not execute again until
	// something external prompts it.
	TurnEnded

	// TurnStranded means no turn is in flight but real (non-dim) text is
	// sitting in the composer — a submit that never went through. Waking the
	// agent by typing more text would append to that text rather than replace
	// it, so this state is reported separately rather than folded into
	// TurnEnded.
	TurnStranded
)

func (s TurnState) String() string {
	switch s {
	case TurnActive:
		return "active"
	case TurnEnded:
		return "ended"
	case TurnStranded:
		return "stranded"
	default:
		return "unknown"
	}
}

const (
	// turnBusyLookback is how many non-empty lines ABOVE the composer are
	// included in the busy-indicator scan. Everything from the composer down is
	// always included.
	//
	// The composer is the anchor because the busy marker's home is the status
	// line below it, while the transcript — the only place text that merely
	// looks like the marker can appear — is above it. Scanning the WHOLE pane is
	// the mistake this bounds: an agent's own transcript can contain the literal
	// string "esc to interrupt" (as prose, in a quoted command, in a message
	// about pane-reading), and a whole-pane scan then reports "working" for an
	// agent that has sat at an empty prompt for an hour. That was observed live
	// on two agents at once, and it is silent — the check returns a plausible
	// answer rather than an error.
	//
	// The lookback is not zero because some agent TUIs render the marker in the
	// spinner line just above the composer box rather than in the status footer.
	// Two non-empty lines reaches the box border and the spinner above it,
	// without reaching the transcript body.
	//
	// The two failure directions are not symmetric, which is why the bound sits
	// here rather than wider. Missing a marker reads a working agent as stopped
	// and costs one queued message: the nudge transport suppresses its Escape
	// whenever it sees a busy indicator, so a wake appends to the agent's queue
	// instead of interrupting it. Matching stray text reads a stopped agent as
	// working and costs the entire fix — the loop stays stopped and every status
	// surface keeps reporting it running.
	turnBusyLookback = 2

	// turnComposerScanLines bounds how far back from the end of the pane the
	// composer line is looked for. The prompt character can also appear in
	// transcript output, so the search is both backwards (last match wins) and
	// bounded.
	turnComposerScanLines = 10
)

// analyzeTurnState derives a [TurnState] from an escape-preserving pane
// capture (tmux capture-pane -p -e). promptPrefix is the agent's ready-prompt
// prefix; without it the composer cannot be located and the result is
// TurnUnknown.
//
// The busy indicator is decided before the composer's contents, because an
// agent blocked inside its own await call renders an empty composer — identical
// to a stopped agent on that surface alone. The composer says whether anything
// is typed; only the status line says whether a turn is running.
func analyzeTurnState(escContent, promptPrefix string) TurnState {
	if promptPrefix == "" {
		return TurnUnknown
	}

	plain, dim := stripAnsiTrackDim(escContent)
	lines, lineDims := splitRunesAndDim(plain, dim)

	// Drop trailing blank lines so the scans start at real content.
	for len(lines) > 0 && strings.TrimSpace(string(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
		lineDims = lineDims[:len(lineDims)-1]
	}

	text := make([]string, len(lines))
	for i, line := range lines {
		text[i] = string(line)
	}

	composer := findComposerLine(text, promptPrefix)
	if composer < 0 {
		return TurnUnknown
	}

	if busyIndicatorNearAnchor(text, composer) {
		return TurnActive
	}

	content, contentDim := composerContent(lines[composer], lineDims[composer], promptPrefix)
	if len(content) == 0 || allDim(contentDim) {
		// An all-dim span is the TUI's placeholder ghost, not typed text.
		// Reading it as content is how a stopped agent gets misreported as one
		// holding a stranded submit — the ghost even varies per agent and can
		// read like a plausible instruction ("what did the dolt test say?").
		return TurnEnded
	}
	return TurnStranded
}

// findComposerLine returns the index of the last line that looks like the
// agent's input prompt, or -1 if none is within range. Callers should pass a
// snapshot with trailing blank lines already removed (see
// trimTrailingBlankLines) so the bounded window starts at real content.
func findComposerLine(lines []string, promptPrefix string) int {
	start := len(lines) - turnComposerScanLines
	if start < 0 {
		start = 0
	}
	for i := len(lines) - 1; i >= start; i-- {
		if matchesPromptPrefix(lines[i], promptPrefix) {
			return i
		}
	}
	return -1
}

// busyIndicatorNearAnchor reports whether a busy indicator appears in the
// status region around anchor: the anchor line, everything below it, and up to
// turnBusyLookback non-empty lines above it. Blank lines are skipped rather
// than counted so gaps in the layout cannot shrink the window.
//
// The anchor is normally the composer line (see findComposerLine); idle
// detection falls back to the mode-status line, which sits in the same footer
// region, when the composer is not on screen.
func busyIndicatorNearAnchor(lines []string, anchor int) bool {
	for i := anchor; i < len(lines); i++ {
		if hasBusyIndicator(lines[i]) {
			return true
		}
	}
	checked := 0
	for i := anchor - 1; i >= 0 && checked < turnBusyLookback; i-- {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		checked++
		if hasBusyIndicator(line) {
			return true
		}
	}
	return false
}

// trimTrailingBlankLines drops trailing blank lines from a pane snapshot.
//
// capture-pane pads its output to the bottom of the pane and the split on "\n"
// adds one more empty element, so without this the bounded windows above burn
// their budget on padding and miss the footer entirely.
func trimTrailingBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TurnState reports the turn state of an agent session from its visible pane.
//
// The capture is escape-preserving and scoped to the visible pane: scrollback
// is deliberately excluded, because the composer and the status footer — the
// only two things this reads — are always on screen, while scrollback is where
// text that mimics them lives.
//
// Returns TurnUnknown when the pane cannot be captured. Callers must treat
// TurnUnknown as "do not act": it is the same value a missing session
// produces.
func (t *Tmux) TurnState(session string) TurnState {
	content, err := t.run("capture-pane", "-p", "-e", "-t", session)
	if err != nil {
		return TurnUnknown
	}
	return analyzeTurnState(content, readyPromptPrefixForSession(t, session))
}
