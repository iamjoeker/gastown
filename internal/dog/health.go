package dog

import (
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
)

// sessionChecker abstracts the tmux health-check methods needed by the
// health checker.  Satisfied by *tmux.Tmux; mockable in tests.
type sessionChecker interface {
	CheckSessionHealth(session string, maxInactivity time.Duration) tmux.ZombieStatus
	HasSession(name string) (bool, error)
	KillSession(name string) error
}

// DispatchInspector inspects and reclaims a dog's dispatch mail. It is
// optional on HealthChecker: when absent, dispatch orphans and staleness are
// simply not reported.
type DispatchInspector interface {
	// Scan reports the open dispatch mail for a dog.
	Scan(dogName string) (DispatchScan, error)
	// Reclaim archives every open dispatch for a dog, returning the count.
	Reclaim(dogName string) (int, error)
}

// DogHealthResult describes the health of a single dog.
type DogHealthResult struct {
	Name           string        `json:"name"`
	State          State         `json:"state"`
	ExecState      ExecState     `json:"exec_state,omitempty"`    // observed state, not pool intent
	SessionStatus  string        `json:"session_status"`          // from ZombieStatus.String()
	WorkDuration   time.Duration `json:"work_duration,omitempty"` // how long current work has been running
	NeedsAttention bool          `json:"needs_attention"`
	AutoCleared    bool          `json:"auto_cleared,omitempty"`
	Recommendation string        `json:"recommendation,omitempty"`

	// OpenDispatches is the number of dispatch mails still open for this dog.
	OpenDispatches int `json:"open_dispatches,omitempty"`
	// OldestDispatchAge is the age of the oldest open dispatch.
	OldestDispatchAge time.Duration `json:"oldest_dispatch_age,omitempty"`
	// DispatchesReclaimed counts dispatches archived by this check.
	DispatchesReclaimed int `json:"dispatches_reclaimed,omitempty"`
	// DispatchAlarm is set when this dog's dispatch backlog warrants an
	// escalation and the per-dog cooldown has elapsed.
	DispatchAlarm string `json:"dispatch_alarm,omitempty"`
}

// HealthChecker performs health checks on dogs in the kennel.
type HealthChecker struct {
	mgr        *Manager
	checker    sessionChecker
	dispatch   DispatchInspector
	staleAfter time.Duration
	cooldown   time.Duration
	killHung   bool
	now        func() time.Time
}

// NewHealthChecker creates a HealthChecker.
func NewHealthChecker(mgr *Manager, checker sessionChecker) *HealthChecker {
	return &HealthChecker{
		mgr:        mgr,
		checker:    checker,
		staleAfter: DefaultStaleDispatchAfter,
		cooldown:   DefaultDispatchAlarmCooldown,
		now:        time.Now,
	}
}

// WithDispatch enables dispatch-mail inspection. staleAfter is how long a
// dispatch may stay open before it alarms (zero uses the default); cooldown
// throttles repeat alarms per dog (zero uses the default).
func (hc *HealthChecker) WithDispatch(insp DispatchInspector, staleAfter, cooldown time.Duration) *HealthChecker {
	hc.dispatch = insp
	if staleAfter > 0 {
		hc.staleAfter = staleAfter
	}
	if cooldown > 0 {
		hc.cooldown = cooldown
	}
	return hc
}

// WithKillHung opts in to destroying the live session of a hung dog during
// auto-clear.
//
// It is off by default and deliberately separate from autoClear. A hung dog's
// session is ALIVE — the agent may be mid-execution rather than finished — so
// ending it is a judgement call reserved for the Deacon (ZFC principle), not
// something a routine clear-the-orphans run should do as a side effect. Only
// an operator who asks for it by name gets it.
func (hc *HealthChecker) WithKillHung(kill bool) *HealthChecker {
	hc.killHung = kill
	return hc
}

// dogSessionName returns the tmux session name for a dog.
func dogSessionName(name string) string {
	return fmt.Sprintf("hq-dog-%s", name)
}

