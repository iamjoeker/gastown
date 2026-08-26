package tmux

import (
	"strconv"
	"strings"
	"time"
)

// Liveness is what an agent's pane says the agent is actually DOING, as opposed
// to what its bead says it was dispatched to do.
//
// Every liveness surface in this repo used to have two states where the
// observable world has four, and the two it had were derived from hooked-ness.
// A polecat holding a hooked bead reported "working" whether it was generating,
// blocked in a tool call, sitting at an auth wall, or parked at an empty prompt
// — so gastown/foundation read WORKING for eighteen minutes while its pane said
// "Login expired · Please run /login", and the same variable left the Deacon
// logged out for 3.27 days (gt-acb1, hq-nms9g, gt-y39t).
//
// The four states and the evidence that separates them:
//
//	duration climbing + tokens CLIMBING    -> LivenessWorking
//	duration climbing + tokens STATIC      -> LivenessBlockingWait
//	no activity line + auth-wall marker    -> LivenessLoggedOut
//	no activity line, neither of the above -> LivenessParked
//
// The detector was developed by beads/witness over 33 cycles and independently
// reproduced by the Mayor on two live agents before it was proposed: bd-cheedo
// (clock 51m28s->52m48s, tokens 140.7k->140.7k, pane running `sleep 300`) and
// bd-dementus (clock 36m4s->37m24s, tokens 64.7k->66.4k, no long-running
// command). Both read IDENTICALLY under the `esc to interrupt` detector this
// town had been using. Only the token signature separates them.
//
// THE LIMIT, which is the load-bearing part: tokens-static means NOT THINKING.
// It does not mean unhealthy. A wedged agent and an agent inside `sleep 300`
// produce the same signature — neither consumes tokens. What separates THOSE is
// whether the pane shows a command in flight, which is why LivenessBlockingWait
// is a state and not a verdict, and why SuspectStall (not the state) is what a
// caller escalates on.
type Liveness int

const (
	// LivenessUnknown means the pane could not be read, could not be located as
	// an agent pane, or the two samples do not support a claim. Callers must not
	// act on it: it is the same value a missing session produces.
	LivenessUnknown Liveness = iota

	// LivenessWorking means the activity line's token counter moved between
	// samples. The agent is generating.
	LivenessWorking

	// LivenessBlockingWait means a turn is in flight (the activity line's clock
	// is climbing) but the token counter did not move. The agent is not
	// thinking: it is waiting on something — a tool call, a sleep, a poll, or a
	// wedge. Which of those it is, this state does NOT say. See SuspectStall.
	LivenessBlockingWait

	// LivenessLoggedOut means no turn is in flight and the status region carries
	// the auth-wall marker. It does not clear on its own and no restart path
	// supplies credentials; only a human running /login does.
	LivenessLoggedOut

	// LivenessParked means no turn is in flight and nothing else is wrong. The
	// agent is sitting at its prompt and will not execute again until something
	// external prompts it.
	LivenessParked

	// LivenessTurnInFlight means a turn is running but only ONE sample was
	// taken, so working and blocking-wait could not be separated.
	//
	// It is deliberately not folded into LivenessUnknown. Unknown means "do not
	// act"; this state is actionable in exactly one direction — leave the agent
	// alone — and conflating the two would make a single-sample caller unable to
	// say the one true thing it does know.
	LivenessTurnInFlight
)

func (l Liveness) String() string {
	switch l {
	case LivenessWorking:
		return "working"
	case LivenessBlockingWait:
		return "blocking-wait"
	case LivenessLoggedOut:
		return "logged-out"
	case LivenessParked:
		return "parked"
	case LivenessTurnInFlight:
		return "turn-in-flight"
	default:
		return "unknown"
	}
}

// MinLivenessWindow is the shortest gap between the two samples that
// ClassifyLiveness will treat as able to separate working from blocking-wait.
//
// It is enforced rather than documented because a too-short window does not
// fail loudly — it reports a healthy agent as blocked, which is the false alarm
// this whole detector is supposed to end.
//
// The number is measured, not guessed. Three captures of gastown/witness taken
// twenty seconds apart on 2026-08-25 read clock 37m51s -> 38m11s -> 38m31s with
// tokens 40.8k -> 41.4k -> 41.4k: the third interval was STATIC on an agent
// that was demonstrably generating the whole time. The counter is rendered
// rounded to a tenth of a thousand — one tick is a hundred tokens — so a short
// window cannot resolve it at all. The Mayor's validated reproduction used 75s;
// 60s is the floor below which a static reading says more about the window than
// about the agent.
const MinLivenessWindow = 60 * time.Second

