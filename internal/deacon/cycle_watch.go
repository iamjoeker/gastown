package deacon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Cycle-advancement thresholds — compiled-in defaults. Callers that have
// configured values pass them through HealthThresholds instead.
const (
	// CycleStallThreshold is how long the wake-cycle counter may stay frozen
	// before the stall is worth surfacing to an operator.
	//
	// This is a REPORTING threshold only. It deliberately does not feed the
	// wedged verdict: a Deacon legitimately parked in await-signal advances no
	// cycles either, and patrol backoff-max is 15m, so any purely time-based
	// stall test condemns healthy sleeping Deacons. See CycleFrozen.
	CycleStallThreshold = 20 * time.Minute

	// CycleWedgeAdvanceConfirmations is how many times the heartbeat timestamp
	// must move forward while the cycle counter stays frozen before the Deacon
	// is called wedged.
	//
	// WARNING — this signal does not occur in production. It was derived from a
	// field report that has since been REFUTED (gt-s6r), and the verdict built
	// on it cannot fire. Do not extend this detector without reading that bead.
	//
	// The premise was that a wedge shows a signature nothing else shows:
	//
	//	healthy patrolling  timestamp advances, cycle advances
	//	await-signal sleep  neither advances
	//	dead session        neither advances
	//	WEDGED              timestamp advances, cycle does NOT   <- WRONG
	//
	// The last row came from two Mayor polls of a wedged hq-deacon that read
	// 3m25s and then 4m10s old at a frozen cycle 421, believed to be "4 minutes
	// apart". The Mayor's own transcript puts them 45.06s apart, and the ages
	// differ by exactly 45s: the heartbeat was frozen too. Twelve polls of that
	// cycle by four sessions all reconstruct the same single write. A sweep of
	// 1182 polls across 161 sessions found no same-cycle timestamp advance,
	// against a control that fires on 100% of cycle-advancing pairs.
	//
	// A real wedge is "neither advances" — indistinguishable, by this signal,
	// from sleep and from death. Every production writer (Touch,
	// TouchWithAction) sets Timestamp and Cycle in the same write, so
	// NextObservation resets its window on each one and TimestampAdvances can
	// never leave 0. The one path to a nonzero count is a lost update between
	// concurrent writers, which would be a false positive on a healthy Deacon.
	//
	// heartbeat.LastAction is the field that does separate the two: the Deacon
	// writes "pre-await checkpoint" immediately before parking in await-signal,
	// so a frozen heartbeat resting on any other action stopped mid-patrol.
	// EvaluateHealth does not use it yet.
	//
	// Two confirmations rather than one, so a single interleaved read between
	// a heartbeat write and its cycle increment cannot trip it.
	CycleWedgeAdvanceConfirmations = 2
)

// Verdict is the health conclusion for a Deacon.
type Verdict string

const (
	// VerdictUnknown means there is no heartbeat to judge.
	VerdictUnknown Verdict = "unknown"

	// VerdictFresh means the Deacon is advancing normally.
	VerdictFresh Verdict = "fresh"

	// VerdictStale means the heartbeat is aging but not yet actionable.
	VerdictStale Verdict = "stale"

	// VerdictVeryStale means the heartbeat is old enough to poke or restart.
	VerdictVeryStale Verdict = "very stale"

	// VerdictWedged means the session is alive and writing heartbeats but the
	// wake-cycle counter is not advancing — the Deacon is frozen mid-patrol.
	//
	// Unreachable in production as written: it requires WedgeConfirmed, whose
	// signal the coupled heartbeat writer cannot produce. See
	// CycleWedgeAdvanceConfirmations for why, gt-s6r for the measurement, and
	// gt-dndw for the rebuild.
	VerdictWedged Verdict = "wedged"
)

// Healthy reports whether the verdict means the Deacon needs no intervention.
func (v Verdict) Healthy() bool {
	return v == VerdictFresh
}

