package deacon

import (
	"testing"
	"time"
)

func at(base time.Time, d time.Duration) time.Time { return base.Add(d) }

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
	if obs.TimestampAdvances != 0 {
		t.Errorf("TimestampAdvances = %d, want 0", obs.TimestampAdvances)
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
		TimestampAdvances:  3,
	}
	hb := &Heartbeat{Timestamp: at(base, 6*time.Minute), Cycle: 422}

	obs := NextObservation(prev, hb, at(base, 6*time.Minute))

	if obs.Cycle != 422 {
		t.Errorf("Cycle = %d, want 422", obs.Cycle)
	}
	if !obs.FirstSeen.Equal(at(base, 6*time.Minute)) {
		t.Errorf("FirstSeen = %v, want the advance time — the stall window must reset", obs.FirstSeen)
	}
	if obs.TimestampAdvances != 0 {
		t.Errorf("TimestampAdvances = %d, want 0 after a cycle advance", obs.TimestampAdvances)
	}
}

// A frozen cycle whose heartbeat timestamp keeps moving is the exact signature
// reported in gt-bvo: the Deacon writes heartbeats but completes no wake cycles.
func TestNextObservation_CountsTimestampAdvancesUnderFrozenCycle(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	obs := NextObservation(nil, &Heartbeat{Timestamp: base, Cycle: 421}, base)

	for i := 1; i <= 3; i++ {
		tick := at(base, time.Duration(i)*time.Minute)
		obs = NextObservation(obs, &Heartbeat{Timestamp: tick, Cycle: 421}, tick)
	}

	if obs.TimestampAdvances != 3 {
		t.Errorf("TimestampAdvances = %d, want 3", obs.TimestampAdvances)
	}
	if !obs.FirstSeen.Equal(base) {
		t.Errorf("FirstSeen = %v, want %v — the stall window must not reset", obs.FirstSeen, base)
	}
}

// A Deacon parked in await-signal advances neither timestamp nor cycle. It must
// not accumulate wedge confirmations, or every legitimate sleep reads as a wedge.
func TestNextObservation_IdleSleepDoesNotCountAsAdvance(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	frozen := &Heartbeat{Timestamp: base, Cycle: 421}

	obs := NextObservation(nil, frozen, base)
	for i := 1; i <= 5; i++ {
		tick := at(base, time.Duration(i)*time.Minute)
		obs = NextObservation(obs, frozen, tick)
	}

	if obs.TimestampAdvances != 0 {
		t.Errorf("TimestampAdvances = %d, want 0 when the heartbeat itself is frozen", obs.TimestampAdvances)
	}
}

func TestWedgeConfirmed(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		obs  *CycleObservation
		want bool
	}{
		{
			name: "nil observation is not a wedge",
			obs:  nil,
			want: false,
		},
		{
			name: "no advances",
			obs:  &CycleObservation{Cycle: 421, FirstSeen: base},
			want: false,
		},
		{
			name: "one timestamp advance is not yet confirmation",
			obs:  &CycleObservation{Cycle: 421, FirstSeen: base, TimestampAdvances: 1},
			want: false,
		},
		{
			name: "two timestamp advances confirm a wedge",
			obs:  &CycleObservation{Cycle: 421, FirstSeen: base, TimestampAdvances: 2},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.obs.WedgeConfirmed(); got != tt.want {
				t.Errorf("WedgeConfirmed() = %v, want %v", got, tt.want)
			}
		})
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

// The regression that guards the Mayor's safety correction on gt-bvo: a Deacon
// parked in await-signal advances neither timestamp nor cycle. It must stay
// unwedged for an unbounded sleep — an earlier design used a 20m elapsed-stall
// backstop here, which condemned healthy sleeping Deacons.
func TestEvaluateHealth_LongIdleSleepIsNeverWedged(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	frozen := &Heartbeat{Timestamp: base, Cycle: 421}

	obs := NextObservation(nil, frozen, base)
	for i := 1; i <= 240; i++ { // four hours of polling a sleeping Deacon
		tick := at(base, time.Duration(i)*time.Minute)
		obs = NextObservation(obs, frozen, tick)

		if got := EvaluateHealth(frozen, obs, tick, DefaultHealthThresholds()); got == VerdictWedged {
			t.Fatalf("EvaluateHealth() = %q after %dm of legitimate sleep, want any non-wedged verdict", got, i)
		}
	}
}

// The core acceptance criterion from gt-bvo: a frozen cycle counter must be
// reported unhealthy even though the heartbeat is young enough to read "fresh".
func TestEvaluateHealth_FrozenCycleWithFreshHeartbeatIsWedged(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	now := at(base, 4*time.Minute)

	// Heartbeat written 45s ago — comfortably inside the 5m "fresh" window.
	hb := &Heartbeat{Timestamp: at(now, -45*time.Second), Cycle: 421}
	obs := &CycleObservation{
		Cycle:             421,
		FirstSeen:         base,
		TimestampAdvances: CycleWedgeAdvanceConfirmations,
	}

	// Precondition on the injected clock — hb.IsFresh() reads the wall clock,
	// which these synthetic timestamps are not anchored to.
	if age := now.Sub(hb.Timestamp); age >= HeartbeatStaleThreshold {
		t.Fatalf("precondition: heartbeat age %s must be inside the %s fresh window, or this test proves nothing",
			age, HeartbeatStaleThreshold)
	}

	got := EvaluateHealth(hb, obs, now, DefaultHealthThresholds())

	if got != VerdictWedged {
		t.Errorf("EvaluateHealth() = %q, want %q — a frozen cycle must outrank a fresh heartbeat age", got, VerdictWedged)
	}
	if got.Healthy() {
		t.Error("a wedged Deacon must not report Healthy()")
	}
}

