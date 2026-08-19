package reaper

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// These tests cover the retention half of purge protection (gt-6xwt): protected
// wisp types are exported to a durable archive and only then deleted, so the
// protection stops being unbounded growth without becoming data loss again.
//
// The property that matters is an ORDERING one — record first, row second — and
// ordering is not directly observable from outside a transaction. What IS
// observable is the failure direction, so that is what these assert: when the
// archive refuses, the rows are all still there; when it accepts, the rows are
// gone and the record has the content. An implementation that deleted first
// cannot satisfy the first half.

// mrDescription is what a real merge-request wisp carries. branch, target and
// source_issue are NOT columns — the merge queue writes them as "key: value"
// lines in the description (internal/beads ParseMRFields), so an archive that
// dropped or reformatted this field would lose exactly the fields gt-6xwt names
// while still looking like it archived something.
const mrDescription = `branch: polecat/guzzle/gt-6xwt
target: main
source_issue: gt-6xwt
worker: guzzle
commit_sha: 0badc0ffee0badc0ffee0badc0ffee0badc0ffee

Rejected: gates were red on the rebased result.`

// recordingArchive is an Archiver that keeps what it was given and can be told
// to fail on a chosen call.
type recordingArchive struct {
	dir       string
	records   [][]ArchivedWisp
	failOnNth int // 1-based; 0 never fails
	calls     int
}

func (a *recordingArchive) ArchiveWisps(records []ArchivedWisp) error {
	a.calls++
	// Copy: the caller owns the slice and the test asserts on it later.
	batch := append([]ArchivedWisp(nil), records...)
	a.records = append(a.records, batch)
	if a.failOnNth > 0 && a.calls == a.failOnNth {
		return fmt.Errorf("archive unavailable (injected)")
	}
	return nil
}

func (a *recordingArchive) Location() string { return a.dir }

func (a *recordingArchive) all() []ArchivedWisp {
	var out []ArchivedWisp
	for _, batch := range a.records {
		out = append(out, batch...)
	}
	return out
}

func (a *recordingArchive) ids() []string {
	var ids []string
	for _, rec := range a.all() {
		ids = append(ids, rec.ID)
	}
	sort.Strings(ids)
	return ids
}

