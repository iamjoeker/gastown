package beads

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeMockBDForReopen installs a mock `bd` that logs its arguments and fails
// the named subcommands.
func writeMockBDForReopen(t *testing.T, failCmds ...string) (binDir, logPath string) {
	t.Helper()
	binDir = t.TempDir()
	logPath = filepath.Join(binDir, "bd.log")
	fail := strings.Join(failCmds, "|")
	if fail == "" {
		fail = "__none__"
	}
	script := fmt.Sprintf(`#!/bin/sh
LOG=%q
printf 'args=%%s\n' "$*" >> "$LOG"
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  %s)
    echo "mock failure for $cmd" >&2
    exit 1
    ;;
esac
exit 0
`, logPath, fail)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return binDir, logPath
}

func newReopenTestBeads(t *testing.T) *Beads {
	t.Helper()
	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	return NewWithBeadsDir(workDir, beadsDir)
}

func TestReopenUsesBdReopenWithReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}
	_, logPath := writeMockBDForReopen(t)

	if err := newReopenTestBeads(t).Reopen("gt-egq9", "MR mr-1 rejected: gates failed"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}

	log := readMockLog(t, logPath)
	if !strings.Contains(log, "args=reopen gt-egq9 --reason=MR mr-1 rejected: gates failed") {
		t.Fatalf("Reopen did not call bd reopen with the reason; log:\n%s", log)
	}
	if strings.Contains(log, "args=update") {
		t.Fatalf("Reopen fell back to update even though reopen succeeded; log:\n%s", log)
	}
}

// Some Dolt backends reject the reopen path. Falling back to a status update is
// what keeps a rejected MR's source issue re-slingable there.
func TestReopenFallsBackToStatusUpdate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}
	_, logPath := writeMockBDForReopen(t, "reopen")

	if err := newReopenTestBeads(t).Reopen("gt-egq9", "rejected"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}

	log := readMockLog(t, logPath)
	if !strings.Contains(log, "args=reopen gt-egq9") {
		t.Fatalf("Reopen never tried bd reopen; log:\n%s", log)
	}
	if !strings.Contains(log, "--status=open") {
		t.Fatalf("Reopen did not fall back to a status update; log:\n%s", log)
	}
}

// When both paths fail the caller must hear about it: a silent failure here is
// the same silent stranding the reject fix exists to prevent.
func TestReopenReportsBothFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}
	_, _ = writeMockBDForReopen(t, "reopen", "update")

	err := newReopenTestBeads(t).Reopen("gt-egq9", "rejected")
	if err == nil {
		t.Fatal("Reopen returned nil when both reopen and update failed")
	}
	if !strings.Contains(err.Error(), "gt-egq9") {
		t.Errorf("error %q does not name the issue", err)
	}
}

func readMockLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mock log: %v", err)
	}
	return string(data)
}