// livenessActivityLookback is how many NON-EMPTY lines above the status anchor
// are searched for the activity line.
//
// It is wider than turnBusyLookback (2) because the activity line does not sit
// in the same place: the busy marker's home is the mode-status footer BELOW the
// composer, while the activity line is the spinner in the transcript region
// above it, and what sits between the two varies. Measured on live panes, the
// gap is two non-empty lines on a bare pane (the composer's top border, then
// the spinner) and four when the TUI has a tip line under the spinner —
// "⎿  Tip: Use /clear to start fresh…" was live on gastown/witness while this
// was written, and a lookback of 2 missed the activity line entirely there.
//
// So the measured requirement is three, and this is three plus one rung of
// slack — not more. Every additional rung is a reach further into the
// transcript, which is where text that merely LOOKS like a status line lives,
// and a test in this package plants a quoted activity line five rungs up
// precisely to hold the bound down: at six it was read as the live spinner.
//
// Two things bound the damage from what does get through. The shape matched is
// specific — a parenthesised segment carrying both a clock and a token count —
// and a quoted line's clock does not climb, which ClassifyLiveness requires
// before it will classify anything (see the duration-static arm there).
const livenessActivityLookback = 4

// livenessCommandLookback is how many non-empty lines above the status anchor
// are searched for evidence that a command is in flight.
//
// It is deliberately the most generous window in this file, because the two
// directions of error are not remotely symmetric. Over-detecting a running
// command suppresses an escalation, which leaves the caller exactly where it
// was before this detector existed. Under-detecting one produces a stall alarm
// for an agent that is healthily sitting in a `sleep` or a test poll — and the
// bead that asked for this detector asked, in the same breath, not to do that:
// treating the healthy long-run case as a stall "produces exactly the false
// alarms this town spent the night chasing".
//
// So this scan fails toward "a command is visible", and the window is sized to
// clear the tool-call block (its header, the command, and its wrapped
// continuation lines) that sits between the transcript body and the spinner.
const livenessCommandLookback = 14

// ActivitySample is one reading of an agent pane's status region: everything
// the four-state detector needs from a single capture.
//
// Present is the pivot. When it is true a turn is in flight and the clock and
// token counter are meaningful; when it is false the turn has ended and
// LoggedOut decides between the two remaining states. The zero value is what an
// unreadable pane produces, and Captured distinguishes that from a pane that
// was read and simply had nothing in flight.
type ActivitySample struct {
	// Captured records that the pane was read and located as an agent pane. A
	// false value means nothing below it carries any information.
	Captured bool

	// Present records that an activity line — a spinner carrying a parenthesised
	// clock and token count — was found in the status region.
	Present bool

	// Duration is the clock the activity line shows. Only meaningful when
	// Present.
	Duration time.Duration

	// Tokens is the token count the activity line shows, expanded from the
	// TUI's abbreviated rendering ("44.5k tokens" -> 44500). Only meaningful
	// when Present.
	//
	// It carries the TUI's rounding, not the agent's real count: the display is
	// quantised to a tenth of a thousand, which is what MinLivenessWindow exists
	// to work around.
	Tokens int

	// LoggedOut records the auth-wall marker in the status region. Read from the
	// same capture as everything else here on purpose — busy and logged-out used
	// to be read from two separate captures taken moments apart, so the two
	// halves of one verdict could describe two different instants.
	LoggedOut bool

	// CommandInFlight records that the pane shows a tool call running. It is the
	// second step of the two-step form: the token signature narrows to
	// not-thinking, and this says whether that was on purpose.
	CommandInFlight bool
}

// LivenessReading is a classified pair of samples, with the evidence that
// produced it kept alongside the answer.
//
// The evidence is carried rather than discarded because two of these states are
// reported to humans who must decide what to do, and a bare state name gives
// them nothing to check it against. `gt polecat check-recovery` printed the same
// prose for two verdicts resting on completely different evidence once already,
// and a reader could not tell which one to believe (gt-mkpm).
type LivenessReading struct {
	State Liveness

	// Sampled records that a second sample was actually taken. When false, State
	// can never be Working or BlockingWait — those require a delta — and a
	// turn in flight reports as LivenessTurnInFlight instead.
	Sampled bool

	// Window is the gap between the two samples, zero when only one was taken.
	Window time.Duration

	// TokenDelta is the movement in the displayed token counter across the
	// window. Only meaningful when Sampled and both samples had an activity
	// line.
	TokenDelta int

	// CommandInFlight is the second sample's reading of whether a tool call was
	// running. See SuspectStall.
	CommandInFlight bool
}

