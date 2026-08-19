package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests drive the check through a stub `bd` on PATH rather than through a
// mocked Go function, because the defect being pinned was in the process
// boundary itself: `bd config get` exits ZERO for an unset key and prints the
// sentence "routing.mode (not set in config.yaml)" on stdout. A fake that
// returned ("", errNotFound) would have agreed with the broken code.

const (
	envRoutingYAMLMode      = "GT_TEST_ROUTING_YAML_MODE"
	envRoutingDoltMode      = "GT_TEST_ROUTING_DOLT_MODE"
	envRoutingAutoRoute     = "GT_TEST_ROUTING_AUTO_ROUTE"
	envRoutingSQLFail       = "GT_TEST_ROUTING_SQL_FAIL"
	envRoutingNoJSON        = "GT_TEST_ROUTING_NO_JSON"
	envRoutingSetMarker     = "GT_TEST_ROUTING_SET_MARKER"
	routingUnsetSentence    = "routing.mode (not set in config.yaml)"
	routingPlanningStorePat = "not set"
)

// stubRoutingBdScript answers exactly as the real bd does, including the two
// behaviours that broke the check: exit 0 with a human sentence for an unset
// key, and a routing.mode read that only ever sees config.yaml. Every `config
// set` is recorded instead of performed, so "the fix wrote nothing" is an
// assertion about an unreachable call rather than an unobserved one.
const stubRoutingBdScript = `#!/bin/sh
if [ "$1" = "config" ] && [ "$2" = "get" ]; then
  if [ "$4" = "--json" ]; then
    if [ "$GT_TEST_ROUTING_NO_JSON" = "1" ]; then
      echo "unknown flag: --json" >&2
      exit 1
    fi
    printf '{"key":"routing.mode","location":"config.yaml","schema_version":1,"value":"%s"}\n' "$GT_TEST_ROUTING_YAML_MODE"
    exit 0
  fi
  if [ -z "$GT_TEST_ROUTING_YAML_MODE" ]; then
    echo "routing.mode (not set in config.yaml)"
    exit 0
  fi
  echo "$GT_TEST_ROUTING_YAML_MODE"
  exit 0
fi
if [ "$1" = "config" ] && [ "$2" = "set" ]; then
  echo "$@" >> "$GT_TEST_ROUTING_SET_MARKER"
  exit 0
fi
if [ "$1" = "sql" ]; then
  if [ "$GT_TEST_ROUTING_SQL_FAIL" = "1" ]; then
    echo "dial tcp 127.0.0.1:3307: connect: connection refused" >&2
    exit 1
  fi
  echo "key,value"
  if [ -n "$GT_TEST_ROUTING_DOLT_MODE" ]; then
    echo "routing.mode,$GT_TEST_ROUTING_DOLT_MODE"
  fi
  if [ -n "$GT_TEST_ROUTING_AUTO_ROUTE" ]; then
    echo "contributor.auto_route,$GT_TEST_ROUTING_AUTO_ROUTE"
  fi
  exit 0
fi
exit 1
`

// routingStubEnv is the store state a test tells the stub bd to report.
type routingStubEnv struct {
	yamlMode  string // routing.mode in config.yaml, "" for unset
	doltMode  string // routing.mode row in the Dolt config table, "" for absent
	autoRoute string // contributor.auto_route row, "" for absent
	sqlFails  bool   // the Dolt config table cannot be read
	noJSON    bool   // an older bd without `config get --json`
}

// installStubRoutingBd puts the stub on PATH and returns the marker path that
// records every `bd config set` it was asked to run.
func installStubRoutingBd(t *testing.T, env routingStubEnv) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub bd is a shell script")
	}

	dir := t.TempDir()
	stubPath := filepath.Join(dir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubRoutingBdScript), 0755); err != nil {
		t.Fatalf("write stub bd: %v", err)
	}

	marker := filepath.Join(dir, "config-set-invocations")

	// PATH is replaced outright so a real bd cannot be reached if the stub
	// falls through.
	t.Setenv("PATH", dir)
	t.Setenv(envRoutingSetMarker, marker)
	t.Setenv(envRoutingYAMLMode, env.yamlMode)
	t.Setenv(envRoutingDoltMode, env.doltMode)
	t.Setenv(envRoutingAutoRoute, env.autoRoute)
	t.Setenv(envRoutingSQLFail, boolEnv(env.sqlFails))
	t.Setenv(envRoutingNoJSON, boolEnv(env.noJSON))

	return marker
}

func boolEnv(b bool) string {
	if b {
		return "1"
	}
	return ""
}

