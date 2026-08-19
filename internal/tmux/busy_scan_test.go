package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixtures are shared with idle_scan_test.go (plainPrompt, plainFooterBusy, …),
// because the busy side reads the same pane layout from the other direction.

func TestBusyFromLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  bool
	}{
		{
			// The regression. An agent that has been discussing pane-reading
			// carries the busy marker in its own transcript; the whole-pane scan
			// read that as mid-turn, so a session parked at an empty prompt was
			// never reclaimed and every nudge to it lost its Escape.
			name: "busy marker in transcript prose is not evidence of a turn",
			lines: []string{
				"  The discriminator is the status line: if it shows 'esc to interrupt'",
				"  the agent is mid-turn; my whole-pane grep matched this very line.",
				"",
				plainSpinnerIdle,
				"",
				plainBox, plainPrompt, plainBox, plainFooterIdle,
			},
			want: false,
		},
		{
			name: "busy marker in the footer is a turn in flight",
			lines: []string{
				"  ⎿  $ gt mol step await-event --channel refinery (4m 18s)",
				"",
				plainBox, plainPrompt, plainBox, plainFooterBusy,
			},
			want: true,
		},
		{
			name: "busy marker in the spinner above the box is a turn in flight",
			lines: []string{
				"  transcript line",
				"",
				plainSpinnerBusy,
				"",
				plainBox, plainPrompt, plainBox, plainFooterIdle,
			},
			want: true,
		},
		{
			name: "marker far above the composer is out of the status region",
			lines: append(
				append([]string{"  older output quoting esc to interrupt from a log"},
					strings.Split(strings.TrimSuffix(strings.Repeat("transcript filler\n", 20), "\n"), "\n")...),
				plainBox, plainPrompt, plainBox, plainFooterIdle,
			),
			want: false,
		},
		{
			name: "trailing blank padding does not push the footer out of range",
			lines: []string{
				plainBox, plainPrompt, plainBox, plainFooterBusy, "", "", "", "",
			},
			want: true,
		},
		{
			name: "mode-status footer anchors the scan when the composer is missing",
			lines: []string{
				"  transcript mentioning esc to interrupt in passing",
				"  more transcript",
				"  still more transcript",
				"",
				"some other TUI's input line",
				plainFooterIdle,
			},
			want: false,
		},
		{
			// No anchor at all: neither a composer nor the mode footer is on
			// screen, so there is no status region and the scan falls back to
			// the whole snapshot — what every caller did before anchoring. The
			// two callers both fail safe in that direction: a suppressed Escape
			// or a deferred reclaim, never an interrupted or destroyed agent.
			name:  "unreadable pane falls back to the whole-snapshot scan",
			lines: []string{"• Working (2m 18s • esc to interrupt)"},
			want:  true,
		},
		{
			name:  "shell output is not a turn in flight",
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
			if got := busyFromLines(tt.lines, DefaultReadyPromptPrefix); got != tt.want {
				t.Errorf("busyFromLines() = %v, want %v", got, tt.want)
			}
		})
	}
}

// busyFromLines is not the negation of idleFromLines, and callers reason about
// both, so the one pane where they agree on "no" is pinned here rather than left
// to be rediscovered. A pane with no readable layout is neither parked at a
// prompt nor demonstrably working.
func TestBusyAndIdleAreNotComplements(t *testing.T) {
	t.Parallel()

	unreadable := []string{"$ some shell output", "$ more output"}
	if idleFromLines(unreadable, DefaultReadyPromptPrefix) {
		t.Error("idleFromLines() = true on a non-agent pane, want false")
	}
	if busyFromLines(unreadable, DefaultReadyPromptPrefix) {
		t.Error("busyFromLines() = true on a non-agent pane, want false")
	}
}

