//go:build windows

package util

import (
	"os"
	"syscall"
)

// terminationSignal always reports false on Windows: processes there are
// terminated with an exit code, not a signal, so there is no signaled death to
// distinguish.
func terminationSignal(*os.ProcessState) (syscall.Signal, bool) {
	return 0, false
}

// OOMScoreAdj is Linux-only; Windows has no equivalent per-process OOM bias.
func OOMScoreAdj() (int, bool) {
	return 0, false
}
