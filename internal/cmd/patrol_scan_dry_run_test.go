package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/witness"
)

func TestPatrolScanDryRunFlagIsRegistered(t *testing.T) {
	flag := patrolScanCmd.Flags().Lookup("dry-run")
	if flag == nil {
		t.Fatal("gt patrol scan has no --dry-run flag")
	}
	if flag.Value.String() != "false" {
		t.Errorf("--dry-run defaults to %q, want false — a scan must act unless told not to", flag.Value.String())
	}
}

// The verdict is what --dry-run exists to expose, so it has to survive the trip
// from the detection result into the reported output. A field that is computed
// and then dropped at the boundary is indistinguishable from one never computed.
func TestPatrolScanDryRunReportsRestartVerdict(t *testing.T) {
	zombies := &witness.DetectZombiePolecatsResult{
		Checked: 2,
		Zombies: []witness.ZombieResult{{
			PolecatName:    "furiosa",
			AgentState:     "working",
			Classification: witness.ZombieStuckInDone,
			HookBead:       "gt-gq7",
			WasActive:      true,
			RestartVerdict: "busy",
			Action:         "restart-deferred-session-busy (stuck-in-done verdict is stale; agent is mid-turn)",
		}},
	}

	out := captureStdout(t, func() {
		if err := outputPatrolScanHuman("gastown", true, zombies,
			&witness.DetectStalledPolecatsResult{}, &witness.DiscoverCompletionsResult{}, nil, nil); err != nil {
			t.Fatalf("outputPatrolScanHuman: %v", err)
		}
	})

	for _, want := range []string{"DRY RUN", "Restart guard: busy", "WOULD:", "furiosa",
		"reached 1 restart decision"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out)
		}
	}
	// "Action:" is the live label. Under a dry run every line it prefixes would
	// be read as something that happened.
	if strings.Contains(out, "Action:") {
		t.Errorf("dry-run output claims actions were taken:\n%s", out)
	}
}

// The control for the test above: with dry-run off, the same result renders as
// actions taken and carries no dry-run banner. Without this, "no Action: label"
// is satisfied by an output function that stopped printing actions at all.
func TestPatrolScanLiveRunReportsActionsTaken(t *testing.T) {
	zombies := &witness.DetectZombiePolecatsResult{
		Checked: 2,
		Zombies: []witness.ZombieResult{{
			PolecatName:    "quiet",
			AgentState:     "working",
			Classification: witness.ZombieStuckInDone,
			RestartVerdict: "proceed",
			Action:         "restarted-stuck-session (done-intent age=2h)",
		}},
	}

	out := captureStdout(t, func() {
		if err := outputPatrolScanHuman("gastown", false, zombies,
			&witness.DetectStalledPolecatsResult{}, &witness.DiscoverCompletionsResult{}, nil, nil); err != nil {
			t.Fatalf("outputPatrolScanHuman: %v", err)
		}
	})

	if !strings.Contains(out, "Action: restarted-stuck-session") {
		t.Errorf("live output does not report the action taken:\n%s", out)
	}
	if strings.Contains(out, "DRY RUN") || strings.Contains(out, "WOULD:") {
		t.Errorf("live scan output carries dry-run framing:\n%s", out)
	}
	if !strings.Contains(out, "Restart guard: proceed") {
		t.Errorf("live output drops the restart verdict:\n%s", out)
	}
}

// dry_run is written unconditionally so a consumer can tell a live scan from an
// old scan that predates the field. omitempty would collapse those two.
func TestPatrolScanJSONAlwaysCarriesDryRun(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		data, err := json.Marshal(PatrolScanOutput{Rig: "gastown", DryRun: dryRun})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got, present := decoded["dry_run"]
		if !present {
			t.Fatalf("dry_run absent from JSON with DryRun=%v: %s", dryRun, data)
		}
		if got != dryRun {
			t.Errorf("dry_run = %v, want %v", got, dryRun)
		}
	}
}

// A healthy fleet produces no restart decisions at all. That zero is the exact
// silence the split hold was about, so a dry run has to say it out loud rather
// than print an all-clear that reads identically to a guard that never ran.
func TestPatrolScanDryRunStatesWhenGuardReachedNoDecision(t *testing.T) {
	zombies := &witness.DetectZombiePolecatsResult{
		Checked: 25,
		Zombies: []witness.ZombieResult{{
			PolecatName:    "idle-dirty",
			Classification: witness.ZombieIdleDirtySandbox,
			CleanupStatus:  "has_unpushed",
			Action:         "detected-dirty-idle-polecat",
		}},
	}

	out := captureStdout(t, func() {
		if err := outputPatrolScanHuman("gastown", true, zombies,
			&witness.DetectStalledPolecatsResult{}, &witness.DiscoverCompletionsResult{}, nil, nil); err != nil {
			t.Fatalf("outputPatrolScanHuman: %v", err)
		}
	})

	if !strings.Contains(out, "reached 0 restart decisions") {
		t.Errorf("dry run does not state that the guard reached no decision:\n%s", out)
	}
	if strings.Contains(out, "Restart guard: proceed") || strings.Contains(out, "Restart guard: busy") {
		t.Errorf("a classification with no restart decision reported a verdict:\n%s", out)
	}
}

// restart_decisions is written unconditionally: zero is the load-bearing value.
func TestPatrolScanJSONAlwaysCarriesRestartDecisions(t *testing.T) {
	data, err := json.Marshal(PatrolScanZombieOutput{Checked: 25, Found: 0, RestartDecisions: 0})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"restart_decisions":0`) {
		t.Errorf("restart_decisions elided when zero — the case it exists for: %s", data)
	}
}

func TestCountRestartDecisionsCountsOnlyDecidedVerdicts(t *testing.T) {
	got := countRestartDecisions(&witness.DetectZombiePolecatsResult{
		Zombies: []witness.ZombieResult{
			{PolecatName: "a", RestartVerdict: "busy"},
			{PolecatName: "b", RestartVerdict: "proceed"},
			{PolecatName: "c"}, // no restart decision reached
		},
	})
	if got != 2 {
		t.Errorf("countRestartDecisions = %d, want 2", got)
	}
	if got := countRestartDecisions(nil); got != 0 {
		t.Errorf("countRestartDecisions(nil) = %d, want 0", got)
	}
}

func TestPatrolScanJSONCarriesRestartVerdict(t *testing.T) {
	data, err := json.Marshal(PatrolScanZombieItem{Polecat: "furiosa", RestartVerdict: "busy"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"restart_verdict":"busy"`) {
		t.Errorf("restart_verdict missing from zombie item JSON: %s", data)
	}

	// Absent, not "proceed", when no restart decision was reached — those are
	// different facts and the JSON must not merge them.
	data, err = json.Marshal(PatrolScanZombieItem{Polecat: "idle-dirty"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "restart_verdict") {
		t.Errorf("restart_verdict present for a classification that reached no decision: %s", data)
	}
}
