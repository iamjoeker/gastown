package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The tests below never let the real `bd` run. A stub earlier on PATH records
// what it was asked to do instead of doing it, so the destructive call is
// unreachable rather than merely unused — running the gc to see what survives
// is itself the incident this file is about (gt-22s).

const (
	envStubGCMarker    = "GT_TEST_BD_GC_MARKER"
	envStubWispsJSON   = "GT_TEST_BD_WISPS_JSON"
	envStubProtectCSV  = "GT_TEST_BD_PROTECTED_CSV"
	envStubOpenCSV     = "GT_TEST_BD_OPEN_CSV"
	envStubSQLFail     = "GT_TEST_BD_SQL_FAIL"
	oldTimestamp       = "2020-01-01T00:00:00Z"
	unprotectedWispRow = `{"id":"gt-wisp-abandoned","status":"open","updated_at":"` + oldTimestamp + `"}`
	protectedWispRow   = `{"id":"gt-wisp-mr","status":"open","updated_at":"` + oldTimestamp + `"}`
)

// stubBdScript uses only shell builtins. PATH is replaced outright rather than
// prepended, so that a real bd cannot be reached even if the stub falls
// through — which also means no external binary, cat included, is on PATH.
const stubBdScript = `#!/bin/sh
if [ "$1" = "mol" ] && [ "$3" = "gc" ]; then
  echo "$@" >> "$GT_TEST_BD_GC_MARKER"
  exit 0
fi
if [ "$1" = "mol" ] && [ "$3" = "list" ]; then
  printf '%s' "$GT_TEST_BD_WISPS_JSON"
  exit 0
fi
if [ "$1" = "sql" ]; then
  if [ "$GT_TEST_BD_SQL_FAIL" = "1" ]; then
    exit 1
  fi
  case "$3" in
    *wisp_labels*) printf '%s' "$GT_TEST_BD_PROTECTED_CSV" ;;
    *) printf '%s' "$GT_TEST_BD_OPEN_CSV" ;;
  esac
  exit 0
fi
exit 1
`

// wispStubEnv is what a test tells the stub bd to answer with.
type wispStubEnv struct {
	wispsJSON    string // `bd mol wisp list --json` output
	protectedCSV string // wisp_labels probe output
	openCSV      string // non-closed wisps probe output
	sqlFails     bool
}

// installStubBd puts the stub on PATH and returns the marker file path. The
// marker is written only when the stub is asked to run the gc, so its absence
// is the assertion that the destructive command was never issued.
func installStubBd(t *testing.T, env wispStubEnv) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub bd is a shell script")
	}

	dir := t.TempDir()
	stubPath := filepath.Join(dir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubBdScript), 0755); err != nil {
		t.Fatalf("write stub bd: %v", err)
	}

	marker := filepath.Join(dir, "gc-invocations")

	t.Setenv("PATH", dir)
	t.Setenv(envStubGCMarker, marker)
	t.Setenv(envStubWispsJSON, env.wispsJSON)
	t.Setenv(envStubProtectCSV, env.protectedCSV)
	t.Setenv(envStubOpenCSV, env.openCSV)
	if env.sqlFails {
		t.Setenv(envStubSQLFail, "1")
	}
	return marker
}