// TestPurgeArchivesProtectedWispsThenReleasesThem is the acceptance test for
// gt-6xwt.
//
// gt-nmg made ProtectedWispLabels mean "never deleted", which is right about the
// deletion and wrong about the ledger: MR wisps close at roughly 25/day on one
// busy rig, so the protection buys recoverability with rows nothing will ever
// remove. This asserts the trade is now "never deleted WITHOUT A RECORD FIRST".
//
// The NEGATIVE CONTROLS are what make it mean anything. w-pinned is protected by
// the column an incident responder sets by hand and must survive untouched — an
// archive that released everything protected would pass every assertion about
// w-mr and fail here. w-ordinary is an unprotected row of the same age that must
// still be purged outright, so a purge that stopped working cannot pass either.
func TestPurgeArchivesProtectedWispsThenReleasesThem(t *testing.T) {
	f := newFixture(t, "purge_archive_release")
	now := time.Now().UTC()
	oldClose := now.Add(-30 * 24 * time.Hour)

	f.insertWisps(t,
		wispRow{id: "w-mr", title: "MR: gt-6xwt retention", status: "closed",
			createdAt: oldClose, closedAt: &oldClose,
			description: mrDescription, closeReason: "rejected", assignee: "gastown/refinery",
			labels: []string{"gt:merge-request"}},
		// Protected by pin, not by type: never archived, never deleted.
		wispRow{id: "w-pinned", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			pinned: boolPtr(true), labels: []string{"gt:merge-request"}},
		// NEGATIVE CONTROL: same age, same status, ordinary label.
		wispRow{id: "w-ordinary", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			labels: []string{"gt:wisp"}},
		// Not yet past the cutoff: outside the window entirely.
		wispRow{id: "w-recent", status: "closed", createdAt: oldClose,
			closedAt: timePtr(now.Add(-1 * time.Hour)), labels: []string{"gt:merge-request"}},
	)
	f.insertWispComment(t, "w-mr", "witness: gates red on rebase, see run 4412")
	f.insertWispDependency(t, "wd-mr", "w-mr", "", "gt-6xwt", "parent-child")

	scan, err := Scan(f.db, f.dbName, purgeAge, purgeAge, purgeAge, staleAge)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scan.ProtectedFromPurge != 2 {
		t.Errorf("Scan.ProtectedFromPurge = %d, want 2 (w-mr, w-pinned)", scan.ProtectedFromPurge)
	}
	if scan.ArchivableFromPurge != 1 {
		t.Errorf("Scan.ArchivableFromPurge = %d, want 1 (w-mr only — the pinned row is not "+
			"archivable, or the count advertises a release the purge declines)", scan.ArchivableFromPurge)
	}

	archive := &recordingArchive{dir: "test://archive"}

	dry, err := Purge(f.db, f.dbName, purgeAge, purgeAge, true, WithArchive(archive))
	if err != nil {
		t.Fatalf("dry-run Purge: %v", err)
	}
	if dry.WispsArchived != 1 || dry.WispsPurged != 1 || dry.WispsProtected != 1 {
		t.Errorf("dry run archived/purged/protected = %d/%d/%d, want 1/1/1",
			dry.WispsArchived, dry.WispsPurged, dry.WispsProtected)
	}
	if archive.calls != 0 {
		t.Errorf("dry run called ArchiveWisps %d times, want 0 — a dry run that writes records "+
			"is not a preview", archive.calls)
	}
	if got := len(f.ids(t, "wisps")); got != 4 {
		t.Errorf("dry run left %d wisps, want 4", got)
	}

	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false, WithArchive(archive))
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}

	// The three counts partition the closed-past-cutoff window.
	if result.WispsArchived != 1 {
		t.Errorf("WispsArchived = %d, want 1", result.WispsArchived)
	}
	if result.WispsPurged != 1 {
		t.Errorf("WispsPurged = %d, want 1 (w-ordinary) — the ordinary purge must keep working, "+
			"or every protection assertion below passes for the wrong reason", result.WispsPurged)
	}
	if result.WispsProtected != 1 {
		t.Errorf("WispsProtected = %d, want 1 (w-pinned) — what the archive released must leave "+
			"the protected count, or the two numbers double-count", result.WispsProtected)
	}
	if len(result.Anomalies) != 0 {
		t.Errorf("unexpected anomalies: %+v", result.Anomalies)
	}

	// The ROW is released.
	wantWisps := []string{"w-pinned", "w-recent"}
	if got := f.ids(t, "wisps"); !reflect.DeepEqual(got, wantWisps) {
		t.Errorf("surviving wisps = %v, want %v", got, wantWisps)
	}
	// ...and so are its auxiliary rows: releasing the wisp while its labels and
	// comments stay behind would trade unbounded wisps for unbounded orphans.
	for _, table := range []string{"wisp_labels", "wisp_comments", "wisp_dependencies"} {
		for _, id := range f.issueIDs(t, table) {
			if id == "w-mr" {
				t.Errorf("%s still holds w-mr rows after release", table)
			}
		}
	}

	// The RECORD survives, with the content that made it worth protecting.
	if got := archive.ids(); !reflect.DeepEqual(got, []string{"w-mr"}) {
		t.Fatalf("archived ids = %v, want [w-mr] — the pinned row must not be exported", got)
	}
	rec := archive.all()[0]
	if rec.Database != f.dbName {
		t.Errorf("record Database = %q, want %q — a record that cannot say which server it came "+
			"from is not traceable back to anything", rec.Database, f.dbName)
	}
	if rec.Description != mrDescription {
		t.Errorf("record Description = %q, want the description verbatim — branch, target and "+
			"source_issue live in this field, not in columns", rec.Description)
	}
	for _, field := range []string{"branch: polecat/guzzle/gt-6xwt", "target: main", "source_issue: gt-6xwt"} {
		if !strings.Contains(rec.Description, field) {
			t.Errorf("record lost %q", field)
		}
	}
	if rec.Title != "MR: gt-6xwt retention" {
		t.Errorf("record Title = %q", rec.Title)
	}
	if rec.CloseReason != "rejected" {
		t.Errorf("record CloseReason = %q, want %q — the reason the work did not land is the "+
			"whole point of protecting this type", rec.CloseReason, "rejected")
	}
	if rec.Assignee != "gastown/refinery" {
		t.Errorf("record Assignee = %q", rec.Assignee)
	}
	// Within a second, not exact: the DATETIME column has no fractional part and
	// rounds on the way in, so an exact comparison here is a coin flip on the
	// sub-second value the fixture happened to insert, not a check of the archive.
	if rec.ClosedAt == nil || rec.ClosedAt.Sub(oldClose).Abs() > time.Second {
		t.Errorf("record ClosedAt = %v, want %v", rec.ClosedAt, oldClose)
	}
	if rec.ArchivedAt.IsZero() {
		t.Error("record ArchivedAt is zero — the export needs its own timestamp, closed_at is not it")
	}
	if !reflect.DeepEqual(rec.Labels, []string{"gt:merge-request"}) {
		t.Errorf("record Labels = %v, want [gt:merge-request]", rec.Labels)
	}
	if len(rec.Comments) != 1 || !strings.Contains(rec.Comments[0], "gates red on rebase") {
		t.Errorf("record Comments = %v — a comment is where a rationale that did not fit the "+
			"close reason ends up, so dropping it archives a husk", rec.Comments)
	}
	if len(rec.Dependencies) != 1 || rec.Dependencies[0].DependsOnExternal != "gt-6xwt" {
		t.Errorf("record Dependencies = %+v, want the parent-child edge to gt-6xwt", rec.Dependencies)
	}
}

