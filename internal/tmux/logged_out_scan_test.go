package tmux

import (
	"strings"
	"testing"
)

// Fixtures are shared with idle_scan_test.go (plainPrompt, plainFooterIdle, …).
// The auth wall is read out of the same status region as the busy marker.

// plainFooterLoggedOut is the status line a logged-out Claude Code renders,
// transcribed from the gastown/foundation pane in gt-acb1.
const plainFooterLoggedOut = "   Not logged in · Run /login"

func TestLoggedOutFromLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  bool
	}{
		{
			// The shape from the incident: a session started without
			// CLAUDE_CONFIG_DIR, one minute old, holding a hooked bead.
			name: "auth wall in the status region",
			lines: []string{
				"● Login expired · Please run /login",
				"✻ Crunched for 0s · done 8:58 PM",
				plainBox, plainPrompt, plainBox, plainFooterLoggedOut,
			},
			want: true,
		},
		{
			// CONTAMINATION, the failure this scan is bounded to avoid. Text
			// ABOUT being logged out satisfies a naive search FOR being logged
			// out, and the agents most likely to have "Not logged in" in their
			// transcript are the ones investigating this very defect. A false
			// positive here parks a healthy polecat on a verdict whose only
			// remedy is a person at a browser.
			name: "auth wall quoted in transcript prose is not evidence",
			lines: []string{
				"  The pane read 'Not logged in · Run /login', which is why the",
				"  restart could not have fixed it — see gt-acb1.",
				"",
				plainSpinnerIdle,
				"",
				plainBox, plainPrompt, plainBox, plainFooterIdle,
			},
			want: false,
		},
		{
			name: "auth wall far above the composer is out of the status region",
			lines: append(
				append([]string{"● Login expired · Please run /login"},
					strings.Split(strings.TrimSuffix(strings.Repeat("transcript filler\n", 20), "\n"), "\n")...),
				plainBox, plainPrompt, plainBox, plainFooterIdle,
			),
			want: false,
		},
		{
			name: "trailing blank padding does not push the marker out of range",
			lines: []string{
				plainBox, plainPrompt, plainBox, plainFooterLoggedOut, "", "", "", "",
			},
			want: true,
		},
		{
			name: "mode-status footer anchors the scan when the composer is missing",
			lines: []string{
				"  transcript", "  more transcript", "",
				"some other TUI's input line",
				plainFooterIdle,
				plainFooterLoggedOut,
			},
			want: true,
		},
		{
			// The one place this deliberately differs from busyFromLines, which
			// falls back to scanning the whole snapshot when it can find no
			// anchor. Here that fallback would be the contamination case with no
			// bound at all. The costs are asymmetric: a false negative leaves
			// things exactly as they were before this function existed, while a
			// false positive sends a working agent to a human.
			name:  "no anchor is no evidence, not a whole-snapshot scan",
			lines: []string{"   Not logged in · Run /login"},
			want:  false,
		},
		{
			name:  "healthy idle pane",
			lines: []string{plainBox, plainPrompt, plainBox, plainFooterIdle},
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
			if got := loggedOutFromLines(tt.lines, DefaultReadyPromptPrefix); got != tt.want {
				t.Errorf("loggedOutFromLines() = %v, want %v", got, tt.want)
			}
		})
	}
}

// requireNaiveScanSeesAuthWall is the control for the contamination case: it
// asserts the fixture still contains the marker a naive whole-pane grep would
// match. Without it, "loggedOutFromLines said false" is indistinguishable from
// "the fixture no longer mentions the marker at all", and the test proves
// nothing about the bound it exists to pin.
func requireNaiveScanSeesAuthWall(t *testing.T, lines []string) {
	t.Helper()
	for _, line := range lines {
		if hasLoggedOutIndicator(line) {
			return
		}
	}
	t.Fatalf("fixture no longer contains an auth-wall marker, so a false verdict proves nothing:\n%s",
		strings.Join(lines, "\n"))
}

func TestLoggedOutScanIsBoundedNotBlind(t *testing.T) {
	t.Parallel()

	contaminated := []string{
		"  The pane read 'Not logged in · Run /login' — see gt-acb1.",
		"",
		plainSpinnerIdle,
		"",
		plainBox, plainPrompt, plainBox, plainFooterIdle,
	}
	requireNaiveScanSeesAuthWall(t, contaminated)
	if loggedOutFromLines(contaminated, DefaultReadyPromptPrefix) {
		t.Error("loggedOutFromLines() = true on transcript prose, want false")
	}

	// Self-validating: the same scan must still find the real thing, or the
	// verdict above is just a scan that never fires.
	real := []string{plainBox, plainPrompt, plainBox, plainFooterLoggedOut}
	if !loggedOutFromLines(real, DefaultReadyPromptPrefix) {
		t.Error("loggedOutFromLines() = false on a real auth wall, want true")
	}
}

// A logged-out pane is not a busy pane and not an idle one either. Pinned
// because the three scans read the same snapshot and callers reason about them
// together: DecideWorkstate checks busy first and logged-out second, which is
// only sound while the two cannot both be true of the same real pane.
func TestLoggedOutPaneIsNotBusy(t *testing.T) {
	t.Parallel()

	loggedOut := []string{
		"● Login expired · Please run /login",
		plainBox, plainPrompt, plainBox, plainFooterLoggedOut,
	}
	if busyFromLines(loggedOut, DefaultReadyPromptPrefix) {
		t.Error("busyFromLines() = true on a logged-out pane, want false")
	}
	if !loggedOutFromLines(loggedOut, DefaultReadyPromptPrefix) {
		t.Error("loggedOutFromLines() = false on a logged-out pane, want true")
	}
}

// TestLoggedOutIndicators pins the markers so an edit to them is intentional
// rather than accidental — the same guard busyIndicators has. These are scraped
// from upstream status text and nothing here can detect a silent rename; if the
// wording changes, this scan fails closed and reports no evidence.
func TestLoggedOutIndicators(t *testing.T) {
	t.Parallel()

	want := map[string]bool{"Not logged in": true, "Login expired": true}
	if len(loggedOutIndicators) != len(want) {
		t.Fatalf("loggedOutIndicators = %q, want exactly %d markers", loggedOutIndicators, len(want))
	}
	for _, marker := range loggedOutIndicators {
		if !want[marker] {
			t.Errorf("unexpected marker %q — adding one widens every logged-out verdict in the tree", marker)
		}
	}
}
