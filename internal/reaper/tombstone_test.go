package reaper

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteReadTombstonesRoundTrip is the same property archive_behavior_test.go
// asserts for ArchivedWisp: what is written is what comes back, including the
// aux-row counts a Tombstone exists to carry.
func TestWriteReadTombstonesRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tombstones")
	purgedAt := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	records := []Tombstone{
		{PurgedAt: purgedAt, Database: "gastown", ID: "gt-wisp-a1", WispType: "step",
			AuxCounts: map[string]int{"wisp_labels": 2, "wisp_events": 4}},
		{PurgedAt: purgedAt, Database: "gastown", ID: "gt-wisp-a2", WispType: "sling-context",
			AuxCounts: map[string]int{}},
	}
	if err := WriteTombstones(dir, records); err != nil {
		t.Fatalf("WriteTombstones: %v", err)
	}

	scan, err := ReadTombstones(dir, TombstoneFilter{})
	if err != nil {
		t.Fatalf("ReadTombstones: %v", err)
	}
	if len(scan.Records) != 2 {
		t.Fatalf("records = %d, want 2: %+v", len(scan.Records), scan.Records)
	}
	rec, ok := findTombstone(scan.Records, "gt-wisp-a1")
	if !ok {
		t.Fatal("gt-wisp-a1 not found in scan")
	}
	if rec.WispType != "step" || rec.AuxCounts["wisp_labels"] != 2 || rec.AuxCounts["wisp_events"] != 4 {
		t.Errorf("gt-wisp-a1 = %+v, want wisp_type=step aux_counts={labels:2,events:4}", rec)
	}

	// A missing directory means "nothing tombstoned yet", not an error — the
	// same contract ReadArchive gives ArchivedWisp readers.
	empty, err := ReadTombstones(filepath.Join(t.TempDir(), "nope"), TombstoneFilter{})
	if err != nil {
		t.Fatalf("ReadTombstones on missing dir: %v", err)
	}
	if len(empty.Records) != 0 {
		t.Errorf("missing dir records = %d, want 0", len(empty.Records))
	}
}

// TestReadTombstonesFilters exercises the ID and Database filters, the ones a
// `gt reaper archive --id` lookup actually depends on.
func TestReadTombstonesFilters(t *testing.T) {
	dir := t.TempDir()
	purgedAt := time.Now().UTC()
	records := []Tombstone{
		{PurgedAt: purgedAt, Database: "gastown", ID: "gt-wisp-1", AuxCounts: map[string]int{}},
		{PurgedAt: purgedAt, Database: "beads", ID: "gt-wisp-2", AuxCounts: map[string]int{}},
	}
	if err := WriteTombstones(dir, records); err != nil {
		t.Fatalf("WriteTombstones: %v", err)
	}

	scan, err := ReadTombstones(dir, TombstoneFilter{ID: "gt-wisp-2"})
	if err != nil {
		t.Fatalf("ReadTombstones: %v", err)
	}
	if len(scan.Records) != 1 || scan.Records[0].ID != "gt-wisp-2" {
		t.Fatalf("ID filter = %+v, want exactly gt-wisp-2", scan.Records)
	}

	scan, err = ReadTombstones(dir, TombstoneFilter{Database: "gastown"})
	if err != nil {
		t.Fatalf("ReadTombstones: %v", err)
	}
	if len(scan.Records) != 1 || scan.Records[0].ID != "gt-wisp-1" {
		t.Fatalf("Database filter = %+v, want exactly gt-wisp-1", scan.Records)
	}
}

