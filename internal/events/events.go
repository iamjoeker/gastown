// Package events provides event logging for the gt activity feed.
//
// Events are written to ~/gt/.events.jsonl (raw audit log) and later
// curated by the feed daemon into ~/.feed.jsonl (user-facing).
package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Event represents an activity event in Gas Town.
type Event struct {
	Timestamp  string                 `json:"ts"`
	Source     string                 `json:"source"`
	Type       string                 `json:"type"`
	Actor      string                 `json:"actor"`
	Payload    map[string]interface{} `json:"payload,omitempty"`
	Visibility string                 `json:"visibility"`
}

// Visibility levels for events.
const (
	VisibilityAudit = "audit" // Only in raw events log
	VisibilityFeed  = "feed"  // Appears in curated feed
	VisibilityBoth  = "both"  // Both audit and feed
)

// Common event types for gt commands.
const (
	TypeSling   = "sling"
	TypeHook    = "hook"
	TypeUnhook  = "unhook"
	TypeHandoff = "handoff"
	TypeDone    = "done"
	TypeMail    = "mail"
	TypeSpawn   = "spawn"
	TypeKill    = "kill"
	TypeNudge   = "nudge"
	TypeBoot    = "boot"
	TypeHalt    = "halt"
	TypePark    = "park"
	TypeUnpark  = "unpark"

	// TypePoolReuseRefused records that `gt sling` considered the idle polecat
	// pool and reused NOTHING, with the per-candidate reasons. The success path
	// has always logged TypeSpawn; the refusal logged nothing at all, so the
	// cost of a fresh worktree was measurable while its cause was not (gt-49dp).
	//
	// This type used to fire on success too — whenever ANY candidate had been
	// passed over, including the runs that went on to reuse the very next one.
	// Every one of the 21 events on record was a success; a genuine total
	// refusal had never been emitted, and two witnesses plus a deacon each read
	// the name at face value and jointly scoped a P1 on it (gt-ibtb, gt-uapr).
	// The passed-over case is TypePoolReuseSkipped now, and both carry the
	// outcome in the payload so the name is not the only thing to go on.
	TypePoolReuseRefused = "pool_reuse_refused"

	// TypePoolReuseSkipped records that `gt sling` passed over one or more idle
	// polecats and then REUSED a later one. It is a success: a fresh worktree
	// was not allocated and the pool did not grow. Split out of
	// TypePoolReuseRefused, which named the opposite outcome (gt-ibtb).
	TypePoolReuseSkipped = "pool_reuse_skipped"

	// Session events (for seance discovery)
	TypeSessionStart = "session_start"
	TypeSessionEnd   = "session_end"

	// Session death events (for crash investigation)
	TypeSessionDeath = "session_death" // Feed-visible session termination
	TypeMassDeath    = "mass_death"    // Multiple sessions died in short window

	// Witness patrol events
	TypePatrolStarted   = "patrol_started"
	TypePolecatChecked  = "polecat_checked"
	TypePolecatNudged   = "polecat_nudged"
	TypeEscalationSent   = "escalation_sent"
	TypeEscalationAcked  = "escalation_acked"
	TypeEscalationClosed = "escalation_closed"
	TypePatrolComplete   = "patrol_complete"

	// Merge queue events (emitted by refinery)
	TypeMergeStarted = "merge_started"
	TypeMerged       = "merged"
	TypeMergeFailed  = "merge_failed"
	TypeMergeSkipped = "merge_skipped"

	// Scheduler events
	TypeSchedulerEnqueue        = "scheduler_enqueue"         // Bead scheduled for deferred dispatch
	TypeSchedulerDispatch       = "scheduler_dispatch"        // Bead dispatched from scheduler
	TypeSchedulerDispatchFailed = "scheduler_dispatch_failed" // Bead dispatch failed (requeued)
	TypeSchedulerCloseRetry     = "scheduler_close_retry"     // Context close needed last-resort attempt
)

// EventsFile is the name of the raw events log.
const EventsFile = ".events.jsonl"

// Log writes an event to the events log.
// The event is appended to ~/gt/.events.jsonl.
// Returns nil if logging fails (events are best-effort).
func Log(eventType, actor string, payload map[string]interface{}, visibility string) error {
	event := Event{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Source:     "gt",
		Type:       eventType,
		Actor:      actor,
		Payload:    payload,
		Visibility: visibility,
	}
	return write(event)
}

// LogFeed is a convenience wrapper for feed-visible events.
func LogFeed(eventType, actor string, payload map[string]interface{}) error {
	return Log(eventType, actor, payload, VisibilityFeed)
}