// SuspectStall reports the one shape worth escalating: the agent is not
// consuming tokens AND the pane shows no command in flight to explain why.
//
// This is deliberately a predicate over the reading rather than a fifth state.
// LivenessBlockingWait on its own is not a fault — it is what an agent inside
// `sleep 300` or a long test run looks like, and it is the majority case. The
// fault is blocking-wait with nothing visible that would block on. Reporting
// the state as if it were the fault is precisely the false-alarm generator the
// detector was asked to avoid.
//
// A single-sample reading always answers false: without a delta there is no
// evidence of not-thinking at all, and escalating on that would be escalating
// on the absence of a measurement.
func (r LivenessReading) SuspectStall() bool {
	return r.Sampled && r.State == LivenessBlockingWait && !r.CommandInFlight
}

// ClassifyLiveness derives a [Liveness] from one or two samples of the same
// pane, cur being the later.
//
// Pass a zero-valued prev (and a zero window) for a single-sample read: the two
// states that need no delta — logged-out and parked — are still decided, and a
// turn in flight reports as LivenessTurnInFlight rather than guessing.
//
// The delta arm is guarded three ways, each of which was a way to be confidently
// wrong:
//
//   - The window must reach MinLivenessWindow. A shorter one reports a working
//     agent as blocked; measured live at 20s.
//   - Both samples must show an activity line. A turn that ended between them
//     leaves nothing to compare.
//   - The CLOCK must have climbed. This is the control for a captured line that
//     is not live — a transcript quoting an activity line has a fixed clock,
//     and a frozen TUI is not a blocking wait either. A static clock therefore
//     yields Unknown, never blocking-wait.
func ClassifyLiveness(prev, cur ActivitySample, window time.Duration) LivenessReading {
	r := LivenessReading{CommandInFlight: cur.CommandInFlight}

	if !cur.Captured {
		return r
	}

	if !cur.Present {
		// No turn in flight. Both remaining states are decidable from this one
		// sample, so a single-sample caller gets a full answer here.
		if cur.LoggedOut {
			r.State = LivenessLoggedOut
			return r
		}
		r.State = LivenessParked
		return r
	}

	// A turn IS in flight. Without a usable second sample that is all that can
	// be said, and saying it is better than guessing which half it is.
	if !prev.Captured || !prev.Present || window < MinLivenessWindow {
		r.State = LivenessTurnInFlight
		return r
	}

	if cur.Duration <= prev.Duration {
		// The pane is showing the same clock it showed a minute ago. Whatever
		// this line is, it is not a live activity line, and neither of the two
		// states below is a claim this evidence supports.
		r.State = LivenessUnknown
		return r
	}

	r.Sampled = true
	r.Window = window
	r.TokenDelta = cur.Tokens - prev.Tokens
	if r.TokenDelta > 0 {
		r.State = LivenessWorking
	} else {
		r.State = LivenessBlockingWait
	}
	return r
}

// activityFromLines reads an [ActivitySample] out of a pane snapshot.
//
// It is anchored the same way every other status scan in this package is, and
// for the same reason: the snapshot is the whole visible pane, and an agent's
// own transcript is perfectly capable of containing text that looks like a
// status line. With no anchor at all there is no status region to read, so this
// returns a sample that is not Captured — deliberately unlike busyFromLines,
// which falls back to an unanchored scan. That fallback is safe for busy
// because over-reporting busy costs a suppressed keystroke; here it would feed
// a fabricated clock and token count into a delta.
func activityFromLines(lines []string, promptPrefix string) ActivitySample {
	lines = trimTrailingBlankLines(lines)
	anchor := statusAnchor(lines, promptPrefix)
	if anchor < 0 {
		return ActivitySample{}
	}

	s := ActivitySample{Captured: true}
	// Deliberately the SAME window loggedOutFromLines uses, not the wider one
	// the activity line needs. Two surfaces answering "is this agent logged
	// out?" differently about the same pane would be worse than either being
	// narrow, and this one has no claim to widen it that the other lacks. The
	// narrowness is real — a TUI tip line between the marker and the composer
	// hides it — and is filed as gt-dxcb against loggedOutFromLines, where a fix
	// moves both.
	s.LoggedOut = indicatorNearAnchor(lines, anchor, hasLoggedOutIndicator)
	s.CommandInFlight = linesAboveAnchor(lines, anchor, livenessCommandLookback, hasCommandInFlightMarker)

	if line, ok := findActivityLine(lines, anchor); ok {
		if a, parsed := parseActivityLine(line); parsed {
			s.Present = true
			s.Duration = a.duration
			s.Tokens = a.tokens
		}
	}
	return s
}

