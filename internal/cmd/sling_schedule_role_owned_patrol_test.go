package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestScheduleBeadRefusesRoleOwnedPatrolStep pins the deferred-dispatch half of
// gt-o26h.
//
// runSling (sling.go) and executeSling (sling_dispatch.go) have refused to
// sling a role-owned patrol step (a deacon's/witness's own patrol-cycle
// content, tagged attached_formula: mol-*-patrol) since gt-pjuow, but only on
// the direct path: with scheduler.max_polecats > 0, dispatch routes through
// scheduleBead instead, and that gate was missing there — the same shape as
// gt-bel1/gt-ygb7/gt-s1id above it. A deacon heartbeat-refresh step landed on
// a polecat's hook through exactly this gap.
//
// The bd stub exits non-zero on every mutating subcommand, so the test also
// proves the refusal lands before anything is written: no sling context, no
// auto-convoy, no formula cook.
func TestScheduleBeadRefusesRoleOwnedPatrolStep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping sling schedule test on Windows")
	}

	_, logPath := setupRoleOwnedPatrolScheduleTest(t)

	err := scheduleBead("zz-hb", "gastown", ScheduleOptions{})
	if err == nil {
		t.Fatal("expected scheduleBead to refuse a role-owned patrol step")
	}
	if !strings.Contains(err.Error(), "role-owned patrol step") {
		t.Fatalf("unexpected error: %v", err)
	}

	assertNoBdSideEffects(t, logPath)
}

// TestScheduleBeadForceOverridesRoleOwnedPatrol is the control: --force must
// still get through, or the gate above would be indistinguishable from a
// scheduleBead that refuses everything. It fails later (the stub refuses the
// context write), which is itself the proof that the guard let it past.
func TestScheduleBeadForceOverridesRoleOwnedPatrol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping sling schedule test on Windows")
	}

	_, logPath := setupRoleOwnedPatrolScheduleTest(t)

	err := scheduleBead("zz-hb", "gastown", ScheduleOptions{Force: true})
	if err != nil && strings.Contains(err.Error(), "role-owned patrol step") {
		t.Fatalf("--force must override the role-owned-patrol gate, got: %v", err)
	}

	logBytes, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read bd log: %v", readErr)
	}
	if !strings.Contains(string(logBytes), "create") {
		t.Fatalf("--force did not reach the sling-context write; log:\n%s", logBytes)
	}
}

// setupRoleOwnedPatrolScheduleTest builds a town whose only bead, zz-hb, is a
// deacon-patrol heartbeat-refresh step (attached_formula: mol-deacon-patrol),
// backed by a bd stub that refuses every mutating subcommand.
func setupRoleOwnedPatrolScheduleTest(t *testing.T) (townRoot, logPath string) {
	t.Helper()

	townRoot, logPath = setupCrossDatabaseSlingGuardTest(t)

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
    echo '[{"id":"zz-hb","title":"Refresh heartbeat","status":"open","assignee":"","description":"attached_formula: mol-deacon-patrol"}]'
    ;;
  create|update|cook|mol|close|dep|sql)
    echo "unexpected side effect: $cmd" >&2
    exit 2
    ;;
esac
exit 0
`
	_ = writeBDStub(t, filepath.Join(townRoot, "bin"), script, "")

	// setupCrossDatabaseSlingGuardTest's stub logs a `show` on the way in; start
	// from a clean log so assertNoBdSideEffects reads only this test's calls.
	if err := os.WriteFile(logPath, nil, 0644); err != nil {
		t.Fatalf("truncate bd log: %v", err)
	}

	return townRoot, logPath
}
