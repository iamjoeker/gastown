// Package reaper provides wisp and issue cleanup operations for Dolt databases.
//
// These functions are the "callable helper functions" for the Dog-driven
// mol-dog-reaper formula. They execute SQL operations but do not make
// eligibility decisions — the Dog (or daemon orchestrator) decides what
// to reap, purge, and auto-close based on the formula.
package reaper

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// validDBName matches safe database names (alphanumeric, underscore, hyphen).
var validDBName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// DefaultDatabases is the static fallback list of known production databases.
// Used only when SHOW DATABASES fails (server unreachable).
// GH#2385: Removed legacy "gt" and "bd" names — modern towns use "hq" (town
// beads) and rig-specific names. Those databases no longer exist in most
// installations and their presence in the fallback caused phantom DB errors.
var DefaultDatabases = []string{"hq"}

// testPollutionPrefixes are database name prefixes created by tests.
var testPollutionPrefixes = []string{"testdb_", "beads_t", "beads_pt", "doctest_"}

// isNothingToCommit returns true if the error is a Dolt "nothing to commit" error.
func isNothingToCommit(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "nothing to commit")
}

// isTableNotFound returns true if the error indicates a missing table.
// This happens when beads stores its data on a separate Dolt instance from
// the gt Dolt server, so tables like issues/labels/dependencies don't exist
// on the server the reaper connects to.
func isTableNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "table not found") || strings.Contains(msg, "doesn't exist")
}

// DiscoverDatabases queries SHOW DATABASES on the Dolt server and returns
// all production databases, filtering out system databases and test pollution.
// Falls back to DefaultDatabases on any error.
func DiscoverDatabases(host string, port int) []string {
	dsn := fmt.Sprintf("root@tcp(%s:%d)/?parseTime=true&timeout=5s", host, port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return DefaultDatabases
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return DefaultDatabases
	}
	defer rows.Close()

	var databases []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if name == "information_schema" || name == "mysql" {
			continue
		}
		lower := strings.ToLower(name)
		skip := false
		for _, prefix := range testPollutionPrefixes {
			if strings.HasPrefix(lower, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		databases = append(databases, name)
	}

	if len(databases) == 0 {
		return DefaultDatabases
	}
	return databases
}

// ScanResult holds the results of scanning a database for reaper candidates.
type ScanResult struct {
	Database               string `json:"database"`
	ReapCandidates         int    `json:"reap_candidates"`
	MoleculeStepCandidates int    `json:"molecule_step_candidates,omitempty"`
	PurgeCandidates        int    `json:"purge_candidates"`
	MailCandidates         int    `json:"mail_candidates"`
	StaleCandidates        int    `json:"stale_candidates"`
	OpenWisps              int    `json:"open_wisps"`
	// ProtectedFromPurge counts closed wisps past purge_age that purge will
	// NOT delete because they are pinned or carry a protected label. They are
	// excluded from PurgeCandidates, so the two never double-count.
	ProtectedFromPurge int `json:"protected_from_purge,omitempty"`
	// ArchivableFromPurge counts the subset of ProtectedFromPurge that a purge
	// running with an archive would export and then release: label-protected
	// but not pinned.
	//
	// Reported unconditionally, because Scan is read-only and has no archive to
	// ask. It answers the question the protected count alone cannot — "how much
	// of this accumulation is retention policy would clear" — without which
	// ProtectedFromPurge only ever grows and says nothing about why (gt-6xwt).
	ArchivableFromPurge int       `json:"archivable_from_purge,omitempty"`
	Anomalies           []Anomaly `json:"anomalies,omitempty"`
}

// ReapResult holds the results of a reap operation.
type ReapResult struct {
	Database            string `json:"database"`
	Reaped              int    `json:"reaped"`
	MoleculeStepsClosed int    `json:"molecule_steps_closed,omitempty"`
	OpenRemain          int    `json:"open_remain"`
	// Passes counts the closing rounds Reap ran, including the final round that
	// closed nothing and thereby proved the fixed point. A run with nothing to do
	// is 1; any run that closes anything is at least 2. Omitted for dry runs,
	// which close nothing and so run no rounds at all.
	Passes    int       `json:"passes,omitempty"`
	DryRun    bool      `json:"dry_run,omitempty"`
	Anomalies []Anomaly `json:"anomalies,omitempty"`
}

// PurgeResult holds the results of a purge operation.
//
// WispsPurged, WispsArchived and WispsProtected PARTITION the closed-past-
// purge_age window: every row in it is deleted outright, exported and then
// deleted, or held back. No row is counted twice and none is counted nowhere,
// which is what keeps a shrinking number readable — the reason the protected
// count exists at all is that a purge which declines to delete must not look
// like a purge that had less to do.
type PurgeResult struct {
	Database    string `json:"database"`
	WispsPurged int    `json:"wisps_purged"`
	MailPurged  int    `json:"mail_purged"`
	// WispsArchived counts closed wisps past purge_age that were exported to
	// the durable archive and then deleted. Zero when no archive is configured,
	// which is also when protection is absolute (gt-6xwt).
	WispsArchived int `json:"wisps_archived,omitempty"`
	// WispsProtected counts closed wisps past purge_age that were skipped
	// because they are pinned, carry a protected label with no archive
	// configured, or could not be archived. Reported so a purge that declines
	// to delete says so, rather than looking like a smaller purge.
	WispsProtected int       `json:"wisps_protected,omitempty"`
	DryRun         bool      `json:"dry_run,omitempty"`
	Anomalies      []Anomaly `json:"anomalies,omitempty"`
}

// ClosedEntry records an individual issue closure with details for logging.
type ClosedEntry struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	AgeDays  int    `json:"age_days"`
	Database string `json:"database"`
}

// AutoCloseResult holds the results of an auto-close operation.
type AutoCloseResult struct {
	Database      string        `json:"database"`
	Closed        int           `json:"closed"`
	ClosedEntries []ClosedEntry `json:"closed_entries,omitempty"`
	DryRun        bool          `json:"dry_run,omitempty"`
	Anomalies     []Anomaly     `json:"anomalies,omitempty"`
}

// Anomaly represents an unexpected condition found during reaper operations.
type Anomaly struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

const (
	// DefaultQueryTimeout is the timeout for individual reaper SQL queries.
	DefaultQueryTimeout = 30 * time.Second
	// DefaultBatchSize is the number of rows per batch DELETE operation.
	DefaultBatchSize = 100
	// DefaultAlertThreshold is the open-wisp count above which callers should
	// surface a warning. This must fire on genuine runaway accumulation, NOT on
	// normal operation. The open-wisp count is dominated by healthy, recent
	// wisps (observed steady-state ~1966 open in a busy town); actionable wisps
	// are limited to stale open-parent-free wisps past max-age plus closed
	// molecule-step wisps (typically ~15-25). The previous value of 800 sat below
	// the healthy open count, so it false-alarmed HIGH every scan despite nothing
	// being wrong. Raised to 3000 so the alert tracks runaway growth rather than
	// the normal working set. See hq-57jr8.
	DefaultAlertThreshold = 3000
	// maxReapPasses bounds Reap's fixed-point loop (see the loop in Reap for why
	// one pass cannot converge). Each pass releases the children of whatever the
	// previous pass closed, so the passes a real cascade needs is the depth of the
	// molecule/parent chain — 2-4 in the field, and every pass past the last one
	// with work is two cheap COUNT-shaped SELECTs that return nothing. The limit
	// exists only so a candidate query that somehow reopens work cannot spin
	// against production Dolt until the context deadline; reaching it is an
	// anomaly, not a normal outcome.
	maxReapPasses = 20
)

// ValidateDBName returns an error if the database name is unsafe.
func ValidateDBName(dbName string) error {
	if !validDBName.MatchString(dbName) {
		return fmt.Errorf("invalid database name: %q", dbName)
	}
	return nil
}

// OpenDB opens a connection to the Dolt server for a given database.
func OpenDB(host string, port int, dbName string, readTimeout, writeTimeout time.Duration) (*sql.DB, error) {
	if err := ValidateDBName(dbName); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("root@tcp(%s:%d)/%s?parseTime=true&timeout=5s&readTimeout=%s&writeTimeout=%s",
		host, port, dbName,
		fmt.Sprintf("%ds", int(readTimeout.Seconds())),
		fmt.Sprintf("%ds", int(writeTimeout.Seconds())))
	return sql.Open("mysql", dsn)
}

