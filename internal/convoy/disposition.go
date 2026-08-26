// Package convoy — how a convoy's tracked work actually stands.
//
// This file is the single classifier for convoy state, and it is shared code
// because two surfaces used to answer the same question with different
// evidence. `gt convoy stranded` consulted EXECUTION state — is the assignee's
// session alive, is its work sitting in the merge queue, is the bead deferred —
// while the dashboard re-derived a verdict from the AGE of the last activity
// event alone. Age is not evidence of stalling; it is evidence that nothing was
// logged. A polecat reading code, running a gate, or thinking logs nothing, so
// every convoy doing work slower than the ten-minute red threshold turned red
// and stayed red until it completed (gt-skzk.1).
//
// Both surfaces now call Classify/WorkStatus here, and Reason is derived FROM
// WorkStatus rather than computed alongside it, so the two cannot disagree
// about a convoy without the disagreement being a compile-time impossibility.
package convoy

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/session"
)

// How a tracked issue stands relative to dispatch. Only DispoReady is work the
// convoy can be fed; the rest each name a distinct answer to "why not", which is
// what the old bare "0 ready" threw away. (gt-bel1)
const (
	DispoReady        = "ready"         // dispatchable right now
	DispoClosed       = "closed"        // done
	DispoDeferred     = "deferred"      // intentionally postponed — waiting on a stated condition
	DispoScheduled    = "scheduled"     // open sling context — waiting for dispatch capacity
	DispoWorking      = "working"       // assignee's session is alive — being worked
	DispoInQueue      = "in-queue"      // assignee gone, but its work is an open MR
	DispoBlocked      = "blocked"       // has open blockers
	DispoNotSlingable = "not-slingable" // town-level or non-slingable type — deacon/mayor's job
	DispoUnknown      = "unknown"       // status unresolved (cross-rig DB unreachable)
)

// StatusUnknown is the status a caller records for a tracked bead whose store
// could not be read. It is not a beads status — it means "not observed".
const StatusUnknown = "unknown"

// DispositionOrder fixes the rendering order of evidence so two runs of the same
// state read identically.
var DispositionOrder = []string{
	DispoReady, DispoWorking, DispoInQueue, DispoScheduled,
	DispoDeferred, DispoBlocked, DispoNotSlingable, DispoUnknown, DispoClosed,
}

// WaitingDispositions are the states with a benign explanation: something is
// already happening, or the bead is deliberately not to be dispatched. A convoy
// where every non-closed issue is in one of these is WAITING, not stranded.
var WaitingDispositions = map[string]bool{
	DispoDeferred:  true,
	DispoScheduled: true,
	DispoWorking:   true,
	DispoInQueue:   true,
}

// What a convoy as a whole is doing. This is the operator-facing verdict: the
// dashboard renders it directly, and Reason maps it onto the coarser vocabulary
// `gt convoy stranded` reports.
//
// Activity age appears nowhere in it. A convoy is stuck when its tracked work
// has no path forward that anything is already taking — never merely because
// no event has been logged for a while.
const (
	WorkStatusEmpty    = "empty"    // no tracked beads
	WorkStatusReady    = "ready"    // dispatchable work with no worker on it
	WorkStatusStuck    = "stuck"    // blocked / unroutable / unreadable — an agent must look
	WorkStatusComplete = "complete" // every tracked bead closed
	WorkStatusWorking  = "working"  // a polecat's session is alive on a tracked bead
	WorkStatusInQueue  = "in-queue" // tracked work is an open MR awaiting merge
	WorkStatusWaiting  = "waiting"  // deferred or scheduled — waiting by design
)

// Why a convoy appears in the stranded list. Each names a different action, so
// callers can act without inferring one from tracked_count/ready_count.
const (
	ReasonFeedable    = "feedable"     // ready work with no worker — sling it
	ReasonEmpty       = "empty"        // 0 tracked issues — clean it up
	ReasonComplete    = "complete"     // every tracked issue closed — close the convoy
	ReasonNeedsReview = "needs-review" // genuinely unexplained — an agent must look
	ReasonWaiting     = "waiting"      // benign wait; NOT returned as stranded
)

