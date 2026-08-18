//go:build !windows

package dog

import (
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
)

// =============================================================================
// gt-p2e7: a dog session outlives its agent
//
// EnsureRunning used to gate on HasSession — "does the tmux session exist" —
// and a session whose agent process has died still answers yes. So Start was
// never reached, the corpse was reused, and the dispatch about to be delivered
// into it was destroyed: the nudge typed at a dead pane, the mail left open, the
// dog left in StateWorking, and the dolt backup never written. Dogs are the one
// agent whose output is a file on disk rather than a branch or a bead, so
// nothing downstream noticed; six backups went missing on dog alpha in a single
// day against a 7-for-7 control group before anyone tallied artifacts against
// dispatches.
//
// HasLiveAgent is the verdict that tells those two states apart. These tests
// exercise it against a real tmux server, because the whole defect lives in the
// gap between what tmux reports and what is actually running.
//
// SAFETY: every session here is created on a UNIQUE SOCKET. tmux targets by
// session name on a shared socket, so a test that used the default socket could
// reach hq-mayor, hq-deacon and the production dogs. Directory isolation does
// not help; the socket is what isolates.
// =============================================================================

// testSocket returns a socket name no other process is using, and guarantees the
// server on it is torn down when the test ends.
func testSocket(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available, skipping session liveness test")
	}
	socket := fmt.Sprintf("gt-p2e7-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	return socket
}

// startBareSession creates a session with a plain sleep in it: a live tmux
// session with no agent, which is exactly what a dog session decays into when
// its agent dies.
func startBareSession(t *testing.T, socket, session string) {
	t.Helper()
	cmd := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", session, "sleep 600")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not create tmux session on isolated socket %s: %v (%s)", socket, err, out)
	}
}

// The regression. A session with no agent in it must not be reported as live,
// or a dispatch gets delivered into it and is lost.
func TestHasLiveAgent_SessionWithoutAgentIsNotLive(t *testing.T) {
	socket := testSocket(t)
	sm := NewSessionManager(tmux.NewTmuxWithSocket(socket), t.TempDir(), nil)

	session := sm.SessionName("alpha")
	startBareSession(t, socket, session)

	// The old gate. This is true, and being true is precisely the problem.
	running, err := sm.IsRunning("alpha")
	if err != nil {
		t.Fatalf("IsRunning() error = %v", err)
	}
	if !running {
		t.Fatalf("test setup failed: session %s was not created on socket %s", session, socket)
	}

	// The new gate must disagree. Whatever the probe reports about itself, it
	// must not claim there is an agent here — the pane is running sleep.
	live, _ := sm.HasLiveAgent("alpha")
	if live {
		t.Fatal("HasLiveAgent reported a live agent in a session running only `sleep`. " +
			"That is the gt-p2e7 defect: EnsureRunning would reuse this session instead " +
			"of replacing it, and the dispatch delivered into it would vanish with no error")
	}
}

// The other half of the discrimination: no session at all is confidently not
// live, and must not be reported as a probe failure. An unknown verdict is
// treated as "live" by callers, so returning one here would leave a cold dog
// unable to have a session started for it.
func TestHasLiveAgent_NoSessionIsConfidentlyNotLive(t *testing.T) {
	socket := testSocket(t)
	sm := NewSessionManager(tmux.NewTmuxWithSocket(socket), t.TempDir(), nil)

	live, err := sm.HasLiveAgent("never-existed")
	if err != nil {
		t.Fatalf("HasLiveAgent() error = %v, want nil: a missing session is a known "+
			"answer, not a failed probe", err)
	}
	if live {
		t.Fatal("HasLiveAgent reported a live agent for a dog with no session at all")
	}
}

// IsRunning still answers its own, narrower question. Keeping both is the point:
// the two verdicts differ exactly where the defect lives, and callers that only
// need "is there a pane" (status display) must not be forced through a liveness
// probe.
func TestIsRunning_StillReportsSessionExistenceOnly(t *testing.T) {
	socket := testSocket(t)
	sm := NewSessionManager(tmux.NewTmuxWithSocket(socket), t.TempDir(), nil)

	running, err := sm.IsRunning("bravo")
	if err != nil {
		t.Fatalf("IsRunning() error = %v", err)
	}
	if running {
		t.Fatal("IsRunning reported a session for a dog with none")
	}

	startBareSession(t, socket, sm.SessionName("bravo"))

	running, err = sm.IsRunning("bravo")
	if err != nil {
		t.Fatalf("IsRunning() error = %v", err)
	}
	if !running {
		t.Fatal("IsRunning failed to see a session that exists")
	}
}