// gcInvocations returns the argv lines the stub recorded, empty if the gc was
// never run.
func gcInvocations(t *testing.T, marker string) []string {
	t.Helper()
	data, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read gc marker: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func wispListJSON(rows ...string) string {
	return fmt.Sprintf(`{"count":%d,"schema_version":1,"wisps":[%s]}`, len(rows), strings.Join(rows, ","))
}

func newRiggedTown(t *testing.T) (townRoot, rigName string) {
	t.Helper()
	townRoot = t.TempDir()
	rigName = "testrig"
	writeRigsJSON(t, townRoot, rigName)
	if err := os.MkdirAll(filepath.Join(townRoot, rigName), 0755); err != nil {
		t.Fatalf("MkdirAll rig: %v", err)
	}
	return townRoot, rigName
}

// TestParseWispList_Envelope pins the shape bd actually emits. The rows arrive
// wrapped in an object with a count; decoding that into a slice fails, and the
// caller reports zero on any decode error, so the check silently found no
// abandoned wisps on every rig.
func TestParseWispList_Envelope(t *testing.T) {
	rows, err := parseWispList([]byte(wispListJSON(unprotectedWispRow)))
	if err != nil {
		t.Fatalf("parseWispList: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].ID != "gt-wisp-abandoned" || rows[0].Status != "open" {
		t.Errorf("unexpected row: %+v", rows[0])
	}
}

// TestParseWispList_BareArray keeps an older bd, which returned a bare array,
// working.
func TestParseWispList_BareArray(t *testing.T) {
	rows, err := parseWispList([]byte("[" + unprotectedWispRow + "]"))
	if err != nil {
		t.Fatalf("parseWispList: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "gt-wisp-abandoned" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestWispGCCheck_CountsAbandonedWisps(t *testing.T) {
	townRoot, _ := newRiggedTown(t)
	installStubBd(t, wispStubEnv{
		wispsJSON:    wispListJSON(unprotectedWispRow),
		protectedCSV: "issue_id\n",
		openCSV:      "id\ngt-wisp-abandoned\n",
	})

	result := NewWispGCCheck().Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusWarning {
		t.Fatalf("got %v (%s), want StatusWarning", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "1 abandoned") {
		t.Errorf("message = %q, want a count of 1", result.Message)
	}
	if !strings.Contains(result.FixHint, RigWispGCOptInEnv) {
		t.Errorf("fix hint should name the opt-in, got %q", result.FixHint)
	}
}

// TestWispGCCheck_ExcludesProtectedFromCount keeps the number reported equal to
// the number the fix would act on. Counting a record the fix refuses reads as
// outstanding work and invites reaching for the override to clear it.
func TestWispGCCheck_ExcludesProtectedFromCount(t *testing.T) {
	townRoot, _ := newRiggedTown(t)
	installStubBd(t, wispStubEnv{
		wispsJSON:    wispListJSON(protectedWispRow),
		protectedCSV: "issue_id\ngt-wisp-mr\n",
		openCSV:      "id\ngt-wisp-mr\n",
	})

	result := NewWispGCCheck().Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusOK {
		t.Fatalf("got %v (%s), want StatusOK — the only abandoned wisp is protected", result.Status, result.Message)
	}
}

// TestWispGCCheck_FixDeclinesWithoutOptIn is the guard gt-22s asks for: this
// call site runs against rig databases, so it must not delete without an
// explicit override.
func TestWispGCCheck_FixDeclinesWithoutOptIn(t *testing.T) {
	townRoot, _ := newRiggedTown(t)
	marker := installStubBd(t, wispStubEnv{
		wispsJSON:    wispListJSON(unprotectedWispRow),
		protectedCSV: "issue_id\n",
		openCSV:      "id\ngt-wisp-abandoned\n",
	})
	t.Setenv(RigWispGCOptInEnv, "")

	check := NewWispGCCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("setup: expected a warning to fix, got %v (%s)", result.Status, result.Message)
	}

	err := check.Fix(ctx)
	if err == nil {
		t.Fatal("Fix should refuse without the opt-in")
	}
	if !strings.Contains(err.Error(), RigWispGCOptInEnv) {
		t.Errorf("refusal should name the opt-in, got %q", err)
	}
	if got := gcInvocations(t, marker); len(got) != 0 {
		t.Fatalf("gc was run despite the refusal: %v", got)
	}
}

// TestWispGCCheck_FixRunsWithOptIn is the positive control for every "gc was
// never run" assertion above and below. If this test stopped observing an
// invocation, the marker mechanism would be broken and those assertions would
// pass for the wrong reason.
func TestWispGCCheck_FixRunsWithOptIn(t *testing.T) {
	townRoot, _ := newRiggedTown(t)
	marker := installStubBd(t, wispStubEnv{
		wispsJSON:    wispListJSON(unprotectedWispRow),
		protectedCSV: "issue_id\n",
		openCSV:      "id\ngt-wisp-abandoned\n",
	})
	t.Setenv(RigWispGCOptInEnv, "1")

	check := NewWispGCCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("setup: expected a warning to fix, got %v (%s)", result.Status, result.Message)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	got := gcInvocations(t, marker)
	if len(got) != 1 {
		t.Fatalf("got %d gc invocations, want 1: %v", len(got), got)
	}
	if got[0] != strings.Join(wispGCArgs(), " ") {
		t.Errorf("gc argv = %q, want %q", got[0], strings.Join(wispGCArgs(), " "))
	}
}

// TestWispGCCheck_FixSkipsRigWithReachableProtectedWisp covers the moment the
// record matters most: an MR wisp sits open for as long as the merge is
// pending, and the age threshold here is one hour.
func TestWispGCCheck_FixSkipsRigWithReachableProtectedWisp(t *testing.T) {
	townRoot, _ := newRiggedTown(t)
	marker := installStubBd(t, wispStubEnv{
		wispsJSON:    wispListJSON(unprotectedWispRow, protectedWispRow),
		protectedCSV: "issue_id\ngt-wisp-mr\n",
		openCSV:      "id\ngt-wisp-abandoned\ngt-wisp-mr\n",
	})
	t.Setenv(RigWispGCOptInEnv, "1")

	check := NewWispGCCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("setup: expected a warning to fix, got %v (%s)", result.Status, result.Message)
	}

	err := check.Fix(ctx)
	if err == nil {
		t.Fatal("Fix should skip a rig holding a reachable protected wisp")
	}
	if !strings.Contains(err.Error(), "gt-wisp-mr") {
		t.Errorf("skip should name the protected wisp, got %q", err)
	}
	if got := gcInvocations(t, marker); len(got) != 0 {
		t.Fatalf("gc was run with a protected wisp reachable: %v", got)
	}
}

// TestWispGCCheck_FixSkipsWhenProbeFails treats an unanswerable question as a
// reachable protected wisp. A Dolt outage is not evidence that there is nothing
// to lose, and only one of the two outcomes can be retried.
func TestWispGCCheck_FixSkipsWhenProbeFails(t *testing.T) {
	townRoot, rigName := newRiggedTown(t)
	marker := installStubBd(t, wispStubEnv{
		wispsJSON:    wispListJSON(unprotectedWispRow),
		protectedCSV: "issue_id\n",
		openCSV:      "id\n",
	})
	t.Setenv(RigWispGCOptInEnv, "1")

	check := NewWispGCCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	// Populate abandonedRigs while the probe still answers, then break it, so
	// the test exercises Fix's guard rather than Run's counting.
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("setup: expected a warning to fix, got %v (%s)", result.Status, result.Message)
	}
	t.Setenv(envStubSQLFail, "1")

	err := check.Fix(ctx)
	if err == nil {
		t.Fatal("Fix should skip a rig whose protection probe failed")
	}
	if !strings.Contains(err.Error(), rigName) {
		t.Errorf("skip should name the rig, got %q", err)
	}
	if got := gcInvocations(t, marker); len(got) != 0 {
		t.Fatalf("gc was run without confirming protection: %v", got)
	}
}

// TestWispGCArgs_StaysBare pins the argv the reachability probe vouches for.
// protectedWispsAtRisk only looks at non-closed wisps, so adding --closed or
// --all would widen the delete set past what was checked, and --force removes
// the preview that makes the closed forms declinable.
func TestWispGCArgs_StaysBare(t *testing.T) {
	args := strings.Join(wispGCArgs(), " ")
	for _, forbidden := range []string{"--closed", "--all", "--force", "--age"} {
		if strings.Contains(args, forbidden) {
			t.Errorf("wispGCArgs must not pass %s (got %q)", forbidden, args)
		}
	}
}
