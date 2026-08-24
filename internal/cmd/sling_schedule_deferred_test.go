package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestScheduleBeadRefusesDeferredBead pins the sling half of gt-bel1.
//
// `gt sling` has refused deferred beads since gt-1326mw, but only on the direct
// path: with scheduler.max_polecats > 0, dispatch routes through scheduleBead
// instead, and that gate was missing there. bd-sdj — deferred on a condition
// stated in its own title — collected five sling contexts through this path.
//
// The bd stub exits non-zero on every mutating subcommand, so the test also
// proves the refusal lands before anything is written: no sling context, no
// auto-convoy, no formula cook.
func TestScheduleBeadRefusesDeferredBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping sling schedule test on Windows")
	}

	_, logPath := setupDeferredScheduleTest(t)

	err := scheduleBead("zz-sdj", "gastown", ScheduleOptions{})
	if err == nil {
		t.Fatal("expected scheduleBead to refuse a deferred bead")
	}
	if !strings.Contains(err.Error(), "is deferred") {
		t.Fatalf("unexpected error: %v", err)
	}

	assertNoBdSideEffects(t, logPath)
}

// TestScheduleBeadForceOverridesDeferred is the control: --force must still get
// through, or the gate above would be indistinguishable from a scheduleBead that
// refuses everything. It fails later (the stub refuses the context write), which
// is itself the proof that the deferred gate let it past.
func TestScheduleBeadForceOverridesDeferred(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping sling schedule test on Windows")
	}

	_, logPath := setupDeferredScheduleTest(t)

	err := scheduleBead("zz-sdj", "gastown", ScheduleOptions{Force: true})
	if err != nil && strings.Contains(err.Error(), "is deferred") {
		t.Fatalf("--force must override the deferred gate, got: %v", err)
	}

	// It reached the sling-context write, which is the side effect the refusal
	// above prevents. Without this the test would pass on any early failure.
	logBytes, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read bd log: %v", readErr)
	}
	if !strings.Contains(string(logBytes), "create") {
		t.Fatalf("--force did not reach the sling-context write; log:\n%s", logBytes)
	}
}

func assertNoBdSideEffects(t *testing.T, logPath string) {
	t.Helper()

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	log := string(logBytes)
	for _, sideEffect := range []string{"create", "update", "cook", "mol", "close", "dep", "sql"} {
		if strings.Contains(log, sideEffect) {
			t.Fatalf("bd side effect %q ran before the deferred gate refused the bead; log:\n%s", sideEffect, log)
		}
	}
}

// setupDeferredScheduleTest builds a town whose only bead, zz-sdj, is deferred,
// backed by a bd stub that refuses every mutating subcommand.
func setupDeferredScheduleTest(t *testing.T) (townRoot, logPath string) {
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
    echo '[{"id":"zz-sdj","title":"Deferred docs follow-up","status":"deferred","assignee":"","description":""}]'
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
