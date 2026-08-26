package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests pin gt-s1id: a polecat whose session died holding a hooked bead
// stranded its convoy permanently. The daemon's convoy feeder runs
// `gt sling <bead> <rig>` with no --force, and with scheduler.max_polecats > 0
// that lands in scheduleBead — the one dispatch path with no dead-agent check.
// The feeder retried the identical sling every 30s and logged the identical
// failure forever; only a human noticing ever cleared it.

// TestScheduleBeadReleasesStaleHookFromDeadAgent is the defect itself: with the
// holder's session confirmed gone, scheduleBead must release the hook and go on
// to queue the bead.
//
// Releasing is not decoration on top of "bypass the gate". The scheduler
// dispatches a context only when its work bead is "open"
// (isScheduledWorkBeadReady), and cleanupStaleContexts closes any context whose
// work bead is "hooked" as "stale-work-bead" — so a sling context written over
// a still-hooked bead is reaped before it can run, and the convoy deadlocks
// silently instead of loudly. Hence the assertion is on the unhook write, not
// merely on the absence of the refusal.
func TestScheduleBeadReleasesStaleHookFromDeadAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping sling schedule test on Windows")
	}

	_, logPath := setupStaleHookScheduleTest(t, true)
	stubHookedAgentDead(t, true)

	// Fails at the sling-context write (the stub refuses `bd create`), which is
	// itself proof the gate let it through. What matters is what ran first.
	err := scheduleBead("zz-hook", "gastown", ScheduleOptions{})
	if err != nil && strings.Contains(err.Error(), "already hooked") {
		t.Fatalf("dead hook holder must not block scheduling, got: %v", err)
	}

	log := readBdLog(t, logPath)
	if !strings.Contains(log, "update zz-hook --status=open --assignee=") {
		t.Fatalf("stale hook was not released; bd log:\n%s", log)
	}
	if !bdSubcommandsRun(log)["create"] {
		t.Fatalf("scheduling did not reach the sling-context write; bd log:\n%s", log)
	}
}

// TestScheduleBeadRefusesHookedBeadWithLiveAgent is the control. Without it the
// test above is indistinguishable from a scheduleBead that reclaims every hook
// it meets — which would steal work from live polecats.
func TestScheduleBeadRefusesHookedBeadWithLiveAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping sling schedule test on Windows")
	}

	_, logPath := setupStaleHookScheduleTest(t, true)
	stubHookedAgentDead(t, false)

	err := scheduleBead("zz-hook", "gastown", ScheduleOptions{})
	if err == nil {
		t.Fatal("expected scheduleBead to refuse a bead hooked to a live agent")
	}
	if !strings.Contains(err.Error(), "already hooked") {
		t.Fatalf("unexpected error: %v", err)
	}

	assertNoBdWrites(t, readBdLog(t, logPath), "for a live hook holder")
}

// TestScheduleBeadFailsWhenStaleHookReleaseDoesNotTake covers the release that
// exits 0 without changing anything — a routing miss, a write that lands in the
// wrong store. Trusting that exit code would print "✓ Scheduled" for a context
// the next cleanup pass closes as stale, converting a loud livelock into a
// silent one. The write is verified by re-reading the row, and an unverified
// release must fail before any context exists.
func TestScheduleBeadFailsWhenStaleHookReleaseDoesNotTake(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping sling schedule test on Windows")
	}

	_, logPath := setupStaleHookScheduleTest(t, false)
	stubHookedAgentDead(t, true)

	err := scheduleBead("zz-hook", "gastown", ScheduleOptions{})
	if err == nil {
		t.Fatal("expected scheduleBead to fail when the hook release does not take")
	}
	if !strings.Contains(err.Error(), "did not take") {
		t.Fatalf("unexpected error: %v", err)
	}

	log := readBdLog(t, logPath)
	if !strings.Contains(log, "update zz-hook --status=open --assignee=") {
		t.Fatalf("release was never attempted; bd log:\n%s", log)
	}
	if bdSubcommandsRun(log)["create"] {
		t.Fatalf("a sling context was written over a bead that is still hooked; bd log:\n%s", log)
	}
}

