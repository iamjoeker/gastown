package events

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Rig attribution for the activity feed.
//
// ~/gt/.events.jsonl is one town-wide file: every rig's slings, spawns, mail
// and session churn land in the same stream. `gt mol await-signal` tails that
// file and returns on the first new line, so before this filter existed an
// idle rig's patrol agent woke on every event any rig produced. That inverts
// the exponential backoff it is supposed to feed — the busier the town, the
// more often a completely idle rig is woken, and the backoff never gets to
// grow (gt-p54t).
//
// The events schema has no rig column, so attribution is derived from the
// identities an event already carries: payload.rig where the emitter set it,
// the actor, and any payload field naming an agent. An identity is
// "<rig>/<role>/<name>" for rig-scoped agents and a bare town role (mayor/,
// deacon/, boot, gt) for town-scoped ones, so the leading path segment is the
// rig whenever there is one.
//
// Two rules keep the filter from ever losing a signal that matters:
//
//   - Attribution is a SET, not a single owner, and it includes addressees.
//     A mail event from beads/ace to gastown/witness concerns BOTH rigs, so it
//     still wakes gastown. Mail wakes are town-wide by design and a filter that
//     swallowed cross-rig mail would be a worse defect than the one it fixes.
//   - An event that attributes to no rig at all — mayor↔deacon mail, boot
//     sessions, escalation closes, or any line this code cannot parse — wakes
//     everyone. Those are town-scoped, not "confined to another rig", and
//     failing open costs a wake while failing closed would lose one.
//
// Suppression therefore happens in exactly one case: the event names one or
// more rigs and none of them is the watcher's.

// townIdentitySegments are leading segments of an agent identity that belong to
// the town rather than to any rig. A rig may not take one of these names.
var townIdentitySegments = map[string]bool{
	"":        true,
	".":       true,
	"mayor":   true,
	"deacon":  true,
	"boot":    true,
	"gt":      true,
	"town":    true,
	"hq":      true,
	"unknown": true,
	// "wisp" is not a role but appears as the second segment of wisp bead IDs
	// (gt-wisp-d74rh). Some emitters put a wisp ID in payload.rig, and reading
	// that as a rig named "wisp" would suppress the event for every real rig.
	"wisp": true,
}

// scopePayloadKeys are payload fields that name an agent whose rig the event
// concerns. "rig" is handled separately; "bead" and "session_id" are
// deliberately absent because they name work and sessions, not agents.
var scopePayloadKeys = []string{
	"to", "target", "agent", "role", "assignee",
	"recipient", "from", "closed_by", "acked_by", "worker",
}

// rigNamePattern is what a rig name can look like: a rig is a directory under
// the town root, so it is a plain filename.
//
// This is a guard against reading a non-identity as a rig, and it must stay
// strict because the failure it prevents is the dangerous direction. Nudges to
// a channel put "channel:<name>" in payload.target; without this check that
// parses as a rig called "channel:<name>", which belongs to nobody, so an event
// whose only other identity is town-scoped would be suppressed for EVERY rig.
// Rejecting it instead leaves the event unattributed, which fails open.
var rigNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// RigOfIdentity extracts the rig from a Gas Town agent identity, returning ""
// for town-scoped identities and for anything it cannot attribute.
//
// Recognised forms:
//
//	gastown/witness, gastown/polecats/dust  -> "gastown"  (path form)
//	gt-gastown-witness, gt-gastown-crew-joe -> "gastown"  (agent-bead form)
//	mayor/, deacon/dogs/alpha, boot, gt     -> ""         (town-scoped)
//	gt-mycat, hq-deacon, ""                 -> ""         (no rig in the name)
func RigOfIdentity(identity string) string {
	s := strings.TrimSpace(identity)
	if s == "" {
		return ""
	}

	// Agent-bead form: gt-<rig>-<role>[-<name>]. A two-segment name such as
	// gt-mycat is a bare agent name with no rig in it, so it stays unattributed
	// rather than being read as a rig called "mycat".
	if rest, ok := strings.CutPrefix(s, "gt-"); ok {
		parts := strings.Split(rest, "-")
		if len(parts) >= 2 {
			return asRigName(parts[0])
		}
		return ""
	}

	// hq-* is a town bead/agent ID (hq-mayor, hq-deacon, hq-wisp-...).
	if strings.HasPrefix(s, "hq-") {
		return ""
	}

	return asRigName(strings.SplitN(s, "/", 2)[0])
}

// asRigName returns seg if it can be a rig name, and "" otherwise.
func asRigName(seg string) string {
	if townIdentitySegments[seg] || !rigNamePattern.MatchString(seg) {
		return ""
	}
	return seg
}

// EventRigs returns the rigs an event concerns, deduplicated and sorted.
//
// An empty result means the event is town-scoped: it named no rig anywhere.
// Callers must treat that as "concerns everyone", not as "concerns nobody".
func EventRigs(e Event) []string {
	set := make(map[string]bool, 4)
	add := func(identity string) {
		if r := RigOfIdentity(identity); r != "" {
			set[r] = true
		}
	}

	add(e.Actor)

	if e.Payload != nil {
		// payload.rig is the emitter's own declaration. It goes through the
		// same normalisation as every other identity because it is sometimes
		// empty and has been seen carrying a wisp ID instead of a rig name.
		if v, ok := e.Payload["rig"].(string); ok {
			add(v)
		}
		for _, k := range scopePayloadKeys {
			if v, ok := e.Payload[k].(string); ok {
				add(v)
			}
		}
	}

	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// LineRigs parses one raw line of the events file and returns the rigs it
// concerns. ok is false when the line is blank or is not a parseable event.
func LineRigs(line string) (rigs []string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, false
	}
	var e Event
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return nil, false
	}
	return EventRigs(e), true
}

// LineWakesRig reports whether an event line should wake a watcher scoped to
// rig. An empty rig means a town-wide watcher, for which every event wakes.
//
// The three fail-open paths (town-wide watcher, unparseable line, town-scoped
// event) are the deliberate ones described at the top of this file: a wake that
// was not needed costs a turn, a wake that was dropped loses work.
func LineWakesRig(line, rig string) bool {
	if rig == "" {
		return true
	}
	rigs, ok := LineRigs(line)
	if !ok || len(rigs) == 0 {
		return true
	}
	for _, r := range rigs {
		if r == rig {
			return true
		}
	}
	return false
}
