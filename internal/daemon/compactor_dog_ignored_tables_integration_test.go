//go:build integration

package daemon

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/gastown/internal/testutil"
)

// setupCompactorIgnoreTestDB creates a scratch database with a tracked table
// (several commits of real history, like production) and a dolt_ignore'd
// table mirroring wisps/wisp_% — never referenced by any commit, exactly as
// production wisp tables are. Returns the open *sql.DB and a cleanup func.
func setupCompactorIgnoreTestDB(t *testing.T, dbName string) *sql.DB {
	t.Helper()
	testutil.RequireDoltContainer(t)

	admin, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/", testutil.DoltContainerPort()))
	if err != nil {
		t.Fatalf("connect to dolt server: %v", err)
	}
	defer admin.Close()

	if _, err := admin.Exec("DROP DATABASE IF EXISTS `" + dbName + "`"); err != nil {
		t.Fatalf("drop pre-existing test db: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE `" + dbName + "`"); err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS `" + dbName + "`")
	})

	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/%s", testutil.DoltContainerPort(), dbName))
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	exec("CREATE TABLE tracked (id INT PRIMARY KEY, val VARCHAR(64))")
	exec("CALL DOLT_COMMIT('-Am', 'create tracked')")
	for i := range 5 {
		exec(fmt.Sprintf("INSERT INTO tracked VALUES (%d, 'row-%d')", i, i))
		exec(fmt.Sprintf("CALL DOLT_COMMIT('-Am', 'insert %d')", i))
	}

	exec("CREATE TABLE wisp_probe (id INT PRIMARY KEY, val VARCHAR(64))")
	exec("INSERT INTO dolt_ignore (pattern, ignored) VALUES ('wisp_probe', true)")
	exec("INSERT INTO wisp_probe VALUES (1, 'alive'), (2, 'also-alive'), (3, 'still-alive')")

	return db
}

func newCompactorTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	t.Setenv("GT_DOLT_PORT", testutil.DoltContainerPort())
	return &Daemon{
		config: &Config{},
		logger: log.New(io.Discard, "", 0),
	}
}

// TestCompactDatabasePreservesDoltIgnoredTables reproduces, against a real
// Dolt server, the exact mechanism gt-3mnn / hq-by6wo raised as a LATENT
// hazard: `DOLT_COMMIT('-Am', ...)` — like `git commit -A` — never stages
// dolt_ignore'd tables (wisps/wisp_% in production), so the flattened
// commit's tree has no reference to their chunks. compactDatabase's own
// integrity check runs BEFORE dolt_gc(), so it can't observe anything gc
// does; if gc ever reclaimed those chunks as unreferenced garbage, the loss
// would go undetected.
//
// Run five repeated flatten+gc cycles (more churn than a single pass) and
// confirm the ignored table's rows still read back correctly. This is the
// experiment hq-by6wo's author identified as the one that would "settle"
// whether the hazard is real: it did not reproduce data loss for the Dolt
// version under test (Dolt's gc preserves the current working set
// regardless of commit-graph reachability) — see compactorVerifyPostGC for
// why the fix here is a monitoring guard rather than a workaround.
func TestCompactDatabasePreservesDoltIgnoredTables(t *testing.T) {
	dbName := "compactor_ignore_test"
	db := setupCompactorIgnoreTestDB(t, dbName)
	d := newCompactorTestDaemon(t)

	var preCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM wisp_probe").Scan(&preCount); err != nil {
		t.Fatalf("pre-flight count wisp_probe: %v", err)
	}
	if preCount != 3 {
		t.Fatalf("pre-flight sanity: wisp_probe has %d rows, want 3", preCount)
	}

	for i := 0; i < 5; i++ {
		if err := d.compactDatabase(dbName); err != nil {
			t.Fatalf("compactDatabase (cycle %d): %v", i, err)
		}
		if err := d.compactorRunGC(dbName); err != nil {
			t.Fatalf("compactorRunGC (cycle %d): %v", i, err)
		}
		var cycleCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM wisp_probe").Scan(&cycleCount); err != nil {
			t.Fatalf("count wisp_probe (cycle %d): %v", i, err)
		}
		if cycleCount != preCount {
			t.Fatalf("cycle %d: dolt_ignore'd table lost data across flatten+gc: pre=%d post=%d (gt-3mnn regression)",
				i, preCount, cycleCount)
		}
	}
}

// TestCompactorVerifyPostGCCatchesLoss is a positive control on
// compactorVerifyPostGC itself: it must fail when a table genuinely loses
// rows between the pre-gc and post-gc snapshots, not just pass because
// nothing in this Dolt version happens to trigger loss.
func TestCompactorVerifyPostGCCatchesLoss(t *testing.T) {
	dbName := "compactor_ignore_test_control"
	db := setupCompactorIgnoreTestDB(t, dbName)
	d := newCompactorTestDaemon(t)

	preGCCounts, err := d.compactorGetRowCountsForDB(dbName)
	if err != nil {
		t.Fatalf("compactorGetRowCountsForDB: %v", err)
	}

	// Simulate the exact loss gt-3mnn hypothesized: the ignored table's data
	// disappears between the pre-gc snapshot and the post-gc check.
	if _, err := db.Exec("DELETE FROM wisp_probe"); err != nil {
		t.Fatalf("simulate loss: %v", err)
	}

	if err := d.compactorVerifyPostGC(dbName, preGCCounts); err == nil {
		t.Fatalf("compactorVerifyPostGC did not catch simulated data loss in wisp_probe")
	}
}