// configSetInvocations returns the argv lines the stub recorded, empty if
// `bd config set` was never run.
func configSetInvocations(t *testing.T, marker string) []string {
	t.Helper()
	data, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read config set marker: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// newRoutingTown returns a town root whose .beads parent directory exists, so
// the check's exec can chdir into it.
func newRoutingTown(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func runRoutingCheck(t *testing.T, townRoot string) *CheckResult {
	t.Helper()
	return NewRoutingModeCheck().Run(&CheckContext{TownRoot: townRoot})
}

// TestRoutingMode_UnsetIsOK is the regression for the reported defect. bd exits
// 0 for an unset key, so the "not found" branch was unreachable and the whole
// sentence fell through into the value comparison — the check warned that
// routing.mode was "'routing.mode (not set in config.yaml)'". Unset does not
// auto-route (beads reaches the role swap only under mode=="auto"), so the
// correct verdict is OK.
func TestRoutingMode_UnsetIsOK(t *testing.T) {
	installStubRoutingBd(t, routingStubEnv{})

	result := runRoutingCheck(t, newRoutingTown(t))

	if result.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK (message: %q)", result.Status, result.Message)
	}
	if strings.Contains(result.Message, routingUnsetSentence) {
		t.Errorf("message quotes bd's unset sentence back as a value: %q", result.Message)
	}
}

// TestRoutingMode_UnsetIsOK_OldBd covers a bd without `config get --json`,
// which gastown still supports (MinBeadsVersion 0.57.0). The text fallback must
// recognise the same unset case.
func TestRoutingMode_UnsetIsOK_OldBd(t *testing.T) {
	installStubRoutingBd(t, routingStubEnv{noJSON: true})

	result := runRoutingCheck(t, newRoutingTown(t))

	if result.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK (message: %q)", result.Status, result.Message)
	}
	if strings.Contains(result.Message, routingPlanningStorePat) &&
		strings.Contains(result.Message, "(") {
		t.Errorf("message leaks bd's unset sentence: %q", result.Message)
	}
}

func TestRoutingMode_ExplicitIsOK(t *testing.T) {
	installStubRoutingBd(t, routingStubEnv{yamlMode: "explicit"})

	result := runRoutingCheck(t, newRoutingTown(t))

	if result.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK (message: %q)", result.Status, result.Message)
	}
}

