// Package deacon provides the Deacon agent infrastructure.
// The Deacon is a Claude agent that monitors Mayor and Witnesses,
// handles lifecycle requests, and keeps Gas Town running.
package deacon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

// Heartbeat age thresholds — these are the compiled-in defaults. Both are
// overridable via operational.deacon.heartbeat_stale_threshold and
// operational.deacon.heartbeat_very_stale_threshold in settings/config.json;
// see HealthThresholdsFrom, which is what the daemon and `gt deacon status`
// actually judge against.
//
// # Why the stale threshold is not five minutes
//
// The heartbeat stamps at fixed points in the patrol cycle — cycle start,
// mid-cycle, and immediately before parking in await-signal — so its age
// measures POSITION IN THE LOOP, not liveness: it ramps from zero to the cycle
// duration and resets. Any threshold below the cycle length therefore fires on
// a Deacon that is merely working or legitimately asleep.
//
// Measured over the last 30 closed mol-deacon-patrol wisps (gt-cbd):
//
//	samples 30 | min 224s | max 957s | mean 604s
//	cycles crossing a 300s threshold : 29 of 30
//	cycles reaching a 1200s threshold:  0 of 30
//	wall-clock spent labelled stale  : 9183s of 18107s = 50.7%
//
// A five-minute threshold labelled a healthy Deacon stale for more than half of
// every cycle, on every surface an operator consults, and drove the daemon to
// nudge a working agent. Three independent agents raised a wedge alarm on the
// same healthy Deacon in one day off that signal.
const (
	// HeartbeatStaleThreshold is the age at which a heartbeat is considered
	// stale. Set above the observed cycle distribution and equal to patrol
	// backoff-max, so the stale band means "slept past the longest designed
	// park" rather than "is mid-cycle". A cycle at the observed maximum (957s)
	// still crosses it briefly; that residue is the point of the band, and the
	// wedged verdict needs the out-of-band [LivenessSignals] on top of it.
	HeartbeatStaleThreshold = config.DefaultDeaconHeartbeatStaleThreshold

	// HeartbeatVeryStaleThreshold is the age at which a heartbeat is considered
	// very stale, meaning the Deacon should be poked or restarted.
	// Must be greater than patrol backoff-max (15m) to avoid false positives
	// during legitimate await-signal sleep. Measured false-positive rate on the
	// sample above: 0 of 30.
	HeartbeatVeryStaleThreshold = config.DefaultDeaconHeartbeatVeryStale
)

// Heartbeat represents the Deacon's heartbeat file contents.
// Written by the Deacon on each wake cycle.
// Read by the Go daemon to decide whether to poke.
type Heartbeat struct {
	// Timestamp is when the heartbeat was written.
	Timestamp time.Time `json:"timestamp"`

	// Cycle is the current wake cycle number.
	Cycle int64 `json:"cycle"`

	// LastAction describes what the Deacon did in this cycle.
	LastAction string `json:"last_action,omitempty"`

	// HealthyAgents is the count of healthy agents observed.
	HealthyAgents int `json:"healthy_agents"`

	// UnhealthyAgents is the count of unhealthy agents observed.
	UnhealthyAgents int `json:"unhealthy_agents"`
}

// HeartbeatFile returns the path to the Deacon heartbeat file.
func HeartbeatFile(townRoot string) string {
	return filepath.Join(townRoot, "deacon", "heartbeat.json")
}

