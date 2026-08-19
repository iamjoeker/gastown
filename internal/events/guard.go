package events

import (
	"testing"

	"github.com/steveyegge/gastown/internal/testguard"
)

// guardTestEvents is the structural backstop against a unit test appending to a
// live town's event feed.
//
// The feed is the fourth way a test run inside a town workspace reaches the town,
// after tmux keystrokes, the nudge queue and the town log. It reaches no pane, so
// it is not a delivery — it is worse in a different way. Agents read the feed to
// work out what the town has been doing, and a synthetic event is indistinguishable
// from a real one: the live feed currently holds 81 events from an actor called
// "testrig/refinery", a rig that does not exist.
//
// That is the mechanism behind this bead's own misdiagnosis. gt-vmj was opened on
// a count read out of logs/town.log, and concluded from those lines that a
// delivery path had been reached — when the lines were written on the path that
// intercepted the delivery. Test writes into the records agents reason from do not
// merely add noise; they produce confident, wrong conclusions later.
//
// Same rule and same boundary as the town log's: a test may write to a town it
// owns, and a town under the system temporary directory is one it built.
//
// Returns handled=true when the caller must not write. Note that write() already
// returns nil for "no town root at all", so a refusal here is strictly narrower
// than an outcome callers already handle.
func guardTestEvents(townRoot string) (handled bool, err error) {
	if !testing.Testing() {
		return false, nil
	}

	// testing.Testing() is true only in a binary built by `go test`, so this
	// cannot fire in production regardless of the environment.
	if testguard.Disposable(townRoot) || testguard.Authorized(townRoot) {
		return false, nil
	}

	return true, testguard.Refusal("append to event feed of", townRoot, "town root", townRoot)
}
