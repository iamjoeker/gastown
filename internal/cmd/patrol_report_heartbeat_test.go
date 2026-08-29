package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/deacon"
)

func TestStampDeaconHeartbeatOnReport_StampsAllStores(t *testing.T) {
	townRoot := t.TempDir()
	syncs := 0
	oldSync := deaconAgentBeadHeartbeatSync
	deaconAgentBeadHeartbeatSync = func(string) { syncs++ }
	t.Cleanup(func() { deaconAgentBeadHeartbeatSync = oldSync })

	stampDeaconHeartbeatOnReport(townRoot, "all clear")

	hb := deacon.ReadHeartbeat(townRoot)
	if hb == nil {
		t.Fatal("expected heartbeat file")
	}
	if hb.LastAction != "patrol report: all clear" {
		t.Fatalf("LastAction = %q, want patrol report summary", hb.LastAction)
	}
	if _, err := os.Stat(filepath.Join(townRoot, "deacon", ".deacon-heartbeat")); err != nil {
		t.Fatalf("expected legacy heartbeat file: %v", err)
	}
	if syncs != 1 {
		t.Fatalf("agent bead syncs = %d, want 1", syncs)
	}
}

func TestStampDeaconHeartbeatOnReport_SkipsWhenPaused(t *testing.T) {
	townRoot := t.TempDir()
	syncs := 0
	oldSync := deaconAgentBeadHeartbeatSync
	deaconAgentBeadHeartbeatSync = func(string) { syncs++ }
	t.Cleanup(func() { deaconAgentBeadHeartbeatSync = oldSync })
	if err := deacon.Pause(townRoot, "maintenance", "test"); err != nil {
		t.Fatal(err)
	}

	stampDeaconHeartbeatOnReport(townRoot, "paused")

	if hb := deacon.ReadHeartbeat(townRoot); hb != nil {
		t.Fatalf("expected no heartbeat when paused, got %+v", hb)
	}
	if syncs != 0 {
		t.Fatalf("agent bead syncs = %d, want 0", syncs)
	}
}

func TestStampDeaconHeartbeatOnReport_SkipsOnCorruptPauseFile(t *testing.T) {
	townRoot := t.TempDir()
	syncs := 0
	oldSync := deaconAgentBeadHeartbeatSync
	deaconAgentBeadHeartbeatSync = func(string) { syncs++ }
	t.Cleanup(func() { deaconAgentBeadHeartbeatSync = oldSync })
	pauseFile := deacon.GetPauseFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(pauseFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pauseFile, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}

	stampDeaconHeartbeatOnReport(townRoot, "corrupt")

	if hb := deacon.ReadHeartbeat(townRoot); hb != nil {
		t.Fatalf("expected no heartbeat on corrupt pause state, got %+v", hb)
	}
	if syncs != 0 {
		t.Fatalf("agent bead syncs = %d, want 0", syncs)
	}
}

// gt-h9yh: gt patrol report only stamped the heartbeat for the Deacon role.
// A witness or refinery that closes a cycle and goes straight into more
// work without reaching await-signal never stamped at all, so its
// heartbeat:EPOCH label froze while it was demonstrably alive.
func TestStampAgentHeartbeatOnReport_WitnessStampsResolvedBead(t *testing.T) {
	oldResolve := resolveAgentHeartbeatBead
	oldUpdate := updateAgentHeartbeatFn
	t.Cleanup(func() {
		resolveAgentHeartbeatBead = oldResolve
		updateAgentHeartbeatFn = oldUpdate
	})

	var gotRole, gotRig string
	resolveAgentHeartbeatBead = func(role, rig string) (*agentBeadCandidate, error) {
		gotRole, gotRig = role, rig
		return &agentBeadCandidate{ID: "gt-witness-institute", BeadsDir: "/beads"}, nil
	}
	var stamped, stampedDir string
	updateAgentHeartbeatFn = func(agentBead, beadsDir string) error {
		stamped, stampedDir = agentBead, beadsDir
		return nil
	}

	stampAgentHeartbeatOnReport("witness", "institute/witness")

	if gotRole != "witness" || gotRig != "institute" {
		t.Fatalf("resolveAgentHeartbeatBead(role=%q, rig=%q), want (witness, institute)", gotRole, gotRig)
	}
	if stamped != "gt-witness-institute" || stampedDir != "/beads" {
		t.Fatalf("updateAgentHeartbeatFn(%q, %q), want (gt-witness-institute, /beads)", stamped, stampedDir)
	}
}

func TestStampAgentHeartbeatOnReport_NoMatchDoesNotPanic(t *testing.T) {
	oldResolve := resolveAgentHeartbeatBead
	oldUpdate := updateAgentHeartbeatFn
	t.Cleanup(func() {
		resolveAgentHeartbeatBead = oldResolve
		updateAgentHeartbeatFn = oldUpdate
	})

	resolveAgentHeartbeatBead = func(role, rig string) (*agentBeadCandidate, error) {
		return nil, nil
	}
	calls := 0
	updateAgentHeartbeatFn = func(agentBead, beadsDir string) error {
		calls++
		return nil
	}

	stampAgentHeartbeatOnReport("refinery", "institute/refinery")

	if calls != 0 {
		t.Fatalf("updateAgentHeartbeatFn calls = %d, want 0 when no agent bead resolves", calls)
	}
}

func TestStampAgentHeartbeatOnReport_UnparsableAssigneeDoesNotPanic(t *testing.T) {
	oldResolve := resolveAgentHeartbeatBead
	t.Cleanup(func() { resolveAgentHeartbeatBead = oldResolve })

	calls := 0
	resolveAgentHeartbeatBead = func(role, rig string) (*agentBeadCandidate, error) {
		calls++
		return nil, nil
	}

	stampAgentHeartbeatOnReport("witness", "no-slash-here")

	if calls != 0 {
		t.Fatalf("resolveAgentHeartbeatBead calls = %d, want 0 when assignee has no rig prefix", calls)
	}
}
