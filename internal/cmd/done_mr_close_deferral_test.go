package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSourceCloseDeferredToMergeReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		exitType string
		mrID     string
		deferred bool
	}{
		{name: "completion that queued an MR", exitType: ExitCompleted, mrID: "gt-wisp-mr1", deferred: true},
		{name: "completion with no MR", exitType: ExitCompleted, mrID: "", deferred: false},
		{name: "completion with a blank MR ID", exitType: ExitCompleted, mrID: "   ", deferred: false},
		{name: "escalation", exitType: ExitEscalated, mrID: "gt-wisp-mr1", deferred: false},
		{name: "deferral", exitType: ExitDeferred, mrID: "gt-wisp-mr1", deferred: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reason := sourceCloseDeferredToMergeReason(tt.exitType, tt.mrID)
			if got := reason != ""; got != tt.deferred {
				t.Fatalf("sourceCloseDeferredToMergeReason(%q, %q) = %q, want deferred=%v", tt.exitType, tt.mrID, reason, tt.deferred)
			}
			if tt.deferred && !strings.Contains(reason, strings.TrimSpace(tt.mrID)) {
				t.Errorf("reason %q does not name the MR %q", reason, tt.mrID)
			}
		})
	}
}

// TestDoneDefersSourceCloseToMerge is gt-429i: a completion that put the work in
// the merge queue must leave the source bead open. Closing it here is what made
// the refinery's pre-merge recheck reject every MR gt done produced, and what
// made a stranded branch read DONE on every listing surface.
//
// The molecule still closes — it is this dispatch's scaffolding, and leaving it
// open would leave the base bead blocked by a wisp nobody will collect.
func TestDoneDefersSourceCloseToMerge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	t.Run("with an MR the base bead stays open", func(t *testing.T) {
		closes := runDoneStateUpdateWithStub(t, "gt-wisp-mr1")
		if beadWasClosed(closes, "gt-base-123") {
			t.Errorf("hooked bead gt-base-123 was closed despite a queued MR\nClose calls:\n%s", closes)
		}
		if !beadWasClosed(closes, "gt-wisp-xyz") {
			t.Errorf("attached molecule gt-wisp-xyz was NOT closed\nClose calls:\n%s", closes)
		}
		if !beadWasClosed(closes, "gt-step-1") {
			t.Errorf("molecule step gt-step-1 was NOT closed\nClose calls:\n%s", closes)
		}
	})

	// Control: the same stub with no MR must still close the bead, so a passing
	// case above cannot be a stub that simply never closes anything.
	t.Run("with no MR the base bead closes", func(t *testing.T) {
		closes := runDoneStateUpdateWithStub(t, "")
		if !beadWasClosed(closes, "gt-base-123") {
			t.Errorf("hooked bead gt-base-123 was NOT closed on a completion with no MR\nClose calls:\n%s", closes)
		}
	})
}

func beadWasClosed(closes, beadID string) bool {
	for _, line := range strings.Split(strings.TrimSpace(closes), "\n") {
		if strings.TrimSpace(line) == beadID {
			return true
		}
	}
	return false
}

// runDoneStateUpdateWithStub runs updateAgentStateOnDone against a stub bd and
// returns every bead ID it closed, newest last. Mirrors the stub in
// done_closeDescendants_test.go: an agent bead, a base bead carrying an attached
// molecule, and a two-step molecule.
func runDoneStateUpdateWithStub(t *testing.T, mrID string) string {
	t.Helper()

	townRoot := t.TempDir()
	for _, dir := range []string{"mayor", "gastown", filepath.Join(".beads", "locks"), "bin"} {
		if err := os.MkdirAll(filepath.Join(townRoot, dir), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	routes := `{"prefix":"gt-","path":"gastown"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	closesLog := filepath.Join(townRoot, "closes.log")
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
closes_log="%s"
status_of() {
  if [ -f "$closes_log" ] && grep -qx "$1" "$closes_log"; then echo closed; else echo "$2"; fi
}
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-base-123","agent_state":"working"}]'
        ;;
      gt-base-123)
        printf '[{"id":"gt-base-123","title":"Base bead","status":"%%s","description":"attached_molecule: gt-wisp-xyz"}]\n' "$(status_of gt-base-123 hooked)"
        ;;
      gt-wisp-xyz)
        printf '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"%%s","ephemeral":true}]\n' "$(status_of gt-wisp-xyz open)"
        ;;
    esac
    ;;
  list)
    if echo "$*" | grep -q "parent=gt-wisp-xyz"; then
      printf '[{"id":"gt-step-1","title":"Step 1","status":"%%s"},{"id":"gt-step-2","title":"Step 2","status":"%%s"}]\n' "$(status_of gt-step-1 open)" "$(status_of gt-step-2 open)"
    else
      echo '[]'
    fi
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "$closes_log"
    done
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, closesLog)

	binDir := filepath.Join(townRoot, "bin")
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
	rigDir := filepath.Join(townRoot, "gastown")
	if err := os.Chdir(rigDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	if err := updateAgentStateOnDone(rigDir, townRoot, ExitCompleted, "gt-base-123", mrID); err != nil {
		t.Fatalf("updateAgentStateOnDone: %v", err)
	}

	closes, err := os.ReadFile(closesLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read closes log: %v", err)
	}
	return string(closes)
}
