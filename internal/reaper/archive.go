package reaper

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// This file is the retention half of purge protection (gt-6xwt).
//
// ProtectedWispLabels stops purge from deleting types whose closed rows are
// evidence rather than residue (gt-nmg, after the gt-6dp incident). That is the
// right trade — the growth is merely expensive, the deletion is final — but on
// its own it is not a policy, it is a deferral: MR wisps close at roughly 25/day
// on one busy rig, so "protect and never revisit" buys recoverability with
// unbounded rows that nothing will ever remove.
//
// The way out is not a bigger age bound. gt-nmg's argument holds: a merge queue
// that stalls for a week produces a closed unmerged MR older than any purge_age
// we would pick, so age alone eventually eats exactly the record the protection
// exists for. What releases the ROW without losing the RECORD is exporting the
// record somewhere durable first, and only then deleting.
//
// WHY A FILE AND NOT A TABLE: an archive that lives in another Dolt table does
// not relieve anything — the server still carries the rows forever, which is the
// cost being paid down. The archive is append-only JSONL on the filesystem:
// outside Dolt, greppable, covered by ordinary filesystem backups, and free of
// the schema/commit machinery that makes wisp rows expensive.
//
// WHAT IS NOT ARCHIVED-AND-RELEASED: pinned rows. `pinned` is the column an
// incident responder sets by hand to keep one specific record where they can
// reach it right now; honouring that is the point of the guard, and an archive
// they did not ask for is not the thing they pinned.

// ArchiveDirEnv overrides the archive location. Set it when the default data
// directory is not where records should land (a test, or a host whose home
// directory is not durable).
const ArchiveDirEnv = "GT_WISP_ARCHIVE_DIR"

// archiveFilePerm and archiveDirPerm match the modes gt uses for its other
// on-disk data (~/.gt/costs.jsonl and friends).
const (
	archiveFilePerm = 0o644
	archiveDirPerm  = 0o755
)

// DefaultArchiveDir returns the directory wisp archives are written to.
//
// The resolution deliberately mirrors internal/cmd's gtDataDir — $GT_HOME/.gt
// when GT_HOME is set, ~/.gt otherwise — because the archive belongs with gt's
// other runtime data. It is duplicated rather than imported because
// internal/cmd imports this package, so the dependency cannot run the other
// way; every caller in the tree resolves the archive path through THIS function
// so the two cannot drift into two different archives.
//
// One Dolt server serves every database on the host, so one archive root per
// host is the matching granularity; the database name is part of each filename.
func DefaultArchiveDir() string {
	if dir := os.Getenv(ArchiveDirEnv); dir != "" {
		return dir
	}
	if home := os.Getenv("GT_HOME"); home != "" {
		return filepath.Join(home, ".gt", "wisp-archive")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".gt", "wisp-archive")
	}
	return filepath.Join(home, ".gt", "wisp-archive")
}

// ArchivedDependency is one edge of a wisp's dependency graph, kept so an
// archived record still says what molecule or issue it belonged to.
type ArchivedDependency struct {
	Type              string `json:"type,omitempty"`
	DependsOnWispID   string `json:"depends_on_wisp_id,omitempty"`
	DependsOnIssueID  string `json:"depends_on_issue_id,omitempty"`
	DependsOnExternal string `json:"depends_on_external,omitempty"`
}

