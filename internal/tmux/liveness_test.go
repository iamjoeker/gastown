package tmux

import (
	"testing"
	"time"
)

// The fixtures below are lifted verbatim off live agent panes on 2026-08-25
// (gastown/brahmin, gastown/witness, hq/mayor, gastown/crater) via
// `tmux capture-pane -p`, which is what CapturePaneLines returns. They are
// copied rather than reconstructed on purpose: a paraphrase of a status line is
// a rendering of it, and grading a detector against your own memory of the text
// is how a probe gets validated in a frame where it cannot fail.
const (
	liveSpinnerWorking  = "· Symbioting… (17m 5s · ↓ 44.5k tokens · thinking)"
	liveSpinnerWorking2 = "✶ Symbioting… (19m 8s · ↓ 54.2k tokens · thought for 5s)"
	liveSpinnerPlain    = "✢ Simmering… (38m 31s · ↓ 41.4k tokens)"
	liveSpinnerEnded    = "✻ Cooked for 16m 21s"
	liveSpinnerEnded2   = "✻ Baked for 1m 33s"
	liveSpinnerEnded3   = "✻ Crunched for 0s · done 8:58 PM"
	liveTipLine         = "  ⎿  Tip: Use /clear to start fresh when switching topics and free up context"
	liveRunningCmd      = "● Running 1 shell command · 3s…"
	liveRunningCmd2     = "  Running 1 shell command…"
	liveCommandBlock    = "  ⎿  $ env BEADS_DOLT_PORT=1 go test ./internal/cmd/ -run"
	// The auth wall as gastown/foundation rendered it (gt-acb1) — three
	// contiguous lines, with the second marker nearest the composer.
	liveAuthWall     = "● Login expired · Please run /login"
	liveAuthWallDone = "✻ Crunched for 0s · done 8:58 PM"
	liveAuthWallRun  = "   Not logged in · Run /login"
)

func TestParseActivityLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		line     string
		wantOK   bool
		wantDur  time.Duration
		wantToks int
	}{
		{
			name:     "live spinner with a trailing thinking field",
			line:     liveSpinnerWorking,
			wantOK:   true,
			wantDur:  17*time.Minute + 5*time.Second,
			wantToks: 44500,
		},
		{
			name:     "live spinner with a trailing thought-for field",
			line:     liveSpinnerWorking2,
			wantOK:   true,
			wantDur:  19*time.Minute + 8*time.Second,
			wantToks: 54200,
		},
		{
			name:     "live spinner with nothing after the token count",
			line:     liveSpinnerPlain,
			wantOK:   true,
			wantDur:  38*time.Minute + 31*time.Second,
			wantToks: 41400,
		},
		{
			name:     "seconds-only clock and the busy marker inline",
			line:     plainSpinnerBusy,
			wantOK:   true,
			wantDur:  12 * time.Second,
			wantToks: 2100,
		},
		{
			// The three lines below are the reason BOTH fields are required.
			// Each is a turn that has ENDED, and each carries a clock; a
			// detector keyed on the clock alone reads all three as a turn in
			// flight, which is the exact misreading gt-y39t is about.
			name:   "ended turn: past tense with a bare clock",
			line:   liveSpinnerEnded,
			wantOK: false,
		},
		{
			name:   "ended turn: short clock",
			line:   liveSpinnerEnded2,
			wantOK: false,
		},
		{
			name:   "ended turn: zero clock with a done timestamp",
			line:   liveSpinnerEnded3,
			wantOK: false,
		},
		{
			name:   "mode-status footer has parentheses but no clock or tokens",
			line:   plainFooterBusy,
			wantOK: false,
		},
		{
			name:   "auth wall line",
			line:   liveAuthWall,
			wantOK: false,
		},
		{
			name:   "in-flight command line carries a clock but no token count",
			line:   liveRunningCmd,
			wantOK: false,
		},
		{
			name:     "hour component",
			line:     "✻ Ruminating… (1h 2m 3s · ↑ 1.5m tokens)",
			wantOK:   true,
			wantDur:  time.Hour + 2*time.Minute + 3*time.Second,
			wantToks: 1500000,
		},
		{
			name:     "unabbreviated token count",
			line:     "✻ Ruminating… (4s · ↑ 950 tokens)",
			wantOK:   true,
			wantDur:  4 * time.Second,
			wantToks: 950,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseActivityLine(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("parseActivityLine(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.duration != tt.wantDur {
				t.Errorf("duration = %v, want %v", got.duration, tt.wantDur)
			}
			if got.tokens != tt.wantToks {
				t.Errorf("tokens = %d, want %d", got.tokens, tt.wantToks)
			}
		})
	}
}

