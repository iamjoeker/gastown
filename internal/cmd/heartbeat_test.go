package cmd

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// gt-oefn: syncDeaconAgentBeadHeartbeat used to discard every failure
// (getAllAgentLabels error, updateAgentHeartbeat error) instead of returning
// it, so both `gt heartbeat` and `gt deacon heartbeat` printed "Heartbeat
// updated" unconditionally while the agent-bead label — the store Witness
// second-order monitoring reads — silently stayed frozen. Mirrors hq-97l7.
func TestSyncDeaconAgentBeadHeartbeat_ReturnsErrorOnFailure(t *testing.T) {
	townRoot := t.TempDir() // no .beads directory, so bd lookup must fail

	err := syncDeaconAgentBeadHeartbeat(townRoot)
	if err == nil {
		t.Fatal("expected an error when the agent bead cannot be reached, got nil")
	}
}

// gt-64u14: gt heartbeat only stamped the heartbeat:EPOCH label on the agent
// bead when GT_ROLE was "deacon" (via syncDeaconHeartbeatStores). Every other
// role — witness, refinery, polecat, crew, mayor — fell straight through to
// "Heartbeat updated" having touched only the session heartbeat file, so the
// agent bead's label never moved no matter how often the agent heartbeat.
func TestSyncAgentBeadHeartbeat_ResolvesBeadForNonDeaconRole(t *testing.T) {
	oldGetRole := getRoleForHeartbeat
	getRoleForHeartbeat = func() (RoleInfo, error) {
		return RoleInfo{Role: RoleWitness, Rig: "nonexistent-rig", TownRoot: t.TempDir()}, nil
	}
	t.Cleanup(func() { getRoleForHeartbeat = oldGetRole })

	// No real rig/beads store exists, so the resolved agent bead lookup must
	// fail — proving syncAgentBeadHeartbeat actually resolved a bead ID and
	// attempted to sync it, rather than silently no-oping for this role.
	err := syncAgentBeadHeartbeat(t.TempDir())
	if err == nil {
		t.Fatal("expected an error attempting to sync a heartbeat for an unresolvable witness bead, got nil")
	}
}

func TestSyncAgentBeadHeartbeat_NoopForRoleWithoutAgentBead(t *testing.T) {
	oldGetRole := getRoleForHeartbeat
	getRoleForHeartbeat = func() (RoleInfo, error) {
		// Witness/refinery/polecat/crew all return "" from getAgentBeadID
		// when Rig/Polecat are unset. Confirm that's treated as "nothing to
		// stamp" rather than an error.
		return RoleInfo{Role: RoleWitness}, nil
	}
	t.Cleanup(func() { getRoleForHeartbeat = oldGetRole })

	if err := syncAgentBeadHeartbeat(t.TempDir()); err != nil {
		t.Fatalf("expected no error for a role with no resolvable agent bead, got: %v", err)
	}
}

func TestSyncDeaconHeartbeatStores_SurfacesAgentBeadSyncError(t *testing.T) {
	townRoot := t.TempDir()
	oldSync := deaconAgentBeadHeartbeatSync
	deaconAgentBeadHeartbeatSync = func(string) error {
		return errors.New("boom: agent bead label write failed")
	}
	t.Cleanup(func() { deaconAgentBeadHeartbeatSync = oldSync })

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	storeErr := syncDeaconHeartbeatStores(townRoot, "test action")

	_ = w.Close()
	os.Stderr = oldStderr
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	captured := buf.String()

	// The deacon heartbeat file write itself should still succeed even though
	// the agent-bead sync failed — the two stores are independent.
	if storeErr != nil {
		t.Fatalf("syncDeaconHeartbeatStores returned unexpected error: %v", storeErr)
	}
	if !strings.Contains(captured, "boom: agent bead label write failed") {
		t.Fatalf("expected the agent-bead sync failure to be surfaced as a warning, got stderr: %q", captured)
	}
}
