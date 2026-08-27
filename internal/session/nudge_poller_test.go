package session

import (
	"errors"
	"strings"
	"testing"
)

// stubNudgePoller replaces EnsureNudgePoller's two side-effecting calls for the
// duration of a test, and reports what they were asked to do.
func stubNudgePoller(t *testing.T, alive bool, startErr error) (starts *[]string, aliveCalls *[]string) {
	t.Helper()

	origAlive := nudgePollerAlive
	origStart := nudgePollerStart
	t.Cleanup(func() {
		nudgePollerAlive = origAlive
		nudgePollerStart = origStart
	})

	var started, asked []string
	nudgePollerAlive = func(townRoot, session string) (int, bool) {
		asked = append(asked, townRoot+"|"+session)
		if alive {
			return 4242, true
		}
		return 0, false
	}
	nudgePollerStart = func(townRoot, session string) (int, error) {
		started = append(started, townRoot+"|"+session)
		if startErr != nil {
			return 0, startErr
		}
		return 9999, nil
	}
	return &started, &asked
}

func TestEnsureNudgePoller_StartsWhenNoneAlive(t *testing.T) {
	started, asked := stubNudgePoller(t, false, nil)

	if err := EnsureNudgePoller("/town", "gt-rig-dust"); err != nil {
		t.Fatalf("EnsureNudgePoller = %v, want nil", err)
	}
	if len(*asked) != 1 || (*asked)[0] != "/town|gt-rig-dust" {
		t.Fatalf("liveness checked with %v, want one check for /town|gt-rig-dust", *asked)
	}
	if len(*started) != 1 || (*started)[0] != "/town|gt-rig-dust" {
		t.Fatalf("started %v, want one poller for /town|gt-rig-dust; a session with no poller cannot "+
			"drain a queued nudge while it is parked at its prompt", *started)
	}
}

func TestEnsureNudgePoller_NoSecondPollerWhenOneIsAlive(t *testing.T) {
	started, _ := stubNudgePoller(t, true, nil)

	if err := EnsureNudgePoller("/town", "gt-rig-dust"); err != nil {
		t.Fatalf("EnsureNudgePoller with a live poller = %v, want nil", err)
	}
	if len(*started) != 0 {
		t.Fatalf("started %v, want none: a poller was already draining this session", *started)
	}
}

// TestEnsureNudgePoller_ReportsStartFailure is the point of the helper. The
// spawn paths treat the result as non-fatal, so an error that says nothing is
// indistinguishable from success — which is the defect (gt-xmq6), not a
// cosmetic concern.
func TestEnsureNudgePoller_ReportsStartFailure(t *testing.T) {
	boom := errors.New("finding gt binary: no such file")
	stubNudgePoller(t, false, boom)

	err := EnsureNudgePoller("/town", "gt-rig-dust")
	if err == nil {
		t.Fatal("EnsureNudgePoller = nil when the poller could not be started; nothing will drain this " +
			"session's queue and the caller has nothing to warn about")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the underlying failure %v", err, boom)
	}
	if !strings.Contains(err.Error(), "gt-rig-dust") {
		t.Errorf("error %q does not name the session it is about", err)
	}
}

// TestEnsureNudgePoller_RefusesWithoutATownRoot covers the case the callers can
// actually hit: SessionConfig.TownRoot is optional, and the queue lives under
// the town root. Silently returning nil there would report a drain that does not
// exist.
func TestEnsureNudgePoller_RefusesWithoutATownRoot(t *testing.T) {
	started, asked := stubNudgePoller(t, false, nil)

	err := EnsureNudgePoller("", "gt-rig-dust")
	if err == nil {
		t.Fatal("EnsureNudgePoller with no town root = nil; a poller cannot be started and the caller " +
			"must not be told one is running")
	}
	if !strings.Contains(err.Error(), "gt-rig-dust") {
		t.Errorf("error %q does not name the session it is about", err)
	}
	if len(*started) != 0 || len(*asked) != 0 {
		t.Fatalf("probed the poller with an empty town root: alive=%v start=%v", *asked, *started)
	}
}

func TestEnsureNudgePoller_RefusesWithoutASessionName(t *testing.T) {
	started, _ := stubNudgePoller(t, false, nil)

	if err := EnsureNudgePoller("/town", ""); err == nil {
		t.Fatal("EnsureNudgePoller with no session name = nil, want an error")
	}
	if len(*started) != 0 {
		t.Fatalf("started %v for an unnamed session", *started)
	}
}