// TestParseTokenFieldExpandsMagnitude pins the one arithmetic mistake that would
// make this detector silently useless: dropping the "k" turns 44.5k and 48.4k
// into 44 and 48, and truncating instead of scaling turns them both into 44 —
// a permanent, plausible "static" on every working agent in the town.
func TestParseTokenFieldExpandsMagnitude(t *testing.T) {
	t.Parallel()

	first, ok := parseTokenField("↓ 44.5k tokens")
	if !ok {
		t.Fatal("parseTokenField refused a live token field")
	}
	second, ok := parseTokenField("↓ 44.6k tokens")
	if !ok {
		t.Fatal("parseTokenField refused a live token field")
	}
	if second-first != 100 {
		t.Errorf("one displayed tick = %d tokens, want 100 (44.5k -> 44.6k)", second-first)
	}
}

// paneWith assembles a plausible agent pane around the given transcript tail:
// the transcript, then the composer box, then the mode-status footer. It is the
// layout every scan in this package anchors on.
func paneWith(footer string, tail ...string) []string {
	lines := append([]string{
		"  Some earlier transcript content the agent produced.",
		"",
	}, tail...)
	return append(lines, "", plainBox, plainPrompt, plainBox, footer, "", "")
}

func TestActivityFromLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  ActivitySample
	}{
		{
			name:  "working agent: spinner two non-empty lines above the composer",
			lines: paneWith(plainFooterBusy, liveSpinnerWorking),
			want: ActivitySample{
				Captured: true,
				Present:  true,
				Duration: 17*time.Minute + 5*time.Second,
				Tokens:   44500,
			},
		},
		{
			// The layout that a lookback of 2 (turnBusyLookback) misses: the TUI
			// renders a tip line under the spinner, pushing it a rung further
			// from the composer. Live on gastown/witness while this was written.
			name:  "spinner pushed up by a tip line is still found",
			lines: paneWith(plainFooterBusy, liveSpinnerPlain, liveTipLine),
			want: ActivitySample{
				Captured: true,
				Present:  true,
				Duration: 38*time.Minute + 31*time.Second,
				Tokens:   41400,
			},
		},
		{
			name:  "ended turn: no activity line, nothing else wrong",
			lines: paneWith(plainFooterIdle, liveSpinnerEnded),
			want:  ActivitySample{Captured: true},
		},
		{
			// The gt-acb1 pane. No activity line — a logged-out agent is not
			// mid-turn — so the auth-wall marker is what separates it from
			// parked, and ClassifyLiveness turns this exact sample into
			// LivenessLoggedOut.
			name:  "auth wall in the status region",
			lines: paneWith(plainFooterIdle, liveAuthWall, liveAuthWallDone, liveAuthWallRun),
			want:  ActivitySample{Captured: true, LoggedOut: true},
		},
		{
			// A KNOWN NARROWNESS, pinned so it is a documented bound rather than
			// a surprise. The auth-wall scan reuses loggedOutFromLines' window
			// (two non-empty lines above the anchor) so that this sample and
			// Tmux.IsLoggedOut can never disagree about the same pane — two
			// surfaces giving different logged-out answers would be worse than
			// either being narrow. The cost is that a TUI tip line between the
			// marker and the composer hides it, and the sample reads parked.
			//
			// Parked is the safe direction (it prescribes a nudge or a restart,
			// not a human at a browser) but it is still wrong. Filed as gt-dxcb;
			// widening the window is a change to the auth-wall detector, not to
			// this one, and it belongs in loggedOutFromLines so both surfaces
			// move together.
			name:  "a tip line between the auth wall and the composer hides it",
			lines: paneWith(plainFooterIdle, liveAuthWall, liveAuthWallRun, liveTipLine),
			want:  ActivitySample{Captured: true},
		},
		{
			name:  "in-flight command above the spinner",
			lines: paneWith(plainFooterBusy, liveRunningCmd, liveCommandBlock, liveSpinnerWorking),
			want: ActivitySample{
				Captured:        true,
				Present:         true,
				Duration:        17*time.Minute + 5*time.Second,
				Tokens:          44500,
				CommandInFlight: true,
			},
		},
		{
			name:  "in-flight command rendered without its elapsed timer",
			lines: paneWith(plainFooterBusy, liveRunningCmd2, liveCommandBlock, liveSpinnerPlain),
			want: ActivitySample{
				Captured:        true,
				Present:         true,
				Duration:        38*time.Minute + 31*time.Second,
				Tokens:          41400,
				CommandInFlight: true,
			},
		},
		{
			// The tip line begins with the same ⎿ glyph a command block does.
			// Matching on the glyph alone would report a command in flight for
			// every agent the TUI has ever offered a tip to, which suppresses
			// every stall report there is.
			name:  "a tip line is not a command in flight",
			lines: paneWith(plainFooterBusy, liveSpinnerPlain, liveTipLine),
			want: ActivitySample{
				Captured: true,
				Present:  true,
				Duration: 38*time.Minute + 31*time.Second,
				Tokens:   41400,
			},
		},
		{
			// Contamination: an agent discussing this very detector has the
			// shape of an activity line in its own transcript. The anchored scan
			// must not reach that far up.
			name: "an activity line quoted deep in the transcript is not read",
			lines: append([]string{
				"  The detector keys on the spinner, e.g.",
				"  " + liveSpinnerWorking2,
				"  which carries both a clock and a token count.",
				"", "", "", "", "", "", "",
			}, paneWith(plainFooterIdle, liveSpinnerEnded)...),
			want: ActivitySample{Captured: true},
		},
		{
			// No composer, no mode-status line: no status region to read. Unlike
			// busyFromLines this refuses rather than falling back to a
			// whole-snapshot scan, because a fabricated clock and token count
			// would be fed straight into a delta.
			name: "a pane with no anchor is not captured at all",
			lines: []string{
				"  just some output",
				liveSpinnerWorking,
			},
			want: ActivitySample{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := activityFromLines(tt.lines, plainPrompt)
			if got != tt.want {
				t.Errorf("activityFromLines() =\n  %+v\nwant\n  %+v", got, tt.want)
			}
		})
	}
}

