package nudge

import (
	"fmt"
	"testing"

	"github.com/steveyegge/gastown/internal/testguard"
)

// guardTestEnqueue is the structural backstop against a unit test putting a
// message in front of a live agent by way of the queue.
//
// The queue is a delivery path in its own right, not a staging area. A file
// under <townRoot>/.runtime/nudge_queue is drained by the target agent's
// UserPromptSubmit hook and injected into its next turn, so writing one into the
// live town is a delivery — just a deferred one, made by a different mechanism
// than the keystrokes that internal/tmux guards.
//
// That distinction is why this exists. gt-vmj was fixed at the tmux transport,
// and `gt nudge` does not use that transport by default: --mode defaults to
// wait-idle, whose ordinary outcome for a busy agent is nudge.Enqueue. A test
// binary that reaches deliverNudge without the log hook set therefore wrote into
// the live queue, returned nil, and had its caller report success and append to
// the town log — with the tmux guard entirely bypassed, because no keystroke was
// ever sent.
//
// The town root is this route's isolation boundary, the way the socket is the
// tmux route's. Unlike a socket name, it carries the isolation in the path
// itself, so a test that already built a temporary town is recognized rather
// than required to declare itself, and no existing test has to remember
// anything. AllowEnv remains available for a test whose town root is
// deliberately somewhere else.
//
// Returns handled=true when the caller must not write to the queue. The error is
// nil when the nudge was recorded via the test hook (an expected outcome that
// callers treat as success) and testguard.ErrRefused when a test reached the
// queue without arranging isolation.
func guardTestEnqueue(townRoot, session, message string) (handled bool, err error) {
	// Ownership is checked before the log hook, which is the opposite of the
	// order the tmux guard uses, because the queue is also a fixture. A test that
	// builds a queue to exercise Drain, Requeue or the watcher is not delivering
	// anything — nothing polls a temporary town — and recording those writes
	// instead of performing them would break the setup of tests that have nothing
	// to do with delivery. The tmux transport has no equivalent case: there is no
	// such thing as writing keystrokes to a pane for later inspection.
	if testguard.Disposable(townRoot) || testguard.Authorized(townRoot) {
		return false, nil
	}

	if logPath, inTest := testguard.TestLog(); inTest {
		testguard.Record(logPath, "queue:"+session+":"+message+"\n")
		return true, nil
	}

	// testing.Testing() is true only in a binary built by `go test`, so the
	// refusal below cannot fire in production regardless of the environment.
	if !testing.Testing() {
		return false, nil
	}

	return true, fmt.Errorf("%w, or set %s to record instead of queueing",
		testguard.Refusal("queue nudge for", session, "town root", townRoot), testguard.LogEnv)
}
