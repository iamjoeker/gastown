package reaper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This file answers a narrower question than archive.go does (gt-60ju, split
// from gt-wv8h).
//
// purgeClosedWisps' UNPROTECTED half deletes closed, unprotected wisps and all
// four aux tables with no archive at all. That is by design: those rows are
// residue, and archiving ~229/day of them with their full description/labels/
// comments/events would recreate the storage cost the purge exists to pay
// down. But it leaves no per-id answer anywhere to "was id X purged, or did it
// never exist" — the DOLT_COMMIT message and daemon.log both name a POPULATION
// and a COUNT, never an ID.
//
// A tombstone is the lean record that answers exactly that question and
// nothing more: id, when, which database, its wisp_type, and how many aux
// rows went with it. No title, no description, no labels — that is still the
// full archive's job, for the wisps that warrant it.
//
// WHERE: tombstones live in their own subdirectory under the archive root
// (<archive-dir>/tombstones/), not beside the *.jsonl archive files. ReadArchive
// globs every *.jsonl file directly under its dir and decodes each line as an
// ArchivedWisp; it already skips subdirectories (entry.IsDir()), so a
// tombstone file placed in its own directory cannot be mistaken for a
// half-populated ArchivedWisp record and pad an archive scan with blanks.
// ReadTombstones, below, is the tombstone-shaped counterpart, kept separate on
// purpose rather than taught to ArchivedWisp's decoder.
//
// RETENTION: this population is roughly 10x the protected archive's (~229/day
// of residue vs. the rarer protected types), so "keep forever" — the archive's
// policy — would just relocate the unbounded-growth problem one directory
// over. Tombstones are pruned by age instead: PruneTombstones removes whole
// monthly files once every record in them is older than the retention window.
// A pruned id answers "was it purged" with silence again, but only after the
// window during which an audit question is actually plausible has passed —
// which is the same trade every purge_age already makes for the rows
// themselves.
//
// ON/OFF: tombstoning shares the archive's directory and the archive's
// on/off switch (a nil Archiver, e.g. from --no-archive). It does not get its
// own flag: an operator who disabled the durable store for protected wisps
// has said they do not want a filesystem archive at all, and a lean tombstone
// is still a filesystem archive.

// tombstoneSubdir is the directory name under an Archiver's Location() that
// tombstones are written to.
const tombstoneSubdir = "tombstones"

// Tombstone is the durable, per-id record that a wisp was purged.
//
// It deliberately does not carry title, description, labels, or events — that
// is what an ArchivedWisp is for. A Tombstone exists only to answer "was id X
// purged, or did it never exist", not to reconstruct the row.
type Tombstone struct {
	PurgedAt time.Time `json:"purged_at"`
	Database string    `json:"database"`
	ID       string    `json:"id"`
	WispType string    `json:"wisp_type,omitempty"`
	// AuxCounts is the number of rows removed from each aux table alongside
	// this wisp, keyed by table name. Not omitempty: a Tombstone written by
	// this code always sets it, even to an empty map, so a nil map (were one
	// ever read back) marks a record from before aux counts were collected —
	// the same "the key is the discriminator" reasoning ArchivedWisp.Events
	// uses for the same purpose.
	AuxCounts map[string]int `json:"aux_counts"`
}

// tombstoneDirFor returns the tombstone subdirectory for an archive rooted at
// archiveDir, or "" if archiveDir is empty (tombstoning off).
func tombstoneDirFor(archiveDir string) string {
	if archiveDir == "" {
		return ""
	}
	return filepath.Join(archiveDir, tombstoneSubdir)
}

// WriteTombstones appends records to <dir>/<database>-<YYYY-MM>.jsonl,
// creating dir if needed. It mirrors FileArchive.ArchiveWisps: grouped by
// destination file, sorted paths for deterministic ordering, one JSON line
// per record.
func WriteTombstones(dir string, records []Tombstone) error {
	if len(records) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, archiveDirPerm); err != nil {
		return fmt.Errorf("create tombstone dir %s: %w", dir, err)
	}

	byFile := map[string][]Tombstone{}
	for _, rec := range records {
		path := tombstonePathFor(dir, rec)
		byFile[path] = append(byFile[path], rec)
	}

	paths := make([]string, 0, len(byFile))
	for path := range byFile {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		if err := appendTombstoneJSONL(path, byFile[path]); err != nil {
			return err
		}
	}
	return nil
}