// TestScheduleBeadDryRunReleasesNothing keeps --dry-run inert: it must report
// the release it would perform without performing it.
func TestScheduleBeadDryRunReleasesNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping sling schedule test on Windows")
	}

	_, logPath := setupStaleHookScheduleTest(t, true)
	stubHookedAgentDead(t, true)

	if err := scheduleBead("zz-hook", "gastown", ScheduleOptions{DryRun: true}); err != nil {
		t.Fatalf("dry-run schedule: %v", err)
	}

	assertNoBdWrites(t, readBdLog(t, logPath), "under --dry-run")
}

func stubHookedAgentDead(t *testing.T, dead bool) {
	t.Helper()
	prev := isHookedAgentDeadFn
	t.Cleanup(func() { isHookedAgentDeadFn = prev })
	isHookedAgentDeadFn = func(string) bool { return dead }
}

func readBdLog(t *testing.T, logPath string) string {
	t.Helper()
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	return string(logBytes)
}

// bdSubcommandsRun extracts the subcommand from each logged bd invocation.
//
// A bare strings.Contains over the log cannot answer "did a write run": the
// merge-request lookup logs a SELECT naming created_at and created_by, so
// searching for "create" reports a bead creation that never happened. Match the
// leading token of each line instead.
func bdSubcommandsRun(log string) map[string]bool {
	seen := make(map[string]bool)
	for _, line := range strings.Split(log, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "--allow-stale" {
			fields = fields[1:]
		}
		if len(fields) > 0 {
			seen[fields[0]] = true
		}
	}
	return seen
}

// assertNoBdWrites fails if any mutating bd subcommand ran.
func assertNoBdWrites(t *testing.T, log, context string) {
	t.Helper()
	ran := bdSubcommandsRun(log)
	for _, sideEffect := range []string{"create", "update", "cook", "mol", "close", "dep"} {
		if ran[sideEffect] {
			t.Fatalf("bd side effect %q ran %s; bd log:\n%s", sideEffect, context, log)
		}
	}
}

// setupStaleHookScheduleTest builds a town whose only bead, zz-hook, is hooked
// to gastown/polecats/chrome. When releaseTakes is true the bd stub honours the
// unhook and later reads return an open row; when it is false the stub exits 0
// and changes nothing, which is the failure mode the verification exists for.
func setupStaleHookScheduleTest(t *testing.T, releaseTakes bool) (townRoot, logPath string) {
	t.Helper()

	townRoot, logPath = setupCrossDatabaseSlingGuardTest(t)

	releasedMarker := filepath.Join(townRoot, "released")
	t.Setenv("RELEASED_MARKER", releasedMarker)

	honour := "true"
	if !releaseTakes {
		honour = "false"
	}
	t.Setenv("RELEASE_TAKES", honour)

	script := `#!/bin/sh
echo "$*" >> "${BD_LOG}"
cmd="$1"
shift || true
if [ "$cmd" = "--allow-stale" ]; then
  cmd="$1"
  shift || true
fi
case "$cmd" in
  show)
    if [ -f "${RELEASED_MARKER}" ]; then
      echo '[{"id":"zz-hook","title":"Hooked work","status":"open","assignee":"","description":""}]'
    else
      echo '[{"id":"zz-hook","title":"Hooked work","status":"hooked","assignee":"gastown/polecats/chrome","description":""}]'
    fi
    ;;
  update)
    if [ "${RELEASE_TAKES}" = "true" ]; then
      : > "${RELEASED_MARKER}"
    fi
    ;;
  create|cook|mol|close|dep|sql)
    echo "unexpected side effect: $cmd" >&2
    exit 2
    ;;
esac
exit 0
`
	_ = writeBDStub(t, filepath.Join(townRoot, "bin"), script, "")

	// setupCrossDatabaseSlingGuardTest's own stub logs a `show` on the way in;
	// start from a clean log so the assertions read only this test's calls.
	if err := os.WriteFile(logPath, nil, 0644); err != nil {
		t.Fatalf("truncate bd log: %v", err)
	}

	return townRoot, logPath
}
