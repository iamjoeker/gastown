package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// These tests pin the three defects gt-ti84 recorded in `gt dolt cleanup`:
// the balk asserted an explanation it has no evidence for, --dry-run returned
// before the balk so it could not warn about it, and `gt dolt list` called a
// real database an orphan that cleanup would never have removed.

// fixtureBirth is the burst of test-fixture creation times gt-ti84 measured:
// six databases born within 34 seconds of one another.
func fixtureBirth(offsetSeconds int) time.Time {
	return time.Date(2026, 8, 18, 20, 59, 9, 0, time.Local).Add(time.Duration(offsetSeconds) * time.Second)
}

func fixtureOrphans(n int, withBirth bool) []doltserver.OrphanedDatabase {
	names := []string{"forkrig", "pc1", "pc2", "pc3", "testrig", "testrip", "extra1", "extra2"}
	var orphans []doltserver.OrphanedDatabase
	for i := 0; i < n; i++ {
		o := doltserver.OrphanedDatabase{
			Name:      names[i%len(names)],
			Path:      "/town/.dolt-data/" + names[i%len(names)],
			SizeBytes: 4096,
		}
		if withBirth {
			o.CreatedAt = fixtureBirth(i * 7)
		}
		orphans = append(orphans, o)
	}
	return orphans
}

func TestOrphanRatioBalkMessageDoesNotBlameDetection(t *testing.T) {
	msg := orphanRatioBalkMessage(fixtureOrphans(6, true), 11)
	if msg == "" {
		t.Fatal("6 of 11 databases must trip the ratio balk")
	}

	// The old text asserted the explanation that is false in exactly the case
	// this balk meets most often — a small town with real test pollution.
	for _, forbidden := range []string{
		"usually means",
		"not that the databases are actually orphaned",
		"this is suspicious",
	} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("balk must not rank one explanation as the usual one; found %q in:\n%s", forbidden, msg)
		}
	}

	// Both explanations must be offered, neither privileged.
	if !strings.Contains(msg, "metadata.json files are missing") {
		t.Errorf("balk must still offer the broken-detection explanation:\n%s", msg)
	}
	if !strings.Contains(msg, "11 databases total") {
		t.Errorf("balk must offer the small-town explanation with the actual count:\n%s", msg)
	}
	if !strings.Contains(msg, "gt dolt cleanup --force") {
		t.Errorf("balk must still name the override:\n%s", msg)
	}
}

func TestOrphanRatioBalkMessageShowsCreationEvidence(t *testing.T) {
	msg := orphanRatioBalkMessage(fixtureOrphans(6, true), 11)

	first := fixtureBirth(0).Format("2006-01-02 15:04:05")
	last := fixtureBirth(35).Format("2006-01-02 15:04:05")
	if !strings.Contains(msg, first) || !strings.Contains(msg, last) {
		t.Errorf("balk must print the window the flagged databases were created in (%s .. %s):\n%s", first, last, msg)
	}
	if !strings.Contains(msg, "signature of one test run") {
		t.Errorf("a 35s creation burst is the evidence that settles it; balk must say so:\n%s", msg)
	}
}

func TestOrphanCreationEvidenceWithoutBirthTimes(t *testing.T) {
	out := orphanCreationEvidence(fixtureOrphans(6, false))

	if !strings.Contains(out, "not available") {
		t.Errorf("with no birth times the balk must say the evidence is missing:\n%s", out)
	}
	if strings.Contains(out, "signature of one test run") {
		t.Errorf("no timestamps means no clustering claim:\n%s", out)
	}
	if strings.Contains(out, "0001-01-01") || strings.Contains(out, "1970-01-01") {
		t.Errorf("unknown creation time must not be rendered as a zero timestamp:\n%s", out)
	}
}

func TestOrphanCreationEvidenceSpreadOverMonths(t *testing.T) {
	orphans := fixtureOrphans(3, true)
	orphans[0].CreatedAt = time.Date(2026, 2, 1, 9, 0, 0, 0, time.Local)
	orphans[1].CreatedAt = time.Date(2026, 5, 3, 11, 30, 0, 0, time.Local)
	orphans[2].CreatedAt = time.Date(2026, 8, 18, 20, 59, 9, 0, time.Local)

	out := orphanCreationEvidence(orphans)
	if strings.Contains(out, "signature of one test run") {
		t.Errorf("databases created months apart are not one test run:\n%s", out)
	}
}

func TestOrphanCreationEvidencePartialBirthTimes(t *testing.T) {
	orphans := fixtureOrphans(4, true)
	orphans[2].CreatedAt = time.Time{}

	out := orphanCreationEvidence(orphans)
	if !strings.Contains(out, "3 of the 4") {
		t.Errorf("must say how many databases the evidence actually covers:\n%s", out)
	}
}

