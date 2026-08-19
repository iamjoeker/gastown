package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultLifecycleConfig(t *testing.T) {
	config := DefaultLifecycleConfig()

	if config.Type != "daemon-patrol-config" {
		t.Errorf("expected type daemon-patrol-config, got %s", config.Type)
	}
	if config.Version != 1 {
		t.Errorf("expected version 1, got %d", config.Version)
	}
	if config.Patrols == nil {
		t.Fatal("expected patrols to be non-nil")
	}

	p := config.Patrols

	// Verify all patrols are enabled with expected defaults
	if p.WispReaper == nil || !p.WispReaper.Enabled {
		t.Error("expected wisp_reaper to be enabled")
	}
	// 1h, not 30m: the provisioned interval is now read from
	// defaultWispReaperInterval, which is the value a town without a daemon.json
	// has always used (gt-r4lv).
	if p.WispReaper.IntervalStr != "1h" {
		t.Errorf("expected wisp_reaper interval 1h, got %s", p.WispReaper.IntervalStr)
	}
	if p.WispReaper.DeleteAgeStr != "168h" {
		t.Errorf("expected wisp_reaper delete_age 168h, got %s", p.WispReaper.DeleteAgeStr)
	}

	if p.CompactorDog == nil || !p.CompactorDog.Enabled {
		t.Error("expected compactor_dog to be enabled")
	}
	if p.CompactorDog.Threshold != 2000 {
		t.Errorf("expected compactor_dog threshold 2000, got %d", p.CompactorDog.Threshold)
	}

	if p.CheckpointDog == nil || !p.CheckpointDog.Enabled {
		t.Error("expected checkpoint_dog to be enabled")
	}
	if p.CheckpointDog.IntervalStr != "10m" {
		t.Errorf("expected checkpoint_dog interval 10m, got %s", p.CheckpointDog.IntervalStr)
	}

	if p.DoctorDog == nil || !p.DoctorDog.Enabled {
		t.Error("expected doctor_dog to be enabled")
	}

	if p.JsonlGitBackup == nil || !p.JsonlGitBackup.Enabled {
		t.Error("expected jsonl_git_backup to be enabled")
	}
	if p.JsonlGitBackup.Scrub == nil || !*p.JsonlGitBackup.Scrub {
		t.Error("expected jsonl_git_backup scrub to be true")
	}

	if p.DoltBackup == nil || !p.DoltBackup.Enabled {
		t.Error("expected dolt_backup to be enabled")
	}

	if p.ScheduledMaintenance == nil || !p.ScheduledMaintenance.Enabled {
		t.Error("expected scheduled_maintenance to be enabled")
	}
	if p.ScheduledMaintenance.Window != "03:00" {
		t.Errorf("expected maintenance window 03:00, got %s", p.ScheduledMaintenance.Window)
	}
	if p.ScheduledMaintenance.Threshold == nil || *p.ScheduledMaintenance.Threshold != 1000 {
		t.Error("expected maintenance threshold 1000")
	}

	if p.MainBranchTest == nil || !p.MainBranchTest.Enabled {
		t.Error("expected main_branch_test to be enabled")
	}
	if p.MainBranchTest.IntervalStr != "30m" {
		t.Errorf("expected main_branch_test interval 30m, got %s", p.MainBranchTest.IntervalStr)
	}
	if p.MainBranchTest.TimeoutStr != "10m" {
		t.Errorf("expected main_branch_test timeout 10m, got %s", p.MainBranchTest.TimeoutStr)
	}
}