// LogAudit is a convenience wrapper for audit-only events.
func LogAudit(eventType, actor string, payload map[string]interface{}) error {
	return Log(eventType, actor, payload, VisibilityAudit)
}

// write appends an event to the events file.
// Uses flock for cross-process synchronization — sync.Mutex only protects
// intra-process goroutines, but multiple gt processes write concurrently.
func write(event Event) error {
	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		// Silently ignore - we're not in a Gas Town workspace
		return nil
	}

	// Structural backstop: a unit test must never append to a live town's event
	// feed. Checked here because this is the one function every event passes
	// through. See guardTestEvents.
	if handled, err := guardTestEvents(townRoot); handled {
		return err
	}

	eventsPath := filepath.Join(townRoot, EventsFile)

	// Marshal event to JSON
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling event: %w", err)
	}
	data = append(data, '\n')

	// Acquire cross-process file lock
	fl := flock.New(eventsPath + ".lock")
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("acquiring events file lock: %w", err)
	}
	defer fl.Unlock() //nolint:errcheck // best-effort unlock

	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec // G302: events file is non-sensitive operational data
	if err != nil {
		return fmt.Errorf("opening events file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing event: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing events file: %w", err)
	}

	return nil
}

// Payload helpers for common event structures.

// SlingPayload creates a payload for sling events.
func SlingPayload(beadID, target string) map[string]interface{} {
	return map[string]interface{}{
		"bead":   beadID,
		"target": target,
	}
}

// HookPayload creates a payload for hook events.
func HookPayload(beadID string) map[string]interface{} {
	return map[string]interface{}{
		"bead": beadID,
	}
}

// HandoffPayload creates a payload for handoff events.
func HandoffPayload(subject string, toSession bool) map[string]interface{} {
	p := map[string]interface{}{
		"to_session": toSession,
	}
	if subject != "" {
		p["subject"] = subject
	}
	return p
}

// DonePayload creates a payload for done events.
func DonePayload(beadID, branch string) map[string]interface{} {
	return map[string]interface{}{
		"bead":   beadID,
		"branch": branch,
	}
}

// MailPayload creates a payload for mail events.
func MailPayload(to, subject string) map[string]interface{} {
	return map[string]interface{}{
		"to":      to,
		"subject": subject,
	}
}

// SpawnPayload creates a payload for spawn events.
func SpawnPayload(rig, polecat string) map[string]interface{} {
	return map[string]interface{}{
		"rig":     rig,
		"polecat": polecat,
	}
}

// Candidate-list dispositions carried by pool reuse events. The reuse gate
// short-circuits on the first reusable polecat, so a list that ends in a reuse
// is a PREFIX of the roster and not a survey of it: nine rejections there means
// the tenth was accepted, not that ten were turned down.
const (
	CandidateListPrefix   = "prefix"
	CandidateListComplete = "complete"
)

// PoolReuseOutcome is what one pass of the idle-polecat reuse gate decided.
// It is a struct rather than a parameter list because its two booleans are
// independent and easy to swap: the gate can accept a candidate that reuse then
// fails on, which is a refusal reported over a prefix.
type PoolReuseOutcome struct {
	Rig string
	// Considered is how many polecats the gate evaluated, which is NOT the
	// roster size — see GateAccepted.
	Considered int
	// Rejections are "<polecat>=<reason> state=<state>" triples, one per
	// candidate turned down.
	Rejections []string
	// LookupError is the FindIdlePolecat error, if any, which the caller used
	// to discard unexamined (gt-49dp).
	LookupError string
	// ReusedPolecat is the polecat actually reused, empty when reuse did not
	// happen. This is the field that decides "reused".
	ReusedPolecat string
	// GateAccepted reports that the gate short-circuited on a candidate, so the
	// candidate list is a PREFIX of the roster. It is not the same as a reuse:
	// ReuseIdlePolecat can still fail on the accepted candidate, and the
	// candidates after it were never evaluated either way.
	GateAccepted bool
}

// PoolReuseOutcomePayload creates a payload for pool reuse events —
// TypePoolReuseSkipped when a polecat was reused, TypePoolReuseRefused when
// none was.
//
// "reused" is written unconditionally, never omitempty: an absent field would
// be indistinguishable from a recorded false, which is the exact ambiguity this
// payload exists to end. A reader of the feed alone must be able to say whether
// a polecat was reused and which one (gt-ibtb).
func PoolReuseOutcomePayload(o PoolReuseOutcome) map[string]interface{} {
	candidateList := CandidateListComplete
	if o.GateAccepted {
		candidateList = CandidateListPrefix
	}
	p := map[string]interface{}{
		"rig":            o.Rig,
		"considered":     o.Considered,
		"rejected":       o.Rejections,
		"reused":         o.ReusedPolecat != "",
		"candidate_list": candidateList,
	}
	if o.ReusedPolecat != "" {
		p["reused_polecat"] = o.ReusedPolecat
	}
	if o.LookupError != "" {
		p["lookup_error"] = o.LookupError
	}
	return p
}

