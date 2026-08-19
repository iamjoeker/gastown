package daemon

import (
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// Patrol agents — witnesses, refineries, the Deacon — are conversational
// agents, and a conversational agent executes only inside a turn. Its patrol
// loop, including the await-signal/await-event call that would put it back to
// sleep, is a child of that turn. When the turn ends, that process is gone and
// nothing is left running: the agent sits at an empty prompt indefinitely.
// Re-entering the loop is not a capability the agent has, so no amount of
// instruction or discipline in the formula can prevent this — any finite turn
// ends, and nothing re-invokes an ended turn.
//
// The loop therefore needs an owner that outlives a turn. This is that owner.
// The Deacon's manual nudge was already the de facto mechanism and worked every
// time it was applied; this promotes it from a repair someone has to remember
// into the design. It terminates the "who wakes the waker?" recursion because
// the daemon is not itself a turn-taking agent.
//
// Two things it deliberately does NOT key on, both of which were measured and
// found to be confounded:
//
//   - Open patrol-wisp age. It climbs for a stopped agent AND for a healthy
//     one on a long working turn, because the wisp only rotates at the report.
//     Agents have been observed over cap while mid-merge; waking on age alone
//     would have interrupted live work.
//   - Absence of the await process. That separates "not waiting" from
//     "waiting", not stopped from working — a computing agent has no await
//     process either. It is also easy to point at the wrong process name, since
//     witnesses run await-signal and refineries run await-event.
//
// What it keys on instead is the pane, which carries the turn-ended signal
// itself: see [tmux.TurnState].
//
// The PRESENCE of an await is a different signal from its absence, and that one
// is used: see [awaitProbe]. An ended turn is read here as "nothing is pending",
// which is false for an agent whose await was backgrounded — the await outlives
// the turn that started it, and the wake lands on a healthy wait. Two such wakes
// were measured on gastown/witness seven minutes apart with the await pid alive
// at both (gt-ghw7). Presence is decisive where absence is not, so a live await
// suppresses the wake and nothing else about the decision changes.

// patrolWakeConfirmDelay is the pause between the two pane samples that must
// agree before a wake is sent. A pane caught between a tool result and the next
// request renders like a stopped one for a moment; one delay per heartbeat buys
// out that race for the whole set of candidates.
//
// A var so tests can shorten it; nothing in production reassigns it.
var patrolWakeConfirmDelay = 3 * time.Second

// patrolWakeTarget names one agent whose patrol loop the daemon keeps in motion.
type patrolWakeTarget struct {
	role    string // patrol config key: "witness", "refinery", "deacon"
	rig     string // empty for town-level agents
	session string
	// bead is the agent bead ID this role passes to its await step as
	// --agent-bead. It is what makes an await in the process table attributable
	// to this agent rather than to another rig's patrol loop on the same host.
	// Empty means "cannot attribute", and the await check is then skipped.
	bead string
}

func (t patrolWakeTarget) label() string {
	if t.rig == "" {
		return fmt.Sprintf("%s (%s)", t.role, t.session)
	}
	return fmt.Sprintf("%s/%s (%s)", t.rig, t.role, t.session)
}

// awaitStep names the formula step the role ends its turn inside when the loop
// is running correctly. Witnesses and refineries do NOT share a loop, and
// naming the wrong one in a wake message would send an agent looking for a
// command its formula does not contain.
func (t patrolWakeTarget) awaitStep() string {
	switch t.role {
	case "refinery":
		return "gt mol step await-event"
	case "witness":
		return "gt mol step await-signal"
	default:
		return "your patrol formula's next step"
	}
}

// wakeStoppedPatrolAgents re-invokes patrol agents whose turn has ended.
//
// The Mayor is deliberately absent from the target set: it sits at a prompt
// waiting for its operator, so an ended turn there is the resting state rather
// than a fault. Polecats are absent for the opposite reason — an idle polecat
// is handled by reapIdlePolecats, which ends the session instead of extending it.
func (d *Daemon) wakeStoppedPatrolAgents() {
	daemonCfg := d.loadOperationalConfig().GetDaemonConfig()
	if !daemonCfg.PatrolWakeEnabledV() {
		return
	}
	if d.tmux == nil || !d.tmux.IsAvailable() {
		return
	}

	d.wakeStoppedTargets(d.patrolWakeTargets())
}

// wakeStoppedTargets samples each target's pane and wakes the ones whose turn
// has ended. Split from wakeStoppedPatrolAgents so the decision can be
// exercised against known panes without going through rig discovery.
func (d *Daemon) wakeStoppedTargets(targets []patrolWakeTarget) {
	if len(targets) == 0 {
		return
	}

	var candidates []patrolWakeTarget
	for _, tgt := range targets {
		switch state := d.tmux.TurnState(tgt.session); state {
		case tmux.TurnEnded:
			candidates = append(candidates, tgt)
		case tmux.TurnStranded:
			// Real text is sitting unsent in the composer. Typing a wake into
			// it would append to that text rather than replace it, producing a
			// garbled submission — and the stranded text is itself a defect
			// worth seeing rather than papering over.
			d.logger.Printf("patrol-wake: %s has unsent text in its composer, not waking", tgt.label())
		}
	}
	if len(candidates) == 0 {
		return
	}

	time.Sleep(patrolWakeConfirmDelay)

	// One snapshot of the process table for the whole sweep, taken after the
	// confirm delay so it is as close in time to the wake decision as possible.
	probe := &awaitProbe{}

	for _, tgt := range candidates {
		if state := d.tmux.TurnState(tgt.session); state != tmux.TurnEnded {
			d.logger.Printf("patrol-wake: %s read %s on the second sample, not waking", tgt.label(), state)
			continue
		}
		// An ended turn does not mean nothing is pending: an await that was
		// backgrounded outlives the turn that launched it, and waking then
		// interrupts a healthy wait. Only a confirmed live await suppresses the
		// wake — awaitUnknown leaves the pre-check behavior intact, because a
		// wake that was not needed costs one cycle whereas a wake that was
		// needed and withheld costs the loop.
		if state := probe.state(tgt.bead); state == awaitPending {
			d.logger.Printf("patrol-wake: %s has a live await (%s), not waking", tgt.label(), tgt.bead)
			continue
		}
		d.wakePatrolAgent(tgt)
	}
}

// patrolWakeTargets lists the live patrol sessions eligible for a wake.
// Roles with their patrol disabled are skipped entirely: the daemon must not
// restart a loop an operator turned off.
func (d *Daemon) patrolWakeTargets() []patrolWakeTarget {
	var targets []patrolWakeTarget

	if d.isPatrolActive("deacon") {
		targets = append(targets, patrolWakeTarget{
			role:    "deacon",
			session: d.getDeaconSessionName(),
			bead:    beads.DeaconBeadIDTown(),
		})
	}

	if d.isPatrolActive("witness") {
		for _, rigName := range d.getPatrolRigs("witness") {
			prefix := session.PrefixFor(rigName)
			targets = append(targets, patrolWakeTarget{
				role:    "witness",
				rig:     rigName,
				session: session.WitnessSessionName(prefix),
				bead:    beads.WitnessBeadIDWithPrefix(prefix, rigName),
			})
		}
	}

	if d.isPatrolActive("refinery") {
		for _, rigName := range d.getPatrolRigs("refinery") {
			prefix := session.PrefixFor(rigName)
			targets = append(targets, patrolWakeTarget{
				role:    "refinery",
				rig:     rigName,
				session: session.RefinerySessionName(prefix),
				bead:    beads.RefineryBeadIDWithPrefix(prefix, rigName),
			})
		}
	}

	return targets
}

// wakePatrolAgent sends one wake, subject to the per-session cooldown and the
// usage-limit check.
func (d *Daemon) wakePatrolAgent(tgt patrolWakeTarget) {
	cooldown := d.loadOperationalConfig().GetDaemonConfig().PatrolWakeCooldownD()
	now := time.Now()
	if last, ok := d.lastPatrolWake[tgt.session]; ok && now.Sub(last) < cooldown {
		d.logger.Printf("patrol-wake: %s woken %s ago, within cooldown (%s), skipping",
			tgt.label(), now.Sub(last).Round(time.Second), cooldown)
		return
	}

	// A session paused at a Claude usage-limit prompt reads as an ended turn
	// and is not one: the agent cannot proceed until quota_dog rotates
	// accounts, so a wake is both useless and misleading in the log.
	if pane, err := d.tmux.CapturePane(tgt.session, 30); err == nil && IsClaudeUsageLimit(pane) {
		d.logger.Printf("patrol-wake: %s is paused at a Claude usage limit, not waking", tgt.label())
		return
	}

	msg := fmt.Sprintf("PATROL WAKE: your turn ended at an empty prompt and nothing re-invokes an ended turn. "+
		"Resume your patrol loop now, and end this turn inside %s rather than after a report or a summary.",
		tgt.awaitStep())

	if err := d.tmux.NudgeSession(tgt.session, msg); err != nil {
		// No cooldown is recorded on failure: a wake that did not land should
		// be retried on the next heartbeat, not suppressed as though it had.
		d.logger.Printf("patrol-wake: failed to wake %s: %v", tgt.label(), err)
		return
	}

	if d.lastPatrolWake == nil {
		d.lastPatrolWake = make(map[string]time.Time)
	}
	d.lastPatrolWake[tgt.session] = now
	d.logger.Printf("patrol-wake: woke %s (turn had ended at an empty prompt)", tgt.label())
}
