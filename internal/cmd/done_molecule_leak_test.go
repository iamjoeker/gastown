package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// moleculeLeakStub builds a town with a stub bd that serves one agent bead, one
// hooked bead (whose status the caller chooses) carrying attached_molecule, and
// a molecule with two open steps. It returns the path of the log every close
// call is appended to, and the rig directory to pass as cwd.
func moleculeLeakStub(t *testing.T, hookedBeadID, hookedStatus string) (closesLog, rigDir string) {
	t.Helper()

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	rigDir = filepath.Join(townRoot, "gastown")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gastown"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog = filepath.Join(townRoot, "closes.log")

	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"%[1]s","agent_state":"working"}]'
        ;;
      %[1]s)
        echo '[{"id":"%[1]s","title":"Hooked work","status":"%[2]s","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
    esac
    ;;
  list)
    if echo "$*" | grep -q "parent=gt-wisp-xyz"; then
      echo '[{"id":"gt-step-1","title":"Step 1","status":"open"},{"id":"gt-step-2","title":"Step 2","status":"open"}]'
    else
      echo '[]'
    fi
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%[3]s"
    done
    ;;
esac
exit 0
`, hookedBeadID, hookedStatus, closesLog)

	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nux")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(rigDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return closesLog, rigDir
}

// closedBeads reads the stub's close log. A missing file means nothing closed.
func closedBeads(t *testing.T, closesLog string) []string {
	t.Helper()
	data, err := os.ReadFile(closesLog)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read closes log: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func assertClosed(t *testing.T, closed []string, id string) {
	t.Helper()
	for _, line := range closed {
		if strings.Contains(line, id) {
			return
		}
	}
	t.Errorf("%s was NOT closed\nClose calls: %v", id, closed)
}

// TestDoneClosesMoleculeWhenHookedBeadAlreadyClosed is the regression test for
// gt-pkw. A polecat that closes its own bead before running gt done (the
// documented "nothing to implement" path) used to skip molecule cleanup
// entirely, leaking the molecule root plus one wisp per formula step. Nothing
// else collects those: the witness orphan sweep only inspects hooked and
// in_progress beads, so a closed bead's molecule is invisible to it.
func TestDoneClosesMoleculeWhenHookedBeadAlreadyClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	closesLog, rigDir := moleculeLeakStub(t, "gt-base-123", "closed")

	updateAgentStateOnDone(rigDir, filepath.Dir(rigDir), ExitCompleted, "gt-base-123")

	closed := closedBeads(t, closesLog)
	assertClosed(t, closed, "gt-step-1")
	assertClosed(t, closed, "gt-step-2")
	assertClosed(t, closed, "gt-wisp-xyz")
}

// TestDoneClosesMoleculeForClosedWorkflowStepBead covers the shape that actually
// produced the leak in the gastown rig: dog/reaper workflow step beads
// (*-wfs-*), which are closed by the workflow engine before their agent runs
// gt done, and which exit DEFERRED because they are report-only.
func TestDoneClosesMoleculeForClosedWorkflowStepBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	closesLog, rigDir := moleculeLeakStub(t, "gt-wfs-abc12", "closed")

	updateAgentStateOnDone(rigDir, filepath.Dir(rigDir), ExitDeferred, "gt-wfs-abc12")

	closed := closedBeads(t, closesLog)
	assertClosed(t, closed, "gt-step-1")
	assertClosed(t, closed, "gt-step-2")
	assertClosed(t, closed, "gt-wisp-xyz")
}

// TestDoneClosesMoleculeForRigIdentityBead verifies the rig identity guard still
// refuses to close the permanent bead, but no longer leaks the dispatch's
// molecule along with it — the rig bead never closes, so no later sweep would
// ever collect it.
func TestDoneClosesMoleculeForRigIdentityBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	rigDir := filepath.Join(townRoot, "gastown")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gastown"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")

	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    case "$1" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-rig-gastown","agent_state":"working"}]'
        ;;
      gt-rig-gastown)
        echo '[{"id":"gt-rig-gastown","title":"gastown rig","status":"hooked","labels":["gt:rig"],"description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
    esac
    ;;
  list)
    if echo "$*" | grep -q "parent=gt-wisp-xyz"; then
      echo '[{"id":"gt-step-1","title":"Step 1","status":"open"}]'
    else
      echo '[]'
    fi
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
esac
exit 0
`, closesLog)

	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nux")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(rigDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	updateAgentStateOnDone(rigDir, townRoot, ExitCompleted, "gt-rig-gastown")

	closed := closedBeads(t, closesLog)
	assertClosed(t, closed, "gt-step-1")
	assertClosed(t, closed, "gt-wisp-xyz")
	for _, line := range closed {
		if strings.Contains(line, "gt-rig-gastown") {
			t.Errorf("rig identity bead gt-rig-gastown must never be closed\nClose calls: %v", closed)
		}
	}
}
