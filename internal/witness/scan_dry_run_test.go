package witness

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/tmux"
)

// swapScanEffectPrimitives redirects every mutating primitive a patrol scan can
// reach to a counter, and restores the originals afterwards. The returned map is
// keyed by the primitive's name so a test can name what fired.
//
// This is the harness the whole file rests on, so it is worth being explicit
// about what it does and does not prove: it proves the GATE, not the primitives.
// If a mutation site were added that bypasses scanEffects entirely, no counter
// here would move — which is why TestScanEffectsGateCoversEveryPrimitive pins
// the set, and why the CLI additionally wraps bd in ReadOnlyBdCli.
func swapScanEffectPrimitives(t *testing.T) map[string]int {
	t.Helper()
	fired := map[string]int{}

	origRestart := effRestartSession
	origNuke := effNukePolecat
	origCreateWisp := effCreateCleanupWisp
	origUpdateWisp := effUpdateCleanupWispState
	origClear := effClearCompletionMetadata
	origNudgeRefinery := effNudgeRefinery
	origNotifyMayor := effNotifyMayorSlotOpen
	origCloseBead := effCloseBead
	origNudgeSession := effNudgeSession
	origDismiss := effDismissStartupDialogs

	t.Cleanup(func() {
		effRestartSession = origRestart
		effNukePolecat = origNuke
		effCreateCleanupWisp = origCreateWisp
		effUpdateCleanupWispState = origUpdateWisp
		effClearCompletionMetadata = origClear
		effNudgeRefinery = origNudgeRefinery
		effNotifyMayorSlotOpen = origNotifyMayor
		effCloseBead = origCloseBead
		effNudgeSession = origNudgeSession
		effDismissStartupDialogs = origDismiss
	})

	effRestartSession = func(string, string, string) error { fired["restartSession"]++; return nil }
	effNukePolecat = func(*BdCli, string, string, string) error { fired["nukePolecat"]++; return nil }
	effCreateCleanupWisp = func(*BdCli, string, string, string, string) (string, error) {
		fired["createCleanupWisp"]++
		return "gt-wisp-live", nil
	}
	effUpdateCleanupWispState = func(*BdCli, string, string, string) error {
		fired["updateCleanupWispState"]++
		return nil
	}
	effClearCompletionMetadata = func(*BdCli, string, string) error { fired["clearCompletionMetadata"]++; return nil }
	effNudgeRefinery = func(string, string) error { fired["nudgeRefinery"]++; return nil }
	effNotifyMayorSlotOpen = func(string, string, string, string) { fired["notifyMayorSlotOpen"]++ }
	effCloseBead = func(*BdCli, string, string, string) { fired["closeBead"]++ }
	effNudgeSession = func(*tmux.Tmux, string, string) error { fired["nudgeSession"]++; return nil }
	effDismissStartupDialogs = func(*tmux.Tmux, string) error { fired["dismissStartupDialogs"]++; return nil }

	return fired
}

// exerciseEveryEffect calls every method on scanEffects exactly once.
//
// Keep this in step with scanEffects. A method that exists but is never called
// here is a method the dry-run gate is untested for, and the test would pass
// silently — which is the failure this whole change exists to make impossible.
func exerciseEveryEffect(e scanEffects) {
	bd := &BdCli{
		Exec: func(string, ...string) (string, error) { return "", nil },
		Run:  func(string, ...string) error { return nil },
	}
	var tm *tmux.Tmux // never dereferenced: both paths go through a swapped primitive

	_ = e.restartSession("/work", "rig", "polecat")
	_ = e.nukePolecat(bd, "/work", "rig", "polecat")
	_, _ = e.createCleanupWisp(bd, "/work", "polecat", "gt-1", "branch")
	_ = e.updateCleanupWispState(bd, "/work", "gt-wisp-1", "merge-requested")
	e.closeBead(bd, "/work", "gt-wisp-1", "duplicate")
	_ = e.clearCompletionMetadata(bd, "/work", "gt-agent-1")
	_ = e.nudgeSession(tm, "gt-rig-polecat", "hello")
	_ = e.dismissStartupDialogs(tm, "gt-rig-polecat")
	_ = e.nudgeRefinery("/town", "rig")
	e.notifyMayorSlotOpen("/work", "rig", "polecat", "COMPLETED")
}