// PoolReuseSummary renders a pool reuse event as one self-explaining line,
// from the payload alone and with no source access.
//
// It lives here, not in the two renderers, because there are two: the feed
// curator and `gt audit`. Both used to fall through to their default arm and
// print the bare type name, so "pool_reuse_refused" WAS the whole rendered
// line — and that line was a lie on every event ever emitted (gt-ibtb). One
// implementation is what keeps the two surfaces from drifting apart again.
//
// Events emitted before the outcome was recorded carry no "reused" key. Those
// are reported as outcome-not-recorded rather than guessed at: inferring reuse
// from considered == len(rejected)+1 is exactly the short-circuit contract a
// reader of the feed should not have to know.
func PoolReuseSummary(eventType string, payload map[string]interface{}) string {
	rig, _ := payload["rig"].(string)
	if rig == "" {
		rig = "?"
	}
	considered := payloadInt(payload, "considered")
	rejected := payloadStrings(payload, "rejected")
	lookupErr, _ := payload["lookup_error"].(string)
	reused, outcomeRecorded := payload["reused"].(bool)
	// Fall back to the type when the payload predates the flag but the type
	// itself is the post-split one, which can only mean a reuse.
	if !outcomeRecorded && eventType == TypePoolReuseSkipped {
		reused, outcomeRecorded = true, true
	}
	reusedName, _ := payload["reused_polecat"].(string)

	var b strings.Builder
	switch {
	case !outcomeRecorded:
		fmt.Fprintf(&b, "%s: pool reuse outcome NOT RECORDED by this event (%d considered, %d passed over)", rig, considered, len(rejected))
	case reused:
		name := reusedName
		if name == "" {
			name = "an idle polecat"
		}
		fmt.Fprintf(&b, "%s: REUSED %s after passing over %d of %d candidates; no fresh worktree", rig, name, len(rejected), considered)
	default:
		fmt.Fprintf(&b, "%s: REFUSED — no idle polecat reused (%d considered); allocating a fresh worktree", rig, considered)
	}
	// The compounding half of gt-ibtb: nine rejections over a prefix is not a
	// survey that turned down nine polecats. Say so on the line, so the reader
	// does not need the short-circuit contract to size the number.
	if payload["candidate_list"] == CandidateListPrefix {
		b.WriteString("; candidate list is a PREFIX — the gate stopped at the one it accepted and never evaluated the rest")
	}
	if len(rejected) > 0 {
		fmt.Fprintf(&b, "; passed over: %s", strings.Join(rejected, "; "))
	}
	if lookupErr != "" {
		fmt.Fprintf(&b, "; lookup error: %s", lookupErr)
	}
	return b.String()
}

