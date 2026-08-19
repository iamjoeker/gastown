package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

// TestReportPatrolCycleStartsNextCycleWithNoActivePatrol is the gt-u2u
// anti-stall regression.
//
// The deacon formula tells the Deacon to work and close its steps, but closing
// the final step (loop-or-exit) auto-closes the patrol root. `gt patrol report`
// then found no active patrol, returned "no active patrol found for deacon",
// and exited before spawning — leaving the chain with no next wisp at all.
// Following the formula and running the command were mutually exclusive.
// Starting the next cycle is the command's whole job, so a missing root now
// warns instead of aborting.
func TestReportPatrolCycleStartsNextCycleWithNoActivePatrol(t *testing.T) {
	body := `
case "$cmd" in
  list|query) echo '[]' ;;
  close|update)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$cmd $arg" >> "$closes_log"
    done
    ;;
esac`

	townRoot, closesLog := newDescendantStubTown(t, body)

	spawned := 0
	spawn := func(PatrolConfig) (string, error) {
		spawned++
		return "hq-wisp-next", nil
	}

	cfg := PatrolConfig{
		RoleName:      "deacon",
		PatrolMolName: constants.MolDeaconPatrol,
		BeadsDir:      townRoot,
		Assignee:      "deacon",
		Beads:         beads.New(townRoot),
	}

	if err := reportPatrolCycle(cfg, "all clear", "heartbeat:OK", spawn); err != nil {
		t.Fatalf("reportPatrolCycle: %v", err)
	}
	if spawned != 1 {
		t.Errorf("spawn calls = %d, want 1 — the chain must not stall when the root is already closed", spawned)
	}
	if got := readClosedIDs(t, closesLog); len(got) != 0 {
		t.Errorf("unexpected close/update calls with no active patrol: %v", got)
	}
}

// TestReportPatrolCycleClosesWispStepsBeforeRoot verifies the happy path still
// closes the step wisps under the root before closing the root itself — the
// half of gt-u2u where the audit printed "loop-or-exit OK" while the step wisp
// stayed open under a closed parent.
func TestReportPatrolCycleClosesWispStepsBeforeRoot(t *testing.T) {
	body := `
case "$cmd" in
  list) echo '[]' ;;
  query)
    case "$all" in
      *'parent="hq-wisp-root"'*)
        echo '[{"id":"hq-wisp-loop-or-exit","title":"loop-or-exit","status":"open","ephemeral":true}]'
        ;;
      *'status="hooked"'*)
        echo '[{"id":"hq-wisp-root","title":"mol-deacon-patrol cycle","status":"hooked","assignee":"deacon","ephemeral":true}]'
        ;;
      *) echo '[]' ;;
    esac
    ;;
  close|update)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$cmd $arg" >> "$closes_log"
    done
    ;;
esac`

	townRoot, closesLog := newDescendantStubTown(t, body)

	spawned := 0
	spawn := func(PatrolConfig) (string, error) {
		spawned++
		return "hq-wisp-next", nil
	}

	cfg := PatrolConfig{
		RoleName:      "deacon",
		PatrolMolName: constants.MolDeaconPatrol,
		BeadsDir:      townRoot,
		Assignee:      "deacon",
		Beads:         beads.New(townRoot),
	}

	if err := reportPatrolCycle(cfg, "all clear", "heartbeat:OK", spawn); err != nil {
		t.Fatalf("reportPatrolCycle: %v", err)
	}
	if spawned != 1 {
		t.Errorf("spawn calls = %d, want 1", spawned)
	}

	calls := strings.Join(readClosedIDs(t, closesLog), "\n")
	if !strings.Contains(calls, "close hq-wisp-loop-or-exit") {
		t.Errorf("orphaned step wisp was not closed; calls:\n%s", calls)
	}
	if !strings.Contains(calls, "close hq-wisp-root") {
		t.Errorf("patrol root was not closed; calls:\n%s", calls)
	}
}
