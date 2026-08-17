package cmd

import (
	"os"

	"github.com/steveyegge/gastown/internal/tmux"
)

// testNudgeHookEnv marks the process as running under test and names the file
// that nudges should be recorded to instead of being delivered.
//
// Aliased from the tmux package rather than restated: the enforcing check now
// lives at the transport (tmux.NudgeSessionWithOpts), and two copies of this
// name could drift apart silently — leaving one layer guarded and the other not,
// which is the shape of the defect this whole mechanism exists to prevent.
const testNudgeHookEnv = tmux.TestNudgeLogEnv

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
// This is now a fast path, not the backstop. The check that actually enforces
// the rule is tmux.NudgeSessionWithOpts's, which no call site can forget; this
// one returns earlier so a guarded test skips the queue writes and idle polling
// on the way there.
func testNudgeHook() (logPath string, inTest bool) {
	return os.LookupEnv(testNudgeHookEnv)
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
