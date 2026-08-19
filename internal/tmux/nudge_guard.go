package tmux

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

// TestNudgeLogEnv marks the process as running under test and names the file
// that nudges should be recorded to instead of being delivered.
//
// PRESENCE of the variable is the signal, not its value. A test that sets it to
// the empty string is still a test and must not reach a real pane. That
// distinction is load-bearing: the equivalent check in internal/cmd was once
// `os.Getenv(...) != ""`, and a test that deliberately blanked the variable to
// exercise the tmux path fell straight through to live delivery.
//
// An empty path means "test mode, nothing to record" — delivery is still
// refused.
const TestNudgeLogEnv = "GT_TEST_NUDGE_LOG"

// AllowTestNudgeEnv opts a test in to real tmux nudge delivery. Its value must
// be the socket name the nudge is being delivered on; any other value — including
// a bare "1" — authorizes nothing.
//
// Naming the socket rather than setting a boolean is deliberate. A test that
// genuinely needs to exercise delivery owns an isolated tmux server and can name
// it (see uniqueSocketName in socket_guard_unix_test.go). A test that reaches a
// delivery path by accident is running against whatever socket the production
// code resolved — the live town socket — which it never named, so it is refused.
// Authorization cannot be granted by remembering a flag; it is granted only by
// having built the isolation the flag describes.
const AllowTestNudgeEnv = "GT_ALLOW_TEST_NUDGE"

// ErrTestNudgeRefused reports that a nudge originating in a test binary was not
// delivered. It is returned rather than silently swallowed: a caller that
// believes it delivered is how 252 synthetic nudges reached six production
// agents under a passing test suite (gt-vmj / gt-kqf).
var ErrTestNudgeRefused = errors.New("refusing to deliver a nudge from a test binary")

// guardTestNudge is the single structural backstop against a unit test
// delivering keystrokes into a live agent pane.
//
// It lives here, at the tmux transport, because that is the one place every
// nudge must pass through. The same check previously existed only in
// internal/cmd, where it covered three call sites out of roughly thirty: the
// nudges to the Mayor and the Deacon that were actually reported originate in
// internal/witness, and a test there could set GT_TEST_NUDGE_LOG — as
// TestNotifyRefineryMergeReady_EmitsChannelEvent does — with no effect
// whatsoever, because the package it named the variable for was not in the call
// path. A guard that must be remembered at each call site has the same shape as
// the defect it is meant to prevent, so this one is remembered nowhere and
// enforced everywhere.
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
	if logPath, inTest := os.LookupEnv(TestNudgeLogEnv); inTest {
		writeTestNudgeLog(logPath, fmt.Sprintf("nudge:%s:%s\n", session, message))
		return true, nil
	}

	if !testing.Testing() {
		return false, nil
	}

	// testing.Testing() is true only in a binary built by `go test`, so this
	// cannot fire in production regardless of the environment. That is the
	// point: the signal is a property of the binary, not a variable someone has
	// to set correctly.
	if allowed := os.Getenv(AllowTestNudgeEnv); allowed != "" && allowed == t.socketName {
		return false, nil
	}

	return true, fmt.Errorf("%w: session %q on socket %s; set %s to this socket name if the test owns an isolated tmux server, or set %s to record instead of deliver",
		ErrTestNudgeRefused, session, describeSocket(t.socketName), AllowTestNudgeEnv, TestNudgeLogEnv)
}

// describeSocket renders a socket name for error messages, distinguishing the
// empty name (the user's own default tmux server — never a correct target for a
// test) from a named one.
func describeSocket(socket string) string {
	if socket == "" {
		return "(default server)"
	}
	return fmt.Sprintf("%q", socket)
}

// writeTestNudgeLog appends an entry to the test nudge log, if one was named.
// Failures are ignored: the log exists for test observability, and losing an
// entry must never change the behavior of the code under test.
func writeTestNudgeLog(logPath, entry string) {
	if logPath == "" {
		return
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(entry)
	_ = f.Close()
}