// renderFixturePane paints lines into a live tmux pane and returns the capture
// once every marker in wantOnScreen is visible.
//
// The pane's process is `cat fixture; sleep`, not an interactive shell, for two
// reasons. A shell would print its own prompt below the fixture, and that prompt
// becomes the anchor (fish uses ❯) — the fixture's composer and footer would
// then sit in scrollback and the pane would no longer be the shape under test.
// It would also make every verdict here shell-dependent. Waiting on content
// before asserting matters for the same family of reasons: an assertion made
// before the fixture renders passes against an empty pane and says nothing
// (gt-qye).
func renderFixturePane(t *testing.T, tm *Tmux, session string, lines []string, wantOnScreen ...string) string {
	t.Helper()

	fixture := filepath.Join(t.TempDir(), "pane.txt")
	if err := os.WriteFile(fixture, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_ = tm.KillSession(session)
	if err := tm.NewSessionWithCommand(session, "", "cat "+fixture+"; sleep 120"); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSession(session) })

	deadline := time.Now().Add(10 * time.Second)
	for {
		captured, err := tm.CapturePane(session, idleCaptureHistoryLines)
		if err == nil {
			rendered := true
			for _, want := range wantOnScreen {
				if !strings.Contains(captured, want) {
					rendered = false
					break
				}
			}
			if rendered {
				return captured
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture pane did not render within timeout; pane:\n%s", captured)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// requireOldScanSaysBusy is the control for the live tests below: it asserts the
// rendered pane still reproduces the defect. This is the predicate the old code
// ran over every captured line; if it stops firing — a reworded fixture, a pane
// too short to hold the transcript — the verdicts above it become vacuous.
func requireOldScanSaysBusy(t *testing.T, pane string) {
	t.Helper()
	if !anyLineHasBusyIndicator(strings.Split(pane, "\n")) {
		t.Fatalf("whole-pane scan no longer sees the marker, so the verdict proves nothing; pane:\n%s", pane)
	}
}

// contaminatedIdlePane is an agent parked at an empty prompt whose transcript
// quotes the busy marker — the live shape of the bug.
var contaminatedIdlePane = []string{
	"  Earlier in this session I explained that the status line reads",
	"  'esc to interrupt' while a turn is in flight.",
	"",
	plainSpinnerIdle,
	"",
	plainBox,
	plainPrompt,
	plainBox,
	plainFooterIdle,
}

// A parked agent whose transcript quotes the busy marker must not read as
// mid-turn. IsBusy's one-way contract is what session reclaim and recovery hang
// off (gt-5tg): a false "busy" means the sessions that most need reclaiming are
// exactly the ones that never are.
func TestIsBusy_LivePaneWithContaminatedTranscript(t *testing.T) {
	tm := newTestTmux(t)

	pane := renderFixturePane(t, tm, "gt-test-is-busy-contaminated",
		contaminatedIdlePane, "esc to interrupt", plainFooterIdle)

	if tm.IsBusy("gt-test-is-busy-contaminated") {
		t.Fatalf("IsBusy on a parked agent whose transcript quotes the busy marker = true, want false; pane:\n%s", pane)
	}
	requireOldScanSaysBusy(t, pane)
}

// The same pane must not cost the nudge its vim-mode Escape: without it, Enter
// leaves the message sitting in an INSERT-mode composer instead of submitting.
func TestShouldSendEscape_LivePaneWithContaminatedTranscript(t *testing.T) {
	tm := newTestTmux(t)

	pane := renderFixturePane(t, tm, "gt-test-escape-contaminated",
		contaminatedIdlePane, "esc to interrupt", plainFooterIdle)

	if !tm.shouldSendEscape("gt-test-escape-contaminated") {
		t.Fatalf("shouldSendEscape on a parked agent whose transcript quotes the busy marker = false, want true; pane:\n%s", pane)
	}
	requireOldScanSaysBusy(t, pane)
}

// The other direction, live: a busy footer under the composer still suppresses
// the Escape and still reads as busy. Anchoring must not have cost the signal
// the two calls exist for.
func TestBusyFooterIsStillDetectedLive(t *testing.T) {
	tm := newTestTmux(t)

	busyPane := []string{
		"  ⎿  $ go test ./internal/tmux/ (18s)",
		"",
		plainBox,
		plainPrompt,
		plainBox,
		plainFooterBusy,
	}
	pane := renderFixturePane(t, tm, "gt-test-busy-footer", busyPane, plainFooterBusy)

	if !tm.IsBusy("gt-test-busy-footer") {
		t.Fatalf("IsBusy on a pane with a busy footer = false, want true; pane:\n%s", pane)
	}
	if tm.shouldSendEscape("gt-test-busy-footer") {
		t.Fatalf("shouldSendEscape on a pane with a busy footer = true, want false; pane:\n%s", pane)
	}
}
