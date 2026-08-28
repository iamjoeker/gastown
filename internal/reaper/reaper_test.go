package reaper

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestValidateDBName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"hq", false},
		{"beads", false},
		{"gt", false},
		{"test_db_123", false},
		{"", true},
		{"drop table", true},
		{"db;--", true},
		{"db`name", true},
		{"../etc/passwd", true},
	}
	for _, tt := range tests {
		err := ValidateDBName(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateDBName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestDefaultDatabases(t *testing.T) {
	if len(DefaultDatabases) == 0 {
		t.Error("DefaultDatabases should not be empty")
	}
	for _, db := range DefaultDatabases {
		if err := ValidateDBName(db); err != nil {
			t.Errorf("DefaultDatabases contains invalid name %q: %v", db, err)
		}
	}
}

func TestDogReaperFormulaAlertThresholdMatchesDefault(t *testing.T) {
	data, err := os.ReadFile("../formula/formulas/mol-dog-reaper.formula.toml")
	if err != nil {
		t.Fatalf("read mol-dog-reaper formula: %v", err)
	}

	threshold := fmt.Sprintf("%d", DefaultAlertThreshold)
	source := string(data)
	alertThresholdVars := sourceBetween(t, source, "[vars.alert_threshold]", "[vars.dry_run]")
	if !strings.Contains(alertThresholdVars, fmt.Sprintf("default = %q", threshold)) {
		t.Fatalf("mol-dog-reaper alert_threshold default should match DefaultAlertThreshold %s", threshold)
	}
	if !strings.Contains(source, fmt.Sprintf("default %s", threshold)) {
		t.Fatalf("mol-dog-reaper alert_threshold prose should document default %s", threshold)
	}
}

func TestFormatJSON(t *testing.T) {
	result := FormatJSON(map[string]int{"count": 42})
	if result == "" {
		t.Error("FormatJSON should not return empty string")
	}
	if result[0] != '{' {
		t.Errorf("FormatJSON should return JSON object, got %q", result[:10])
	}
}

func TestParentExcludeJoin(t *testing.T) {
	joinClause, whereCondition := parentExcludeJoin("testdb")

	// JOIN clause should reference the correct database.
	if joinClause == "" {
		t.Error("parentExcludeJoin joinClause should not be empty")
	}
	// parentExcludeJoin no longer qualifies table names with the database — the
	// reaper connects to a specific database via the DSN, so unqualified names
	// are correct. The dbName parameter is retained for API compatibility.

	// JOIN should select wisps with open parents from wisp_dependencies.
	if !contains(joinClause, "wisp_dependencies") {
		t.Error("parentExcludeJoin should query wisp_dependencies")
	}
	if !contains(joinClause, "wd.depends_on_wisp_id") {
		t.Error("parentExcludeJoin should join wisp parents through depends_on_wisp_id")
	}
	if !contains(joinClause, "wd.depends_on_issue_id") {
		t.Error("parentExcludeJoin should join issue parents through depends_on_issue_id")
	}
	if contains(joinClause, "wd.depends_on_id") {
		t.Error("parentExcludeJoin should not use legacy depends_on_id")
	}
	if !contains(joinClause, "parent-child") {
		t.Error("parentExcludeJoin should filter on parent-child type")
	}
	if !contains(joinClause, "'open', 'hooked', 'in_progress'") {
		t.Error("parentExcludeJoin should check for open parent statuses")
	}

	// WHERE condition should be an IS NULL anti-join filter.
	if whereCondition == "" {
		t.Error("parentExcludeJoin whereCondition should not be empty")
	}
	if !contains(whereCondition, "IS NULL") {
		t.Error("parentExcludeJoin whereCondition should use IS NULL for anti-join")
	}
}

func TestReaperQueriesUseTypedDependencyColumns(t *testing.T) {
	sourcePath := "reaper.go"
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read %s: %v", sourcePath, err)
	}
	source := string(data)
	if strings.Contains(source, "depends_on_id") {
		t.Fatalf("reaper queries should not use legacy depends_on_id")
	}

	scanBody := sourceBetween(t, source, "func Scan(", "func Reap(")
	autoCloseBody := sourceBetween(t, source, "func AutoClose(", "// batchDeleteRows")
	batchDeleteBody := sourceBetween(t, source, "func batchDeleteRows(", "// ClosePluginReceiptResult")
	schemaBody := sourceBetween(t, source, "func HasReaperSchema(", "func tableExists(")

	for _, want := range []string{
		`hasColumns(ctx, db, "wisp_dependencies", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external")`,
		`hasColumns(ctx, db, "dependencies", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external")`,
	} {
		if !strings.Contains(schemaBody, want) {
			t.Fatalf("HasReaperSchema missing typed schema gate %q", want)
		}
	}

	for _, body := range []struct {
		name string
		text string
	}{
		{name: "Scan", text: scanBody},
		{name: "AutoClose", text: autoCloseBody},
	} {
		if !strings.Contains(body.text, "d.depends_on_issue_id = dep.id") {
			t.Fatalf("%s should join dependency blockers through depends_on_issue_id", body.name)
		}
		if !strings.Contains(body.text, "SELECT DISTINCT d.depends_on_issue_id") {
			t.Fatalf("%s should exclude blocked issues through depends_on_issue_id", body.name)
		}
		if !strings.Contains(body.text, "d.depends_on_issue_id IS NOT NULL") {
			t.Fatalf("%s should guard nullable depends_on_issue_id in NOT IN subquery", body.name)
		}
	}

	if !strings.Contains(scanBody, "wd.depends_on_wisp_id IS NOT NULL OR wd.depends_on_issue_id IS NOT NULL") {
		t.Fatal("Scan dangling-parent anomaly should ignore external-only dependency rows")
	}
	if !strings.Contains(batchDeleteBody, "DELETE FROM wisp_dependencies WHERE depends_on_wisp_id IN %s") {
		t.Fatal("batchDeleteRows should clean reverse wisp dependency references")
	}
	if !strings.Contains(batchDeleteBody, "DELETE FROM dependencies WHERE depends_on_wisp_id IN %s") {
		t.Fatal("batchDeleteRows should clean reverse issue dependency references to wisps")
	}
	if !strings.Contains(batchDeleteBody, "DELETE FROM wisp_dependencies WHERE depends_on_issue_id IN %s") {
		t.Fatal("batchDeleteRows should clean reverse wisp parent references to issues")
	}
	if !strings.Contains(batchDeleteBody, "DELETE FROM dependencies WHERE depends_on_issue_id IN %s") {
		t.Fatal("batchDeleteRows should clean reverse issue dependency references")
	}
}

// TestReapQueryNoDatabaseNameInjection verifies that the Reap function's batch
// SELECT query does not inject the database name into the SQL string. Previously,
// dbName was passed as a Sprintf arg but the format string didn't use it, causing
// positional shift: "FROM wisps w gt WHERE..." instead of "FROM wisps w LEFT JOIN...".
func TestReapQueryNoDatabaseNameInjection(t *testing.T) {
	// Reproduce the exact Sprintf call from Reap() to verify no dbName injection.
	dbName := "gt"
	parentJoin, parentWhere := parentExcludeJoin(dbName)
	whereClause := fmt.Sprintf(
		"w.status IN ('open', 'hooked', 'in_progress') AND w.created_at < ? AND %s", parentWhere)

	// This is the fixed query — dbName is NOT in the Sprintf args.
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s WHERE %s LIMIT %d",
		parentJoin, whereClause, DefaultBatchSize)

	// The query must NOT contain the literal database name as a bare token.
	// Before the fix, "gt" appeared between "wisps w" and "WHERE".
	if strings.Contains(idQuery, "wisps w gt") {
		t.Errorf("Reap idQuery contains injected database name: %s", idQuery)
	}
	if !strings.Contains(idQuery, "LEFT JOIN") {
		t.Errorf("Reap idQuery should contain LEFT JOIN from parentExcludeJoin, got: %s", idQuery)
	}
	if !strings.Contains(idQuery, fmt.Sprintf("LIMIT %d", DefaultBatchSize)) {
		t.Errorf("Reap idQuery should end with LIMIT %d, got: %s", DefaultBatchSize, idQuery)
	}
}