// The gate proven in BOTH directions from one harness. The live half is the one
// that can fail: a gate that is inert because the primitives were never wired up
// would pass the dry-run half and tell you nothing.
func TestScanEffectsGateIsInertOnlyUnderDryRun(t *testing.T) {
	t.Run("live run performs every effect", func(t *testing.T) {
		fired := swapScanEffectPrimitives(t)
		exerciseEveryEffect(scanEffects{dryRun: false})

		if len(fired) == 0 {
			t.Fatal("no effect fired in a live run — the harness is not wired to anything")
		}
		for name, count := range fired {
			if count != 1 {
				t.Errorf("%s fired %d times in a live run, want 1", name, count)
			}
		}
	})

	t.Run("dry run performs none", func(t *testing.T) {
		fired := swapScanEffectPrimitives(t)
		exerciseEveryEffect(scanEffects{dryRun: true})

		for name, count := range fired {
			t.Errorf("%s fired %d times under --dry-run, want 0", name, count)
		}
	})
}

// The live half above proves each primitive it calls is reached. This proves the
// set it calls is the whole set: a new scanEffects method that nobody added to
// exerciseEveryEffect would leave its primitive var untouched, and this catches
// that without needing reflection over methods.
func TestScanEffectsGateCoversEveryPrimitive(t *testing.T) {
	fired := swapScanEffectPrimitives(t)
	exerciseEveryEffect(scanEffects{dryRun: false})

	want := []string{
		"restartSession", "nukePolecat", "createCleanupWisp", "updateCleanupWispState",
		"closeBead", "clearCompletionMetadata", "nudgeSession", "dismissStartupDialogs",
		"nudgeRefinery", "notifyMayorSlotOpen",
	}
	for _, name := range want {
		if fired[name] == 0 {
			t.Errorf("effect %q was never exercised — scanEffects and this test have drifted", name)
		}
	}
	if len(fired) != len(want) {
		t.Errorf("exercised %d effects, expected exactly %d: %v", len(fired), len(want), fired)
	}
}

// A dry run must not fabricate a bead ID that something downstream could act on.
func TestDryRunCleanupWispIDIsNotABeadID(t *testing.T) {
	e := scanEffects{dryRun: true}
	id, err := e.createCleanupWisp(nil, "/work", "polecat", "gt-1", "branch")
	if err != nil {
		t.Fatalf("createCleanupWisp under dry run: %v", err)
	}
	if id != DryRunWispID {
		t.Fatalf("wisp ID = %q, want the dry-run sentinel %q", id, DryRunWispID)
	}
	if !strings.HasPrefix(id, "(") {
		t.Errorf("sentinel %q could be mistaken for a real bead ID", id)
	}
}

func TestReadOnlyBdCliBlocksWritesAndAllowsReads(t *testing.T) {
	var ran []string
	inner := &BdCli{
		Exec: func(_ string, args ...string) (string, error) {
			ran = append(ran, strings.Join(args, " "))
			return "[]", nil
		},
		Run: func(_ string, args ...string) error {
			ran = append(ran, strings.Join(args, " "))
			return nil
		},
	}
	ro := ReadOnlyBdCli(inner)

	// Reads pass through, and the wrapper must not be inert in this direction —
	// a wrapper that blocked everything would pass the write half of this test.
	if _, err := ro.Exec("/work", "show", "gt-1", "--json"); err != nil {
		t.Fatalf("read-only bd refused a read: %v", err)
	}
	if len(ran) != 1 {
		t.Fatalf("read did not reach the inner bd: %v", ran)
	}

	writes := [][]string{
		{"create", "--title=x"},
		{"update", "gt-1", "--status=open"},
		{"close", "gt-1"},
		{"delete", "gt-1", "--force"},
		{"purge", "--pattern=x"},
	}
	for _, args := range writes {
		before := len(ran)
		if _, err := ro.Exec("/work", args...); err == nil {
			t.Errorf("bd %v was permitted under --dry-run", args)
		}
		if err := ro.Run("/work", args...); err == nil {
			t.Errorf("bd %v (Run) was permitted under --dry-run", args)
		}
		if len(ran) != before {
			t.Errorf("bd %v reached the inner bd despite being refused", args)
		}
	}

	// An unrecognised subcommand is a write until proven otherwise. A denylist
	// would let a bd subcommand added after this file was written straight
	// through to the production store.
	if _, err := ro.Exec("/work", "some-future-subcommand"); err == nil {
		t.Error("an unknown bd subcommand was permitted under --dry-run")
	}
}

