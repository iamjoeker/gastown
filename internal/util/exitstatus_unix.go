//go:build !windows

package util

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// terminationSignal returns the signal that killed the process, if any.
func terminationSignal(ps *os.ProcessState) (syscall.Signal, bool) {
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0, false
	}
	return ws.Signal(), true
}

// oomScoreAdjPath is a variable so tests can point it at a fixture.
var oomScoreAdjPath = "/proc/self/oom_score_adj"

// OOMScoreAdj returns this process's OOM-killer bias and whether it could be
// read. Only Linux exposes it; elsewhere the read fails and ok is false.
func OOMScoreAdj() (int, bool) {
	data, err := os.ReadFile(oomScoreAdjPath)
	if err != nil {
		return 0, false
	}
	adj, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return adj, true
}
