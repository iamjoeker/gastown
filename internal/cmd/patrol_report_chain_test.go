package cmd

import (
	"os"
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
        printf '[{"id":"hq-wisp-loop-or-exit","title":"loop-or-exit","status":"%s","ephemeral":true}]\n' "$(status_of hq-wisp-loop-or-exit open)"
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
    if [ "$cmd" = close ]; then mark_closed "$@"; fi
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

// TestReportPatrolCycleEmitsFeedEvents is the hq-iija regression: a completed
// patrol cycle used to leave no trace in the feed at all, so a consumer that
// infers idleness from feed silence (Boot's triage) could not tell a healthy,
// silent patrol from a dead one and false-woke it. `gt patrol report` is the
// one action every patrol cycle takes, so it must be the one that makes the
// cycle visible.
func TestReportPatrolCycleEmitsFeedEvents(t *testing.T) {
	body := `
case "$cmd" in
  list) echo '[]' ;;
  query)
    case "$all" in
      *'parent="hq-wisp-root"'*) echo '[]' ;;
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
    if [ "$cmd" = close ]; then mark_closed "$@"; fi
    ;;
esac`

	townRoot, _ := newDescendantStubTown(t, body)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	spawn := func(PatrolConfig) (string, error) { return "hq-wisp-next", nil }
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

	eventsData, err := os.ReadFile(".events.jsonl")
	if err != nil {
		t.Fatalf("read events file: %v", err)
	}
	events := string(eventsData)
	if !strings.Contains(events, `"type":"patrol_complete"`) {
		t.Errorf("no patrol_complete event; events:\n%s", events)
	}
	if !strings.Contains(events, `"type":"patrol_started"`) {
		t.Errorf("no patrol_started event; events:\n%s", events)
	}
	if !strings.Contains(events, `"actor":"deacon"`) {
		t.Errorf("patrol events not attributed to deacon; events:\n%s", events)
	}
}
