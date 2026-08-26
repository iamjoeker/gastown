package cmd

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/reaper"
)

// This file is compaction's pre-delete record (gt-hv3p).
//
// `gt compact` deletes wisps one `bd delete --force` at a time, and the wisp
// tables are dolt-ignored: no history to read AS OF, no backup, no restore. On
// 2026-08-22 a run was killed by an operator's `timeout 90` partway through its
// delete pass. 454 rows were gone, the run had printed nothing, and no table
// anywhere named which 454 — wisp_events has no "deleted" event type. The
// population was known only because an unrelated evidence dump happened to have
// run seven minutes earlier. An interrupted run was unauditable by
// construction, and that is true of every non-dry-run compaction, not just the
// one that was interrupted.
//
// The fix is the ordering internal/reaper/archive.go already uses for the rows
// it releases: every record is written and fsynced BEFORE the first row is
// deleted. An interruption can then only leave records with no deletion, never
// a deletion with no record. The archive is a superset of the casualties, and
// the exact set is recoverable — archived ids minus the ids still present in
// `wisps`. Duplicates are cheap to live with; the other failure is permanent.
//
// It writes into the same archive the reaper writes and `gt reaper archive`
// reads, so the record is enumerable by a command that already exists:
//
//	gt reaper archive --id gt-wisp-abc123
//	gt reaper archive --grep patrol
//
// A record that cannot be written CANCELS the delete pass. A wisp not deleted
// today is deleted tomorrow once the archive is writable again; a wisp deleted
// today without a record is gone.
//
// DEPTH, not just presence (gt-wv8h). Both writers append to the same archive
// file, so a reader cannot tell which one produced a given line — and until
// gt-wv8h neither recorded wisp_events at all, while the reaper's delete list
// named that table and its comment claimed the rows "were written out first".
// The events key is now mandatory in every record for exactly that reason, so
// this writer has to fill it too: a record here that omitted events while the
// reaper's carried them would put the two meanings of an empty list back into
// one file. Whether `bd delete --force` cascades into wisp_events is bd's
// business and not observable from here, which is the argument FOR recording
// them rather than against — an archive that depends on a guess about someone
// else's delete semantics is not a record.

// archiveIDChunk bounds how many ids go into one enrichment query. The list is
// inlined into SQL (bd's sql subcommand takes a query string, not placeholders),
// and a delete pass on a busy town can be thousands of wisps — enough to exceed
// the server's max_allowed_packet in a single statement.
const archiveIDChunk = 200

// safeWispID matches the ids the enrichment query is willing to inline. Wisp
// ids are generated (`gt-wisp-ekava`), so anything outside this alphabet is
// unexpected input rather than an id, and it is quietly left out of the
// enrichment query. The wisp is still ARCHIVED — from the fields compaction
// already holds — because an odd id must cost detail, never the record.
var safeWispID = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// compactArchiveColumns mirrors reaper.archiveWispColumns: the columns that
// make an archived row a usable record rather than a name. description is the
// load-bearing one — a merge-request wisp keeps its branch, target and
// source_issue in there as "key: value" lines, and an escalation keeps its
// entire content there.
const compactArchiveColumns = `id, title, description, design, notes, close_reason, ` +
	`status, priority, issue_type, wisp_type, assignee, created_by, owner, ` +
	`source_repo, created_at, updated_at, closed_at`

// compactEventColumns mirrors the reaper's attachArchiveEvents. Compaction
// writes into the SAME archive file the reaper writes, so the two must record a
// wisp to the same depth: an events key that means "collected, there were none"
// in one writer and "never looked" in the other is the unreadable absence
// gt-wv8h was reported as, moved one file over.
const compactEventColumns = `issue_id, event_type, actor, old_value, new_value, comment, created_at`