// parentExcludeJoin returns a LEFT JOIN clause and WHERE condition that restricts
// results to wisps whose parent molecule is closed, missing, or nonexistent.
//
// This replaces the previous parentCheckWhere() which used 3 correlated EXISTS
// subqueries per row, causing O(n*m) query cost on large wisp tables (gt-jd1z).
// The LEFT JOIN approach runs the subquery once and hash-joins: O(n+m).
//
// Semantics (unchanged from parentCheckWhere):
//   - No parent-child dependency → eligible (orphan wisps)
//   - Parent status is 'closed' → eligible (parent already reaped)
//   - Parent row missing (dangling ref) → eligible (parent already purged)
//
// The inverse is simpler: exclude wisps that have an OPEN parent.
//
// Usage:
//
//	join, where := parentExcludeJoin(dbName)
//	query := fmt.Sprintf("SELECT ... FROM wisps w %s WHERE ... AND %s", dbName, join, where)
func parentExcludeJoin(dbName string) (joinClause, whereCondition string) {
	joinClause = `LEFT JOIN (
		SELECT DISTINCT wd.issue_id
		FROM wisp_dependencies wd
		LEFT JOIN wisps pw ON pw.id = wd.depends_on_wisp_id LEFT JOIN issues pi ON pi.id = wd.depends_on_issue_id
		WHERE wd.type = 'parent-child'
		AND (pw.status IN ('open', 'hooked', 'in_progress') OR pi.status IN ('open', 'hooked', 'in_progress') OR wd.depends_on_external IS NOT NULL)
	) open_parent ON open_parent.issue_id = w.id`
	whereCondition = "open_parent.issue_id IS NULL"
	return
}

const openWispStatusWhere = "w.status IN ('open', 'hooked', 'in_progress')"

// ProtectedWispLabels lists labels whose wisps purge must never delete,
// whatever their age, status, or caller.
//
// Exported because it is the town's single list of never-delete types, and
// gastown has more than one deleter. gt-6dp's whole shape is a guard that
// holds on one path and is inert on the next: `bd purge` skips these labels,
// this package's native SQL delete skips them, and `gt compact` — which reaches
// the database by a third route, `bd delete --force` — did not, because
// --force is documented as the deliberate override that BYPASSES the bd-side
// skip. A second copy of this list would drift; a second list would be a second
// bug. Add a deleter, import this.
//
// gt:merge-request is here because "closed" is not a proxy for "worthless" for
// this one type, and that is a property of the type rather than of the caller
// (gt-nmg). A merge-request wisp closed WITHOUT merging is the only record that
// the work did not land, and it carries the rejection rationale. On 2026-08-17
// seven closed MR beads on the beads rig were deleted eleven minutes after
// closure, including one such rejection; the reasoning is unrecoverable
// (gt-6dp). Wisps are unversioned and unbacked — there is no dolt history to
// restore from, so an age bound alone only delays that outcome: a merge queue
// that stalls for a week produces exactly the record this protects.
//
// THE COST IS REAL AND DELIBERATE: these rows now accumulate without bound.
// Measured on the gastown database 2026-08-18, MR wisps close at roughly 25/day
// on one busy rig, so this trades unbounded growth for recoverability. That is
// the trade gt-nmg argues for explicitly, on the grounds that the growth is
// merely expensive while the deletion is final. It is not free, and it wants a
// retention policy that archives before deleting rather than a bigger age bound
// — filed separately. The "Protected (skipped): N" line the purge now prints is
// what keeps the accumulation visible instead of silent.
//
// gt:escalation is here for the same reason and one more (gt-nhp). An
// escalation record is a durable artifact by definition — it is the town's
// "somebody must look at this" channel — but `gt escalate` creates it as a wisp,
// so it landed in the age-delete set with no ownership filter and no digest on
// delete. The record carries the structured severity/reason/escalated_by/
// closed_by fields; the delivered copy carries the mail body. Deleting the
// record destroys the only copy of the resolution rationale, and wisps are
// unversioned and unbacked, so there is nothing to restore from.
//
// It compounds: reap keys eligibility on age, so an escalation nobody touches
// ages into the window FIRST, and the escalations most likely to be deleted are
// exactly the ones nobody acted on — the population escalation exists to
// protect. See reapProtectWhere for the closing half of that.
//
// Anything added here must be a type whose closed rows are evidence, not
// residue. It is not a place to park beads that are merely inconvenient to lose.
var ProtectedWispLabels = []string{"gt:merge-request", "gt:escalation"}

// ReapProtectedWispLabels lists labels whose OPEN wisps the age-based reap must
// never close. It is a different question from ProtectedWispLabels, which is
// about DELETING, and the two lists are deliberately not the same list.
//
// Reap closes any open wisp past max_age whose parent molecule is closed or
// absent. An escalation record has no parent at all, so it is always eligible,
// and reap closing it is not a harmless bookkeeping change: `gt escalate list`
// renders the delivered copies and hides any copy whose RECORD is closed
// (dropResolvedEscalations). So a reaped record silently removes a live,
// unacknowledged escalation from the only surface an operator reads — the
// escalation still exists and still needs attention, and now nothing shows it.
// Purge protection alone does not cover this: the record survives as a row and
// vanishes from the queue anyway (gt-nhp).
//
// gt:merge-request is NOT here. Its wisps are closed by the merge queue's own
// lifecycle and gt-nmg's fix deliberately scoped MR protection to deletion;
// changing when MR wisps close is a merge-queue decision, not this one.
var ReapProtectedWispLabels = []string{"gt:escalation"}

// AutoCloseExemptLabels lists labels whose OPEN issues staleness auto-close must
// never touch.
//
// This is the ISSUES-table counterpart of the wisp lists above, and it is a var
// rather than two hand-copied SQL literals because it was already inlined in two
// places that drifted: Scan's candidate count omitted the filter entirely and
// over-reported what AutoClose would close (gt-jbn). Both queries now render
// this one list.
//
//   - gt:agent — the town's per-role identity rows. `gt agents resolve` answers
//     "no agent bead found" once one is CLOSED, which halts mol-witness-patrol.
//   - gt:message — unread mail. Reading a message closes its bead, so an OPEN
//     one is by definition unread; auto-closing it stamps closed_at and
//     purgeOldMail deletes it mailDeleteAge later (gt-jbn).
//   - gt:escalation — the durable delivered copy of an escalation (gt-nhp).
//     Same shape as gt:message: an open escalation is by definition one nobody
//     has resolved, and the P0/P1 exclusion does not save it, because only
//     critical and high map to P0/P1 — a medium or low escalation sits at P2/P3
//     and is closed purely for having been ignored. Its updated_at never moves
//     precisely BECAUSE nobody attended to it, so an unattended escalation
//     reaches the window before an attended one.
var AutoCloseExemptLabels = []string{
	"gt:standing-orders", "gt:keep", "gt:role", "gt:rig", "gt:agent", "gt:message", "gt:escalation",
}

// sqlLabelList renders labels as a SQL IN(...) body: 'a', 'b', 'c'.
//
// The labels are compile-time constants from the lists above, never user input.
func sqlLabelList(labels []string) string {
	quoted := make([]string, len(labels))
	for i, label := range labels {
		quoted[i] = "'" + label + "'"
	}
	return strings.Join(quoted, ", ")
}

// reapProtectWhere returns a WHERE fragment, for queries that alias wisps as
// "w", excluding wisps whose type must never be closed by age.
//
// Applied to every candidate query that feeds closeWispsInBatches — the stale
// max-age path AND the closed-molecule-step path — so "no reap path closes an
// escalation" holds without depending on which route a wisp arrives by.
//
// Excluding protected rows from SELECTION rather than declining them at UPDATE
// time is deliberate, and matches purgeProtectWhere: closeWispsInBatches
// re-runs its id query until it stops yielding work, so a row that is offered
// and then declined makes no progress and is reported as a stall.
func reapProtectWhere() string {
	return fmt.Sprintf(
		"w.id NOT IN (SELECT DISTINCT rl.issue_id FROM wisp_labels rl WHERE rl.label IN (%s))",
		sqlLabelList(ReapProtectedWispLabels))
}

// purgeProtectWhere returns a WHERE fragment, for queries that alias wisps as
// "w", selecting only rows purge is permitted to delete.
//
// Two independent guards:
//
//   - pinned — the column an incident responder can set by hand
//     (`bd sql "update wisps set pinned=1 where id=..."`) to protect a specific
//     record right now. bd purge already honours it; this path did not, so a
//     responder who pinned a record was protected from one deleter and not the
//     other (gt-nmg). COALESCE because the column is nullable.
//   - ProtectedWispLabels — protection by type, which needs nobody to have
//     anticipated the specific record.
//
// Deliberately an uncorrelated NOT IN over wisp_labels, mirroring the stale-issue
// query, rather than a correlated EXISTS: the correlated form on this table is
// what cost O(n*m) and produced the CPU spikes in gt-wvd2.
//
// Callers must apply this to the counting query AND the deleting query, or scan
// reports candidates purge will never take. It is also what keeps
// batchDeleteRows terminating: it re-runs its id query until it returns nothing,
// so a row that is selected but not deletable would spin forever. Excluding
// protected rows from selection — rather than declining them at DELETE time —
// is what makes that impossible.
func purgeProtectWhere() string {
	return fmt.Sprintf(
		"COALESCE(w.pinned, 0) = 0 AND w.id NOT IN (SELECT DISTINCT pl.issue_id FROM wisp_labels pl WHERE pl.label IN (%s))",
		sqlLabelList(ProtectedWispLabels))
}

