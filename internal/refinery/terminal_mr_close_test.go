package refinery

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestCloseTerminalMRForceClosesPinnedMR pins the collision from gt-obth in
// place: MR beads are pinned so `bd purge --force` skips them (gt-6dp), and
// `bd close` refuses a pinned bead without --force. The merge queue owns the
// MR record, so its terminal close must carry --force or post-merge cleanup
// fails on every pinned MR.
func TestCloseTerminalMRForceClosesPinnedMR(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a Unix shell script stub for bd")
	}

	const showJSON = `[{"id":"gt-mr-pinned","title":"Merge: gt-src","status":"open","priority":2,"issue_type":"task","labels":["gt:merge-request"],"description":"branch: polecat/test/pin\nsource_issue: gt-src\ntarget: main\n"}]`

	stubDir := t.TempDir()
	logPath := filepath.Join(stubDir, "close-args.log")
	// The stub refuses to close a pinned bead unless --force is present,
	// mirroring bd's own guard, so the test fails loudly if --force is dropped.
	stubScript := `#!/bin/sh
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  show)
    cat <<'JSONEOF'
` + showJSON + `
JSONEOF
    ;;
  close)
    printf '%s\n' "$*" >> "` + logPath + `"
    case "$*" in
      *--force*) ;;
      *)
        echo "cannot modify pinned issue gt-mr-pinned (use --force to override)" >&2
        exit 1
        ;;
    esac
    ;;
esac
exit 0
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	b := beads.New(workDir)
	result, err := closeTerminalMR(b, "gt-mr-pinned", terminalMRCloseOptions{
		Reason: string(CloseReasonMerged),
	})
	if err != nil {
		t.Fatalf("closeTerminalMR on pinned MR: %v", err)
	}
	if !result.Closed {
		t.Fatalf("closeTerminalMR did not close MR, result = %+v", result)
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading stub close args: %v", err)
	}
	if !strings.Contains(string(logged), "--force") {
		t.Errorf("bd close args missing --force, got: %q", string(logged))
	}
}
