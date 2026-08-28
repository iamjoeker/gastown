//go:build !windows

package nudge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// pollerProcessAlive reports whether pid names a poller that will actually
// drain the queue — which a zombie will not.
//
// signal(pid, 0) succeeds against a zombie (defunct) process: it has exited
// but still holds its PID until its parent calls wait(2). A liveness check
// based on that alone treats a defunct poller as running forever, so
// StartPoller's "already running" guard never fires a replacement — nothing
// is left to drain the queue, and delivery is silently blocked until someone
// notices by hand (hq-hidp; live evidence captured against hq-deacon's
// poller). Zombie status has to be checked past plain liveness.
func pollerProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	if proc.Signal(syscall.Signal(0)) != nil {
		return false
	}

	return !isZombieProcess(pid)
}

// isZombieProcess reports whether pid is a zombie: exited but not yet reaped
// by its parent. On Linux this reads /proc/<pid>/stat directly (no
// subprocess); everywhere else (e.g. macOS, which has no /proc) it falls
// back to `ps -o state=`, which reports the same "Z" state character on BSD-
// family ps implementations.
func isZombieProcess(pid int) bool {
	if state, ok := procStatState(pid); ok {
		return state == "Z"
	}
	return psState(pid) == "Z"
}

// procStatState reads the process state field (field 3) from
// /proc/<pid>/stat. The preceding comm field is parenthesized and may itself
// contain spaces or parentheses, so parsing resumes after the LAST ')' —
// consistent with doltserver.parseProcStatStartTicks, which relies on the
// same layout for the start-time field further along the same line.
func procStatState(pid int) (string, bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", false
	}
	commEnd := strings.LastIndex(string(data), ")")
	if commEnd < 0 {
		return "", false
	}
	fields := strings.Fields(string(data)[commEnd+1:])
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

// psState shells out to `ps` for the process state character. Used only when
// /proc is unavailable (non-Linux). Returns "" if ps fails or the process is
// already gone, which isZombieProcess treats as "not a zombie" — plain
// liveness (signal 0) is what decides that case.
func psState(pid int) string {
	out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return ""
	}
	return s[:1]
}