// archivableProtectWhere returns a WHERE fragment, for queries that alias wisps
// as "w", selecting the protected rows a purge with an archive may export and
// then delete: protected by TYPE, and not pinned.
//
// It is the exact complement of purgeProtectWhere within the closed-past-cutoff
// window, and that is deliberate. purgeProtectWhere selects (not pinned AND not
// labelled); its negation is (pinned OR labelled); this selects (not pinned AND
// labelled). The three sets therefore partition the window into purge / archive
// / hold, which is what lets PurgeResult report three counts that always add up
// and never absorb one another.
//
// PINNED IS EXCLUDED ON PURPOSE, and it is the one line here worth arguing
// about. Label protection is a standing judgement about a type; `pinned` is an
// incident responder reaching for the one lever that protects one specific row
// right now (`bd sql "update wisps set pinned=1 where id=..."`, gt-nmg). A
// retention policy that quietly exported and deleted the row they pinned would
// answer their instruction with a file they did not ask for and a row that is
// gone from every query they were about to run. Retention releases the routine
// accumulation; the pin stays a pin.
func archivableProtectWhere() string {
	return fmt.Sprintf(
		"COALESCE(w.pinned, 0) = 0 AND w.id IN (SELECT DISTINCT al.issue_id FROM wisp_labels al WHERE al.label IN (%s))",
		sqlLabelList(ProtectedWispLabels))
}

// closedMoleculeStepSubquery selects step-wisps whose parent molecule has already closed.
// wisp_dependencies.issue_id is the child; depends_on_wisp_id is the parent molecule.
const closedMoleculeStepSubquery = `
	SELECT DISTINCT wd.issue_id
	FROM wisp_dependencies wd
	INNER JOIN wisps pm ON pm.id = wd.depends_on_wisp_id
	WHERE wd.type = 'parent-child'
	AND pm.issue_type = 'molecule'
	AND pm.status = 'closed'
	AND NOT EXISTS (
		SELECT 1 FROM wisp_dependencies open_dep
		LEFT JOIN wisps open_pw ON open_pw.id = open_dep.depends_on_wisp_id
		LEFT JOIN issues open_pi ON open_pi.id = open_dep.depends_on_issue_id
		WHERE open_dep.issue_id = wd.issue_id
		AND open_dep.type = 'parent-child'
		AND (open_pw.status IN ('open', 'hooked', 'in_progress') OR open_pi.status IN ('open', 'hooked', 'in_progress') OR open_dep.depends_on_external IS NOT NULL)
	)`

func closedMoleculeStepJoin(alias string) string {
	return fmt.Sprintf("INNER JOIN (%s) %s ON %s.issue_id = w.id", closedMoleculeStepSubquery, alias, alias)
}

func closedMoleculeStepExcludeJoin(alias string) string {
	return fmt.Sprintf("LEFT JOIN (%s) %s ON %s.issue_id = w.id", closedMoleculeStepSubquery, alias, alias)
}

type sqlRunner interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

// writeSession is a single pooled connection reserved for one write sequence,
// with autocommit disabled on it.
//
// Every reaper write path follows the same shape: disable autocommit, run the
// UPDATE/DELETE batches, COMMIT to flush the SQL transaction into the Dolt
// working set, then CALL DOLT_COMMIT on that working set. @@autocommit is a
// SESSION variable, so all four steps must run on the SAME session.
//
// Issuing them on the *sql.DB pool does not guarantee that: database/sql hands
// each call whatever connection is free. The batches and the COMMIT can land on
// sessions that were never switched to autocommit=0, which makes the flushing
// COMMIT a no-op, leaves a mid-sequence failure partially committed instead of
// rolled back, and lets the restoring "SET @@autocommit = 1" repair a connection
// that was never changed while the one that was stays at 0 for whoever picks it
// up next. This stayed latent only because the reaper's pool normally settles to
// a single connection (gt-gjh).
type writeSession struct {
	conn      *sql.Conn
	committed bool
}

// beginWriteSession pins a connection out of the pool and disables autocommit on
// it. Callers must defer release.
func beginWriteSession(ctx context.Context, db *sql.DB) (*writeSession, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("pin connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("disable autocommit: %w", err)
	}
	return &writeSession{conn: conn}, nil
}

// commit flushes the SQL transaction into the Dolt working set. With
// autocommit=0 the batched changes sit in the transaction buffer; DOLT_COMMIT
// operates on the working set, so without this it sees "nothing to commit".
func (s *writeSession) commit(ctx context.Context) error {
	if _, err := s.conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	s.committed = true
	return nil
}

// release rolls back anything the session left uncommitted, restores autocommit
// on that same session, and returns the connection to the pool.
func (s *writeSession) release() {
	if !s.committed {
		_, _ = s.conn.ExecContext(context.Background(), "ROLLBACK")
	}
	_, _ = s.conn.ExecContext(context.Background(), "SET @@autocommit = 1")
	_ = s.conn.Close()
}

// HasReaperSchema checks whether the database has the tables required for reaper
// operations (wisps and issues). Returns false (no error) when tables are missing
// — callers use this to skip databases that have incomplete beads schema (e.g.
// partially initialized databases on the central Dolt server).
func HasReaperSchema(db *sql.DB) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_name IN ('wisps', 'issues', 'wisp_dependencies') AND table_schema = DATABASE()").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check reaper schema: %w", err)
	}
	if count < 3 {
		return false, nil
	}

	hasWispDependencyColumns, err := hasColumns(ctx, db, "wisp_dependencies", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external")
	if err != nil || !hasWispDependencyColumns {
		return hasWispDependencyColumns, err
	}
	dependenciesExists, err := tableExists(ctx, db, "dependencies")
	if err != nil || !dependenciesExists {
		return !dependenciesExists, err
	}
	return hasColumns(ctx, db, "dependencies", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external")
}

func tableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ? AND table_schema = DATABASE()", table).Scan(&count)
	return count > 0, err
}

