//go:build !windows

package util

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A SIGKILLed child is the whole point of this file: Go reports it as exit
// code -1, which is why every gt call site currently renders an OOM kill as a
// generic failure.
func TestClassifyExitError_SIGKILLReportsShellCode(t *testing.T) {
	cmd := exec.Command("sh", "-c", "kill -9 $$")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected the child to die on SIGKILL")
	}

	// Establish what the status quo actually is, so the fix is measured
	// against it rather than against an assumption.
	var exitErr *exec.ExitError
	if !asExitError(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %T", err)
	}
	if got := exitErr.ExitCode(); got != -1 {
		t.Fatalf("premise check: Go reports a signaled child as -1, got %d", got)
	}

	info := ClassifyExitError(err, nil)
	if !info.Signaled {
		t.Error("Signaled = false, want true")
	}
	if info.Signal != syscall.SIGKILL {
		t.Errorf("Signal = %v, want SIGKILL", info.Signal)
	}
	if info.Code != SIGKILLExitCode {
		t.Errorf("Code = %d, want %d", info.Code, SIGKILLExitCode)
	}
	if !info.OOMSuspected() {
		t.Error("OOMSuspected = false, want true for an unrequested SIGKILL")
	}
	if desc := info.Describe(); !strings.Contains(desc, "out-of-memory") {
		t.Errorf("Describe() = %q, want it to name the OOM suspicion", desc)
	}
}

// The control that keeps the OOM suspicion honest: exec.CommandContext kills
// with SIGKILL on deadline, so a timeout is byte-identical to an OOM kill in
// the wait status. Without the context check every timeout would be reported
// as an OOM kill.
func TestClassifyExitError_ContextKillIsNotOOM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sleep", "5")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected the deadline to kill the child")
	}

	info := ClassifyExitError(err, ctx.Err())
	if !info.Signaled || info.Signal != syscall.SIGKILL {
		t.Fatalf("premise check: a context kill must be SIGKILL, got signaled=%v sig=%v",
			info.Signaled, info.Signal)
	}
	if info.Code != SIGKILLExitCode {
		t.Errorf("Code = %d, want %d", info.Code, SIGKILLExitCode)
	}
	if info.OOMSuspected() {
		t.Error("OOMSuspected = true for our own timeout kill; the context error must veto it")
	}
	if desc := info.Describe(); !strings.Contains(desc, "deadline") {
		t.Errorf("Describe() = %q, want it to name the deadline", desc)
	}
}

func TestClassifyExitError_NormalExitCodePreserved(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 3").Run()
	info := ClassifyExitError(err, nil)

	if info.Signaled {
		t.Error("Signaled = true for a normal exit")
	}
	if info.Code != 3 {
		t.Errorf("Code = %d, want 3", info.Code)
	}
	if info.OOMSuspected() {
		t.Error("OOMSuspected = true for a normal non-zero exit")
	}
	if !info.Started {
		t.Error("Started = false for a process that ran")
	}
}

func TestClassifyExitError_Success(t *testing.T) {
	info := ClassifyExitError(nil, nil)
	if info.Code != 0 || info.Signaled || !info.Started {
		t.Errorf("ClassifyExitError(nil, nil) = %+v, want a clean zero exit", info)
	}
	if desc := info.Describe(); desc != "" {
		t.Errorf("Describe() = %q, want empty for success", desc)
	}
}

// A failure to exec is not a failure of the child, and must not be reported
// with a shell exit code that implies one ran.
func TestClassifyExitError_NeverStarted(t *testing.T) {
	err := exec.Command(filepath.Join(t.TempDir(), "definitely-not-a-binary")).Run()
	if err == nil {
		t.Fatal("expected exec to fail")
	}

	info := ClassifyExitError(err, nil)
	if info.Started {
		t.Error("Started = true for a command that never ran")
	}
	if info.Signaled {
		t.Error("Signaled = true for a command that never ran")
	}
	if info.Code != -1 {
		t.Errorf("Code = %d, want -1", info.Code)
	}
	if desc := info.Describe(); !strings.Contains(desc, "never started") {
		t.Errorf("Describe() = %q, want it to say the process never started", desc)
	}
}

// SIGTERM is a signaled death that is not an OOM kill; the classifier must not
// widen its suspicion to every signal.
func TestClassifyExitError_SIGTERMIsNotOOM(t *testing.T) {
	err := exec.Command("sh", "-c", "kill -TERM $$").Run()
	info := ClassifyExitError(err, nil)

	if !info.Signaled || info.Signal != syscall.SIGTERM {
		t.Fatalf("expected a SIGTERM death, got %+v", info)
	}
	if info.Code != 128+int(syscall.SIGTERM) {
		t.Errorf("Code = %d, want %d", info.Code, 128+int(syscall.SIGTERM))
	}
	if info.OOMSuspected() {
		t.Error("OOMSuspected = true for SIGTERM")
	}
}

func TestOOMScoreAdj_ReadsTheValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oom_score_adj")
	if err := os.WriteFile(path, []byte("200\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	orig := oomScoreAdjPath
	oomScoreAdjPath = path
	t.Cleanup(func() { oomScoreAdjPath = orig })

	adj, ok := OOMScoreAdj()
	if !ok {
		t.Fatal("OOMScoreAdj reported not-ok for a readable file")
	}
	if adj != 200 {
		t.Errorf("adj = %d, want 200", adj)
	}
}

// An unreadable oom_score_adj (every non-Linux unix) must not turn into a
// bogus 0 that reads as "this process is not sacrificial".
func TestOOMScoreAdj_MissingFileIsNotZero(t *testing.T) {
	orig := oomScoreAdjPath
	oomScoreAdjPath = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { oomScoreAdjPath = orig })

	if _, ok := OOMScoreAdj(); ok {
		t.Error("OOMScoreAdj reported ok for a missing file")
	}
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
