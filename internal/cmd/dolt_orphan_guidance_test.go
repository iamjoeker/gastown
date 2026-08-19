package cmd

import (
	"regexp"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// These tests pin gt-xhjb: `gt dolt status` ended its orphan report with
// "Clean up with: gt dolt cleanup", and the chain a reader followed from there
// was three steps of ordinary-looking tool guidance ending at a bulk delete of
// the live data plane — status recommends cleanup, cleanup refuses at the
// orphan ratio and offers --force, --force is the highest-blast-radius command
// in the town. The recommendation was unconditional, so it was loudest exactly
// when deletion mattered most: when detection was RIGHT.

// readOnlyOrphanCommands is the whitelist of commands a reporting surface may
// tell a reader to run. Everything here reports; nothing here mutates.
var readOnlyOrphanCommands = map[string]bool{
	"gt dolt cleanup --dry-run": true,
	"gt dolt list":              true,
}

// gtDoltCommand matches a `gt dolt <sub>` invocation with an optional flag, so
// a test can check every command a surface names rather than grepping for the
// specific phrasings that happen to exist today.
var gtDoltCommand = regexp.MustCompile(`gt dolt [a-z-]+(?: --[a-z-]+)?`)

// namedCommands returns the `gt dolt ...` invocations that appear in out.
func namedCommands(out string) []string {
	return gtDoltCommand.FindAllString(out, -1)
}

// assertOnlyReadOnlyCommands is the core assertion of this bead: a surface that
// reports must not hand the reader a mutating command to run.
func assertOnlyReadOnlyCommands(t *testing.T, out string) {
	t.Helper()
	for _, cmd := range namedCommands(out) {
		if !readOnlyOrphanCommands[cmd] {
			t.Errorf("a reporting surface named the mutating command %q; only read-only commands may be prescribed:\n%s", cmd, out)
		}
	}
}

func TestOrphanCleanupGuidanceNamesNoMutatingCommand(t *testing.T) {
	townRoot := t.TempDir()

	cases := []struct {
		name    string
		orphans int
		total   int
	}{
		// The dangerous case: cleanup refuses here and its refusal offers --force.
		{"cleanup would refuse on ratio", 6, 11},
		// The case that used to look harmless — and is the one where the
		// recommendation actually would have deleted something.
		{"cleanup would proceed", 2, 11},
		{"cleanup would refuse on volume", maxSQLCleanup + 1, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := orphanCleanupGuidance(townRoot, fixtureOrphans(tc.orphans, true), make([]string, tc.total), "  ")

			assertOnlyReadOnlyCommands(t, out)
			// The exact line the bead was filed about.
			if strings.Contains(out, "Clean up with") {
				t.Errorf("the unconditional recommendation is back:\n%s", out)
			}
			// Control: the whitelist above only proves anything if the surface
			// names commands at all. A surface that named none would pass
			// assertOnlyReadOnlyCommands vacuously.
			if len(namedCommands(out)) == 0 {
				t.Errorf("guidance must still route the reader somewhere, or the whitelist proves nothing:\n%s", out)
			}
		})
	}
}

func TestOrphanCleanupGuidanceSaysCleanupWillRefuse(t *testing.T) {
	townRoot := t.TempDir()
	out := orphanCleanupGuidance(townRoot, fixtureOrphans(6, true), make([]string, 11), "  ")

	if !strings.Contains(out, "REFUSE") {
		t.Errorf("cleanup refuses at 6 of 11; the report must say so instead of leaving it to be discovered by running the deletion command:\n%s", out)
	}
	if !strings.Contains(out, "6 of 11 databases") {
		t.Errorf("the refusal must be quantified with this town's actual numbers:\n%s", out)
	}
	// Item 2 of the bead: if the reader will meet --force, the constraint has to
	// travel with it. Naming it as what the refusal offers is not the same as
	// prescribing it — assertOnlyReadOnlyCommands above proves it is not
	// prescribed.
	if !strings.Contains(out, "--force") {
		t.Errorf("the reader will meet --force one step later; the report must say what it does:\n%s", out)
	}
	if !strings.Contains(out, "without the per-database check") {
		t.Errorf("--force must be described by what it skips, not just named:\n%s", out)
	}
}

func TestOrphanCleanupGuidanceProceedCaseDoesNotClaimARefusal(t *testing.T) {
	townRoot := t.TempDir()
	out := orphanCleanupGuidance(townRoot, fixtureOrphans(2, true), make([]string, 11), "  ")

	if strings.Contains(out, "REFUSE") {
		t.Errorf("2 of 11 does not trip either balk; the report must not invent a refusal:\n%s", out)
	}
	if !strings.Contains(out, "would remove these") {
		t.Errorf("the report must say what cleanup would do here:\n%s", out)
	}
	if !strings.Contains(out, "It has not run.") {
		t.Errorf("a report that describes a deletion must say the deletion has not happened:\n%s", out)
	}
}