// TestPurgeWithoutArchiveKeepsProtectionAbsolute pins the default. Retention is
// something a caller opts into; with no archive, ProtectedWispLabels means what
// it meant before this existed and nothing protected is deleted.
//
// It matters because every other caller in the tree — and every test above —
// relies on that default, so a change that made archiving implicit would weaken
// protection everywhere at once and silently.
func TestPurgeWithoutArchiveKeepsProtectionAbsolute(t *testing.T) {
	f := newFixture(t, "purge_no_archive")
	oldClose := time.Now().UTC().Add(-30 * 24 * time.Hour)
	f.insertWisps(t,
		wispRow{id: "w-mr", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			description: mrDescription, labels: []string{"gt:merge-request"}},
		wispRow{id: "w-ordinary", status: "closed", createdAt: oldClose, closedAt: &oldClose},
	)

	for _, tc := range []struct {
		name string
		opts []PurgeOption
	}{
		{"no option", nil},
		// A nil Archiver is the same as no option, so a caller that resolves an
		// archive which may be unavailable does not have to branch.
		{"nil archiver", []PurgeOption{WithArchive(nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false, tc.opts...)
			if err != nil {
				t.Fatalf("Purge: %v", err)
			}
			if result.WispsArchived != 0 {
				t.Errorf("WispsArchived = %d, want 0", result.WispsArchived)
			}
			if result.WispsProtected != 1 {
				t.Errorf("WispsProtected = %d, want 1", result.WispsProtected)
			}
			found := false
			for _, id := range f.ids(t, "wisps") {
				if id == "w-mr" {
					found = true
				}
			}
			if !found {
				t.Error("w-mr was deleted with no archive configured — protection must be absolute " +
					"unless a caller supplied somewhere for the record to go")
			}
		})
	}
}

