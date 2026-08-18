// Package mail: CC copies as first-class deliverables.
//
// A CC'd message is delivered by assigning the bead to the To: recipient and
// labeling it cc:<identity> for everyone else. That makes the CC copy a second
// *view* of one record rather than a deliverable of its own, and it leaves the
// CC'd recipient with an obligation-looking inbox entry and no legitimate way to
// clear it: closing the bead is the assignee's act, and the beads ownership
// guard correctly refuses it. The guard is right; the surrounding model was
// wrong. See gt-58s.
//
// The fix is a per-recipient dismissal label. Clearing a CC copy adds
// cc-cleared:<my-identity> and touches nothing else — status, assignee, and the
// assignee's own obligation are untouched, so no ownership check is loosened
// and no --force is needed. The inbox then hides CC copies this identity has
// cleared, which is what makes the inbox count stop drifting upward.
package mail

import "strings"

// CCClearedLabelPrefix prefixes the per-recipient CC dismissal label.
// The full label is cc-cleared:<identity> — scoped to one recipient, so each
// CC'd party clears its own copy without affecting anyone else's.
const CCClearedLabelPrefix = "cc-cleared:"

// CCClearedLabel returns the dismissal label for a recipient identity.
func CCClearedLabel(identity string) string {
	return CCClearedLabelPrefix + identity
}

// IsCCOnly reports whether msg landed in this mailbox as a CC copy rather than
// being addressed to it. A message that is both addressed and CC'd to this
// identity is addressed: the assignee owns it and clears it by closing it.
func (m *Mailbox) IsCCOnly(msg *Message) bool {
	if msg == nil {
		return false
	}
	variants := m.identityVariants()
	if matchesIdentity(AddressToIdentity(msg.To), variants) {
		return false
	}
	for _, cc := range msg.CC {
		if matchesIdentity(AddressToIdentity(cc), variants) {
			return true
		}
	}
	return false
}

// DismissCC clears this mailbox's CC copy of a message by adding
// cc-cleared:<identity>. The underlying bead keeps its status and assignee: the
// To: recipient's obligation is unchanged and still theirs to close.
func (m *Mailbox) DismissCC(id string) error {
	return m.addLabel(id, CCClearedLabel(m.identity))
}

// ccClearedFor reports whether the message carries a CC dismissal label for any
// of the given identity variants.
func ccClearedFor(bm *BeadsMessage, identities []string) bool {
	for _, id := range identities {
		if bm.HasLabel(CCClearedLabel(id)) {
			return true
		}
	}
	return false
}

// filterClearedCC drops CC results this identity has already dismissed.
// Applied only to the CC query's results: a message reaching the inbox by
// assignee is addressed, and a stale dismissal label must never hide it.
func filterClearedCC(msgs []BeadsMessage, identities []string) []BeadsMessage {
	kept := make([]BeadsMessage, 0, len(msgs))
	for i := range msgs {
		bm := &msgs[i]
		if ccClearedFor(bm, identities) {
			continue
		}
		kept = append(kept, *bm)
	}
	return kept
}

func matchesIdentity(identity string, variants []string) bool {
	if identity == "" {
		return false
	}
	for _, variant := range variants {
		if identity == variant {
			return true
		}
	}
	return false
}

// IsOwnershipRefusal reports whether err is the beads ownership guard refusing
// to close a bead assigned to someone else. Matching is textual because the
// refusal crosses the bd subprocess boundary as a message, not a typed error.
func IsOwnershipRefusal(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "assignee is") && strings.Contains(text, "actor is")
}