// Check performs a health check on a single dog.
func (hc *HealthChecker) Check(d *Dog, maxInactivity time.Duration, autoClear bool) DogHealthResult {
	result := DogHealthResult{
		Name:  d.Name,
		State: d.State,
	}

	// Compute work duration if working and WorkStartedAt is set.
	if d.State == StateWorking && !d.WorkStartedAt.IsZero() {
		result.WorkDuration = time.Since(d.WorkStartedAt)
	}

	session := dogSessionName(d.Name)

	// sessionAlive records whether an agent could actually be executing in
	// this dog's session at the moment of the check — captured before
	// auto-clear kills anything, so the diagnosis describes what was found
	// rather than what this function then did about it.
	sessionAlive := false

	switch d.State {
	case StateWorking:
		status := hc.checker.CheckSessionHealth(session, maxInactivity)
		result.SessionStatus = status.String()
		// AgentDead counts as not alive: the pane exists but nothing in it
		// can read mail or run a plugin.
		sessionAlive = status != tmux.SessionDead && status != tmux.AgentDead

		switch status {
		case tmux.SessionDead:
			// Zombie: state says working but session is gone.
			result.NeedsAttention = true
			result.Recommendation = "zombie: session dead but state=working"
			if autoClear {
				if err := hc.mgr.ClearWork(d.Name); err == nil {
					result.AutoCleared = true
					result.Recommendation = "zombie auto-cleared (session dead)"
				}
			}

		case tmux.AgentDead:
			// Zombie: session exists but agent process died.
			result.NeedsAttention = true
			result.Recommendation = "zombie: agent dead in session"
			if autoClear {
				_ = hc.checker.KillSession(session)
				if err := hc.mgr.ClearWork(d.Name); err == nil {
					result.AutoCleared = true
					result.Recommendation = "zombie auto-cleared (agent dead, session killed)"
				}
			}

		case tmux.AgentHung:
			// Hung: process alive but no tmux activity for maxInactivity.
			// The session is still LIVE, so autoClear alone reports and stops —
			// the agent may be thinking rather than finished, and ending it is
			// the Deacon's call (ZFC). Killing requires the explicit killHung
			// opt-in, which is the only path that touches a live session.
			result.NeedsAttention = true
			if autoClear && hc.killHung {
				if err := hc.checker.KillSession(session); err == nil {
					// The session we diagnosed no longer exists, so the
					// dispatches it was holding are orphans now — say so, or
					// this kill strands the very work it was meant to free.
					sessionAlive = false
				}
				if err := hc.mgr.ClearWork(d.Name); err == nil {
					result.AutoCleared = true
					result.Recommendation = "hung dog cleared (--kill-hung: idle prompt, session killed)"
				} else {
					result.Recommendation = "hung: kill-hung failed: " + err.Error()
				}
			} else {
				result.Recommendation = "hung: agent alive but no tmux activity (reported only; use --kill-hung to end it)"
			}

		default: // SessionHealthy — status.String() already set above
		}

	case StateIdle:
		// Check for orphan session.
		has, _ := hc.checker.HasSession(session)
		sessionAlive = has
		if has {
			result.SessionStatus = "orphan"
			result.NeedsAttention = true
			if autoClear {
				_ = hc.checker.KillSession(session)
				result.AutoCleared = true
				result.Recommendation = "orphan auto-cleared (session killed)"
			} else {
				result.Recommendation = "orphan: dog idle but tmux session exists"
			}
		} else {
			result.SessionStatus = "none"
		}
	}

	// Observed state, independent of dispatch mail. checkDispatch refines this
	// when a dispatch inspector is configured.
	result.ExecState = DeriveExecState(d.State, sessionAlive, 0)

	hc.checkDispatch(d, sessionAlive, autoClear, &result)
	return result
}

// clearAlarm forgets a dog's alarm history, tolerating a nil manager.
func (hc *HealthChecker) clearAlarm(dogName string) {
	if hc.mgr != nil {
		hc.mgr.ClearDispatchAlarm(dogName)
	}
}