func hasColumns(ctx context.Context, db *sql.DB, table string, columns ...string) (bool, error) {
	if len(columns) == 0 {
		return true, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	args := make([]interface{}, 0, len(columns)+1)
	args = append(args, table)
	for _, column := range columns {
		args = append(args, column)
	}
	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name IN (%s)", placeholders)
	err := db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count == len(columns), err
}

// Scan counts reaper candidates in a database without modifying anything.
func Scan(db *sql.DB, dbName string, maxAge, purgeAge, mailDeleteAge, staleIssueAge time.Duration) (*ScanResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	result := &ScanResult{Database: dbName}
	now := time.Now().UTC()
	parentJoin, parentWhere := parentExcludeJoin(dbName)
	moleculeStepJoin := closedMoleculeStepJoin("closed_molecule_step")
	moleculeStepExcludeJoin := closedMoleculeStepExcludeJoin("closed_molecule_step")

	moleculeStepQuery := fmt.Sprintf(
		"SELECT COUNT(*) FROM wisps w %s WHERE %s AND w.issue_type != 'agent' AND %s",
		moleculeStepJoin, openWispStatusWhere, reapProtectWhere())
	if err := db.QueryRowContext(ctx, moleculeStepQuery).Scan(&result.MoleculeStepCandidates); err != nil {
		return nil, fmt.Errorf("count molecule step candidates: %w", err)
	}

	// Count reap candidates: open wisps past max_age with eligible parent status.
	// Must match Reap() eligibility semantics exactly, including the exclusion of
	// agent beads, otherwise scan can report candidates that reap will never close.
	// Uses LEFT JOIN anti-pattern instead of correlated EXISTS to avoid O(n*m) cost (gt-jd1z).
	// Closed-molecule steps are counted separately above and excluded here so counts stay disjoint.
	// reapProtectWhere must match Reap's whereClause exactly, for the same reason
	// purgeProtectWhere must match purgeClosedWisps.
	reapQuery := fmt.Sprintf(
		"SELECT COUNT(*) FROM wisps w %s %s WHERE %s AND w.created_at < ? AND w.issue_type != 'agent' AND %s AND %s AND closed_molecule_step.issue_id IS NULL",
		parentJoin, moleculeStepExcludeJoin, openWispStatusWhere, parentWhere, reapProtectWhere())
	if err := db.QueryRowContext(ctx, reapQuery, now.Add(-maxAge)).Scan(&result.ReapCandidates); err != nil {
		return nil, fmt.Errorf("count reap candidates: %w", err)
	}

	// Count purge candidates: closed wisps past purge_age that are not protected.
	// No parent check needed — closed wisps past the delete age are purgeable.
	// The parent check (correlated subqueries on wisp_dependencies) was causing O(n*m) query
	// cost with 1800+ closed wisps, leading to CPU spikes and connection timeouts (gt-wvd2).
	// purgeProtectWhere must match purgeClosedWisps exactly, or this reports rows
	// purge will never delete — the same scan/act divergence the reap count guards against.
	purgeQuery := "SELECT COUNT(*) FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? AND " + purgeProtectWhere()
	if err := db.QueryRowContext(ctx, purgeQuery, now.Add(-purgeAge)).Scan(&result.PurgeCandidates); err != nil {
		return nil, fmt.Errorf("count purge candidates: %w", err)
	}

	// Count what the protection held back. Reported separately so a shrinking
	// purge count reads as "protected N", not as "there was less to do".
	protectedQuery := "SELECT COUNT(*) FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? AND NOT (" + purgeProtectWhere() + ")"
	if err := db.QueryRowContext(ctx, protectedQuery, now.Add(-purgeAge)).Scan(&result.ProtectedFromPurge); err != nil {
		return nil, fmt.Errorf("count purge-protected wisps: %w", err)
	}

	// Of those, how many a purge with an archive would export and release.
	// Same window and the same fragment purgeClosedWisps archives by, so this
	// cannot advertise rows the archive path would decline — the divergence the
	// purge and reap counts above are both written to avoid.
	archivableQuery := "SELECT COUNT(*) FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? AND " + archivableProtectWhere()
	if err := db.QueryRowContext(ctx, archivableQuery, now.Add(-purgeAge)).Scan(&result.ArchivableFromPurge); err != nil {
		return nil, fmt.Errorf("count archivable wisps: %w", err)
	}

	// Count mail candidates.
	// The issues/labels tables may not exist on the gt Dolt server if beads
	// stores its data on a separate Dolt instance. Skip gracefully.
	mailQuery := "SELECT COUNT(*) FROM issues WHERE status = 'closed' AND closed_at < ? AND id IN (SELECT issue_id FROM labels WHERE label = 'gt:message')"
	if err := db.QueryRowContext(ctx, mailQuery, now.Add(-mailDeleteAge)).Scan(&result.MailCandidates); err != nil {
		if !isTableNotFound(err) {
			return nil, fmt.Errorf("count mail candidates: %w", err)
		}
		// issues/labels table not on this server — skip mail count
	}

	// Count stale issue candidates.
	// Same caveat: issues/dependencies tables may live on a separate Dolt instance.
	// Convoys excluded to mirror AutoClose (hq-jnap): convoy lifecycle is
	// tracked-bead-status driven, never staleness driven.
	// The protected-label exclusion mirrors AutoClose too, so the count the Dog
	// reads is the count AutoClose would actually close — it previously omitted
	// the label filter and over-reported (gt-jbn).
	staleQuery := `
		SELECT COUNT(*) FROM issues i
		WHERE i.status IN ('open', 'in_progress')
		AND i.updated_at < ?
		AND i.priority > 1
		AND i.issue_type NOT IN ('epic', 'convoy')
		AND i.id NOT IN (
			SELECT DISTINCT l.issue_id FROM labels l
			WHERE l.label IN (` + sqlLabelList(AutoCloseExemptLabels) + `)
		)
		AND i.id NOT IN (
			SELECT DISTINCT d.issue_id FROM dependencies d
			INNER JOIN issues dep ON d.depends_on_issue_id = dep.id
			WHERE dep.status IN ('open', 'in_progress')
		)
		AND i.id NOT IN (
			SELECT DISTINCT d.depends_on_issue_id FROM dependencies d
			INNER JOIN issues blocker ON d.issue_id = blocker.id
			WHERE d.depends_on_issue_id IS NOT NULL
			AND blocker.status IN ('open', 'in_progress')
		)`
	if err := db.QueryRowContext(ctx, staleQuery, now.Add(-staleIssueAge)).Scan(&result.StaleCandidates); err != nil {
		if !isTableNotFound(err) {
			return nil, fmt.Errorf("count stale candidates: %w", err)
		}
		// issues/dependencies table not on this server — skip stale count
	}

	// Total open wisps.
	openQuery := "SELECT COUNT(*) FROM wisps WHERE status IN ('open', 'hooked', 'in_progress')"
	if err := db.QueryRowContext(ctx, openQuery).Scan(&result.OpenWisps); err != nil {
		return nil, fmt.Errorf("count open wisps: %w", err)
	}

	// Anomaly detection: dangling parent references.
	danglingQuery := `
		SELECT COUNT(*) FROM wisp_dependencies wd
		LEFT JOIN wisps pw ON pw.id = wd.depends_on_wisp_id LEFT JOIN issues pi ON pi.id = wd.depends_on_issue_id
		WHERE wd.type = 'parent-child' AND wd.depends_on_external IS NULL AND (wd.depends_on_wisp_id IS NOT NULL OR wd.depends_on_issue_id IS NOT NULL) AND pw.id IS NULL AND pi.id IS NULL`
	var danglingCount int
	if err := db.QueryRowContext(ctx, danglingQuery).Scan(&danglingCount); err == nil && danglingCount > 0 {
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type:    "dangling_parent_ref",
			Message: fmt.Sprintf("%d wisp(s) have parent dependency records pointing to purged/missing parents", danglingCount),
			Count:   danglingCount,
		})
	}

	return result, nil
}

// Reap closes stale wisps in a database whose parent molecule is already closed.
// UPDATEs are batched to avoid holding a write lock for extended periods on large tables.
//
// A real (non-dry-run) call is a FIXED POINT: it repeats its closing passes until
// a pass closes nothing, so a caller never has to iterate to finish the cascade
// (gt-r1b). See the loop below for why one pass of each cannot converge.
func Reap(db *sql.DB, dbName string, maxAge time.Duration, dryRun bool) (*ReapResult, error) {
	// Use a longer timeout to accommodate batched processing across large tables.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cutoff := time.Now().UTC().Add(-maxAge)
	parentJoin, parentWhere := parentExcludeJoin(dbName)
	moleculeStepJoin := closedMoleculeStepJoin("closed_molecule_step")
	moleculeStepExcludeJoin := closedMoleculeStepExcludeJoin("closed_molecule_step")
	// Exclude agent beads (issue_type='agent') from reaping — they have persistent
	// identity and should not be closed by the wisp reaper regardless of age.
	// Closed-molecule steps are closed immediately through a separate path, so stale
	// max-age counts exclude them to keep dry-run and scan counts disjoint.
	// reapProtectWhere excludes types that must never be closed by age at all —
	// escalation records, whose whole purpose is to persist while unattended
	// (gt-nhp). It is applied to BOTH candidate paths below, so no route into
	// closeWispsInBatches can close one.
	whereClause := fmt.Sprintf(
		"%s AND w.created_at < ? AND w.issue_type != 'agent' AND %s AND %s AND closed_molecule_step.issue_id IS NULL",
		openWispStatusWhere, parentWhere, reapProtectWhere())

	result := &ReapResult{Database: dbName, DryRun: dryRun}

	if dryRun {
		// These counts are ONE pass, and so a lower bound on what a real reap
		// closes: they report the wisps closable against the database as it stands
		// now, and cannot see the cascade a close releases (a molecule wisp counted
		// here frees its steps only once it is actually closed). Simulating that
		// would mean mutating, which is the one thing a dry run must not do. Read a
		// dry-run count as "at least this many", not as a prediction of Reaped.
		moleculeStepCountQuery := fmt.Sprintf(
			"SELECT COUNT(*) FROM wisps w %s WHERE %s AND w.issue_type != 'agent' AND %s",
			moleculeStepJoin, openWispStatusWhere, reapProtectWhere())
		if err := db.QueryRowContext(ctx, moleculeStepCountQuery).Scan(&result.MoleculeStepsClosed); err != nil {
			return nil, fmt.Errorf("dry-run molecule step count: %w", err)
		}
		countQuery := fmt.Sprintf("SELECT COUNT(*) FROM wisps w %s %s WHERE %s", parentJoin, moleculeStepExcludeJoin, whereClause)
		if err := db.QueryRowContext(ctx, countQuery, cutoff).Scan(&result.Reaped); err != nil {
			return nil, fmt.Errorf("dry-run count: %w", err)
		}
		openQuery := "SELECT COUNT(*) FROM wisps WHERE status IN ('open', 'hooked', 'in_progress')"
		if err := db.QueryRowContext(ctx, openQuery).Scan(&result.OpenRemain); err != nil {
			return nil, fmt.Errorf("count open: %w", err)
		}
		return result, nil
	}

	session, err := beginWriteSession(ctx, db)
	if err != nil {
		return nil, err
	}
	defer session.release()
	conn := session.conn

	// The `issue_type != 'agent'` clause here is paired with the identical clause
	// on closeWispsInBatches' UPDATE. Keep both: a candidate this query offers and
	// the UPDATE declines makes no progress, which the batch loop reports as a
	// stall (see closeWispsInBatches).
	moleculeStepIDQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s WHERE %s AND w.issue_type != 'agent' AND %s LIMIT %d",
		moleculeStepJoin, openWispStatusWhere, reapProtectWhere(), DefaultBatchSize)

	// Batch UPDATE: select IDs in chunks, update each chunk.
	// This avoids holding a write lock on the entire table for minutes.
	// Uses LEFT JOIN anti-pattern instead of correlated EXISTS to avoid O(n*m) cost (gt-jd1z).
	// whereClause carries the same `issue_type != 'agent'` guard as the UPDATE —
	// see the note on moleculeStepIDQuery above.
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s %s WHERE %s LIMIT %d",
		parentJoin, moleculeStepExcludeJoin, whereClause, DefaultBatchSize)

	// Run both passes to a FIXED POINT rather than once each (gt-r1b).
	//
	// The two passes feed each other, so one round of each cannot converge:
	//
	//   - The stale pass closes any wisp past max-age, molecule wisps included.
	//     closedMoleculeStepSubquery selects steps whose parent molecule is
	//     ALREADY closed, so the steps of a molecule this round just closed only
	//     become step-pass candidates afterwards.
	//   - Symmetrically, parentExcludeJoin holds a child back while any parent is
	//     open, so closing a parent in the step pass releases its children to the
	//     stale pass.
	//
	// Observed on hq at --max-age=24h: run 1 reaped 23 (3 of them orphaned
	// molecule wisps) and closed 0 steps; run 2 closed 9 steps as the cascade;
	// runs 3-4 drained the rest. A single invocation left 10 wisps open and
	// reported success, and nothing downstream re-checked. Looping here — rather
	// than asking every caller and formula to iterate — is what makes one call
	// honour its own postcondition.
	//
	// Termination: a round that closes nothing ends the loop, and every close
	// moves a row out of the open set the candidate queries read, so progress is
	// monotone. maxReapPasses is a backstop for a candidate/UPDATE pair that
	// somehow reopens work, not the expected exit — exhausting it is reported as
	// an anomaly rather than passed off as a converged run.
	var (
		moleculeStepsClosed        int
		totalReaped                int
		stepsStalled, staleStalled int
		converged                  bool
	)
	for result.Passes = 1; result.Passes <= maxReapPasses; result.Passes++ {
		stepsThisPass, stepsStalledThisPass, err := closeWispsInBatches(ctx, conn, moleculeStepIDQuery, nil, "closed molecule steps")
		if err != nil {
			return nil, err
		}
		staleThisPass, staleStalledThisPass, err := closeWispsInBatches(ctx, conn, idQuery, []interface{}{cutoff}, "stale wisps")
		if err != nil {
			return nil, err
		}

		moleculeStepsClosed += stepsThisPass
		totalReaped += staleThisPass
		// Stalls are the CURRENT disagreement, not a running total: a stalled row
		// is left open, so it is re-selected and re-declined on every later pass.
		// Summing would multiply one stuck row by the pass count.
		stepsStalled, staleStalled = stepsStalledThisPass, staleStalledThisPass

		if stepsThisPass+staleThisPass == 0 {
			converged = true
			break
		}
	}
	if !converged {
		result.Passes = maxReapPasses
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type: "reap_did_not_converge",
			Message: fmt.Sprintf(
				"reap still closed wisps on pass %d and stopped at the pass limit — closable wisps may remain open; re-run and investigate the candidate queries if it recurs",
				maxReapPasses),
			Count: maxReapPasses,
		})
	}
	result.MoleculeStepsClosed = moleculeStepsClosed

	// A stalled batch means a candidate query and the UPDATE disagree about what
	// is closable. The loop stops rather than re-selecting the same rows forever,
	// but the disagreement is a bug in the query pair, so surface it.
	if stalled := stepsStalled + staleStalled; stalled > 0 {
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type: "batch_close_no_progress",
			Message: fmt.Sprintf(
				"%d candidate wisp(s) were selected for closing but declined by the batch UPDATE — a candidate query and the UPDATE's exclusion clauses have diverged; those wisps were skipped",
				stalled),
			Count: stalled,
		})
	}

	result.Reaped = totalReaped
	totalClosed := totalReaped + moleculeStepsClosed

	if totalClosed > 0 {
		if err := session.commit(ctx); err != nil {
			return result, fmt.Errorf("sql commit: %w", err)
		}
		commitMsg := fmt.Sprintf("reaper: close %d wisps in %s", totalClosed, dbName)
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
			// "nothing to commit" is expected when the reaper reverts dirty working
			// set changes back to match HEAD. The wisps were set to "open" in the
			// server's in-memory working set without being committed; closing them
			// makes the working set match HEAD again, so DOLT_COMMIT sees no diff.
			if !isNothingToCommit(err) {
				return result, fmt.Errorf("dolt commit: %w", err)
			}
		}
	}

	openQuery := "SELECT COUNT(*) FROM wisps WHERE status IN ('open', 'hooked', 'in_progress')"
	if err := conn.QueryRowContext(ctx, openQuery).Scan(&result.OpenRemain); err != nil {
		return result, fmt.Errorf("count open: %w", err)
	}

	return result, nil
}