// ArchivedEvent is one row of a wisp's event history: who changed what, when.
//
// For a merge-request wisp the events ARE the record of its state transitions —
// the wisps row carries only the CURRENT status, so submitted -> merged and
// submitted -> rejected are indistinguishable in the row once it is closed. The
// same holds for an escalation's acknowledgement. Archiving the row without its
// events keeps the outcome and loses the history that explains it.
type ArchivedEvent struct {
	EventType string     `json:"event_type,omitempty"`
	Actor     string     `json:"actor,omitempty"`
	OldValue  string     `json:"old_value,omitempty"`
	NewValue  string     `json:"new_value,omitempty"`
	Comment   string     `json:"comment,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

// ArchivedWisp is the durable record of one wisp, written before purge deletes
// its row.
//
// Description is carried verbatim and is load-bearing: a merge-request wisp
// keeps branch / target / source_issue / commit_sha / worker as "key: value"
// lines in its description rather than in columns (internal/beads
// ParseMRFields), so the whole field is the structured record. Storing parsed
// MR fields instead would both lose everything the parser does not know about
// and make this package depend on the merge-queue's format.
type ArchivedWisp struct {
	// ArchivedAt is when this record was exported, not when the wisp closed.
	ArchivedAt time.Time `json:"archived_at"`
	Database   string    `json:"database"`

	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Design      string `json:"design,omitempty"`
	Notes       string `json:"notes,omitempty"`
	CloseReason string `json:"close_reason,omitempty"`

	Status     string `json:"status"`
	Priority   int    `json:"priority"`
	IssueType  string `json:"issue_type,omitempty"`
	WispType   string `json:"wisp_type,omitempty"`
	Assignee   string `json:"assignee,omitempty"`
	CreatedBy  string `json:"created_by,omitempty"`
	Owner      string `json:"owner,omitempty"`
	SourceRepo string `json:"source_repo,omitempty"`

	CreatedAt *time.Time `json:"created_at,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`

	Labels       []string             `json:"labels,omitempty"`
	Comments     []string             `json:"comments,omitempty"`
	Dependencies []ArchivedDependency `json:"dependencies,omitempty"`

	// Events is NOT omitempty, and the exception is the point (gt-wv8h).
	//
	// Until this field existed the delete named wisp_events among the tables it
	// removed while nothing ever read that table into a record, so every wisp
	// released between 2026-08-19 and 2026-08-25 lost its history with no trace:
	// 98 records on this host, ~4 events each on the merge-request class.
	// Measured, the archive and the live table disagreed by construction, and
	// the code comment above the delete list asserted the opposite.
	//
	// An `omitempty` events key would render "this writer did not collect
	// events" and "this wisp had none" as the same absence — which is the exact
	// unreadable-zero this bug was reported as. Emitting `"events":null` instead
	// makes the KEY the discriminator: a record without it predates the fix and
	// its history is gone, a record with it is complete as written. Labels,
	// Comments and Dependencies stay omitempty because the events key already
	// dates the record, and the same writer fills all four.
	Events []ArchivedEvent `json:"events"`
}

// Archiver stores wisp records durably so purge may delete their rows.
//
// The contract is the whole safety property: ArchiveWisps must not return nil
// until every record it was given will survive a crash of this process and of
// the machine. Purge treats a nil return as permission to delete, and treats
// any error as "protection still holds" — so an implementation that buffers
// without flushing turns the retention policy back into the data loss it exists
// to prevent.
type Archiver interface {
	ArchiveWisps(records []ArchivedWisp) error
	// Location describes where records went, for operator output. It is
	// reported alongside the released count so the deletion always names the
	// place the record moved to.
	Location() string
}

// FileArchive appends records as JSON Lines under a directory, one file per
// database per month: <dir>/<database>-<YYYY-MM>.jsonl.
//
// Monthly rotation keeps a busy rig's files findable and bounded without
// needing a rotation daemon, and the database in the name means a record can be
// traced back to the server it came from.
type FileArchive struct {
	dir string
	mu  sync.Mutex
	// now is overridable so tests can pin the file a record lands in.
	now func() time.Time
}

// NewFileArchive creates the archive directory if needed and returns an
// Archiver writing into it.
//
// The directory is created eagerly, here, rather than lazily on first write:
// a purge must not discover that its archive is unwritable at the moment it has
// already decided to delete.
func NewFileArchive(dir string) (*FileArchive, error) {
	if dir == "" {
		return nil, fmt.Errorf("archive dir is empty")
	}
	if err := os.MkdirAll(dir, archiveDirPerm); err != nil {
		return nil, fmt.Errorf("create archive dir %s: %w", dir, err)
	}
	return &FileArchive{dir: dir, now: time.Now}, nil
}

// Location returns the archive directory.
func (a *FileArchive) Location() string { return a.dir }

// ArchiveWisps appends every record and fsyncs before returning.
//
// A short or failed write is rolled back by truncating the file to the length
// it had on entry, so a partially written line can never be left behind for the
// reader to trip over. The rollback is best-effort by nature — if it fails the
// error is still returned, and the caller's response to any error is to leave
// the rows in place, so the worst case is a damaged tail plus rows that were
// never deleted, not a deletion with no record.
func (a *FileArchive) ArchiveWisps(records []ArchivedWisp) error {
	if len(records) == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	byFile := map[string][]ArchivedWisp{}
	for _, rec := range records {
		byFile[a.pathFor(rec)] = append(byFile[a.pathFor(rec)], rec)
	}

	paths := make([]string, 0, len(byFile))
	for path := range byFile {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		if err := appendJSONL(path, byFile[path]); err != nil {
			return err
		}
	}
	return nil
}