func TestOrphanRatioBalkBelowThreshold(t *testing.T) {
	cases := []struct {
		name    string
		orphans int
		total   int
	}{
		{"minority", 3, 11},
		{"exactly half", 5, 10},
		{"few orphans in a tiny town", 3, 5},
		{"no databases at all", 4, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if msg := orphanRatioBalkMessage(fixtureOrphans(tc.orphans, true), tc.total); msg != "" {
				t.Errorf("%d/%d must not trip the balk, got:\n%s", tc.orphans, tc.total, msg)
			}
		})
	}
}

func TestEvaluateCleanupBalksForceOverridesRatio(t *testing.T) {
	townRoot := t.TempDir()
	orphans := fixtureOrphans(6, true)
	allDBs := make([]string, 11)

	if balk := evaluateCleanupBalks(townRoot, orphans, allDBs, false); balk == nil {
		t.Fatal("6 of 11 must balk without --force")
	}
	if balk := evaluateCleanupBalks(townRoot, orphans, allDBs, true); balk != nil {
		t.Errorf("--force must clear the ratio balk, got:\n%s", balk.Message)
	}
}

func TestEvaluateCleanupBalksTooManyOrphansSurvivesForce(t *testing.T) {
	townRoot := t.TempDir()
	orphans := fixtureOrphans(doltserver.MaxSQLCleanup+1, true)
	allDBs := make([]string, doltserver.MaxSQLCleanup+1)

	// The SQL-cleanup ceiling is about how long DROP takes, not about whether
	// the operator trusts the detection, so --force does not clear it.
	balk := evaluateCleanupBalks(townRoot, orphans, allDBs, true)
	if balk == nil {
		t.Fatal("more than doltserver.MaxSQLCleanup orphans must balk even with --force")
	}
	if !strings.Contains(balk.Message, "rm -rf") {
		t.Errorf("too-many-orphans balk must print the filesystem remedy:\n%s", balk.Message)
	}
	if !strings.Contains(balk.Message, "gt dolt status") {
		t.Errorf("filesystem remedy must keep its server-is-down verification:\n%s", balk.Message)
	}
}

func TestEvaluateCleanupBalksClean(t *testing.T) {
	townRoot := t.TempDir()
	if balk := evaluateCleanupBalks(townRoot, fixtureOrphans(2, true), make([]string, 11), false); balk != nil {
		t.Errorf("2 of 11 orphans must proceed, got:\n%s", balk.Message)
	}
}

// --- Defect 2: --dry-run must not diverge from the real run --------------

func TestDoltCleanupDryRunReportsTheRefusal(t *testing.T) {
	townRoot := newBalkTestTown(t)
	t.Chdir(townRoot)

	dryOut, dryErr := captureDoltCleanup(t, true, false)
	if dryErr != nil {
		t.Fatalf("dry run should not fail: %v", dryErr)
	}
	realOut, realErr := captureDoltCleanup(t, false, false)
	if realErr == nil {
		t.Fatal("the real run must refuse at this ratio")
	}

	balkLine := "of 11 databases"
	if !strings.Contains(realOut, balkLine) {
		t.Fatalf("real run did not print the ratio balk:\n%s", realOut)
	}
	if !strings.Contains(dryOut, balkLine) {
		t.Errorf("--dry-run must print the refusal the real run hits, got:\n%s", dryOut)
	}
	if !strings.Contains(dryOut, "not a preview of a successful cleanup") {
		t.Errorf("--dry-run must say the real run deletes nothing, got:\n%s", dryOut)
	}
	if !strings.Contains(dryOut, "Dry run: no changes made.") {
		t.Errorf("--dry-run must still close with its own summary, got:\n%s", dryOut)
	}
}

func TestDoltCleanupListsCreationTimes(t *testing.T) {
	townRoot := newBalkTestTown(t)
	t.Chdir(townRoot)

	out, err := captureDoltCleanup(t, true, false)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	// The directories were created by this test, so their birth time is today
	// wherever the platform records one. Where it does not, no timestamp is
	// printed at all — never a fabricated one.
	if strings.Contains(out, "created 0001-01-01") {
		t.Errorf("unknown birth time must be omitted, not printed as zero:\n%s", out)
	}
	if _, ok := birthTimeAvailable(t, filepath.Join(townRoot, ".dolt-data", "pc1")); ok {
		if !strings.Contains(out, ", created "+time.Now().Format("2006-01-02")) {
			t.Errorf("orphan listing must carry creation times where they exist:\n%s", out)
		}
	}
}

// --- Defect 3: list and cleanup must share one orphan predicate ----------

func TestDoltDatabaseLabelAgreesWithCleanup(t *testing.T) {
	townRoot := newBalkTestTown(t)
	referenced := doltserver.ReferencedDatabases(townRoot)
	owners := doltserver.CollectDatabaseOwners(townRoot)
	protected, err := doltserver.ProtectedDatabases(townRoot)
	if err != nil {
		t.Fatalf("ProtectedDatabases: %v", err)
	}

	orphans, err := doltserver.FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases: %v", err)
	}
	removable := make(map[string]bool, len(orphans))
	for _, o := range orphans {
		removable[o.Name] = true
	}

	databases, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	for _, db := range databases {
		label := doltDatabaseLabel(townRoot, db, owners, protected, referenced)
		if got := label == "orphan"; got != removable[db] {
			t.Errorf("%s: gt dolt list says %q but cleanup removable=%v — the two surfaces must agree", db, label, removable[db])
		}
	}
}

