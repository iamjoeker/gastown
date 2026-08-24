package mail

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/session"
)

// A mail notification is a pointer, not a message. The queue holds "you have
// new mail from X, subject Y"; the thing it points at lives in the inbox and
// has its own state. Nothing connected the two, so a nudge kept announcing a
// message the recipient had already read and archived — and because a delivery
// that fails to submit is requeued, the same dead pointers were re-rendered for
// hours, aged out one by one by TTL alone. A witness holding ten replayed Mayor
// orders, six of them revisions of each other, could not tell which was in
// force; the live correction had already aged out while a revoked order that
// happened to be newer stayed. See gt-loz6.
//
// The fix is to make the pointer answer to the thing it points at, at the one
// place every consumer passes through: the drain.

// SessionNameToAddress converts a tmux session name to the mail address whose
// inbox that session reads. Returns "" if the format is unrecognized.
//
// Examples:
//   - "gt-gastown-crew-max" -> "gastown/crew/max"
//   - "gt-gastown-alpha"    -> "gastown/alpha"
//   - "gt-gastown-witness"  -> "gastown/witness"
//   - "hq-mayor"            -> "mayor"
func SessionNameToAddress(sessionName string) string {
	identity, err := session.ParseSessionName(sessionName)
	if err != nil {
		return ""
	}

	// Short address format: rig/name (not rig/polecats/name).
	switch identity.Role {
	case session.RoleMayor:
		return constants.RoleMayor
	case session.RoleDeacon:
		return constants.RoleDeacon
	case session.RoleDog:
		return DogAddress(identity.Name)
	case session.RoleWitness:
		return fmt.Sprintf("%s/witness", identity.Rig)
	case session.RoleRefinery:
		return fmt.Sprintf("%s/refinery", identity.Rig)
	case session.RoleCrew:
		return fmt.Sprintf("%s/crew/%s", identity.Rig, identity.Name)
	case session.RolePolecat:
		return fmt.Sprintf("%s/%s", identity.Rig, identity.Name)
	default:
		return ""
	}
}

// DrainLive drains the nudge queue for a session and discards mail-derived
// nudges whose message is no longer live. Use it anywhere nudge.Drain would be
// used for delivery to an agent.
//
// Fails open: if the inbox cannot be read, every drained nudge is returned. A
// stale notification is noise; a dropped one is a lost message.
func DrainLive(townRoot, sessionName string) ([]nudge.QueuedNudge, error) {
	drained, err := nudge.Drain(townRoot, sessionName)
	if err != nil || len(drained) == 0 {
		return drained, err
	}
	return DropSpentMailNudges(townRoot, sessionName, drained), nil
}

// DropSpentMailNudges removes mail-derived nudges announcing messages that are
// no longer live in the session's inbox. Non-mail nudges are always kept: they
// carry their content inline and have nothing to go stale against.
//
// Fails open on every lookup failure.
func DropSpentMailNudges(townRoot, sessionName string, nudges []nudge.QueuedNudge) []nudge.QueuedNudge {
	if !anyMailDerived(nudges) {
		return nudges
	}
	address := SessionNameToAddress(sessionName)
	if address == "" {
		return nudges
	}
	mailbox := NewMailboxFromAddress(address, townRoot)
	if mailbox == nil {
		return nudges
	}
	open, err := mailbox.List()
	if err != nil {
		return nudges
	}
	return FilterLiveNudges(nudges, open)
}

// FilterLiveNudges is the pure half of DropSpentMailNudges: given the messages
// currently open in the recipient's inbox, it returns the nudges still worth
// showing.
//
// A mail or escalation nudge is live only while the message it announces is
// still unread — once read, "you have new mail" is spent whether or not the
// message was archived afterwards. A reply reminder is live while its message
// is still open, since the obligation it carries is to reply, not to read.
//
// A nudge that cannot be matched to a message at all — no message ID and no
// thread ID, which is every nudge written before this field existed — is kept.
func FilterLiveNudges(nudges []nudge.QueuedNudge, open []*Message) []nudge.QueuedNudge {
	byID := make(map[string]*Message, len(open))
	unreadThreads := make(map[string]bool, len(open))
	openThreads := make(map[string]bool, len(open))
	for _, msg := range open {
		if msg == nil {
			continue
		}
		byID[msg.ID] = msg
		if msg.ThreadID != "" {
			openThreads[msg.ThreadID] = true
			if !msg.Read {
				unreadThreads[msg.ThreadID] = true
			}
		}
	}

	live := make([]nudge.QueuedNudge, 0, len(nudges))
	for _, n := range nudges {
		if !n.IsMailDerived() || nudgeStillLive(n, byID, unreadThreads, openThreads) {
			live = append(live, n)
		}
	}
	return live
}

func nudgeStillLive(n nudge.QueuedNudge, byID map[string]*Message, unreadThreads, openThreads map[string]bool) bool {
	needsUnread := n.Kind != nudge.KindReplyReminder

	// Message ID is exact, and exactness is the point: a thread outlives the
	// individual messages on it, so matching by thread keeps a spent
	// notification alive as long as anything newer on the same thread is
	// unread — which is how a superseded order kept being replayed.
	if n.MessageID != "" {
		msg, ok := byID[n.MessageID]
		if !ok {
			return false // archived, closed, or GC'd
		}
		return !needsUnread || !msg.Read
	}

	// Pre-MessageID nudges carry only a thread. Fall back to thread state,
	// which is as precise as the record allows.
	if n.ThreadID != "" {
		if needsUnread {
			return unreadThreads[n.ThreadID]
		}
		return openThreads[n.ThreadID]
	}

	// Nothing to correlate against — keep it rather than guess.
	return true
}

func anyMailDerived(nudges []nudge.QueuedNudge) bool {
	for _, n := range nudges {
		if n.IsMailDerived() {
			return true
		}
	}
	return false
}

// ClearMessageNudges removes queued nudges announcing a message the recipient
// has just read or archived, across every session that address can occupy.
// Best-effort: the drain-time filter in DrainLive is the backstop.
func (r *Router) ClearMessageNudges(address, messageID, threadID string) error {
	if r.townRoot == "" || (messageID == "" && threadID == "") {
		return nil
	}

	var firstErr error
	for _, sessionID := range AddressToSessionIDs(address) {
		if _, err := nudge.RemoveByMessage(r.townRoot, sessionID, messageID, threadID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