// closeWispsInBatches closes wisps in LIMIT-sized chunks, re-running idQuery
// until it stops yielding work. It returns the number of wisps closed and the
// size of the batch that made no progress (0 on every healthy run).
//
// TERMINATION: the loop ends when a batch comes back empty OR when a non-empty
// batch closes nothing. The second condition is load-bearing. The UPDATE below
// carries its own `issue_type != 'agent'` guard, so it can decline rows the
// candidate query offered — and nothing else in the loop changes those rows, so
// the next iteration would select exactly the same batch and decline it again,
// forever. Under Reap's 2-minute context that is a hot SELECT+UPDATE loop
// against production Dolt followed by a timeout error (gt-m46).
//
// Today every candidate query filters agent wisps itself, so no row is ever
// declined and the loop empties the candidate set normally. That coupling is
// invisible from either site, and de-duplicating the guard out of a candidate
// query ("the UPDATE already filters agents") is a plausible future edit.
// Stopping on lack of progress makes termination independent of it, and covers
// any UPDATE clause added later that can decline a selected row. Skipping the
// declined rows under-reaps; the caller reports that as an anomaly.
func closeWispsInBatches(ctx context.Context, runner sqlRunner, idQuery string, queryArgs []interface{}, description string) (closed, stalled int, err error) {
	total := 0
	for {
		rows, err := runner.QueryContext(ctx, idQuery, queryArgs...)
		if err != nil {
			return total, 0, fmt.Errorf("select %s batch: %w", description, err)
		}

		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return total, 0, fmt.Errorf("scan %s id: %w", description, err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return total, 0, fmt.Errorf("read %s ids: %w", description, err)
		}
		rows.Close()

		if len(ids) == 0 {
			return total, 0, nil
		}

		placeholders := make([]string, len(ids))
		args := make([]interface{}, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		inClause := strings.Join(placeholders, ",")

		// The `issue_type != 'agent'` guard is defense in depth: the candidate
		// queries filter agent wisps too. Removing it from either side is not a
		// safe de-duplication — see TERMINATION above.
		updateQuery := fmt.Sprintf(
			"UPDATE wisps SET status='closed', closed_at=NOW() WHERE id IN (%s) AND status IN ('open', 'hooked', 'in_progress') AND issue_type != 'agent'",
			inClause)
		sqlResult, err := runner.ExecContext(ctx, updateQuery, args...)
		if err != nil {
			return total, 0, fmt.Errorf("close %s batch: %w", description, err)
		}

		affected, _ := sqlResult.RowsAffected()
		if affected == 0 {
			// No progress on a non-empty batch. Re-running idQuery would return
			// the same rows, so stop and report them instead of spinning.
			return total, len(ids), nil
		}
		total += int(affected)
	}
}

// PurgeOption configures an optional purge behaviour.
//
// Options rather than more positional parameters because the default must stay
// exactly what it is today: with no options, purge protects by type absolutely
// and deletes nothing it did not already delete. Every existing caller and test
// keeps that contract without an edit, so "protection weakened" can only ever be
// something a caller asked for explicitly.
type PurgeOption func(*purgeOptions)

type purgeOptions struct {
	archive Archiver
}

// WithArchive lets purge export protected wisps to a durable store and then
// delete their rows (gt-6xwt).
//
// Without it, ProtectedWispLabels means "never deleted", and the rows
// accumulate for as long as the town runs. With it, protection means "never
// deleted without a durable record first": a row is released only after
// ArchiveWisps has returned nil for it, and any archive failure leaves it
// exactly as protected as it was.
//
// A nil Archiver is the same as not passing the option, so callers can resolve
// an archive that may be unavailable without branching.
func WithArchive(archive Archiver) PurgeOption {
	return func(o *purgeOptions) { o.archive = archive }
}

// Purge deletes old closed wisps and mail from a database.
func Purge(db *sql.DB, dbName string, purgeAge, mailDeleteAge time.Duration, dryRun bool, opts ...PurgeOption) (*PurgeResult, error) {
	options := purgeOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	result := &PurgeResult{Database: dbName, DryRun: dryRun}

	// Purge closed wisps.
	purged, archived, protected, anomalies, err := purgeClosedWisps(db, dbName, purgeAge, dryRun, options.archive)
	if err != nil {
		return nil, fmt.Errorf("purge wisps: %w", err)
	}
	result.WispsPurged = purged
	result.WispsArchived = archived
	result.WispsProtected = protected
	result.Anomalies = append(result.Anomalies, anomalies...)

	// Purge old mail. Its anomalies are appended before the error check: a
	// failed DOLT_COMMIT is recorded, not returned, so dropping them on the
	// error path would put the mail half back where it started.
	mailPurged, mailAnomalies, err := purgeOldMail(db, dbName, mailDeleteAge, dryRun)
	result.Anomalies = append(result.Anomalies, mailAnomalies...)
	if err != nil {
		return result, fmt.Errorf("purge mail: %w", err)
	}
	result.MailPurged = mailPurged

	return result, nil
}

// purgeClosedWisps deletes closed wisps past purgeAge and, when an archive is
// configured, exports the label-protected ones before deleting those too.
//
// Returns (purged, archived, protected, anomalies, error). The three counts
// partition the closed-past-cutoff window; see PurgeResult.
func purgeClosedWisps(db *sql.DB, dbName string, purgeAge time.Duration, dryRun bool, archive Archiver) (int, int, int, []Anomaly, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	deleteCutoff := time.Now().UTC().Add(-purgeAge)
	var anomalies []Anomaly
	protectWhere := purgeProtectWhere()

	// Count what protection holds back, before anything is deleted. Taken from
	// the same status+age window as the digest, so the pair partitions the
	// window: every closed wisp past the cutoff is either purged or reported
	// protected, and neither number can quietly absorb the other.
	var protectedTotal int
	protectedQuery := "SELECT COUNT(*) FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? AND NOT (" + protectWhere + ")"
	if err := db.QueryRowContext(ctx, protectedQuery, deleteCutoff).Scan(&protectedTotal); err != nil {
		return 0, 0, 0, nil, fmt.Errorf("count protected wisps: %w", err)
	}

	// Retention (gt-6xwt): export the label-protected rows, then release them.
	// Runs BEFORE the ordinary purge so a failure here cannot be mistaken for a
	// smaller purge — whatever it does not release stays counted as protected.
	archived := 0
	if archive != nil {
		var archiveAnomalies []Anomaly
		var err error
		archived, archiveAnomalies, err = archiveProtectedWisps(ctx, db, dbName, deleteCutoff, dryRun, archive)
		anomalies = append(anomalies, archiveAnomalies...)
		if err != nil {
			// Not fatal to the whole purge: the unprotected half below is
			// independent of the archive and there is no reason to skip it
			// because the retention path had a bad day.
			anomalies = append(anomalies, Anomaly{
				Type:    "wisp_archive_failed",
				Message: fmt.Sprintf("archive of protected wisps failed, rows left protected: %v", err),
			})
		}
	}
	// Whatever was released is no longer being held back. Subtracting rather
	// than re-counting keeps the partition exact when only some rows made it:
	// an archive that exported 4 of 7 reports 4 archived and 3 protected.
	protectedTotal -= archived

	// Digest: count by wisp_type.
	// No parent check — closed unprotected wisps past the delete age are purgeable.
	// The parent check (correlated subqueries on wisp_dependencies) was causing O(n*m)
	// query cost with 1800+ closed wisps, leading to CPU spikes and timeouts (gt-wvd2).
	digestQuery := "SELECT COALESCE(w.wisp_type, 'unknown') AS wtype, COUNT(*) AS cnt FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? AND " + protectWhere + " GROUP BY wtype"
	rows, err := db.QueryContext(ctx, digestQuery, deleteCutoff)
	if err != nil {
		return 0, archived, protectedTotal, nil, fmt.Errorf("digest query: %w", err)
	}
	digestTotal := 0
	for rows.Next() {
		var wtype string
		var cnt int
		if err := rows.Scan(&wtype, &cnt); err != nil {
			rows.Close()
			return 0, archived, protectedTotal, nil, fmt.Errorf("digest scan: %w", err)
		}
		digestTotal += cnt
	}
	rows.Close()

	if digestTotal == 0 {
		return 0, archived, protectedTotal, anomalies, nil
	}

	if dryRun {
		return digestTotal, archived, protectedTotal, anomalies, nil
	}

	session, err := beginWriteSession(ctx, db)
	if err != nil {
		return 0, archived, protectedTotal, nil, err
	}
	defer session.release()

	// Batch delete — status+age filter plus the protection, no parent check needed.
	// The protection MUST be in this query and not applied after selection: see
	// purgeProtectWhere on why filtering at DELETE time would not terminate.
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? AND %s LIMIT %d",
		protectWhere, DefaultBatchSize)
	auxTables := []string{"wisp_labels", "wisp_comments", "wisp_events", "wisp_dependencies"}

	totalDeleted, err := batchDeleteRows(ctx, session.conn, idQuery, deleteCutoff, "wisps", auxTables)
	if err != nil {
		// release rolls the batch back, so nothing was purged.
		return 0, archived, protectedTotal, anomalies, err
	}

	if totalDeleted > 0 {
		if err := session.commit(ctx); err != nil {
			anomalies = append(anomalies, Anomaly{
				Type:    "sql_commit_failed",
				Message: fmt.Sprintf("sql commit after purge failed, deletes rolled back: %v", err),
			})
			return 0, archived, protectedTotal, anomalies, nil
		}
		commitMsg := fmt.Sprintf("reaper: purge %d closed wisps from %s", totalDeleted, dbName)
		if _, err := session.conn.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('--allow-empty', '-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
			// Non-fatal — log but continue.
			anomalies = append(anomalies, Anomaly{
				Type:    "dolt_commit_failed",
				Message: fmt.Sprintf("dolt commit after purge failed: %v", err),
			})
		}
	}

	return totalDeleted, archived, protectedTotal, anomalies, nil
}

