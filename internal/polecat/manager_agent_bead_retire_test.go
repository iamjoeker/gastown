package polecat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// recordingRetirer records CloseAgentBead calls and can fail on demand.
type recordingRetirer struct {
	calls []string
	err   error
}

func (r *recordingRetirer) CloseAgentBead(id, reason string) error {
	r.calls = append(r.calls, id+"|"+reason)
	return r.err
}

func TestRetireAgentBeadClosesTheBead(t *testing.T) {
	r := &recordingRetirer{}

	retireAgentBead(r, "gt-gastown-polecat-toast", "polecat removed")

	want := "gt-gastown-polecat-toast|polecat removed"
	if len(r.calls) != 1 || r.calls[0] != want {
		t.Fatalf("CloseAgentBead calls = %v, want [%s]", r.calls, want)
	}
}

// A close failure must not become a removal failure: retireAgentBead swallows it
// so a Dolt hiccup cannot strand a worktree, which is worse than a stale bead.
func TestRetireAgentBeadSwallowsFailures(t *testing.T) {
	for name, err := range map[string]error{
		"not found":   beads.ErrNotFound,
		"store error": errors.New("dolt: connection refused"),
	} {
		t.Run(name, func(t *testing.T) {
			r := &recordingRetirer{err: err}
			retireAgentBead(r, "gt-gastown-polecat-toast", "polecat removed")
			if len(r.calls) != 1 {
				t.Fatalf("expected the close to be attempted, calls = %v", r.calls)
			}
		})
	}
}

// TestRemoveClosesAgentBead is the regression test for gt-qvx7: removal used to
// reset the agent bead but leave it open, so every removed polecat became a
// permanent "dead" entry in gt feed's problems pane. Asserts on the bd close
// actually being issued for the agent bead, not on the exit code — the close is
// best-effort by design and cannot be observed through the returned error.
func TestRemoveClosesAgentBead(t *testing.T) {
	mgr, _ := setupCanonicalBranchManagerTest(t)

	if _, err := mgr.AddWithOptions("toast", AddOptions{}); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	// Start recording only now, so setup traffic cannot supply the close.
	logPath := filepath.Join(t.TempDir(), "bd.log")
	t.Setenv("MOCK_BD_LOG", logPath)

	if err := mgr.RemoveWithOptions("toast", true, true, false); err != nil {
		t.Fatalf("RemoveWithOptions: %v", err)
	}

	agentID := mgr.agentBeadID("toast")
	if !hasMockBdClose(t, logPath, agentID) {
		t.Fatalf("removal did not close agent bead %s; bd invocations:\n%s",
			agentID, readMockBdLog(t, logPath))
	}
}

func readMockBdLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "(no bd invocations recorded)"
		}
		t.Fatalf("read mock bd log: %v", err)
	}
	return string(data)
}

func hasMockBdClose(t *testing.T, path, agentID string) bool {
	t.Helper()
	for _, line := range strings.Split(readMockBdLog(t, path), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f != "close" {
				continue
			}
			for _, arg := range fields[i+1:] {
				if arg == agentID {
					return true
				}
			}
		}
	}
	return false
}