// findActivityLine returns the nearest activity-shaped line to the anchor,
// searching the anchor and everything below it first and then up to
// livenessActivityLookback non-empty lines above.
//
// Nearest-first rather than last-wins: the transcript is above, so on a pane
// where two lines both parse, the lower one is the live spinner and the higher
// one is whatever the agent was talking about.
func findActivityLine(lines []string, anchor int) (string, bool) {
	for i := anchor; i < len(lines); i++ {
		if _, ok := parseActivityLine(lines[i]); ok {
			return lines[i], true
		}
	}
	found := ""
	linesAboveAnchor(lines, anchor, livenessActivityLookback, func(line string) bool {
		if _, ok := parseActivityLine(line); ok {
			found = line
			return true
		}
		return false
	})
	if found != "" {
		return found, true
	}
	return "", false
}

// linesAboveAnchor runs match over up to lookback non-empty lines above anchor,
// nearest first, and reports whether any matched. Blank lines are skipped
// rather than counted, so a gap in the pane layout cannot shrink the window —
// the same rule indicatorNearAnchor uses.
func linesAboveAnchor(lines []string, anchor, lookback int, match func(string) bool) bool {
	checked := 0
	for i := anchor - 1; i >= 0 && checked < lookback; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		checked++
		if match(lines[i]) {
			return true
		}
	}
	return false
}

// commandInFlightMarkers are the substrings an agent TUI renders while a tool
// call is executing. Same role as busyIndicators and loggedOutIndicators, and
// the same fragility: it couples to upstream status text, and a silent rename
// fails toward reporting no command — the direction that produces false alarms,
// which is why the scan around it is deliberately wide.
//
// Both were read off a live gastown/brahmin pane on 2026-08-25 while it ran a
// Go test, eight seconds apart:
//
//	● Running 1 shell command · 3s…
//	  Running 1 shell command…
//
// The ellipsis is part of the marker. Without it "Running" matches ordinary
// prose about running things, which is exactly the contamination the anchored
// scan exists to avoid.
var commandInFlightMarkers = []string{"Running"}

// commandBlockMarkers are the substrings that mark a rendered tool-call block —
// the command itself, echoed under its header:
//
//	⎿  $ env BEADS_DOLT_PORT=1 go test ./internal/cmd/ -run
//
// This matches a block whether the command is still running or has already
// finished, which OVER-detects. That is the safe direction here (see
// livenessCommandLookback): a finished command in view suppresses an
// escalation, and the cost of suppressing one is that a stall is reported by
// the next poll instead of this one.
var commandBlockMarkers = []string{"⎿"}

// hasCommandInFlightMarker reports whether a pane line shows a tool call.
func hasCommandInFlightMarker(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, "…") {
		for _, marker := range commandInFlightMarkers {
			if strings.Contains(trimmed, marker) {
				return true
			}
		}
	}
	for _, marker := range commandBlockMarkers {
		if strings.HasPrefix(trimmed, marker) && strings.Contains(trimmed, "$ ") {
			return true
		}
	}
	return false
}

type activityLine struct {
	duration time.Duration
	tokens   int
}

// parseActivityLine pulls the clock and the token count out of an agent's
// spinner line.
//
// The shape is a parenthesised, middle-dot-separated segment carrying both:
//
//	· Symbioting… (17m 5s · ↓ 44.5k tokens · thinking)
//	✻ Cogitating… (12s · ↑ 2.1k tokens · esc to interrupt)
//	✶ Symbioting… (19m 8s · ↓ 54.2k tokens · thought for 5s)
//
// BOTH fields are required. The verb, the spinner glyph, the arrow, and the
// trailing fields all vary between agents, versions, and moments — the verb is
// randomised per turn — so none of them is matched. What does not vary is that
// a turn in flight reports a clock and a token count together, and that a turn
// which has ENDED reports neither:
//
//	✻ Cooked for 16m 21s
//	✻ Baked for 1m 33s
//	✻ Crunched for 0s · done 8:58 PM
//
// Requiring both fields is what separates those two families. Matching on the
// clock alone would read every one of the ended lines above as a turn in
// flight, which is the misreading the whole detector exists to remove.
func parseActivityLine(line string) (activityLine, bool) {
	open := strings.Index(line, "(")
	if open < 0 {
		return activityLine{}, false
	}
	closed := strings.LastIndex(line, ")")
	if closed <= open {
		return activityLine{}, false
	}

	var (
		out       activityLine
		haveClock bool
		haveToken bool
	)
	for _, field := range strings.Split(line[open+1:closed], "·") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if !haveToken {
			if n, ok := parseTokenField(field); ok {
				out.tokens = n
				haveToken = true
				continue
			}
		}
		if !haveClock {
			if d, ok := parseClockField(field); ok {
				out.duration = d
				haveClock = true
			}
		}
	}
	if !haveClock || !haveToken {
		return activityLine{}, false
	}
	return out, true
}

