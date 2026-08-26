package deacon

import (
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/awaitprobe"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/tmux"
)

func at(base time.Time, d time.Duration) time.Time { return base.Add(d) }

// parked is what a Deacon asleep in await-signal looks like to both signals: a
// live await process, and a pane whose turn is in flight inside it.
var parked = LivenessSignals{Await: awaitprobe.StatePending, Turn: tmux.TurnActive}

// stopped is the wedge: no turn running, and no await pending to start one.
var stopped = LivenessSignals{Await: awaitprobe.StateAbsent, Turn: tmux.TurnEnded}

func TestNextObservation_FirstReadingStartsWindow(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	hb := &Heartbeat{Timestamp: now, Cycle: 421}

	obs := NextObservation(nil, hb, now)

	if obs == nil {
		t.Fatal("NextObservation returned nil for a non-nil heartbeat")
	}
	if obs.Cycle != 421 {
		t.Errorf("Cycle = %d, want 421", obs.Cycle)
	}
	if !obs.FirstSeen.Equal(now) {
		t.Errorf("FirstSeen = %v, want %v", obs.FirstSeen, now)
	}
}

func TestNextObservation_NilHeartbeat(t *testing.T) {
	now := time.Now()
	if obs := NextObservation(nil, nil, now); obs != nil {
		t.Errorf("NextObservation(nil, nil) = %+v, want nil", obs)
	}
}

func TestNextObservation_CycleAdvanceResetsWindow(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	prev := &CycleObservation{
		Cycle:              421,
		FirstSeen:          base,
		LastSeen:           at(base, 5*time.Minute),
		HeartbeatTimestamp: base,
	}
	hb := &Heartbeat{Timestamp: at(base, 6*time.Minute), Cycle: 422}

	obs := NextObservation(prev, hb, at(base, 6*time.Minute))

	if obs.Cycle != 422 {
		t.Errorf("Cycle = %d, want 422", obs.Cycle)
	}
	if !obs.FirstSeen.Equal(at(base, 6*time.Minute)) {
		t.Errorf("FirstSeen = %v, want the advance time — the stall window must reset", obs.FirstSeen)
	}
}

// A frozen cycle extends one stall window rather than starting a new one, so
// StallDuration measures the whole freeze however many times it is polled.
func TestNextObservation_FrozenCycleExtendsOneWindow(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	frozen := &Heartbeat{Timestamp: base, Cycle: 421}

	obs := NextObservation(nil, frozen, base)
	for i := 1; i <= 5; i++ {
		tick := at(base, time.Duration(i)*time.Minute)
		obs = NextObservation(obs, frozen, tick)
	}

	if !obs.FirstSeen.Equal(base) {
		t.Errorf("FirstSeen = %v, want %v — the stall window must not reset", obs.FirstSeen, base)
	}
	if got := obs.StallDuration(at(base, 5*time.Minute)); got != 5*time.Minute {
		t.Errorf("StallDuration = %v, want 5m", got)
	}
}

