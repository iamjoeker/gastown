package formula

import (
	"strings"
	"testing"
)

// The deacon patrol's Idle Town Protocol probe (health-scan step) gated
// HEALTH_CHECK nudges on `bd list --status=in_progress`. That command reads
// the issues table and cannot see wisps, and every merge-request bead is a
// wisp — so a live MR under review read as "no active work" with no error.
// A second, independent defect compounded it: MR beads live in each rig's
// own db, not the town/hq db the deacon patrols from, so even a wisp-aware
// query with no `-C <rig-path>` still saw zero rig work. Either defect alone
// produces the same false-CLEAN "town is idle" verdict while a P0 merge is
// in flight, suppressing the nudge that also resets the polled agent's
// backoff.
//
// See: gt-90ew, hq-eqf2
func deaconPatrolStep(t *testing.T, id string) Step {
	t.Helper()

	content, err := formulasFS.ReadFile("formulas/mol-deacon-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading deacon patrol formula: %v", err)
	}
	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing deacon patrol formula: %v", err)
	}

	for _, s := range f.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("deacon patrol formula: step %q not found", id)
	return Step{}
}

// TestIdleTownProtocolDoesNotUseBdList fences the wisp-exclusion defect: the
// probe must count in_progress work with a command that can see wisps
// (`bd query`, not `bd list`), because MR beads are wisps.
func TestIdleTownProtocolDoesNotUseBdList(t *testing.T) {
	step := deaconPatrolStep(t, "health-scan")

	for _, line := range shellLines(step.Description) {
		if strings.Contains(line, "bd list") && strings.Contains(line, "in_progress") {
			t.Errorf("health-scan runs %q; `bd list` reads the ISSUES table and cannot "+
				"see wisps, so a live merge-request bead (always a wisp) is invisible to "+
				"this probe. Use `bd query` instead. See gt-90ew", line)
		}
	}
}

// TestIdleTownProtocolScopesPerRig fences the db-scope defect: even a
// wisp-aware query must run per rig, since rig work (including MR beads)
// lives in the rig's own db and is invisible from the town/hq context the
// deacon patrols from by default.
func TestIdleTownProtocolScopesPerRig(t *testing.T) {
	step := deaconPatrolStep(t, "health-scan")

	var sawQuery, sawRigScope bool
	for _, line := range shellLines(step.Description) {
		if strings.Contains(line, "bd query") && strings.Contains(line, "in_progress") {
			sawQuery = true
			if strings.Contains(line, "-C") {
				sawRigScope = true
			}
		}
	}
	if !sawQuery {
		t.Fatal("health-scan no longer probes in_progress work with `bd query`; if the " +
			"probe moved, move this fence with it. See gt-90ew")
	}
	if !sawRigScope {
		t.Error("health-scan queries in_progress work without a `-C <rig-path>` scope; " +
			"a query run from the deacon's own cwd only sees the town/hq db and stays " +
			"blind to rig-level work such as MR beads. See gt-90ew")
	}
}