// WriteHeartbeat writes a new heartbeat to disk.
// Called by the Deacon at the start of each wake cycle.
func WriteHeartbeat(townRoot string, hb *Heartbeat) error {
	hbFile := HeartbeatFile(townRoot)

	// Ensure deacon directory exists
	if err := os.MkdirAll(filepath.Dir(hbFile), 0755); err != nil {
		return err
	}

	// Set timestamp if not already set
	if hb.Timestamp.IsZero() {
		hb.Timestamp = time.Now().UTC()
	}

	data, err := json.MarshalIndent(hb, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(hbFile, data, 0600); err != nil {
		return err
	}

	// Also touch .deacon-heartbeat for backward compatibility with shell scripts
	// that check this file's mtime for liveness detection (stuck-agent-dog).
	// These scripts predate heartbeat.json and check mtime, not file contents.
	legacyFile := filepath.Join(filepath.Dir(hbFile), ".deacon-heartbeat")
	_ = os.WriteFile(legacyFile, []byte(""), 0644) //nolint:gosec // G306: world-readable liveness file is intentional

	return nil
}

// ReadHeartbeat reads the Deacon heartbeat from disk.
// Returns nil if the file doesn't exist or can't be read.
func ReadHeartbeat(townRoot string) *Heartbeat {
	hbFile := HeartbeatFile(townRoot)

	data, err := os.ReadFile(hbFile) //nolint:gosec // G304: path is constructed from trusted townRoot
	if err != nil {
		return nil
	}

	var hb Heartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		return nil
	}

	return &hb
}

// Age returns how old the heartbeat is.
// Returns a very large duration if the heartbeat is nil.
func (hb *Heartbeat) Age() time.Duration {
	if hb == nil {
		return 24 * time.Hour * 365 // Very stale
	}
	return time.Since(hb.Timestamp)
}

// IsFresh returns true if the heartbeat is younger than the compiled-in stale
// threshold. A fresh heartbeat means the Deacon is actively working or recently
// finished. Callers that have operational config in hand should prefer
// IsFreshFor with a threshold from HealthThresholdsFrom, so an operator's
// override is not silently ignored.
func (hb *Heartbeat) IsFresh() bool {
	return hb.IsFreshFor(HeartbeatStaleThreshold)
}

// IsFreshFor reports whether the heartbeat is younger than the given stale
// threshold.
func (hb *Heartbeat) IsFreshFor(stale time.Duration) bool {
	return hb != nil && hb.Age() < stale
}

// IsStale returns true if the heartbeat age is in the stale band — old enough
// to be worth reporting, not old enough to restart on. See IsStaleFor.
func (hb *Heartbeat) IsStale() bool {
	return hb.IsStaleFor(HeartbeatStaleThreshold, HeartbeatVeryStaleThreshold)
}

// IsStaleFor reports whether the heartbeat age falls in [stale, veryStale).
func (hb *Heartbeat) IsStaleFor(stale, veryStale time.Duration) bool {
	if hb == nil {
		return false
	}
	age := hb.Age()
	return age >= stale && age < veryStale
}

// IsVeryStale returns true if the heartbeat has passed the compiled-in
// very-stale threshold. A very stale heartbeat means the Deacon should be poked.
func (hb *Heartbeat) IsVeryStale() bool {
	return hb.IsVeryStaleFor(HeartbeatVeryStaleThreshold)
}

// IsVeryStaleFor reports whether the heartbeat has passed the given very-stale
// threshold. A missing heartbeat counts as very stale.
func (hb *Heartbeat) IsVeryStaleFor(veryStale time.Duration) bool {
	return hb == nil || hb.Age() >= veryStale
}

// Touch writes a minimal heartbeat with just the timestamp.
// This is a convenience function for simple heartbeat updates.
func Touch(townRoot string) error {
	// Read existing heartbeat to increment cycle
	existing := ReadHeartbeat(townRoot)
	cycle := int64(1)
	if existing != nil {
		cycle = existing.Cycle + 1
	}

	return WriteHeartbeat(townRoot, &Heartbeat{
		Timestamp: time.Now().UTC(),
		Cycle:     cycle,
	})
}

// TouchWithAction writes a heartbeat with an action description.
func TouchWithAction(townRoot, action string, healthy, unhealthy int) error {
	existing := ReadHeartbeat(townRoot)
	cycle := int64(1)
	if existing != nil {
		cycle = existing.Cycle + 1
	}

	return WriteHeartbeat(townRoot, &Heartbeat{
		Timestamp:       time.Now().UTC(),
		Cycle:           cycle,
		LastAction:      action,
		HealthyAgents:   healthy,
		UnhealthyAgents: unhealthy,
	})
}
