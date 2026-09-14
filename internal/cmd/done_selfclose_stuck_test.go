package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// selfCloseStuckStub builds a town whose agent bead is hooked to hookedBeadID,
// already at the given status when gt done runs. It captures every
// `bd update <agent-bead> --body-file=-` call's stdin (the new agent
// description) into a log file, so the test can assert what agent_state was
// written without needing a real Dolt-backed store.
func selfCloseStuckStub(t *testing.T, hookedBeadID, hookedStatus string) (agentUpdatesLog, rigDir string) {
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
	agentUpdatesLog = filepath.Join(townRoot, "agent-updates.log")

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
        echo '[{"id":"%[1]s","title":"Hooked work","status":"%[2]s"}]'
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  update)
    id="$1"
    if [ "$id" = "gt-gastown-polecat-nux" ]; then
      cat >> "%[3]s"
      echo "---" >> "%[3]s"
    else
      cat >/dev/null
    fi
    ;;
  close)
    :
    ;;
esac
exit 0
`, hookedBeadID, hookedStatus, agentUpdatesLog)

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
	return agentUpdatesLog, rigDir
}

func lastAgentUpdate(t *testing.T, agentUpdatesLog string) string {
	t.Helper()
	data, err := os.ReadFile(agentUpdatesLog)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read agent updates log: %v", err)
	}
	parts := strings.Split(strings.TrimRight(string(data), "\n"), "---\n")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

// TestDoneDoesNotStrandSelfClosedNoChangesPolecat is the regression test for
// gt-6lok0. gt-j9uv/gt-gubw stopped `gt done` from fatally refusing a polecat
// that self-closed its hooked bead as "no-changes" and then exited with
// --status DEFERRED (the formula-prescribed flow for report-only/no-changes
// outcomes) — but updateAgentStateOnDone still unconditionally wrote
// agent_state=stuck for any non-COMPLETED exit, stranding the polecat at
// NEEDS_STATE_CLEAR even though it finished its assignment cleanly.
func TestDoneDoesNotStrandSelfClosedNoChangesPolecat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	agentUpdatesLog, rigDir := selfCloseStuckStub(t, "gt-base-123", "closed")

	if err := updateAgentStateOnDone(rigDir, filepath.Dir(rigDir), ExitDeferred, "gt-base-123", ""); err != nil {
		t.Fatalf("updateAgentStateOnDone: %v", err)
	}

	update := lastAgentUpdate(t, agentUpdatesLog)
	if update == "" {
		t.Fatalf("agent bead was never updated")
	}
	if strings.Contains(update, "agent_state: stuck") {
		t.Errorf("self-closed no-changes polecat landed on agent_state=stuck:\n%s", update)
	}
	if !strings.Contains(update, "agent_state: done") {
		t.Errorf("expected agent_state=done for a self-closed no-changes exit, got:\n%s", update)
	}
}

// TestDoneStillMarksStuckWhenSourceBeadIsNotClosed is the control: a DEFERRED
// exit whose hooked bead is still open (a genuine pause, not a self-closed
// no-changes outcome) must still land on agent_state=stuck so a human/mayor
// notices the paused work.
func TestDoneStillMarksStuckWhenSourceBeadIsNotClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	agentUpdatesLog, rigDir := selfCloseStuckStub(t, "gt-base-123", "in_progress")

	if err := updateAgentStateOnDone(rigDir, filepath.Dir(rigDir), ExitDeferred, "gt-base-123", ""); err != nil {
		t.Fatalf("updateAgentStateOnDone: %v", err)
	}

	update := lastAgentUpdate(t, agentUpdatesLog)
	if update == "" {
		t.Fatalf("agent bead was never updated")
	}
	if !strings.Contains(update, "agent_state: stuck") {
		t.Errorf("expected agent_state=stuck for a genuinely paused (still-open) bead, got:\n%s", update)
	}
}