func TestDoltDatabaseLabelRigPrefixDatabase(t *testing.T) {
	townRoot := newBalkTestTown(t)
	referenced := doltserver.ReferencedDatabases(townRoot)
	owners := doltserver.CollectDatabaseOwners(townRoot)
	protected, err := doltserver.ProtectedDatabases(townRoot)
	if err != nil {
		t.Fatalf("ProtectedDatabases: %v", err)
	}

	// Control: the fixture only reproduces the defect if "gt" really is the
	// unowned-but-claimed case. If a metadata.json started naming it, the old
	// owner-map lookup would have labelled it correctly and this test would
	// pass for the wrong reason.
	if owner, ok := owners["gt"]; ok {
		t.Fatalf(`fixture no longer reproduces the defect: "gt" has owner %q`, owner)
	}
	if !referenced["gt"] {
		t.Fatal(`fixture no longer reproduces the defect: "gt" is not claimed by the rig-prefix safety net`)
	}

	// "gt" is the gastown rig prefix. No metadata.json names it, so it has no
	// owner — but cleanup will not touch it, so list must not call it an orphan.
	label := doltDatabaseLabel(townRoot, "gt", owners, protected, referenced)
	if label == "orphan" {
		t.Fatal(`"gt" is claimed by the rig-prefix safety net; cleanup excludes it, so list must not call it an orphan`)
	}
	if !strings.Contains(label, "rig prefix") {
		t.Errorf("label should say why it is claimed, got %q", label)
	}
}

// --- helpers -------------------------------------------------------------

// newBalkTestTown builds the shape gt-ti84 measured: 11 databases, 6 of them
// unreferenced test fixtures, plus "gt" claimed only by the rig-prefix safety
// net.
func newBalkTestTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	all := []string{
		"hq", "gastown", "beads", "duly_noted", "gt",
		"forkrig", "pc1", "pc2", "pc3", "testrig", "testrip",
	}
	for _, db := range all {
		nomsDir := filepath.Join(dataDir, db, ".dolt", "noms")
		if err := os.MkdirAll(nomsDir, 0o755); err != nil {
			t.Fatalf("creating %s: %v", db, err)
		}
		if err := os.WriteFile(filepath.Join(nomsDir, "manifest"), []byte("test"), 0o644); err != nil {
			t.Fatalf("writing manifest for %s: %v", db, err)
		}
	}

	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("creating mayor dir: %v", err)
	}
	rigs := map[string]any{
		"rigs": map[string]any{
			"gastown":    map[string]any{"beads": map[string]any{"prefix": "gt-"}},
			"beads":      map[string]any{},
			"duly_noted": map[string]any{},
		},
	}
	data, err := json.Marshal(rigs)
	if err != nil {
		t.Fatalf("marshaling rigs.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), data, 0o644); err != nil {
		t.Fatalf("writing rigs.json: %v", err)
	}

	writeBeadsMetadata(t, filepath.Join(townRoot, ".beads"), "hq")
	for _, rig := range []string{"gastown", "beads", "duly_noted"} {
		writeBeadsMetadata(t, filepath.Join(townRoot, rig, "mayor", "rig", ".beads"), rig)
	}

	return townRoot
}

func writeBeadsMetadata(t *testing.T, beadsDir, database string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", beadsDir, err)
	}
	meta := map[string]any{
		"backend":       "dolt",
		"dolt_mode":     "server",
		"dolt_database": database,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshaling metadata for %s: %v", database, err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o644); err != nil {
		t.Fatalf("writing metadata for %s: %v", database, err)
	}
}

// captureDoltCleanup runs runDoltCleanup with the given flags and returns
// everything it printed.
func captureDoltCleanup(t *testing.T, dry, force bool) (string, error) {
	t.Helper()

	prevDry, prevForce := doltCleanupDry, doltCleanupForce
	doltCleanupDry, doltCleanupForce = dry, force
	t.Cleanup(func() { doltCleanupDry, doltCleanupForce = prevDry, prevForce })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prevStdout := os.Stdout
	os.Stdout = w

	// Drain concurrently: a refusal plus its remedy can outrun the pipe buffer.
	captured := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		captured <- string(out)
	}()

	runErr := runDoltCleanup(nil, nil)

	os.Stdout = prevStdout
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("closing pipe: %v", closeErr)
	}
	return <-captured, runErr
}

// birthTimeAvailable reports whether this platform records a birth time for
// path, so tests can assert on timestamps only where they exist.
func birthTimeAvailable(t *testing.T, path string) (time.Time, bool) {
	t.Helper()
	return doltserver.DatabaseBirthTime(path)
}