// CycleObservation is the observer-side record of wake-cycle advancement.
//
// The Deacon cannot record its own stall: a wedged Deacon runs no code. So
// whoever polls (gt deacon status, the daemon) persists what it saw, and the
// stall is derived by comparing successive readings.
type CycleObservation struct {
	// Cycle is the wake-cycle counter value this observation tracks.
	Cycle int64 `json:"cycle"`

	// FirstSeen is when this Cycle value was first observed.
	FirstSeen time.Time `json:"first_seen"`

	// LastSeen is when this Cycle value was most recently observed.
	LastSeen time.Time `json:"last_seen"`

	// HeartbeatTimestamp is the heartbeat's own timestamp at LastSeen.
	HeartbeatTimestamp time.Time `json:"heartbeat_timestamp"`

	// TimestampAdvances counts how many times the heartbeat timestamp moved
	// forward while Cycle stayed frozen. Intended to mean the Deacon is writing
	// heartbeats without completing wake cycles — but the writer couples the two
	// fields, so in practice this stays 0 forever. See
	// CycleWedgeAdvanceConfirmations.
	TimestampAdvances int `json:"timestamp_advances"`
}

// CycleObservationFile returns the path to the cycle observation file.
func CycleObservationFile(townRoot string) string {
	return filepath.Join(townRoot, "deacon", "cycle-observation.json")
}

// ReadCycleObservation reads the stored observation.
// Returns nil if the file doesn't exist or can't be parsed.
func ReadCycleObservation(townRoot string) *CycleObservation {
	data, err := os.ReadFile(CycleObservationFile(townRoot)) //nolint:gosec // G304: path is constructed from trusted townRoot
	if err != nil {
		return nil
	}

	var obs CycleObservation
	if err := json.Unmarshal(data, &obs); err != nil {
		return nil
	}

	return &obs
}

