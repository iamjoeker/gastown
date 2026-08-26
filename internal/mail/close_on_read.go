// Package mail: which messages a read consumes.
//
// Reading a message never closed it. Every CLI read path called MarkReadOnly,
// which acks delivery and adds a "read" label while leaving the bead open, so
// mail piled up in the recipient's work queue permanently and the only closer
// was a human typing `gt mail archive`. Measured on the hq store when gt-qffl
// was filed: 550 of 770 open beads were mail, and 520 of those had already been
// read and acknowledged.
//
// The obvious repair — point the read paths at MarkRead, which closes — is
// wrong, and MarkReadOnly is not a bug. `gt mail mark-read --all` marks a whole
// inbox in one loop; if reading closed messages, running it once would close
// every unanswered question and escalation sitting in that inbox. So there were
// only two states, lingers-forever or listing-closes-everything, and nothing in
// between. This file is the in-between.
//
// The predicate is deliberately layered, and every layer fails CLOSED. Leaving
// a consumed message open costs an inbox row; closing a real question loses it
// and the sender waits forever, which is the failure that put the mayor's inbox
// at 375 unread with a HIGH escalation unactioned for 3.7 days.
package mail

import "strings"

// consumedSubjectPrefixes are the automated subjects whose messages are
// finished the moment they are read: nothing in the body asks for anything, no
// reply is possible, and the next one supersedes the last.
//
// A subject allowlist is needed on top of the message type because msg-type was
// uniformly "notification" until gt-do5c: every message sent before that change
// was typed by a DEFAULT rather than by its sender, so for the backlog
// "notification" means "nobody said", not "no reply possible". Sampling found
// real questions among them. These prefixes are a second, independent signal
// that a specific message really is machine traffic — one that works on beads
// already written, which no change to the send path can.
//
// Matched case-sensitively against the raw subject, for the reason gt-rhxb
// found: a case-insensitive prefix match on this same vocabulary silently
// swallowed ordinary prose ("Merged crater's fix by hand").
//
// Each entry is measured against the hq store, not guessed:
//
//	Convoy complete:  225 beads, 35 still open — the largest single accumulator.
//	                  Sent to mayor/ "for strategic visibility" (convoy.go,
//	                  mol-convoy-cleanup.formula.toml).
//	SCHEDULER_OPEN     28 beads. A capacity notice (witness/handlers.go); the
//	                  next completion supersedes it.
//	MERGED             38 beads. The refinery's merge receipt
//	                  (protocol.NewMergedMessage). Reporting only — a MERGED is
//	                  not an instruction to do anything.
//	LIFECYCLE:          2 beads. Shutdown notices for a polecat that is already
//	                  going away.
//
// Deliberately NOT here, having been considered:
//
//	POLECAT_DIED      6 beads. The body ends in resling instructions; the mayor
//	                  has to act. (Independently excluded: these are the only
//	                  mail on the store stamped with an EMPTY msg-type.)
//	BRANCH SWEEP      8 beads. A short list of branches for a human to triage.
//	PATROL            A patrol report can be awaiting a ruling — gt-ac1l names
//	                  hq-1szee as exactly that.
//	MERGE_READY /     Typed TypeTask, so the type layer already excludes them.
//	MERGE_FAILED      Listed here so a future reader does not "simplify" by
//	                  adding them back.
//
// This is NOT drainableSubjects (internal/cmd/mail_drain.go) and must not be
// merged with it. That list answers "safe to bulk-archive once stale", behind
// an explicit command and a 30-minute age gate, and so can include things that
// needed action but no longer do — CRASHED_POLECAT, MERGE_READY. This list
// answers "consumed the instant it is read", with no age gate and no opt-in.
var consumedSubjectPrefixes = []string{
	"Convoy complete:",
	"SCHEDULER_OPEN",
	"MERGED ",
	"LIFECYCLE:",
}

// HasConsumedSubject reports whether subject is one of the automated protocol
// subjects that carry no obligation. See consumedSubjectPrefixes.
func HasConsumedSubject(subject string) bool {
	for _, prefix := range consumedSubjectPrefixes {
		if strings.HasPrefix(subject, prefix) {
			return true
		}
	}
	return false
}

// ConsumedByReading reports whether reading msg leaves nothing owed, so the
// read path may close it instead of leaving it in the recipient's work queue.
//
// It answers only the question about the MESSAGE. Whether this particular
// reader may act on it — that they are the addressee and not a CC, that the
// bead is not hooked — belongs to the mailbox; see Mailbox.MarkReadConsumed.
//
// Three conditions, all required:
//
//  1. The type must have been STAMPED. RawType is the label verbatim, so a
//     writer that stamped nothing is distinguishable from one that stamped
//     "notification" — through Message.Type it is not, because ParseMessageType
//     rewrites "" to TypeNotification before any predicate sees it.
//  2. The stamped type must say a read consumes it (MessageType.SafeToCloseOnRead:
//     notification, reply and handoff yes; query, task, scavenge, escalation and
//     anything unrecognised no).
//  3. For "notification" specifically, the subject must also be automated
//     traffic. reply and handoff need no such corroboration: "reply" is set
//     structurally by --reply-to rather than typed by a sender, and "handoff"
//     could not be stored at all before gt-do5c, so neither carries the
//     defaulted-notification backlog problem.
func ConsumedByReading(msg *Message) bool {
	if msg == nil {
		return false
	}
	stamped := MessageType(msg.RawType)
	if !stamped.SafeToCloseOnRead() {
		return false
	}
	if stamped == TypeNotification {
		return HasConsumedSubject(msg.Subject)
	}
	return true
}
