package deacon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/awaitprobe"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/tmux"
)

// CycleStallThreshold is how long the wake-cycle counter may stay frozen before
// the stall is worth surfacing to an operator.
//
// This is a REPORTING threshold only. It deliberately does not feed the wedged
// verdict: a Deacon legitimately parked in await-signal advances no cycles
// either, and patrol backoff-max is 15m, so any purely time-based stall test
// condemns healthy sleeping Deacons. See CycleFrozen.
const CycleStallThreshold = 20 * time.Minute

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

	// VerdictWedged means the Deacon stopped mid-patrol: its heartbeat has
	// frozen, its turn has ended, and no await is pending to start another one.
	// Nothing in the system will move it without intervention.
	//
	// This is the state gt-bvo reported: unsubmitted text sitting in the
	// composer, with tmux has-session passing and no other probe complaining.
	// It is deliberately narrower than "unhealthy" — a Deacon that is merely
	// slow, asleep, or dead gets an age verdict instead, because those have
	// different remedies. See EvaluateHealth.
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
	next.HeartbeatTimestamp = hb.Timestamp

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
// Use EvaluateHealth for the verdict.
func (o *CycleObservation) CycleFrozen(now time.Time, stallThreshold time.Duration) bool {
	if o == nil {
		return false
	}
	return o.StallDuration(now) >= stallThreshold
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

// HealthThresholdsFrom returns the thresholds an operator has configured under
// operational.deacon, falling back to the compiled-in defaults for unset keys.
// A nil config yields DefaultHealthThresholds.
//
// Every consumer that judges a Deacon's heartbeat should route through this
// rather than reading the constants directly. The two config keys existed and
// were documented as the escape hatch for a badly calibrated threshold, but
// nothing read them: the daemon and `gt deacon status` both compared against
// the compiled-in constants, so setting them changed nothing and failed
// silently — the shape that left gt-cbd looking like a local misconfiguration
// for nine days.
func HealthThresholdsFrom(cfg *config.DeaconThresholds) HealthThresholds {
	return HealthThresholds{
		Stale:      cfg.HeartbeatStaleThresholdD(),
		VeryStale:  cfg.HeartbeatVeryStaleThresholdD(),
		CycleStall: CycleStallThreshold,
	}
}

// LivenessSignals are the two out-of-band observations that separate a stopped
// Deacon from a sleeping or a working one. Neither is derivable from the
// heartbeat file, and neither is sufficient alone:
//
//   - Await says whether a `gt mol step await-signal` process is waiting on the
//     Deacon's behalf. A pending await means it is parked and will wake itself.
//     Absence does NOT mean stopped — a Deacon computing mid-patrol has no
//     await process either (see [awaitprobe]).
//   - Turn says whether a turn is in flight in the Deacon's pane. An ended turn
//     does NOT mean stopped either: an await backgrounded by an earlier turn
//     outlives it, which is why waking on the pane alone interrupts healthy
//     waits (gt-ghw7, see [tmux.TurnState]).
//
// Each one covers exactly what the other cannot see, so the pair is read
// together and only together.
//
// The caller gathers them — one ps and one capture-pane, both local — so the
// verdict below stays a pure function of what was observed.
type LivenessSignals struct {
	Await awaitprobe.State
	Turn  tmux.TurnState
}

// Stopped reports whether both signals agree that nothing will move this Deacon
// on its own: no turn running, and no await waiting to start one.
//
// Either signal being unreadable (no ps, no pane, no session) yields false.
// That is the safe direction: an unobserved Deacon is not condemned, and the
// verdict falls back to heartbeat age, which is what the town had before.
func (s LivenessSignals) Stopped() bool {
	if s.Await != awaitprobe.StateAbsent {
		return false
	}
	return s.Turn == tmux.TurnEnded || s.Turn == tmux.TurnStranded
}

// EvaluateHealth returns the health verdict for a Deacon: heartbeat age, plus
// one narrower verdict for the Deacon that has stopped mid-patrol.
//
// # What the wedge test is, and why it is not the cycle counter
//
// The state gt-bvo reported is a Deacon sitting at its prompt with unsubmitted
// text in the composer: tmux has-session passes, the heartbeat still reads
// fresh, and no patrol work is happening. The first detector built for it keyed
// on the heartbeat timestamp advancing while the wake-cycle counter stayed
// frozen. That signature does not exist — every production writer sets both
// fields in the same write, and a sweep of 1182 recorded polls across 161
// sessions found zero same-cycle timestamp advances against a control that
// fires on 100% of cycle-advancing pairs (gt-s6r). The verdict was unreachable.
//
// The follow-up (gt-dndw) proposed reading heartbeat.LastAction instead: the
// patrol formula writes "pre-await checkpoint" immediately before parking, so a
// frozen heartbeat resting on any other action would mean stopped mid-patrol.
// Measured against the same corpus, that rule is unsafe. LastAction is typed by
// the Deacon, not written by the code that parks: of 323 recorded readings only
// 9 carry the exact string, while 157 are free-form paraphrases of the same
// intent ("await-signal blocking, patrol 2761 closed", "patrol 3070 closed,
// awaiting signal"). Six of the seven readings at 20m+ rest on such a
// paraphrase, and all seven were legitimate sleep. An exact-match rule would
// have condemned them; a substring rule would let a wedge that stopped just
// after writing one hide behind it.
//
// So the discriminator is not in the heartbeat file at all. It is the pair in
// [LivenessSignals]: no turn in flight AND no await pending. Those are written
// by the operating system, not by an agent describing itself, and each covers
// the other's blind spot. Together with a heartbeat that has gone stale, they
// say the Deacon stopped mid-patrol — which is what a wedge is.
//
// # Why the age gate stays
//
// The stale threshold is not part of the definition; it buys out the races. A
// turn that ended a second ago, a session mid-respawn, and a Deacon between two
// steps all read momentarily like a stopped one, and all of them refresh the
// heartbeat well inside the window (the live town averages a heartbeat write
// every ~3 minutes). Requiring the heartbeat to have gone stale first means the
// verdict describes a Deacon that has already failed to make progress, not one
// caught mid-stride.
//
// # This function reports. It never acts.
//
// Recovery for a wedge is destructive — bare Enter does not clear a stranded
// composer and C-u destroys whatever is queued — so it stays a human decision
// (Mayor's safety correction on gt-bvo). Note that the daemon's patrol-wake
// deliberately refuses to type into a stranded composer for the same reason,
// which is exactly why that case needs reporting: nothing else will pick it up.
func EvaluateHealth(hb *Heartbeat, now time.Time, th HealthThresholds, sig LivenessSignals) Verdict {
	if hb == nil {
		return VerdictUnknown
	}

	age := now.Sub(hb.Timestamp)

	if age >= th.Stale && sig.Stopped() {
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
