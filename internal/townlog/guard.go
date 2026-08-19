package townlog

import (
	"testing"

	"github.com/steveyegge/gastown/internal/testguard"
)

// guardTestTownLog is the structural backstop against a unit test writing agent
// lifecycle events into a live town's log.
//
// This is the route the gt-vmj evidence was actually made of. The count that
// opened the bead — 252 deliveries, evenly split 42 each across six live targets
// — was read out of /home/jkerby/src/gt/logs/town.log, and every one of those
// lines was written here. The nudge guards in internal/tmux and internal/nudge
// stop the delivery, but they stop it by *recording* it, and recording returns
// nil; cmd.runNudge cannot tell that outcome apart from a real delivery, so it
// goes on to announce success and call LogNudge. The result is a town log that
// says six production agents were nudged when nothing was sent to any of them.
//
// Which is worse than a cosmetic defect. The town log is what agents read to
// reconstruct what happened, and the entries are indistinguishable from real
// traffic — the Mayor's investigation reasonably concluded from them that the
// delivery path had been reached, and re-verified the count rather than the
// route. Any guard downstream of this one leaves the evidence trail lying.
//
// The rule is the same one the other two routes use, on the same boundary as the
// queue's: a test may write to a town it owns, and a town under the system
// temporary directory is one it built. Nothing else. The whole Logger is guarded
// rather than the nudge event alone — partial coverage is what this bead is
// about, and a test that stamps a spawn or a kill into the live log is telling
// the same lie about a different event.
//
// Returns handled=true when the caller must not write. The error is
// testguard.ErrRefused: callers of the town log almost all discard their error
// already, so returning one costs nothing, while returning nil would leave a
// test believing the town knows something it does not.
func guardTestTownLog(townRoot, logPath string) (handled bool, err error) {
	if !testing.Testing() {
		return false, nil
	}

	// testing.Testing() is true only in a binary built by `go test`, so nothing
	// below can fire in production regardless of the environment.
	if testguard.Disposable(logPath) || testguard.Authorized(townRoot) {
		return false, nil
	}

	return true, testguard.Refusal("write town log", logPath, "town root", townRoot)
}
