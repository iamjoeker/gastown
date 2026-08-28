package tmux

import "testing"

// Pane fragments below are transcribed from live agent panes captured with
// `tmux capture-pane -p -e`. The details that matter and are easy to lose when
// hand-writing a fixture:
//
//   - the prompt character ❯ is followed by a NON-BREAKING space (U+00A0),
//     not an ordinary space;
//   - the placeholder ghost is wrapped in ESC[2m … ESC[0m (dim), and its text
//     varies per agent — "keep patrolling", "continue patrol", and questions
//     that read like real staged input;
//   - the busy marker "esc to interrupt" appears on the bottom status line,
//     never on the spinner line above it;
//   - an empty composer still renders a reverse-video cursor (ESC[7m + space).
const (
	boxLine    = "\x1b[38;5;244m────────────────────────────────\x1b[39m"
	footerIdle = "  \x1b[38;5;211m⏵⏵ bypass permissions on\x1b[38;5;246m (shift+tab to cycle) · ← for agents\x1b[39m"
	footerBusy = "  \x1b[38;5;211m⏵⏵ bypass permissions on\x1b[38;5;246m (shift+tab to cycle) · esc to interrupt · ← for age…\x1b[39m"
	ghostLine  = "\x1b[39m❯ \x1b[2mkeep patrolling\x1b[0m"
	emptyLine  = "\x1b[38;5;246m❯ \x1b[7m \x1b[0m"
	typedLine  = "\x1b[39m❯ gt patrol report --status green"
	// footerBlocked mimics the chrome an AskUserQuestion-style dialog renders
	// in place of the ordinary footer -- no "esc to interrupt", no composer
	// row, just selection-prompt text (hq-79f59).
	footerBlocked = "  Enter to select · ↑/↓ to navigate"
)

func joinPane(lines ...string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func TestAnalyzeTurnState(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    TurnState
	}{
		{
			name: "stopped agent with dim ghost placeholder",
			content: joinPane(
				"  I'll keep verifying completions by pane, as before.",
				"",
				"\x1b[38;5;246m✻\x1b[39m \x1b[38;5;246mWorked for 37s\x1b[39m",
				"",
				boxLine, ghostLine, boxLine, footerIdle,
			),
			want: TurnEnded,
		},
		{
			name: "working agent blocked in its own await call",
			content: joinPane(
				"\x1b[38;5;246m  ⎿  $ gt mol step await-event --channel refinery (4m 18s)\x1b[39m",
				"",
				"\x1b[38;5;174m✶\x1b[39m \x1b[38;5;216mBloviating…\x1b[39m \x1b[38;5;246m(43m 7s · ↓ 12.4k tokens)\x1b[39m",
				"",
				boxLine, emptyLine, boxLine, footerBusy,
			),
			want: TurnActive,
		},
		{
			name: "empty composer with no busy marker is a stopped agent",
			content: joinPane(
				"  Queue empty, nothing outstanding.",
				"",
				boxLine, emptyLine, boxLine, footerIdle,
			),
			want: TurnEnded,
		},
		{
			name: "real typed text in the composer is a stranded submit",
			content: joinPane(
				"  Cycle complete.",
				"",
				boxLine, typedLine, boxLine, footerIdle,
			),
			want: TurnStranded,
		},
		{
			// The regression that matters: agents that discuss pane-reading put
			// the busy marker into their own transcript. A whole-pane scan
			// reports "working" here and the stopped agent is never woken.
			name: "busy marker in transcript prose does not mask a stopped agent",
			content: joinPane(
				"  The discriminator is the status line: if it shows 'esc to interrupt'",
				"  the agent is mid-turn; my whole-pane grep matched this very line.",
				"",
				"\x1b[38;5;246m✻\x1b[39m \x1b[38;5;246mWorked for 2m 10s\x1b[39m",
				"",
				boxLine, ghostLine, boxLine, footerIdle,
			),
			want: TurnEnded,
		},
		{
			name: "ghost text that reads like staged input is still a ghost",
			content: joinPane(
				"  Report filed.",
				"",
				boxLine,
				"\x1b[39m❯ \x1b[2mwhat did the dolt test come back as?\x1b[0m",
				boxLine, footerIdle,
			),
			want: TurnEnded,
		},
		{
			name:    "no composer line in the pane",
			content: joinPane("$ some shell output", "$ more output"),
			want:    TurnUnknown,
		},
		{
			name:    "empty pane",
			content: "",
			want:    TurnUnknown,
		},
		{
			name: "trailing blank lines do not push the footer out of range",
			content: joinPane(
				boxLine, emptyLine, boxLine, footerBusy, "", "", "",
			),
			want: TurnActive,
		},
		{
			// hq-79f59: the Mayor sat on an AskUserQuestion dialog for ~1h50m
			// reporting "running" on every liveness surface. The dialog
			// replaces the composer row entirely — there is no ❯ prompt line
			// at all — so this must not fall into the "no composer" ->
			// TurnUnknown path above it.
			name: "AskUserQuestion dialog blocked on a human, no composer visible",
			content: joinPane(
				"  Which library should we use for date formatting?",
				"",
				"  ❯ 1. date-fns",
				"    2. Luxon",
				"",
				footerBlocked,
			),
			want: TurnBlocked,
		},
		{
			// Mirrors the busy-marker contamination case above: an agent's own
			// transcript can discuss the blocked-detector's marker text
			// without the agent actually being blocked. Anchoring on the
			// trailing footer window (not a whole-pane scan) is what keeps
			// this from misreading a working investigator as blocked.
			name: "blocked marker in transcript prose does not mask a working agent",
			content: joinPane(
				"  The discriminator is 'Enter to select' in the footer;",
				"  my own investigation quoted that exact string here.",
				"",
				boxLine, emptyLine, boxLine, footerBusy,
			),
			want: TurnActive,
		},
		{
			// A footer that (implausibly) carries both markers reads as busy,
			// not blocked — matching the existing isInRewindMode precedent
			// (gt-z8ra) that a generating agent is never also showing a modal
			// dialog to a human.
			name: "busy indicator wins when both markers are in the footer window",
			content: joinPane(
				boxLine, emptyLine, boxLine, footerBusy, footerBlocked,
			),
			want: TurnActive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := analyzeTurnState(tt.content, DefaultReadyPromptPrefix)
			if got != tt.want {
				t.Errorf("analyzeTurnState() = %v, want %v", got, tt.want)
			}
		})
	}
}