// archiveRow is one wisps-table row as the enrichment query returns it.
// Timestamps stay strings here for the same reason wispAge accepts two layouts:
// bd renders a datetime as RFC3339 when selected bare and as "2006-01-02
// 15:04:05" once an expression wraps it, and a parse failure must not cost the
// record.
type archiveRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Design      string `json:"design"`
	Notes       string `json:"notes"`
	CloseReason string `json:"close_reason"`
	Status      string `json:"status"`
	Priority    int    `json:"priority"`
	IssueType   string `json:"issue_type"`
	WispType    string `json:"wisp_type"`
	Assignee    string `json:"assignee"`
	CreatedBy   string `json:"created_by"`
	Owner       string `json:"owner"`
	SourceRepo  string `json:"source_repo"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	ClosedAt    string `json:"closed_at"`
}

// archiveAuxRow is one wisp_events or wisp_comments row. The two share a shape
// because a comment is an event with only a body: keeping one decoder means a
// column added to one query cannot be silently dropped from the other.
type archiveAuxRow struct {
	IssueID   string `json:"issue_id"`
	EventType string `json:"event_type"`
	Actor     string `json:"actor"`
	OldValue  string `json:"old_value"`
	NewValue  string `json:"new_value"`
	Comment   string `json:"comment"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

// archiveDeletions records every wisp the delete pass is about to remove and
// reports whether the deletion may proceed.
//
// False means NOTHING may be deleted this run. The caller holds the wisps
// instead, which is the same answer the reaper gives when its archive is
// unavailable: protection stands until a record can be kept.
func archiveDeletions(bd *beads.Beads, dbName string, pending []*compactIssue, now time.Time, result *compactResult) bool {
	archive, err := reaper.NewFileArchive(reaper.DefaultArchiveDir())
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"wisp archive unavailable (%v): %d wisps past TTL were held, not deleted (set %s to relocate it)",
			err, len(pending), reaper.ArchiveDirEnv))
		return false
	}
	return archiveDeletionsTo(bd, dbName, pending, now, archive, result)
}

// archiveDeletionsTo is archiveDeletions against an already-opened archive.
// Separated so a test can supply an Archiver that fails on demand: "the delete
// pass is cancelled when the record cannot be written" is the property worth
// pinning, and it cannot be reached through a real filesystem archive that
// works.
func archiveDeletionsTo(bd *beads.Beads, dbName string, pending []*compactIssue, now time.Time, archive reaper.Archiver, result *compactResult) bool {
	records := buildArchiveRecords(bd, dbName, pending, now, result)
	// ArchiveWisps does not return until every record is fsynced, which is what
	// makes the ordering in this function a durability guarantee and not just a
	// statement order.
	if err := archive.ArchiveWisps(records); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"recording %d wisps to %s before deletion failed (%v): they were held, not deleted",
			len(records), archive.Location(), err))
		return false
	}
	result.Archived = len(records)
	result.ArchivedTo = archive.Location()
	return true
}

// buildArchiveRecords returns one record per pending wisp, always.
//
// Enrichment is best-effort by design: if the wisps table cannot be read, the
// records fall back to the fields compaction already holds (id, title, status,
// type, timestamps, labels, parent) and the shortfall is reported as an error
// on the run. That is a deliberate ranking of the two failures — a record
// missing its description is a worse record, a wisp with no record at all is
// the incident.
func buildArchiveRecords(bd *beads.Beads, dbName string, pending []*compactIssue, now time.Time, result *compactResult) []reaper.ArchivedWisp {
	rows, err := loadArchiveRows(bd, pending)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"reading full wisp records for the archive: %v — archived id, title and status only", err))
	}

	// Events and comments are loaded separately so one failing does not cost the
	// other, and neither costs the row.
	events, eventsErr := loadArchiveAux(bd, pending, "wisp_events", compactEventColumns)
	if eventsErr != nil {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"reading wisp_events for the archive: %v — the records say so rather than reading as "+
				"wisps that had no events", eventsErr))
	}
	comments, commentsErr := loadArchiveAux(bd, pending, "wisp_comments", "issue_id, text, created_at")
	if commentsErr != nil {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"reading wisp_comments for the archive: %v — archived without them", commentsErr))
	}

	records := make([]reaper.ArchivedWisp, 0, len(pending))
	for _, w := range pending {
		rec := archiveRecordFor(w, rows[w.ID], dbName, now)
		attachCompactEvents(&rec, events[w.ID], eventsErr)
		for _, c := range comments[w.ID] {
			rec.Comments = append(rec.Comments, c.Text)
		}
		records = append(records, rec)
	}
	return records
}