// TestPurgeLeavesProtectedRowsWhenArchiveFails is the ordering instrument.
//
// The safety property is "record first, row second", which cannot be observed
// directly from outside the transaction — but its failure direction can. If the
// archive refuses and the rows are still there, then no deletion was committed
// ahead of a successful export. If this test ever fails, the retention path has
// become the data loss it replaced.
//
// The archive is failed on its SECOND call with more than one batch of rows in
// play, which also pins the all-or-nothing claim: batch one was accepted, and
// its rows must still come back because the run shares one write session.
func TestPurgeLeavesProtectedRowsWhenArchiveFails(t *testing.T) {
	f := newFixture(t, "purge_archive_fails")
	oldClose := time.Now().UTC().Add(-30 * 24 * time.Hour)

	total := DefaultBatchSize + 5
	rows := make([]wispRow, 0, total+1)
	var wantIDs []string
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("w-mr-%03d", i)
		rows = append(rows, wispRow{id: id, status: "closed", createdAt: oldClose, closedAt: &oldClose,
			description: mrDescription, labels: []string{"gt:merge-request"}})
		wantIDs = append(wantIDs, id)
	}
	// NEGATIVE CONTROL: the unprotected half is independent of the archive and
	// must still be purged. A purge that gave up entirely on an archive error
	// would leave this behind.
	rows = append(rows, wispRow{id: "w-ordinary", status: "closed", createdAt: oldClose, closedAt: &oldClose})
	f.insertWisps(t, rows...)

	archive := &recordingArchive{dir: "test://archive", failOnNth: 2}
	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false, WithArchive(archive))
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}

	if result.WispsArchived != 0 {
		t.Errorf("WispsArchived = %d, want 0 — a run that could not finish must not report rows "+
			"released, or the protected count it is subtracted from goes wrong", result.WispsArchived)
	}
	if result.WispsProtected != total {
		t.Errorf("WispsProtected = %d, want %d — everything the archive did not take is still "+
			"being held back", result.WispsProtected, total)
	}
	if result.WispsPurged != 1 {
		t.Errorf("WispsPurged = %d, want 1 — the ordinary purge does not depend on the archive",
			result.WispsPurged)
	}

	var anomaly bool
	for _, a := range result.Anomalies {
		if a.Type == "wisp_archive_failed" {
			anomaly = true
		}
	}
	if !anomaly {
		t.Errorf("no wisp_archive_failed anomaly in %+v — an archive that silently declines "+
			"looks exactly like an archive with nothing to do", result.Anomalies)
	}

	sort.Strings(wantIDs)
	got := f.ids(t, "wisps")
	if !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("surviving wisps = %d rows, want the %d protected ones intact (including the "+
			"batch the archive accepted before it failed)", len(got), len(wantIDs))
	}

	// The accepted batch is still in the archive. That is deliberate: the next
	// run re-exports and re-deletes those rows, so the archive may hold a
	// duplicate — the one direction of error this design accepts.
	if len(archive.all()) == 0 {
		t.Error("the archive recorded nothing at all; the fixture did not exercise the failure path")
	}
}

// TestPurgeArchivesEveryBatch drives the archive loop past DefaultBatchSize.
//
// The loop selects with a LIMIT and deletes inside an uncommitted session, so it
// only makes progress because the next SELECT runs on that same connection. A
// loop that queried the pool instead would return the same batch forever, and a
// loop that ran once would leave most of the accumulation in place — the exact
// failure this bead exists to prevent, wearing a success message.
func TestPurgeArchivesEveryBatch(t *testing.T) {
	f := newFixture(t, "purge_archive_batches")
	oldClose := time.Now().UTC().Add(-30 * 24 * time.Hour)

	total := DefaultBatchSize*2 + 7
	rows := make([]wispRow, 0, total)
	for i := 0; i < total; i++ {
		rows = append(rows, wispRow{
			id: fmt.Sprintf("w-mr-%04d", i), status: "closed",
			createdAt: oldClose, closedAt: &oldClose,
			description: mrDescription, labels: []string{"gt:merge-request"},
		})
	}
	f.insertWisps(t, rows...)

	archive := &recordingArchive{dir: "test://archive"}
	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false, WithArchive(archive))
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if result.WispsArchived != total {
		t.Errorf("WispsArchived = %d, want %d", result.WispsArchived, total)
	}
	if result.WispsProtected != 0 {
		t.Errorf("WispsProtected = %d, want 0", result.WispsProtected)
	}
	if len(archive.all()) != total {
		t.Errorf("archive received %d records, want %d — every released row needs a record, "+
			"not every batch", len(archive.all()), total)
	}
	if archive.calls < 3 {
		t.Errorf("ArchiveWisps calls = %d, want at least 3 — %d rows at a batch size of %d must "+
			"have driven the loop more than once", archive.calls, total, DefaultBatchSize)
	}
	if got := f.ids(t, "wisps"); len(got) != 0 {
		t.Errorf("%d wisps survived, want 0", len(got))
	}
}

