package cmd

import (
	"github.com/steveyegge/gastown/internal/testguard"
)

// testNudgeHookEnv marks the process as running under test and names the file
// that nudges should be recorded to instead of being delivered.
//
// Aliased from internal/testguard rather than restated: the enforcing checks
// now live at the two transports (tmux.NudgeSessionWithOpts for keystrokes,
// nudge.Enqueue for the queue), and a third copy of this name could drift apart
// from theirs silently — leaving one layer guarded and the others not, which is
// the shape of the defect this whole mechanism exists to prevent.
const testNudgeHookEnv = testguard.LogEnv

// testNudgeHook reports whether the nudge test hook is active, and where (if
// anywhere) nudges should be logged.
//
// PRESENCE of the variable is the signal, not its value. A test that sets it to
// the empty string is still a test and must not reach the real transports. That
// distinction is load-bearing: this check was once `os.Getenv(...) != ""`, and
// TestNudgeRefineryNoOpWithoutLog deliberately blanked the variable to exercise
// the tmux path. The empty value failed the guard, so every run of
// `go test ./internal/cmd/...` inside the town workspace fell through to a live
// channelevents emit and woke every refinery in the town into a full patrol.
//
// An empty path therefore means "test mode, nothing to record" — the caller
// still returns without delivering.
//
// This is a fast path, not the backstop. The checks that actually enforce the
// rule are the transports', which no call site can forget; this one returns
// earlier so a guarded test skips the queue writes and idle polling on the way
// there.
func testNudgeHook() (logPath string, inTest bool) {
	return testguard.TestLog()
}

// writeTestNudgeLog appends an entry to the test nudge log, if one was named.
func writeTestNudgeLog(logPath, entry string) {
	testguard.Record(logPath, entry)
}