// wispArchiveAuxTables are the tables whose rows go with a wisp when it is
// deleted. Identical to the purge path's list, because the retention path
// deletes exactly what purge deletes — the difference is only that these rows
// were written out first.
var wispArchiveAuxTables = []string{"wisp_labels", "wisp_comments", "wisp_events", "wisp_dependencies"}

// archiveProtectedWisps exports closed, label-protected, unpinned wisps past the
// cutoff to the archive and then deletes them.
//
// ORDER IS THE WHOLE SAFETY PROPERTY: every batch is written and fsynced by
// ArchiveWisps before its DELETE is issued, and the DELETE names exactly the ids
// that were archived. A crash, a failed write or a failed commit therefore
// leaves rows in the database — possibly with a duplicate record already in the
// archive — and never the other way round. Duplicates are cheap to live with
// (each record carries its id, and the reader can fold them); a deletion with no
// record is the thing that cost seven MR beads and one rejection rationale on
// 2026-08-17 (gt-6dp), and it is unrecoverable because wisps are unversioned and
// unbacked.
//
// The whole run shares one write session, so an error rolls back every delete
// this call made and it reports zero released. That is why the caller can
// subtract the returned count from the protected total and always get the truth:
// partial success is not a state this can end in.
func archiveProtectedWisps(ctx context.Context, db *sql.DB, dbName string, deleteCutoff time.Time, dryRun bool, archive Archiver) (int, []Anomaly, error) {
	var anomalies []Anomaly
	archivableWhere := archivableProtectWhere()

	if dryRun {
		var candidates int
		countQuery := "SELECT COUNT(*) FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? AND " + archivableWhere
		if err := db.QueryRowContext(ctx, countQuery, deleteCutoff).Scan(&candidates); err != nil {
			return 0, nil, fmt.Errorf("count archivable wisps: %w", err)
		}
		return candidates, nil, nil
	}

	session, err := beginWriteSession(ctx, db)
	if err != nil {
		return 0, nil, err
	}
	defer session.release()

	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? AND %s ORDER BY w.id LIMIT %d",
		archivableWhere, DefaultBatchSize)

	archivedAt := time.Now().UTC()
	total := 0
	for {
		// Selected on the session's own connection: the deletes below are
		// uncommitted, so only this session sees them gone. Any other
		// connection would keep returning the same batch forever.
		ids, err := queryIDs(ctx, session.conn, idQuery, deleteCutoff)
		if err != nil {
			return 0, anomalies, err
		}
		if len(ids) == 0 {
			break
		}

		records, err := collectArchivableWisps(ctx, session.conn, dbName, ids, archivedAt)
		if err != nil {
			return 0, anomalies, err
		}
		if len(records) != len(ids) {
			// A row vanished between the id query and the record query, or the
			// record query silently returned fewer rows. Either way the set to
			// archive and the set to delete no longer match, and deleting the
			// larger one would delete something unrecorded.
			return 0, anomalies, fmt.Errorf("archive batch mismatch: %d ids, %d records", len(ids), len(records))
		}

		if err := archive.ArchiveWisps(records); err != nil {
			return 0, anomalies, fmt.Errorf("write archive: %w", err)
		}

		deleted, err := deleteRowsByID(ctx, session.conn, ids, "wisps", wispArchiveAuxTables)
		if err != nil {
			return 0, anomalies, err
		}
		if deleted == 0 {
			// The batch was non-empty and nothing was deleted, so the next
			// iteration would select the same ids and archive them again.
			// Stop and say so rather than spin (the same reasoning as
			// closeWispsInBatches' no-progress guard).
			anomalies = append(anomalies, Anomaly{
				Type:    "wisp_archive_stalled",
				Message: fmt.Sprintf("archive selected %d wisps in %s but deleted none; stopping", len(ids), dbName),
				Count:   len(ids),
			})
			return 0, anomalies, nil
		}
		total += deleted
	}

	if total == 0 {
		return 0, anomalies, nil
	}

	if err := session.commit(ctx); err != nil {
		// release rolls the deletes back. The archive keeps the records it
		// already wrote, so the next run re-exports and re-deletes them.
		anomalies = append(anomalies, Anomaly{
			Type:    "sql_commit_failed",
			Message: fmt.Sprintf("sql commit after wisp archive failed, deletes rolled back: %v", err),
		})
		return 0, anomalies, nil
	}

	commitMsg := fmt.Sprintf("reaper: archive and release %d protected wisps from %s", total, dbName)
	if _, err := session.conn.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('--allow-empty', '-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
		// Non-fatal: the rows are gone from the working set and the records are
		// in the archive. Reported for the same reason the purge path reports
		// it — the deletion is then unversioned (gt-u5c).
		anomalies = append(anomalies, Anomaly{
			Type:    "dolt_commit_failed",
			Message: fmt.Sprintf("dolt commit after wisp archive failed: %v", err),
		})
	}
	return total, anomalies, nil
}

