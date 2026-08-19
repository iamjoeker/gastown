package tmux

import (
	"fmt"
	"testing"

	"github.com/steveyegge/gastown/internal/testguard"
)

// The guard vocabulary is aliased from internal/testguard rather than restated.
// The queue in internal/nudge enforces the same rule on the other delivery
// route, and a second copy of these names could drift from this one silently —
// leaving one route guarded and the other not, which is the shape of the defect
// this whole mechanism exists to prevent.
//
// See internal/testguard for what each one means and why.
const (
	// TestNudgeLogEnv marks the process as running under test and names the
	// file that nudges should be recorded to instead of being delivered.
	TestNudgeLogEnv = testguard.LogEnv
	// AllowTestNudgeEnv opts a test in to real tmux delivery. Its value must be
	// the socket name the nudge is being delivered on; any other value —
	// including a bare "1" — authorizes nothing.
	AllowTestNudgeEnv = testguard.AllowEnv
)

// ErrTestNudgeRefused reports that a nudge originating in a test binary was not
// delivered. It is returned rather than silently swallowed: a caller that
// believes it delivered is how 252 synthetic nudges reached six production
// agents under a passing test suite (gt-vmj / gt-kqf).
var ErrTestNudgeRefused = testguard.ErrRefused

// guardTestNudge is the structural backstop against a unit test delivering
// keystrokes into a live agent pane.
//
// It lives here, at the tmux transport, because that is the one place every
// keystroke nudge must pass through. The same check previously existed only in
// internal/cmd, where it covered three call sites out of roughly thirty: the
// nudges to the Mayor and the Deacon that were actually reported originate in
// internal/witness, and a test there could set GT_TEST_NUDGE_LOG — as
// TestNotifyRefineryMergeReady_EmitsChannelEvent does — with no effect
// whatsoever, because the package it named the variable for was not in the call
// path. A guard that must be remembered at each call site has the same shape as
// the defect it is meant to prevent, so this one is remembered nowhere and
// enforced everywhere.
//
// This covers the keystroke route only. The queue is the other one, and it has
// its own guard for the same reason two guards were needed in the first place —
// see internal/nudge.guardTestEnqueue.
//
// Note that directory isolation does not help here and a scratch clone is not a
// mitigation: tmux addresses panes by session name on a shared socket, so a test
// run from anywhere at all reaches hq-mayor. The socket is the axis that
// isolates, which is why authorization is expressed as one.
//
// Returns handled=true when the caller must not proceed to delivery. The error
// is nil when the nudge was recorded via the test hook (an expected, benign
// outcome that callers already treat as success) and ErrTestNudgeRefused when a
// test reached delivery without arranging isolation.
func (t *Tmux) guardTestNudge(session, message string) (handled bool, err error) {
	if logPath, inTest := testguard.TestLog(); inTest {
		testguard.Record(logPath, "nudge:"+session+":"+message+"\n")
		return true, nil
	}

	if !testing.Testing() {
		return false, nil
	}

	// testing.Testing() is true only in a binary built by `go test`, so this
	// cannot fire in production regardless of the environment. That is the
	// point: the signal is a property of the binary, not a variable someone has
	// to set correctly.
	//
	// A socket is never auto-authorized by its name the way the queue's town
	// root is by its location. There is no such thing as an inherently
	// disposable socket name — the live town's is as arbitrary as a test's — so
	// the only safe signal is that a test named the server it started.
	if testguard.Authorized(t.socketName) {
		return false, nil
	}

	return true, fmt.Errorf("%w, or set %s to record instead of delivering",
		testguard.Refusal("nudge session", session, "socket", t.socketName), testguard.LogEnv)
}