// TrackedIssue is the part of a convoy's tracked bead that decides its
// disposition. Callers assemble it from whatever store they read.
type TrackedIssue struct {
	ID        string
	Status    string
	IssueType string
	Assignee  string
	Blocked   bool
}

// Env carries the live-system lookups Classify needs. Injecting them keeps the
// classification testable without tmux or a beads DB, and lets a caller that
// cannot answer a question leave it nil rather than guess.
//
// A nil SessionAlive or QueuedMR reads as "no", which is the conservative
// direction for both: it can only move a bead toward ready, never toward a
// falsely reassuring "someone is on it".
type Env struct {
	// Scheduled is a pre-computed set of bead IDs with open sling contexts.
	Scheduled map[string]bool
	// SessionAlive reports whether a tmux session name currently exists.
	SessionAlive func(sessionName string) bool
	// QueuedMR reports whether the bead's work is already an open merge request.
	QueuedMR func(beadID string) bool
}

// Classify decides how a tracked issue stands relative to dispatch.
//
// The two orderings that matter:
//   - deferred is checked before blocked/scheduled, because deferral is the
//     stated intent and outranks whatever mechanical state accompanies it.
//   - a dead session is not evidence of abandonment until the merge queue has
//     been consulted. A polecat that pushed, submitted, and exited leaves
//     exactly this state behind, and it is the ordinary end of a SUCCESSFUL
//     run — the same misreading that gt-0g5r fixed in the stuck-agent dog.
func Classify(t TrackedIssue, env Env) string {
	status := strings.TrimSpace(t.Status)

	// Unresolved issues are not safe to dispatch.
	if status == "" || status == StatusUnknown {
		return DispoUnknown
	}

	if status == "closed" || status == "tombstone" {
		return DispoClosed
	}

	// Deferred beads are waiting on a condition their own body states. No
	// dispatch can advance them and no review can clear them, so they are
	// neither ready nor stranded.
	if status == string(beads.StatusDeferred) {
		return DispoDeferred
	}

	if t.Blocked {
		return DispoBlocked
	}

	// Scheduled beads are not stranded — they're waiting for dispatch capacity.
	if env.Scheduled[t.ID] {
		return DispoScheduled
	}

	// Open issues with no assignee are trivially ready.
	if status == "open" && t.Assignee == "" {
		return DispoReady
	}

	// Non-open status but no assignee is an edge case (shouldn't happen
	// normally, but could occur if molecule detached improperly).
	if t.Assignee == "" {
		return DispoReady
	}

	// Has assignee — check if the session is alive.
	sessionName, _ := AssigneeSessionName(t.Assignee)
	if sessionName == "" {
		return DispoReady // Can't determine session = treat as ready
	}

	if env.SessionAlive != nil && env.SessionAlive(sessionName) {
		return DispoWorking
	}

	// Session is gone. Before calling that abandonment, check whether the work
	// was submitted: an open MR means the branch is queued and about to merge,
	// so re-dispatching would duplicate committed work (the same guard sling
	// applies in checkPriorWorkGuard, gt-79li).
	if env.QueuedMR != nil && env.QueuedMR(t.ID) {
		return DispoInQueue
	}

	// Session doesn't exist and nothing is queued = orphaned molecule or dead
	// worker. Issues with in_progress/hooked status but dead workers are
	// correctly detected as stranded.
	return DispoReady
}

// ClassifyAll classifies every tracked issue, returning the IDs that can be
// dispatched now and a per-disposition tally of all of them.
//
// Town-level beads (hq- prefix with path=".") are not dispatchable via gt sling
// — they're handled by the deacon — and neither are non-slingable types (epics,
// convoys). Those are recorded as not-slingable rather than dropped, so they
// still explain the ready count they reduce.
func ClassifyAll(townRoot string, tracked []TrackedIssue, env Env) (readyIssues []string, evidence map[string]int) {
	evidence = map[string]int{}

	for _, t := range tracked {
		dispo := Classify(t, env)
		if dispo == DispoReady && (!IsSlingableBead(townRoot, t.ID) || !IsSlingableType(t.IssueType)) {
			dispo = DispoNotSlingable
		}
		evidence[dispo]++
		if dispo == DispoReady {
			readyIssues = append(readyIssues, t.ID)
		}
	}

	return readyIssues, evidence
}