// pathFor returns the file a record belongs in. Records with no database or a
// name that would escape the directory land in "unknown" rather than being
// dropped: an archive is the last copy, so an odd name must not lose it.
func (a *FileArchive) pathFor(rec ArchivedWisp) string {
	name := rec.Database
	if name == "" || ValidateDBName(name) != nil {
		name = "unknown"
	}
	stamp := rec.ArchivedAt
	if stamp.IsZero() {
		stamp = a.now()
	}
	return filepath.Join(a.dir, fmt.Sprintf("%s-%s.jsonl", name, stamp.UTC().Format("2006-01")))
}

func appendJSONL(path string, records []ArchivedWisp) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := range records {
		if err := enc.Encode(&records[i]); err != nil {
			return fmt.Errorf("encode archive record %s: %w", records[i].ID, err)
		}
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, archiveFilePerm)
	if err != nil {
		return fmt.Errorf("open archive %s: %w", path, err)
	}
	defer file.Close()

	// The offset to rewind to if anything below fails. Taken from the handle
	// rather than from a stat of the path so a concurrent rotation cannot make
	// it point into someone else's file.
	start, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("seek archive %s: %w", path, err)
	}

	rollback := func(cause error) error {
		_ = file.Truncate(start)
		_ = file.Sync()
		return cause
	}

	if _, err := file.Write(buf.Bytes()); err != nil {
		return rollback(fmt.Errorf("write archive %s: %w", path, err))
	}
	// Durability, not tidiness: the caller deletes the rows once this returns.
	if err := file.Sync(); err != nil {
		return rollback(fmt.Errorf("sync archive %s: %w", path, err))
	}
	return nil
}

// ArchiveFilter narrows what ReadArchive returns. A zero filter reads
// everything.
type ArchiveFilter struct {
	// Database matches ArchivedWisp.Database exactly.
	Database string
	// ID matches ArchivedWisp.ID exactly.
	ID string
	// Contains is a case-insensitive substring match over the fields an
	// operator looks a lost record up by: id, title, description, close reason.
	Contains string
	// Limit caps the returned records, keeping the most recently archived.
	// Zero means no limit.
	Limit int
}

// ArchiveScan is the result of reading an archive directory.
type ArchiveScan struct {
	Records []ArchivedWisp `json:"records"`
	// Files counts the archive files read.
	Files int `json:"files"`
	// Malformed counts lines that would not decode. Reported rather than
	// swallowed: a corrupt archive must not read as an empty one, which is
	// exactly the "zero is not an answer" failure the record exists to avoid.
	Malformed int `json:"malformed,omitempty"`
}

// ReadArchive reads every *.jsonl file under dir and returns the records
// matching filter, most recently archived first.
//
// A missing directory is not an error: nothing has been archived yet.
func ReadArchive(dir string, filter ArchiveFilter) (*ArchiveScan, error) {
	scan := &ArchiveScan{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return scan, nil
		}
		return nil, fmt.Errorf("read archive dir %s: %w", dir, err)
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
		records, malformed, err := readArchiveFile(filepath.Join(dir, name), filter)
		if err != nil {
			return nil, err
		}
		scan.Files++
		scan.Malformed += malformed
		scan.Records = append(scan.Records, records...)
	}

	sort.SliceStable(scan.Records, func(i, j int) bool {
		return scan.Records[i].ArchivedAt.After(scan.Records[j].ArchivedAt)
	})
	if filter.Limit > 0 && len(scan.Records) > filter.Limit {
		scan.Records = scan.Records[:filter.Limit]
	}
	return scan, nil
}

func readArchiveFile(path string, filter ArchiveFilter) ([]ArchivedWisp, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open archive %s: %w", path, err)
	}
	defer file.Close()

	var records []ArchivedWisp
	malformed := 0
	scanner := bufio.NewScanner(file)
	// Descriptions carry whole MR bodies and rejection rationales, so the
	// default 64KiB line cap is too small to assume.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec ArchivedWisp
		if err := json.Unmarshal(line, &rec); err != nil {
			malformed++
			continue
		}
		if matchesArchiveFilter(rec, filter) {
			records = append(records, rec)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, malformed, fmt.Errorf("read archive %s: %w", path, err)
	}
	return records, malformed, nil
}