// TestPurgeArchiveHonoursProtectedLabelList guards the mechanism, not today's
// list: retention follows ProtectedWispLabels, so a type added there is
// protected AND released by the same edit. A path that inlined
// 'gt:merge-request' passes the tests above and fails this one — and would
// quietly leave the next protected type accumulating forever.
func TestPurgeArchiveHonoursProtectedLabelList(t *testing.T) {
	original := ProtectedWispLabels
	ProtectedWispLabels = append(append([]string{}, original...), "gt:test-protected")
	t.Cleanup(func() { ProtectedWispLabels = original })

	f := newFixture(t, "purge_archive_label_list")
	oldClose := time.Now().UTC().Add(-30 * 24 * time.Hour)
	f.insertWisps(t,
		wispRow{id: "w-added", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			labels: []string{"gt:test-protected"}},
		wispRow{id: "w-control", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			labels: []string{"gt:wisp"}},
	)

	archive := &recordingArchive{dir: "test://archive"}
	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false, WithArchive(archive))
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if result.WispsArchived != 1 || result.WispsPurged != 1 {
		t.Errorf("archived/purged = %d/%d, want 1/1", result.WispsArchived, result.WispsPurged)
	}
	if got := archive.ids(); !reflect.DeepEqual(got, []string{"w-added"}) {
		t.Errorf("archived ids = %v, want [w-added] — adding a label to ProtectedWispLabels must "+
			"reach the retention path without editing any query", got)
	}
}

// TestPurgeArchiveWritesThroughFileArchive runs the real FileArchive end to end
// against the real SQL engine, then reads the records back the way an operator
// would.
//
// The recording archive above proves the reaper hands over the right records;
// only this proves the pair actually works — that what purge deleted can be
// found again on disk afterwards.
func TestPurgeArchiveWritesThroughFileArchive(t *testing.T) {
	f := newFixture(t, "purge_archive_file")
	dir := t.TempDir()
	oldClose := time.Now().UTC().Add(-30 * 24 * time.Hour)

	f.insertWisps(t,
		wispRow{id: "w-mr", title: "MR: retention", status: "closed",
			createdAt: oldClose, closedAt: &oldClose,
			description: mrDescription, closeReason: "rejected",
			labels: []string{"gt:merge-request"}},
	)

	archive, err := NewFileArchive(dir)
	if err != nil {
		t.Fatalf("NewFileArchive: %v", err)
	}
	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false, WithArchive(archive))
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if result.WispsArchived != 1 {
		t.Fatalf("WispsArchived = %d, want 1", result.WispsArchived)
	}
	if got := f.ids(t, "wisps"); len(got) != 0 {
		t.Fatalf("wisps survived: %v", got)
	}

	scan, err := ReadArchive(dir, ArchiveFilter{})
	if err != nil {
		t.Fatalf("ReadArchive: %v", err)
	}
	if len(scan.Records) != 1 {
		t.Fatalf("ReadArchive returned %d records, want 1 — the row is gone, so this file is the "+
			"only remaining copy", len(scan.Records))
	}
	rec := scan.Records[0]
	if rec.ID != "w-mr" || rec.CloseReason != "rejected" {
		t.Errorf("read back %+v", rec)
	}
	if !strings.Contains(rec.Description, "source_issue: gt-6xwt") {
		t.Errorf("read-back description lost its MR fields: %q", rec.Description)
	}

	// The filename says which database and which month, so a record can be
	// traced back without opening every file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive dir holds %d entries, want 1", len(entries))
	}
	wantName := fmt.Sprintf("%s-%s.jsonl", f.dbName, time.Now().UTC().Format("2006-01"))
	if entries[0].Name() != wantName {
		t.Errorf("archive file = %q, want %q", entries[0].Name(), wantName)
	}
}

