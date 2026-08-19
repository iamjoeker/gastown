package formula

import (
	"strings"
	"testing"
)

// The deacon patrol's temp-directory cleanup used to be two inline shell
// blocks. The beads-fixture half was an `rm -rf` whose guard was
// `if ! lsof +D "$dir"`, which deletes whenever lsof exits non-zero — and lsof
// exits non-zero for "nothing is open", for "I could not read that directory",
// for "I am not installed", and, on hosts with docker nsfs/overlay mounts, for
// every single invocation including ones that listed live handles on a running
// Dolt server. It failed OPEN into rm -rf for months and was inert only by
// accident, because a bare-glob `for` list aborts under zsh (gt-32z).
//
// The job now belongs to internal/tmpgc behind `gt deacon sweep-tmp`, which
// reads /proc: argv, cwd and open descriptors, with no exit status to misread,
// and with real unit tests that a guard living in a TOML string could never
// have (gt-1gdh).
//
// These tests are the fence against it coming back. A shell reimplementation
// here would be a plausible-looking change — the prose that warns against it
// sits in the same file and reads as history — so the assertion is mechanical.
//
// See: gt-1gdh, gt-32z, gt-hkv
//
// The TITLE, without the section number — see patrol_section_test.go for what
// pinning the number costs.
const tmpSweepSection = "Stranded build and test scratch dirs"

// extractTmpSweepScript pulls the shell block that follows the temp-sweep
// heading out of the deacon patrol formula.
func extractTmpSweepScript(t *testing.T) string {
	t.Helper()
	return patrolSectionScript(t, deaconPatrolFormula, tmpSweepSection)
}

// TestTmpSweepStepDelegatesToTheCommand asserts the step calls the command and
// does nothing destructive itself.
func TestTmpSweepStepDelegatesToTheCommand(t *testing.T) {
	script := extractTmpSweepScript(t)

	if !strings.Contains(script, "gt deacon sweep-tmp") {
		t.Errorf("the temp-sweep step must call 'gt deacon sweep-tmp'; got:\n%s", script)
	}
	// Everything below is a way the shell version came back. The step is
	// allowed to be exactly one command; it is not allowed to delete anything
	// on its own authority.
	for _, banned := range []struct{ token, why string }{
		{"rm -rf", "the step must not delete anything itself — internal/tmpgc owns the rm -rf path, " +
			"where it has unit tests and fails closed. See gt-32z"},
		{"lsof", "liveness must come from /proc, not lsof: lsof's exit status is non-zero for every " +
			"failure mode AND for success-with-nothing-found, which is what failed open into rm -rf. See gt-32z"},
		{"chmod -R", "forcing write permission before a delete is the shell guard's signature; " +
			"a directory that cannot be removed as-is must be refused, not forced. See gt-32z"},
		{"for dir in", "a bare-glob for list aborts the whole statement under zsh when the first " +
			"pattern matches nothing, which silently disables the step. See gt-32z"},
	} {
		if strings.Contains(script, banned.token) {
			t.Errorf("temp-sweep step contains %q: %s\nscript:\n%s", banned.token, banned.why, script)
		}
	}
}

// TestTmpSweepStepKeepsTheEvidenceItWasBuiltOn guards the prose, not the
// script. The measurements that explain WHY this is a command and not a shell
// block are the only record of it; an editor tidying the section would remove
// exactly the paragraphs that stop the next author reinstating the guard.
func TestTmpSweepStepKeepsTheEvidenceItWasBuiltOn(t *testing.T) {
	content, err := formulasFS.ReadFile(deaconPatrolFormula)
	if err != nil {
		t.Fatalf("reading %s: %v", deaconPatrolFormula, err)
	}
	body := string(content)

	for _, required := range []string{
		"DO NOT REIMPLEMENT THIS IN SHELL",
		"exit 127",    // lsof's missing-tool status, from the measured table
		"failed OPEN", // what the old guard did
		"gt-32z",      // the bead that measured it
	} {
		if !strings.Contains(body, required) {
			t.Errorf("the deacon patrol formula no longer contains %q — the temp-sweep section's "+
				"rationale is the only place this measurement survives", required)
		}
	}
}
