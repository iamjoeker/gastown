//go:build !windows

package cmd

import (
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// gt-dr6t violated: the shutdown phase named "Verifying shutdown" sent SIGKILL,
// discarded the result, and returned nothing — so `gt down` printed "shutdown
// complete" and exited 0 with live agent processes still running.
//
// These tests pin the property the fix restores: verifyNoOrphans decides what to
// report by re-reading process state after the kill, never by the fact that the
// kill was attempted. The discriminating case is TestVerifyNoOrphans_
// ReportsProcessThatSurvivedSIGKILL, where the kill call itself succeeds and only
// the post-kill liveness check can tell the truth.

// fakeSignaller records the signals sent and answers liveness checks from a set
// of PIDs it considers still running.
type fakeSignaller struct {
	alive map[int]bool
	// killErr, if set for a PID, is returned from the SIGKILL call for it.
	killErr map[int]error
	sent    []struct {
		pid int
		sig syscall.Signal
	}
}

func (f *fakeSignaller) kill(pid int, sig syscall.Signal) error {
	f.sent = append(f.sent, struct {
		pid int
		sig syscall.Signal
	}{pid, sig})
	if sig == syscall.SIGKILL {
		if err, ok := f.killErr[pid]; ok {
			return err
		}
		return nil
	}
	// Signal 0: reachability probe.
	if f.alive[pid] {
		return nil
	}
	return syscall.ESRCH
}

func (f *fakeSignaller) sigkillCount() int {
	n := 0
	for _, s := range f.sent {
		if s.sig == syscall.SIGKILL {
			n++
		}
	}
	return n
}

// installOrphanFakes swaps the package seams for the duration of one test.
func installOrphanFakes(t *testing.T, orphans []util.OrphanedProcess, zombies []util.ZombieProcess, sig *fakeSignaller) {
	t.Helper()
	origOrphans, origZombies, origSignal, origSleep, origSettle :=
		findOrphanedProcs, findZombieProcs, sendSignal, sleepFor, orphanKillSettle
	t.Cleanup(func() {
		findOrphanedProcs, findZombieProcs, sendSignal, sleepFor, orphanKillSettle =
			origOrphans, origZombies, origSignal, origSleep, origSettle
	})

	findOrphanedProcs = func() ([]util.OrphanedProcess, error) { return orphans, nil }
	findZombieProcs = func() ([]util.ZombieProcess, error) { return zombies, nil }
	sendSignal = sig.kill
	sleepFor = func(time.Duration) {} // do not spend the settle window in tests
}

func TestVerifyNoOrphans_ReportsProcessThatSurvivedSIGKILL(t *testing.T) {
	// SIGKILL is delivered without error and the process is STILL THERE. This is
	// exactly the shape gt-dr6t is about: reading the call's own result reports
	// success, and only measuring the state afterwards catches it.
	sig := &fakeSignaller{alive: map[int]bool{4242: true}}
	installOrphanFakes(t,
		[]util.OrphanedProcess{{PID: 4242, Cmd: "claude", Age: 900}},
		nil, sig)

	err := verifyNoOrphans()
	if err == nil {
		t.Fatal("verifyNoOrphans returned nil for a process that survived SIGKILL. " +
			"Shutdown would report success over a live agent — the gt-dr6t failure mode.")
	}
	if !strings.Contains(err.Error(), "4242") {
		t.Errorf("error does not name the surviving PID, so the operator cannot act on it: %v", err)
	}
	if sig.sigkillCount() != 1 {
		t.Errorf("sent %d SIGKILLs, want 1", sig.sigkillCount())
	}
}

func TestVerifyNoOrphans_SucceedsWhenProcessActuallyDies(t *testing.T) {
	// CONTROL for the test above: same code path, same successful SIGKILL, only
	// the post-kill state differs. If this failed too, the test above would prove
	// nothing about verification.
	sig := &fakeSignaller{alive: map[int]bool{}}
	installOrphanFakes(t,
		[]util.OrphanedProcess{{PID: 4242, Cmd: "claude", Age: 900}},
		nil, sig)

	if err := verifyNoOrphans(); err != nil {
		t.Fatalf("verifyNoOrphans returned an error for a process that died: %v", err)
	}
}

func TestVerifyNoOrphans_ReportsSurvivingZombie(t *testing.T) {
	// Zombies (TTY, no tmux session) go through the same confirmation as orphans.
	sig := &fakeSignaller{alive: map[int]bool{77: true}}
	installOrphanFakes(t, nil,
		[]util.ZombieProcess{{PID: 77, Cmd: "claude", Age: 300, TTY: "s001"}},
		sig)

	err := verifyNoOrphans()
	if err == nil {
		t.Fatal("verifyNoOrphans returned nil for a zombie that survived SIGKILL")
	}
	if !strings.Contains(err.Error(), "77") {
		t.Errorf("error does not name the surviving PID: %v", err)
	}
}

func TestVerifyNoOrphans_UndeliverableSignalIsASurvivor(t *testing.T) {
	// EPERM means the process exists and we cannot touch it. Reporting a clean
	// shutdown there is the same lie as reporting one after a failed kill.
	sig := &fakeSignaller{
		alive:   map[int]bool{99: true},
		killErr: map[int]error{99: syscall.EPERM},
	}
	installOrphanFakes(t,
		[]util.OrphanedProcess{{PID: 99, Cmd: "claude", Age: 120}},
		nil, sig)

	if err := verifyNoOrphans(); err == nil {
		t.Fatal("verifyNoOrphans returned nil for a process it could not signal")
	}
}

func TestVerifyNoOrphans_AlreadyGoneIsNotAFailure(t *testing.T) {
	// The process died between the scan and the kill. ESRCH is the outcome we
	// wanted, not an error, and it must not trigger a liveness probe.
	sig := &fakeSignaller{
		alive:   map[int]bool{},
		killErr: map[int]error{5: syscall.ESRCH},
	}
	installOrphanFakes(t,
		[]util.OrphanedProcess{{PID: 5, Cmd: "claude", Age: 120}},
		nil, sig)

	if err := verifyNoOrphans(); err != nil {
		t.Fatalf("verifyNoOrphans returned an error for an already-dead process: %v", err)
	}
	for _, s := range sig.sent {
		if s.sig == 0 {
			t.Error("probed liveness of a PID that already returned ESRCH")
		}
	}
}

func TestVerifyNoOrphans_CleanShutdownSendsNoSignals(t *testing.T) {
	sig := &fakeSignaller{alive: map[int]bool{}}
	installOrphanFakes(t, nil, nil, sig)

	if err := verifyNoOrphans(); err != nil {
		t.Fatalf("verifyNoOrphans returned an error with no survivors: %v", err)
	}
	if len(sig.sent) != 0 {
		t.Errorf("sent %d signals with no survivors, want 0", len(sig.sent))
	}
}

func TestVerifyNoOrphans_ScanFailureIsSurfaced(t *testing.T) {
	// "I could not look" must not read as "there was nothing there". The old code
	// printed a warning and returned, and shutdown reported success regardless.
	origOrphans := findOrphanedProcs
	t.Cleanup(func() { findOrphanedProcs = origOrphans })

	scanErr := errors.New("ps: command not found")
	findOrphanedProcs = func() ([]util.OrphanedProcess, error) { return nil, scanErr }

	err := verifyNoOrphans()
	if err == nil {
		t.Fatal("verifyNoOrphans returned nil when it could not scan for processes")
	}
	if !errors.Is(err, scanErr) {
		t.Errorf("scan failure not wrapped in the returned error: %v", err)
	}
}
