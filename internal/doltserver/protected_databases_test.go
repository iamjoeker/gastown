package doltserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin item 3 of gt-xhjb: a town must be able to write down which
// unreferenced databases are deliberate, and have the tool honour it. Before
// this, the only thing keeping pc1/pc2/pc3 alive was a mayor's ruling held in
// agents' heads and repeated in mail — something the tool could not read and a
// new agent would never see.

// newProtectionTestTown builds a town with databases on disk, one rig claiming
// one of them, and the rest unreferenced.
func newProtectionTestTown(t *testing.T, protectedInSettings []string) string {
	t.Helper()
	townRoot := t.TempDir()

	for _, db := range []string{"hq", "gastown", "pc1", "pc2", "pc3", "testdb_x"} {
		nomsDir := filepath.Join(townRoot, ".dolt-data", db, ".dolt", "noms")
		if err := os.MkdirAll(nomsDir, 0o755); err != nil {
			t.Fatalf("creating %s: %v", db, err)
		}
		if err := os.WriteFile(filepath.Join(nomsDir, "manifest"), []byte("test"), 0o644); err != nil {
			t.Fatalf("writing manifest for %s: %v", db, err)
		}
	}

	writeProtectionMetadata(t, filepath.Join(townRoot, ".beads"), "hq")
	writeProtectionMetadata(t, filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads"), "gastown")

	if protectedInSettings != nil {
		writeTownSettings(t, townRoot, map[string]any{
			"type":                     "town-settings",
			"version":                  1,
			"protected_dolt_databases": protectedInSettings,
		})
	}
	return townRoot
}

func writeProtectionMetadata(t *testing.T, beadsDir, database string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", beadsDir, err)
	}
	data, err := json.Marshal(map[string]any{
		"backend":       "dolt",
		"dolt_mode":     "server",
		"dolt_database": database,
	})
	if err != nil {
		t.Fatalf("marshaling metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o644); err != nil {
		t.Fatalf("writing metadata: %v", err)
	}
}

func writeTownSettings(t *testing.T, townRoot string, settings any) {
	t.Helper()
	dir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating settings dir: %v", err)
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshaling settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
		t.Fatalf("writing settings: %v", err)
	}
}

func TestProtectedDatabasesMergesBuiltinAndSettings(t *testing.T) {
	townRoot := newProtectionTestTown(t, []string{"pc1", "pc2", "pc3"})

	protected, err := ProtectedDatabases(townRoot)
	if err != nil {
		t.Fatalf("ProtectedDatabases: %v", err)
	}

	for _, db := range []string{"pc1", "pc2", "pc3"} {
		label, ok := protected[db]
		if !ok {
			t.Errorf("%s is listed in settings/config.json but is not protected", db)
			continue
		}
		// The label has to name the file, so a reader who did not write the
		// entry can find and change it.
		if !strings.Contains(label, "settings/config.json") {
			t.Errorf("%s label %q must name where the protection came from", db, label)
		}
	}
	if _, ok := protected["beads_global"]; !ok {
		t.Error("a town's settings must add to gt's built-in registry, not replace it")
	}
	if _, ok := protected["testdb_x"]; ok {
		t.Error("a database nobody protected must not be protected")
	}
}

func TestProtectedDatabasesWithoutSettingsFile(t *testing.T) {
	protected, err := ProtectedDatabases(newProtectionTestTown(t, nil))
	if err != nil {
		t.Fatalf("a town with no settings file is normal, not an error: %v", err)
	}
	if _, ok := protected["beads_global"]; !ok {
		t.Error("built-in protection must survive a town with no settings file")
	}
}

// TestProtectedDatabasesFailsClosedOnUnreadableSettings is the important error
// case. Returning an empty map on a parse failure would mean a corrupt settings
// file silently reads as "nothing is protected" — the one failure mode that
// turns this guard into a deletion.
func TestProtectedDatabasesFailsClosedOnUnreadableSettings(t *testing.T) {
	townRoot := newProtectionTestTown(t, []string{"pc1"})
	settingsPath := filepath.Join(townRoot, "settings", "config.json")
	if err := os.WriteFile(settingsPath, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupting settings: %v", err)
	}

	if _, err := ProtectedDatabases(townRoot); err == nil {
		t.Fatal("an unparseable settings file must be an error, not an empty protection list")
	}

	// The error has to reach the paths that delete, not just the one that reads.
	if _, err := FindOrphanedDatabases(townRoot); err == nil {
		t.Error("orphan detection must not report a deletion list it cannot vet")
	}
	err := RemoveDatabase(townRoot, "pc1", true)
	if err == nil {
		t.Fatal("removal must refuse when protection cannot be determined")
	}
	if !strings.Contains(err.Error(), "protected") {
		t.Errorf("refusal must say why: %v", err)
	}
}