func TestClassifyLiveness(t *testing.T) {
	t.Parallel()

	working := func(d time.Duration, toks int) ActivitySample {
		return ActivitySample{Captured: true, Present: true, Duration: d, Tokens: toks}
	}

	tests := []struct {
		name        string
		prev, cur   ActivitySample
		window      time.Duration
		wantState   Liveness
		wantSuspect bool
	}{
		{
			// The Mayor's live reproduction, bd-dementus: clock 36m4s -> 37m24s,
			// tokens 64.7k -> 66.4k.
			name:      "clock and tokens both climbing is working",
			prev:      working(36*time.Minute+4*time.Second, 64700),
			cur:       working(37*time.Minute+24*time.Second, 66400),
			window:    80 * time.Second,
			wantState: LivenessWorking,
		},
		{
			// The Mayor's live reproduction, bd-cheedo: clock 51m28s -> 52m48s,
			// tokens 140.7k -> 140.7k, pane running `sleep 300`. Blocking-wait,
			// and NOT a suspect stall — the command was visible.
			name:      "clock climbing with static tokens and a visible command is a healthy blocking wait",
			prev:      working(51*time.Minute+28*time.Second, 140700),
			cur:       ActivitySample{Captured: true, Present: true, Duration: 52*time.Minute + 48*time.Second, Tokens: 140700, CommandInFlight: true},
			window:    80 * time.Second,
			wantState: LivenessBlockingWait,
		},
		{
			// Same signature, nothing on screen to be waiting on. This is the one
			// shape worth escalating, and the one a working/parked binary can
			// never surface.
			name:        "clock climbing with static tokens and no command is the escalating case",
			prev:        working(51*time.Minute+28*time.Second, 140700),
			cur:         working(52*time.Minute+48*time.Second, 140700),
			window:      80 * time.Second,
			wantState:   LivenessBlockingWait,
			wantSuspect: true,
		},
		{
			// The measured false positive. gastown/witness read 40.8k -> 41.4k ->
			// 41.4k across two 20s windows while demonstrably generating; the
			// third reading was static purely because the counter is displayed
			// rounded. A short window must not be allowed to produce a verdict.
			name:      "a window shorter than the minimum cannot claim blocking-wait",
			prev:      working(38*time.Minute+11*time.Second, 41400),
			cur:       working(38*time.Minute+31*time.Second, 41400),
			window:    20 * time.Second,
			wantState: LivenessTurnInFlight,
		},
		{
			// The control for a captured line that is not live. A transcript
			// quoting an activity line, or a frozen TUI, shows the same clock
			// twice — and neither of the two delta states is a claim that
			// evidence supports.
			name:      "a clock that did not climb yields unknown, not blocking-wait",
			prev:      working(52*time.Minute+48*time.Second, 140700),
			cur:       working(52*time.Minute+48*time.Second, 140700),
			window:    80 * time.Second,
			wantState: LivenessUnknown,
		},
		{
			name:      "no activity line plus the auth wall is logged-out",
			cur:       ActivitySample{Captured: true, LoggedOut: true},
			wantState: LivenessLoggedOut,
		},
		{
			name:      "no activity line and nothing else is parked",
			cur:       ActivitySample{Captured: true},
			wantState: LivenessParked,
		},
		{
			name:      "an unreadable pane is unknown",
			cur:       ActivitySample{},
			wantState: LivenessUnknown,
		},
		{
			name:      "one sample with a turn in flight does not guess which half",
			cur:       working(17*time.Minute+5*time.Second, 44500),
			wantState: LivenessTurnInFlight,
		},
		{
			name:      "a turn that ended between samples is classified from the later one",
			prev:      working(17*time.Minute+5*time.Second, 44500),
			cur:       ActivitySample{Captured: true},
			window:    90 * time.Second,
			wantState: LivenessParked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyLiveness(tt.prev, tt.cur, tt.window)
			if got.State != tt.wantState {
				t.Errorf("ClassifyLiveness() state = %v, want %v", got.State, tt.wantState)
			}
			if got.SuspectStall() != tt.wantSuspect {
				t.Errorf("SuspectStall() = %v, want %v (state=%v)", got.SuspectStall(), tt.wantSuspect, got.State)
			}
		})
	}
}

