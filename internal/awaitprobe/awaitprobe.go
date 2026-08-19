// Package awaitprobe reports whether a patrol agent has a live await process
// waiting on its behalf, read from the host process table.
//
// It lives in its own package because two very different callers need the same
// answer: the daemon, which uses it to decide whether waking an agent would
// interrupt a healthy wait, and `gt deacon status`, which uses it to tell a
// Deacon parked in await-signal from one that stopped mid-patrol
// (internal/deacon.EvaluateHealth).
package awaitprobe

import (
	"os/exec"
	"strings"
	"sync"
)

// A patrol agent's loop sleeps inside `gt mol step await-signal` (the Deacon and
// witnesses) or `gt mol step await-event` (refineries). Several of the daemon's
// rules turn on whether such a wait is pending, and every one of them used to
// answer that question by inference rather than by looking:
//
//   - The skip rules — "await-signal will fire naturally" on the nudge path and
//     "Deacon is healthy and no active work in flight" on the boot path — assume
//     a wait is pending and decline to wake. For an agent that parked with no
//     await the premise is false: nothing is pending, so nothing fires, and the
//     agent stays parked. When that agent is the Deacon it is the head of the
//     wake chain, so the whole chain stops with no error logged (gt-ghw7).
//   - patrol-wake reads an ended turn as "nothing is pending" and wakes. An
//     await that was backgrounded outlives the turn that started it, so the wake
//     interrupts a healthy wait.
//
// Both directions are the same blind spot: liveness is read off the pane and off
// the absence of work, never off the process table. One check answers both.
//
// What this deliberately does NOT claim: the ABSENCE of an await does not mean
// an agent is stopped. A computing agent has no await process either, which is
// why [tmux.TurnState] remains the stopped-vs-working discriminator. Presence is
// the decisive half — an agent with a live await is waiting, whatever its pane
// says — and on the daemon's paths absence only ever suppresses an action,
// never triggers one.
//
// Absence carries weight in exactly one place, and only alongside the two
// signals that cover what it cannot see: internal/deacon.EvaluateHealth reports
// a Deacon wedged when its heartbeat has frozen past the stale threshold AND its
// pane shows the turn has ended AND no await is pending. There the pane rules
// out the computing agent this probe would otherwise mistake for a stopped one.

// State is what the process table says about one agent's pending await.
type State int

const (
	// StateUnknown means the process table could not be read (no ps, or the
	// command failed). Every caller must treat this as "no information" and fall
	// back to the behavior it had before this check existed.
	StateUnknown State = iota
	// StateAbsent means the table was read and holds no await for this agent.
	StateAbsent
	// StatePending means a live await process is waiting on this agent's behalf.
	StatePending
)

func (s State) String() string {
	switch s {
	case StateAbsent:
		return "absent"
	case StatePending:
		return "pending"
	default:
		return "unknown"
	}
}

// awaitCommands are the molecule steps a patrol agent ends its turn inside.
// Witnesses and the Deacon run await-signal; refineries run await-event.
var awaitCommands = []string{"await-signal", "await-event"}

// agentBeadFlag is the flag every patrol formula passes to its await step. Its
// value is the agent bead ID, which is unique per agent and is therefore what
// makes an await in the table attributable to one agent rather than to any of
// the other rigs' patrol loops running on the same host.
//
// An await launched WITHOUT this flag — the bare `--timeout 60s` form, which an
// out-of-date formula overlay could still be running — cannot be attributed and
// reads as absent. That errs toward acting: the agent gets a nudge that
// interrupts one backoff and then continues, which is the recoverable direction.
const agentBeadFlag = "--agent-bead"

// ListProcessArgs returns the full command line of every process on the host.
//
// `-ww` disables the width truncation ps applies by default; without it a long
// await command line is cut off before its --agent-bead argument and every
// probe reads absent. `-axo args=` is the same portable spelling the rest of
// the tree uses for process snapshots (see getAllDescendants).
// A var, not a func, so tests here and in the daemon can substitute a
// transcribed process table. Nothing in production reassigns it.
var ListProcessArgs = func() ([]string, error) {
	out, err := exec.Command("ps", "-axwwo", "args=").Output()
	if err != nil {
		return nil, err
	}
	return strings.Split(string(out), "\n"), nil
}

// Probe answers "is this agent's await alive" from a single snapshot of the
// process table. The snapshot is taken once on first use and reused for the rest
// of the probe's life, so a sweep over every patrol target costs one ps call
// rather than one per target. Create a fresh probe per sweep — a cached table
// goes stale the moment an await starts or exits.
type Probe struct {
	once  sync.Once
	lines []string
	err   error
}

// State reports whether an await is pending for the given agent bead.
// An empty bead returns StateUnknown: without an ID there is nothing to
// attribute an await to, and any await on the host would match.
func (p *Probe) State(agentBead string) State {
	if agentBead == "" {
		return StateUnknown
	}
	p.once.Do(func() { p.lines, p.err = ListProcessArgs() })
	if p.err != nil {
		return StateUnknown
	}
	return stateFromProcesses(agentBead, p.lines)
}

// stateFromProcesses is the matching rule, split out so it can be exercised
// against transcribed ps output without a live process table.
//
// A line matches when an await command name is followed, later on the same
// command line, by `--agent-bead <bead>` (or `--agent-bead=<bead>`). Requiring
// the flag to come AFTER the await keeps a shell wrapper that chains an await to
// some other bead-scoped command from matching the wrong agent. The wrapper
// process itself matching alongside its child is fine and expected: both are
// evidence the same wait is in flight.
func stateFromProcesses(agentBead string, lines []string) State {
	for _, line := range lines {
		if commandLineAwaitsFor(agentBead, line) {
			return StatePending
		}
	}
	return StateAbsent
}

func commandLineAwaitsFor(agentBead, line string) bool {
	// A reaped process renders as "[gt] <defunct>" with its arguments gone, so
	// it cannot match — which is correct, a defunct await is not waiting.
	fields := strings.Fields(line)
	sawAwait := false
	for i, f := range fields {
		if !sawAwait {
			if isAwaitCommand(f) {
				sawAwait = true
			}
			continue
		}
		if f == agentBeadFlag {
			if i+1 < len(fields) && strings.Trim(fields[i+1], `'"`) == agentBead {
				return true
			}
			continue
		}
		if v, ok := strings.CutPrefix(f, agentBeadFlag+"="); ok && strings.Trim(v, `'"`) == agentBead {
			return true
		}
	}
	return false
}

// isAwaitCommand matches the await step's name as its own argument. The step is
// quoted in the shell wrapper Claude builds around it, so surrounding quotes are
// stripped before comparing.
func isAwaitCommand(field string) bool {
	f := strings.Trim(field, `'"`)
	for _, name := range awaitCommands {
		if f == name {
			return true
		}
	}
	return false
}