// queryIDs runs an id-selecting query and returns the ids.
func queryIDs(ctx context.Context, runner sqlRunner, query string, args ...interface{}) ([]string, error) {
	rows, err := runner.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("select ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read ids: %w", err)
	}
	return ids, nil
}

// purgeOldMail deletes closed gt:message issues past mailDeleteAge.
//
// It returns anomalies for the same reason purgeClosedWisps does: DOLT_COMMIT
// failure is non-fatal — the rows are gone from the working set and the SQL
// COMMIT landed — but the deletion is then unversioned. Reporting nothing would
// make `gt reaper purge --json` read clean by construction on the mail half,
// and the operator check for dolt_commit_failed anomalies could never fire
// (gt-u5c).
func purgeOldMail(db *sql.DB, dbName string, mailDeleteAge time.Duration, dryRun bool) (int, []Anomaly, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	mailCutoff := time.Now().UTC().Add(-mailDeleteAge)
	var anomalies []Anomaly

	countQuery := fmt.Sprintf(
		"SELECT COUNT(*) FROM `%s`.issues WHERE status = 'closed' AND closed_at < ? AND id IN (SELECT issue_id FROM `%s`.labels WHERE label = 'gt:message')",
		dbName, dbName)
	var count int
	if err := db.QueryRowContext(ctx, countQuery, mailCutoff).Scan(&count); err != nil {
		if isTableNotFound(err) {
			return 0, nil, nil // issues/labels not on this server
		}
		return 0, nil, fmt.Errorf("count mail: %w", err)
	}
	if count == 0 {
		return 0, anomalies, nil
	}

	if dryRun {
		return count, anomalies, nil
	}

	session, err := beginWriteSession(ctx, db)
	if err != nil {
		return 0, nil, err
	}
	defer session.release()

	idQuery := fmt.Sprintf(
		"SELECT i.id FROM `%s`.issues i INNER JOIN `%s`.labels l ON i.id = l.issue_id WHERE i.status = 'closed' AND i.closed_at < ? AND l.label = 'gt:message' LIMIT %d",
		dbName, dbName, DefaultBatchSize)
	auxTables := []string{"labels", "comments", "events", "dependencies"}

	totalDeleted, err := batchDeleteRows(ctx, session.conn, idQuery, mailCutoff, "issues", auxTables)
	if err != nil {
		// release rolls the batch back, so nothing was purged.
		return 0, anomalies, err
	}

	if totalDeleted > 0 {
		if err := session.commit(ctx); err != nil {
			return 0, anomalies, fmt.Errorf("sql commit: %w", err)
		}
		commitMsg := fmt.Sprintf("reaper: purge %d old mail from %s", totalDeleted, dbName)
		if _, err := session.conn.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('--allow-empty', '-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
			// Non-fatal — the rows are deleted and the SQL commit landed, but
			// the deletion is unversioned. Record it so the operator check can
			// see it; see purgeClosedWisps for the mirror-image call site.
			anomalies = append(anomalies, Anomaly{
				Type:    "dolt_commit_failed",
				Message: fmt.Sprintf("dolt commit after mail purge failed: %v", err),
			})
		}
	}

	return totalDeleted, anomalies, nil
}

// AutoClose closes issues that have been open with no updates past staleAge.
// Excludes P0/P1 priority, epics, hooked/pinned issues, standing-order labels,
// and issues with active dependencies.
func AutoClose(db *sql.DB, dbName string, staleAge time.Duration, dryRun bool) (*AutoCloseResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	staleCutoff := time.Now().UTC().Add(-staleAge)
	result := &AutoCloseResult{Database: dbName, DryRun: dryRun}

	// Convoys are excluded from staleness auto-close (hq-jnap): their lifecycle
	// is driven by tracked-bead status (`gt convoy check` / refinery post-merge),
	// and the 'tracks' relation is non-blocking so the dependency exclusions
	// below do NOT protect a convoy with open tracked issues. Stale-closing a
	// convoy while its tracked beads are open orphans them from dispatch
	// tracking and causes duplicate dispatches (hq-qouv/hq-shb1 incident).
	whereClause := fmt.Sprintf(`
		i.status IN ('open', 'in_progress')
		AND i.updated_at < ?
		AND i.priority > 1
		AND i.issue_type NOT IN ('epic', 'convoy')
		AND i.id NOT IN (
			-- The list itself is AutoCloseExemptLabels, which documents why each
			-- entry is there. It is rendered rather than inlined because Scan
			-- needs the identical set: the two copies drifted once already and
			-- Scan over-reported what AutoClose would close (gt-jbn).
			SELECT DISTINCT l.issue_id FROM `+"`%s`"+`.labels l
			WHERE l.label IN (%s)
		)
		AND i.id NOT IN (
			SELECT DISTINCT d.issue_id FROM `+"`%s`"+`.dependencies d
			INNER JOIN `+"`%s`"+`.issues dep ON d.depends_on_issue_id = dep.id
			WHERE dep.status IN ('open', 'in_progress')
		)
		AND i.id NOT IN (
			SELECT DISTINCT d.depends_on_issue_id FROM `+"`%s`"+`.dependencies d
			INNER JOIN `+"`%s`"+`.issues blocker ON d.issue_id = blocker.id
			WHERE d.depends_on_issue_id IS NOT NULL
			AND blocker.status IN ('open', 'in_progress')
		)`, dbName, sqlLabelList(AutoCloseExemptLabels), dbName, dbName, dbName, dbName)

	// Two-step SELECT-then-UPDATE to avoid self-referencing subquery in UPDATE,
	// which is not valid MySQL (Error 1093) and fragile in Dolt (dolthub/dolt#10600).
	selectQuery := fmt.Sprintf("SELECT i.id, i.title, i.updated_at FROM issues i WHERE %s", whereClause)
	rows, err := db.QueryContext(ctx, selectQuery, staleCutoff)
	if err != nil {
		if isTableNotFound(err) {
			return result, nil // issues/dependencies not on this server
		}
		return nil, fmt.Errorf("select stale: %w", err)
	}
	type candidate struct {
		id        string
		title     string
		updatedAt time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.title, &c.updatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan stale id: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()

	// Build per-issue closure log entries.
	now := time.Now().UTC()
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
		result.ClosedEntries = append(result.ClosedEntries, ClosedEntry{
			ID:       c.id,
			Title:    c.title,
			AgeDays:  int(now.Sub(c.updatedAt).Hours() / 24),
			Database: dbName,
		})
	}

	if dryRun {
		result.Closed = len(ids)
		return result, nil
	}

	if len(ids) == 0 {
		return result, nil
	}

	session, err := beginWriteSession(ctx, db)
	if err != nil {
		return nil, err
	}
	defer session.release()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	updateQuery := fmt.Sprintf(
		"UPDATE `%s`.issues SET status = 'closed', closed_at = NOW(), close_reason = 'stale:auto-closed by reaper' WHERE id IN (%s)",
		dbName, strings.Join(placeholders, ","))
	if _, err := session.conn.ExecContext(ctx, updateQuery, args...); err != nil {
		return nil, fmt.Errorf("auto-close: %w", err)
	}

	if err := session.commit(ctx); err != nil {
		// release rolls the UPDATE back, so nothing was closed.
		result.ClosedEntries = nil
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type:    "sql_commit_failed",
			Message: fmt.Sprintf("sql commit after auto-close failed, closures rolled back: %v", err),
		})
		return result, nil
	}
	result.Closed = len(ids)

	commitMsg := fmt.Sprintf("reaper: auto-close %d stale issues in %s", len(ids), dbName)
	if _, err := session.conn.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
		// "nothing to commit" is expected when the updated tables are dolt_ignored.
		if !isNothingToCommit(err) {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type:    "dolt_commit_failed",
				Message: fmt.Sprintf("dolt commit after auto-close failed: %v", err),
			})
		}
	}

	return result, nil
}

