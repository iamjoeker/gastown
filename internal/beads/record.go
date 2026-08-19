package beads

import "strings"

// Record beads: durable archives, not work.
//
// After gt-6dp (7 closed MR wisps destroyed) the standing defence against the
// wisp GC is to write incident and merge-ledger state onto a NORMAL bead —
// wisps are what gets purged, ordinary beads are not. That protection has a
// side effect: the dispatch path treats any open, non-wisp bead of a work type
// as implementable work, attaches mol-polecat-work, and slings a polecat at it.
// A ledger is never "done" in the implementer sense, so the polecat reads it,
// finds nothing to build, and — unless it explicitly closes the bead — the
// zombie patrol reopens it and dispatches again. The record's durability is
// exactly what makes it recur (gt-f8td).
//
// A record label breaks that loop without touching durability: the bead stays
// an ordinary open bead in the issues table, out of reach of every wisp GC
// path, but dispatch refuses it and the ready query does not offer it.
const (
	// RecordLabel is the canonical marker for a durable archival record.
	RecordLabel = "gt:record"

	// LedgerLabel and IncidentLabel are accepted synonyms. Ledgers (merge
	// ledgers, session ledgers) and incident write-ups are the two shapes
	// observed in the wild; both are records and neither is dispatchable.
	LedgerLabel   = "gt:ledger"
	IncidentLabel = "gt:incident"
)

// RecordIssueLabel reports whether a label marks a durable archival record —
// a bead that exists to be read, not implemented.
//
// This is deliberately separate from InternalIssueLabel: a record is NOT Gas
// Town runtime state and must not be swept up by anything that treats internal
// beads as ephemeral. It is also broader than ProtectedIssueLabel, which only
// says "do not auto-close"; a record must additionally never be dispatched.
func RecordIssueLabel(label string) bool {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case RecordLabel, LedgerLabel, IncidentLabel:
		return true
	default:
		return false
	}
}

// HasRecordLabel reports whether any label in labels marks a record.
func HasRecordLabel(labels []string) bool {
	for _, label := range labels {
		if RecordIssueLabel(label) {
			return true
		}
	}
	return false
}

// IsRecordBead reports whether an issue is a durable archival record.
func IsRecordBead(issue *Issue) bool {
	if issue == nil {
		return false
	}
	return HasRecordLabel(issue.Labels)
}

// RecordLabelOn returns the record label carried by labels, or "" if none is
// present. Callers use it to name the specific label in refusal messages so the
// operator knows which one to remove.
func RecordLabelOn(labels []string) string {
	for _, label := range labels {
		if RecordIssueLabel(label) {
			return strings.ToLower(strings.TrimSpace(label))
		}
	}
	return ""
}

// RecordDispatchRefusal returns the message explaining why a record bead was
// not dispatched, or "" when labels carry no record label. Shared by every
// dispatch surface so the guidance is identical wherever the refusal surfaces.
func RecordDispatchRefusal(beadID string, labels []string) string {
	label := RecordLabelOn(labels)
	if label == "" {
		return ""
	}
	return "refusing to sling record bead " + beadID + ": labelled " + label +
		" (a durable archive, not implementable work)\n" +
		"  Records are read, not built. Dispatching one burns a polecat session per attempt.\n" +
		"  If this really is work, drop the label first:\n" +
		"    bd label remove " + beadID + " " + label + "\n" +
		"  Or override this once with --force."
}