// TestReadTombstonesMalformedLine matches ArchiveScan's contract: a corrupt
// store must not read as an empty one.
func TestReadTombstonesMalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gastown-2026-08.jsonl")
	if err := WriteTombstones(dir, []Tombstone{
		{PurgedAt: time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC), Database: "gastown", ID: "gt-wisp-1", AuxCounts: map[string]int{}},
	}); err != nil {
		t.Fatalf("WriteTombstones: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString("{not json\n"); err != nil {
		t.Fatalf("append malformed line: %v", err)
	}
	f.Close()

	scan, err := ReadTombstones(dir, TombstoneFilter{})
	if err != nil {
		t.Fatalf("ReadTombstones: %v", err)
	}
	if scan.Malformed != 1 {
		t.Errorf("Malformed = %d, want 1", scan.Malformed)
	}
	if len(scan.Records) != 1 {
		t.Errorf("Records = %d, want 1 (the malformed line dropped, not the whole file)", len(scan.Records))
	}
}

// TestReadArchiveIgnoresTombstoneSubdir is the property the WHERE design
// question in gt-60ju turned on: ReadArchive must not decode tombstone
// records as half-populated ArchivedWisp rows. Placing tombstones in their own
// subdirectory relies on ReadArchive already skipping directories — this test
// pins that behavior so a future change to ReadArchive that started
// recursing would be caught here, not by a padded archive scan in production.
func TestReadArchiveIgnoresTombstoneSubdir(t *testing.T) {
	archiveDir := t.TempDir()
	if err := appendJSONL(filepath.Join(archiveDir, "gastown-2026-08.jsonl"), []ArchivedWisp{
		{ArchivedAt: time.Now().UTC(), Database: "gastown", ID: "gt-wisp-real", Title: "real archive record", Status: "closed"},
	}); err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	tombDir := tombstoneDirFor(archiveDir)
	if err := WriteTombstones(tombDir, []Tombstone{
		{PurgedAt: time.Now().UTC(), Database: "gastown", ID: "gt-wisp-tomb", AuxCounts: map[string]int{}},
	}); err != nil {
		t.Fatalf("WriteTombstones: %v", err)
	}

	scan, err := ReadArchive(archiveDir, ArchiveFilter{})
	if err != nil {
		t.Fatalf("ReadArchive: %v", err)
	}
	if len(scan.Records) != 1 {
		t.Fatalf("ReadArchive records = %+v, want exactly the one real archive record, "+
			"none decoded from the tombstone subdirectory", scan.Records)
	}
	if scan.Records[0].ID != "gt-wisp-real" {
		t.Errorf("ReadArchive record = %+v, want gt-wisp-real", scan.Records[0])
	}
}

func TestPruneTombstonesRemovesOnlyStaleMonths(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)

	old := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := WriteTombstones(dir, []Tombstone{
		{PurgedAt: old, Database: "gastown", ID: "gt-wisp-old", AuxCounts: map[string]int{}},
		{PurgedAt: recent, Database: "gastown", ID: "gt-wisp-recent", AuxCounts: map[string]int{}},
	}); err != nil {
		t.Fatalf("WriteTombstones: %v", err)
	}

	removed, err := PruneTombstones(dir, 90*24*time.Hour, now)
	if err != nil {
		t.Fatalf("PruneTombstones: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	scan, err := ReadTombstones(dir, TombstoneFilter{})
	if err != nil {
		t.Fatalf("ReadTombstones after prune: %v", err)
	}
	if len(scan.Records) != 1 || scan.Records[0].ID != "gt-wisp-recent" {
		t.Fatalf("survivors = %+v, want only gt-wisp-recent", scan.Records)
	}

	// Idempotent: pruning again removes nothing further.
	removed, err = PruneTombstones(dir, 90*24*time.Hour, now)
	if err != nil {
		t.Fatalf("PruneTombstones (second pass): %v", err)
	}
	if removed != 0 {
		t.Errorf("second-pass removed = %d, want 0", removed)
	}
}

func TestPruneTombstonesMissingDir(t *testing.T) {
	removed, err := PruneTombstones(filepath.Join(t.TempDir(), "nope"), 24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("PruneTombstones on missing dir: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

// TestPurgeTombstonesUnprotectedWispsBeforeDeleting is the integration case
// gt-60ju exists for: purgeClosedWisps' unprotected half gets NO ArchivedWisp
// record (by design — see the file comment in tombstone.go), but it must
// still leave a per-id answer to "was id X purged". This asserts the
// tombstone is written, describes what was actually deleted (wisp_type and
// aux-row counts), and that the protected half is unaffected.
func TestPurgeTombstonesUnprotectedWispsBeforeDeleting(t *testing.T) {
	f := newFixture(t, "purge_tombstone")
	now := time.Now().UTC()
	oldClose := now.Add(-30 * 24 * time.Hour)

	f.insertWisps(t,
		// Unprotected: no protected label, past the cutoff. Gets tombstoned.
		wispRow{id: "w-unprotected", status: "closed", wispType: "step",
			createdAt: oldClose, closedAt: &oldClose},
		// Protected by type: archived, not tombstoned — the two mechanisms
		// must not double up on the same row.
		wispRow{id: "w-mr", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			labels: []string{"gt:merge-request"}},
	)
	f.insertWispAux(t, "w-unprotected") // one row each: labels(2 total incl. gt:wisp), comments, events
	f.insertWispComment(t, "w-unprotected", "second comment")

	archiveRoot := t.TempDir()
	archive := &recordingArchive{dir: archiveRoot}
	tombDir := tombstoneDirFor(archiveRoot)

	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false, WithArchive(archive), WithTombstoneDir(tombDir))
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if result.WispsPurged != 1 {
		t.Fatalf("WispsPurged = %d, want 1 (w-unprotected)", result.WispsPurged)
	}
	if result.WispsArchived != 1 {
		t.Fatalf("WispsArchived = %d, want 1 (w-mr)", result.WispsArchived)
	}
	if result.WispsTombstoned != 1 {
		t.Fatalf("WispsTombstoned = %d, want 1 (w-unprotected) — the unprotected half must leave "+
			"exactly one per-id record, or the audit question this bead exists for still has no "+
			"answer", result.WispsTombstoned)
	}
	if len(result.Anomalies) != 0 {
		t.Errorf("Anomalies = %+v, want none", result.Anomalies)
	}
	if got := len(f.ids(t, "wisps")); got != 0 {
		t.Errorf("surviving wisps = %d, want 0 — both should have been purged/archived-and-released", got)
	}

	scan, err := ReadTombstones(tombDir, TombstoneFilter{})
	if err != nil {
		t.Fatalf("ReadTombstones: %v", err)
	}
	if len(scan.Records) != 1 {
		t.Fatalf("tombstone records = %+v, want exactly 1", scan.Records)
	}
	rec := scan.Records[0]
	if rec.ID != "w-unprotected" {
		t.Errorf("tombstoned id = %q, want w-unprotected — a tombstone for the ARCHIVED row would "+
			"be a duplicate record for the wrong half of the partition", rec.ID)
	}
	if rec.Database != f.dbName {
		t.Errorf("tombstoned database = %q, want %q", rec.Database, f.dbName)
	}
	if rec.WispType != "step" {
		t.Errorf("tombstoned wisp_type = %q, want step", rec.WispType)
	}
	// insertWispAux adds one row each to wisp_labels/comments/events;
	// insertWispComment above added a second comment.
	wantAux := map[string]int{"wisp_labels": 1, "wisp_comments": 2, "wisp_events": 1}
	for tbl, want := range wantAux {
		if got := rec.AuxCounts[tbl]; got != want {
			t.Errorf("AuxCounts[%s] = %d, want %d (%+v)", tbl, got, want, rec.AuxCounts)
		}
	}
	if rec.PurgedAt.IsZero() {
		t.Error("PurgedAt is zero")
	}

	// The tombstone directory must not be visible to the protected-wisp
	// archive reader — the WHERE design question this bead resolved.
	archiveScan, err := ReadArchive(archive.Location(), ArchiveFilter{})
	if err != nil {
		t.Fatalf("ReadArchive: %v", err)
	}
	for _, arec := range archiveScan.Records {
		if arec.ID == "w-unprotected" {
			t.Errorf("w-unprotected appeared in the protected-wisp archive scan; the tombstone "+
				"subdirectory leaked into ReadArchive: %+v", arec)
		}
	}
}

// TestPurgeWithoutTombstoneDirTouchesNoFilesystem pins the fix for a real bug
// caught while writing this feature: an Archiver's Location() is a label for
// operator output (see the doc on the Archiver interface), not a contract
// that it is a writable directory — this package's own recordingArchive test
// double uses "test://archive", a string that is a plausible-looking but
// bogus relative path. An earlier version of purgeClosedWisps derived the
// tombstone directory FROM archive.Location() automatically, so every
// existing archive_behavior_test.go test that purged an unprotected wisp
// alongside a protected one silently created a "test:/archive/tombstones/..."
// directory under the package's own source tree. WithTombstoneDir exists so
// tombstoning is always an explicit, separate opt-in with its own real path —
// this test asserts that WithArchive alone, with no WithTombstoneDir, writes
// nothing to disk even when Location() is not a usable path at all.
func TestPurgeWithoutTombstoneDirTouchesNoFilesystem(t *testing.T) {
	f := newFixture(t, "purge_no_tombstone_dir")
	now := time.Now().UTC()
	oldClose := now.Add(-30 * 24 * time.Hour)

	f.insertWisps(t,
		wispRow{id: "w-unprotected", status: "closed", createdAt: oldClose, closedAt: &oldClose},
	)

	cwdBefore, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	bogusDir := filepath.Join(cwdBefore, "test:", "archive")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(cwdBefore, "test:")) })

	archive := &recordingArchive{dir: "test://archive"}
	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false, WithArchive(archive))
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if result.WispsPurged != 1 {
		t.Fatalf("WispsPurged = %d, want 1", result.WispsPurged)
	}
	if result.WispsTombstoned != 0 {
		t.Errorf("WispsTombstoned = %d, want 0 — WithArchive alone must not turn on tombstoning", result.WispsTombstoned)
	}
	if _, err := os.Stat(bogusDir); !os.IsNotExist(err) {
		t.Errorf("archive.Location()-derived path %s exists (err=%v), want no such file — "+
			"purge must not write to a directory it derived from an Archiver's Location() unless "+
			"WithTombstoneDir asked for it explicitly", bogusDir, err)
	}
}

func findTombstone(records []Tombstone, id string) (Tombstone, bool) {
	for _, r := range records {
		if r.ID == id {
			return r, true
		}
	}
	return Tombstone{}, false
}
