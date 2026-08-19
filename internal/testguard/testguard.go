// Package testguard holds the vocabulary shared by every guard that stops a
// test binary from acting on a live Gas Town.
//
// A test run inside a town workspace can reach the town by more routes than the
// one anybody is looking at. gt-vmj is what that costs: nudges reported as
// delivered to six production agents, including the Mayor and the Deacon, for
// five and three quarter hours, under a suite that stayed green throughout.
// Four routes turned out to be involved:
//
//   - keystrokes into a tmux pane (internal/tmux)
//   - a file in the nudge queue that the agent's UserPromptSubmit hook drains
//     (internal/nudge)
//   - the town log those were recorded in (internal/townlog)
//   - the event feed agents read to reconstruct what the town has been doing
//     (internal/events)
//
// Closing any one of them left the rest open, and the last two are the ones that
// made the bead's own diagnosis wrong: the reported count was read out of the
// town log, whose entries are written whether or not anything was delivered.
//
// Each route keeps its own policy, because what counts as isolation differs: the
// tmux transport isolates by socket, the other three by town root. What they must
// not do is keep private copies of the environment variable names, the sentinel
// error, or the rule for what makes a path disposable. Those lived in two places
// once already, with a comment in each warning they could drift apart silently
// and leave one layer guarded and the next not. They live here instead.
package testguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LogEnv marks the process as running under test and names the file that nudges
// should be recorded to instead of being delivered.
//
// PRESENCE of the variable is the signal, not its value. A test that sets it to
// the empty string is still a test and must not reach a real agent. That
// distinction is load-bearing: the check was once `os.Getenv(...) != ""`, and a
// test that deliberately blanked the variable to exercise the transport fell
// straight through to live delivery (gt-5my).
//
// An empty path means "test mode, nothing to record" — delivery is still
// refused.
const LogEnv = "GT_TEST_NUDGE_LOG"

// AllowEnv opts a test in to acting on live town state. Its value must be the
// isolation boundary the action crosses — the tmux socket name for keystroke
// delivery, the town root for the nudge queue and the town log. Any other value,
// including a bare "1", authorizes nothing.
//
// Naming the boundary rather than setting a boolean is deliberate. A test that
// genuinely needs to exercise the real thing owns an isolated server or town and
// can name it. A test that reaches it by accident is acting on whatever the
// production code resolved — the live socket, the live town — which it never
// named, so it is refused. Authorization cannot be granted by remembering a
// flag; it is granted only by having built the isolation the flag describes.
const AllowEnv = "GT_ALLOW_TEST_NUDGE"

// AllowDoltEnv opts a test process in to the production Dolt server, the fifth
// route into a live town (gt-wz3y). It follows the same rule as AllowEnv: the
// value must name the boundary being crossed — here the port — so a bare "1"
// authorizes nothing.
//
// It is a separate variable rather than a reuse of AllowEnv because the two
// grant different things and are held for different durations. AllowEnv is set
// by a test that owns a socket or a fixture town for the length of one action;
// this one is set by hand, for a whole process, by an operator running a smoke
// check against the real server. Neither should be able to grant the other by
// accident.
//
// Unlike the other routes, the Dolt guard's ordinary authorization is not this
// variable at all: a test that owns a server names its port in GT_DOLT_PORT and
// the guard leaves it alone. That is the same principle — isolation is granted
// by having built it, not by remembering a flag — expressed in the variable the
// production resolvers already read.
const AllowDoltEnv = "GT_ALLOW_TEST_DOLT"

// ErrRefused reports that an action originating in a test binary did not touch
// live town state. It is returned rather than silently swallowed: a caller that
// believes it succeeded is how the gt-vmj deliveries hid under a green suite.
var ErrRefused = errors.New("refusing to act on live Gas Town state from a test binary")

// TestLog reports whether the test hook is active, and where (if anywhere)
// nudges should be recorded.
func TestLog() (logPath string, inTest bool) {
	return os.LookupEnv(LogEnv)
}

// Authorized reports whether AllowEnv names this exact isolation boundary.
//
// The empty boundary is never authorized: it means the caller never chose one,
// and an unnamed default is precisely the live target a stray test reaches.
func Authorized(boundary string) bool {
	return AuthorizedBy(AllowEnv, boundary)
}

// AuthorizedBy is Authorized against a named variable, for routes that keep
// their own opt-in (see AllowDoltEnv). The rule is the same one in both cases
// and lives here once so the two cannot drift apart.
func AuthorizedBy(env, boundary string) bool {
	if boundary == "" {
		return false
	}
	allowed, ok := os.LookupEnv(env)
	return ok && allowed == boundary
}

// Disposable reports whether a path belongs to a test fixture rather than to a
// town agents live in.
//
// The test is location: t.TempDir() roots every fixture town under the system
// temporary directory, and a real town is never there — it is a git checkout in
// the user's home. A queue or a log under TMPDIR has no agent, no poller and no
// reader behind it, so it reaches nobody by construction. That makes the
// isolation recognizable rather than something each test has to declare, which
// is the whole difference between this and a guard that must be remembered.
//
// The empty path is never disposable. It is what workspace lookup returns when
// it found no town, and joining it yields a relative path in whatever directory
// the caller happens to be running in.
func Disposable(path string) bool {
	if path == "" {
		return false
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// A path that does not exist yet cannot be resolved, so fall back to the
		// lexical form. The caller would create it, and a location under TMPDIR
		// is as disposable before it exists as after.
		resolved = filepath.Clean(path)
	}
	if !filepath.IsAbs(resolved) {
		return false
	}
	return resolved == tmp || strings.HasPrefix(resolved, tmp+string(filepath.Separator))
}

// Refusal builds the error returned when a test reaches live state without
// arranging isolation.
//
// action names what was attempted ("nudge session", "queue nudge for"), target
// what it was attempted on, and boundaryKind/boundary the isolation the caller
// would have had to own. The kind is spelled out because the value to put in
// AllowEnv is the boundary, never the target: for a tmux nudge that is the
// socket, not the session being nudged, and a message that says otherwise sends
// the reader to authorize the one thing that authorizes nothing.
func Refusal(action, target, boundaryKind, boundary string) error {
	return fmt.Errorf("%w: %s %q via %s %s; set %s to that %s if this test owns it",
		ErrRefused, action, target, boundaryKind, describeBoundary(boundary), AllowEnv, boundaryKind)
}

// describeBoundary renders an isolation boundary for error messages,
// distinguishing the empty one (whatever the production code resolved — never a
// correct target for a test) from a named one.
func describeBoundary(boundary string) string {
	if boundary == "" {
		return "(unnamed default)"
	}
	return fmt.Sprintf("%q", boundary)
}

// Record appends an entry to the test nudge log, if one was named. Failures are
// ignored: the log exists for test observability, and losing an entry must never
// change the behavior of the code under test.
func Record(logPath, entry string) {
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
