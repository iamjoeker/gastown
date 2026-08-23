package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/reaper"
)

// ---------------------------------------------------------------------------
// gt-hv3p: an interrupted delete pass is still enumerable
// ---------------------------------------------------------------------------

// failingArchive is an Archiver that cannot keep a record. The whole safety
// property is what compaction does when it meets one.
type failingArchive struct{ calls int }

func (a *failingArchive) ArchiveWisps(records []reaper.ArchivedWisp) error {
	a.calls++
	return fmt.Errorf("disk full")
}
func (a *failingArchive) Location() string { return "/nowhere" }

// recordingArchive keeps what it was handed, in the order it was handed it.
type recordingArchive struct{ records []reaper.ArchivedWisp }

func (a *recordingArchive) ArchiveWisps(records []reaper.ArchivedWisp) error {
	a.records = append(a.records, records...)
	return nil
}
func (a *recordingArchive) Location() string { return "/archive" }

func TestArchiveDeletionsCancelsDeletionWhenTheRecordCannotBeWritten(t *testing.T) {
	t.Parallel()

	pending := []*compactIssue{
		{Issue: beads.Issue{ID: "gt-wisp-a", Title: "patrol cycle", Status: "closed"}, WispType: "patrol"},
	}
	result := &compactResult{}
	archive := &failingArchive{}

	if archiveDeletionsTo(nil, "gastown", pending, time.Now().UTC(), archive, result) {
		t.Fatal("archiveDeletionsTo returned true after the archive failed — the caller " +
			"would then delete a wisp that nothing anywhere records, which is gt-hv3p")
	}
	if archive.calls != 1 {
		t.Errorf("ArchiveWisps calls = %d, want 1", archive.calls)
	}
	if result.Archived != 0 || result.ArchivedTo != "" {
		t.Errorf("Archived/ArchivedTo = %d/%q, want 0/\"\" — a failed archive must not "+
			"be reported as a kept record", result.Archived, result.ArchivedTo)
	}
	if !containsSubstring(result.Errors, "held, not deleted") {
		t.Errorf("Errors = %v, want one that says the wisps were held", result.Errors)
	}
}

func TestArchiveDeletionsRecordsEveryPendingWisp(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 22, 0, 26, 0, 0, time.UTC)
	pending := []*compactIssue{
		{Issue: beads.Issue{
			ID:        "gt-wisp-a",
			Title:     "heartbeat",
			Status:    "closed",
			Type:      "chore",
			CreatedAt: "2026-08-01T00:00:00Z",
			UpdatedAt: "2026-08-02T00:00:00Z",
			Labels:    []string{"gt:heartbeat"},
			Parent:    "gt-wisp-mol",
		}, WispType: "heartbeat"},
		{Issue: beads.Issue{ID: "gt-wisp-b", Title: "patrol", Status: "closed"}, WispType: "patrol"},
	}
	result := &compactResult{}
	archive := &recordingArchive{}

	if !archiveDeletionsTo(nil, "gastown", pending, now, archive, result) {
		t.Fatalf("archiveDeletionsTo = false, want true; errors: %v", result.Errors)
	}
	if len(archive.records) != len(pending) {
		t.Fatalf("archived %d records for %d pending deletions — every wisp about to be "+
			"deleted needs one, or the interrupted run is unenumerable again",
			len(archive.records), len(pending))
	}
	if result.Archived != 2 || result.ArchivedTo != "/archive" {
		t.Errorf("Archived/ArchivedTo = %d/%q, want 2/\"/archive\"", result.Archived, result.ArchivedTo)
	}

	rec := archive.records[0]
	if rec.ID != "gt-wisp-a" || rec.Database != "gastown" || rec.WispType != "heartbeat" {
		t.Errorf("record = %+v, want the wisp's id, database and type", rec)
	}
	if !rec.ArchivedAt.Equal(now) {
		t.Errorf("ArchivedAt = %v, want %v", rec.ArchivedAt, now)
	}
	if rec.CreatedAt == nil || !rec.CreatedAt.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAt = %v, want the wisp's own timestamp", rec.CreatedAt)
	}
	if len(rec.Labels) != 1 || rec.Labels[0] != "gt:heartbeat" {
		t.Errorf("Labels = %v, want the wisp's labels", rec.Labels)
	}
	// A molecule step is deleted even when it carries comments, so the edge
	// naming the molecule it belonged to is part of the record.
	if len(rec.Dependencies) != 1 || rec.Dependencies[0].DependsOnWispID != "gt-wisp-mol" {
		t.Errorf("Dependencies = %+v, want the parent edge", rec.Dependencies)
	}
}