func TestOrphanCleanupGuidanceDistinguishesTheTwoRefusals(t *testing.T) {
	townRoot := t.TempDir()

	// Volume, not ratio: 51 of 200 is well under the ratio threshold.
	volume := orphanCleanupGuidance(townRoot, fixtureOrphans(maxSQLCleanup+1, true), make([]string, 200), "  ")
	if !strings.Contains(volume, "it can drop by SQL") {
		t.Errorf("past the SQL ceiling the report must give that reason:\n%s", volume)
	}
	if strings.Contains(volume, "above the ratio") {
		t.Errorf("51 of 200 is not a ratio refusal; the report must not give the wrong reason:\n%s", volume)
	}

	// Both trip: evaluateCleanupBalks returns the ratio balk first, because that
	// is the one the real run raises. The report must match the real run.
	both := orphanCleanupGuidance(townRoot, fixtureOrphans(maxSQLCleanup+1, true), make([]string, maxSQLCleanup+1), "  ")
	if !strings.Contains(both, "above the ratio") {
		t.Errorf("when both balks trip, the report must name the one the real run raises first:\n%s", both)
	}
}

// TestOrphanCleanupGuidanceTracksTheDeletionPath is the anti-drift control. The
// report must not carry its own copy of the thresholds: if it did, a change to
// orphanRatioBalkFraction or maxSQLCleanup would make status confidently
// describe a refusal that does not happen, or miss one that does.
func TestOrphanCleanupGuidanceTracksTheDeletionPath(t *testing.T) {
	townRoot := t.TempDir()

	cases := []struct{ orphans, total int }{
		{1, 11}, {2, 11}, {3, 11}, {4, 11}, {6, 11}, {5, 10}, {6, 10},
		{3, 5}, {4, 5}, {maxSQLCleanup, 200}, {maxSQLCleanup + 1, 200},
	}
	sawBoth := map[bool]bool{}
	for _, tc := range cases {
		orphans := fixtureOrphans(tc.orphans, true)
		allDBs := make([]string, tc.total)

		wouldRefuse := evaluateCleanupBalks(townRoot, orphans, allDBs, false) != nil
		reportsRefusal := strings.Contains(orphanCleanupGuidance(townRoot, orphans, allDBs, "  "), "REFUSE")

		if wouldRefuse != reportsRefusal {
			t.Errorf("%d of %d: cleanup refuses=%v but the report says refuses=%v — the two must not be able to disagree",
				tc.orphans, tc.total, wouldRefuse, reportsRefusal)
		}
		sawBoth[wouldRefuse] = true
	}
	// Control: the loop proves agreement only if it exercised both answers.
	if !sawBoth[true] || !sawBoth[false] {
		t.Fatalf("table must cover both refusing and proceeding cases, covered: %v", sawBoth)
	}
}

// TestOrphanCleanupGuidanceNamesTheDurableRemedy pins item 3 of the bead: the
// way to keep a database has to be discoverable from the report, not held in
// agents' heads and repeated in mail.
func TestOrphanCleanupGuidanceNamesTheDurableRemedy(t *testing.T) {
	out := orphanCleanupGuidance(t.TempDir(), fixtureOrphans(6, true), make([]string, 11), "  ")

	if !strings.Contains(out, "protected_dolt_databases") {
		t.Errorf("the report must name the config key that makes a database undeletable:\n%s", out)
	}
	if !strings.Contains(out, "settings/config.json") {
		t.Errorf("the report must name the file the key goes in:\n%s", out)
	}
	if !strings.Contains(out, "even under --force") {
		t.Errorf("the point of the key is that --force does not override it; say so:\n%s", out)
	}
}

func TestOrphanCleanupGuidanceIndents(t *testing.T) {
	out := orphanCleanupGuidance(t.TempDir(), fixtureOrphans(6, true), make([]string, 11), "    ")
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasPrefix(line, "    ") {
			t.Fatalf("every line must carry the caller's indent, got %q", line)
		}
	}
}

// --- the reporting surfaces themselves -----------------------------------

func TestPrintOrphanReportDoesNotPrescribeCleanup(t *testing.T) {
	townRoot := newBalkTestTown(t)
	orphans, err := doltserver.FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases: %v", err)
	}
	if len(orphans) == 0 {
		t.Fatal("fixture must produce orphans or this test proves nothing")
	}
	allDBs, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}

	out := captureStdout(t, func() { printOrphanReport(townRoot, orphans, allDBs) })

	assertOnlyReadOnlyCommands(t, out)
	if !strings.Contains(out, "orphaned database(s)") {
		t.Errorf("report must still name the finding:\n%s", out)
	}
	// The operator decides, so the report has to carry what they decide on.
	for _, o := range orphans {
		if !strings.Contains(out, o.Name) {
			t.Errorf("report must name every flagged database, missing %q:\n%s", o.Name, out)
		}
	}
}