func TestEvaluateHealth(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	// recentlyAdvanced models a healthy Deacon: the cycle changed at `seen`, so
	// the stall window restarted then.
	recentlyAdvanced := func(seen time.Time) *CycleObservation {
		return &CycleObservation{Cycle: 421, FirstSeen: seen, LastSeen: seen}
	}

	tests := []struct {
		name string
		hb   *Heartbeat
		obs  *CycleObservation
		now  time.Time
		want Verdict
	}{
		{
			name: "no heartbeat is unknown",
			hb:   nil,
			obs:  nil,
			now:  base,
			want: VerdictUnknown,
		},
		{
			name: "recent heartbeat, advancing cycle",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			obs:  recentlyAdvanced(base),
			now:  at(base, time.Minute),
			want: VerdictFresh,
		},
		{
			name: "aging heartbeat is stale",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			obs:  recentlyAdvanced(base),
			now:  at(base, 6*time.Minute),
			want: VerdictStale,
		},
		{
			name: "old heartbeat is very stale",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			obs:  recentlyAdvanced(base),
			now:  at(base, 25*time.Minute),
			want: VerdictVeryStale,
		},
		{
			// A Deacon that stopped writing entirely is not a wedge, even though
			// its cycle is equally frozen. It needs a poke/restart, not an
			// input-unsticking, so it must keep the age verdict.
			name: "fully stopped Deacon stays very stale, not wedged",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			obs:  &CycleObservation{Cycle: 421, FirstSeen: base, LastSeen: at(base, 25*time.Minute)},
			now:  at(base, 25*time.Minute),
			want: VerdictVeryStale,
		},
		{
			name: "nil observation falls back to age",
			hb:   &Heartbeat{Timestamp: base, Cycle: 421},
			obs:  nil,
			now:  at(base, time.Minute),
			want: VerdictFresh,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EvaluateHealth(tt.hb, tt.obs, tt.now, DefaultHealthThresholds()); got != tt.want {
				t.Errorf("EvaluateHealth() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestObserveCycle_RoundTripsThroughDisk(t *testing.T) {
	townRoot := t.TempDir()
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	first := ObserveCycle(townRoot, &Heartbeat{Timestamp: base, Cycle: 421}, base)
	if first == nil {
		t.Fatal("ObserveCycle returned nil on first observation")
	}

	// Second poll: cycle frozen, heartbeat refreshed.
	tick := at(base, 2*time.Minute)
	second := ObserveCycle(townRoot, &Heartbeat{Timestamp: tick, Cycle: 421}, tick)

	if second.TimestampAdvances != 1 {
		t.Errorf("TimestampAdvances = %d, want 1 — prior state must survive the disk round-trip", second.TimestampAdvances)
	}
	if !second.FirstSeen.Equal(base) {
		t.Errorf("FirstSeen = %v, want %v", second.FirstSeen, base)
	}

	if stored := ReadCycleObservation(townRoot); stored == nil || stored.TimestampAdvances != 1 {
		t.Errorf("ReadCycleObservation() = %+v, want the persisted second observation", stored)
	}
}

// Replays the field trace recorded in gt-bvo through the real persistence
// path: hq-deacon at a frozen cycle 421 whose heartbeat timestamp kept moving.
// The old age-only verdict called this "fresh" at every poll; it must now
// surface as wedged.
func TestEvaluateHealth_FieldTraceFromBead(t *testing.T) {
	townRoot := t.TempDir()
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	th := DefaultHealthThresholds()

	// Poll 1 — the Mayor's first reading: heartbeat 3m25s old, cycle 421.
	// Nothing to compare against yet, so this correctly reads fresh.
	hb1 := &Heartbeat{Timestamp: at(base, -3*time.Minute-25*time.Second), Cycle: 421}
	obs := ObserveCycle(townRoot, hb1, base)
	if got := EvaluateHealth(hb1, obs, base, th); got != VerdictFresh {
		t.Fatalf("poll 1 verdict = %q, want %q — one reading cannot show advancement", got, VerdictFresh)
	}

	// Poll 2 — "4 minutes later: still cycle 421", heartbeat only 4m10s old.
	// A frozen writer would read ~7m25s here; the timestamp moved.
	p2 := at(base, 4*time.Minute)
	hb2 := &Heartbeat{Timestamp: at(p2, -4*time.Minute-10*time.Second), Cycle: 421}
	obs = ObserveCycle(townRoot, hb2, p2)

	// Poll 3 — second confirmation of the same signature.
	p3 := at(base, 8*time.Minute)
	hb3 := &Heartbeat{Timestamp: at(p3, -30*time.Second), Cycle: 421}
	obs = ObserveCycle(townRoot, hb3, p3)

	if obs.TimestampAdvances != CycleWedgeAdvanceConfirmations {
		t.Fatalf("TimestampAdvances = %d, want %d", obs.TimestampAdvances, CycleWedgeAdvanceConfirmations)
	}

	got := EvaluateHealth(hb3, obs, p3, th)
	if got != VerdictWedged {
		t.Errorf("EvaluateHealth() = %q, want %q for the recorded field trace", got, VerdictWedged)
	}
	if got.Healthy() {
		t.Error("a wedged Deacon must not report Healthy()")
	}

	// The heartbeat is 30s old here — the age-only verdict this replaces would
	// still be calling it "fresh", which is the reporting bug gt-bvo filed.
	// Compare against the injected clock: hb.IsFresh() reads the wall clock,
	// which these synthetic timestamps are not anchored to.
	if age := p3.Sub(hb3.Timestamp); age >= th.Stale {
		t.Fatalf("precondition: trace must end on a heartbeat the old verdict called fresh, got age %s", age)
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