// TestReapUpdateQueryNoDatabaseNameInjection verifies that the UPDATE query in
// Reap() does not inject dbName where the IN clause should go.
func TestReapUpdateQueryNoDatabaseNameInjection(t *testing.T) {
	dbName := "gt"
	inClause := "?,?,?"

	// This is the fixed query — only inClause in the Sprintf args.
	updateQuery := fmt.Sprintf(
		"UPDATE wisps SET status='closed', closed_at=NOW() WHERE id IN (%s)",
		inClause)

	if strings.Contains(updateQuery, dbName) {
		t.Errorf("Reap updateQuery contains injected database name %q: %s", dbName, updateQuery)
	}
	if !strings.Contains(updateQuery, "IN (?,?,?)") {
		t.Errorf("Reap updateQuery should contain parameterized IN clause, got: %s", updateQuery)
	}
}

// TestPurgeDigestQueryNoDatabaseNameInjection verifies that the purge digest
// query is a plain string with no Sprintf interpolation at all.
func TestPurgeDigestQueryNoDatabaseNameInjection(t *testing.T) {
	// The fixed digestQuery is a string literal — no Sprintf.
	digestQuery := "SELECT COALESCE(w.wisp_type, 'unknown') AS wtype, COUNT(*) AS cnt FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? GROUP BY wtype"

	if strings.Contains(digestQuery, "gt") {
		t.Errorf("purge digestQuery should not contain database name, got: %s", digestQuery)
	}
	if !strings.Contains(digestQuery, "GROUP BY wtype") {
		t.Errorf("purge digestQuery should end with GROUP BY, got: %s", digestQuery)
	}
}

// TestPurgeBatchQueryNoDatabaseNameInjection verifies that the purge batch
// SELECT query uses DefaultBatchSize as the LIMIT, not dbName.
func TestPurgeBatchQueryNoDatabaseNameInjection(t *testing.T) {
	// This is the fixed query — only DefaultBatchSize in the Sprintf args.
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? LIMIT %d",
		DefaultBatchSize)

	if strings.Contains(idQuery, "gt") {
		t.Errorf("purge idQuery contains injected database name: %s", idQuery)
	}
	expected := fmt.Sprintf("LIMIT %d", DefaultBatchSize)
	if !strings.Contains(idQuery, expected) {
		t.Errorf("purge idQuery should contain %s, got: %s", expected, idQuery)
	}
}

// TestIsNothingToCommit verifies that "nothing to commit" errors are recognized
// correctly. This prevents false-positive dolt_commit_failed anomalies when the
// reaper operates on dolt_ignored tables (wisps, wisp_*), where Dolt has nothing
// to version after a successful SQL DELETE.
func TestIsNothingToCommit(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"nothing to commit", true},
		{"NOTHING TO COMMIT", true},
		{"Error 1105 (HY000): nothing to commit", true},
		{"no changes to commit", false}, // must also contain "commit" — see isNothingToCommit
		{"no changes", false},
		{"connection refused", false},
		{"table not found: wisps", false},
		{"", false},
	}
	for _, c := range cases {
		var err error
		if c.msg != "" {
			err = fmt.Errorf("%s", c.msg)
		}
		got := isNothingToCommit(err)
		if got != c.want {
			t.Errorf("isNothingToCommit(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func sourceBetween(t *testing.T, source, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	if start == -1 {
		t.Fatalf("could not find %q", startMarker)
	}
	end := strings.Index(source[start:], endMarker)
	if end == -1 {
		t.Fatalf("could not find %q after %q", endMarker, startMarker)
	}
	return source[start : start+end]
}

// TestReapExcludesAgentBeads verifies that the Reap function excludes agent beads
// from being closed, regardless of their age. This is a regression test for the bug
// where the wisp reaper was closing agent beads (hq-mayor, hq-deacon, witness, refinery,
// etc.) after 24 hours, causing doctor to report them as missing.
func TestReapExcludesAgentBeads(t *testing.T) {
	// SUPERSEDED (gt-am7). This test asserts nothing: it checks no source
	// pattern and calls no code, so it cannot fail for any change to Reap. Its
	// original comments claimed the exclusion was "a compile-time guard", was
	// "verified manually", and was "tested in integration tests with a real
	// database" — none of which was true; no such integration test existed.
	//
	// TestReapExcludesAgentBeadsBehaviour in reaper_behavior_test.go now runs
	// Reap against a real engine and observes the agent wisp surviving next to
	// a same-age control wisp that does not. Kept only so the two log lines
	// below stay findable from the incident history.
	t.Log("Agent beads (issue_type='agent') are excluded from wisp reaping")
	t.Log("This prevents hq-mayor, hq-deacon, witness, refinery, etc. from being closed")
}

// TestScanExcludesAgentBeads documents that Scan() must use the same eligibility
// predicate as Reap() for stale open wisps. If Scan counts agent beads but Reap
// excludes them, the operator sees scan>0 and reap=0 for the same cutoff.
func TestScanExcludesAgentBeads(t *testing.T) {
	sourcePath := "reaper.go"
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read %s: %v", sourcePath, err)
	}
	source := string(data)
	scanStart := strings.Index(source, "func Scan(")
	reapStart := strings.Index(source, "func Reap(")
	if scanStart == -1 || reapStart == -1 || reapStart <= scanStart {
		t.Fatalf("could not isolate Scan() body in %s", sourcePath)
	}
	scanBody := source[scanStart:reapStart]
	if !strings.Contains(scanBody, "w.issue_type != 'agent'") {
		t.Fatalf("expected Scan() eligibility to exclude agent beads, scan body was:\n%s", scanBody)
	}
}

