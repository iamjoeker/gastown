package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

// TestReconcileAgentStateWritesToARealStore is the positive control the bead
// asks for by name: "confirm the STORED agent_state changed — not the command's
// rendered output."
//
// Everything else about this reconcile is measured against a double. A double
// proves the logic and cannot prove the write reaches a row, and this repair
// exists because a field nobody could write stranded three polecats — so the
// one claim worth checking against a real store is that the write lands and
// reads back.
//
// The read-back is deliberately done through a SECOND beads handle opened on
// the same directory, not through the one that wrote. Asking the writer whether
// it wrote is the shape of a guard compared against its own source.
func TestReconcileAgentStateWritesToARealStore(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed")
	}

	dir := t.TempDir()
	// A TMPDIR under $HOME makes every `bd init` report "already initialized",
	// because ~/.beads is an ancestor workspace. Fail loudly rather than let the
	// fixture silently reuse somebody else's store.
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(dir, home+string(filepath.Separator)) {
		t.Fatalf("TempDir %s is under $HOME; bd init would resolve an ancestor workspace", dir)
	}

	// Embedded Dolt. Port 1 is unreachable on purpose: a fixture that silently
	// falls through to the production server on 3307 is the failure mode this
	// guards against, not a convenience.
	env := append(os.Environ(), "BEADS_DOLT_PORT=1")
	init := exec.Command("bd", "init", "--prefix=iso")
	init.Dir = dir
	init.Env = env
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("bd init in %s: %v\n%s", dir, err, out)
	}
	t.Setenv("BEADS_DOLT_PORT", "1")

	const agentBeadID = "iso-gastown-polecat-chrome"
	writer := beads.New(dir)
	if _, err := writer.CreateAgentBead(agentBeadID, agentBeadID, &beads.AgentFields{
		RoleType:   "polecat",
		Rig:        "gastown",
		AgentState: string(beads.AgentStateWorking),
	}); err != nil {
		t.Fatalf("creating the agent bead fixture: %v", err)
	}

	// The fixture must be the state under test before anything is concluded from
	// the run. A store that came up without the field would make the assertion
	// below pass for the wrong reason.
	_, before, err := beads.New(dir).GetAgentBead(agentBeadID)
	if err != nil || before == nil {
		t.Fatalf("reading the fixture back: %v (fields=%v)", err, before)
	}
	if before.AgentState != string(beads.AgentStateWorking) {
		t.Fatalf("fixture agent_state = %q, want working — the test never had its subject", before.AgentState)
	}

	// The town-log audit line resolves its root from the cwd. Run from a
	// directory that is not a town so this cannot append to a live town's log.
	t.Chdir(t.TempDir())

	status := staleWorkingStatus()
	reconcileAgentStateIfStale(status, writer, agentBeadID,
		&polecat.Polecat{State: polecat.StateIdle}, before, staleWorkingInput())

	out := assertReconcileOutcome(t, status.Reconcile, "agent_state", reconcileActionWritten)
	if out.Previous != string(beads.AgentStateWorking) {
		t.Fatalf("previous = %q, want working", out.Previous)
	}

	// The claim, checked where the bead says to check it.
	_, after, err := beads.New(dir).GetAgentBead(agentBeadID)
	if err != nil || after == nil {
		t.Fatalf("re-reading the agent bead: %v (fields=%v)", err, after)
	}
	if after.AgentState != string(beads.AgentStateIdle) {
		t.Fatalf("STORED agent_state = %q, want idle", after.AgentState)
	}
}