// attachCompactEvents fills the record's event history, or says why it could
// not.
//
// The events key is deliberately not omitempty in reaper.ArchivedWisp: a record
// without it predates gt-wv8h and lost its history, a record with it is
// complete as written. That only holds while nothing writes a null events key
// for a wisp it never queried — so when the query failed, this writes one
// marker event saying so instead of an empty list that would read as "this wisp
// had no events". A record that overstates its own completeness is worse than
// one that admits a hole, because only the second can be found later.
func attachCompactEvents(rec *reaper.ArchivedWisp, rows []archiveAuxRow, loadErr error) {
	if loadErr != nil {
		rec.Events = []reaper.ArchivedEvent{{
			EventType: archiveEventsUnread,
			Comment:   fmt.Sprintf("wisp_events could not be read for this record: %v", loadErr),
		}}
		return
	}
	for _, e := range rows {
		rec.Events = append(rec.Events, reaper.ArchivedEvent{
			EventType: e.EventType,
			Actor:     e.Actor,
			OldValue:  e.OldValue,
			NewValue:  e.NewValue,
			Comment:   e.Comment,
			CreatedAt: parseWispTimestamp(e.CreatedAt),
		})
	}
}

// archiveEventsUnread is the event_type of the marker attachCompactEvents
// writes when the events query failed. It is not an event type the wisp tables
// produce, so a reader grepping the archive for it finds exactly the records
// whose history is unknown rather than absent.
const archiveEventsUnread = "gt:archive-events-unread"

