package daemon

import (
	"fmt"
	"strings"
	"testing"
)

// Command lines transcribed from `ps -axwwo args=` on a live Gas Town host.
// The wrapper form is what Claude's shell tool builds around the step; the bare
// form is the process the wrapper execs. Both appear in the table at once, and
// the probe must read either as evidence of the same pending wait.
const (
	// The Deacon's await, foreground, exactly as the Deacon formula spells it.
	psDeaconAwait = `gt mol step await-signal --agent-bead hq-deacon --backoff-base 60s --backoff-mult 2 --backoff-max 5m`

	// A witness on another rig. Present in the table on every real host and the
	// reason attribution by agent bead is required rather than matching any
	// await at all.
	psOtherRigWitnessAwait = `timeout 400 gt mol step await-signal --agent-bead bd-beads-witness --backoff-base 60s --backoff-mult 2 --backoff-max 5m`

	// A refinery, which runs await-event rather than await-signal.
	psRefineryAwait = `gt mol step await-event --channel refinery --agent-bead bd-beads-refinery --backoff-base 30s --backoff-mult 2 --backoff-max 15m --cleanup --context-check-interval 5m`

	// The shell wrapper, with the step quoted inside an eval.
	psWrappedDeaconAwait = `/usr/bin/zsh -c source /home/jkerby/.claude/shell-snapshots/snapshot-zsh-1787023656482-mq22vn.sh 2>/dev/null || true && eval 'cd /home/jkerby/src/gt && gt mol step await-signal --agent-bead hq-deacon --backoff-base 60s --backoff-mult 2 --backoff-max 5m 2>&1 | tail -4' < /dev/null && pwd -P >| /tmp/claude-1cf9-cwd`

	// Noise that must not match: the agent's own bead appears, but no await.
	psDeaconHeartbeat = `gt deacon heartbeat pre-await checkpoint --agent-bead hq-deacon`

	// A reaped await. Its arguments are gone, so it cannot be mistaken for one
	// that is still waiting.
	psDefunctAwait = `[gt] <defunct>`
)

// The probe's whole job is telling a pending wait from an absent one for a
// NAMED agent. Getting that attribution wrong in either direction reintroduces
// the bug: a cross-rig match makes a parked Deacon look like it is waiting.
func TestAwaitStateFromProcesses(t *testing.T) {
	tests := []struct {
		name  string
		bead  string
		lines []string
		want  awaitState
	}{
		{
			name:  "the agent's own await is pending",
			bead:  "hq-deacon",
			lines: []string{psDeaconAwait},
			want:  awaitPending,
		},
		{
			name:  "the wrapper alone counts — same wait, one process up",
			bead:  "hq-deacon",
			lines: []string{psWrappedDeaconAwait},
			want:  awaitPending,
		},
		{
			name:  "wrapper and child together are still one pending wait",
			bead:  "hq-deacon",
			lines: []string{psWrappedDeaconAwait, psDeaconAwait},
			want:  awaitPending,
		},
		{
			name:  "an await wrapped in timeout still counts",
			bead:  "bd-beads-witness",
			lines: []string{psOtherRigWitnessAwait},
			want:  awaitPending,
		},
		{
			name:  "await-event counts for refineries",
			bead:  "bd-beads-refinery",
			lines: []string{psRefineryAwait},
			want:  awaitPending,
		},
		{
			// The failure this whole bead is about: a table full of other
			// agents' awaits, none of them this one's.
			name:  "another rig's await is not this agent's",
			bead:  "hq-deacon",
			lines: []string{psOtherRigWitnessAwait, psRefineryAwait},
			want:  awaitAbsent,
		},
		{
			name:  "empty table",
			bead:  "hq-deacon",
			lines: nil,
			want:  awaitAbsent,
		},
		{
			name:  "the bead appearing without an await is not a wait",
			bead:  "hq-deacon",
			lines: []string{psDeaconHeartbeat},
			want:  awaitAbsent,
		},
		{
			name:  "a defunct await is not waiting",
			bead:  "hq-deacon",
			lines: []string{psDefunctAwait},
			want:  awaitAbsent,
		},
		{
			// Prefix matching would make gt-gastown-witness satisfy a probe for
			// gt-gastown-witness-two and vice versa.
			name:  "a longer bead ID sharing this one's prefix does not match",
			bead:  "hq-deacon",
			lines: []string{`gt mol step await-signal --agent-bead hq-deacon-shadow --backoff-base 60s`},
			want:  awaitAbsent,
		},
		{
			name:  "--agent-bead=value form",
			bead:  "hq-deacon",
			lines: []string{`gt mol step await-signal --agent-bead=hq-deacon --backoff-base 60s`},
			want:  awaitPending,
		},
		{
			// The flag must belong to the await, not to something chained after
			// it on the same command line.
			name:  "a bead flag belonging to a different command does not match",
			bead:  "hq-deacon",
			lines: []string{`sh -c gt deacon heartbeat --agent-bead hq-deacon && gt mol step await-signal --agent-bead bd-beads-witness`},
			want:  awaitAbsent,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := awaitStateFromProcesses(tc.bead, tc.lines); got != tc.want {
				t.Errorf("awaitStateFromProcesses(%q) = %v, want %v", tc.bead, got, tc.want)
			}
		})
	}
}

