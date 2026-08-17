package reaper

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/server"
	gmssql "github.com/dolthub/go-mysql-server/sql"
	gmstypes "github.com/dolthub/go-mysql-server/sql/types"
	"github.com/sirupsen/logrus"
)

// This file provides a REAL SQL fixture for the reaper: an in-process
// MySQL-protocol server (go-mysql-server, the engine Dolt itself is built on)
// loaded with the beads schema, reached through the same go-sql-driver/mysql
// client the production code uses.
//
// Why a real engine rather than a fake driver: the reaper's behaviour lives
// entirely in its SQL text. A fake driver that re-implements the WHERE clause
// in Go cannot fail when the SQL stops matching that logic — it would be
// another test that agrees with itself. Only an engine that actually evaluates
// the query can tell a working guard from an inert one (gt-am7, gt-caq).

// doltCommitLog records every CALL DOLT_COMMIT(...) the reaper issues, so
// tests can assert the commit actually happened.
type doltCommitLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *doltCommitLog) record(args []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, strings.Join(args, " "))
}

// matching returns the recorded commit messages containing substr. Tests pass
// their own database name, which the reaper puts in every commit message, so a
// shared log still attributes commits to the right fixture.
func (l *doltCommitLog) matching(substr string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, call := range l.calls {
		if strings.Contains(call, substr) {
			out = append(out, call)
		}
	}
	return out
}

var (
	doltCommits          = &doltCommitLog{}
	registerDoltCommitOn sync.Once
	quietServerOn        sync.Once
)

// quietServer drops the fixture engine's startup chatter. Each fixture logs a
// banner and a per-connection line; with a fixture per test that buries the
// assertion output of whichever test just failed.
func quietServer() {
	quietServerOn.Do(func() { logrus.SetLevel(logrus.ErrorLevel) })
}

// registerDoltCommitStub installs a DOLT_COMMIT stored procedure on the fixture
// engine. Dolt supplies this procedure in production; go-mysql-server does not,
// and without it every reaper commit path would report a dolt_commit_failed
// anomaly and mask real failures. Registration is process-wide and must happen
// once — the memory provider builds its procedure registry from this global.
func registerDoltCommitStub() {
	registerDoltCommitOn.Do(func() {
		memory.ExternalStoredProcedures = append(memory.ExternalStoredProcedures,
			gmssql.ExternalStoredProcedureDetails{
				Name:   "dolt_commit",
				Schema: gmssql.Schema{&gmssql.Column{Name: "hash", Type: gmstypes.LongText}},
				Function: func(_ *gmssql.Context, args ...string) (gmssql.RowIter, error) {
					doltCommits.record(args)
					return gmssql.RowsToRowIter(gmssql.Row{"0000000000000000000000000000000000000000"}), nil
				},
			})
	})
}

// fixture is a live beads-shaped database the reaper can be pointed at.
type fixture struct {
	db     *sql.DB
	dbName string
	host   string
	port   int
}

// doltCommitMessages returns the DOLT_COMMIT messages issued against this
// fixture's database.
func (f *fixture) doltCommitMessages() []string {
	return doltCommits.matching(f.dbName)
}

// newFixture starts an in-process SQL server, creates dbName, applies the
// beads schema, and returns a connection opened through reaper.OpenDB — the
// same DSN construction production uses.
func newFixture(t *testing.T, dbName string) *fixture {
	t.Helper()

	quietServer()
	registerDoltCommitStub()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	provider := memory.NewDBProvider()
	engine := sqle.NewDefault(provider)
	srv, err := server.NewServer(
		server.Config{Protocol: "tcp", Listener: listener},
		engine,
		gmssql.NewContext,
		memory.NewSessionBuilder(provider),
		nil,
	)
	if err != nil {
		t.Fatalf("new sql server: %v", err)
	}
	go func() { _ = srv.Start() }()
	t.Cleanup(func() { _ = srv.Close() })

	host, portStr, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	admin, err := sql.Open("mysql", fmt.Sprintf("root@tcp(%s:%d)/?parseTime=true&timeout=5s", host, port))
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()
	if err := waitForServer(admin); err != nil {
		t.Fatalf("sql server never became ready: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE " + dbName); err != nil { //nolint:gosec // G202: test-controlled name
		t.Fatalf("create database %s: %v", dbName, err)
	}

	db, err := OpenDB(host, port, dbName, 30*time.Second, 30*time.Second)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	// The reaper toggles @@autocommit as a session variable but issues the
	// following statements on the pool, so a single connection keeps fixture
	// behaviour deterministic.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	for _, ddl := range beadsFixtureDDL {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("apply fixture schema: %v\n%s", err, ddl)
		}
	}

	return &fixture{db: db, dbName: dbName, host: host, port: port}
}