// TestDefaultLifecycleConfigMatchesCompiledFallbacks is the guard for the whole
// class of bug behind gt-r4lv: a patrol knob whose provisioned default (this
// file, written into mayor/daemon.json by gt init) is a different number from the
// compiled-in fallback its accessor returns when the key is absent.
//
// The town then behaves one way with a provisioned daemon.json and another way
// without one, and no surface reports which you have — `gt config list` reads the
// provisioned tree, the daemon reads the accessor.
//
// The assertion is deliberately phrased through the accessors rather than
// comparing literals: for every knob, asking the daemon what it will do with the
// provisioned config must give the same answer as asking it with no config at
// all. That holds for any future patrol added to the tree, without this test
// naming its constant.
//
// Two kinds of field are exempt, and the exemptions are the point rather than
// oversights:
//   - Enabled flags: provisioned true, absent false. "Not configured" genuinely
//     means "not running"; that is not a disagreement about a default.
//   - ScheduledMaintenance.Window: absent means "no maintenance window", so
//     provisioning 03:00 is an opt-in, not a restatement.
func TestDefaultLifecycleConfigMatchesCompiledFallbacks(t *testing.T) {
	provisioned := DefaultLifecycleConfig()

	durations := []struct {
		key      string
		accessor func(*DaemonPatrolConfig) time.Duration
	}{
		{"patrols.wisp_reaper.interval", wispReaperInterval},
		{"patrols.wisp_reaper.max_age", wispReaperMaxAge},
		{"patrols.wisp_reaper.delete_age", wispDeleteAge},
		{"patrols.wisp_reaper.stale_issue_age", staleIssueAge},
		{"patrols.wisp_reaper.mail_delete_age", mailDeleteAge},
		{"patrols.compactor_dog.interval", compactorDogInterval},
		{"patrols.checkpoint_dog.interval", checkpointDogInterval},
		{"patrols.doctor_dog.interval", doctorDogInterval},
		{"patrols.jsonl_git_backup.interval", jsonlGitBackupInterval},
		{"patrols.dolt_backup.interval", doltBackupInterval},
		{"patrols.main_branch_test.interval", mainBranchTestInterval},
		{"patrols.main_branch_test.timeout", mainBranchTestTimeout},
	}
	for _, tc := range durations {
		got, fallback := tc.accessor(provisioned), tc.accessor(nil)
		if got != fallback {
			t.Errorf("%s: provisioned default acts as %v, compiled fallback acts as %v — "+
				"a town created by gt init would behave differently from a town without daemon.json",
				tc.key, got, fallback)
		}
	}

	counts := []struct {
		key      string
		accessor func(*DaemonPatrolConfig) int
	}{
		{"patrols.compactor_dog.threshold", compactorDogThreshold},
		{"patrols.scheduled_maintenance.threshold", maintenanceThreshold},
	}
	for _, tc := range counts {
		got, fallback := tc.accessor(provisioned), tc.accessor(nil)
		if got != fallback {
			t.Errorf("%s: provisioned default acts as %d, compiled fallback acts as %d",
				tc.key, got, fallback)
		}
	}

	if got, fallback := maintenanceInterval(provisioned), maintenanceInterval(nil); got != fallback {
		t.Errorf("patrols.scheduled_maintenance.interval: provisioned %q, compiled fallback %q", got, fallback)
	}
}

// TestDurationDefaultRoundTrips holds the property durationDefault exists for:
// the string it writes into daemon.json must parse back to the constant it came
// from. Trimming "0m0s" for readability must never change the value, and must not
// mangle a duration that has real minutes.
func TestDurationDefaultRoundTrips(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{time.Hour, "1h"},
		{24 * time.Hour, "24h"},
		{7 * 24 * time.Hour, "168h"},
		{30 * 24 * time.Hour, "720h"},
		{5 * time.Minute, "5m"},
		{10 * time.Minute, "10m"},
		{30 * time.Minute, "30m"},
		{time.Hour + 30*time.Minute, "1h30m"}, // real minutes survive
		{90 * time.Second, "1m30s"},           // real seconds survive
		{30 * time.Second, "30s"},
		{0, "0s"},
	}
	for _, tc := range cases {
		got := durationDefault(tc.in)
		if got != tc.want {
			t.Errorf("durationDefault(%v) = %q, want %q", tc.in, got, tc.want)
		}
		back, err := time.ParseDuration(got)
		if err != nil {
			t.Errorf("durationDefault(%v) = %q does not parse: %v", tc.in, got, err)
			continue
		}
		if back != tc.in {
			t.Errorf("durationDefault(%v) = %q parses back to %v", tc.in, got, back)
		}
	}
}

func TestEnsureLifecycleDefaults_NilConfig(t *testing.T) {
	if EnsureLifecycleDefaults(nil) {
		t.Error("expected false for nil config")
	}
}

