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
