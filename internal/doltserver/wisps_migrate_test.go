//go:build integration

package doltserver

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/gastown/internal/testutil"
)

// requireBd skips before a container is started if the bd CLI is missing.
//
// Before the helper, not after: starting a Dolt container and then skipping
// costs a pull and a start for nothing.
func requireBd(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not found in PATH — skipping integration test")
	}
}

// setupBdWorkDir creates a beads-compatible working directory pointing at an
// isolated Dolt server, lets bd build its own schema there, and seeds one
// issue. It returns the working directory.
//
// The schema is bd's, not this fixture's. An earlier version hand-rolled a
// five-column issues table, which bd's migration chain then refused to run
// over — first because the table was dirty in the working set, then because
// migration 0017 expects columns the hand-rolled table does not have. Creating
// an empty database and letting bd migrate into it gets the real schema and
// cannot drift from it.
func setupBdWorkDir(t *testing.T, port int) string {
	t.Helper()

	// bd refuses to open a database whose name ends in _test unless the server
	// is declared a dedicated test server. The container this fixture is handed
	// is one: it was started for this test alone and is terminated when the
	// test finishes. Scoped to the test rather than set by the container helper
	// — the helper does not choose the database name, this fixture does.
	t.Setenv("BEADS_TEST_SERVER", "1")

	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	// 0700, not 0755: bd warns about a group/world-readable .beads on every
	// invocation, and the warning has no business in this fixture's output.
	// It used to matter more than that — bdSQLCSV read CombinedOutput, so the
	// warning landed in the CSV bdSQLCount parses and a healthy server reported
	// a count of zero (gt-g12p). That is fixed in bdSQLCSV and pinned by
	// TestBdSQLCSV_StderrStaysOutOfCSV; the mode here is now hygiene, not a
	// workaround.
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("creating .beads dir: %v", err)
	}

	// dolt_database, not database: beads.DatabaseNameFromMetadata reads that
	// field and nothing else, so the wrong key leaves bd resolving its default
	// "beads" and reporting "database not found" against a server that has
	// beads_test sitting right there.
	metadata := fmt.Sprintf(`{"backend":"dolt","dolt_database":"beads_test","dolt_mode":"server","dolt_server_host":"127.0.0.1","dolt_server_port":%d}`, port)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0600); err != nil {
		t.Fatalf("writing metadata.json: %v", err)
	}

	// Create the database empty and let bd migrate its schema into it.
	server, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/", port))
	if err != nil {
		t.Fatalf("connecting to isolated server: %v", err)
	}
	defer server.Close()
	if _, err := server.Exec("CREATE DATABASE IF NOT EXISTS beads_test"); err != nil {
		t.Fatalf("creating test database: %v", err)
	}

	// The first bd command runs the schema migration. Failing here is a real
	// failure: the container is up and this fixture has just named it.
	if err := bdSQL(workDir, "SELECT 1"); err != nil {
		t.Fatalf("bd cannot reach the isolated server on port %d: %v", port, err)
	}

	// issue_prefix lives in the Dolt config table; bd reads it from there, and
	// an issue-prefix key in .beads/config.yaml does not satisfy it. Without
	// this, bd create fails with "database not initialized".
	if err := bdSQL(workDir, "INSERT INTO config (`key`, value) VALUES ('issue_prefix', 'bt')"); err != nil {
		t.Fatalf("seeding issue_prefix: %v", err)
	}

	// Seed through bd rather than INSERT: the real issues table has NOT NULL
	// columns with no defaults, and naming them here is exactly the drift the
	// hand-rolled schema already caused once.
	if err := bdExec(workDir, "create", "--title", "fixture issue", "-p", "2"); err != nil {
		t.Fatalf("seeding an issue: %v", err)
	}

	return workDir
}

// TestMigrateWisps_TableCreation verifies that bd can operate on the wisps
// table of an isolated server: the table is part of bd's schema, and
// `bd mol wisp list` works against it.
func TestMigrateWisps_TableCreation(t *testing.T) {
	requireBd(t)
	portStr := testutil.StartIsolatedDoltContainer(t)

	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("container port %q is not a number: %v", portStr, err)
	}
	workDir := setupBdWorkDir(t, port)

	if !bdTableExists(workDir, "issues") {
		t.Fatal("issues table missing after bd initialised the schema")
	}
	if !bdTableExists(workDir, "wisps") {
		t.Fatal("wisps table missing after bd initialised the schema")
	}

	cmd := exec.Command("bd", "mol", "wisp", "list")
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd mol wisp list failed: %s: %v", string(output), err)
	}
}

// TestBdSQLCount verifies the count helper returns the real row count.
//
// The count asserted is 1, not >= 0: bdSQLCount returns 0 for an unparseable
// result as well as for an empty table, so a zero-or-more assertion passes
// whether or not anything worked. The fixture seeds exactly one issue.
func TestBdSQLCount(t *testing.T) {
	requireBd(t)
	portStr := testutil.StartIsolatedDoltContainer(t)

	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("container port %q is not a number: %v", portStr, err)
	}
	workDir := setupBdWorkDir(t, port)

	cnt, err := bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM issues")
	if err != nil {
		t.Fatalf("bdSQLCount: %v", err)
	}
	if cnt != 1 {
		t.Errorf("bdSQLCount(issues) = %d, want 1", cnt)
	}
}
