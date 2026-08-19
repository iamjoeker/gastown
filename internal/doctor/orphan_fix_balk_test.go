package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin gt-baj6: `gt doctor --fix` reached the same bulk deletion as
// `gt dolt cleanup` with none of its guards — no orphan-ratio balk, no
// per-database user-tables check (force=true skipped it), no dry run, no
// confirmation. The six test fixtures that prompted gt-xhjb would have been
// deleted without a single warning.

// newSmallTownWithFixtures builds the shape gt-ti84 measured and gt-baj6 is
// about: 11 databases, 6 of them unreferenced test fixtures. 6 of 11 is above
// the orphan ratio, so the deletion must refuse.
func newSmallTownWithFixtures(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()

	referenced := []string{"hq", "gastown", "beads", "duly_noted", "wyvern"}
	fixtures := []string{"forkrig", "pc1", "pc2", "pc3", "testrig", "testrip"}
	for _, db := range append(append([]string{}, referenced...), fixtures...) {
		setupDoltDB(t, townRoot, db)
	}

	rigs := referenced[1:]
	setupRigsJSON(t, townRoot, rigs)
	setupRigMetadata(t, townRoot, "hq", "hq")
	for _, rig := range rigs {
		setupRigMetadata(t, townRoot, rig, rig)
	}
	return townRoot
}

func dbNames(t *testing.T, townRoot string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(townRoot, ".dolt-data"))
	if err != nil {
		t.Fatalf("reading .dolt-data: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestDoltOrphanedDatabaseFixRefusesAboveTheOrphanRatio(t *testing.T) {
	townRoot := newSmallTownWithFixtures(t)
	check := NewDoltOrphanedDatabaseCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	// Control: the fixture only reproduces the defect if detection really does
	// flag all six. With fewer than four flagged, nothing would balk and this
	// test would pass for the wrong reason.
	if len(check.orphanNames) != 6 {
		t.Fatalf("fixture must produce 6 orphans, got %d: %v", len(check.orphanNames), check.orphanNames)
	}
	if check.totalDatabases != 11 {
		t.Fatalf("fixture must have 11 databases, got %d", check.totalDatabases)
	}

	before := dbNames(t, townRoot)

	err := check.Fix(ctx)
	if err == nil {
		t.Fatal("Fix must refuse at this ratio, not delete 6 of 11 databases")
	}
	for _, want := range []string{"refusing to remove", "6 of 11"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must say %q, got: %v", want, err)
		}
	}
	// The refusal explains and points at read-only commands. It must not send
	// the reader on to --force, which is what made the old status hint a route
	// to a bulk delete. (gt-xhjb)
	if strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal must not name the override, got: %v", err)
	}
	if !strings.Contains(err.Error(), "gt dolt cleanup --dry-run") {
		t.Errorf("refusal must name the read-only way to inspect, got: %v", err)
	}

	if after := dbNames(t, townRoot); len(after) != len(before) {
		t.Errorf("a refused Fix must delete nothing: %d databases before, %d after", len(before), len(after))
	}
	for _, name := range check.orphanNames {
		if _, statErr := os.Stat(filepath.Join(townRoot, ".dolt-data", name)); statErr != nil {
			t.Errorf("%s was deleted by a Fix that refused: %v", name, statErr)
		}
	}
}

// TestDoltOrphanedDatabaseRunAnnouncesTheRefusal is the same divergence gt-ti84
// removed from `gt dolt cleanup --dry-run`: the report must not list databases
// as removable while the fix refuses them. `gt doctor` is the rehearsal for
// `gt doctor --fix`.
func TestDoltOrphanedDatabaseRunAnnouncesTheRefusal(t *testing.T) {
	townRoot := newSmallTownWithFixtures(t)
	check := NewDoltOrphanedDatabaseCheck()

	result := check.Run(&CheckContext{TownRoot: townRoot})
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "will REFUSE") {
		t.Errorf("report must say --fix will refuse these:\n%s", details)
	}
	if !strings.Contains(details, "6 of 11") {
		t.Errorf("report must give the ratio the refusal is based on:\n%s", details)
	}
}

