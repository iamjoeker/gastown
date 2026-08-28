package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/reaper"
)

// This file is receipt pruning's pre-delete record, mirroring `gt compact`'s
// (gt-hv3p) for the same reason and the same class of bug (gt-wg81,
// hq-8rc0j): a destructive cleanup whose own record of what it destroyed can
// itself be erased.
//
// PruneReceipts deletes closed plugin-run wisps with `bd delete --force`, and
// wisp tables are dolt-ignored — no history to read AS OF, no backup to
// restore from. Until this file existed, the only trace of a prune was one
// daemon.log line ("pruned N plugin receipt(s)..."), and daemon.log is rotated
// and eventually deleted by lumberjack (see log_rotation.go's staleArchiveMaxAge
// and daemonDiskBudget). A destructive cleanup recorded only in a log that a
// second cleanup process also destroys is exactly the shape this bead names:
// post-hoc, there is no way to tell what was lost.
//
// The fix reuses the archive `gt compact` and the reaper already write to and
// `gt reaper archive` already reads: every eligible receipt is written and
// fsynced to that archive BEFORE any delete runs. An interrupted or failed
// archive write cancels the delete pass for this run — a receipt not pruned
// today is pruned tomorrow once the archive is writable again; a receipt
// deleted today without a record is gone.

// receiptArchiveColumns mirrors compactArchiveColumns in internal/cmd/compact_archive.go.
const receiptArchiveColumns = `id, title, description, design, notes, close_reason, ` +
	`status, priority, issue_type, wisp_type, assignee, created_by, owner, ` +
	`source_repo, created_at, updated_at, closed_at`

// receiptEventColumns mirrors compactEventColumns.
const receiptEventColumns = `issue_id, event_type, actor, old_value, new_value, comment, created_at`

// receiptArchiveIDChunk bounds how many ids go into one enrichment query, for
// the same reason as compact's archiveIDChunk: the list is inlined into SQL.
const receiptArchiveIDChunk = 200

// safeReceiptID matches ids the enrichment/event queries are willing to
// inline. Receipt ids are wisp ids (gt-wisp-ekava); anything outside this
// alphabet is unexpected input rather than an id and is left out of the
// enrichment query — the receipt is still archived from the fields the prune
// pass already holds.
var safeReceiptID = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// receiptArchiveEventsUnread marks a record whose event history could not be
// read, so it reads as "unknown" rather than as "had none" (mirrors
// compact_archive.go's archiveEventsUnread; see reaper.ArchivedWisp.Events).
const receiptArchiveEventsUnread = "gt:archive-events-unread"

type receiptArchiveRow struct {
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

type receiptEventRow struct {
	IssueID   string `json:"issue_id"`
	EventType string `json:"event_type"`
	Actor     string `json:"actor"`
	OldValue  string `json:"old_value"`
	NewValue  string `json:"new_value"`
	Comment   string `json:"comment"`
	CreatedAt string `json:"created_at"`
}

// archiveReceipts records every eligible receipt to the durable wisp archive
// before any delete runs, and reports whether the deletion may proceed.
//
// False means nothing may be deleted this run. The caller holds every
// eligible receipt instead — the same answer `gt compact` gives when its
// archive is unavailable: protection stands until a record can be kept.
func (r *Recorder) archiveReceipts(eligible []PrunedReceipt, now time.Time, result *ReceiptPruneResult) bool {
	if len(eligible) == 0 {
		return true
	}
	archive, err := reaper.NewFileArchive(reaper.DefaultArchiveDir())
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"wisp archive unavailable (%v): %d receipt(s) past retention were held, not deleted (set %s to relocate it)",
			err, len(eligible), reaper.ArchiveDirEnv))
		return false
	}
	return r.archiveReceiptsTo(eligible, now, archive, result)
}

// archiveReceiptsTo is archiveReceipts against an already-opened archive, so a
// test can supply an Archiver that fails on demand.
func (r *Recorder) archiveReceiptsTo(eligible []PrunedReceipt, now time.Time, archive reaper.Archiver, result *ReceiptPruneResult) bool {
	dbName := beads.DatabaseNameFromMetadata(beads.ResolveBeadsDir(r.townRoot))
	records := r.buildReceiptArchiveRecords(eligible, dbName, now, result)
	// ArchiveWisps does not return until every record is fsynced, which is what
	// makes the ordering here a durability guarantee and not just statement order.
	if err := archive.ArchiveWisps(records); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"recording %d receipt(s) to %s before deletion failed (%v): they were held, not deleted",
			len(records), archive.Location(), err))
		return false
	}
	result.Archived = len(records)
	result.ArchivedTo = archive.Location()
	return true
}

// buildReceiptArchiveRecords returns one record per eligible receipt, always.
//
// Enrichment is best-effort by design, same ranking as compact_archive.go: a
// record missing its description is a worse record, a receipt with no record
// at all is the incident.
func (r *Recorder) buildReceiptArchiveRecords(eligible []PrunedReceipt, dbName string, now time.Time, result *ReceiptPruneResult) []reaper.ArchivedWisp {
	ids := make([]string, 0, len(eligible))
	for _, rec := range eligible {
		if safeReceiptID.MatchString(rec.ID) {
			ids = append(ids, rec.ID)
		}
	}

	rows, err := r.loadReceiptArchiveRows(ids)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"reading full receipt records for the archive: %v — archived id, plugin and status only", err))
	}

	events, eventsErr := r.loadReceiptEvents(ids)
	if eventsErr != nil {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"reading wisp_events for the receipt archive: %v — the records say so rather than reading "+
				"as receipts that had no events", eventsErr))
	}

	records := make([]reaper.ArchivedWisp, 0, len(eligible))
	for _, pr := range eligible {
		rec := receiptArchiveRecordFor(pr, rows[pr.ID], dbName, now)
		attachReceiptEvents(&rec, events[pr.ID], eventsErr)
		records = append(records, rec)
	}
	return records
}