// WriteCycleObservation persists an observation.
func WriteCycleObservation(townRoot string, obs *CycleObservation) error {
	obsFile := CycleObservationFile(townRoot)

	if err := os.MkdirAll(filepath.Dir(obsFile), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(obs, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(obsFile, data, 0600)
}

// NextObservation folds a heartbeat reading into the previous observation and
// returns the updated one. Pure — no IO, no clock — so the state machine is
// directly testable.
//
// Returns nil when there is no heartbeat to observe.
func NextObservation(prev *CycleObservation, hb *Heartbeat, now time.Time) *CycleObservation {
	if hb == nil {
		return nil
	}

	// No prior reading, or the cycle advanced: start a fresh stall window.
	if prev == nil || prev.Cycle != hb.Cycle {
		return &CycleObservation{
			Cycle:              hb.Cycle,
			FirstSeen:          now,
			LastSeen:           now,
			HeartbeatTimestamp: hb.Timestamp,
		}
	}

	// Same cycle as last time: extend the stall window.
	next := *prev
	next.LastSeen = now

	// The heartbeat timestamp moving forward under a frozen cycle was believed
	// to be the signature of a wedge — count it. No production writer can
	// produce it, so this branch is dead in the town; see
	// CycleWedgeAdvanceConfirmations.
	if hb.Timestamp.After(prev.HeartbeatTimestamp) {
		next.TimestampAdvances++
		next.HeartbeatTimestamp = hb.Timestamp
	}

	return &next
}

// ObserveCycle folds the current heartbeat into the persisted observation and
// returns the updated record. Best-effort: a write failure still yields the
// correct in-memory observation, since the caller's verdict matters more than
// the bookkeeping.
func ObserveCycle(townRoot string, hb *Heartbeat, now time.Time) *CycleObservation {
	obs := NextObservation(ReadCycleObservation(townRoot), hb, now)
	if obs == nil {
		return nil
	}

	_ = WriteCycleObservation(townRoot, obs)

	return obs
}

// StallDuration returns how long the cycle counter has been frozen.
func (o *CycleObservation) StallDuration(now time.Time) time.Duration {
	if o == nil {
		return 0
	}
	return now.Sub(o.FirstSeen)
}

// CycleFrozen reports whether the cycle counter has sat unchanged for longer
// than stallThreshold.
//
// This is a FACTUAL observation for display, not a health verdict. A Deacon
// sleeping in await-signal is equally "frozen" by this test and is perfectly
// healthy, so callers must not derive unhealthiness from it alone — that is
// the false positive that made an earlier version of this detector unsafe.
// Use WedgeConfirmed for the verdict.
func (o *CycleObservation) CycleFrozen(now time.Time, stallThreshold time.Duration) bool {
	if o == nil {
		return false
	}
	return o.StallDuration(now) >= stallThreshold
}

// WedgeConfirmed reports whether the heartbeat timestamp has advanced under a
// frozen cycle often enough to conclude the Deacon is wedged.
//
// This is the verdict signal, and it always returns false against the current
// writer — the advance it counts does not happen, so this detects nothing. The
// live town has recorded 0 advances in the ~7650 cycles since it shipped. See
// CycleWedgeAdvanceConfirmations for the refuted premise and gt-s6r for the
// measurement.
func (o *CycleObservation) WedgeConfirmed() bool {
	return o != nil && o.TimestampAdvances >= CycleWedgeAdvanceConfirmations
}

// HealthThresholds bundles the durations a health verdict depends on, so
// callers can supply configured values instead of the compiled-in defaults.
type HealthThresholds struct {
	Stale      time.Duration
	VeryStale  time.Duration
	CycleStall time.Duration
}

// DefaultHealthThresholds returns the compiled-in thresholds.
func DefaultHealthThresholds() HealthThresholds {
	return HealthThresholds{
		Stale:      HeartbeatStaleThreshold,
		VeryStale:  HeartbeatVeryStaleThreshold,
		CycleStall: CycleStallThreshold,
	}
}

// EvaluateHealth returns the health verdict for a Deacon. It consults wake-cycle
// advancement first and heartbeat age second — but only the second one decides
// anything today.
//
// The wedge branch does not fire. It was built on a report that a Deacon frozen
// mid-patrol keeps writing heartbeats and so reads "fresh" while completing no
// cycles; the measurement in gt-s6r refutes that — the wedged Deacon's
// heartbeat was frozen as well, and aged past the stale threshold like any
// other stopped writer. What made gt-bvo's wedge look "fresh" was that the
// Mayor happened to poll 3m25s and 4m10s in, both inside the 5m window.
//
// So in practice this function is the age verdict. The wedge branch is retained
// because the state it names is real and still undetected, not because this
// test detects it. Rebuilding it on heartbeat.LastAction is gt-dndw.
//
// The wedge test requires a young heartbeat as well as a confirmed wedge
// signature. Those two conditions together mean the Deacon is demonstrably
// still running code but making no progress — recoverable by unsticking its
// input. The freshness requirement also bounds the verdict in time: a Deacon
// that wedged and then died stops refreshing its heartbeat and decays to
// stale/very stale rather than being reported wedged forever off a stale
// confirmation count. That is a different failure with a different remedy
// (poke, then restart).
//
// This function reports. It never acts: recovery for a wedge is destructive
// (clear + retype) and must stay a human decision until the detector has been
// validated in the field.
func EvaluateHealth(hb *Heartbeat, obs *CycleObservation, now time.Time, th HealthThresholds) Verdict {
	if hb == nil {
		return VerdictUnknown
	}

	age := now.Sub(hb.Timestamp)

	if age < th.Stale && obs.WedgeConfirmed() {
		return VerdictWedged
	}

	switch {
	case age >= th.VeryStale:
		return VerdictVeryStale
	case age >= th.Stale:
		return VerdictStale
	default:
		return VerdictFresh
	}
}
