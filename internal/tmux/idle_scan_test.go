package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixtures here are the ANSI-stripped form of a Claude Code pane, because that
// is what CapturePaneLines returns (capture-pane without -e). The prompt
// character is followed by a non-breaking space (U+00A0) on live panes, so at
// least one case below uses it rather than an ordinary space.
const (
	plainBox         = "────────────────────────────────"
	plainPrompt      = "❯ "
	plainPromptNBSP  = "\u276f\u00a0" // live panes use a non-breaking space after the prompt
	plainFooterIdle  = "  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents"
	plainFooterBusy  = "  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt"
	plainSpinnerBusy = "✻ Cogitating… (12s · ↑ 2.1k tokens · esc to interrupt)"
	plainSpinnerIdle = "✻ Worked for 37s"
)

func TestIdleFromLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  bool
	}{
		{
			// The regression this exists for. An agent that has been discussing
			// pane-reading has the busy marker in its own transcript; the old
			// whole-pane scan read that as "working" and the idle agent was
			// never reaped, nudged, or reported idle again.
			name: "busy marker in transcript prose does not mask an idle agent",
			lines: []string{
				"  The discriminator is the status line: if it shows 'esc to interrupt'",
				"  the agent is mid-turn; my whole-pane grep matched this very line.",
				"",
				plainSpinnerIdle,
				"",
				plainBox, plainPrompt, plainBox, plainFooterIdle,
			},
			want: true,
		},
		{
			name: "busy marker in the footer means working",
			lines: []string{
				"  ⎿  $ gt mol step await-event --channel refinery (4m 18s)",
				"",
				plainBox, plainPrompt, plainBox, plainFooterBusy,
			},
			want: false,
		},
		{
			// Some agent TUIs render the marker in the spinner above the box
			// rather than in the footer, which is why the scan reaches upward at
			// all — and why a blind tail-N slice is not the fix.
			name: "busy marker in the spinner above the box means working",
			lines: []string{
				"  transcript line",
				"",
				plainSpinnerBusy,
				"",
				plainBox, plainPrompt, plainBox, plainFooterIdle,
			},
			want: false,
		},
		{
			name: "prompt with a non-breaking space is still the composer",
			lines: []string{
				"  Queue empty, nothing outstanding.",
				"",
				plainBox, plainPromptNBSP + "keep patrolling", plainBox, plainFooterIdle,
			},
			want: true,
		},
		{
			// capture-pane pads to the bottom of the pane and the split adds one
			// more empty element; untrimmed, that padding eats the bounded
			// windows and the footer falls out of range.
			name: "trailing blank padding does not push the footer out of range",
			lines: []string{
				plainBox, plainPrompt, plainBox, plainFooterBusy, "", "", "", "",
			},
			want: false,
		},
		{
			// The capture is the whole visible pane plus scrollback, so lines
			// well above the composer arrive whether or not they were asked for.
			// A pane this tall is the normal case, not an edge case.
			name: "marker far above the composer is out of the status region",
			lines: append(
				append([]string{"  older output quoting esc to interrupt from a log"},
					strings.Split(strings.TrimSuffix(strings.Repeat("transcript filler\n", 20), "\n"), "\n")...),
				plainBox, plainPrompt, plainBox, plainFooterIdle,
			),
			want: true,
		},
		{
			// Fallback anchor: the prompt prefix did not match (another agent
			// TUI, a resized pane), but the mode-status footer is on screen and
			// sits in the same region. The bound is a region, not immunity —
			// text within two non-empty lines of the anchor is inside the status
			// region by design, so the contamination here sits where transcript
			// prose actually sits, several lines up.
			name: "mode-status footer anchors the scan when the composer is missing",
			lines: []string{
				"  transcript mentioning esc to interrupt in passing",
				"  more transcript",
				"  still more transcript",
				"",
				"some other TUI's input line",
				plainFooterIdle,
			},
			want: true,
		},
		{
			name: "mode-status fallback still sees a busy footer",
			lines: []string{
				"  transcript line",
				"  more transcript",
				"  still more transcript",
				"",
				"some other TUI's input line",
				plainFooterBusy,
			},
			want: false,
		},
		{
			name:  "shell output is not an agent pane",
			lines: []string{"$ some shell output", "$ more output"},
			want:  false,
		},
		{
			name:  "empty capture",
			lines: nil,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := idleFromLines(tt.lines, DefaultReadyPromptPrefix); got != tt.want {
				t.Errorf("idleFromLines() = %v, want %v", got, tt.want)
			}
		})
	}
}

