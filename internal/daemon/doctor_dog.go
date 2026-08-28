package daemon

import (
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
)

// Operational constants — timeouts needed to perform checks.
const (
	defaultDoctorDogInterval = 5 * time.Minute
)

// Default advisory thresholds — used for recommendations in the report.
// These are defaults; override via DoctorDogConfig fields.
const (
	defaultDoctorDogLatencyAlertMs     = 5000.0
	defaultDoctorDogOrphanAlertCount   = 20
	defaultDoctorDogBackupStaleSeconds = 3600.0
)

// DoctorDogConfig holds configuration for the doctor_dog patrol.
type DoctorDogConfig struct {
	// Enabled controls whether the doctor dog runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "5m").
	IntervalStr string `json:"interval,omitempty"`

	// Databases lists the expected production databases.
	// If empty, uses the default set.
	Databases []string `json:"databases,omitempty"`

	// Advisory thresholds — when exceeded, recommendations are added to the report.
	// Agents (Mayor/Deacon) read the report and decide what actions to take.
	// Zero values mean "use default".

	// LatencyAlertMs: latency threshold in ms. Default: 5000 (5s).
	LatencyAlertMs float64 `json:"latency_alert_ms,omitempty"`

	// OrphanAlertCount: database count threshold. Default: 20.
	OrphanAlertCount int `json:"orphan_alert_count,omitempty"`

	// BackupStaleSeconds: backup age threshold in seconds. Default: 3600 (1hr).
	BackupStaleSeconds float64 `json:"backup_stale_seconds,omitempty"`
}

// doctorDogThresholds returns the effective thresholds, using config overrides or defaults.
func doctorDogThresholds(config *DaemonPatrolConfig) (latencyMs float64, orphanCount int, backupStaleSec float64) {
	latencyMs = defaultDoctorDogLatencyAlertMs
	orphanCount = defaultDoctorDogOrphanAlertCount
	backupStaleSec = defaultDoctorDogBackupStaleSeconds

	if config != nil && config.Patrols != nil && config.Patrols.DoctorDog != nil {
		cfg := config.Patrols.DoctorDog
		if cfg.LatencyAlertMs > 0 {
			latencyMs = cfg.LatencyAlertMs
		}
		if cfg.OrphanAlertCount > 0 {
			orphanCount = cfg.OrphanAlertCount
		}
		if cfg.BackupStaleSeconds > 0 {
			backupStaleSec = cfg.BackupStaleSeconds
		}
	}
	return
}

// doctorDogInterval returns the configured interval, or the default (5m).
func doctorDogInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.DoctorDog != nil {
		if config.Patrols.DoctorDog.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.DoctorDog.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultDoctorDogInterval
}

// doctorDogDatabases returns the list of production databases for health checks.
func doctorDogDatabases(config *DaemonPatrolConfig) []string {
	if config != nil && config.Patrols != nil && config.Patrols.DoctorDog != nil {
		if len(config.Patrols.DoctorDog.Databases) > 0 {
			return config.Patrols.DoctorDog.Databases
		}
	}
	return []string{"hq", "gt", "mo"}
}

// lastDoltWarnings returns the warnings from the most recent Dolt health check,
// or nil when there is no managed server to have checked.
func (d *Daemon) lastDoltWarnings() []string {
	if d.doltServer == nil {
		return nil
	}
	return d.doltServer.LastWarnings()
}

// runDoctorDog reports the Dolt health check, pouring a mol-dog-doctor molecule
// only when that check found something wrong.
//
// It used to pour one unconditionally, every cycle, "for agent execution" — and
// no agent ever executed it. Nothing in the tree slings mol-dog-doctor (only
// mol-dog-reaper has a dispatcher), and the pour was followed immediately by
// `defer mol.close()`, so the molecule was closed a second later by the same
// call that created it. No executor could have reached it even if one existed.
//
// The cost was the town's largest single wisp emitter. Measured on hq
// 2026-08-24: 49 mol-dog-doctor roots and 147 step children across the daemon's
// ~4h of uptime in that window — four wisps every five minutes, each one a
// permanent Dolt commit in the data plane CLAUDE.md flags as fragile. At full
// uptime that is ~1150 wisps/day, which is the whole of the ~1000 beads/day
// growth gt-bnpw was filed for.
//
// It bought nothing, because the five checks the mol-dog-doctor formula
// describes — connectivity/latency, connection count, disk usage, orphan
// database count, backup freshness — are exactly the five the daemon already
// runs itself on every heartbeat, in checkHealthLocked, whose findings land in
// LastWarnings. The work was never missing; only its report was, and the
// molecule was standing in for a report nobody wrote.
//
// So this now defers to the policy ensureDoltServerRunning already applies to
// the same molecule: pour ONLY on anomaly, and share lastDoctorMolTime so the
// two emitters cannot double-pour inside one cooldown. A clean cycle logs the
// result instead of minting four beads to say nothing is wrong — which also
// answers gt-bnpw's sharpest complaint, that the town believed it had a
// periodic health check and had only the beads representing one.
func (d *Daemon) runDoctorDog() {
	if !d.isPatrolActive("doctor_dog") {
		return
	}

	warnings := d.lastDoltWarnings()
	if len(warnings) == 0 {
		d.logger.Printf("doctor_dog: last Dolt health check found no warnings — no molecule poured")
		return
	}

	summary := strings.Join(warnings, "; ")
	if time.Since(d.lastDoctorMolTime) < doctorMolCooldown {
		d.logger.Printf("doctor_dog: %d warning(s), molecule suppressed by cooldown: %s", len(warnings), summary)
		return
	}
	d.lastDoctorMolTime = time.Now()

	port := d.doltServerPort()
	latencyThreshold, orphanCount, backupStaleSec := doctorDogThresholds(d.patrolConfig)

	mol := d.pourDogMolecule(constants.MolDogDoctor, map[string]string{
		"port":              strconv.Itoa(port),
		"latency_threshold": strconv.FormatFloat(latencyThreshold, 'f', 0, 64) + "ms",
		"orphan_threshold":  strconv.Itoa(orphanCount),
		"backup_threshold":  strconv.FormatFloat(backupStaleSec, 'f', 0, 64) + "s",
	})
	defer mol.close()

	if mol.rootID == "" {
		d.logger.Printf("doctor_dog: molecule pour failed (non-fatal); %d warning(s): %s", len(warnings), summary)
		return
	}

	// The probe and the inspection already happened — checkHealthLocked ran all
	// five checks and these warnings are its output — so close those steps with
	// the result rather than leaving them open for an executor that does not
	// exist. That is the same shape every other dog patrol in this daemon uses
	// (checkpoint, jsonl, backup, compactor): the daemon does the work and the
	// molecule records it.
	mol.closeStep("probe")
	mol.closeStep("inspect")
	d.logger.Printf("doctor_dog: poured %s → %s for %d warning(s): %s",
		constants.MolDogDoctor, mol.rootID, len(warnings), summary)
	mol.closeStep("report")
}
