package deacon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHeartbeatFile(t *testing.T) {
	townRoot := "/tmp/test-town"
	expected := filepath.Join(townRoot, "deacon", "heartbeat.json")

	result := HeartbeatFile(townRoot)
	if result != expected {
		t.Errorf("HeartbeatFile() = %q, want %q", result, expected)
	}
}

func TestWriteReadHeartbeat(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	hb := &Heartbeat{
		Timestamp:       time.Now().UTC(),
		Cycle:           42,
		LastAction:      "health check",
		HealthyAgents:   3,
		UnhealthyAgents: 1,
	}

	// Write heartbeat
	if err := WriteHeartbeat(tmpDir, hb); err != nil {
		t.Fatalf("WriteHeartbeat error: %v", err)
	}

	// Verify file exists
	hbFile := HeartbeatFile(tmpDir)
	if _, err := os.Stat(hbFile); err != nil {
		t.Errorf("heartbeat file not created: %v", err)
	}

	// Read heartbeat
	loaded := ReadHeartbeat(tmpDir)
	if loaded == nil {
		t.Fatal("ReadHeartbeat returned nil")
	}

	if loaded.Cycle != 42 {
		t.Errorf("Cycle = %d, want 42", loaded.Cycle)
	}
	if loaded.LastAction != "health check" {
		t.Errorf("LastAction = %q, want 'health check'", loaded.LastAction)
	}
	if loaded.HealthyAgents != 3 {
		t.Errorf("HealthyAgents = %d, want 3", loaded.HealthyAgents)
	}
	if loaded.UnhealthyAgents != 1 {
		t.Errorf("UnhealthyAgents = %d, want 1", loaded.UnhealthyAgents)
	}
}

func TestReadHeartbeat_NonExistent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Read from non-existent file
	hb := ReadHeartbeat(tmpDir)
	if hb != nil {
		t.Error("expected nil for non-existent heartbeat")
	}
}

func TestHeartbeat_Age(t *testing.T) {
	// Test nil heartbeat
	var nilHb *Heartbeat
	if nilHb.Age() < 24*time.Hour {
		t.Error("nil heartbeat should have very large age")
	}

	// Test recent heartbeat
	hb := &Heartbeat{
		Timestamp: time.Now().Add(-30 * time.Second),
	}
	if hb.Age() > time.Minute {
		t.Errorf("Age() = %v, expected < 1 minute", hb.Age())
	}
}

// aged returns a heartbeat stamped the given duration ago.
func aged(age time.Duration) *Heartbeat {
	return &Heartbeat{Timestamp: time.Now().Add(-age)}
}

// bandAges names one age in each band, expressed relative to the thresholds
// rather than as literal minutes. Pinning the ages to numbers is what let the
// old 5m calibration sit under a fully green suite: every case had been chosen
// to sit either side of 5m, so the tests agreed with the threshold no matter
// what the threshold was worth (gt-cbd).
var (
	ageFresh     = HeartbeatStaleThreshold / 2
	ageStale     = HeartbeatStaleThreshold + time.Minute
	ageLateStale = HeartbeatVeryStaleThreshold - time.Minute
	ageVeryStale = HeartbeatVeryStaleThreshold + time.Minute
)

