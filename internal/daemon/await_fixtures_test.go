package daemon

import (
	"testing"

	"github.com/steveyegge/gastown/internal/awaitprobe"
)

// Process-table fixtures for the daemon rules that turn on whether an agent has
// a live await. Transcribed from `ps -axwwo args=` on a live Gas Town host.
//
// The probe's own matching rules are exercised in internal/awaitprobe, which
// carries the full fixture set (wrapped forms, defunct processes, near-miss bead
// IDs). These three are the ones the daemon's decisions read: this agent's
// await, and two other agents' awaits that must not satisfy it.
const (
	// The Deacon's await, exactly as the Deacon patrol formula spells it.
	psDeaconAwait = `gt mol step await-signal --agent-bead hq-deacon --backoff-base 60s --backoff-mult 2 --backoff-max 5m`

	// A witness on another rig. Present in the table on every real host, and the
	// reason attribution by agent bead is required rather than matching any
	// await at all.
	psOtherRigWitnessAwait = `timeout 400 gt mol step await-signal --agent-bead bd-beads-witness --backoff-base 60s --backoff-mult 2 --backoff-max 5m`

	// A refinery, which runs await-event rather than await-signal.
	psRefineryAwait = `gt mol step await-event --channel refinery --agent-bead bd-beads-refinery --backoff-base 30s --backoff-mult 2 --backoff-max 15m --cleanup --context-check-interval 5m`
)

// stubProcessTable replaces the host process table for the duration of a test.
// A nil lines with a nil err models a readable but empty table; a non-nil err
// models one that could not be read at all, which the callers must treat as no
// information rather than as an absent await.
func stubProcessTable(t *testing.T, lines []string, err error) func() {
	t.Helper()
	prev := awaitprobe.ListProcessArgs
	awaitprobe.ListProcessArgs = func() ([]string, error) { return lines, err }
	return func() { awaitprobe.ListProcessArgs = prev }
}
