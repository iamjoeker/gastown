package daemon

import (
	"strings"
	"time"
)

// DefaultLifecycleConfig returns a DaemonPatrolConfig with sensible defaults
// for the six-stage Dolt lifecycle (CREATE → LIVE → CLOSE → DECAY → COMPACT → FLATTEN).
//
// All patrols are enabled with conservative intervals:
//   - Wisp Reaper (DECAY): every 1h, delete closed wisps after 7d,
//     auto-close stale issues after 30d
//   - Compactor Dog (COMPACT): every 24h, threshold 2000 commits
//   - Checkpoint Dog: every 10m, auto-commit dirty polecat worktrees
//   - Doctor Dog (health): every 5m
//   - JSONL Git Backup: every 15m
//   - Dolt Filesystem Backup: every 15m
//   - Scheduled Maintenance (FLATTEN): daily at 03:00, threshold 1000
//   - Main Branch Test: every 30m, 10m timeout per rig
//
// EVERY VALUE HERE IS READ FROM THE PATROL'S OWN COMPILED-IN FALLBACK — the
// constant its accessor returns when the key is absent. Never hand-write a
// literal into this tree. This file provisions mayor/daemon.json, so a literal
// that disagrees with the constant makes a town created by `gt init` behave
// differently from a town with no daemon.json, and nothing reports which one you
// have: wisp_reaper reaped every 30m provisioned against a compiled 1h for the
// whole life of gt-r4lv, because the two numbers lived in different files and
// only the file you weren't reading was authoritative. Same class as gt-il30.
//
// TestDefaultLifecycleConfigMatchesCompiledFallbacks enforces it for every knob
// with an accessor. Enabled flags and the maintenance window are deliberately
// exempt — see that test.
func DefaultLifecycleConfig() *DaemonPatrolConfig {
	threshold := defaultMaintenanceThreshold
	scrub := true
	return &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:          true,
				IntervalStr:      durationDefault(defaultWispReaperInterval),
				MaxAgeStr:        durationDefault(defaultWispMaxAge),
				DeleteAgeStr:     durationDefault(defaultWispDeleteAge),
				StaleIssueAgeStr: durationDefault(defaultStaleIssueAge),
				MailDeleteAgeStr: durationDefault(defaultMailDeleteAge),
			},
			CompactorDog: &CompactorDogConfig{
				Enabled:     true,
				IntervalStr: durationDefault(defaultCompactorDogInterval),
				Threshold:   defaultCompactorCommitThreshold,
			},
			CheckpointDog: &CheckpointDogConfig{
				Enabled:     true,
				IntervalStr: durationDefault(defaultCheckpointDogInterval),
			},
			DoctorDog: &DoctorDogConfig{
				Enabled:     true,
				IntervalStr: durationDefault(defaultDoctorDogInterval),
			},
			JsonlGitBackup: &JsonlGitBackupConfig{
				Enabled:     true,
				IntervalStr: durationDefault(defaultJsonlGitBackupInterval),
				Scrub:       &scrub,
			},
			DoltBackup: &DoltBackupConfig{
				Enabled:     true,
				IntervalStr: durationDefault(defaultDoltBackupInterval),
			},
			ScheduledMaintenance: &ScheduledMaintenanceConfig{
				Enabled: true,
				// The window has no compiled fallback: an absent window means
				// "no scheduled maintenance", so provisioning one is an opt-in,
				// not a restatement of a default.
				Window:    defaultMaintenanceWindow,
				Interval:  defaultMaintenanceInterval,
				Threshold: &threshold,
			},
			MainBranchTest: &MainBranchTestConfig{
				Enabled:     true,
				IntervalStr: durationDefault(defaultMainBranchTestInterval),
				TimeoutStr:  durationDefault(defaultMainBranchTestTimeout),
			},
			Handler: &PatrolConfig{
				Enabled: true,
			},
		},
	}
}

// durationDefault renders a compiled-in default duration for the provisioned
// config file. Duration.String() spells an hour "1h0m0s"; the zero components
// are noise in a file humans hand-edit, so trim them where they carry nothing.
// The result must always parse back to the same duration — the round trip is
// what keeps the provisioned string and the constant the same value, and
// TestDurationDefaultRoundTrips holds it.
func durationDefault(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// EnsureLifecycleDefaults populates missing patrol configuration with sensible
// defaults. It never overwrites existing user configuration — only fills in
// patrols that are nil (not yet configured).
//
// Returns true if any defaults were applied (caller should persist the config).
func EnsureLifecycleDefaults(config *DaemonPatrolConfig) bool {
	if config == nil {
		return false
	}

	defaults := DefaultLifecycleConfig()
	changed := false

	if config.Patrols == nil {
		config.Patrols = defaults.Patrols
		return true
	}

	p := config.Patrols
	d := defaults.Patrols

	if p.WispReaper == nil {
		p.WispReaper = d.WispReaper
		changed = true
	}
	if p.CompactorDog == nil {
		p.CompactorDog = d.CompactorDog
		changed = true
	}
	if p.CheckpointDog == nil {
		p.CheckpointDog = d.CheckpointDog
		changed = true
	}
	if p.DoctorDog == nil {
		p.DoctorDog = d.DoctorDog
		changed = true
	}
	if p.JsonlGitBackup == nil {
		p.JsonlGitBackup = d.JsonlGitBackup
		changed = true
	}
	if p.DoltBackup == nil {
		p.DoltBackup = d.DoltBackup
		changed = true
	}
	if p.ScheduledMaintenance == nil {
		p.ScheduledMaintenance = d.ScheduledMaintenance
		changed = true
	}
	if p.MainBranchTest == nil {
		p.MainBranchTest = d.MainBranchTest
		changed = true
	}
	if p.Handler == nil {
		p.Handler = d.Handler
		changed = true
	}

	return changed
}

// EnsureLifecycleConfigFile loads the patrol config from disk (or creates a new
// one if it doesn't exist), applies lifecycle defaults for any unconfigured
// patrols, and saves the result. Returns nil on success.
//
// This is the top-level function called by gt init and gt up.
func EnsureLifecycleConfigFile(townRoot string) error {
	config := LoadPatrolConfig(townRoot)
	if config == nil {
		config = DefaultLifecycleConfig()
		return SavePatrolConfig(townRoot, config)
	}

	if EnsureLifecycleDefaults(config) {
		return SavePatrolConfig(townRoot, config)
	}

	return nil // Already configured, nothing to do
}
