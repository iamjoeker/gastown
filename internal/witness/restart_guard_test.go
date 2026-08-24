package witness

import (
	"errors"
	"strings"
	"testing"
)

// fakeSessionProbe stands in for *tmux.Tmux in the restart decision, and counts
// its calls so a test can tell "did not veto" from "never asked".
type fakeSessionProbe struct {
	alive     bool
	aliveErr  error
	busy      bool
	aliveHits int
	busyHits  int
}

func (f *fakeSessionProbe) HasSession(string) (bool, error) {
	f.aliveHits++
	return f.alive, f.aliveErr
}

func (f *fakeSessionProbe) IsBusy(string) bool {
	f.busyHits++
	return f.busy
}

func TestDecideRestartVetoesBusySession(t *testing.T) {
	tests := []struct {
		name         string
		probe        fakeSessionProbe
		vetoIfBusy   bool
		want         restartAction
		wantBusyHits int
	}{
		{
			// gt-nof6: the furiosa case. A live, mid-turn polecat carrying a
			// done-intent age of 30h on a twenty-minute-old session.
			name:         "busy session with veto is not restarted",
			probe:        fakeSessionProbe{alive: true, busy: true},
			vetoIfBusy:   vetoBusySessions,
			want:         restartBusy,
			wantBusyHits: 1,
		},
		{
			name:         "idle session with veto is restarted",
			probe:        fakeSessionProbe{alive: true, busy: false},
			vetoIfBusy:   vetoBusySessions,
			want:         restartProceed,
			wantBusyHits: 1,
		},
		{
			// The agent-dead path: its pane can hold the busy footer of the frame
			// the agent died rendering, so it must not consult IsBusy at all.
			name:         "busy pane without veto is still restarted",
			probe:        fakeSessionProbe{alive: true, busy: true},
			vetoIfBusy:   restartEvenIfBusy,
			want:         restartProceed,
			wantBusyHits: 0,
		},
		{
			name:         "dead session is dropped before the busy check",
			probe:        fakeSessionProbe{alive: false, busy: true},
			vetoIfBusy:   vetoBusySessions,
			want:         restartSessionGone,
			wantBusyHits: 0,
		},
		{
			// HasSession erroring is indistinguishable from a gone session here,
			// which is the conservative reading: do not act.
			name:         "liveness error is treated as gone",
			probe:        fakeSessionProbe{alive: false, aliveErr: errors.New("no server"), busy: false},
			vetoIfBusy:   vetoBusySessions,
			want:         restartSessionGone,
			wantBusyHits: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			probe := tc.probe
			got := decideRestart(&probe, "gt-rig-polecat", tc.vetoIfBusy)
			if got != tc.want {
				t.Errorf("decideRestart = %v, want %v", got, tc.want)
			}
			if probe.busyHits != tc.wantBusyHits {
				t.Errorf("IsBusy called %d times, want %d", probe.busyHits, tc.wantBusyHits)
			}
			if probe.aliveHits != 1 {
				t.Errorf("HasSession called %d times, want 1", probe.aliveHits)
			}
		})
	}
}

func TestApplyRestartSkipsRestartOnBusySession(t *testing.T) {
	probe := &fakeSessionProbe{alive: true, busy: true}
	restarted := false

	zombie := ZombieResult{
		PolecatName:    "furiosa",
		AgentState:     "working",
		Classification: ZombieStuckInDone,
		HookBead:       "bd-gq7",
		WasActive:      true,
		Action:         "restarted-stuck-session (done-intent age=30h20m25s)",
	}

	got, found := applyRestart(probe, "bd-furiosa", vetoBusySessions, zombie, func() error {
		restarted = true
		return nil
	}, "restart-stuck-session-failed")

	if restarted {
		t.Fatal("restarted a session that was demonstrably mid-turn")
	}
	if !found {
		t.Fatal("vetoed restart should still report the stale verdict, not swallow it")
	}
	if !strings.Contains(got.Action, "restart-deferred-session-busy") {
		t.Errorf("Action = %q, want it to report the deferral", got.Action)
	}
	if strings.Contains(got.Action, "restarted-stuck-session") {
		t.Errorf("Action = %q, still claims a restart that did not happen", got.Action)
	}
	if got.Error != nil {
		t.Errorf("Error = %v, want nil for a clean deferral", got.Error)
	}
}

func TestApplyRestartActsOnQuietSession(t *testing.T) {
	probe := &fakeSessionProbe{alive: true, busy: false}
	restarted := false

	zombie := ZombieResult{Classification: ZombieStuckInDone, Action: "restarted-stuck-session (done-intent age=2h)"}
	got, found := applyRestart(probe, "bd-furiosa", vetoBusySessions, zombie, func() error {
		restarted = true
		return nil
	}, "restart-stuck-session-failed")

	if !restarted {
		t.Fatal("a genuinely stuck session must still be restarted")
	}
	if !found {
		t.Fatal("restarted zombie should be reported")
	}
	if got.Action != "restarted-stuck-session (done-intent age=2h)" {
		t.Errorf("Action = %q, want the original restart action preserved", got.Action)
	}
}

func TestApplyRestartRecordsRestartFailure(t *testing.T) {
	probe := &fakeSessionProbe{alive: true, busy: false}
	boom := errors.New("session restart failed")

	got, found := applyRestart(probe, "bd-furiosa", vetoBusySessions,
		ZombieResult{Classification: ZombieStuckInDone}, func() error { return boom },
		"restart-stuck-session-failed")

	if !found {
		t.Fatal("failed restart should still be reported")
	}
	if !errors.Is(got.Error, boom) {
		t.Errorf("Error = %v, want the restart error", got.Error)
	}
	if !strings.HasPrefix(got.Action, "restart-stuck-session-failed") {
		t.Errorf("Action = %q, want the failure prefix", got.Action)
	}
}

func TestApplyRestartDropsVerdictWhenSessionExited(t *testing.T) {
	probe := &fakeSessionProbe{alive: false}
	restarted := false

	got, found := applyRestart(probe, "bd-furiosa", vetoBusySessions,
		ZombieResult{PolecatName: "furiosa", Classification: ZombieStuckInDone},
		func() error { restarted = true; return nil }, "restart-stuck-session-failed")

	if restarted {
		t.Fatal("restarted a session that no longer exists")
	}
	if found {
		t.Fatalf("session that exited on its own is not a zombie, got %+v", got)
	}
}