// The acceptance case from gt-3516, at the level where the decision is made:
// a polecat that is demonstrably mid-turn is reported as busy and skipped, and
// the dry run reaches that verdict without a live polecat having to be at risk.
func TestDryRunReportsBusyVerdictWithoutRestarting(t *testing.T) {
	fired := swapScanEffectPrimitives(t)
	eff := scanEffects{dryRun: true}
	probe := &fakeSessionProbe{alive: true, busy: true}

	zombie := ZombieResult{
		PolecatName:    "furiosa",
		AgentState:     "working",
		Classification: ZombieStuckInDone,
		HookBead:       "bd-gq7",
		WasActive:      true,
		Action:         "restarted-stuck-session (done-intent age=30h20m25s)",
	}

	got, found := applyRestart(probe, "bd-furiosa", vetoBusySessions, zombie, func() error {
		return eff.restartSession("/work", "rig", "furiosa")
	}, "restart-stuck-session-failed")

	if !found {
		t.Fatal("a vetoed restart must still be reported — the stale metadata is real")
	}
	if got.RestartVerdict != "busy" {
		t.Errorf("RestartVerdict = %q, want %q", got.RestartVerdict, "busy")
	}
	if fired["restartSession"] != 0 {
		t.Errorf("restartSession fired %d times, want 0", fired["restartSession"])
	}
	if probe.busyHits != 1 {
		t.Errorf("IsBusy consulted %d times, want 1 — the dry run must reach the guard, not skip it", probe.busyHits)
	}
}

// The verdict has to be reported for the quiet case too. Otherwise "no verdict"
// and "would not restart" are the same output, and a dry run that silently
// stopped reaching decideRestart would read as a clean all-clear.
func TestRestartVerdictIsRecordedWhenRestartProceeds(t *testing.T) {
	fired := swapScanEffectPrimitives(t)
	eff := scanEffects{dryRun: true}
	probe := &fakeSessionProbe{alive: true, busy: false}

	got, found := applyRestart(probe, "bd-quiet", vetoBusySessions,
		ZombieResult{PolecatName: "quiet", Classification: ZombieStuckInDone, Action: "restarted-stuck-session (done-intent age=2h)"},
		func() error { return eff.restartSession("/work", "rig", "quiet") },
		"restart-stuck-session-failed")

	if !found {
		t.Fatal("a proceed verdict should still be reported")
	}
	if got.RestartVerdict != "proceed" {
		t.Errorf("RestartVerdict = %q, want %q", got.RestartVerdict, "proceed")
	}
	if fired["restartSession"] != 0 {
		t.Errorf("restartSession fired under --dry-run on a proceed verdict")
	}
	if got.Action != "restarted-stuck-session (done-intent age=2h)" {
		t.Errorf("Action = %q, want the action it WOULD have taken, unchanged", got.Action)
	}
}

// The live control for the two tests above: with the gate open, the same
// proceed verdict does restart. Without this, "restartSession fired 0 times"
// is satisfied by a closure that was never wired to anything.
func TestLiveRunRestartsOnProceedVerdict(t *testing.T) {
	fired := swapScanEffectPrimitives(t)
	eff := scanEffects{dryRun: false}
	probe := &fakeSessionProbe{alive: true, busy: false}

	got, _ := applyRestart(probe, "bd-quiet", vetoBusySessions,
		ZombieResult{PolecatName: "quiet", Classification: ZombieStuckInDone},
		func() error { return eff.restartSession("/work", "rig", "quiet") },
		"restart-stuck-session-failed")

	if fired["restartSession"] != 1 {
		t.Fatalf("restartSession fired %d times in a live run, want 1", fired["restartSession"])
	}
	if got.RestartVerdict != "proceed" {
		t.Errorf("RestartVerdict = %q, want %q", got.RestartVerdict, "proceed")
	}
}

func TestRestartActionStringNamesEachVerdict(t *testing.T) {
	for action, want := range map[restartAction]string{
		restartProceed:     "proceed",
		restartBusy:        "busy",
		restartSessionGone: "session-gone",
	} {
		if got := action.String(); got != want {
			t.Errorf("restartAction(%d).String() = %q, want %q", action, got, want)
		}
	}
}

// The mountain failure count is derived from reads, so a dry run must report it
// truthfully — the number and the auto-skip decision — while writing neither.
func TestDryRunReportsMountainFailureWithoutWriting(t *testing.T) {
	fired := swapScanEffectPrimitives(t)
	var wrote []string
	bd := &BdCli{
		Exec: func(_ string, args ...string) (string, error) {
			if args[0] != "show" && args[0] != "list" && args[0] != "dep" {
				wrote = append(wrote, strings.Join(args, " "))
			}
			return `[{"labels":["mountain:failures:1"]}]`, nil
		},
		Run: func(_ string, args ...string) error {
			wrote = append(wrote, strings.Join(args, " "))
			return errors.New("unexpected write")
		},
	}

	result := &ConvoyFailureResult{}
	if err := trackMountainFailure(bd, "/work", "gt-1", result, scanEffects{dryRun: true}); err != nil {
		t.Fatalf("trackMountainFailure under dry run: %v", err)
	}
	if result.FailureCount != 2 {
		t.Errorf("FailureCount = %d, want 2 — the count is a read and must still be reported", result.FailureCount)
	}
	if len(wrote) != 0 {
		t.Errorf("dry run issued bd writes: %v", wrote)
	}
	if len(fired) != 0 {
		t.Errorf("dry run fired effects: %v", fired)
	}
}