// payloadInt reads a numeric payload field, tolerating both the native int a
// same-process caller passes and the float64 encoding/json hands back after a
// round trip through the feed file.
func payloadInt(payload map[string]interface{}, key string) int {
	switch v := payload[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// payloadStrings reads a string-slice payload field, tolerating both the native
// []string and the []interface{} a JSON round trip produces.
func payloadStrings(payload map[string]interface{}, key string) []string {
	switch v := payload[key].(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// BootPayload creates a payload for rig boot events.
func BootPayload(rig string, agents []string) map[string]interface{} {
	return map[string]interface{}{
		"rig":    rig,
		"agents": agents,
	}
}

// MergePayload creates a payload for merge queue events.
// mrID: merge request ID
// worker: polecat name that submitted the work
// branch: source branch being merged
// reason: failure reason (for merge_failed/merge_skipped events)
func MergePayload(mrID, worker, branch, reason string) map[string]interface{} {
	p := map[string]interface{}{
		"mr":     mrID,
		"worker": worker,
		"branch": branch,
	}
	if reason != "" {
		p["reason"] = reason
	}
	return p
}

// PatrolPayload creates a payload for patrol start/complete events.
func PatrolPayload(rig string, polecatCount int, message string) map[string]interface{} {
	p := map[string]interface{}{
		"rig":           rig,
		"polecat_count": polecatCount,
	}
	if message != "" {
		p["message"] = message
	}
	return p
}

// PolecatCheckPayload creates a payload for polecat check events.
func PolecatCheckPayload(rig, polecat, status, issue string) map[string]interface{} {
	p := map[string]interface{}{
		"rig":     rig,
		"polecat": polecat,
		"status":  status,
	}
	if issue != "" {
		p["issue"] = issue
	}
	return p
}

// NudgePayload creates a payload for nudge events.
func NudgePayload(rig, target, reason string) map[string]interface{} {
	return map[string]interface{}{
		"rig":    rig,
		"target": target,
		"reason": reason,
	}
}

// EscalationPayload creates a payload for escalation events.
func EscalationPayload(rig, target, to, reason string) map[string]interface{} {
	return map[string]interface{}{
		"rig":    rig,
		"target": target,
		"to":     to,
		"reason": reason,
	}
}

// UnhookPayload creates a payload for unhook events.
func UnhookPayload(beadID string) map[string]interface{} {
	return map[string]interface{}{
		"bead": beadID,
	}
}

// KillPayload creates a payload for kill events.
func KillPayload(rig, target, reason string) map[string]interface{} {
	return map[string]interface{}{
		"rig":    rig,
		"target": target,
		"reason": reason,
	}
}

// HaltPayload creates a payload for halt events.
func HaltPayload(services []string) map[string]interface{} {
	return map[string]interface{}{
		"services": services,
	}
}

// ParkPayload creates a payload for park/unpark events. stoppedAgents lists
// what was stopped as part of parking (e.g. "Witness stopped"); it is empty
// on unpark. Recording actor and rig here is what lets a reader of the feed
// tell a deliberate operator pause from an unexplained state change, instead
// of having to trust that whoever noticed the change happened to also read a
// report about it (mirrors hq-qq5).
func ParkPayload(rig string, stoppedAgents []string) map[string]interface{} {
	return map[string]interface{}{
		"rig":            rig,
		"stopped_agents": stoppedAgents,
	}
}

// SessionDeathPayload creates a payload for session death events.
// session: tmux session name that died
// agent: Gas Town agent identity (e.g., "gastown/polecats/Toast")
// reason: why the session was killed (e.g., "zombie cleanup", "user request", "doctor fix")
// caller: what initiated the kill (e.g., "daemon", "doctor", "gt down")
func SessionDeathPayload(session, agent, reason, caller string) map[string]interface{} {
	return map[string]interface{}{
		"session": session,
		"agent":   agent,
		"reason":  reason,
		"caller":  caller,
	}
}

// MassDeathPayload creates a payload for mass death events.
// count: number of sessions that died
// window: time window in which deaths occurred (e.g., "5s")
// sessions: list of session names that died
// possibleCause: suspected cause if known
func MassDeathPayload(count int, window string, sessions []string, possibleCause string) map[string]interface{} {
	p := map[string]interface{}{
		"count":    count,
		"window":   window,
		"sessions": sessions,
	}
	if possibleCause != "" {
		p["possible_cause"] = possibleCause
	}
	return p
}

// SessionPayload creates a payload for session start/end events.
// sessionID: Claude Code session UUID
// role: Gas Town role (e.g., "gastown/crew/joe", "deacon")
// topic: What the session is working on
// cwd: Working directory
func SessionPayload(sessionID, role, topic, cwd string) map[string]interface{} {
	p := map[string]interface{}{
		"session_id": sessionID,
		"role":       role,
		"actor_pid":  fmt.Sprintf("%s-%d", role, os.Getpid()),
	}
	if topic != "" {
		p["topic"] = topic
	}
	if cwd != "" {
		p["cwd"] = cwd
	}
	return p
}

// SchedulerEnqueuePayload creates a payload for scheduler enqueue events.
func SchedulerEnqueuePayload(beadID, rig string) map[string]interface{} {
	return map[string]interface{}{
		"bead": beadID,
		"rig":  rig,
	}
}

// SchedulerDispatchPayload creates a payload for scheduler dispatch events.
func SchedulerDispatchPayload(beadID, rig, polecat string) map[string]interface{} {
	return map[string]interface{}{
		"bead":    beadID,
		"rig":     rig,
		"polecat": polecat,
	}
}

// SchedulerDispatchFailedPayload creates a payload for scheduler dispatch failure events.
func SchedulerDispatchFailedPayload(beadID, rig, errMsg string) map[string]interface{} {
	return map[string]interface{}{
		"bead":  beadID,
		"rig":   rig,
		"error": errMsg,
	}
}
