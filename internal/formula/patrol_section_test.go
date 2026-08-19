package formula

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// Locating a patrol section by its ORDINAL is brittle by construction, and it
// has already cost a merge. gt-hkv added a guard test that found its subject by
// the literal heading "**3. Stale PID/lock files**"; gt-yb33 inserted a new
// "**3. Orphaned Go build work directories**" above it and renumbered the rest.
// Both changes were correct alone, and together the test could not find its
// section — a test with nothing to do with either change.
//
// So sections are located by TITLE and the number is allowed to float. The
// lookup is deliberately strict in the other two directions: two sections with
// the same title is an error rather than a silent first-match, and the search
// for the bash block stops at the next heading so a section that has no script
// cannot silently borrow its neighbour's.
//
// See: gt-yb33, gt-hkv, gt-32z

// patrolSectionHeading matches any "**<n>. <title>**" heading line.
var patrolSectionHeading = regexp.MustCompile(`(?m)^\*\*\d+\. .*\*\*\r?$`)

const deaconPatrolFormula = "formulas/mol-deacon-patrol.formula.toml"

// orphanedGoWorkDirSection is the section gt-yb33 added. Nothing extracts its
// script yet; it is named here so a rename is caught by
// TestPatrolSectionScriptFindsRealDeaconSections rather than by a silent skip.
const orphanedGoWorkDirSection = "Orphaned Go build work directories"

// findPatrolSectionScript returns the first bash block inside the section of
// body titled title. Errors name the way the lookup failed, so a renamed
// section, a duplicated one and a section with no script are distinguishable.
func findPatrolSectionScript(body, title string) (string, error) {
	heading := regexp.MustCompile(`(?m)^\*\*\d+\. ` + regexp.QuoteMeta(title) + `\*\*\r?$`)
	locs := heading.FindAllStringIndex(body, -1)
	switch len(locs) {
	case 1:
		// found
	case 0:
		return "", fmt.Errorf("no section titled %q (expected a heading of the form %q)",
			title, "**<n>. "+title+"**")
	default:
		return "", fmt.Errorf("%d sections titled %q; the lookup cannot tell which one is meant", len(locs), title)
	}

	// Bound the search at the next heading. Without this, a section whose script
	// was removed would silently return the NEXT section's block and the test
	// would pass against the wrong subject.
	start := locs[0][0]
	section := body[start:]
	if next := patrolSectionHeading.FindStringIndex(section[locs[0][1]-start:]); next != nil {
		section = section[:locs[0][1]-start+next[0]]
	}

	const fence = "```bash\n"
	open := strings.Index(section, fence)
	if open < 0 {
		return "", fmt.Errorf("section %q contains no bash block", title)
	}
	rest := section[open+len(fence):]
	closeIdx := strings.Index(rest, "\n```")
	if closeIdx < 0 {
		return "", fmt.Errorf("section %q has an unterminated bash block", title)
	}
	return rest[:closeIdx], nil
}

// patrolSectionScript reads an embedded formula and returns the bash block of
// the section with the given title.
func patrolSectionScript(t *testing.T, formulaFile, title string) string {
	t.Helper()

	content, err := formulasFS.ReadFile(formulaFile)
	if err != nil {
		t.Fatalf("reading %s: %v", formulaFile, err)
	}
	script, err := findPatrolSectionScript(string(content), title)
	if err != nil {
		t.Fatalf("%s: %v", formulaFile, err)
	}
	return script
}

// synthetic patrol body used by the lookup's own tests. %s takes the ordinal of
// the "Stale PID/lock files" section so a renumbering can be simulated.
const patrolBodyTemplate = "## Cleanup\n\n" +
	"**1. Rogue dolt servers**\n\n" +
	"```bash\necho rogue\n```\n\n" +
	"**%s. Stale PID/lock files**\n\n" +
	"Some prose about the guard.\n\n" +
	"```bash\necho pidfiles\n```\n\n" +
	"**9. Report**\n\n" +
	"```bash\necho report\n```\n"

func TestFindPatrolSectionScriptIsOrdinalIndependent(t *testing.T) {
	// The regression this file exists for: the same section under two different
	// numbers must resolve to the same script.
	for _, ordinal := range []string{"2", "3", "4", "11"} {
		body := fmt.Sprintf(patrolBodyTemplate, ordinal)

		script, err := findPatrolSectionScript(body, "Stale PID/lock files")
		if err != nil {
			t.Fatalf("ordinal %s: %v — the lookup is ordinal-sensitive again; "+
				"inserting a section above this one will break unrelated tests. See gt-yb33", ordinal, err)
		}
		if script != "echo pidfiles" {
			t.Errorf("ordinal %s: got script %q, want %q", ordinal, script, "echo pidfiles")
		}
	}
}

// The control for the test above: the lookup must still be able to FAIL. An
// extractor that returned something for every title would pass the
// ordinal-independence test vacuously.
func TestFindPatrolSectionScriptRejectsUnknownTitle(t *testing.T) {
	body := fmt.Sprintf(patrolBodyTemplate, "3")

	if _, err := findPatrolSectionScript(body, "Stale PID/lock file"); err == nil {
		t.Error("lookup matched a title that is not a section heading; it must match the whole title")
	}
	if _, err := findPatrolSectionScript(body, "Dead dog worktrees"); err == nil {
		t.Error("lookup returned a script for a section that does not exist")
	}
}

func TestFindPatrolSectionScriptRejectsDuplicateTitles(t *testing.T) {
	body := fmt.Sprintf(patrolBodyTemplate, "3") +
		"\n**4. Stale PID/lock files**\n\n```bash\necho other\n```\n"

	_, err := findPatrolSectionScript(body, "Stale PID/lock files")
	if err == nil {
		t.Fatal("two sections share a title and the lookup silently picked one")
	}
	if !strings.Contains(err.Error(), "2 sections") {
		t.Errorf("error should name the ambiguity, got: %v", err)
	}
}

func TestFindPatrolSectionScriptDoesNotBorrowTheNextSectionsScript(t *testing.T) {
	body := "**1. Scriptless section**\n\nProse only, no block.\n\n" +
		"**2. Report**\n\n```bash\necho report\n```\n"

	script, err := findPatrolSectionScript(body, "Scriptless section")
	if err == nil {
		t.Fatalf("a section with no bash block returned %q — that is the NEXT section's script, "+
			"and a guard test asserting against it would be testing the wrong subject", script)
	}
	if !strings.Contains(err.Error(), "no bash block") {
		t.Errorf("error should say the section has no block, got: %v", err)
	}
}

// The synthetic tests above cannot notice that the real formula was reshaped.
// This one fails if a heading is renamed or its script removed.
func TestPatrolSectionScriptFindsRealDeaconSections(t *testing.T) {
	for _, title := range []string{
		staleTempDirSection,
		stalePIDFileSection,
		orphanedGoWorkDirSection,
	} {
		if script := patrolSectionScript(t, deaconPatrolFormula, title); strings.TrimSpace(script) == "" {
			t.Errorf("section %q resolved to an empty script", title)
		}
	}
}