// atIdlePromptFromLines differs from idleFromLines in exactly one place: it has
// no mode-status fallback, because its callers are about to type into the
// composer. Both sides of that difference are pinned here.
func TestAtIdlePromptFromLines(t *testing.T) {
	t.Parallel()

	contaminated := []string{
		"  quoting the marker: esc to interrupt",
		"",
		plainSpinnerIdle,
		"",
		plainBox, plainPrompt, plainBox, plainFooterIdle,
	}
	if !atIdlePromptFromLines(contaminated, DefaultReadyPromptPrefix) {
		t.Error("atIdlePromptFromLines() = false on an idle pane whose transcript quotes the marker, want true")
	}

	noComposer := []string{"some other TUI's input line", plainFooterIdle}
	if atIdlePromptFromLines(noComposer, DefaultReadyPromptPrefix) {
		t.Error("atIdlePromptFromLines() = true with no composer on screen, want false")
	}
	if !idleFromLines(noComposer, DefaultReadyPromptPrefix) {
		t.Error("idleFromLines() = false with no composer but an idle footer, want true")
	}
}

// TestIsIdle_LivePaneWithContaminatedTranscript runs the whole path against a
// real tmux capture, so it pins the property that made the bug invisible: the
// capture is not a bounded tail. The pane below is taller than any tail the
// call site asks for, and the busy marker sits at the top of it.
func TestIsIdle_LivePaneWithContaminatedTranscript(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-is-idle-contaminated"

	pane := strings.Join([]string{
		"  Earlier in this session I explained that the status line reads",
		"  'esc to interrupt' while a turn is in flight.",
		"",
		plainSpinnerIdle,
		"",
		plainBox,
		plainPrompt,
		plainBox,
		plainFooterIdle,
	}, "\n") + "\n"

	fixture := filepath.Join(t.TempDir(), "pane.txt")
	if err := os.WriteFile(fixture, []byte(pane), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_ = tm.KillSession(session)
	if err := tm.NewSession(session, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(session) }()

	// The typed command must not itself carry the marker, or it would land
	// below the composer where the scan legitimately looks.
	if err := tm.SendKeys(session, "cat "+fixture); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	// Wait for the fixture to be on screen BEFORE asking IsIdle anything. The
	// test shell's own prompt can match the composer prefix (fish uses ❯), so
	// polling IsIdle until it returns true passes the instant the command is
	// typed — before the contaminating line exists. The verdict is only
	// evidence once the thing that used to break it is actually in the capture.
	deadline := time.Now().Add(10 * time.Second)
	var out string
	for {
		captured, err := tm.CapturePane(session, idleCaptureHistoryLines)
		if err == nil && strings.Contains(captured, "esc to interrupt") && strings.Contains(captured, plainFooterIdle) {
			out = captured
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture pane did not render within timeout; pane:\n%s", captured)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !tm.IsIdle(session) {
		t.Fatalf("IsIdle on an idle pane whose transcript quotes the busy marker = false, want true; pane:\n%s", out)
	}

	// Control: confirm the fixture still reproduces the defect. This is the
	// predicate the old code ran over every captured line; if it stops firing —
	// a reworded fixture, a pane too short to hold the transcript — the pass
	// above becomes vacuous and says nothing about anchoring.
	oldScanSaysBusy := false
	for _, line := range strings.Split(out, "\n") {
		if hasBusyIndicator(line) {
			oldScanSaysBusy = true
			break
		}
	}
	if !oldScanSaysBusy {
		t.Fatalf("whole-pane scan no longer sees the marker, so the idle verdict proves nothing; pane:\n%s", out)
	}
}