// batchDeleteRows deletes rows from a primary table and its auxiliary tables in
// batches. It takes a sqlRunner rather than a *sql.DB so callers can hand it the
// connection they pinned for the write sequence — every batch must run on the
// session that disabled autocommit, or the deletes commit one at a time and the
// caller's flushing COMMIT has nothing to flush (gt-gjh).
func batchDeleteRows(ctx context.Context, db sqlRunner, idQuery string, cutoffArg time.Time, primaryTable string, auxTables []string) (int, error) {
	totalDeleted := 0
	for {
		idRows, err := db.QueryContext(ctx, idQuery, cutoffArg)
		if err != nil {
			return totalDeleted, fmt.Errorf("select batch: %w", err)
		}

		var ids []string
		for idRows.Next() {
			var id string
			if err := idRows.Scan(&id); err != nil {
				idRows.Close()
				return totalDeleted, fmt.Errorf("scan id: %w", err)
			}
			ids = append(ids, id)
		}
		idRows.Close()

		if len(ids) == 0 {
			break
		}

		affected, err := deleteRowsByID(ctx, db, ids, primaryTable, auxTables)
		if err != nil {
			return totalDeleted, err
		}
		totalDeleted += affected
	}

	return totalDeleted, nil
}

// deleteRowsByID deletes one explicit set of ids from a primary table, its
// auxiliary tables, and any typed reverse references to it.
//
// Split out of batchDeleteRows so the retention path can delete the exact rows
// it has already archived (see archiveProtectedWisps) rather than re-deriving
// them from a query. Re-deriving is what the id-query loop does, and it is the
// wrong shape there: the rows to delete must be precisely the rows the archive
// accepted, not whatever the predicate matches a moment later.
func deleteRowsByID(ctx context.Context, db sqlRunner, ids []string, primaryTable string, auxTables []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := "(" + strings.Join(placeholders, ",") + ")"

	for _, tbl := range auxTables {
		delAux := fmt.Sprintf("DELETE FROM `%s` WHERE issue_id IN %s", tbl, inClause) //nolint:gosec // G201: tbl is internal
		if _, err := db.ExecContext(ctx, delAux, args...); err != nil {
			// Non-fatal: log and continue.
			_ = err
		}
	}

	// Clean up typed reverse dependency references to prevent dangling parent refs.
	var reverseDeletes []string
	switch primaryTable {
	case "wisps":
		reverseDeletes = []string{
			fmt.Sprintf("DELETE FROM wisp_dependencies WHERE depends_on_wisp_id IN %s", inClause),
			fmt.Sprintf("DELETE FROM dependencies WHERE depends_on_wisp_id IN %s", inClause),
		}
	case "issues":
		reverseDeletes = []string{
			fmt.Sprintf("DELETE FROM wisp_dependencies WHERE depends_on_issue_id IN %s", inClause),
			fmt.Sprintf("DELETE FROM dependencies WHERE depends_on_issue_id IN %s", inClause),
		}
	}
	for _, delReverse := range reverseDeletes {
		if _, err := db.ExecContext(ctx, delReverse, args...); err != nil {
			// Non-fatal.
			_ = err
		}
	}

	delPrimary := fmt.Sprintf("DELETE FROM `%s` WHERE id IN %s", primaryTable, inClause) //nolint:gosec // G201: primaryTable is internal
	sqlResult, err := db.ExecContext(ctx, delPrimary, args...)
	if err != nil {
		return 0, fmt.Errorf("delete %s batch: %w", primaryTable, err)
	}
	affected, _ := sqlResult.RowsAffected()
	return int(affected), nil
}

// ClosePluginReceiptResult holds the results of closing plugin run receipts.
type ClosePluginReceiptResult struct {
	Database  string    `json:"database"`
	Closed    int       `json:"closed"`
	DryRun    bool      `json:"dry_run,omitempty"`
	Anomalies []Anomaly `json:"anomalies,omitempty"`
}

// ClosePluginReceipts closes open issues labeled "type:plugin-run" that are
// older than maxAge. These are transient run receipts created by deacon dog
// plugins; they should be closed shortly after creation since they exist only
// for audit/cooldown-gate purposes. The standard AutoClose path requires 7 days
// of staleness, which lets plugin receipts accumulate into the hundreds.
func ClosePluginReceipts(db *sql.DB, dbName string, maxAge time.Duration, dryRun bool) (*ClosePluginReceiptResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	cutoff := time.Now().UTC().Add(-maxAge)
	result := &ClosePluginReceiptResult{Database: dbName, DryRun: dryRun}

	// Find open issues with the "type:plugin-run" label older than maxAge.
	selectQuery := fmt.Sprintf(`
		SELECT i.id FROM `+"`%s`"+`.issues i
		INNER JOIN `+"`%s`"+`.labels l ON i.id = l.issue_id
		WHERE i.status IN ('open', 'in_progress')
		AND l.label = 'type:plugin-run'
		AND i.created_at < ?`, dbName, dbName)

	rows, err := db.QueryContext(ctx, selectQuery, cutoff)
	if err != nil {
		if isTableNotFound(err) {
			return result, nil
		}
		return nil, fmt.Errorf("select plugin receipts: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan plugin receipt id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	result.Closed = len(ids)
	if len(ids) == 0 || dryRun {
		return result, nil
	}

	session, err := beginWriteSession(ctx, db)
	if err != nil {
		return nil, err
	}
	defer session.release()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	updateQuery := fmt.Sprintf(
		"UPDATE `%s`.issues SET status = 'closed', closed_at = NOW() WHERE id IN (%s)",
		dbName, strings.Join(placeholders, ","))
	if _, err := session.conn.ExecContext(ctx, updateQuery, args...); err != nil {
		return nil, fmt.Errorf("close plugin receipts: %w", err)
	}

	// Flush and commit.
	if err := session.commit(ctx); err != nil {
		// release rolls the UPDATE back, so nothing was closed.
		result.Closed = 0
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type:    "sql_commit_failed",
			Message: fmt.Sprintf("sql commit after plugin receipt close failed, closures rolled back: %v", err),
		})
		return result, nil
	}
	commitMsg := fmt.Sprintf("reaper: close %d plugin receipts in %s", len(ids), dbName)
	if _, err := session.conn.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
		if !isNothingToCommit(err) {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type:    "dolt_commit_failed",
				Message: fmt.Sprintf("dolt commit after plugin receipt close failed: %v", err),
			})
		}
	}

	return result, nil
}

// ClosePluginDispatches closes open dispatch mail beads created by the daemon
// when sending plugin instructions to dogs. These beads are labeled "gt:message"
// + "from:daemon" with a title prefix "Plugin:" and are never closed after the
// dog completes. Without this, they accumulate at ~288/day (one per 5-minute
// stuck-agent-dog run) and are only caught by AutoClose after 7 days.
func ClosePluginDispatches(db *sql.DB, dbName string, maxAge time.Duration, dryRun bool) (*ClosePluginReceiptResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	cutoff := time.Now().UTC().Add(-maxAge)
	result := &ClosePluginReceiptResult{Database: dbName, DryRun: dryRun}

	// Find open issues with both "gt:message" and "from:daemon" labels whose
	// title starts with "Plugin:", older than maxAge.
	selectQuery := fmt.Sprintf(`
		SELECT i.id FROM `+"`%s`"+`.issues i
		INNER JOIN `+"`%s`"+`.labels l1 ON i.id = l1.issue_id
		INNER JOIN `+"`%s`"+`.labels l2 ON i.id = l2.issue_id
		WHERE i.status IN ('open', 'in_progress')
		AND l1.label = 'gt:message'
		AND l2.label = 'from:daemon'
		AND i.title LIKE 'Plugin:%%'
		AND i.created_at < ?`, dbName, dbName, dbName)

	rows, err := db.QueryContext(ctx, selectQuery, cutoff)
	if err != nil {
		if isTableNotFound(err) {
			return result, nil
		}
		return nil, fmt.Errorf("select plugin dispatches: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan plugin dispatch id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	result.Closed = len(ids)
	if len(ids) == 0 || dryRun {
		return result, nil
	}

	session, err := beginWriteSession(ctx, db)
	if err != nil {
		return nil, err
	}
	defer session.release()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	updateQuery := fmt.Sprintf(
		"UPDATE `%s`.issues SET status = 'closed', closed_at = NOW() WHERE id IN (%s)",
		dbName, strings.Join(placeholders, ","))
	if _, err := session.conn.ExecContext(ctx, updateQuery, args...); err != nil {
		return nil, fmt.Errorf("close plugin dispatches: %w", err)
	}

	// Flush and commit.
	if err := session.commit(ctx); err != nil {
		// release rolls the UPDATE back, so nothing was closed.
		result.Closed = 0
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type:    "sql_commit_failed",
			Message: fmt.Sprintf("sql commit after plugin dispatch close failed, closures rolled back: %v", err),
		})
		return result, nil
	}
	commitMsg := fmt.Sprintf("reaper: close %d plugin dispatches in %s", len(ids), dbName)
	if _, err := session.conn.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
		if !isNothingToCommit(err) {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type:    "dolt_commit_failed",
				Message: fmt.Sprintf("dolt commit after plugin dispatch close failed: %v", err),
			})
		}
	}

	return result, nil
}

// FormatJSON marshals any value to indented JSON.
func FormatJSON(v interface{}) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(data)
}