// loadArchiveAux reads one auxiliary table for the pending wisps, in chunks,
// keyed by issue_id and kept in the order the query returned.
//
// A chunk that fails aborts the whole load for the same reason loadArchiveRows
// does: a caller holding half the events and no error would write half the
// records complete and say nothing about the rest.
func loadArchiveAux(bd *beads.Beads, pending []*compactIssue, table, columns string) (map[string][]archiveAuxRow, error) {
	if bd == nil {
		return nil, fmt.Errorf("no beads handle")
	}

	ids := make([]string, 0, len(pending))
	for _, w := range pending {
		if safeWispID.MatchString(w.ID) {
			ids = append(ids, w.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	byID := map[string][]archiveAuxRow{}
	for start := 0; start < len(ids); start += archiveIDChunk {
		end := start + archiveIDChunk
		if end > len(ids) {
			end = len(ids)
		}
		chunk, err := queryArchiveAuxRows(bd, table, columns, ids[start:end])
		if err != nil {
			return nil, err
		}
		for _, row := range chunk {
			byID[row.IssueID] = append(byID[row.IssueID], row)
		}
	}
	return byID, nil
}

func queryArchiveAuxRows(bd *beads.Beads, table, columns string, ids []string) ([]archiveAuxRow, error) {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = "'" + id + "'"
	}
	// Ordered by created_at then id: the value of an event list is the SEQUENCE,
	// and an AUTO_INCREMENT id shared with concurrent writers is only
	// incidentally chronological.
	query := "SELECT " + columns + " FROM " + table + " WHERE issue_id IN (" +
		strings.Join(quoted, ", ") + ") ORDER BY issue_id, created_at, id"

	out, err := bd.Run("sql", "--json", query)
	if err != nil {
		return nil, fmt.Errorf("querying %s for archive: %w", table, err)
	}

	var rows []archiveAuxRow
	if err := json.Unmarshal(extractJSONArray(out), &rows); err != nil {
		return nil, fmt.Errorf("parsing %s rows: %w", table, err)
	}
	return rows, nil
}

// loadArchiveRows reads the archive columns for the pending wisps, in chunks.
//
// A chunk that fails aborts the whole load rather than returning a partial map:
// a caller that got half the descriptions and no error would write half a
// record set and say nothing about the other half.
func loadArchiveRows(bd *beads.Beads, pending []*compactIssue) (map[string]*archiveRow, error) {
	if bd == nil {
		return nil, fmt.Errorf("no beads handle")
	}

	ids := make([]string, 0, len(pending))
	for _, w := range pending {
		if safeWispID.MatchString(w.ID) {
			ids = append(ids, w.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	rows := make(map[string]*archiveRow, len(ids))
	for start := 0; start < len(ids); start += archiveIDChunk {
		end := start + archiveIDChunk
		if end > len(ids) {
			end = len(ids)
		}
		chunk, err := queryArchiveRows(bd, ids[start:end])
		if err != nil {
			return nil, err
		}
		for i := range chunk {
			rows[chunk[i].ID] = &chunk[i]
		}
	}
	return rows, nil
}

func queryArchiveRows(bd *beads.Beads, ids []string) ([]archiveRow, error) {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = "'" + id + "'"
	}
	query := "SELECT " + compactArchiveColumns + " FROM wisps WHERE id IN (" +
		strings.Join(quoted, ", ") + ")"

	out, err := bd.Run("sql", "--json", query)
	if err != nil {
		return nil, fmt.Errorf("querying wisps for archive: %w", err)
	}

	var rows []archiveRow
	if err := json.Unmarshal(extractJSONArray(out), &rows); err != nil {
		return nil, fmt.Errorf("parsing archive rows: %w", err)
	}
	return rows, nil
}

// archiveRecordFor merges what compaction already knows about a wisp with the
// row the enrichment query returned. row may be nil.
func archiveRecordFor(w *compactIssue, row *archiveRow, dbName string, now time.Time) reaper.ArchivedWisp {
	rec := reaper.ArchivedWisp{
		ArchivedAt: now,
		Database:   dbName,
		ID:         w.ID,
		Title:      w.Title,
		Status:     w.Status,
		IssueType:  w.Type,
		WispType:   w.WispType,
		CreatedAt:  parseWispTimestamp(w.CreatedAt),
		UpdatedAt:  parseWispTimestamp(w.UpdatedAt),
		Labels:     w.Labels,
	}
	// The parent edge says which molecule a step wisp belonged to, and molecule
	// steps are the one class compaction deletes even when they carry comments.
	if w.Parent != "" {
		rec.Dependencies = []reaper.ArchivedDependency{{
			Type:            "parent-child",
			DependsOnWispID: w.Parent,
		}}
	}
	if row == nil {
		return rec
	}

	if row.Title != "" {
		rec.Title = row.Title
	}
	rec.Description = row.Description
	rec.Design = row.Design
	rec.Notes = row.Notes
	rec.CloseReason = row.CloseReason
	rec.Priority = row.Priority
	rec.Assignee = row.Assignee
	rec.CreatedBy = row.CreatedBy
	rec.Owner = row.Owner
	rec.SourceRepo = row.SourceRepo
	rec.ClosedAt = parseWispTimestamp(row.ClosedAt)
	if row.Status != "" {
		rec.Status = row.Status
	}
	if row.IssueType != "" {
		rec.IssueType = row.IssueType
	}
	if row.WispType != "" {
		rec.WispType = row.WispType
	}
	if t := parseWispTimestamp(row.CreatedAt); t != nil {
		rec.CreatedAt = t
	}
	if t := parseWispTimestamp(row.UpdatedAt); t != nil {
		rec.UpdatedAt = t
	}
	return rec
}

// parseWispTimestamp returns nil for a value in no layout it knows. An
// unparseable timestamp costs one field of the record; returning an error would
// cost the record.
func parseWispTimestamp(ts string) *time.Time {
	if ts == "" {
		return nil
	}
	for _, layout := range wispTimeLayouts {
		if t, err := time.Parse(layout, ts); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}