// TestRoutingMode_YAMLAutoWarns keeps the one state that actually diverts
// flagged.
func TestRoutingMode_YAMLAutoWarns(t *testing.T) {
	installStubRoutingBd(t, routingStubEnv{yamlMode: "auto"})

	result := runRoutingCheck(t, newRoutingTown(t))

	if result.Status != StatusWarning {
		t.Fatalf("status = %v, want StatusWarning (message: %q)", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "'auto'") {
		t.Errorf("message does not name the offending value: %q", result.Message)
	}
	if !strings.Contains(result.Message, routingLayerYAML) {
		t.Errorf("message does not name the layer holding it: %q", result.Message)
	}
}

// TestRoutingMode_DoltAutoWarns pins the blind spot the original check had.
// `bd config get routing.mode` reads config.yaml only, and gt-frr's auto value
// lived in the Dolt config table with yaml unset — so the check reported
// "not set" for a store that was auto-routing.
func TestRoutingMode_DoltAutoWarns(t *testing.T) {
	installStubRoutingBd(t, routingStubEnv{doltMode: "auto"})

	result := runRoutingCheck(t, newRoutingTown(t))

	if result.Status != StatusWarning {
		t.Fatalf("status = %v, want StatusWarning (message: %q)", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, routingLayerDolt) {
		t.Errorf("message does not name the Dolt config table: %q", result.Message)
	}
}

// TestRoutingMode_YAMLWinsOverDoltRow pins the resolution order beads uses: a
// non-empty config.yaml value is read before the database row, so an explicit
// yaml value is not overridden by a stale auto row.
func TestRoutingMode_YAMLWinsOverDoltRow(t *testing.T) {
	installStubRoutingBd(t, routingStubEnv{yamlMode: "explicit", doltMode: "auto"})

	result := runRoutingCheck(t, newRoutingTown(t))

	if result.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK (message: %q)", result.Status, result.Message)
	}
}

// TestRoutingMode_LegacyAutoRouteWarns covers the pre-routing.mode key: beads
// promotes an empty mode to "auto" when contributor.auto_route is true, so a
// store carrying it auto-routes with routing.mode showing as unset.
func TestRoutingMode_LegacyAutoRouteWarns(t *testing.T) {
	installStubRoutingBd(t, routingStubEnv{autoRoute: "true"})

	result := runRoutingCheck(t, newRoutingTown(t))

	if result.Status != StatusWarning {
		t.Fatalf("status = %v, want StatusWarning (message: %q)", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, routingLegacyAutoRoute) {
		t.Errorf("message does not name the legacy key: %q", result.Message)
	}
}

// TestRoutingMode_UnreadableStoreWarns: with routing.mode unset in yaml the
// database row is the acting value, so a store that cannot be read leaves the
// mode genuinely unknown. Reporting OK there would be the same silent pass this
// check was fixed for.
func TestRoutingMode_UnreadableStoreWarns(t *testing.T) {
	installStubRoutingBd(t, routingStubEnv{sqlFails: true})

	result := runRoutingCheck(t, newRoutingTown(t))

	if result.Status != StatusWarning {
		t.Fatalf("status = %v, want StatusWarning (message: %q)", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "Could not determine") {
		t.Errorf("message does not report the probe as inconclusive: %q", result.Message)
	}
}

// TestRoutingMode_ChecksRigLocation confirms the rig namespace is still visited
// when a rig is named.
func TestRoutingMode_ChecksRigLocation(t *testing.T) {
	installStubRoutingBd(t, routingStubEnv{})

	townRoot := newRoutingTown(t)
	rigName := "testrig"
	if err := os.MkdirAll(filepath.Join(townRoot, rigName), 0755); err != nil {
		t.Fatalf("MkdirAll rig: %v", err)
	}

	result := NewRoutingModeCheck().Run(&CheckContext{TownRoot: townRoot, RigName: rigName})

	if result.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK (message: %q)", result.Status, result.Message)
	}
}

// TestRoutingModeFix_LeavesUnsetAlone is the second reported defect. gt-frr was
// resolved by DELETING routing.mode per Overseer decision; --fix ran
// `bd config set routing.mode explicit` unconditionally and put the key back,
// and the check nagged on every run to do exactly that. An unset store is
// already safe, so the fix must be a no-op.
func TestRoutingModeFix_LeavesUnsetAlone(t *testing.T) {
	marker := installStubRoutingBd(t, routingStubEnv{})

	if err := NewRoutingModeCheck().Fix(&CheckContext{TownRoot: newRoutingTown(t)}); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	if invocations := configSetInvocations(t, marker); len(invocations) != 0 {
		t.Errorf("fix re-added routing.mode to a store that had none: %v", invocations)
	}
}

func TestRoutingModeFix_LeavesExplicitAlone(t *testing.T) {
	marker := installStubRoutingBd(t, routingStubEnv{yamlMode: "explicit"})

	if err := NewRoutingModeCheck().Fix(&CheckContext{TownRoot: newRoutingTown(t)}); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	if invocations := configSetInvocations(t, marker); len(invocations) != 0 {
		t.Errorf("fix wrote to an already-safe store: %v", invocations)
	}
}

func TestRoutingModeFix_ClearsYAMLAuto(t *testing.T) {
	marker := installStubRoutingBd(t, routingStubEnv{yamlMode: "auto"})

	if err := NewRoutingModeCheck().Fix(&CheckContext{TownRoot: newRoutingTown(t)}); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	invocations := configSetInvocations(t, marker)
	if len(invocations) != 1 {
		t.Fatalf("got %d config set invocations, want 1: %v", len(invocations), invocations)
	}
	if !strings.Contains(invocations[0], "routing.mode explicit") {
		t.Errorf("unexpected fix command: %q", invocations[0])
	}
}

// TestRoutingModeFix_ClearsLegacyAutoRoute: writing routing.mode defeats the
// legacy promotion, which applies only while the mode is empty.
func TestRoutingModeFix_ClearsLegacyAutoRoute(t *testing.T) {
	marker := installStubRoutingBd(t, routingStubEnv{autoRoute: "true"})

	if err := NewRoutingModeCheck().Fix(&CheckContext{TownRoot: newRoutingTown(t)}); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	if invocations := configSetInvocations(t, marker); len(invocations) != 1 {
		t.Errorf("got %d config set invocations, want 1: %v", len(invocations), invocations)
	}
}

// TestRoutingModeFix_RefusesDoltRow: `bd config set` writes config.yaml and
// cannot remove a database row. Shadowing the row would report a fix whose
// effect depends on which layer wins — the failure that cost hours in gt-il30 —
// so the fix names the row instead of guessing.
func TestRoutingModeFix_RefusesDoltRow(t *testing.T) {
	marker := installStubRoutingBd(t, routingStubEnv{doltMode: "auto"})

	err := NewRoutingModeCheck().Fix(&CheckContext{TownRoot: newRoutingTown(t)})
	if err == nil {
		t.Fatal("Fix reported success for an auto value it cannot remove")
	}
	if !strings.Contains(err.Error(), "DELETE FROM config") {
		t.Errorf("error does not give the remediation: %v", err)
	}
	if invocations := configSetInvocations(t, marker); len(invocations) != 0 {
		t.Errorf("fix wrote config.yaml anyway: %v", invocations)
	}
}