// WorkStatus turns the evidence into the one verdict the operator reads.
//
// Ready is tested before working, and that order is deliberate: a convoy with
// dispatchable work has something an operator can DO about it, and the deacon
// will feed it whether or not a sibling bead has a live polecat. Keeping this
// order identical to the one `gt convoy stranded` has always used is what makes
// Reason a pure function of this result instead of a second opinion.
func WorkStatus(trackedCount int, evidence map[string]int) string {
	switch {
	case trackedCount == 0:
		return WorkStatusEmpty
	case evidence[DispoReady] > 0:
		return WorkStatusReady
	case evidence[DispoBlocked]+evidence[DispoUnknown]+evidence[DispoNotSlingable] > 0:
		// Nothing here has a self-resolving explanation: a blocker chain can be
		// broken, an unroutable bead can be routed, a town-level bead needs the
		// deacon. An agent has something to do.
		return WorkStatusStuck
	case evidence[DispoClosed] == trackedCount:
		// Every tracked issue is done — the convoy itself should close.
		return WorkStatusComplete
	case evidence[DispoWorking] > 0:
		return WorkStatusWorking
	case evidence[DispoInQueue] > 0:
		return WorkStatusInQueue
	default:
		// Everything left is deferred or scheduled. The convoy is waiting.
		return WorkStatusWaiting
	}
}

// Reason names the action a convoy needs, in the vocabulary
// `gt convoy stranded` reports. It is a total mapping of WorkStatus, which is
// the whole point: the stranded list and the dashboard render two different
// levels of detail of ONE verdict, so neither can contradict the other.
func Reason(workStatus string) string {
	switch workStatus {
	case WorkStatusEmpty:
		return ReasonEmpty
	case WorkStatusReady:
		return ReasonFeedable
	case WorkStatusStuck:
		return ReasonNeedsReview
	case WorkStatusComplete:
		return ReasonComplete
	default:
		// working, in-queue, waiting: something is already happening, or the
		// bead is deliberately not to be dispatched.
		return ReasonWaiting
	}
}

// FormatEvidence renders an evidence tally in a stable order, e.g.
// "1 deferred, 2 working". Closed issues are omitted unless they are all there
// is: they are the normal end state and add noise to every other verdict.
func FormatEvidence(evidence map[string]int) string {
	var parts []string
	for _, dispo := range DispositionOrder {
		n := evidence[dispo]
		if n == 0 {
			continue
		}
		if dispo == DispoClosed && len(parts) > 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, dispo))
	}
	return strings.Join(parts, ", ")
}

// AssigneeSessionName maps an agent address to the tmux session that would be
// running it, and reports whether that session is a persistent (crew) one.
// An empty name means the address is not one this town runs sessions for.
func AssigneeSessionName(assignee string) (sessionName string, isPersistent bool) {
	parts := strings.Split(assignee, "/")

	switch len(parts) {
	case 2:
		// rig/polecatName -> gt-rig-polecatName
		return session.PolecatSessionName(session.PrefixFor(parts[0]), parts[1]), false
	case 3:
		// rig/crew/name -> gt-rig-crew-name
		if parts[1] == "crew" {
			return session.CrewSessionName(session.PrefixFor(parts[0]), parts[2]), true
		}
		// rig/polecats/name -> gt-rig-name
		if parts[1] == "polecats" {
			return session.PolecatSessionName(session.PrefixFor(parts[0]), parts[2]), false
		}
		// Other 3-part formats not recognized
		return "", false
	default:
		return "", false
	}
}

// IsSlingableBead reports whether a bead can be dispatched via gt sling.
// Town-level beads (hq- prefix with path=".") and beads with unknown
// prefixes are not slingable — they're handled by the deacon/mayor.
func IsSlingableBead(townRoot, beadID string) bool {
	prefix := beads.ExtractPrefix(beadID)
	if prefix == "" {
		return true // No prefix info, assume slingable
	}
	return beads.GetRigNameForPrefix(townRoot, prefix) != ""
}