// TestSuspectStallNeedsTwoSamples pins the guard that keeps a cheap caller from
// arming an escalation. A single-sample read has no evidence of not-thinking at
// all, so escalating on it would be escalating on the absence of a measurement.
func TestSuspectStallNeedsTwoSamples(t *testing.T) {
	t.Parallel()

	r := LivenessReading{State: LivenessBlockingWait, CommandInFlight: false}
	if r.SuspectStall() {
		t.Error("SuspectStall() was true for an unsampled reading")
	}
	r.Sampled = true
	if !r.SuspectStall() {
		t.Error("SuspectStall() was false for a sampled blocking-wait with no command in flight")
	}
}

// TestLivenessStringsAreDistinct guards the surface names. They are printed by
// `gt rig status` and carried in check-recovery's JSON, so two states sharing a
// string would make the two halves of a disagreement indistinguishable to a
// reader — the failure mode gt-mkpm is about.
func TestLivenessStringsAreDistinct(t *testing.T) {
	t.Parallel()

	seen := map[string]Liveness{}
	for _, l := range []Liveness{
		LivenessUnknown, LivenessWorking, LivenessBlockingWait,
		LivenessLoggedOut, LivenessParked, LivenessTurnInFlight,
	} {
		s := l.String()
		if prev, dup := seen[s]; dup {
			t.Errorf("Liveness(%d) and Liveness(%d) both render as %q", prev, l, s)
		}
		seen[s] = l
	}
}

// TestMinLivenessWindowIsAMinute pins the floor to the measurement behind it.
// Lowering it is what a future caller impatient with a blocking status command
// would reach for, and the cost is a healthy agent reported as blocked.
func TestMinLivenessWindowIsAMinute(t *testing.T) {
	t.Parallel()

	if MinLivenessWindow < time.Minute {
		t.Fatalf("MinLivenessWindow = %v; a 20s window was measured reporting a working agent as static", MinLivenessWindow)
	}
}