func TestEnsureLifecycleDefaults_EmptyConfig(t *testing.T) {
	config := &DaemonPatrolConfig{Type: "daemon-patrol-config", Version: 1}
	changed := EnsureLifecycleDefaults(config)

	if !changed {
		t.Error("expected changes for empty config")
	}
	if config.Patrols == nil {
		t.Fatal("expected patrols to be set")
	}
	if config.Patrols.WispReaper == nil || !config.Patrols.WispReaper.Enabled {
		t.Error("expected wisp_reaper to be set")
	}
	if config.Patrols.CompactorDog == nil || !config.Patrols.CompactorDog.Enabled {
		t.Error("expected compactor_dog to be set")
	}
	if config.Patrols.CheckpointDog == nil || !config.Patrols.CheckpointDog.Enabled {
		t.Error("expected checkpoint_dog to be set")
	}
	if config.Patrols.Handler == nil || !config.Patrols.Handler.Enabled {
		t.Error("expected handler to be set")
	}
}

func TestEnsureLifecycleDefaults_PreservesExisting(t *testing.T) {
	// Config with user-customized wisp_reaper
	config := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:     true,
				IntervalStr: "1h", // User customized to 1h
				DeleteAgeStr: "336h", // User customized to 14 days
			},
		},
	}

	changed := EnsureLifecycleDefaults(config)

	if !changed {
		t.Error("expected changes (other patrols were nil)")
	}

	// User's wisp_reaper should be preserved
	if config.Patrols.WispReaper.IntervalStr != "1h" {
		t.Errorf("expected preserved interval 1h, got %s", config.Patrols.WispReaper.IntervalStr)
	}
	if config.Patrols.WispReaper.DeleteAgeStr != "336h" {
		t.Errorf("expected preserved delete_age 336h, got %s", config.Patrols.WispReaper.DeleteAgeStr)
	}

	// Other patrols should be filled in
	if config.Patrols.CompactorDog == nil || !config.Patrols.CompactorDog.Enabled {
		t.Error("expected compactor_dog to be filled in")
	}
	if config.Patrols.DoctorDog == nil {
		t.Error("expected doctor_dog to be filled in")
	}
}

func TestEnsureLifecycleDefaults_FullyConfigured(t *testing.T) {
	// Config with all patrols already set (even if disabled)
	threshold := 2000
	config := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{
			WispReaper:           &WispReaperConfig{Enabled: false},
			CompactorDog:         &CompactorDogConfig{Enabled: false},
			CheckpointDog:        &CheckpointDogConfig{Enabled: false},
			DoctorDog:            &DoctorDogConfig{Enabled: false},
			JsonlGitBackup:       &JsonlGitBackupConfig{Enabled: false},
			DoltBackup:           &DoltBackupConfig{Enabled: false},
			ScheduledMaintenance: &ScheduledMaintenanceConfig{Enabled: false, Threshold: &threshold},
			MainBranchTest:       &MainBranchTestConfig{Enabled: false},
			Handler:              &PatrolConfig{Enabled: false},
		},
	}

	changed := EnsureLifecycleDefaults(config)

	if changed {
		t.Error("expected no changes for fully configured config")
	}

	// User's disabled settings should be preserved
	if config.Patrols.WispReaper.Enabled {
		t.Error("expected wisp_reaper to remain disabled")
	}
	if config.Patrols.ScheduledMaintenance.Threshold == nil || *config.Patrols.ScheduledMaintenance.Threshold != 2000 {
		t.Error("expected threshold to remain 2000")
	}
}

