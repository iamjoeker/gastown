package session

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/nudge"
)

// Injection points for EnsureNudgePoller, so its decision can be tested without
// spawning a real poller process or reading a real PID file.
var (
	nudgePollerAlive = nudge.PollerAlive
	nudgePollerStart = nudge.StartPoller
)

// EnsureNudgePoller guarantees that the session just created has something that
// will take a queued nudge back OUT of the queue.
//
// Exactly two things drain the nudge queue: the agent's own turn-boundary hook
// (Claude's UserPromptSubmit), which fires when the agent submits a prompt, and
// a background nudge-poller, which polls every 10s and injects once the pane is
// idle. An agent PARKED at its prompt has no next turn boundary — that is what
// parked means — so for a parked session with no poller the first mechanism
// cannot fire and the second does not exist. A nudge queued for it is then not
// unlucky or racy but structurally undeliverable, and every producer that
// reaches nudge.Enqueue reports success over it (gt-1t0v, gt-xmq6).
//
// Four spawn paths — crew, deacon, refinery, witness — each started a poller of
// their own. Polecat, mayor, dog and boot sessions did not, and neither did the
// daemon's restart path, which recreates a session for ANY role and so silently
// undid the poller the original spawn had started. Measured on one box on
// 2026-08-26: 9 of 16 live sessions had no poller, polecats and the Mayor among
// them.
//
// The rule that has to be remembered once per spawn path has the same shape as
// the defect, so TestSpawnPathsStartANudgePoller does the remembering.
//
// Callers treat a failure as non-fatal and WARN: the session is already up by
// the time this runs, and killing a working agent over a delayed nudge would be
// a worse outcome than the delay. What they must not do is swallow it — a
// session with no poller is exactly the state nothing else reports.
func EnsureNudgePoller(townRoot, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("cannot start a nudge poller: no session name")
	}
	if townRoot == "" {
		return fmt.Errorf("cannot start a nudge poller for %s: no town root, and the queue lives under "+
			"the town root; nudges queued for this session will sit undelivered unless it takes another turn", sessionID)
	}

	// StartPoller is idempotent — it no-ops when a poller is already alive — but
	// asking first keeps the common re-spawn case free of the pid-dir mkdir and
	// os.Executable lookup, and makes the "already had one" case observable to
	// tests.
	if _, alive := nudgePollerAlive(townRoot, sessionID); alive {
		return nil
	}

	if _, err := nudgePollerStart(townRoot, sessionID); err != nil {
		return fmt.Errorf("starting nudge poller for %s: %w; until one runs, a nudge queued for this "+
			"session is delivered only if the agent takes another turn, which an agent parked at its "+
			"prompt never does", sessionID, err)
	}
	return nil
}