func TestClosedMoleculeStepReapBehavior(t *testing.T) {
	now := time.Now().UTC()
	state := &fakeReaperState{
		wisps: map[string]*fakeWisp{
			"mol-closed":               {id: "mol-closed", status: "closed", issueType: "molecule", createdAt: now},
			"mol-open":                 {id: "mol-open", status: "open", issueType: "molecule", createdAt: now},
			"closed-epic":              {id: "closed-epic", status: "closed", issueType: "epic", createdAt: now},
			"step-closed-mol-recent":   {id: "step-closed-mol-recent", status: "open", issueType: "task", createdAt: now.Add(-1 * time.Hour)},
			"step-closed-mol-old":      {id: "step-closed-mol-old", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"step-mixed-parent-old":    {id: "step-mixed-parent-old", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"step-external-parent-old": {id: "step-external-parent-old", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"step-open-parent-old":     {id: "step-open-parent-old", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"step-non-molecule-parent": {id: "step-non-molecule-parent", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"agent-step":               {id: "agent-step", status: "open", issueType: "agent", createdAt: now.Add(-48 * time.Hour)},
			"stale-orphan":             {id: "stale-orphan", status: "open", issueType: "task", createdAt: now.Add(-48 * time.Hour)},
			"fresh-orphan":             {id: "fresh-orphan", status: "open", issueType: "task", createdAt: now.Add(-1 * time.Hour)},
		},
		deps: []fakeDep{
			{issueID: "step-closed-mol-recent", dependsOnID: "mol-closed", depType: "parent-child"},
			{issueID: "step-closed-mol-old", dependsOnID: "mol-closed", depType: "parent-child"},
			{issueID: "step-mixed-parent-old", dependsOnID: "mol-closed", depType: "parent-child"},
			{issueID: "step-mixed-parent-old", dependsOnID: "mol-open", depType: "parent-child"},
			{issueID: "step-external-parent-old", dependsOnID: "mol-closed", depType: "parent-child"},
			{issueID: "step-external-parent-old", dependsOnExternal: "external:other", depType: "parent-child"},
			{issueID: "step-open-parent-old", dependsOnID: "mol-open", depType: "parent-child"},
			{issueID: "step-non-molecule-parent", dependsOnID: "closed-epic", depType: "parent-child"},
			{issueID: "agent-step", dependsOnID: "mol-closed", depType: "parent-child"},
		},
		ops: map[int][]string{},
	}
	db := openFakeReaperDB(t, state)
	t.Cleanup(func() { _ = db.Close() })

	maxAge := 24 * time.Hour
	scan, err := Scan(db, "testdb", maxAge, 7*24*time.Hour, 7*24*time.Hour, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scan.MoleculeStepCandidates != 2 {
		t.Fatalf("Scan MoleculeStepCandidates = %d, want 2", scan.MoleculeStepCandidates)
	}
	if scan.ReapCandidates != 2 {
		t.Fatalf("Scan ReapCandidates = %d, want 2", scan.ReapCandidates)
	}

	beforeDryRun := state.statuses()
	dryRun, err := Reap(db, "testdb", maxAge, true)
	if err != nil {
		t.Fatalf("dry-run Reap: %v", err)
	}
	if dryRun.MoleculeStepsClosed != 2 {
		t.Fatalf("dry-run MoleculeStepsClosed = %d, want 2", dryRun.MoleculeStepsClosed)
	}
	if dryRun.Reaped != 2 {
		t.Fatalf("dry-run Reaped = %d, want 2", dryRun.Reaped)
	}
	if dryRun.OpenRemain != 10 {
		t.Fatalf("dry-run OpenRemain = %d, want 10", dryRun.OpenRemain)
	}
	if afterDryRun := state.statuses(); !reflect.DeepEqual(afterDryRun, beforeDryRun) {
		t.Fatalf("dry-run mutated statuses: before=%v after=%v", beforeDryRun, afterDryRun)
	}

	preRealOps := state.opCounts()
	realRun, err := Reap(db, "testdb", maxAge, false)
	if err != nil {
		t.Fatalf("real Reap: %v", err)
	}
	if realRun.MoleculeStepsClosed != 2 {
		t.Fatalf("real MoleculeStepsClosed = %d, want 2", realRun.MoleculeStepsClosed)
	}
	if realRun.Reaped != 2 {
		t.Fatalf("real Reaped = %d, want 2", realRun.Reaped)
	}
	if realRun.OpenRemain != 6 {
		t.Fatalf("real OpenRemain = %d, want 6", realRun.OpenRemain)
	}

	for _, id := range []string{"step-closed-mol-recent", "step-closed-mol-old", "step-non-molecule-parent", "stale-orphan"} {
		if got := state.status(id); got != "closed" {
			t.Fatalf("%s status = %q, want closed", id, got)
		}
	}
	for _, id := range []string{"step-mixed-parent-old", "step-external-parent-old", "step-open-parent-old", "agent-step", "fresh-orphan", "mol-open"} {
		if got := state.status(id); got != "open" {
			t.Fatalf("%s status = %q, want open", id, got)
		}
	}
	realOps := state.opsSince(preRealOps)
	if len(realOps) != 1 {
		t.Fatalf("real Reap used %d connections, want 1: %#v", len(realOps), realOps)
	}
	for connID, ops := range realOps {
		assertOpsContainInOrder(t, ops,
			"EXEC SET @@autocommit = 0",
			"QUERY SELECT w.id FROM wisps w INNER JOIN",
			"EXEC UPDATE wisps SET status='closed'",
			"QUERY SELECT w.id FROM wisps w LEFT JOIN",
			"EXEC UPDATE wisps SET status='closed'",
			"EXEC COMMIT",
			"EXEC CALL DOLT_COMMIT",
			"QUERY SELECT COUNT(*) FROM wisps WHERE status IN",
			"EXEC SET @@autocommit = 1",
		)
		t.Logf("real Reap used pinned connection %d", connID)
	}
}

var fakeReaperDriverID uint64

func openFakeReaperDB(t *testing.T, state *fakeReaperState) *sql.DB {
	t.Helper()
	driverName := fmt.Sprintf("fake_reaper_%d", atomic.AddUint64(&fakeReaperDriverID, 1))
	sql.Register(driverName, &fakeReaperDriver{state: state})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	return db
}

type fakeWisp struct {
	id        string
	status    string
	issueType string
	createdAt time.Time
	assignee  string
	// description carries the hook-bead fields when this wisp is itself a hook
	// bead. A molecule's dispatch record lives here, not on the molecule.
	description string
}

// fakeIssue is a row in `issues`. Only the description matters to the reaper's
// stranded-molecule probe: it is where "attached_molecule: <id>" is written.
type fakeIssue struct {
	id          string
	description string
}

type fakeDep struct {
	issueID           string
	dependsOnID       string
	dependsOnExternal string
	depType           string
}

type fakeReaperState struct {
	mu       sync.Mutex
	wisps    map[string]*fakeWisp
	issues   map[string]*fakeIssue
	deps     []fakeDep
	nextConn int
	ops      map[int][]string
}

func (s *fakeReaperState) status(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wisps[id].status
}

func (s *fakeReaperState) statuses() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	statuses := make(map[string]string, len(s.wisps))
	for id, w := range s.wisps {
		statuses[id] = w.status
	}
	return statuses
}

func (s *fakeReaperState) opCounts() map[int]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := make(map[int]int, len(s.ops))
	for connID, ops := range s.ops {
		counts[connID] = len(ops)
	}
	return counts
}

func (s *fakeReaperState) opsSince(counts map[int]int) map[int][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	opsSince := map[int][]string{}
	for connID, ops := range s.ops {
		start := counts[connID]
		if start < len(ops) {
			opsSince[connID] = append([]string(nil), ops[start:]...)
		}
	}
	return opsSince
}

func (s *fakeReaperState) record(connID int, op string) {
	s.ops[connID] = append(s.ops[connID], normalizeSQL(op))
}

func (s *fakeReaperState) moleculeStepCandidatesLocked() []string {
	var ids []string
	for id := range s.wisps {
		if s.isMoleculeStepCandidateLocked(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (s *fakeReaperState) isMoleculeStepCandidateLocked(id string) bool {
	w := s.wisps[id]
	if w == nil || !isOpenWispStatus(w.status) || w.issueType == "agent" {
		return false
	}
	for _, dep := range s.deps {
		if dep.issueID != id || dep.depType != "parent-child" {
			continue
		}
		if dep.dependsOnExternal != "" {
			return false
		}
		if s.hasOpenParentLocked(id) {
			return false
		}
		parent := s.wisps[dep.dependsOnID]
		if parent != nil && parent.issueType == "molecule" && parent.status == "closed" {
			return true
		}
	}
	return false
}

func (s *fakeReaperState) staleCandidatesLocked(cutoff time.Time, excludeMoleculeSteps bool) []string {
	var ids []string
	for id, w := range s.wisps {
		if !isOpenWispStatus(w.status) || w.issueType == "agent" || !w.createdAt.Before(cutoff) {
			continue
		}
		if s.hasOpenParentLocked(id) {
			continue
		}
		if excludeMoleculeSteps && s.isMoleculeStepCandidateLocked(id) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *fakeReaperState) hasOpenParentLocked(id string) bool {
	for _, dep := range s.deps {
		if dep.issueID != id || dep.depType != "parent-child" {
			continue
		}
		if dep.dependsOnExternal != "" {
			return true
		}
		parent := s.wisps[dep.dependsOnID]
		if parent != nil && isOpenWispStatus(parent.status) {
			return true
		}
	}
	return false
}

// unassignedMoleculesLocked mirrors the column-level half of Scan's
// stranded-molecule probe: open molecule wisps created before the cutoff that
// carry no assignee. The attachment half is applied by the reaper in Go, off the
// descriptions the two arms below serve, so this deliberately does NOT filter on
// dispatch — a fake that pre-filtered here would answer the very question under
// test and the probe could not fail.
func (s *fakeReaperState) unassignedMoleculesLocked(cutoff time.Time) []string {
	var ids []string
	for id, w := range s.wisps {
		if w.issueType != "molecule" || !isOpenWispStatus(w.status) {
			continue
		}
		if w.assignee != "" || !w.createdAt.Before(cutoff) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// descriptionsLocked returns the descriptions the reaper's attachment prefilter
// would match, from `issues` or `wisps`. The prefilter is reproduced here rather
// than assumed: a fake that returned every row would let a probe pass whose SQL
// filters away the rows it needs.
func (s *fakeReaperState) descriptionsLocked(table string) []string {
	var descs []string
	matches := func(desc string) bool {
		lower := strings.ToLower(desc)
		i := strings.Index(lower, "attached")
		return i >= 0 && strings.Contains(lower[i:], "molecule")
	}
	switch table {
	case "issues":
		for _, i := range s.issues {
			if matches(i.description) {
				descs = append(descs, i.description)
			}
		}
	case "wisps":
		for _, w := range s.wisps {
			if matches(w.description) {
				descs = append(descs, w.description)
			}
		}
	}
	sort.Strings(descs)
	return descs
}

func (s *fakeReaperState) openCountLocked() int {
	count := 0
	for _, w := range s.wisps {
		if isOpenWispStatus(w.status) {
			count++
		}
	}
	return count
}

type fakeReaperDriver struct {
	state *fakeReaperState
}

func (d *fakeReaperDriver) Open(string) (driver.Conn, error) {
	d.state.mu.Lock()
	defer d.state.mu.Unlock()
	d.state.nextConn++
	connID := d.state.nextConn
	d.state.ops[connID] = nil
	return &fakeReaperConn{state: d.state, id: connID}, nil
}

type fakeReaperConn struct {
	state *fakeReaperState
	id    int
}

func (c *fakeReaperConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare not implemented")
}

func (c *fakeReaperConn) Close() error { return nil }

func (c *fakeReaperConn) Begin() (driver.Tx, error) { return fakeReaperTx{}, nil }

func (c *fakeReaperConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *fakeReaperConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	normalized := normalizeSQL(query)
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.record(c.id, "QUERY "+normalized)

	switch {
	// Stranded-molecule probe, first half. Must precede the stale-candidate
	// cases: it is also a SELECT over wisps w with a created_at bound, so it
	// would otherwise be answered by the stale-candidate arm — which rejects it,
	// and the caller swallows that error and reports zero. A probe that cannot
	// fail is a probe that certifies nothing, so match it explicitly.
	case strings.Contains(normalized, "FROM wisps w") && strings.Contains(normalized, "w.assignee IS NULL"):
		return fakeIDRows(c.state.unassignedMoleculesLocked(namedTime(args))), nil
	// Stranded-molecule probe, second half: the dispatch records. These live in
	// hook-bead descriptions, in either table.
	case strings.HasPrefix(normalized, "SELECT description FROM issues"):
		return fakeIDRows(c.state.descriptionsLocked("issues")), nil
	case strings.HasPrefix(normalized, "SELECT description FROM wisps"):
		return fakeIDRows(c.state.descriptionsLocked("wisps")), nil
	case strings.Contains(normalized, "SELECT COUNT(*) FROM wisps w") && strings.Contains(normalized, "created_at <"):
		if err := validateStaleWispQuery(normalized); err != nil {
			return nil, err
		}
		return fakeCountRows(len(c.state.staleCandidatesLocked(namedTime(args), strings.Contains(normalized, "closed_molecule_step.issue_id IS NULL")))), nil
	case strings.Contains(normalized, "SELECT COUNT(*) FROM wisps w") && strings.Contains(normalized, "pm.issue_type = 'molecule'"):
		if err := validateMoleculeStepQuery(normalized); err != nil {
			return nil, err
		}
		return fakeCountRows(len(c.state.moleculeStepCandidatesLocked())), nil
	case strings.Contains(normalized, "SELECT COUNT(*) FROM wisps WHERE status IN"):
		return fakeCountRows(c.state.openCountLocked()), nil
	case strings.Contains(normalized, "SELECT COUNT(*) FROM wisps w WHERE w.status = 'closed'"):
		return fakeCountRows(0), nil
	case strings.Contains(normalized, "SELECT COUNT(*) FROM issues"):
		return fakeCountRows(0), nil
	case strings.Contains(normalized, "SELECT COUNT(*) FROM wisp_dependencies wd"):
		return fakeCountRows(0), nil
	case strings.Contains(normalized, "SELECT w.id FROM wisps w") && strings.Contains(normalized, "created_at <"):
		if err := validateStaleWispQuery(normalized); err != nil {
			return nil, err
		}
		return fakeIDRows(c.state.staleCandidatesLocked(namedTime(args), strings.Contains(normalized, "closed_molecule_step.issue_id IS NULL"))), nil
	case strings.Contains(normalized, "SELECT w.id FROM wisps w") && strings.Contains(normalized, "pm.issue_type = 'molecule'"):
		if err := validateMoleculeStepQuery(normalized); err != nil {
			return nil, err
		}
		return fakeIDRows(c.state.moleculeStepCandidatesLocked()), nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", normalized)
	}
}

func (c *fakeReaperConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	normalized := normalizeSQL(query)
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.record(c.id, "EXEC "+normalized)

	switch {
	case strings.HasPrefix(normalized, "UPDATE wisps SET status='closed'"):
		affected := int64(0)
		for _, arg := range args {
			id, _ := arg.Value.(string)
			if w := c.state.wisps[id]; w != nil && isOpenWispStatus(w.status) {
				w.status = "closed"
				affected++
			}
		}
		return fakeReaperResult(affected), nil
	case normalized == "SET @@autocommit = 0" || normalized == "SET @@autocommit = 1" || normalized == "ROLLBACK" || normalized == "COMMIT" || strings.HasPrefix(normalized, "CALL DOLT_COMMIT"):
		return fakeReaperResult(0), nil
	default:
		return nil, fmt.Errorf("unexpected exec: %s", normalized)
	}
}

type fakeReaperTx struct{}

func (fakeReaperTx) Commit() error   { return nil }
func (fakeReaperTx) Rollback() error { return nil }

type fakeReaperResult int64

func (r fakeReaperResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeReaperResult) RowsAffected() (int64, error) { return int64(r), nil }

type fakeReaperRows struct {
	cols []string
	rows [][]driver.Value
	next int
}

func fakeCountRows(count int) *fakeReaperRows {
	return &fakeReaperRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(count)}}}
}

func fakeIDRows(ids []string) *fakeReaperRows {
	rows := make([][]driver.Value, len(ids))
	for i, id := range ids {
		rows[i] = []driver.Value{id}
	}
	return &fakeReaperRows{cols: []string{"id"}, rows: rows}
}

func (r *fakeReaperRows) Columns() []string { return r.cols }
func (r *fakeReaperRows) Close() error      { return nil }

func (r *fakeReaperRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

func namedTime(args []driver.NamedValue) time.Time {
	if len(args) == 0 {
		return time.Time{}
	}
	if value, ok := args[0].Value.(time.Time); ok {
		return value
	}
	return time.Time{}
}

func isOpenWispStatus(status string) bool {
	return status == "open" || status == "hooked" || status == "in_progress"
}

func normalizeSQL(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

func validateMoleculeStepQuery(query string) error {
	return requireSQL(query,
		"wd.issue_id",
		"pm.id = wd.depends_on_wisp_id",
		"wd.type = 'parent-child'",
		"pm.issue_type = 'molecule'",
		"pm.status = 'closed'",
		"NOT EXISTS",
		"open_dep.depends_on_external IS NOT NULL",
		"w.issue_type != 'agent'",
		"w.status IN ('open', 'hooked', 'in_progress')",
	)
}

func validateStaleWispQuery(query string) error {
	return requireSQL(query,
		"wd.issue_id",
		"pw.id = wd.depends_on_wisp_id",
		"pi.id = wd.depends_on_issue_id",
		"pi.status IN ('open', 'hooked', 'in_progress')",
		"depends_on_external IS NOT NULL",
		"wd.type = 'parent-child'",
		"w.issue_type != 'agent'",
		"w.created_at < ?",
		"open_parent.issue_id IS NULL",
		"closed_molecule_step.issue_id IS NULL",
	)
}

func requireSQL(query string, required ...string) error {
	if strings.Contains(query, "depends_on_id") {
		return fmt.Errorf("query uses legacy depends_on_id column: %s", query)
	}
	for _, want := range required {
		if !strings.Contains(query, want) {
			return fmt.Errorf("query missing %q: %s", want, query)
		}
	}
	return nil
}

func assertOpsContainInOrder(t *testing.T, ops []string, want ...string) {
	t.Helper()
	next := 0
	for _, op := range ops {
		if strings.Contains(op, want[next]) {
			next++
			if next == len(want) {
				return
			}
		}
	}
	t.Fatalf("ops missing ordered sequence %v in %v", want[next:], ops)
}

// TestAutoCloseExemptsAgentBeads pins the fix for the recurring agent-bead
// reaping incident (gt-caq / hq-02sk).
//
// AGENT BEADS are the town's per-role identity rows — hq-mayor, hq-deacon,
// bd-beads-witness, gt-gastown-refinery and so on. `gt agents resolve` looks
// them up by role and answers "no agent bead found" once one is CLOSED.
// mol-witness-patrol's loop-or-exit makes exactly that call and is told to STOP
// and report on failure, so every witness/refinery respawning after a sweep
// walks its own molecule into a halt — the layer that would notice a stalled
// town is the layer the sweep disables.
//
// The reaper stale-closed 7 of 8 agent beads on 2026-08-10 and AGAIN on
// 2026-08-17. Nothing else in AutoClose catches them:
//   - issue_type is 'task', so the epic/convoy exclusion does not apply, and
//     neither does the wisp-side "issue_type != 'agent'" guard — no agent bead
//     has ever carried type 'agent'.
//   - role_type is EMPTY on every agent bead, so keying on role_type (as the
//     escalation originally proposed) would be equally INERT: it would look
//     like a fix and change nothing.
//
// The gt:agent LABEL is the only populated discriminator; all 8 carry it.
// hq-deacon survived both sweeps only because it is the most active agent and
// its updated_at stays under the staleness cutoff — recency, not protection —
// so "one survivor" is not evidence that any guard works.
// The exemption list now lives in AutoCloseExemptLabels rather than inline in
// the query, because Scan needs the identical set and the two hand-copied
// literals drifted once already (gt-jbn). So this checks both halves: the label
// is in the list, and the query is still rendered FROM the list — a query that
// went back to an inline literal would keep passing the first check while the
// list stopped meaning anything.
func TestAutoCloseExemptsAgentBeads(t *testing.T) {
	if !slices.Contains(AutoCloseExemptLabels, "gt:agent") {
		t.Fatal("AutoCloseExemptLabels must contain gt:agent, or the reaper stale-closes " +
			"every agent bead and gt agents resolve stops answering by role")
	}
	// The pre-existing exemptions must survive alongside it.
	for _, label := range []string{"gt:standing-orders", "gt:keep", "gt:role", "gt:rig", "gt:message"} {
		if !slices.Contains(AutoCloseExemptLabels, label) {
			t.Errorf("exemption %q was dropped from AutoCloseExemptLabels", label)
		}
	}

	data, err := os.ReadFile("reaper.go")
	if err != nil {
		t.Fatalf("read reaper.go: %v", err)
	}
	autoCloseBody := sourceBetween(t, string(data), "func AutoClose(", "// batchDeleteRows")

	idx := strings.Index(autoCloseBody, "l.label IN (")
	if idx < 0 {
		t.Fatal("AutoClose no longer has a label exclusion list")
	}
	rest := autoCloseBody[idx:]
	end := strings.Index(rest, ")")
	if end < 0 {
		t.Fatal("label exclusion list is not terminated")
	}
	if list := rest[:end]; !strings.Contains(list, "%s") {
		t.Errorf("AutoClose's label exclusion list must be rendered from AutoCloseExemptLabels, "+
			"not inlined — an inline list drifts from Scan's copy.\nlist: %s", list)
	}
	if !strings.Contains(autoCloseBody, "AutoCloseExemptLabels") {
		t.Error("AutoClose no longer references AutoCloseExemptLabels; its exclusion list " +
			"is being fed from somewhere else")
	}
}

// TestScanAndAutoCloseShareOneExemptList is the drift guard the gt-jbn incident
// asked for: Scan's stale count and AutoClose's sweep must exempt the same set,
// or the Dog reads a candidate count the sweep will not act on. Both queries are
// built from AutoCloseExemptLabels, so the way to break this is to inline a
// literal back into one of them.
func TestScanAndAutoCloseShareOneExemptList(t *testing.T) {
	data, err := os.ReadFile("reaper.go")
	if err != nil {
		t.Fatalf("read reaper.go: %v", err)
	}
	scanBody := sourceBetween(t, string(data), "func Scan(", "// Reap closes stale wisps")
	if !strings.Contains(scanBody, "sqlLabelList(AutoCloseExemptLabels)") {
		t.Error("Scan's stale-issue query must render AutoCloseExemptLabels; a second " +
			"hand-written copy is what over-reported stale candidates in gt-jbn")
	}
}

// TestScanFlagsStrandedMolecules covers the self-surfacing sweep gt-bnpw asked
// for: an emitter that pours molecules nobody runs must show up in the scan
// rather than waiting for a human to notice the table growing.
//
// Every other number Scan reports is about AGE, and a runaway emitter's output
// is individually young — that is exactly why ~1000 dog-molecule wisps a day
// accumulated silently. Open + unassigned + older than one cycle is the signal
// that says "poured for an executor that never came".
func TestScanFlagsStrandedMolecules(t *testing.T) {
	now := time.Now().UTC()
	state := &fakeReaperState{
		wisps: map[string]*fakeWisp{
			// Stranded: open, unassigned, unattached, past StrandedMoleculeAge.
			"mol-stranded": {id: "mol-stranded", status: "open", issueType: "molecule", createdAt: now.Add(-4 * time.Hour)},
			// Just poured — a dispatcher may still be on its way.
			"mol-fresh": {id: "mol-fresh", status: "open", issueType: "molecule", createdAt: now.Add(-1 * time.Minute)},
			// Old but picked up: somebody owns it, so it is work in flight.
			"mol-assigned": {id: "mol-assigned", status: "hooked", issueType: "molecule", createdAt: now.Add(-4 * time.Hour), assignee: "deacon/dogs/alpha"},
			// Old and unassigned but already closed — it ran, or was reaped.
			"mol-closed": {id: "mol-closed", status: "closed", issueType: "molecule", createdAt: now.Add(-4 * time.Hour)},
			// A step, not a molecule. Steps are unassigned by design; counting them
			// would swamp the signal with the very population it is meant to explain.
			"step-old": {id: "step-old", status: "open", issueType: "task", createdAt: now.Add(-4 * time.Hour)},
		},
		issues: map[string]*fakeIssue{
			// A hook bead that dispatched something else. It must not launder
			// mol-stranded: the exclusion is keyed on the molecule ID, not on the
			// mere presence of an attachment line somewhere in the database.
			"bug-other": {id: "bug-other", description: "attached_molecule: mol-elsewhere\nattached_formula: mol-polecat-work"},
			// Prose that discusses attachment without recording one. The reaper's
			// LIKE is a prefilter; the parser is what decides, so this row is read
			// and contributes nothing (the CONTAMINATION failure mode: text ABOUT a
			// thing satisfying a search FOR it).
			"bug-prose": {id: "bug-prose", description: "The Attached Molecule mol-stranded looked wrong to me."},
		},
		ops: map[int][]string{},
	}
	db := openFakeReaperDB(t, state)
	t.Cleanup(func() { _ = db.Close() })

	scan, err := Scan(db, "testdb", 24*time.Hour, 7*24*time.Hour, 7*24*time.Hour, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	var found *Anomaly
	for i := range scan.Anomalies {
		if scan.Anomalies[i].Type == "stranded_molecules" {
			found = &scan.Anomalies[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a stranded_molecules anomaly, got %+v", scan.Anomalies)
	}
	if found.Count != 1 {
		t.Errorf("stranded_molecules Count = %d, want 1 (only mol-stranded qualifies)", found.Count)
	}
	if !strings.Contains(found.Message, "emitter") {
		t.Errorf("the anomaly must point at the emitter, not at the rows; got %q", found.Message)
	}
}

// TestScanQuietWhenNothingStranded keeps the anomaly from becoming noise. It
// fires on a leak, not on a healthy town: measured across every production
// database on 2026-08-24 it matched a single wisp.
//
// It proves nothing on its own — it passes just as happily when the probe is
// broken and reports zero for everything, which is exactly what happens if the
// query is answered by the wrong arm of the fake driver. Read it only alongside
// TestScanFlagsStrandedMolecules, which is the half that can fail.
func TestScanQuietWhenNothingStranded(t *testing.T) {
	now := time.Now().UTC()
	state := &fakeReaperState{
		wisps: map[string]*fakeWisp{
			"mol-assigned": {id: "mol-assigned", status: "hooked", issueType: "molecule", createdAt: now.Add(-4 * time.Hour), assignee: "deacon/dogs/alpha"},
			"mol-fresh":    {id: "mol-fresh", status: "open", issueType: "molecule", createdAt: now.Add(-1 * time.Minute)},
		},
		ops: map[int][]string{},
	}
	db := openFakeReaperDB(t, state)
	t.Cleanup(func() { _ = db.Close() })

	scan, err := Scan(db, "testdb", 24*time.Hour, 7*24*time.Hour, 7*24*time.Hour, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, a := range scan.Anomalies {
		if a.Type == "stranded_molecules" {
			t.Errorf("healthy town must not raise stranded_molecules: %q", a.Message)
		}
	}
}

// TestScanIgnoresAttachedMolecules is the gt-id8x regression: a molecule that
// was dispatched by ATTACHMENT must not be called stranded.
//
// The old predicate read wisps.assignee and nothing else, so it answered "does
// this row carry an assignee" for a question that was "was this dispatched".
// Those come apart for the whole mol-polecat-work family: gt sling writes the
// dispatch onto the hook bead as "attached_molecule: <id>", the molecule wisp
// keeps a NULL assignee for its entire working life, and a root-only molecule
// materializes no child wisps to carry one either. Every in-flight polecat older
// than an hour therefore matched, and the scan escalated five times in one day
// about work that was proceeding normally.
//
// Verified against the live gastown database while this fix was being written:
// this polecat's own molecule (gt-wisp-roivi, open, assignee NULL, attached to
// gt-id8x which was hooked to gastown/polecats/brahmin) was the sole row the old
// predicate returned. That is the empirical observation the bead's scope note
// said was missing.
//
// Each case here must be able to FAIL: the "stranded" fixture in every one of
// them is identical to the attached fixture except for the dispatch record, so a
// probe that stopped looking at attachments would flag two and a probe that
// excluded everything would flag none.
func TestScanIgnoresAttachedMolecules(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-4 * time.Hour)

	tests := []struct {
		name   string
		issues map[string]*fakeIssue
		wisps  map[string]*fakeWisp
	}{
		{
			// The shape that produced gt-id8x: a base bead in `issues`.
			name: "hook bead is an issue",
			issues: map[string]*fakeIssue{
				"gt-id8x": {id: "gt-id8x", description: "attached_molecule: mol-dispatched\nattached_formula: mol-polecat-work\nattached_at: 2026-08-25T23:41:04Z"},
			},
		},
		{
			// A hook bead can itself be a wisp, so the dispatch record can live in
			// either table. Checking only `issues` would leave this one flagged.
			name: "hook bead is a wisp",
			wisps: map[string]*fakeWisp{
				"hq-wisp-hook": {id: "hq-wisp-hook", status: "hooked", issueType: "task", createdAt: old, description: "attached_molecule: mol-dispatched"},
			},
		},
		{
			// Key aliases beads.ParseAttachmentFields accepts. The writer only ever
			// emits the underscore form, but the reader must not be narrower than
			// the parser the rest of gastown reads attachments with.
			name: "hyphenated key",
			issues: map[string]*fakeIssue{
				"gt-id8x": {id: "gt-id8x", description: "Attached-Molecule: mol-dispatched"},
			},
		},
		{
			// The hook bead has finished. The molecule is a leftover, not something
			// that was never dispatched — so the "no dispatch record" claim is still
			// false and this must not be reported under that heading.
			name: "hook bead already closed",
			issues: map[string]*fakeIssue{
				"gt-id8x": {id: "gt-id8x", description: "attached_molecule: mol-dispatched"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wisps := map[string]*fakeWisp{
				// Dispatched by attachment: open, NULL assignee, hours old. Healthy.
				"mol-dispatched": {id: "mol-dispatched", status: "open", issueType: "molecule", createdAt: old},
				// Identical but for the dispatch record. This one IS stranded, and it
				// is the control that proves the case can fail: if the probe went
				// silent altogether, the anomaly would disappear and this test with it.
				"mol-stranded": {id: "mol-stranded", status: "open", issueType: "molecule", createdAt: old},
			}
			for id, w := range tc.wisps {
				wisps[id] = w
			}
			state := &fakeReaperState{wisps: wisps, issues: tc.issues, ops: map[int][]string{}}
			db := openFakeReaperDB(t, state)
			t.Cleanup(func() { _ = db.Close() })

			scan, err := Scan(db, "testdb", 24*time.Hour, 7*24*time.Hour, 7*24*time.Hour, 30*24*time.Hour)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}

			var found *Anomaly
			for i := range scan.Anomalies {
				if scan.Anomalies[i].Type == "stranded_molecules" {
					found = &scan.Anomalies[i]
				}
			}
			if found == nil {
				t.Fatalf("mol-stranded has no dispatch record and must still be reported; anomalies: %+v", scan.Anomalies)
			}
			if found.Count != 1 {
				t.Errorf("stranded_molecules Count = %d, want 1: only mol-stranded qualifies — mol-dispatched was dispatched by attachment, which is what %q means", found.Count, found.Message)
			}
		})
	}
}

// TestStrandedMessageStatesWhatWasMeasured pins the message to its evidence.
//
// The old text asserted "poured but never dispatched" and told the reader to
// "find the emitter" — a cause the check never tested and a hunt with no quarry,
// since the emitter of an attached molecule is the ordinary gt mol attach path.
// Three agents spent cycles on that sentence before anyone read the query. A
// message may name what it measured; it may not name what it inferred.
func TestStrandedMessageStatesWhatWasMeasured(t *testing.T) {
	now := time.Now().UTC()
	state := &fakeReaperState{
		wisps: map[string]*fakeWisp{
			"mol-stranded": {id: "mol-stranded", status: "open", issueType: "molecule", createdAt: now.Add(-4 * time.Hour)},
		},
		ops: map[int][]string{},
	}
	db := openFakeReaperDB(t, state)
	t.Cleanup(func() { _ = db.Close() })

	scan, err := Scan(db, "testdb", 24*time.Hour, 7*24*time.Hour, 7*24*time.Hour, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var msg string
	for _, a := range scan.Anomalies {
		if a.Type == "stranded_molecules" {
			msg = a.Message
		}
	}
	if msg == "" {
		t.Fatalf("expected a stranded_molecules anomaly, got %+v", scan.Anomalies)
	}
	if strings.Contains(msg, "never dispatched") {
		t.Errorf("the check cannot observe that a molecule was never dispatched, only that no dispatch record exists; got %q", msg)
	}
	for _, want := range []string{"no assignee", "attached to no hook bead"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message must name what it measured (%q); got %q", want, msg)
		}
	}
}

// TestParseAttachedMoleculeIDMatchesBeads is the anti-drift control for the one
// piece of beads logic this package reimplements.
//
// internal/reaper deliberately imports nothing from gastown, so the attachment
// parser here is a copy. A copy that drifts is worse than no check at all: it
// would silently start disagreeing with `gt mol attachment` about which
// molecules are dispatched, and the disagreement would surface as the same false
// anomaly this fix removes. Every case below is run through both.
func TestParseAttachedMoleculeIDMatchesBeads(t *testing.T) {
	descriptions := []string{
		"",
		"no fields here at all",
		"attached_molecule: gt-wisp-roivi",
		"attached_molecule: gt-wisp-roivi\nattached_formula: mol-polecat-work\nattached_at: 2026-08-25T23:41:04Z",
		"mode: ralph\nattached-molecule: gt-wisp-abc\nconvoy_id: hq-cv-xyz",
		"AttachedMolecule: gt-wisp-caps",
		"  attached_molecule:   gt-wisp-padded  ",
		"attached_molecule:",
		"attached_molecule: \n",
		"attached_molecule: gt-wisp-first\nattached_molecule: gt-wisp-last",
		"attached_formula: mol-polecat-work\nno molecule attached",
		"The attached molecule was gt-wisp-prose, with no colon on that line.",
	}
	for _, desc := range descriptions {
		want := ""
		if fields := beads.ParseAttachmentFields(&beads.Issue{Description: desc}); fields != nil {
			want = fields.AttachedMolecule
		}
		if got := parseAttachedMoleculeID(desc); got != want {
			t.Errorf("parseAttachedMoleculeID(%q) = %q, beads.ParseAttachmentFields = %q", desc, got, want)
		}
	}
}