// TestArchiveRecordSurvivesUnreadableEnrichment covers the ranking between the
// two failures: a record missing its description is a worse record, a wisp with
// no record at all is the incident. buildArchiveRecords must still return one
// record per wisp when the enrichment query cannot be run, and must say so.
func TestArchiveRecordSurvivesUnreadableEnrichment(t *testing.T) {
	t.Parallel()

	pending := []*compactIssue{
		{Issue: beads.Issue{ID: "gt-wisp-a", Title: "patrol", Status: "closed"}, WispType: "patrol"},
	}
	result := &compactResult{}

	// bd == nil makes the enrichment query unrunnable.
	records := buildArchiveRecords(nil, "gastown", pending, time.Now().UTC(), result)

	if len(records) != 1 || records[0].ID != "gt-wisp-a" {
		t.Fatalf("records = %+v, want one record for the pending wisp", records)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "archive") {
		t.Errorf("Errors = %v, want the degraded record reported, not swallowed", result.Errors)
	}
}

func TestArchiveRecordForMergesTheStoredRow(t *testing.T) {
	t.Parallel()

	w := &compactIssue{Issue: beads.Issue{
		ID:        "gt-wisp-a",
		Title:     "stale title",
		Status:    "closed",
		CreatedAt: "2026-08-01T00:00:00Z",
	}}
	row := &archiveRow{
		ID:          "gt-wisp-a",
		Title:       "MR: polecat/chrome/gt-hv3p",
		Description: "branch: polecat/chrome\ntarget: main",
		CloseReason: "rejected: gates red",
		Priority:    1,
		Status:      "closed",
		IssueType:   "merge-request",
		WispType:    "merge_request",
		ClosedAt:    "2026-08-22 00:26:00",
	}

	rec := archiveRecordFor(w, row, "gastown", time.Now().UTC())

	// The description is the point of archiving an MR wisp at all: branch,
	// target and source_issue live in there as "key: value" lines.
	if !strings.Contains(rec.Description, "branch: polecat/chrome") {
		t.Errorf("Description = %q, want the stored description", rec.Description)
	}
	if rec.CloseReason != "rejected: gates red" || rec.Priority != 1 {
		t.Errorf("record = %+v, want the stored close reason and priority", rec)
	}
	if rec.Title != "MR: polecat/chrome/gt-hv3p" {
		t.Errorf("Title = %q, want the stored title", rec.Title)
	}
	// The space-separated layout is what bd emits once an expression wraps a
	// datetime; refusing it would silently drop the field.
	if rec.ClosedAt == nil || !rec.ClosedAt.Equal(time.Date(2026, 8, 22, 0, 26, 0, 0, time.UTC)) {
		t.Errorf("ClosedAt = %v, want the space-separated timestamp parsed", rec.ClosedAt)
	}
}

// ---------------------------------------------------------------------------
// End to end, through runCompact
// ---------------------------------------------------------------------------