func matchesArchiveFilter(rec ArchivedWisp, filter ArchiveFilter) bool {
	if filter.Database != "" && rec.Database != filter.Database {
		return false
	}
	if filter.ID != "" && rec.ID != filter.ID {
		return false
	}
	if filter.Contains != "" {
		needle := strings.ToLower(filter.Contains)
		haystacks := []string{rec.ID, rec.Title, rec.Description, rec.CloseReason}
		found := false
		for _, h := range haystacks {
			if strings.Contains(strings.ToLower(h), needle) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// nullText and nullTime scan columns that are nullable in production even where
// a narrowed fixture declares them NOT NULL. Scanning straight into string or
// time.Time fails on NULL with a driver error, which purge would report as an
// archive failure and answer by protecting rows it could have released — so the
// wrappers are about keeping the retention path working, not about tidiness.
type nullText struct{ sql.NullString }

func (n nullText) value() string {
	if n.Valid {
		return n.String
	}
	return ""
}

type nullTime struct{ sql.NullTime }

func (n nullTime) value() *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time.UTC()
	return &t
}

// archiveWispColumns is the column list collectArchivableWisps selects, in scan
// order. Every one of these exists in the production wisps DDL
// (internal/doltserver wispsCreateDDL); a database missing any of them fails the
// SELECT, which purge reports as an anomaly and answers by leaving the rows
// protected.
const archiveWispColumns = `w.id, w.title, w.description, w.design, w.notes, w.close_reason,
	w.status, w.priority, w.issue_type, w.wisp_type, w.assignee, w.created_by, w.owner,
	w.source_repo, w.created_at, w.updated_at, w.closed_at`

// collectArchivableWisps loads full records for the given wisp ids, including
// the auxiliary rows purge is about to delete alongside them.
//
// The auxiliary rows are part of the record, not decoration: wisp_comments is
// where a rejection rationale that did not fit the close reason ends up, and
// protecting the wisp row while its comments are deleted would leave a husk
// (the same point the gt-nmg tests assert about the protected path).
//
// The set collected here MUST stay equal to wispArchiveAuxTables, the tables
// the caller then deletes. Any table in that list with no attach* call below is
// a deletion with no record — which is what wisp_events was, for as long as the
// comment on that list claimed the two lists were the same thing (gt-wv8h).
func collectArchivableWisps(ctx context.Context, runner sqlRunner, dbName string, ids []string, archivedAt time.Time) ([]ArchivedWisp, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]interface{}, len(ids))
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		placeholders[i] = "?"
	}
	inClause := "(" + strings.Join(placeholders, ",") + ")"

	query := fmt.Sprintf("SELECT %s FROM wisps w WHERE w.id IN %s", archiveWispColumns, inClause)
	rows, err := runner.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("select archivable wisps: %w", err)
	}
	defer rows.Close()

	byID := map[string]*ArchivedWisp{}
	var out []ArchivedWisp
	for rows.Next() {
		var (
			rec                                                     ArchivedWisp
			description, design, notes, closeReason                 nullText
			issueType, wispType, assignee, createdBy, owner, srcRep nullText
			createdAt, updatedAt, closedAt                          nullTime
		)
		if err := rows.Scan(&rec.ID, &rec.Title, &description, &design, &notes, &closeReason,
			&rec.Status, &rec.Priority, &issueType, &wispType, &assignee, &createdBy, &owner,
			&srcRep, &createdAt, &updatedAt, &closedAt); err != nil {
			return nil, fmt.Errorf("scan archivable wisp: %w", err)
		}
		rec.Database = dbName
		rec.ArchivedAt = archivedAt
		rec.Description = description.value()
		rec.Design = design.value()
		rec.Notes = notes.value()
		rec.CloseReason = closeReason.value()
		rec.IssueType = issueType.value()
		rec.WispType = wispType.value()
		rec.Assignee = assignee.value()
		rec.CreatedBy = createdBy.value()
		rec.Owner = owner.value()
		rec.SourceRepo = srcRep.value()
		rec.CreatedAt = createdAt.value()
		rec.UpdatedAt = updatedAt.value()
		rec.ClosedAt = closedAt.value()
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read archivable wisps: %w", err)
	}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}

	if err := attachArchiveLabels(ctx, runner, byID, inClause, args); err != nil {
		return nil, err
	}
	if err := attachArchiveComments(ctx, runner, byID, inClause, args); err != nil {
		return nil, err
	}
	if err := attachArchiveDependencies(ctx, runner, byID, inClause, args); err != nil {
		return nil, err
	}
	if err := attachArchiveEvents(ctx, runner, byID, inClause, args); err != nil {
		return nil, err
	}
	return out, nil
}

