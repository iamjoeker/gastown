package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/witness"
)

type progressDiagnostics struct {
	bytes.Buffer
	sawProgress chan struct{}
	once        sync.Once
}

func (d *progressDiagnostics) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "still running") {
		d.once.Do(func() { close(d.sawProgress) })
	}
	return d.Buffer.Write(p)
}

func TestPatrolScanOutputJSON(t *testing.T) {
	output := PatrolScanOutput{
		Rig:       "gastown",
		Timestamp: "2026-03-17T12:00:00Z",
		Zombies: &PatrolScanZombieOutput{
			Checked: 3,
			Found:   1,
			Zombies: []PatrolScanZombieItem{
				{
					Polecat:        "alpha",
					Classification: "session-dead-active",
					AgentState:     "working",
					HookBead:       "gas-abc",
					Action:         "restarted",
					WasActive:      true,
				},
			},
		},
		Receipts: []witness.PatrolReceipt{
			{
				Rig:               "gastown",
				Polecat:           "alpha",
				Verdict:           witness.PatrolVerdictStale,
				RecommendedAction: "restarted",
				Evidence: witness.PatrolReceiptEvidence{
					AgentState:     "working",
					Classification: witness.ZombieSessionDeadActive,
					HookBead:       "gas-abc",
				},
			},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal output: %v", err)
	}

	var parsed PatrolScanOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if parsed.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", parsed.Rig, "gastown")
	}
	if parsed.Zombies.Found != 1 {
		t.Errorf("Zombies.Found = %d, want 1", parsed.Zombies.Found)
	}
	if parsed.Zombies.Checked != 3 {
		t.Errorf("Zombies.Checked = %d, want 3", parsed.Zombies.Checked)
	}
	if len(parsed.Zombies.Zombies) != 1 {
		t.Fatalf("len(Zombies) = %d, want 1", len(parsed.Zombies.Zombies))
	}
	z := parsed.Zombies.Zombies[0]
	if z.Polecat != "alpha" {
		t.Errorf("zombie Polecat = %q, want %q", z.Polecat, "alpha")
	}
	if z.Classification != "session-dead-active" {
		t.Errorf("zombie Classification = %q, want %q", z.Classification, "session-dead-active")
	}
	if !z.WasActive {
		t.Error("zombie WasActive = false, want true")
	}
	if len(parsed.Receipts) != 1 {
		t.Fatalf("len(Receipts) = %d, want 1", len(parsed.Receipts))
	}
	if parsed.Receipts[0].Verdict != witness.PatrolVerdictStale {
		t.Errorf("receipt Verdict = %q, want %q", parsed.Receipts[0].Verdict, witness.PatrolVerdictStale)
	}
}

func TestCountActiveWorkZombies(t *testing.T) {
	result := &witness.DetectZombiePolecatsResult{
		Zombies: []witness.ZombieResult{
			{PolecatName: "alpha", WasActive: true},
			{PolecatName: "beta", WasActive: false},
			{PolecatName: "gamma", WasActive: true},
		},
	}

	got := countActiveWorkZombies(result)
	if got != 2 {
		t.Errorf("countActiveWorkZombies() = %d, want 2", got)
	}
}

func TestCountActiveWorkZombies_Empty(t *testing.T) {
	result := &witness.DetectZombiePolecatsResult{}
	got := countActiveWorkZombies(result)
	if got != 0 {
		t.Errorf("countActiveWorkZombies() = %d, want 0", got)
	}
}

func TestRunPatrolScanPhaseEmitsProgressDiagnostics(t *testing.T) {
	oldInterval := patrolScanProgressInterval
	patrolScanProgressInterval = 10 * time.Millisecond
	defer func() { patrolScanProgressInterval = oldInterval }()

	diagnostics := &progressDiagnostics{sawProgress: make(chan struct{})}
	release := make(chan struct{})
	go func() {
		select {
		case <-diagnostics.sawProgress:
		case <-time.After(time.Second):
		}
		close(release)
	}()

	got, reason := runPatrolScanPhase(diagnostics, "slow phase", func() string {
		<-release
		return "ok"
	})

	if got != "ok" {
		t.Fatalf("runPatrolScanPhase result = %q, want ok", got)
	}
	if reason != "" {
		t.Fatalf("runPatrolScanPhase reason = %q, want empty for a phase that finished", reason)
	}

	output := diagnostics.String()
	select {
	case <-diagnostics.sawProgress:
	default:
		t.Fatalf("diagnostics %q never emitted progress", output)
	}
	for _, want := range []string{
		"gt patrol scan: starting slow phase",
		"gt patrol scan: still running slow phase after",
		"gt patrol scan: finished slow phase in",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("diagnostics %q missing %q", output, want)
		}
	}
}

func TestRunPatrolScanPhaseZeroIntervalSkipsProgressTicks(t *testing.T) {
	oldInterval := patrolScanProgressInterval
	patrolScanProgressInterval = 0
	defer func() { patrolScanProgressInterval = oldInterval }()

	var diagnostics bytes.Buffer
	got, reason := runPatrolScanPhase(&diagnostics, "fast phase", func() int {
		return 42
	})

	if got != 42 {
		t.Fatalf("runPatrolScanPhase result = %d, want 42", got)
	}
	if reason != "" {
		t.Fatalf("runPatrolScanPhase reason = %q, want empty for a phase that finished", reason)
	}

	output := diagnostics.String()
	if strings.Contains(output, "still running") {
		t.Fatalf("diagnostics should not include progress tick when interval is disabled: %q", output)
	}
	for _, want := range []string{
		"gt patrol scan: starting fast phase",
		"gt patrol scan: finished fast phase in",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("diagnostics %q missing %q", output, want)
		}
	}
}

