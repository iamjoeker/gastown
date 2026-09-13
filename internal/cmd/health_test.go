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

// TestCheckBackupHealthReportsUnconfiguredNotFresh guards against gt-zdvg: a
// town with no backups at all must never report dolt_stale/jsonl_stale as
// false, since that reads as "backups are fresh" when no backup exists.
func TestCheckBackupHealthReportsUnconfiguredNotFresh(t *testing.T) {
	townRoot := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	bh := checkBackupHealth(townRoot)

	if bh.DoltStatus != BackupStatusUnconfigured {
		t.Errorf("DoltStatus = %q, want %q", bh.DoltStatus, BackupStatusUnconfigured)
	}
	if !bh.DoltStale {
		t.Errorf("DoltStale = false for a town with no backups; must never read as fresh")
	}
	if bh.JSONLStatus != BackupStatusUnconfigured {
		t.Errorf("JSONLStatus = %q, want %q", bh.JSONLStatus, BackupStatusUnconfigured)
	}
	if !bh.JSONLStale {
		t.Errorf("JSONLStale = false for a town with no backups; must never read as fresh")
	}
}

// TestCheckBackupHealthReportsFreshWhenRecent guards against the fix
// over-correcting: a genuinely fresh backup must still report as fresh, not
// as unconfigured or stale.
func TestCheckBackupHealthReportsFreshWhenRecent(t *testing.T) {
	townRoot := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	backupDir := filepath.Join(townRoot, ".dolt-backup")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "snapshot.sql"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	bh := checkBackupHealth(townRoot)

	if bh.DoltStatus != BackupStatusFresh {
		t.Errorf("DoltStatus = %q, want %q", bh.DoltStatus, BackupStatusFresh)
	}
	if bh.DoltStale {
		t.Errorf("DoltStale = true for a backup written moments ago")
	}
}
