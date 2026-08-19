package daemon

import (
	"testing"
	"time"
)

// TestStaleIssueAgeMatchesDocumentedDefault pins the daemon's auto-close age to
// the value every user-facing surface advertises (gt-zjb).
//
// The constant was 7*24h while the formula var, the `gt reaper auto-close --help`
// default, and the docs all said 720h — so the daemon closed issues 4.3x sooner
// than documented. It is the constant that auto-closed 7 of 8 agent beads
// town-wide, twice.
//
// Why this is asserted against a LITERAL rather than against the formula: the
// formula var is the OVERRIDE, and this constant is what acts when nobody sets
// one. Reading the formula here would assert that a default agrees with itself.
//
// (When this test was written there was no code path at all from the formula var
// to this constant — stale_issue_age was used bare at the reaper.AutoClose call
// while max_age and purge_age went through wispReaperMaxAge()/wispDeleteAge().
// gt-7hs added the missing staleIssueAge() reader. The wiring is now guarded
// separately by TestReaperFormulaVarsAreConfigurable; this test still owns the
// default, because an override nobody sets leaves this constant acting alone.)
func TestStaleIssueAgeMatchesDocumentedDefault(t *testing.T) {
	const documented = 720 * time.Hour // 30d, per formula var + CLI --help default

	if defaultStaleIssueAge != documented {
		t.Errorf("defaultStaleIssueAge = %s (%s), but every documented surface says %s.\n"+
			"A shorter value silently auto-closes issues before users expect it; that is how\n"+
			"the agent beads died. If this must change, change the formula var and the CLI\n"+
			"default in the same commit.",
			defaultStaleIssueAge, defaultStaleIssueAge.String(), documented)
	}

	// Guard the specific typo that caused it: the 7-day pattern from the two
	// preceding constants applied one line too far.
	if defaultStaleIssueAge == defaultWispDeleteAge {
		t.Errorf("defaultStaleIssueAge equals defaultWispDeleteAge (%s). Those are different "+
			"policies — deleting a closed wisp after 7d is fine, auto-closing a live issue "+
			"after 7d is not. Equality here is the original bug.", defaultWispDeleteAge)
	}
}

// TestStaleIssueAgeIsRenderedIntoTheDigest documents why the wrong value was
// visible for as long as it was: the constant is rendered with Duration.String()
// into the patrol digest as "720h0m0s", so anyone grepping the town for "720h"
// finds the formula and the help text but never the acting value. The digest is
// the ONLY place the real number surfaces.
func TestStaleIssueAgeIsRenderedIntoTheDigest(t *testing.T) {
	got := defaultStaleIssueAge.String()
	if got != "720h0m0s" {
		t.Errorf("digest renders stale_issue_age as %q; expected \"720h0m0s\". "+
			"If this string changes, the patrol digest is the only surface that shows "+
			"the acting value and it must still be recognisable.", got)
	}
}