// The two signals cover each other's blind spot, so each one alone must be
// inconclusive. Getting either direction wrong reintroduces a known failure:
// treating an ended turn as stopped wakes agents whose await was backgrounded
// (gt-ghw7), and treating an absent await as stopped condemns a Deacon that is
// simply computing.
func TestLivenessSignals_Stopped(t *testing.T) {
	tests := []struct {
		name string
		sig  LivenessSignals
		want bool
	}{
		{
			name: "no await and an ended turn is stopped",
			sig:  LivenessSignals{Await: awaitprobe.StateAbsent, Turn: tmux.TurnEnded},
			want: true,
		},
		{
			name: "no await and a stranded composer is stopped",
			sig:  LivenessSignals{Await: awaitprobe.StateAbsent, Turn: tmux.TurnStranded},
			want: true,
		},
		{
			name: "a live await means parked, whatever the pane says",
			sig:  LivenessSignals{Await: awaitprobe.StatePending, Turn: tmux.TurnEnded},
			want: false,
		},
		{
			name: "a turn in flight means working, even with no await",
			sig:  LivenessSignals{Await: awaitprobe.StateAbsent, Turn: tmux.TurnActive},
			want: false,
		},
		{
			name: "an unreadable process table decides nothing",
			sig:  LivenessSignals{Await: awaitprobe.StateUnknown, Turn: tmux.TurnEnded},
			want: false,
		},
		{
			name: "an unreadable pane decides nothing",
			sig:  LivenessSignals{Await: awaitprobe.StateAbsent, Turn: tmux.TurnUnknown},
			want: false,
		},
		{
			name: "the zero value decides nothing",
			sig:  LivenessSignals{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sig.Stopped(); got != tt.want {
				t.Errorf("Stopped() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The regression that guards the Mayor's safety correction on gt-bvo: a Deacon
// parked in await-signal advances neither cycle nor heartbeat, and must stay
// unwedged for an unbounded sleep. An earlier design used a 20m elapsed-stall
// backstop here, which condemned healthy sleeping Deacons.
func TestEvaluateHealth_LongIdleSleepIsNeverWedged(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	frozen := &Heartbeat{Timestamp: base, Cycle: 421}

	for i := 1; i <= 240; i++ { // four hours of polling a sleeping Deacon
		tick := at(base, time.Duration(i)*time.Minute)

		if got := EvaluateHealth(frozen, tick, DefaultHealthThresholds(), parked); got == VerdictWedged {
			t.Fatalf("EvaluateHealth() = %q after %dm of legitimate sleep, want any non-wedged verdict", got, i)
		}
	}
}

// The core acceptance criterion from gt-bvo, rebuilt on signals that exist: a
// Deacon sitting at its prompt with no await pending is wedged, and says so
// while every other probe the town has still reads healthy.
func TestEvaluateHealth_StoppedMidPatrolIsWedged(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	th := DefaultHealthThresholds()
	hb := &Heartbeat{Timestamp: base, Cycle: 421, LastAction: "cycle 415 closed (abbreviated)"}
	now := at(base, ageStale)

	// Precondition: the age flags this replaces still read as an ordinary stale
	// heartbeat, so the wedge verdict is carrying information nothing else does.
	if age := now.Sub(hb.Timestamp); age < th.Stale || age >= th.VeryStale {
		t.Fatalf("precondition: age %s must be an ordinary stale heartbeat, or this test proves nothing", age)
	}

	got := EvaluateHealth(hb, now, th, stopped)

	if got != VerdictWedged {
		t.Errorf("EvaluateHealth() = %q, want %q", got, VerdictWedged)
	}
	if got.Healthy() {
		t.Error("a wedged Deacon must not report Healthy()")
	}
}

func TestEvaluateHealth(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		hb   *Heartbeat
		now  time.Time
		sig  LivenessSignals
		want Verdict
	}{
		{
			name: "no heartbeat is unknown",
			hb:   nil,
			now:  base,
			sig:  stopped,
			want: VerdictUnknown,
		},
		{
			name: "recent heartbeat is fresh",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			now:  at(base, time.Minute),
			sig:  parked,
			want: VerdictFresh,
		},
		{
			// The age gate is what buys out the races: a turn that ended a
			// moment ago, a session mid-respawn, and a Deacon between two steps
			// all read stopped for an instant, and all refresh the heartbeat
			// well inside this window.
			name: "a fresh heartbeat outranks the stopped signals",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			now:  at(base, ageFresh),
			sig:  stopped,
			want: VerdictFresh,
		},
		{
			name: "aging heartbeat with a live await is stale",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			now:  at(base, ageStale),
			sig:  parked,
			want: VerdictStale,
		},
		{
			// A long patrol step freezes the heartbeat and has no await
			// process. The pane is what tells it apart from a stopped Deacon.
			name: "a working Deacon with no await is stale, not wedged",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			now:  at(base, ageStale),
			sig:  LivenessSignals{Await: awaitprobe.StateAbsent, Turn: tmux.TurnActive},
			want: VerdictStale,
		},
		{
			// Absence of an await cannot be established without a process
			// table, so a host that cannot be read falls back to age.
			name: "no process table means no wedge verdict",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			now:  at(base, ageVeryStale),
			sig:  LivenessSignals{Await: awaitprobe.StateUnknown, Turn: tmux.TurnEnded},
			want: VerdictVeryStale,
		},
		{
			// TurnState reports unknown for a session that does not exist, so a
			// Deacon that died keeps the age verdict. That is the right remedy
			// signal: restart it, do not go looking at its composer.
			name: "a dead session is very stale, not wedged",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			now:  at(base, ageVeryStale),
			sig:  LivenessSignals{Await: awaitprobe.StateAbsent, Turn: tmux.TurnUnknown},
			want: VerdictVeryStale,
		},
		{
			name: "old heartbeat with a live await is very stale",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			now:  at(base, ageVeryStale),
			sig:  parked,
			want: VerdictVeryStale,
		},
		{
			name: "stopped past the very stale threshold is still wedged",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			now:  at(base, ageVeryStale),
			sig:  stopped,
			want: VerdictWedged,
		},
		{
			// patrol-wake refuses to type into a stranded composer, so nothing
			// in the town recovers this one automatically. It must be reported.
			name: "a stranded composer is wedged",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			now:  at(base, ageStale),
			sig:  LivenessSignals{Await: awaitprobe.StateAbsent, Turn: tmux.TurnStranded},
			want: VerdictWedged,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EvaluateHealth(tt.hb, tt.now, DefaultHealthThresholds(), tt.sig); got != tt.want {
				t.Errorf("EvaluateHealth() = %q, want %q", got, tt.want)
			}
		})
	}
}

// gt-dndw proposed keying the verdict on heartbeat.LastAction: the patrol
// formula writes "pre-await checkpoint" immediately before parking, so a frozen
// heartbeat resting on anything else would mean stopped mid-patrol. LastAction
// is typed by the Deacon rather than written by the code that parks, and the
// strings below are what it actually contains — every one transcribed from a
// recorded `gt deacon status --json` poll of a LEGITIMATELY SLEEPING hq-deacon.
// Of 323 recorded readings only 9 carry the exact string; 157 paraphrase it.
//
// So the verdict must not read the field at all: parked is parked whatever the
// Deacon called it.
func TestEvaluateHealth_SleepVerdictIgnoresLastAction(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	now := at(base, ageVeryStale) // past very stale, where an exact-match rule would condemn

	sleeping := []string{
		"pre-await checkpoint",                      // the formula's spelling
		"await-signal blocking, patrol 2761 closed", // the most common paraphrase
		"patrol 3070 closed, awaiting signal",
		"patrol 2668 complete, awaiting signal",
		"patrol 2751 closing",
		"recovering from 48th await-signal kill",
		"", // heartbeats written by Touch carry no action at all
	}

	for _, action := range sleeping {
		t.Run(action, func(t *testing.T) {
			hb := &Heartbeat{Timestamp: base, Cycle: 421, LastAction: action}
			if got := EvaluateHealth(hb, now, DefaultHealthThresholds(), parked); got == VerdictWedged {
				t.Errorf("EvaluateHealth() = %q for a parked Deacon whose last action was %q, want any non-wedged verdict",
					got, action)
			}
		})
	}

	// The mirror: a Deacon that stopped just after writing the formula's own
	// pre-await string is wedged, which is the false NEGATIVE an exact-match
	// suppression rule would have produced. The await it announced is not there.
	hb := &Heartbeat{Timestamp: base, Cycle: 421, LastAction: "pre-await checkpoint"}
	if got := EvaluateHealth(hb, now, DefaultHealthThresholds(), stopped); got != VerdictWedged {
		t.Errorf("EvaluateHealth() = %q for a Deacon stopped after announcing a park it never entered, want %q",
			got, VerdictWedged)
	}
}

func TestObserveCycle_RoundTripsThroughDisk(t *testing.T) {
	townRoot := t.TempDir()
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	first := ObserveCycle(townRoot, &Heartbeat{Timestamp: base, Cycle: 421}, base)
	if first == nil {
		t.Fatal("ObserveCycle returned nil on first observation")
	}

	// Second poll, same cycle: the stall window must survive the disk round-trip
	// rather than restarting at every poll.
	tick := at(base, 2*time.Minute)
	second := ObserveCycle(townRoot, &Heartbeat{Timestamp: base, Cycle: 421}, tick)

	if !second.FirstSeen.Equal(base) {
		t.Errorf("FirstSeen = %v, want %v", second.FirstSeen, base)
	}
	if got := second.StallDuration(tick); got != 2*time.Minute {
		t.Errorf("StallDuration = %v, want 2m", got)
	}

	if stored := ReadCycleObservation(townRoot); stored == nil || !stored.FirstSeen.Equal(base) {
		t.Errorf("ReadCycleObservation() = %+v, want the persisted second observation", stored)
	}
}

// CycleFrozen is display-only: it reports elapsed stall and must NOT be read as
// a wedge, because legitimate await-signal sleep freezes the cycle identically.
func TestCycleFrozen_IsElapsedTimeOnly(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	obs := &CycleObservation{Cycle: 421, FirstSeen: base}

	if obs.CycleFrozen(at(base, 19*time.Minute), CycleStallThreshold) {
		t.Error("CycleFrozen() = true below the stall threshold")
	}
	if !obs.CycleFrozen(at(base, 21*time.Minute), CycleStallThreshold) {
		t.Error("CycleFrozen() = false past the stall threshold")
	}
	if (*CycleObservation)(nil).CycleFrozen(base, CycleStallThreshold) {
		t.Error("nil observation must not report frozen")
	}
}

// The gt-bvo wedge as it was actually recorded, replayed against the detector
// built to catch it.
//
// The timings are the Mayor's transcript, not the bead's retelling of it. The
// bead reported two polls "4 minutes apart" whose ages differed by 45s, which
// implied the heartbeat timestamp advanced under a frozen cycle; gt-s6r
// recovered the raw records and the polls are 45.06s apart, so the heartbeat was
// FROZEN. Twelve polls of cycle 421 by four sessions reconstruct one write:
//
//	23:23:49.4    the only write cycle 421 ever got
//	23:27:14.437  age 3m25s  cycle 421   Mayor poll 1
//	23:27:59.497  age 4m10s  cycle 421   Mayor poll 2
//
// The Mayor's own account of the session supplies the liveness signals the
// heartbeat file cannot: the pane held "continue patrolling", typed and never
// submitted, so the turn had ended with real text in the composer and no await
// was running.
func TestEvaluateHealth_FieldTraceFromBead(t *testing.T) {
	th := DefaultHealthThresholds()

	write := time.Date(2026, 8, 2, 23, 23, 49, 437_000_000, time.UTC)
	hb := &Heartbeat{Timestamp: write, Cycle: 421, LastAction: "cycle 415 closed (abbreviated)"}

	p1 := time.Date(2026, 8, 2, 23, 27, 14, 437_000_000, time.UTC)
	p2 := time.Date(2026, 8, 2, 23, 27, 59, 497_000_000, time.UTC)

	if gap := p2.Sub(p1); gap > time.Minute {
		t.Fatalf("precondition: the polls are 45s apart, not %s — the trace has been altered", gap)
	}
	if age1, age2 := p1.Sub(write), p2.Sub(write); age2-age1 != p2.Sub(p1) {
		t.Fatalf("precondition: age must track elapsed time exactly for a frozen writer, got %s vs %s",
			age2-age1, p2.Sub(p1))
	}

	wedged := LivenessSignals{Await: awaitprobe.StateAbsent, Turn: tmux.TurnStranded}

	// Both Mayor polls land inside the fresh window, so both still read
	// fresh. That is the reporting bug gt-bvo filed and it is not fixable by a
	// verdict: at 3m25s the Deacon is not yet distinguishable from one that is
	// simply between steps.
	for _, p := range []time.Time{p1, p2} {
		if got := EvaluateHealth(hb, p, th, wedged); got != VerdictFresh {
			t.Errorf("EvaluateHealth() at %s = %q, want %q — inside the fresh window nothing is concluded",
				p.Sub(write), got, VerdictFresh)
		}
	}

	// 50s after the Mayor gave up waiting, the heartbeat crosses stale and the
	// two signals are still saying the same thing. Age alone reported "stale"
	// here — the same verdict a Deacon asleep in backoff gets.
	atStale := write.Add(th.Stale)
	if got := EvaluateHealth(hb, atStale, th, wedged); got != VerdictWedged {
		t.Errorf("EvaluateHealth() at the stale threshold = %q, want %q", got, VerdictWedged)
	}

	// The counterfactual that makes the assertion above mean something: the same
	// heartbeat, at the same instant, on a Deacon that was merely asleep.
	if got := EvaluateHealth(hb, atStale, th, parked); got != VerdictStale {
		t.Errorf("EvaluateHealth() for a parked Deacon on the same trace = %q, want %q", got, VerdictStale)
	}
}

// The three false positives duly_noted/witness recorded on 2026-08-02, which
// are the reason the Mayor's safety correction forbids condemning a Deacon on a
// frozen counter alone. All three were the SAME Deacon, healthy, mid-turn:
//
//	cycle 423 frozen 2m+     live spinner "Crunching... 2m 34s", empty input
//	cycle 425 frozen ~3m     active Bash tool call, "esc to interrupt", empty input
//	cycle 425 frozen 4m9s    live spinner "Crunching... 9m 26s", empty input
//
// The witness nearly escalated the first one and then derived the rule this
// verdict implements: "WORKING = frozen cycle + live spinner or active tool call
// + EMPTY input box". A working turn has no await process either, so the pane is
// the only thing separating these from the wedge.
//
// The last entry is the one that keeps this test from proving nothing. The four
// recorded ages all sit inside the fresh window now that the stale threshold
// covers a whole patrol cycle, so on those four the age gate decides and the
// pane is never consulted. The relative case restates the same claim where it
// still bites: a working turn that has outlived the stale threshold — which the
// gt-cbd measurements show happens, cycles reaching 957s — is saved by the pane
// and by nothing else.
func TestEvaluateHealth_KnownFalsePositivesStayUnwedged(t *testing.T) {
	base := time.Date(2026, 8, 2, 23, 0, 0, 0, time.UTC)
	th := DefaultHealthThresholds()

	// Empty input box with a live spinner: no turn ended, nothing stranded.
	working := LivenessSignals{Await: awaitprobe.StateAbsent, Turn: tmux.TurnActive}

	frozen := []struct {
		name string
		age  time.Duration
	}{
		{"cycle 423 frozen 2m", 2 * time.Minute},
		{"cycle 425 frozen 3m", 3 * time.Minute},
		{"cycle 425 frozen 4m9s", 4*time.Minute + 9*time.Second},
		{"the same turn still crunching at 9m26s", 9*time.Minute + 26*time.Second},
		{"a turn still crunching past the stale threshold", th.Stale + time.Minute},
	}

	for _, f := range frozen {
		t.Run(f.name, func(t *testing.T) {
			hb := &Heartbeat{Timestamp: base, Cycle: 425, LastAction: "starting patrol cycle 416"}
			now := at(base, f.age)

			if got := EvaluateHealth(hb, now, th, working); got == VerdictWedged {
				t.Errorf("EvaluateHealth() = %q for a Deacon working with an empty composer, want any non-wedged verdict", got)
			}

			// Non-vacuity: at this same age, the same heartbeat on a Deacon whose
			// turn had ended is judged on the pane alone, so the case above is
			// being decided by the signal it claims to be decided by.
			want := VerdictWedged
			if f.age < th.Stale {
				want = VerdictFresh
			}
			if got := EvaluateHealth(hb, now, th, stopped); got != want {
				t.Errorf("EvaluateHealth() with an ended turn = %q, want %q", got, want)
			}
		})
	}
}

func TestObserveCycle_NoHeartbeat(t *testing.T) {
	townRoot := t.TempDir()
	if obs := ObserveCycle(townRoot, nil, time.Now()); obs != nil {
		t.Errorf("ObserveCycle with no heartbeat = %+v, want nil", obs)
	}
}

func TestReadCycleObservation_MissingAndCorrupt(t *testing.T) {
	townRoot := t.TempDir()

	if obs := ReadCycleObservation(townRoot); obs != nil {
		t.Errorf("ReadCycleObservation on empty town = %+v, want nil", obs)
	}

	if err := WriteCycleObservation(townRoot, &CycleObservation{Cycle: 7}); err != nil {
		t.Fatalf("WriteCycleObservation: %v", err)
	}
	if obs := ReadCycleObservation(townRoot); obs == nil || obs.Cycle != 7 {
		t.Errorf("ReadCycleObservation() = %+v, want Cycle 7", obs)
	}
}

// HealthThresholdsFrom is the only path by which operational.deacon reaches a
// verdict. Both keys were documented as the escape hatch for a badly calibrated
// threshold, and nothing read them — the daemon and `gt deacon status` compared
// against the compiled-in constants, so an operator setting them saw no change
// and no error (gt-cbd).
func TestHealthThresholdsFrom(t *testing.T) {
	t.Run("nil config falls back to the compiled-in defaults", func(t *testing.T) {
		if got := HealthThresholdsFrom(nil); got != DefaultHealthThresholds() {
			t.Errorf("HealthThresholdsFrom(nil) = %+v, want %+v", got, DefaultHealthThresholds())
		}
	})

	t.Run("an empty config falls back to the compiled-in defaults", func(t *testing.T) {
		if got := HealthThresholdsFrom(&config.DeaconThresholds{}); got != DefaultHealthThresholds() {
			t.Errorf("HealthThresholdsFrom(empty) = %+v, want %+v", got, DefaultHealthThresholds())
		}
	})

	t.Run("configured values are used", func(t *testing.T) {
		th := HealthThresholdsFrom(&config.DeaconThresholds{
			HeartbeatStaleThreshold:     "9m",
			HeartbeatVeryStaleThreshold: "42m",
		})
		if th.Stale != 9*time.Minute {
			t.Errorf("Stale = %s, want 9m", th.Stale)
		}
		if th.VeryStale != 42*time.Minute {
			t.Errorf("VeryStale = %s, want 42m", th.VeryStale)
		}

		// The verdict has to move with them, or the wiring is decorative.
		base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
		hb := &Heartbeat{Timestamp: base, Cycle: 421}
		if got := EvaluateHealth(hb, at(base, 10*time.Minute), th, parked); got != VerdictStale {
			t.Errorf("EvaluateHealth() at 10m under a 9m stale threshold = %q, want %q", got, VerdictStale)
		}
		if got := EvaluateHealth(hb, at(base, 10*time.Minute), DefaultHealthThresholds(), parked); got != VerdictFresh {
			t.Errorf("control: the same age under the defaults = %q, want %q — the override is not what moved the verdict",
				got, VerdictFresh)
		}
	})
}

// The measurements gt-cbd was filed on, kept as an executable record. These are
// the only numbers that were actually recorded: 30 consecutive closed
// mol-deacon-patrol wisps, min 224s, mean 604s, max 957s. A threshold below the
// mean labels a healthy Deacon stale for most of its life, which is what 5m did
// (29 of 30 cycles, 50.7% of wall-clock).
//
// Deliberately asserted against the recorded summary statistics and not against
// a reconstructed sample: the 30 individual durations were not preserved, and
// inventing them would make this test agree with whatever it was built to agree
// with.
func TestThresholdsClearTheMeasuredCycleDistribution(t *testing.T) {
	const (
		measuredMeanCycle = 604 * time.Second
		measuredMaxCycle  = 957 * time.Second
	)

	if HeartbeatStaleThreshold <= measuredMeanCycle {
		t.Errorf("HeartbeatStaleThreshold = %s does not clear the measured mean patrol cycle (%s): "+
			"a healthy Deacon reads stale on most cycles",
			HeartbeatStaleThreshold, measuredMeanCycle)
	}
	if HeartbeatVeryStaleThreshold <= measuredMaxCycle {
		t.Errorf("HeartbeatVeryStaleThreshold = %s does not clear the longest measured patrol cycle (%s): "+
			"the daemon would kill and restart a Deacon on a normal long cycle",
			HeartbeatVeryStaleThreshold, measuredMaxCycle)
	}
}