// TestFileArchiveAppendsAndSurvivesReopen covers the durability contract the
// purge path leans on: ArchiveWisps returning nil means the record is on disk,
// and a second call adds to the file rather than replacing it.
//
// Append-vs-truncate is worth its own assertion because the failure is silent
// and total: an O_TRUNC open would keep passing every single-record test in this
// file while destroying the entire history on each purge.
func TestFileArchiveAppendsAndSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	archive, err := NewFileArchive(dir)
	if err != nil {
		t.Fatalf("NewFileArchive: %v", err)
	}
	stamp := time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)

	if err := archive.ArchiveWisps([]ArchivedWisp{
		{ID: "w-1", Database: "gastown", Title: "first", ArchivedAt: stamp},
	}); err != nil {
		t.Fatalf("first ArchiveWisps: %v", err)
	}
	// A separate instance, as a later purge run would be.
	second, err := NewFileArchive(dir)
	if err != nil {
		t.Fatalf("second NewFileArchive: %v", err)
	}
	if err := second.ArchiveWisps([]ArchivedWisp{
		{ID: "w-2", Database: "gastown", Title: "second", ArchivedAt: stamp},
		{ID: "w-3", Database: "hq", Title: "other db", ArchivedAt: stamp},
	}); err != nil {
		t.Fatalf("second ArchiveWisps: %v", err)
	}

	scan, err := ReadArchive(dir, ArchiveFilter{})
	if err != nil {
		t.Fatalf("ReadArchive: %v", err)
	}
	if len(scan.Records) != 3 {
		t.Fatalf("read %d records, want 3 — a second write must append, not replace", len(scan.Records))
	}
	if scan.Files != 2 {
		t.Errorf("read %d files, want 2 (one per database)", scan.Files)
	}

	// Records are separated by database, so one rig's archive is readable
	// without wading through another's.
	gastownPath := filepath.Join(dir, "gastown-2026-08.jsonl")
	data, err := os.ReadFile(gastownPath)
	if err != nil {
		t.Fatalf("read %s: %v", gastownPath, err)
	}
	if lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; lines != 2 {
		t.Errorf("%s holds %d lines, want 2", gastownPath, lines)
	}
}

// TestReadArchiveFilters covers the read surface. An archive nobody can search
// is a hole with a nicer name: the record was kept so somebody hunting a merge
// that did not land, months later, can find the rationale.
func TestReadArchiveFilters(t *testing.T) {
	dir := t.TempDir()
	archive, err := NewFileArchive(dir)
	if err != nil {
		t.Fatalf("NewFileArchive: %v", err)
	}
	base := time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)
	records := []ArchivedWisp{
		{ID: "w-old", Database: "gastown", Title: "old one", ArchivedAt: base},
		{ID: "w-new", Database: "gastown", Title: "new one", ArchivedAt: base.Add(time.Hour),
			Description: "source_issue: gt-6xwt"},
		{ID: "w-hq", Database: "hq", Title: "hq one", ArchivedAt: base.Add(2 * time.Hour),
			CloseReason: "superseded"},
	}
	if err := archive.ArchiveWisps(records); err != nil {
		t.Fatalf("ArchiveWisps: %v", err)
	}

	t.Run("newest first", func(t *testing.T) {
		scan, err := ReadArchive(dir, ArchiveFilter{})
		if err != nil {
			t.Fatalf("ReadArchive: %v", err)
		}
		var ids []string
		for _, rec := range scan.Records {
			ids = append(ids, rec.ID)
		}
		if !reflect.DeepEqual(ids, []string{"w-hq", "w-new", "w-old"}) {
			t.Errorf("ids = %v, want newest first", ids)
		}
	})

	t.Run("by database", func(t *testing.T) {
		scan, err := ReadArchive(dir, ArchiveFilter{Database: "hq"})
		if err != nil {
			t.Fatalf("ReadArchive: %v", err)
		}
		if len(scan.Records) != 1 || scan.Records[0].ID != "w-hq" {
			t.Errorf("records = %+v", scan.Records)
		}
	})

	t.Run("by id", func(t *testing.T) {
		scan, err := ReadArchive(dir, ArchiveFilter{ID: "w-old"})
		if err != nil {
			t.Fatalf("ReadArchive: %v", err)
		}
		if len(scan.Records) != 1 || scan.Records[0].ID != "w-old" {
			t.Errorf("records = %+v", scan.Records)
		}
	})

	t.Run("substring reaches the description", func(t *testing.T) {
		// The MR fields an operator searches by are inside the description, so
		// a substring search that only looked at titles would never find them.
		scan, err := ReadArchive(dir, ArchiveFilter{Contains: "GT-6XWT"})
		if err != nil {
			t.Fatalf("ReadArchive: %v", err)
		}
		if len(scan.Records) != 1 || scan.Records[0].ID != "w-new" {
			t.Errorf("records = %+v, want w-new matched case-insensitively inside its description",
				scan.Records)
		}
	})

	t.Run("limit keeps the newest", func(t *testing.T) {
		scan, err := ReadArchive(dir, ArchiveFilter{Limit: 1})
		if err != nil {
			t.Fatalf("ReadArchive: %v", err)
		}
		if len(scan.Records) != 1 || scan.Records[0].ID != "w-hq" {
			t.Errorf("records = %+v", scan.Records)
		}
	})
}