// An empty prompt prefix means the composer cannot be located at all. The
// result must be TurnUnknown rather than a confident answer derived from the
// footer alone — a caller acting on "ended" here would be acting on a check
// that never looked at the composer.
func TestAnalyzeTurnStateWithoutPromptPrefix(t *testing.T) {
	content := joinPane(boxLine, ghostLine, boxLine, footerIdle)
	if got := analyzeTurnState(content, ""); got != TurnUnknown {
		t.Errorf("analyzeTurnState(_, \"\") = %v, want %v", got, TurnUnknown)
	}
}

// The busy scan reaches a short way above the composer, because some agent TUIs
// put the marker in the spinner line rather than the status footer. Both sides
// of that boundary are pinned here: a marker in the spinner counts, and one
// further up in the transcript does not.
func TestBusyScanBoundary(t *testing.T) {
	spinnerBusy := "\x1b[38;5;246m✻ Cogitating… (12s · ↑ 2.1k tokens · esc to interrupt)\x1b[39m"
	spinnerIdle := "\x1b[38;5;246m✻\x1b[39m \x1b[38;5;246mWorked for 37s\x1b[39m"

	tests := []struct {
		name    string
		content string
		want    TurnState
	}{
		{
			name: "marker in the spinner line above the box counts as working",
			content: joinPane(
				"  transcript line", "", spinnerBusy, "",
				boxLine, emptyLine, boxLine, footerIdle,
			),
			want: TurnActive,
		},
		{
			name: "marker further up in the transcript does not",
			content: joinPane(
				"  quoting the marker: esc to interrupt", "", spinnerIdle, "",
				boxLine, ghostLine, boxLine, footerIdle,
			),
			want: TurnEnded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := analyzeTurnState(tt.content, DefaultReadyPromptPrefix); got != tt.want {
				t.Errorf("analyzeTurnState() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTurnStateString(t *testing.T) {
	tests := map[TurnState]string{
		TurnUnknown:  "unknown",
		TurnActive:   "active",
		TurnEnded:    "ended",
		TurnStranded: "stranded",
		TurnState(9): "unknown",
	}
	for state, want := range tests {
		if got := state.String(); got != want {
			t.Errorf("TurnState(%d).String() = %q, want %q", int(state), got, want)
		}
	}
}