// checkDispatch joins the session verdict with the dog's open dispatch mail.
//
// Two failures live here, and they are distinct:
//
//   - Orphaned: the assignee's session is gone but its dispatches are still
//     open. Nothing will ever execute them, so with autoClear they are
//     archived — session death fails the dispatch instead of stranding it.
//   - Stale: dispatches are open past staleAfter while the session is alive.
//     The dog is holding work it is not doing (the instruction was destroyed
//     in delivery, say), which is the condition that previously went unnoticed
//     for twelve days. This alarms rather than reclaims, because a live
//     session may still be mid-execution.
func (hc *HealthChecker) checkDispatch(d *Dog, sessionAlive, autoClear bool, result *DogHealthResult) {
	if hc.dispatch == nil {
		return
	}

	scan, err := hc.dispatch.Scan(d.Name)
	if err != nil {
		// A mailbox we cannot read is itself worth surfacing: it is the same
		// blind spot that let orphans accumulate.
		result.NeedsAttention = true
		result.Recommendation = appendRecommendation(result.Recommendation,
			"dispatch scan failed: "+err.Error())
		return
	}

	result.OpenDispatches = scan.Open
	result.OldestDispatchAge = scan.OldestAge
	result.ExecState = DeriveExecState(d.State, sessionAlive, scan.Open)

	if scan.Open == 0 {
		// Nothing outstanding — forget any prior alarm so the next real
		// problem is reported immediately instead of waiting out a cooldown.
		hc.clearAlarm(d.Name)
		return
	}

	if !sessionAlive {
		result.NeedsAttention = true
		reason := fmt.Sprintf("%d dispatch(es) orphaned by dead session (oldest %s)",
			scan.Open, scan.OldestAge.Truncate(time.Second))
		if autoClear {
			n, reclaimErr := hc.dispatch.Reclaim(d.Name)
			result.DispatchesReclaimed = n
			if reclaimErr != nil {
				reason = fmt.Sprintf("%s — reclaim incomplete (%d archived): %v", reason, n, reclaimErr)
			} else {
				reason = fmt.Sprintf("%d orphaned dispatch(es) reclaimed (dead session)", n)
				hc.clearAlarm(d.Name)
			}
		}
		result.Recommendation = appendRecommendation(result.Recommendation, reason)
		if result.DispatchesReclaimed == 0 {
			hc.raiseAlarm(d.Name, reason, result)
		}
		return
	}

	if hc.staleAfter > 0 && scan.OldestAge > hc.staleAfter {
		result.NeedsAttention = true
		reason := fmt.Sprintf("%d dispatch(es) open past %s (oldest %s) — session alive but not executing",
			scan.Open, hc.staleAfter, scan.OldestAge.Truncate(time.Second))
		result.Recommendation = appendRecommendation(result.Recommendation, reason)
		hc.raiseAlarm(d.Name, reason, result)
	}
}

// raiseAlarm records a dispatch alarm on the result if the dog's cooldown has
// elapsed. The caller is responsible for actually escalating it.
func (hc *HealthChecker) raiseAlarm(dogName, reason string, result *DogHealthResult) {
	if hc.mgr == nil {
		return
	}
	now := time.Now
	if hc.now != nil {
		now = hc.now
	}
	if hc.mgr.ShouldAlarmDispatch(dogName, hc.cooldown, now()) {
		result.DispatchAlarm = fmt.Sprintf("dog %s: %s", dogName, reason)
	}
}

// appendRecommendation joins recommendations so a dispatch finding never
// overwrites the session finding that explains it.
func appendRecommendation(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}

// CheckAll performs health checks on all dogs.
func (hc *HealthChecker) CheckAll(maxInactivity time.Duration, autoClear bool) ([]DogHealthResult, error) {
	dogs, err := hc.mgr.List()
	if err != nil {
		return nil, fmt.Errorf("listing dogs: %w", err)
	}

	results := make([]DogHealthResult, 0, len(dogs))
	for _, d := range dogs {
		results = append(results, hc.Check(d, maxInactivity, autoClear))
	}
	return results, nil
}

// NeedsAttentionCount returns how many results need attention.
func NeedsAttentionCount(results []DogHealthResult) int {
	n := 0
	for _, r := range results {
		if r.NeedsAttention {
			n++
		}
	}
	return n
}