// TestReadArchiveReportsMalformedLines: a corrupt archive must not read as an
// empty one. Silently skipping a damaged line would answer "no such record" for
// a record that is right there, which is the one answer this whole mechanism
// exists to never give.
func TestReadArchiveReportsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gastown-2026-08.jsonl")
	content := `{"id":"w-good","database":"gastown","title":"fine","archived_at":"2026-08-19T06:00:00Z"}
{"id":"w-truncated","datab
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	scan, err := ReadArchive(dir, ArchiveFilter{})
	if err != nil {
		t.Fatalf("ReadArchive: %v", err)
	}
	if len(scan.Records) != 1 || scan.Records[0].ID != "w-good" {
		t.Errorf("records = %+v, want the readable one", scan.Records)
	}
	if scan.Malformed != 1 {
		t.Errorf("Malformed = %d, want 1 — an unreadable line has to be counted, or a damaged "+
			"archive reads as a clean empty one", scan.Malformed)
	}
}

// TestReadArchiveMissingDirIsEmptyNotAnError: nothing has been archived yet is
// an ordinary state, not a failure. `gt reaper archive` on a fresh town must
// print "nothing here", not an error an operator has to interpret.
func TestReadArchiveMissingDirIsEmptyNotAnError(t *testing.T) {
	scan, err := ReadArchive(filepath.Join(t.TempDir(), "never-created"), ArchiveFilter{})
	if err != nil {
		t.Fatalf("ReadArchive on a missing dir: %v", err)
	}
	if len(scan.Records) != 0 || scan.Files != 0 {
		t.Errorf("scan = %+v, want empty", scan)
	}
}

// TestDefaultArchiveDirResolution pins the precedence. It matters because the
// CLI and the daemon both call this function and must land in the SAME
// directory: two resolutions would mean two archives, and a record written by
// one and searched for through the other reads as missing.
func TestDefaultArchiveDirResolution(t *testing.T) {
	t.Setenv(ArchiveDirEnv, "")
	t.Setenv("GT_HOME", "")

	t.Run("explicit override wins", func(t *testing.T) {
		t.Setenv(ArchiveDirEnv, "/tmp/explicit-archive")
		t.Setenv("GT_HOME", "/tmp/town")
		if got := DefaultArchiveDir(); got != "/tmp/explicit-archive" {
			t.Errorf("DefaultArchiveDir() = %q", got)
		}
	})

	t.Run("GT_HOME keeps data with the workspace", func(t *testing.T) {
		t.Setenv(ArchiveDirEnv, "")
		t.Setenv("GT_HOME", "/tmp/town")
		want := filepath.Join("/tmp/town", ".gt", "wisp-archive")
		if got := DefaultArchiveDir(); got != want {
			t.Errorf("DefaultArchiveDir() = %q, want %q", got, want)
		}
	})

	t.Run("falls back to the home directory", func(t *testing.T) {
		t.Setenv(ArchiveDirEnv, "")
		t.Setenv("GT_HOME", "")
		got := DefaultArchiveDir()
		if !strings.HasSuffix(got, filepath.Join(".gt", "wisp-archive")) {
			t.Errorf("DefaultArchiveDir() = %q, want a .gt/wisp-archive path", got)
		}
	})
}

func timePtr(t time.Time) *time.Time { return &t }