// parseTokenField reads "↓ 44.5k tokens" and its variants into a count.
//
// The magnitude suffix is expanded rather than dropped because the delta is
// taken between two of these, and "44.5k" -> 44 next to "44.5k" -> 44 would
// make every reading static.
func parseTokenField(field string) (int, bool) {
	if !strings.HasSuffix(field, "tokens") && !strings.HasSuffix(field, "token") {
		return 0, false
	}
	field = strings.TrimSuffix(field, "tokens")
	field = strings.TrimSuffix(field, "token")
	fields := strings.Fields(field)
	if len(fields) == 0 {
		return 0, false
	}
	// The arrow (↑/↓) is a separate field; the number is the last one before
	// the unit. Direction is deliberately ignored: it names which side of the
	// exchange the count is for, and both climb while an agent generates.
	num := fields[len(fields)-1]

	mult := 1.0
	switch {
	case strings.HasSuffix(num, "k"), strings.HasSuffix(num, "K"):
		mult = 1000
		num = num[:len(num)-1]
	case strings.HasSuffix(num, "m"), strings.HasSuffix(num, "M"):
		mult = 1000000
		num = num[:len(num)-1]
	}
	num = strings.ReplaceAll(num, ",", "")
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, false
	}
	return int(v * mult), true
}

// parseClockField reads "17m 5s", "12s", or "1h 2m 3s" into a duration.
//
// Written out rather than handed to time.ParseDuration because the TUI renders
// its components space-separated ("17m 5s") and ParseDuration rejects that.
// Every component must parse: a partial match here would let a fragment of
// ordinary prose stand in for a clock.
func parseClockField(field string) (time.Duration, bool) {
	fields := strings.Fields(field)
	if len(fields) == 0 {
		return 0, false
	}
	var total time.Duration
	for _, part := range fields {
		if len(part) < 2 {
			return 0, false
		}
		var unit time.Duration
		switch part[len(part)-1] {
		case 'h':
			unit = time.Hour
		case 'm':
			unit = time.Minute
		case 's':
			unit = time.Second
		default:
			return 0, false
		}
		v, err := strconv.Atoi(part[:len(part)-1])
		if err != nil || v < 0 {
			return 0, false
		}
		total += time.Duration(v) * unit
	}
	return total, true
}

// SampleActivity captures a session's pane once and reads the whole status
// region out of it.
//
// One capture, not three. Busy, logged-out and the activity line used to be
// read by separate calls that each took their own snapshot, so the halves of a
// single verdict could describe different instants of the same pane. They are
// all in the same footer; there is no reason to read it more than once.
//
// The capture is escape-free and scoped to the visible pane, matching every
// other scan here: scrollback is where text that mimics the status region
// lives, and the status region itself is always on screen.
func (t *Tmux) SampleActivity(session string) ActivitySample {
	lines, err := t.CapturePaneLines(session, idleCaptureHistoryLines)
	if err != nil {
		return ActivitySample{}
	}
	return activityFromLines(lines, readyPromptPrefixForSession(t, session))
}

// Liveness reads a session's pane and classifies it into the four states.
//
// window is how long to wait between the two samples. Pass zero for a
// single-sample read — logged-out and parked are still answered, and a turn in
// flight reports as LivenessTurnInFlight. Pass at least MinLivenessWindow to
// separate working from blocking-wait; anything shorter and non-zero is treated
// as a single-sample read rather than silently producing a delta the window
// cannot support.
//
// This BLOCKS for window. That is why it is opt-in at every call site: a
// minute per agent is not a price a status command should pay without being
// asked, and the states that need no delta are available for free.
func (t *Tmux) Liveness(session string, window time.Duration) LivenessReading {
	if window < MinLivenessWindow {
		return ClassifyLiveness(ActivitySample{}, t.SampleActivity(session), 0)
	}
	prev := t.SampleActivity(session)
	time.Sleep(window)
	return ClassifyLiveness(prev, t.SampleActivity(session), window)
}