// TestDoltOrphanedDatabaseReportDoesNotWarnWhenFixWouldProceed is the control
// for the test above: the warning is conditional, so a report that always
// carried it would satisfy that assertion without tracking the fix at all.
func TestDoltOrphanedDatabaseReportDoesNotWarnWhenFixWouldProceed(t *testing.T) {
	townRoot := t.TempDir()
	setupDoltDB(t, townRoot, "hq")
	setupDoltDB(t, townRoot, "orphan1")
	setupRigsJSON(t, townRoot, []string{})
	setupRigMetadata(t, townRoot, "hq", "hq")

	check := NewDoltOrphanedDatabaseCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot})
	if details := strings.Join(result.Details, "\n"); strings.Contains(details, "will REFUSE") {
		t.Errorf("1 of 2 orphans is below the ratio; report must not claim a refusal:\n%s", details)
	}
}

// TestDoltOrphanedDatabaseFixKeepsTheUserTablesCheck covers the second half of
// the old force=true: it skipped RemoveDatabase's per-database check, so an
// unreferenced database holding real data was deleted along with the empty
// fixtures. With the server down that check is a size heuristic, which is what
// this exercises.
func TestDoltOrphanedDatabaseFixKeepsTheUserTablesCheck(t *testing.T) {
	townRoot := t.TempDir()
	setupDoltDB(t, townRoot, "hq")
	setupDoltDB(t, townRoot, "empty_fixture")
	setupDoltDB(t, townRoot, "has_real_data")
	setupRigsJSON(t, townRoot, []string{})
	setupRigMetadata(t, townRoot, "hq", "hq")

	// >1MB: more than an empty orphan, which is all RemoveDatabase can tell
	// while the server is not running.
	bulk := make([]byte, 2<<20)
	dataFile := filepath.Join(townRoot, ".dolt-data", "has_real_data", ".dolt", "noms", "table")
	if err := os.WriteFile(dataFile, bulk, 0o644); err != nil {
		t.Fatalf("writing bulk data: %v", err)
	}

	check := NewDoltOrphanedDatabaseCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	// Control: 2 orphans is at the ratio minimum, so nothing balks and the only
	// thing that can keep has_real_data is the per-database check.
	if len(check.orphanNames) != 2 {
		t.Fatalf("expected 2 orphans, got %v", check.orphanNames)
	}

	err := check.Fix(ctx)
	if err == nil {
		t.Fatal("Fix must report that it kept a database with real data")
	}
	if !strings.Contains(err.Error(), "has_real_data") {
		t.Errorf("error must name the database it kept, got: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(townRoot, ".dolt-data", "has_real_data")); statErr != nil {
		t.Errorf("a database with real data must survive --fix: %v", statErr)
	}
	// Every orphan is attempted, so one refusal does not hide the rest.
	if _, statErr := os.Stat(filepath.Join(townRoot, ".dolt-data", "empty_fixture")); !os.IsNotExist(statErr) {
		t.Errorf("the empty fixture should still have been removed, stat: %v", statErr)
	}
}

// TestDoltOrphanedDatabaseFixRefusesWithoutARun covers Fix being called with no
// cached state at all: it must delete nothing rather than fall through its
// threshold checks on zero values.
func TestDoltOrphanedDatabaseFixRefusesWithoutARun(t *testing.T) {
	townRoot := newSmallTownWithFixtures(t)
	check := NewDoltOrphanedDatabaseCheck()

	if err := check.Fix(&CheckContext{TownRoot: townRoot}); err != nil {
		t.Fatalf("Fix with nothing cached is a no-op, got: %v", err)
	}
	if names := dbNames(t, townRoot); len(names) != 11 {
		t.Errorf("Fix without a Run must delete nothing, got %d databases", len(names))
	}
}

// TestDoltOrphanedDatabaseFixRefusesWhenTheTownCannotBeCounted pins the
// fail-closed arm: an unknown total is not an acceptable ratio.
func TestDoltOrphanedDatabaseFixRefusesWhenTheTownCannotBeCounted(t *testing.T) {
	townRoot := newSmallTownWithFixtures(t)
	check := NewDoltOrphanedDatabaseCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}

	// Control: the run above did get a count, so the refusal below comes from
	// losing it rather than from the ratio.
	if !check.totalKnown {
		t.Fatal("Run must record the town's database count")
	}
	check.totalKnown, check.totalDatabases = false, 0
	check.orphanNames = check.orphanNames[:1] // below every threshold

	err := check.Fix(ctx)
	if err == nil {
		t.Fatal("Fix must refuse when it cannot evaluate the ratio")
	}
	if !strings.Contains(err.Error(), "could not be counted") {
		t.Errorf("refusal must say why it stopped, got: %v", err)
	}
	if names := dbNames(t, townRoot); len(names) != 11 {
		t.Errorf("a refused Fix must delete nothing, got %d databases", len(names))
	}
}