func tombstonePathFor(dir string, rec Tombstone) string {
	name := rec.Database
	if name == "" || ValidateDBName(name) != nil {
		name = "unknown"
	}
	stamp := rec.PurgedAt
	if stamp.IsZero() {
		stamp = time.Now()
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%s.jsonl", name, stamp.UTC().Format("2006-01")))
}

func appendTombstoneJSONL(path string, records []Tombstone) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := range records {
		if err := enc.Encode(&records[i]); err != nil {
			return fmt.Errorf("encode tombstone %s: %w", records[i].ID, err)
		}
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, archiveFilePerm)
	if err != nil {
		return fmt.Errorf("open tombstone file %s: %w", path, err)
	}
	defer file.Close()

	info, statErr := file.Stat()
	var startSize int64
	if statErr == nil {
		startSize = info.Size()
	}

	if _, err := file.Write(buf.Bytes()); err != nil {
		if statErr == nil {
			_ = file.Truncate(startSize)
		}
		return fmt.Errorf("write tombstone file %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		if statErr == nil {
			_ = file.Truncate(startSize)
		}
		return fmt.Errorf("sync tombstone file %s: %w", path, err)
	}
	return nil
}

// TombstoneFilter narrows what ReadTombstones returns. A zero filter reads
// everything.
type TombstoneFilter struct {
	// Database matches Tombstone.Database exactly.
	Database string
	// ID matches Tombstone.ID exactly.
	ID string
	// Limit caps the returned records, keeping the most recently purged.
	// Zero means no limit.
	Limit int
}

// TombstoneScan is the result of reading a tombstone directory.
type TombstoneScan struct {
	Records []Tombstone `json:"records"`
	// Files counts the tombstone files read.
	Files int `json:"files"`
	// Malformed counts lines that would not decode. Reported rather than
	// swallowed, matching ArchiveScan.Malformed: a corrupt tombstone store
	// must not read as an empty one.
	Malformed int `json:"malformed,omitempty"`
}

// ReadTombstones reads every *.jsonl file under dir and returns the records
// matching filter, most recently purged first.
//
// A missing directory is not an error: nothing has been tombstoned yet.
func ReadTombstones(dir string, filter TombstoneFilter) (*TombstoneScan, error) {
	scan := &TombstoneScan{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return scan, nil
		}
		return nil, fmt.Errorf("read tombstone dir %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		records, malformed, err := readTombstoneFile(filepath.Join(dir, name), filter)
		if err != nil {
			return nil, err
		}
		scan.Files++
		scan.Malformed += malformed
		scan.Records = append(scan.Records, records...)
	}

	sort.SliceStable(scan.Records, func(i, j int) bool {
		return scan.Records[i].PurgedAt.After(scan.Records[j].PurgedAt)
	})
	if filter.Limit > 0 && len(scan.Records) > filter.Limit {
		scan.Records = scan.Records[:filter.Limit]
	}
	return scan, nil
}

func readTombstoneFile(path string, filter TombstoneFilter) ([]Tombstone, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open tombstone file %s: %w", path, err)
	}
	defer file.Close()

	var records []Tombstone
	malformed := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 4*1024), 256*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec Tombstone
		if err := json.Unmarshal(line, &rec); err != nil {
			malformed++
			continue
		}
		if matchesTombstoneFilter(rec, filter) {
			records = append(records, rec)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, malformed, fmt.Errorf("read tombstone file %s: %w", path, err)
	}
	return records, malformed, nil
}

func matchesTombstoneFilter(rec Tombstone, filter TombstoneFilter) bool {
	if filter.Database != "" && rec.Database != filter.Database {
		return false
	}
	if filter.ID != "" && rec.ID != filter.ID {
		return false
	}
	return true
}

// tombstoneFileMonth parses the "<database>-<YYYY-MM>.jsonl" name this
// package writes and returns the month it covers. ok is false for any name
// that does not match, so a file this package did not write is left alone by
// PruneTombstones rather than guessed at.
func tombstoneFileMonth(name string) (year int, month time.Month, ok bool) {
	base := strings.TrimSuffix(name, ".jsonl")
	idx := strings.LastIndex(base, "-")
	if idx <= 0 || idx+1 >= len(base) {
		return 0, 0, false
	}
	// "<db>-YYYY-MM": the stamp is the last two dash-separated components.
	dashIdx := strings.LastIndex(base[:idx], "-")
	if dashIdx < 0 {
		return 0, 0, false
	}
	stamp := base[dashIdx+1:]
	t, err := time.Parse("2006-01", stamp)
	if err != nil {
		return 0, 0, false
	}
	return t.Year(), t.Month(), true
}

// PruneTombstones removes whole monthly tombstone files once every record
// they could contain is older than retention, measured from now.
//
// Pruning by file rather than by record keeps this cheap and exact: a
// tombstone file's name already commits to the single month every record in
// it was written in (tombstonePathFor), so a file is safe to remove as soon as
// that month's last possible timestamp falls outside the window — no need to
// open and re-write files to drop individual lines.
//
// A missing directory is not an error: nothing has been tombstoned yet.
func PruneTombstones(dir string, retention time.Duration, now time.Time) (removed int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read tombstone dir %s: %w", dir, err)
	}

	cutoff := now.UTC().Add(-retention)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		year, month, ok := tombstoneFileMonth(entry.Name())
		if !ok {
			continue
		}
		// The last instant a record in this file could carry: the start of
		// the following month.
		monthEnd := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
		if monthEnd.After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return removed, fmt.Errorf("remove stale tombstone file %s: %w", entry.Name(), err)
		}
		removed++
	}
	return removed, nil
}

