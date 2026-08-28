//go:build !windows

package util

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// procState reads the single-letter state from /proc/<pid>/stat. "Z" is a
// zombie: exited, but not yet reaped by its parent.
//
// The field is read from the last ")" rather than by splitting on spaces —
// comm sits in parentheses and may itself contain spaces.
func procState(t *testing.T, pid int) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", false
	}
	line := string(data)
	close := strings.LastIndex(line, ")")
	if close < 0 || close+2 >= len(line) {
		return "", false
	}
	fields := strings.Fields(line[close+1:])
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

func waitForState(t *testing.T, pid int, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, ok := procState(t, pid)
		if !ok {
			// Gone from /proc entirely — reaped.
			return want == ""
		}
		if state == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// The defect this fixes, demonstrated: a started-but-unwaited child stays in
// the process table as a zombie for as long as its parent lives.
func TestUnwaitedChildBecomesZombie(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no procfs: cannot observe process state")
	}

	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid

	if !waitForState(t, pid, "Z", 2*time.Second) {
		state, _ := procState(t, pid)
		t.Fatalf("premise check failed: unwaited child in state %q, expected Z", state)
	}

	// Release does NOT reap — this is the part that has been mistaken for a
	// fix at more than one call site.
	_ = cmd.Process.Release()
	if state, ok := procState(t, pid); !ok || state != "Z" {
		t.Errorf("after Release, state = %q ok=%v; Release must not be treated as reaping", state, ok)
	}

	// Clean up the fixture's own zombie so the test does not leak one.
	var ws syscall.WaitStatus
	_, _ = syscall.Wait4(pid, &ws, 0, nil)
}

func TestReapDetached_ReapsTheChild(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no procfs: cannot observe process state")
	}

	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid

	ReapDetached(cmd)

	if !waitForState(t, pid, "", 2*time.Second) {
		state, _ := procState(t, pid)
		t.Fatalf("child left in state %q; ReapDetached did not reap it", state)
	}
}

// A detached child that is OOM-killed leaves no other trace: it cannot log,
// and its parent never inspected the wait status. The reaper is the only place
// the kill can be noticed.
func TestReapDetachedFunc_ReportsSignaledDeath(t *testing.T) {
	cmd := exec.Command("sh", "-c", "kill -9 $$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	got := make(chan ExitInfo, 1)
	ReapDetachedFunc(cmd, func(info ExitInfo) { got <- info })

	select {
	case info := <-got:
		if !info.OOMSuspected() {
			t.Errorf("OOMSuspected = false for an unrequested SIGKILL; info = %+v", info)
		}
		if info.Code != SIGKILLExitCode {
			t.Errorf("Code = %d, want %d", info.Code, SIGKILLExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onExit never fired")
	}
}

func TestReapDetachedFunc_ReportsCleanExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	got := make(chan ExitInfo, 1)
	ReapDetachedFunc(cmd, func(info ExitInfo) { got <- info })

	select {
	case info := <-got:
		if info.Code != 0 || info.Signaled {
			t.Errorf("info = %+v, want a clean zero exit", info)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onExit never fired")
	}
}

// Guarding on cmd.Process keeps a caller that checked Start's error in the
// wrong order from panicking inside the reaper.
func TestReapDetached_ToleratesUnstartedCommand(t *testing.T) {
	ReapDetached(nil)
	ReapDetached(exec.Command("sh", "-c", "exit 0")) // never started
}

func TestReapGroup_WaitsForAll(t *testing.T) {
	var g ReapGroup
	const n = 5
	done := make(chan struct{}, n)

	for i := 0; i < n; i++ {
		cmd := exec.Command("sh", "-c", "exit 0")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		g.Add(cmd, func(ExitInfo) { done <- struct{}{} })
	}

	g.Wait()
	if len(done) != n {
		t.Errorf("onExit fired %d times, want %d — Wait returned early", len(done), n)
	}
}