// TestCompactWritesTheRecordBeforeItDeletes is the ordering assertion, made
// from inside the delete itself: the fake bd snapshots the archive at the
// moment `bd delete` is invoked. A run that archived afterwards — or in the
// same breath — leaves that snapshot empty.
//
// This is the property that makes an interrupted pass enumerable: the archive
// is always a superset of the casualties, so the exact set is the archived ids
// minus the ids still in `wisps`.
func TestCompactWritesTheRecordBeforeItDeletes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}
	env := setupCompactStubs(t)

	if err := runCompact(nil, nil); err != nil {
		t.Fatalf("runCompact() = %v, want nil", err)
	}

	if got := readStubLog(t, env.deleteLog); len(got) != 1 || !strings.Contains(got[0], "gt-wisp-doomed") {
		t.Fatalf("deleted = %v, want exactly [gt-wisp-doomed] — without a deletion this "+
			"test proves nothing about ordering", got)
	}

	snapshot, err := os.ReadFile(env.archiveSnapshot)
	if err != nil {
		t.Fatalf("archive snapshot taken during `bd delete`: %v", err)
	}
	if !strings.Contains(string(snapshot), "gt-wisp-doomed") {
		t.Errorf("the archive did not yet contain gt-wisp-doomed when it was deleted:\n%s\n"+
			"An interrupted run at this instant leaves a deletion nothing records — gt-hv3p",
			string(snapshot))
	}
	// And the record is the real one, not a stub: the description is what an
	// operator reconstructs the wisp from.
	if !strings.Contains(string(snapshot), "the payload that would have been lost") {
		t.Errorf("archived record carries no description:\n%s", string(snapshot))
	}
}

// TestCompactHoldsDeletionsWhenTheArchiveIsUnwritable proves the run fails
// safe. A wisp not deleted today is deleted tomorrow; a wisp deleted today with
// no record is gone, and wisps are dolt-ignored — no AS OF, no backup.
func TestCompactHoldsDeletionsWhenTheArchiveIsUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}
	env := setupCompactStubs(t)

	// A regular file where the archive directory should be: MkdirAll fails, so
	// the archive cannot be opened at all.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	t.Setenv(reaper.ArchiveDirEnv, blocked)

	if err := runCompact(nil, nil); err != nil {
		t.Fatalf("runCompact() = %v, want nil (an unwritable archive holds wisps, it is "+
			"not a fatal error)", err)
	}

	if got := readStubLog(t, env.deleteLog); len(got) != 0 {
		t.Errorf("bd delete ran %v with no archive to record it", got)
	}
}

// TestHeldWispsStillAccountForEveryScannedRow guards the summary's own
// invariant against the hold this change introduces. An archive failure holds
// N wisps with ONE error message, so an accounting that counted error MESSAGES
// as wisps would declare a correct summary incomplete — and the warning that
// says "these counts cannot be trusted" is worth more than the case it fires on.
func TestHeldWispsStillAccountForEveryScannedRow(t *testing.T) {
	t.Parallel()

	held := &compactResult{
		Scanned:   3,
		Skipped:   2,
		Protected: []compactAction{{ID: "gt-wisp-a", Reason: "no durable record"}},
		Errors:    []string{"wisp archive unavailable (disk full): 1 wisps past TTL were held"},
	}
	if got := compactAccounted(held); got != held.Scanned {
		t.Errorf("accounted = %d, want %d — a run-level error is not a wisp", got, held.Scanned)
	}

	// Control: a wisp that fell out because ITS OWN delete failed must still be
	// accounted for, or the warning stops catching under-reporting.
	failed := &compactResult{Scanned: 2, Skipped: 1, Failed: 1,
		Errors: []string{"delete gt-wisp-b: bd exited 1"}}
	if got := compactAccounted(failed); got != failed.Scanned {
		t.Errorf("accounted = %d, want %d — a per-wisp failure must be counted", got, failed.Scanned)
	}
	if got := compactAccounted(&compactResult{Scanned: 2, Skipped: 1}); got == 2 {
		t.Error("a scanned wisp that landed in no bucket was accounted for — the " +
			"incompleteness warning can no longer fire")
	}
}

type compactStubEnv struct {
	bdLog           string
	deleteLog       string
	archiveSnapshot string
	archiveDir      string
}