// collectTombstones gathers the per-id wisp_type and aux-row counts for ids
// about to be deleted from the wisps table, so the tombstone written just
// before the delete describes exactly what the delete removed.
//
// It must run on the same session that will do the delete (db is the pinned
// write-session connection, not the pool) and must run BEFORE deleteRowsByID:
// once the rows are gone there is nothing left to count.
func collectTombstones(ctx context.Context, db sqlRunner, dbName string, ids []string, purgedAt time.Time, auxTables []string) ([]Tombstone, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := "(" + strings.Join(placeholders, ",") + ")"

	byID := make(map[string]*Tombstone, len(ids))
	order := make([]string, 0, len(ids))
	for _, id := range ids {
		byID[id] = &Tombstone{
			PurgedAt:  purgedAt,
			Database:  dbName,
			ID:        id,
			AuxCounts: map[string]int{},
		}
		order = append(order, id)
	}

	typeQuery := fmt.Sprintf("SELECT id, COALESCE(wisp_type, 'unknown') FROM wisps WHERE id IN %s", inClause) //nolint:gosec // G201: inClause is ?-placeholders
	rows, err := db.QueryContext(ctx, typeQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("tombstone wisp_type query: %w", err)
	}
	for rows.Next() {
		var id, wtype string
		if err := rows.Scan(&id, &wtype); err != nil {
			rows.Close()
			return nil, fmt.Errorf("tombstone wisp_type scan: %w", err)
		}
		if rec, ok := byID[id]; ok {
			rec.WispType = wtype
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("tombstone wisp_type rows: %w", err)
	}
	rows.Close()

	for _, tbl := range auxTables {
		countQuery := fmt.Sprintf("SELECT issue_id, COUNT(*) FROM `%s` WHERE issue_id IN %s GROUP BY issue_id", tbl, inClause) //nolint:gosec // G201: tbl is internal, inClause is ?-placeholders
		auxRows, err := db.QueryContext(ctx, countQuery, args...)
		if err != nil {
			return nil, fmt.Errorf("tombstone aux count query (%s): %w", tbl, err)
		}
		for auxRows.Next() {
			var id string
			var cnt int
			if err := auxRows.Scan(&id, &cnt); err != nil {
				auxRows.Close()
				return nil, fmt.Errorf("tombstone aux count scan (%s): %w", tbl, err)
			}
			if rec, ok := byID[id]; ok && cnt > 0 {
				rec.AuxCounts[tbl] = cnt
			}
		}
		if err := auxRows.Err(); err != nil {
			auxRows.Close()
			return nil, fmt.Errorf("tombstone aux count rows (%s): %w", tbl, err)
		}
		auxRows.Close()
	}

	out := make([]Tombstone, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// tombstoneBeforeDelete returns a batchDeleteRows beforeDelete hook that
// writes a Tombstone for each id about to be deleted from the wisps table.
//
// dir empty means tombstoning off (see WithTombstoneDir). count is
// incremented by the number of tombstones written; anomalies collects
// non-fatal failures. The hook itself always returns nil: a tombstone write
// failure must not abort the purge it is trying to leave a record of (see
// batchDeleteRows' beforeDelete doc).
func tombstoneBeforeDelete(dbName string, dir string, purgedAt time.Time, auxTables []string, count *int, anomalies *[]Anomaly) func(ctx context.Context, db sqlRunner, ids []string) error {
	if dir == "" {
		return nil
	}
	return func(ctx context.Context, db sqlRunner, ids []string) error {
		records, err := collectTombstones(ctx, db, dbName, ids, purgedAt, auxTables)
		if err != nil {
			*anomalies = append(*anomalies, Anomaly{
				Type:    "tombstone_collect_failed",
				Message: fmt.Sprintf("collecting tombstone data for %d wisp(s) in %s failed, purging without a record: %v", len(ids), dbName, err),
				Count:   len(ids),
			})
			return nil
		}
		if err := WriteTombstones(dir, records); err != nil {
			*anomalies = append(*anomalies, Anomaly{
				Type:    "tombstone_write_failed",
				Message: fmt.Sprintf("writing %d tombstone(s) for %s to %s failed, purging without a record: %v", len(records), dbName, dir, err),
				Count:   len(records),
			})
			return nil
		}
		*count += len(records)
		return nil
	}
}