func TestFindOrphanedDatabasesSkipsProtected(t *testing.T) {
	townRoot := newProtectionTestTown(t, []string{"pc1", "pc2", "pc3"})

	orphans, err := FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases: %v", err)
	}
	found := map[string]bool{}
	for _, o := range orphans {
		found[o.Name] = true
	}

	for _, db := range []string{"pc1", "pc2", "pc3"} {
		if found[db] {
			t.Errorf("%s is protected in settings/config.json but was reported as an orphan", db)
		}
	}
	// Control: the fixture only proves anything if these databases WOULD have
	// been orphans. Without it, a change that broke detection entirely would
	// pass the assertions above.
	if !found["testdb_x"] {
		t.Fatal("fixture must still produce an unprotected orphan, or the skip above proves nothing")
	}
	unprotected, err := FindOrphanedDatabases(newProtectionTestTown(t, nil))
	if err != nil {
		t.Fatalf("FindOrphanedDatabases (control town): %v", err)
	}
	controlFound := map[string]bool{}
	for _, o := range unprotected {
		controlFound[o.Name] = true
	}
	for _, db := range []string{"pc1", "pc2", "pc3"} {
		if !controlFound[db] {
			t.Errorf("control: %s must be an orphan when the town has NOT protected it", db)
		}
	}
}

// TestRemoveDatabaseRefusesProtectedUnderForce is the guarantee the config key
// is worth having. --force clears the orphan-ratio balk and the user-tables
// check; it must not clear this. `gt doctor --fix` and AddRig's orphan drop
// both pass force=true, so a guard that force could clear would be inert on
// two of the three paths that delete.
func TestRemoveDatabaseRefusesProtectedUnderForce(t *testing.T) {
	townRoot := newProtectionTestTown(t, []string{"pc1"})

	for _, force := range []bool{false, true} {
		err := RemoveDatabase(townRoot, "pc1", force)
		if err == nil {
			t.Fatalf("force=%v: removal of a protected database must be refused", force)
		}
		if !strings.Contains(err.Error(), "protected") {
			t.Errorf("force=%v: refusal must say the database is protected, got: %v", force, err)
		}
	}

	// The database must still be there. A refusal that deleted first would
	// satisfy every assertion above.
	if !DatabaseExists(townRoot, "pc1") {
		t.Fatal("pc1 was removed despite the refusal")
	}

	// Control: the refusal must be about protection specifically, not about
	// this fixture being un-removable in general. An unprotected database gets
	// past the protection check — it may still fail later for other reasons,
	// but not with this message.
	if err := RemoveDatabase(townRoot, "testdb_x", true); err != nil && strings.Contains(err.Error(), "is protected") {
		t.Errorf("an unprotected database must not be refused as protected: %v", err)
	}
}

func TestIsOrphanDatabaseHonoursProtectedSet(t *testing.T) {
	referenced := map[string]bool{"hq": true}
	protected := map[string]string{"pc1": ProtectedDatabaseSettingsLabel}

	if IsOrphanDatabase(protected, referenced, "pc1") {
		t.Error("a protected database is not an orphan")
	}
	if IsOrphanDatabase(protected, referenced, "hq") {
		t.Error("a referenced database is not an orphan")
	}
	if !IsOrphanDatabase(protected, referenced, "testdb_x") {
		t.Error("an unreferenced, unprotected database is an orphan")
	}
}

func TestCollectDatabaseOwnersLabelsSettingsProtected(t *testing.T) {
	townRoot := newProtectionTestTown(t, []string{"pc1"})

	owners := CollectDatabaseOwners(townRoot)
	label, ok := owners["pc1"]
	if !ok {
		t.Fatal("a protected database must carry an owner label, or reporting surfaces call it an orphan")
	}
	if label != ProtectedDatabaseSettingsLabel {
		t.Errorf("pc1 label = %q, want %q", label, ProtectedDatabaseSettingsLabel)
	}
	if _, ok := owners["testdb_x"]; ok {
		t.Error("an unprotected orphan must not be given an owner")
	}
}

// TestProtectedDatabasesIgnoresBlankEntries keeps a stray "" in the settings
// array from protecting the empty database name, which would be a silent
// no-op that nonetheless reads as a configured protection.
func TestProtectedDatabasesIgnoresBlankEntries(t *testing.T) {
	townRoot := newProtectionTestTown(t, []string{"", "  ", "pc1"})

	protected, err := ProtectedDatabases(townRoot)
	if err != nil {
		t.Fatalf("ProtectedDatabases: %v", err)
	}
	if _, ok := protected[""]; ok {
		t.Error("a blank entry must not protect the empty database name")
	}
	if _, ok := protected["pc1"]; !ok {
		t.Error("blank entries must not stop the real ones from being read")
	}
}