// setupCompactStubs puts a fake bd on PATH that answers compaction's queries
// with one closed, long-expired wisp, and points the wisp archive at a
// temporary directory.
func setupCompactStubs(t *testing.T) compactStubEnv {
	t.Helper()

	binDir := t.TempDir()
	logDir := t.TempDir()
	archiveDir := filepath.Join(t.TempDir(), "wisp-archive")
	env := compactStubEnv{
		bdLog:           filepath.Join(logDir, "bd.log"),
		deleteLog:       filepath.Join(logDir, "delete.log"),
		archiveSnapshot: filepath.Join(logDir, "archive-at-delete.jsonl"),
		archiveDir:      archiveDir,
	}

	wispRows := []map[string]any{{
		"id":            "gt-wisp-doomed",
		"title":         "patrol cycle 41",
		"status":        "closed",
		"issue_type":    "chore",
		"wisp_type":     "patrol",
		"created_at":    "2020-01-01T00:00:00Z",
		"updated_at":    "2020-01-02T00:00:00Z",
		"pinned":        0,
		"comment_count": 0,
		"labels_csv":    "",
		"parent":        "",
	}}
	archiveRows := []map[string]any{{
		"id":           "gt-wisp-doomed",
		"title":        "patrol cycle 41",
		"description":  "the payload that would have been lost",
		"design":       "",
		"notes":        "",
		"close_reason": "cycle complete",
		"status":       "closed",
		"priority":     2,
		"issue_type":   "chore",
		"wisp_type":    "patrol",
		"assignee":     "gastown/deacon",
		"created_by":   "gastown/deacon",
		"owner":        "",
		"source_repo":  "",
		"created_at":   "2020-01-01T00:00:00Z",
		"updated_at":   "2020-01-02T00:00:00Z",
		"closed_at":    "2020-01-02T00:00:00Z",
	}}
	wispRowsPath := writeStubJSON(t, filepath.Join(logDir, "wisps.json"), wispRows)
	archiveRowsPath := writeStubJSON(t, filepath.Join(logDir, "archive.json"), archiveRows)

	// $* is matched rather than $3: the wrapper may prepend a global flag, which
	// would shift every positional argument.
	bdScript := `#!/bin/sh
case "$1" in
  --allow-stale) exit 1 ;;
esac
echo "$*" >> "$BD_LOG"
case "$1" in
  sql)
    case "$*" in
      *"FROM wisps w "*)          cat "$WISP_ROWS" ; exit 0 ;;
      *"FROM wisps WHERE id IN"*) cat "$ARCHIVE_ROWS" ; exit 0 ;;
      *"SHOW COLUMNS"*)           printf 'Field,Type\n' ; exit 0 ;;
      *)                          printf 'OK, 0 rows affected\n' ; exit 0 ;;
    esac
    ;;
  delete)
    if [ ! -f "$ARCHIVE_SNAPSHOT" ]; then
      cat "$ARCHIVE_DIR"/*.jsonl > "$ARCHIVE_SNAPSHOT" 2>/dev/null
      touch "$ARCHIVE_SNAPSHOT"
    fi
    echo "$*" >> "$DELETE_LOG"
    printf 'deleted\n'
    exit 0
    ;;
esac
echo "unexpected bd command: $*" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", env.bdLog)
	t.Setenv("DELETE_LOG", env.deleteLog)
	t.Setenv("WISP_ROWS", wispRowsPath)
	t.Setenv("ARCHIVE_ROWS", archiveRowsPath)
	t.Setenv("ARCHIVE_SNAPSHOT", env.archiveSnapshot)
	t.Setenv("ARCHIVE_DIR", archiveDir)
	t.Setenv(reaper.ArchiveDirEnv, archiveDir)
	t.Setenv("GT_RIG", "")
	t.Chdir(t.TempDir())

	resetCompactFlags(t)
	return env
}

func containsSubstring(haystack []string, want string) bool {
	for _, s := range haystack {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func writeStubJSON(t *testing.T, path string, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal stub rows: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write stub rows: %v", err)
	}
	return path
}

func resetCompactFlags(t *testing.T) {
	t.Helper()
	oldDryRun, oldVerbose, oldJSON, oldRig := compactDryRun, compactVerbose, compactJSON, compactRig
	compactDryRun, compactVerbose, compactJSON, compactRig = false, false, false, ""
	t.Cleanup(func() {
		compactDryRun, compactVerbose, compactJSON, compactRig = oldDryRun, oldVerbose, oldJSON, oldRig
	})
}
