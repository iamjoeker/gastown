package beads

import (
	"fmt"
	"strings"
)

// Wisp type vocabulary for TTL-based compaction (gt-fqd5).
//
// This mirrors beads' internal/types.validWispTypes exactly. bd validates the
// value at create time and rejects anything outside the set with
// "invalid wisp-type %q (must be heartbeat, ping, patrol, gc_report, recovery,
// error, or escalation)", so the set is a contract, not a suggestion: there is
// no value that means "merge request" or "sling context". Wisps with no
// vocabulary match are left unstamped and fall to gt compact's "default" TTL.
const (
	WispTypeHeartbeat  = "heartbeat"  // Liveness pings — 6h
	WispTypePing       = "ping"       // Health check ACKs — 6h
	WispTypePatrol     = "patrol"     // Patrol cycle reports — 24h
	WispTypeGCReport   = "gc_report"  // Garbage collection reports — 24h
	WispTypeRecovery   = "recovery"   // Force-kill, recovery actions — 7d
	WispTypeError      = "error"      // Error reports — 7d
	WispTypeEscalation = "escalation" // Human escalations — 7d
)

var validWispTypes = []string{
	WispTypeHeartbeat, WispTypePing, WispTypePatrol, WispTypeGCReport,
	WispTypeRecovery, WispTypeError, WispTypeEscalation,
}

// IsValidWispType reports whether s is a wisp type bd will accept. The empty
// string is valid and means "unclassified" — gt compact applies its default TTL.
func IsValidWispType(s string) bool {
	if s == "" {
		return true
	}
	for _, v := range validWispTypes {
		if v == s {
			return true
		}
	}
	return false
}

// ValidWispTypes returns the accepted vocabulary, for error messages.
func ValidWispTypes() []string {
	return append([]string(nil), validWispTypes...)
}

// validateWispType rejects a CreateOptions whose WispType would not reach the
// database, so the caller learns which field is wrong instead of getting a bd
// rejection (invalid value) or a silently unclassified wisp (value set on a
// non-ephemeral create, where there is no wisps row to hold the column).
//
// Failing loudly is deliberate: gt-fqd5's whole shape is a write path that
// looked healthy while writing nothing, and the same mistake is easy to repeat
// one caller at a time.
func (o CreateOptions) validateWispType() error {
	if o.WispType == "" {
		return nil
	}
	if !o.Ephemeral {
		return fmt.Errorf("wisp type %q requires Ephemeral: wisp_type is a wisps-table column", o.WispType)
	}
	if !IsValidWispType(o.WispType) {
		return fmt.Errorf("invalid wisp type %q (must be one of %s)",
			o.WispType, strings.Join(validWispTypes, ", "))
	}
	return nil
}

// WispTypeUpdateSQL builds the statement that stamps wispType onto the given
// wisp IDs, for `bd sql`.
//
// Why SQL rather than a flag: `bd create --wisp-type` exists, but the molecule
// spawn commands gastown uses — `bd mol wisp`, `bd mol wisp create`,
// `bd mol bond` — accept no such flag, and `bd update` has no --wisp-type
// either. A molecule wisp and its steps therefore have no CLI route to the
// column; a post-spawn UPDATE is the only one. Retire this in favour of a flag
// if beads grows one.
//
// The statement targets the wisps table only. Callers pass IDs they just
// created, so a stray match is not possible, but the ephemeral guard is kept
// anyway: a typo that reached the issues table would stamp a permanent bead.
//
// Returns an error for an invalid type or an ID containing a quote — IDs are
// bd-generated slugs, so a quote means the caller parsed the wrong token out of
// command output, and interpolating it would be an injection.
func WispTypeUpdateSQL(wispType string, ids []string) (string, error) {
	if wispType == "" {
		return "", fmt.Errorf("wisp type is empty")
	}
	if !IsValidWispType(wispType) {
		return "", fmt.Errorf("invalid wisp type %q (must be one of %s)",
			wispType, strings.Join(validWispTypes, ", "))
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("no wisp IDs given")
	}

	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			return "", fmt.Errorf("empty wisp ID")
		}
		if strings.ContainsAny(id, "'\"\\`") {
			return "", fmt.Errorf("refusing to build SQL for wisp ID %q: contains a quote", id)
		}
		quoted = append(quoted, "'"+id+"'")
	}

	return fmt.Sprintf("UPDATE wisps SET wisp_type = '%s' WHERE id IN (%s)",
		wispType, strings.Join(quoted, ", ")), nil
}