func attachArchiveLabels(ctx context.Context, runner sqlRunner, byID map[string]*ArchivedWisp, inClause string, args []interface{}) error {
	query := "SELECT issue_id, label FROM wisp_labels WHERE issue_id IN " + inClause + " ORDER BY issue_id, label"
	rows, err := runner.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("select archive labels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, label string
		if err := rows.Scan(&id, &label); err != nil {
			return fmt.Errorf("scan archive label: %w", err)
		}
		if rec := byID[id]; rec != nil {
			rec.Labels = append(rec.Labels, label)
		}
	}
	return rows.Err()
}

func attachArchiveComments(ctx context.Context, runner sqlRunner, byID map[string]*ArchivedWisp, inClause string, args []interface{}) error {
	query := "SELECT issue_id, text FROM wisp_comments WHERE issue_id IN " + inClause + " ORDER BY issue_id, id"
	rows, err := runner.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("select archive comments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var text nullText
		if err := rows.Scan(&id, &text); err != nil {
			return fmt.Errorf("scan archive comment: %w", err)
		}
		if rec := byID[id]; rec != nil {
			rec.Comments = append(rec.Comments, text.value())
		}
	}
	return rows.Err()
}

func attachArchiveDependencies(ctx context.Context, runner sqlRunner, byID map[string]*ArchivedWisp, inClause string, args []interface{}) error {
	query := `SELECT issue_id, type, depends_on_wisp_id, depends_on_issue_id, depends_on_external
		FROM wisp_dependencies WHERE issue_id IN ` + inClause + ` ORDER BY issue_id, id`
	rows, err := runner.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("select archive dependencies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var dep ArchivedDependency
		var depType, wispID, issueID, external nullText
		if err := rows.Scan(&id, &depType, &wispID, &issueID, &external); err != nil {
			return fmt.Errorf("scan archive dependency: %w", err)
		}
		dep.Type = depType.value()
		dep.DependsOnWispID = wispID.value()
		dep.DependsOnIssueID = issueID.value()
		dep.DependsOnExternal = external.value()
		if rec := byID[id]; rec != nil {
			rec.Dependencies = append(rec.Dependencies, dep)
		}
	}
	return rows.Err()
}

// attachArchiveEvents reads wisp_events into the records, oldest first.
//
// Ordered by created_at and then id because the value of an event list is the
// SEQUENCE — submitted, then rejected, then resubmitted — and the id ordering
// alone is only incidentally chronological once ids come from an
// AUTO_INCREMENT shared with concurrent writers.
//
// Every column but issue_id is nullable in production or scanned through
// nullText, so a row with no actor or an empty comment costs a field and never
// the record: the caller answers an error here by leaving the rows in place,
// and holding a wisp forever because one of its events had a NULL old_value
// would be its own outage.
func attachArchiveEvents(ctx context.Context, runner sqlRunner, byID map[string]*ArchivedWisp, inClause string, args []interface{}) error {
	query := `SELECT issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM wisp_events WHERE issue_id IN ` + inClause + ` ORDER BY issue_id, created_at, id`
	rows, err := runner.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("select archive events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var eventType, actor, oldValue, newValue, comment nullText
		var createdAt nullTime
		if err := rows.Scan(&id, &eventType, &actor, &oldValue, &newValue, &comment, &createdAt); err != nil {
			return fmt.Errorf("scan archive event: %w", err)
		}
		if rec := byID[id]; rec != nil {
			rec.Events = append(rec.Events, ArchivedEvent{
				EventType: eventType.value(),
				Actor:     actor.value(),
				OldValue:  oldValue.value(),
				NewValue:  newValue.value(),
				Comment:   comment.value(),
				CreatedAt: createdAt.value(),
			})
		}
	}
	return rows.Err()
}