func TestHeartbeat_IsFresh(t *testing.T) {
	tests := []struct {
		name     string
		hb       *Heartbeat
		expected bool
	}{
		{
			name:     "nil heartbeat",
			hb:       nil,
			expected: false,
		},
		{
			name:     "just now",
			hb:       aged(0),
			expected: true,
		},
		{
			name:     "inside the fresh window",
			hb:       aged(ageFresh),
			expected: true,
		},
		{
			name:     "past the stale threshold",
			hb:       aged(ageStale),
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.hb.IsFresh()
			if result != tc.expected {
				t.Errorf("IsFresh() = %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestHeartbeat_IsStale(t *testing.T) {
	tests := []struct {
		name     string
		hb       *Heartbeat
		expected bool
	}{
		{
			name:     "nil heartbeat",
			hb:       nil,
			expected: false,
		},
		{
			name:     "inside the fresh window",
			hb:       aged(ageFresh),
			expected: false,
		},
		{
			name:     "just into the stale band",
			hb:       aged(ageStale),
			expected: true,
		},
		{
			name:     "late in the stale band",
			hb:       aged(ageLateStale),
			expected: true,
		},
		{
			name:     "past very-stale is no longer merely stale",
			hb:       aged(ageVeryStale),
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.hb.IsStale()
			if result != tc.expected {
				t.Errorf("IsStale() = %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestHeartbeat_IsVeryStale(t *testing.T) {
	tests := []struct {
		name     string
		hb       *Heartbeat
		expected bool
	}{
		{
			name:     "nil heartbeat",
			hb:       nil,
			expected: true,
		},
		{
			name:     "inside the fresh window",
			hb:       aged(ageFresh),
			expected: false,
		},
		{
			name:     "just into the stale band",
			hb:       aged(ageStale),
			expected: false,
		},
		{
			name:     "late in the stale band",
			hb:       aged(ageLateStale),
			expected: false,
		},
		{
			name:     "past the very-stale threshold",
			hb:       aged(ageVeryStale),
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.hb.IsVeryStale()
			if result != tc.expected {
				t.Errorf("IsVeryStale() = %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestTouch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// First touch
	if err := Touch(tmpDir); err != nil {
		t.Fatalf("Touch error: %v", err)
	}

	hb := ReadHeartbeat(tmpDir)
	if hb == nil {
		t.Fatal("expected heartbeat after Touch")
	}
	if hb.Cycle != 1 {
		t.Errorf("first Touch: Cycle = %d, want 1", hb.Cycle)
	}

	// Second touch should increment cycle
	if err := Touch(tmpDir); err != nil {
		t.Fatalf("Touch error: %v", err)
	}

	hb = ReadHeartbeat(tmpDir)
	if hb.Cycle != 2 {
		t.Errorf("second Touch: Cycle = %d, want 2", hb.Cycle)
	}
}

func TestTouchWithAction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := TouchWithAction(tmpDir, "health scan", 5, 2); err != nil {
		t.Fatalf("TouchWithAction error: %v", err)
	}

	hb := ReadHeartbeat(tmpDir)
	if hb == nil {
		t.Fatal("expected heartbeat after TouchWithAction")
	}
	if hb.Cycle != 1 {
		t.Errorf("Cycle = %d, want 1", hb.Cycle)
	}
	if hb.LastAction != "health scan" {
		t.Errorf("LastAction = %q, want 'health scan'", hb.LastAction)
	}
	if hb.HealthyAgents != 5 {
		t.Errorf("HealthyAgents = %d, want 5", hb.HealthyAgents)
	}
	if hb.UnhealthyAgents != 2 {
		t.Errorf("UnhealthyAgents = %d, want 2", hb.UnhealthyAgents)
	}
}

// Timestamp and Cycle must move together on every production write.
//
// This invariant is load-bearing well outside this file: the wedge detector in
// cycle_watch.go treats "timestamp advanced, cycle did not" as proof that a
// Deacon is frozen mid-patrol. gt-bvo reported seeing exactly that in the field
// and gt-s6r spent a bead disproving it — 1182 recorded polls, no such write.
//
// So if this test ever fails, an off-path writer has appeared, and it is not a
// test to relax: it is the signal gt-s6r went looking for. The detector's
// premise would become true, and its verdict would start firing.
func TestTouch_CouplesTimestampAndCycle(t *testing.T) {
	tmpDir := t.TempDir()

	var prev *Heartbeat
	for i, write := range []func() error{
		func() error { return Touch(tmpDir) },
		func() error { return TouchWithAction(tmpDir, "starting patrol cycle", 3, 0) },
		func() error { return Touch(tmpDir) },
		func() error { return TouchWithAction(tmpDir, "pre-await checkpoint", 0, 0) },
	} {
		if err := write(); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}

		hb := ReadHeartbeat(tmpDir)
		if hb == nil {
			t.Fatalf("write %d: no heartbeat on disk", i)
		}
		if want := int64(i + 1); hb.Cycle != want {
			t.Fatalf("write %d: Cycle = %d, want %d", i, hb.Cycle, want)
		}
		if prev != nil && !hb.Timestamp.After(prev.Timestamp) {
			t.Errorf("write %d: Timestamp %v did not advance past %v — a write that "+
				"refreshes the cycle without the timestamp breaks the same coupling",
				i, hb.Timestamp, prev.Timestamp)
		}
		prev = hb
	}

	// The decoupled write the detector looks for: same cycle, later timestamp.
	// Reachable only by constructing the Heartbeat directly, which no non-test
	// caller does — Touch and TouchWithAction are the only writers.
	frozen := prev.Cycle
	if err := WriteHeartbeat(tmpDir, &Heartbeat{Timestamp: prev.Timestamp.Add(time.Minute), Cycle: frozen}); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}
	if hb := ReadHeartbeat(tmpDir); hb.Cycle != frozen {
		t.Fatalf("Cycle = %d, want %d — the escape hatch must be the raw writer, not Touch", hb.Cycle, frozen)
	}
	if err := Touch(tmpDir); err != nil {
		t.Fatalf("Touch after raw write: %v", err)
	}
	if hb := ReadHeartbeat(tmpDir); hb.Cycle != frozen+1 {
		t.Errorf("Cycle = %d, want %d — Touch must resume incrementing from whatever it reads",
			hb.Cycle, frozen+1)
	}
}

func TestWriteHeartbeat_CreatesDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Ensure deacon directory doesn't exist
	deaconDir := filepath.Join(tmpDir, "deacon")
	if _, err := os.Stat(deaconDir); !os.IsNotExist(err) {
		t.Fatal("deacon directory should not exist initially")
	}

	// Write heartbeat should create directory
	hb := &Heartbeat{Cycle: 1}
	if err := WriteHeartbeat(tmpDir, hb); err != nil {
		t.Fatalf("WriteHeartbeat error: %v", err)
	}

	// Verify directory was created
	if _, err := os.Stat(deaconDir); err != nil {
		t.Errorf("deacon directory should exist: %v", err)
	}
}

func TestWriteHeartbeat_TouchesLegacyFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	hb := &Heartbeat{Cycle: 1}
	if err := WriteHeartbeat(tmpDir, hb); err != nil {
		t.Fatalf("WriteHeartbeat error: %v", err)
	}

	// Legacy .deacon-heartbeat should also exist so shell scripts (stuck-agent-dog)
	// that check this file's mtime get accurate data.
	legacyFile := filepath.Join(tmpDir, "deacon", ".deacon-heartbeat")
	info, err := os.Stat(legacyFile)
	if err != nil {
		t.Errorf(".deacon-heartbeat not created: %v", err)
		return
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Error(".deacon-heartbeat mtime should be recent")
	}
}

func TestWriteHeartbeat_SetsTimestamp(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Write heartbeat without timestamp
	hb := &Heartbeat{Cycle: 1}
	if err := WriteHeartbeat(tmpDir, hb); err != nil {
		t.Fatalf("WriteHeartbeat error: %v", err)
	}

	// Read back and verify timestamp was set
	loaded := ReadHeartbeat(tmpDir)
	if loaded == nil {
		t.Fatal("expected heartbeat")
	}
	if loaded.Timestamp.IsZero() {
		t.Error("expected Timestamp to be set")
	}
	if time.Since(loaded.Timestamp) > time.Minute {
		t.Error("Timestamp should be recent")
	}
}