func TestPatrolScanZombieItemSerialization(t *testing.T) {
	item := PatrolScanZombieItem{
		Polecat:        "obsidian",
		Classification: "agent-dead-in-session",
		AgentState:     "working",
		HookBead:       "gas-xyz",
		CleanupStatus:  "has_uncommitted",
		Action:         "restarted-dirty (cleanup_status=has_uncommitted, wisp=gas-wisp-123)",
		WasActive:      true,
		Error:          "restart failed: tmux error",
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("failed to marshal item: %v", err)
	}

	var parsed PatrolScanZombieItem
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal item: %v", err)
	}

	if parsed.Polecat != "obsidian" {
		t.Errorf("Polecat = %q, want %q", parsed.Polecat, "obsidian")
	}
	if parsed.CleanupStatus != "has_uncommitted" {
		t.Errorf("CleanupStatus = %q, want %q", parsed.CleanupStatus, "has_uncommitted")
	}
	if parsed.Error != "restart failed: tmux error" {
		t.Errorf("Error = %q, want %q", parsed.Error, "restart failed: tmux error")
	}
}

// A phase that never returns used to park the whole command — the witness that
// hit this was killed at 5m0s inside completion discovery with Dolt healthy
// throughout (gt-nof6). The deadline turns that into a reported failure.
func TestRunPatrolScanPhaseAbandonsHungPhase(t *testing.T) {
	oldTimeout := patrolScanPhaseTimeout
	oldInterval := patrolScanProgressInterval
	patrolScanPhaseTimeout = 20 * time.Millisecond
	patrolScanProgressInterval = 5 * time.Millisecond
	defer func() {
		patrolScanPhaseTimeout = oldTimeout
		patrolScanProgressInterval = oldInterval
	}()

	release := make(chan struct{})
	defer close(release)

	var diagnostics bytes.Buffer
	got, reason := runPatrolScanPhase(&diagnostics, "completion discovery", func() *witness.DiscoverCompletionsResult {
		<-release
		return &witness.DiscoverCompletionsResult{}
	})

	if got != nil {
		t.Fatalf("runPatrolScanPhase result = %v, want nil for an abandoned phase", got)
	}
	if !strings.Contains(reason, "completion discovery timed out") {
		t.Fatalf("reason = %q, want it to name the phase and the timeout", reason)
	}
	if !strings.Contains(diagnostics.String(), "not empty") {
		t.Fatalf("diagnostics %q must say the phase's results are unknown, not empty", diagnostics.String())
	}
}

// The deadline must not depend on progress reporting being enabled: with the
// ticker off, the old loop blocked on the result channel alone.
func TestRunPatrolScanPhaseAbandonsHungPhaseWithProgressDisabled(t *testing.T) {
	oldTimeout := patrolScanPhaseTimeout
	oldInterval := patrolScanProgressInterval
	patrolScanPhaseTimeout = 20 * time.Millisecond
	patrolScanProgressInterval = 0
	defer func() {
		patrolScanPhaseTimeout = oldTimeout
		patrolScanProgressInterval = oldInterval
	}()

	release := make(chan struct{})
	defer close(release)

	var diagnostics bytes.Buffer
	_, reason := runPatrolScanPhase(&diagnostics, "zombie detection", func() int {
		<-release
		return 1
	})

	if reason == "" {
		t.Fatal("hung phase returned no reason with progress reporting disabled")
	}
	if strings.Contains(diagnostics.String(), "still running") {
		t.Fatalf("progress ticks leaked with interval disabled: %q", diagnostics.String())
	}
}

// A timed-out phase reports zero of everything. The summary line must not turn
// that into an all-clear — the reading that gets believed is the wrong one.
func TestPatrolScanHumanSummaryFlagsIncompleteScan(t *testing.T) {
	out := captureStdout(t, func() {
		if err := outputPatrolScanHuman("gastown", false,
			&witness.DetectZombiePolecatsResult{}, &witness.DetectStalledPolecatsResult{}, nil,
			nil, []string{"completion discovery timed out after 3m0s"}); err != nil {
			t.Fatalf("outputPatrolScanHuman: %v", err)
		}
	})

	if strings.Contains(out, "All clear") {
		t.Fatalf("incomplete scan reported as all clear:\n%s", out)
	}
	for _, want := range []string{"INCOMPLETE", "completion discovery timed out", "PARTIAL"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPatrolScanHumanSummaryStillReportsAllClear(t *testing.T) {
	out := captureStdout(t, func() {
		if err := outputPatrolScanHuman("gastown", false,
			&witness.DetectZombiePolecatsResult{}, &witness.DetectStalledPolecatsResult{},
			&witness.DiscoverCompletionsResult{}, nil, nil); err != nil {
			t.Fatalf("outputPatrolScanHuman: %v", err)
		}
	})

	if !strings.Contains(out, "All clear") {
		t.Fatalf("a scan that finished with nothing to find should still be all clear:\n%s", out)
	}
}
