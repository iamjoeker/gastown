//go:build !windows

package util

import (
	"strings"
	"testing"
)

// A SIGKILLed child writes nothing to stderr, so this is the one case where
// the wait status is the only information there is. Before this it surfaced as
// Go's "signal: killed" — no exit code, no hint at the cause.
func TestExecWithOutput_KilledSubprocessNamesTheSignal(t *testing.T) {
	_, err := ExecWithOutput("", "sh", "-c", "kill -9 $$")
	if err == nil {
		t.Fatal("expected an error from a killed subprocess")
	}
	msg := err.Error()
	if !strings.Contains(msg, "SIGKILL") {
		t.Errorf("error = %q, want it to name SIGKILL", msg)
	}
	if !strings.Contains(msg, "137") {
		t.Errorf("error = %q, want the shell exit code 137", msg)
	}
	if !strings.Contains(msg, "out-of-memory") {
		t.Errorf("error = %q, want the OOM suspicion", msg)
	}
}

func TestExecRun_KilledSubprocessNamesTheSignal(t *testing.T) {
	err := ExecRun("", "sh", "-c", "kill -9 $$")
	if err == nil {
		t.Fatal("expected an error from a killed subprocess")
	}
	if !strings.Contains(err.Error(), "SIGKILL") {
		t.Errorf("error = %q, want it to name SIGKILL", err.Error())
	}
}

// The control: when the subprocess did manage to explain itself, its own
// message must still win. A diagnosis is a fallback, not a replacement.
func TestExecWithOutput_StderrStillWins(t *testing.T) {
	_, err := ExecWithOutput("", "sh", "-c", "echo 'the real reason' >&2; exit 2")
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != "the real reason" {
		t.Errorf("error = %q, want the subprocess's own stderr", err.Error())
	}
}

func TestExecWithOutput_PlainExitUnchanged(t *testing.T) {
	_, err := ExecWithOutput("", "sh", "-c", "exit 7")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "out-of-memory") {
		t.Errorf("error = %q, must not diagnose a normal non-zero exit as a kill", err.Error())
	}
}