// attachReceiptEvents fills the record's event history, or says why it could
// not (mirrors compact_archive.go's attachCompactEvents).
func attachReceiptEvents(rec *reaper.ArchivedWisp, rows []receiptEventRow, loadErr error) {
	if loadErr != nil {
		rec.Events = []reaper.ArchivedEvent{{
			EventType: receiptArchiveEventsUnread,
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
			CreatedAt: parseReceiptArchiveTimestamp(e.CreatedAt),
		})
	}
}

// receiptArchiveRecordFor merges what the prune pass already knows about a
// receipt with the row the enrichment query returned. row may be nil.
func receiptArchiveRecordFor(pr PrunedReceipt, row *receiptArchiveRow, dbName string, now time.Time) reaper.ArchivedWisp {
	rec := reaper.ArchivedWisp{
		ArchivedAt: now,
		Database:   dbName,
		ID:         pr.ID,
		Status:     "closed",
		WispType:   "", // receipts deliberately carry no wisp_type; see retention.go
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
	rec.ClosedAt = parseReceiptArchiveTimestamp(row.ClosedAt)
	if row.Status != "" {
		rec.Status = row.Status
	}
	if row.IssueType != "" {
		rec.IssueType = row.IssueType
	}
	if t := parseReceiptArchiveTimestamp(row.CreatedAt); t != nil {
		rec.CreatedAt = t
	}
	if t := parseReceiptArchiveTimestamp(row.UpdatedAt); t != nil {
		rec.UpdatedAt = t
	}
	return rec
}

// loadReceiptArchiveRows reads the archive columns for the eligible receipts,
// in chunks. A chunk that fails aborts the whole load rather than returning a
// partial map — a caller that got half the descriptions and no error would
// write half a record set and say nothing about the other half.
func (r *Recorder) loadReceiptArchiveRows(ids []string) (map[string]*receiptArchiveRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	rows := make(map[string]*receiptArchiveRow, len(ids))
	for start := 0; start < len(ids); start += receiptArchiveIDChunk {
		end := start + receiptArchiveIDChunk
		if end > len(ids) {
			end = len(ids)
		}
		chunk, err := r.queryReceiptArchiveRows(ids[start:end])
		if err != nil {
			return nil, err
		}
		for i := range chunk {
			rows[chunk[i].ID] = &chunk[i]
		}
	}
	return rows, nil
}

func (r *Recorder) queryReceiptArchiveRows(ids []string) ([]receiptArchiveRow, error) {
	list, err := sqlIDList(ids)
	if err != nil {
		return nil, err
	}
	query := "SELECT " + receiptArchiveColumns + " FROM wisps WHERE id IN (" + list + ")"

	out, err := r.runBD(receiptDeleteTimeout, beads.ReadOnlyPinned, "sql", "--json", query)
	if err != nil {
		return nil, fmt.Errorf("querying wisps for receipt archive: %w", err)
	}

	var rows []receiptArchiveRow
	if err := json.Unmarshal(extractJSONArray(out), &rows); err != nil {
		return nil, fmt.Errorf("parsing receipt archive rows: %w", err)
	}
	return rows, nil
}

// loadReceiptEvents reads wisp_events for the eligible receipts, in chunks,
// keyed by issue_id.
func (r *Recorder) loadReceiptEvents(ids []string) (map[string][]receiptEventRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	byID := map[string][]receiptEventRow{}
	for start := 0; start < len(ids); start += receiptArchiveIDChunk {
		end := start + receiptArchiveIDChunk
		if end > len(ids) {
			end = len(ids)
		}
		chunk, err := r.queryReceiptEvents(ids[start:end])
		if err != nil {
			return nil, err
		}
		for _, row := range chunk {
			byID[row.IssueID] = append(byID[row.IssueID], row)
		}
	}
	return byID, nil
}

func (r *Recorder) queryReceiptEvents(ids []string) ([]receiptEventRow, error) {
	list, err := sqlIDList(ids)
	if err != nil {
		return nil, err
	}
	// Ordered by created_at then id: the value of an event list is the
	// SEQUENCE, and an AUTO_INCREMENT id shared with concurrent writers is
	// only incidentally chronological.
	query := "SELECT " + receiptEventColumns + " FROM wisp_events WHERE issue_id IN (" +
		list + ") ORDER BY issue_id, created_at, id"

	out, err := r.runBD(receiptDeleteTimeout, beads.ReadOnlyPinned, "sql", "--json", query)
	if err != nil {
		return nil, fmt.Errorf("querying wisp_events for receipt archive: %w", err)
	}

	var rows []receiptEventRow
	if err := json.Unmarshal(extractJSONArray(out), &rows); err != nil {
		return nil, fmt.Errorf("parsing wisp_events rows: %w", err)
	}
	return rows, nil
}

// parseReceiptArchiveTimestamp accepts both layouts bd emits for a datetime
// column (see parseReceiptTime in retention.go).
func parseReceiptArchiveTimestamp(ts string) *time.Time {
	if ts == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, ts); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}
