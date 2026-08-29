package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeMetadata writes a minimal .beads/metadata.json under dir naming db as
// the dolt_database, mirroring what bd init produces.
func writeMetadata(t *testing.T, beadsDir, db string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"dolt_database": db})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestProductionDatabasesReflectsReferencedRigs guards against gt-aac4: a
// hardcoded {hq, gt, mo} list named databases ("gt", "mo") that were never
// the real dolt_database name for any rig on this town, while missing real
// production databases like "beads", "duly_noted", and "gastown".
// productionDatabases must instead derive the set from what rigs actually
// reference, not a fixed guess.
func TestProductionDatabasesReflectsReferencedRigs(t *testing.T) {
	townRoot := t.TempDir()

	// Town-level HQ database.
	writeMetadata(t, filepath.Join(townRoot, ".beads"), "hq")

	// A rig whose dolt_database name does not match its directory name —
	// exactly the case the old hardcoded list got wrong.
	writeMetadata(t, filepath.Join(townRoot, "gastown", ".beads"), "gastown")
	writeMetadata(t, filepath.Join(townRoot, "duly_noted", ".beads"), "duly_noted")

	got := productionDatabases(townRoot)
	want := []string{"duly_noted", "gastown", "hq"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("productionDatabases(townRoot) = %v, want %v", got, want)
	}

	for _, phantom := range []string{"gt", "mo"} {
		for _, db := range got {
			if db == phantom {
				t.Errorf("productionDatabases(townRoot) contains phantom database %q not referenced by any rig", phantom)
			}
		}
	}
}