func TestEnsureLifecycleConfigFile_NewFile(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	err := EnsureLifecycleConfigFile(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file was created
	configFile := filepath.Join(mayorDir, "daemon.json")
	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	var config DaemonPatrolConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if config.Patrols == nil {
		t.Fatal("expected patrols in created config")
	}
	if config.Patrols.WispReaper == nil || !config.Patrols.WispReaper.Enabled {
		t.Error("expected wisp_reaper to be enabled in new config")
	}
}

func TestEnsureLifecycleConfigFile_ExistingPartial(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write partial config with just env and wisp_reaper
	existing := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Env:     map[string]string{"GT_DOLT_PORT": "3307"},
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:     true,
				IntervalStr: "1h",
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	configFile := filepath.Join(mayorDir, "daemon.json")
	if err := os.WriteFile(configFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	err := EnsureLifecycleConfigFile(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reload and verify
	data, _ = os.ReadFile(configFile)
	var config DaemonPatrolConfig
	json.Unmarshal(data, &config)

	// Existing env preserved
	if config.Env["GT_DOLT_PORT"] != "3307" {
		t.Error("expected env to be preserved")
	}

	// Existing wisp_reaper preserved
	if config.Patrols.WispReaper.IntervalStr != "1h" {
		t.Errorf("expected preserved interval 1h, got %s", config.Patrols.WispReaper.IntervalStr)
	}

	// New patrols filled in
	if config.Patrols.CompactorDog == nil || !config.Patrols.CompactorDog.Enabled {
		t.Error("expected compactor_dog to be added")
	}
	if config.Patrols.DoctorDog == nil || !config.Patrols.DoctorDog.Enabled {
		t.Error("expected doctor_dog to be added")
	}
	if config.Patrols.ScheduledMaintenance == nil || !config.Patrols.ScheduledMaintenance.Enabled {
		t.Error("expected scheduled_maintenance to be added")
	}
}

func TestEnsureLifecycleConfigFile_ProductionScenario(t *testing.T) {
	// Simulates the actual production daemon.json: has core patrols (deacon,
	// refinery, witness) and explicitly disabled dolt_backup, but is missing
	// all data maintenance tickers (wisp_reaper, compactor_dog, doctor_dog,
	// jsonl_git_backup, scheduled_maintenance).
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	existing := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{
			Deacon:   &PatrolConfig{Enabled: true, Interval: "5m", Agent: "deacon"},
			Refinery: &PatrolConfig{Enabled: true, Interval: "5m", Agent: "refinery"},
			Witness:  &PatrolConfig{Enabled: true, Interval: "5m", Agent: "witness"},
			DoltBackup: &DoltBackupConfig{Enabled: false},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	configFile := filepath.Join(mayorDir, "daemon.json")
	if err := os.WriteFile(configFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	err := EnsureLifecycleConfigFile(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reload and verify
	data, _ = os.ReadFile(configFile)
	var config DaemonPatrolConfig
	json.Unmarshal(data, &config)

	// Core patrols preserved
	if config.Patrols.Deacon == nil || !config.Patrols.Deacon.Enabled {
		t.Error("expected deacon to remain enabled")
	}
	if config.Patrols.Refinery == nil || !config.Patrols.Refinery.Enabled {
		t.Error("expected refinery to remain enabled")
	}
	if config.Patrols.Witness == nil || !config.Patrols.Witness.Enabled {
		t.Error("expected witness to remain enabled")
	}

	// Explicitly disabled dolt_backup preserved (user intent)
	if config.Patrols.DoltBackup == nil {
		t.Fatal("expected dolt_backup config to be preserved")
	}
	if config.Patrols.DoltBackup.Enabled {
		t.Error("expected dolt_backup to remain disabled (user explicitly set false)")
	}

	// Missing lifecycle tickers auto-populated with defaults
	if config.Patrols.WispReaper == nil || !config.Patrols.WispReaper.Enabled {
		t.Error("expected wisp_reaper to be auto-populated and enabled")
	}
	if config.Patrols.CompactorDog == nil || !config.Patrols.CompactorDog.Enabled {
		t.Error("expected compactor_dog to be auto-populated and enabled")
	}
	if config.Patrols.CheckpointDog == nil || !config.Patrols.CheckpointDog.Enabled {
		t.Error("expected checkpoint_dog to be auto-populated and enabled")
	}
	if config.Patrols.DoctorDog == nil || !config.Patrols.DoctorDog.Enabled {
		t.Error("expected doctor_dog to be auto-populated and enabled")
	}
	if config.Patrols.JsonlGitBackup == nil || !config.Patrols.JsonlGitBackup.Enabled {
		t.Error("expected jsonl_git_backup to be auto-populated and enabled")
	}
	if config.Patrols.ScheduledMaintenance == nil || !config.Patrols.ScheduledMaintenance.Enabled {
		t.Error("expected scheduled_maintenance to be auto-populated and enabled")
	}
}

func TestEnsureLifecycleConfigFile_AlreadyComplete(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write fully configured file
	config := DefaultLifecycleConfig()
	data, _ := json.MarshalIndent(config, "", "  ")
	configFile := filepath.Join(mayorDir, "daemon.json")
	if err := os.WriteFile(configFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Get mod time before
	info1, _ := os.Stat(configFile)

	err := EnsureLifecycleConfigFile(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// File should not have been rewritten (same mod time)
	info2, _ := os.Stat(configFile)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("expected file to not be rewritten when already complete")
	}
}