func waitForServer(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var lastErr error
	for {
		if err := db.PingContext(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return lastErr
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// beadsFixtureDDL mirrors the production beads schema (internal/doltserver
// wispsCreateDDL and the beads issues schema) narrowed to the columns the
// reaper reads or writes. Foreign keys and CHECK constraints are omitted: the
// reaper never relies on cascade behaviour — it deletes auxiliary rows itself,
// and that explicit cleanup is what these tests verify.
var beadsFixtureDDL = []string{
	`CREATE TABLE issues (
		id varchar(255) NOT NULL PRIMARY KEY,
		title varchar(500) NOT NULL DEFAULT '',
		status varchar(32) NOT NULL DEFAULT 'open',
		priority int NOT NULL DEFAULT 2,
		issue_type varchar(32) NOT NULL DEFAULT 'task',
		created_at datetime NOT NULL,
		updated_at datetime NOT NULL,
		closed_at datetime,
		close_reason text
	)`,
	`CREATE TABLE labels (
		issue_id varchar(255) NOT NULL,
		label varchar(255) NOT NULL,
		PRIMARY KEY (issue_id, label)
	)`,
	`CREATE TABLE comments (
		id bigint NOT NULL AUTO_INCREMENT PRIMARY KEY,
		issue_id varchar(255) NOT NULL,
		text text NOT NULL
	)`,
	`CREATE TABLE events (
		id bigint NOT NULL AUTO_INCREMENT PRIMARY KEY,
		issue_id varchar(255) NOT NULL,
		event_type varchar(32) NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE dependencies (
		id varchar(64) NOT NULL PRIMARY KEY,
		issue_id varchar(255) NOT NULL,
		type varchar(32) NOT NULL DEFAULT 'blocks',
		depends_on_issue_id varchar(255),
		depends_on_wisp_id varchar(255),
		depends_on_external varchar(255)
	)`,
	`CREATE TABLE wisps (
		id varchar(255) NOT NULL PRIMARY KEY,
		title varchar(500) NOT NULL DEFAULT '',
		status varchar(32) NOT NULL DEFAULT 'open',
		issue_type varchar(32) NOT NULL DEFAULT 'task',
		wisp_type varchar(32),
		created_at datetime NOT NULL,
		closed_at datetime
	)`,
	`CREATE TABLE wisp_labels (
		issue_id varchar(255) NOT NULL,
		label varchar(255) NOT NULL,
		PRIMARY KEY (issue_id, label)
	)`,
	`CREATE TABLE wisp_comments (
		id bigint NOT NULL AUTO_INCREMENT PRIMARY KEY,
		issue_id varchar(255) NOT NULL,
		text text NOT NULL
	)`,
	`CREATE TABLE wisp_events (
		id bigint NOT NULL AUTO_INCREMENT PRIMARY KEY,
		issue_id varchar(255) NOT NULL,
		event_type varchar(32) NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE wisp_dependencies (
		id varchar(64) NOT NULL PRIMARY KEY,
		issue_id varchar(255) NOT NULL,
		type varchar(32) NOT NULL DEFAULT 'blocks',
		depends_on_issue_id varchar(255),
		depends_on_wisp_id varchar(255),
		depends_on_external varchar(255)
	)`,
}

// --- fixture row builders -------------------------------------------------

type issueRow struct {
	id        string
	title     string
	status    string
	priority  int
	issueType string
	updatedAt time.Time
	closedAt  *time.Time
	labels    []string
}

func (f *fixture) insertIssues(t *testing.T, rows ...issueRow) {
	t.Helper()
	for _, r := range rows {
		if r.status == "" {
			r.status = "open"
		}
		if r.issueType == "" {
			r.issueType = "task"
		}
		if r.title == "" {
			r.title = r.id
		}
		if _, err := f.db.Exec(
			"INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at, closed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			r.id, r.title, r.status, r.priority, r.issueType, r.updatedAt, r.updatedAt, r.closedAt); err != nil {
			t.Fatalf("insert issue %s: %v", r.id, err)
		}
		for _, label := range r.labels {
			if _, err := f.db.Exec("INSERT INTO labels (issue_id, label) VALUES (?, ?)", r.id, label); err != nil {
				t.Fatalf("insert label %s/%s: %v", r.id, label, err)
			}
		}
		if _, err := f.db.Exec("INSERT INTO comments (issue_id, text) VALUES (?, ?)", r.id, "comment"); err != nil {
			t.Fatalf("insert comment for %s: %v", r.id, err)
		}
		if _, err := f.db.Exec("INSERT INTO events (issue_id, event_type) VALUES (?, ?)", r.id, "created"); err != nil {
			t.Fatalf("insert event for %s: %v", r.id, err)
		}
	}
}

func (f *fixture) insertDependency(t *testing.T, id, issueID, dependsOnIssueID, depType string) {
	t.Helper()
	if _, err := f.db.Exec(
		"INSERT INTO dependencies (id, issue_id, type, depends_on_issue_id) VALUES (?, ?, ?, ?)",
		id, issueID, depType, dependsOnIssueID); err != nil {
		t.Fatalf("insert dependency %s: %v", id, err)
	}
}

type wispRow struct {
	id        string
	status    string
	issueType string
	wispType  string
	createdAt time.Time
	closedAt  *time.Time
}

func (f *fixture) insertWisps(t *testing.T, rows ...wispRow) {
	t.Helper()
	for _, r := range rows {
		if r.status == "" {
			r.status = "open"
		}
		if r.issueType == "" {
			r.issueType = "task"
		}
		if _, err := f.db.Exec(
			"INSERT INTO wisps (id, title, status, issue_type, wisp_type, created_at, closed_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			r.id, r.id, r.status, r.issueType, nullString(r.wispType), r.createdAt, r.closedAt); err != nil {
			t.Fatalf("insert wisp %s: %v", r.id, err)
		}
	}
}

// insertWispAux gives a wisp one row in each auxiliary table, so purge's
// auxiliary cleanup has something to remove (or wrongly remove).
func (f *fixture) insertWispAux(t *testing.T, wispID string) {
	t.Helper()
	stmts := []struct {
		query string
		args  []interface{}
	}{
		{"INSERT INTO wisp_labels (issue_id, label) VALUES (?, ?)", []interface{}{wispID, "gt:wisp"}},
		{"INSERT INTO wisp_comments (issue_id, text) VALUES (?, ?)", []interface{}{wispID, "comment"}},
		{"INSERT INTO wisp_events (issue_id, event_type) VALUES (?, ?)", []interface{}{wispID, "created"}},
	}
	for _, stmt := range stmts {
		if _, err := f.db.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatalf("insert aux row for %s: %v", wispID, err)
		}
	}
}

// insertWispDependency records a parent-child (or other) edge. Exactly one of
// dependsOnWispID / dependsOnExternal is expected to be set, matching the
// production one-target constraint.
func (f *fixture) insertWispDependency(t *testing.T, id, issueID, dependsOnWispID, dependsOnExternal, depType string) {
	t.Helper()
	if _, err := f.db.Exec(
		"INSERT INTO wisp_dependencies (id, issue_id, type, depends_on_wisp_id, depends_on_external) VALUES (?, ?, ?, ?, ?)",
		id, issueID, depType, nullString(dependsOnWispID), nullString(dependsOnExternal)); err != nil {
		t.Fatalf("insert wisp dependency %s: %v", id, err)
	}
}

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// --- fixture assertions ---------------------------------------------------

func (f *fixture) issueStatus(t *testing.T, id string) string {
	t.Helper()
	var status string
	if err := f.db.QueryRow("SELECT status FROM issues WHERE id = ?", id).Scan(&status); err != nil {
		t.Fatalf("read status of %s: %v", id, err)
	}
	return status
}

func (f *fixture) issueCloseReason(t *testing.T, id string) string {
	t.Helper()
	var reason sql.NullString
	if err := f.db.QueryRow("SELECT close_reason FROM issues WHERE id = ?", id).Scan(&reason); err != nil {
		t.Fatalf("read close_reason of %s: %v", id, err)
	}
	return reason.String
}

// exec runs a statement against the fixture, failing the test on error. Tests
// use it to damage the schema and observe how the reaper's gates respond.
func (f *fixture) exec(t *testing.T, query string, args ...interface{}) {
	t.Helper()
	if _, err := f.db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// createDatabase adds another database to the fixture's server, so tests can
// observe how database discovery treats it.
func (f *fixture) createDatabase(t *testing.T, name string) {
	t.Helper()
	f.exec(t, "CREATE DATABASE `"+name+"`")
}

func (f *fixture) wispStatus(t *testing.T, id string) string {
	t.Helper()
	var status string
	if err := f.db.QueryRow("SELECT status FROM wisps WHERE id = ?", id).Scan(&status); err != nil {
		t.Fatalf("read status of wisp %s: %v", id, err)
	}
	return status
}

// closedIssueIDs returns the sorted ids of every closed issue, so tests can
// assert on the exact set the sweep touched rather than on a count.
func (f *fixture) closedIssueIDs(t *testing.T) []string {
	t.Helper()
	rows, err := f.db.Query("SELECT id FROM issues WHERE status = 'closed' ORDER BY id")
	if err != nil {
		t.Fatalf("list closed issues: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan closed issue id: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read closed issue ids: %v", err)
	}
	return out
}

// ids returns the sorted ids present in a table, so tests can assert on the
// exact surviving set rather than on counts.
func (f *fixture) ids(t *testing.T, table string) []string {
	t.Helper()
	rows, err := f.db.Query(fmt.Sprintf("SELECT id FROM `%s` ORDER BY id", table)) //nolint:gosec // test-controlled table name
	if err != nil {
		t.Fatalf("list %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan %s id: %v", table, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s ids: %v", table, err)
	}
	return out
}

// issueIDs returns the sorted issue_id values in an auxiliary table.
func (f *fixture) issueIDs(t *testing.T, table string) []string {
	t.Helper()
	rows, err := f.db.Query(fmt.Sprintf("SELECT issue_id FROM `%s` ORDER BY issue_id", table)) //nolint:gosec // test-controlled table name
	if err != nil {
		t.Fatalf("list %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan %s issue_id: %v", table, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s issue_ids: %v", table, err)
	}
	return out
}