// An unreadable process table must be distinguishable from a readable one that
// holds no await. Collapsing the two is what turns a Windows host, or a ps that
// failed, into a daemon that nudges every patrol agent on every heartbeat.
func TestAwaitProbeUnreadableTableIsUnknown(t *testing.T) {
	restore := stubProcessTable(t, nil, fmt.Errorf("ps: command not found"))
	defer restore()

	if got := (&awaitProbe{}).state("hq-deacon"); got != awaitUnknown {
		t.Errorf("state with an unreadable process table = %v, want %v", got, awaitUnknown)
	}
}

// Without an agent bead there is nothing to attribute an await to, and any
// await on the host would satisfy the probe. That must read as no information
// rather than as a pending wait.
func TestAwaitProbeEmptyBeadIsUnknown(t *testing.T) {
	restore := stubProcessTable(t, []string{psDeaconAwait}, nil)
	defer restore()

	if got := (&awaitProbe{}).state(""); got != awaitUnknown {
		t.Errorf("state with an empty agent bead = %v, want %v", got, awaitUnknown)
	}
}

// One sweep must cost one ps call however many targets it probes: the daemon
// runs this on every heartbeat, and a call per target scales with rig count.
func TestAwaitProbeSnapshotsOnce(t *testing.T) {
	calls := 0
	prev := listProcessArgs
	listProcessArgs = func() ([]string, error) {
		calls++
		return []string{psDeaconAwait}, nil
	}
	defer func() { listProcessArgs = prev }()

	p := &awaitProbe{}
	for _, bead := range []string{"hq-deacon", "bd-beads-witness", "bd-beads-refinery"} {
		p.state(bead)
	}

	if calls != 1 {
		t.Errorf("process table read %d times for one probe, want 1", calls)
	}
}

// The live probe must actually find a live await. Everything above runs against
// transcribed fixtures, which cannot catch a ps invocation that returns nothing
// usable on this host — the failure mode where every probe silently reads
// absent and the daemon nudges healthy agents forever.
func TestListProcessArgsSeesThisProcess(t *testing.T) {
	lines, err := listProcessArgs()
	if err != nil {
		t.Skipf("no readable process table on this host: %v", err)
	}
	if len(lines) < 2 {
		t.Fatalf("process table returned %d lines, want the host's full process list", len(lines))
	}

	// This test binary is itself in the table, under a name ending in ".test".
	found := false
	for _, l := range lines {
		if strings.Contains(l, ".test") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("process table does not contain this test binary; ps output is not what the probe expects:\n%s",
			strings.Join(lines[:min(len(lines), 5)], "\n"))
	}
}

// stubProcessTable replaces the host process table for the duration of a test.
func stubProcessTable(t *testing.T, lines []string, err error) func() {
	t.Helper()
	prev := listProcessArgs
	listProcessArgs = func() ([]string, error) { return lines, err }
	return func() { listProcessArgs = prev }
}
