package util

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// An OOM kill is a SIGKILL. The victim gets no chance to log, flush, or run a
// deferred cleanup, and it returns no exit code of its own — so the process
// itself can never report that it was killed. Only the spawner can notice, and
// only by inspecting the wait status.
//
// Go reports a signaled child as ExitCode() == -1, which is why a killed bd
// looks like a generic failure at every gt call site today. Shells report the
// same death as 128+signal, i.e. 137 for SIGKILL — the number operators
// recognize. ExitInfo carries the shell convention so gt's logs and error
// messages say the same thing an operator would see in a terminal.
//
// The distinction that matters for attribution: exec.CommandContext also kills
// with SIGKILL when its deadline expires, and util.SetProcessGroup installs a
// Cancel hook that SIGKILLs the whole process group. Those deaths are
// indistinguishable from an OOM kill in the wait status alone. ExitInfo
// therefore takes the context's error as an input and refuses to call a
// cancellation an OOM kill.

// SIGKILLExitCode is the exit code a shell reports for a SIGKILLed process
// (128 + SIGKILL). Operators know this number as "the OOM killer got it".
const SIGKILLExitCode = 137

// ExitInfo describes how a subprocess ended.
type ExitInfo struct {
	// Code is the exit status in shell convention: the process's own exit code
	// when it exited normally, or 128+signal when it died on a signal. It is 0
	// for success and -1 when the command never ran (Started is false).
	Code int

	// Signal is the signal that killed the process, or 0 if it exited normally.
	Signal syscall.Signal

	// Signaled reports whether the process died on a signal rather than
	// exiting on its own.
	Signaled bool

	// Started reports whether the process ran at all. False means the failure
	// was in exec itself (binary not found, permission denied), not in the
	// child.
	Started bool

	// Canceled reports whether the caller's context ended before the process
	// did. A SIGKILL with Canceled set is our own timeout, not the kernel's
	// OOM killer.
	Canceled bool
}

// ClassifyExitError classifies the error returned by exec.Cmd.Run/Wait/Output.
// ctxErr is the error from the context governing the command (nil when the
// command was not context-bound); it is what separates our own timeout kill
// from a kill imposed from outside.
func ClassifyExitError(err error, ctxErr error) ExitInfo {
	if err == nil {
		return ExitInfo{Started: true}
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		// exec never handed the process to the kernel — no wait status exists.
		return ExitInfo{Code: -1, Canceled: ctxErr != nil}
	}

	return ClassifyProcessState(exitErr.ProcessState, ctxErr)
}

// ClassifyProcessState classifies a finished process from its wait status.
func ClassifyProcessState(ps *os.ProcessState, ctxErr error) ExitInfo {
	info := ExitInfo{Started: true, Canceled: ctxErr != nil}
	if ps == nil {
		info.Code = -1
		info.Started = false
		return info
	}

	if sig, ok := terminationSignal(ps); ok {
		info.Signaled = true
		info.Signal = sig
		info.Code = 128 + int(sig)
		return info
	}

	info.Code = ps.ExitCode()
	return info
}

// OOMSuspected reports whether this death looks like an out-of-memory kill:
// SIGKILL that we did not ask for.
//
// It is deliberately a suspicion and not a verdict. SIGKILL from an operator,
// a supervisor, or another process is indistinguishable from the OOM killer's
// from the wait status alone — /proc keeps no per-victim record a spawner can
// read after the fact. Reporting the suspicion is still far better than the
// status quo, where the death is reported as exit code -1 with no signal
// mentioned at all.
func (e ExitInfo) OOMSuspected() bool {
	return e.Signaled && e.Signal == syscall.SIGKILL && !e.Canceled
}

// Describe returns a one-line, operator-readable account of how the process
// ended. It returns "" for a process that exited zero.
func (e ExitInfo) Describe() string {
	switch {
	case !e.Started:
		return "process never started"
	case e.OOMSuspected():
		return fmt.Sprintf("killed by SIGKILL (exit %d) — suspected out-of-memory kill; "+
			"the process could not log or clean up%s", e.Code, oomScoreAdjSuffix())
	case e.Signaled && e.Canceled:
		return fmt.Sprintf("killed by %s (exit %d) after the caller's deadline expired", e.Signal, e.Code)
	case e.Signaled:
		return fmt.Sprintf("killed by %s (exit %d)", e.Signal, e.Code)
	case e.Code == 0:
		return ""
	default:
		return fmt.Sprintf("exited %d", e.Code)
	}
}

// oomScoreAdjSuffix reports the spawner's own OOM bias when it is worse than
// neutral. Children inherit oom_score_adj, so the spawner's value is the
// victim's value, and it is the only half of the picture that survives the
// kill.
//
// The bias is inherited from whatever launched the town — a terminal started
// by a desktop session hands its systemd app-scope value down to every
// descendant. Raising oom_score_adj is unprivileged; LOWERING it requires
// CAP_SYS_RESOURCE, so a process that inherits a sacrificial value cannot undo
// it for itself. That is why the message names the ancestry rather than
// offering a fix the caller cannot apply.
func oomScoreAdjSuffix() string {
	adj, ok := OOMScoreAdj()
	if !ok || adj <= 0 {
		return ""
	}
	return fmt.Sprintf(". This process runs at oom_score_adj %d (inherited from the "+
		"session that launched it, so its children run there too); lowering it "+
		"requires CAP_SYS_RESOURCE and cannot be done from here", adj)
}
