package reaper

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

// These tests run the reaper's real SQL against a real engine (see
// fixture_test.go). Every other test in this package asserts that a guard's
// SQL TEXT is present; none of them can fail when the guard is present but
// inert, which is exactly how gt-caq reached two incidents (2026-08-10 and
// 2026-08-17) with a green suite. Each test below therefore carries a
// same-age NEGATIVE CONTROL: a bead the sweep must close. Without it a sweep
// that does nothing at all would pass.

const (
	staleAge = 7 * 24 * time.Hour
	purgeAge = 7 * 24 * time.Hour
)

// TestAutoCloseExemptsAgentBeadsBehaviour is the acceptance test for gt-am7.
//
// AGENT BEADS are the town's per-role identity rows (hq-mayor, bd-beads-witness,
// ...). `gt agents resolve` answers "no agent bead found" once one is CLOSED,
// which halts mol-witness-patrol — the layer that would notice a stalled town is
// the layer the sweep disables. They are issue_type 'task' with an empty
// role_type, so the gt:agent LABEL is the only populated discriminator.
//
// The control bead is stale by the SAME margin and differs from the agent bead
// only in that label. A guard that stops discriminating fails here; so does an
// AutoClose that has stopped closing anything.
func TestAutoCloseExemptsAgentBeadsBehaviour(t *testing.T) {
	f := newFixture(t, "autoclose_agent")
	stale := time.Now().UTC().Add(-30 * 24 * time.Hour)

	f.insertIssues(t,
		issueRow{id: "hq-mayor", title: "Mayor", priority: 2, updatedAt: stale, labels: []string{"gt:agent"}},
		issueRow{id: "hq-plain", title: "Ordinary stale bead", priority: 2, updatedAt: stale},
	)

	result, err := AutoClose(f.db, f.dbName, staleAge, false)
	if err != nil {
		t.Fatalf("AutoClose: %v", err)
	}

	if got := f.issueStatus(t, "hq-mayor"); got != "open" {
		t.Errorf("agent bead hq-mayor status = %q, want open — the gt:agent exemption is inert", got)
	}
	if got := f.issueStatus(t, "hq-plain"); got != "closed" {
		t.Errorf("control bead hq-plain status = %q, want closed — AutoClose closed nothing, so the "+
			"exemption above proves nothing", got)
	}
	if result.Closed != 1 {
		t.Errorf("Closed = %d, want 1", result.Closed)
	}
	if got := closedEntryIDs(result); !reflect.DeepEqual(got, []string{"hq-plain"}) {
		t.Errorf("ClosedEntries = %v, want [hq-plain]", got)
	}
	if got := f.issueCloseReason(t, "hq-plain"); got != "stale:auto-closed by reaper" {
		t.Errorf("close_reason = %q, want %q", got, "stale:auto-closed by reaper")
	}
	if len(result.Anomalies) != 0 {
		t.Errorf("unexpected anomalies: %+v", result.Anomalies)
	}
	if commits := f.doltCommitMessages(); len(commits) != 1 {
		t.Errorf("DOLT_COMMIT calls = %v, want exactly one", commits)
	}
}

// TestAutoCloseExemptionMatrix pins every AutoClose exemption to an observed
// outcome rather than to the presence of a clause. The stale/fresh split and
// the dependency wiring are all set from the same "now", so a single fixture
// distinguishes each exemption from the plain stale case.
func TestAutoCloseExemptionMatrix(t *testing.T) {
	f := newFixture(t, "autoclose_matrix")
	now := time.Now().UTC()
	stale := now.Add(-30 * 24 * time.Hour)
	fresh := now.Add(-1 * time.Hour)

	f.insertIssues(t,
		// Negative controls — these MUST close.
		issueRow{id: "close-open", priority: 2, updatedAt: stale},
		issueRow{id: "close-in-progress", status: "in_progress", priority: 2, updatedAt: stale},
		issueRow{id: "close-p4", priority: 4, updatedAt: stale},
		issueRow{id: "close-blocked-by-closed", priority: 2, updatedAt: stale},
		issueRow{id: "close-bug-type", issueType: "bug", priority: 2, updatedAt: stale},

		// Label exemptions.
		issueRow{id: "keep-agent", priority: 2, updatedAt: stale, labels: []string{"gt:agent"}},
		issueRow{id: "keep-standing-orders", priority: 2, updatedAt: stale, labels: []string{"gt:standing-orders"}},
		issueRow{id: "keep-keep", priority: 2, updatedAt: stale, labels: []string{"gt:keep"}},
		issueRow{id: "keep-role", priority: 2, updatedAt: stale, labels: []string{"gt:role"}},
		issueRow{id: "keep-rig", priority: 2, updatedAt: stale, labels: []string{"gt:rig"}},
		issueRow{id: "keep-mail", priority: 2, updatedAt: stale, labels: []string{"gt:message"}},

		// Priority exemptions: P0/P1 are never stale-closed.
		issueRow{id: "keep-p0", priority: 0, updatedAt: stale},
		issueRow{id: "keep-p1", priority: 1, updatedAt: stale},

		// Type exemptions. Convoys are excluded because their lifecycle is
		// tracked-bead driven and the 'tracks' relation does not block (hq-jnap).
		issueRow{id: "keep-epic", issueType: "epic", priority: 2, updatedAt: stale},
		issueRow{id: "keep-convoy", issueType: "convoy", priority: 2, updatedAt: stale},

		// Recency: not stale yet.
		issueRow{id: "keep-fresh", priority: 2, updatedAt: fresh},

		// Dependency exemptions.
		issueRow{id: "keep-blocked-by-open", priority: 2, updatedAt: stale},
		issueRow{id: "keep-blocks-open", priority: 2, updatedAt: stale},
		issueRow{id: "open-blocker", priority: 2, updatedAt: fresh},
		issueRow{id: "open-dependent", priority: 2, updatedAt: fresh},
		issueRow{id: "closed-blocker", status: "closed", priority: 2, updatedAt: stale},
	)

	// keep-blocked-by-open waits on an open issue; close-blocked-by-closed waits
	// on a closed one and must still be swept.
	f.insertDependency(t, "d1", "keep-blocked-by-open", "open-blocker", "blocks")
	f.insertDependency(t, "d2", "close-blocked-by-closed", "closed-blocker", "blocks")
	// An open issue depends on keep-blocks-open, so closing it would strand the
	// dependent.
	f.insertDependency(t, "d3", "open-dependent", "keep-blocks-open", "blocks")

	wantClosed := []string{
		"close-blocked-by-closed",
		"close-bug-type",
		"close-in-progress",
		"close-open",
		"close-p4",
	}

	dryRun, err := AutoClose(f.db, f.dbName, staleAge, true)
	if err != nil {
		t.Fatalf("dry-run AutoClose: %v", err)
	}
	if got := closedEntryIDs(dryRun); !reflect.DeepEqual(got, wantClosed) {
		t.Errorf("dry-run candidates = %v, want %v", got, wantClosed)
	}
	if got := f.closedIssueIDs(t); !reflect.DeepEqual(got, []string{"closed-blocker"}) {
		t.Errorf("dry-run mutated the database: closed = %v", got)
	}

	result, err := AutoClose(f.db, f.dbName, staleAge, false)
	if err != nil {
		t.Fatalf("AutoClose: %v", err)
	}
	if got := closedEntryIDs(result); !reflect.DeepEqual(got, wantClosed) {
		t.Errorf("ClosedEntries = %v, want %v", got, wantClosed)
	}

	// closed-blocker was already closed before the sweep.
	wantAfter := append([]string{"closed-blocker"}, wantClosed...)
	sort.Strings(wantAfter)
	if got := f.closedIssueIDs(t); !reflect.DeepEqual(got, wantAfter) {
		t.Errorf("closed issues after AutoClose = %v, want %v", got, wantAfter)
	}
}

// TestPurgeClosedWispsBehaviour covers the reaper's only unrecoverable
// operation: purge DELETEs rows. The controls are a recently-closed wisp and a
// stale OPEN wisp, both of which must survive — an over-broad DELETE takes them
// with it — plus the auxiliary and reverse-dependency rows of the survivor,
// which must not be swept up with the purged wisp's.
func TestPurgeClosedWispsBehaviour(t *testing.T) {
	f := newFixture(t, "purge_wisps")
	now := time.Now().UTC()
	oldClose := now.Add(-30 * 24 * time.Hour)
	recentClose := now.Add(-1 * time.Hour)

	f.insertWisps(t,
		wispRow{id: "w-purge", status: "closed", wispType: "step", createdAt: oldClose, closedAt: &oldClose},
		wispRow{id: "w-recent", status: "closed", wispType: "step", createdAt: oldClose, closedAt: &recentClose},
		wispRow{id: "w-open", status: "open", wispType: "step", createdAt: oldClose},
		wispRow{id: "w-hooked", status: "hooked", wispType: "step", createdAt: oldClose},
	)

	// Auxiliary rows: the purged wisp's must go, the survivors' must stay.
	for _, id := range []string{"w-purge", "w-recent", "w-open"} {
		f.insertWispAux(t, id)
	}
	// Forward dependency owned by the purged wisp.
	f.insertWispDependency(t, "wd-forward", "w-purge", "w-open", "", "parent-child")
	// Reverse dependency POINTING AT the purged wisp — left behind, this is the
	// dangling_parent_ref anomaly Scan reports.
	f.insertWispDependency(t, "wd-reverse", "w-open", "w-purge", "", "parent-child")
	// Unrelated dependency between two survivors.
	f.insertWispDependency(t, "wd-keep", "w-open", "w-recent", "", "parent-child")

	dryRun, err := Purge(f.db, f.dbName, purgeAge, purgeAge, true)
	if err != nil {
		t.Fatalf("dry-run Purge: %v", err)
	}
	if dryRun.WispsPurged != 1 {
		t.Errorf("dry-run WispsPurged = %d, want 1", dryRun.WispsPurged)
	}
	if got := f.ids(t, "wisps"); len(got) != 4 {
		t.Errorf("dry-run deleted wisps: %v", got)
	}

	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if result.WispsPurged != 1 {
		t.Errorf("WispsPurged = %d, want 1", result.WispsPurged)
	}
	if len(result.Anomalies) != 0 {
		t.Errorf("unexpected anomalies: %+v", result.Anomalies)
	}

	wantWisps := []string{"w-hooked", "w-open", "w-recent"}
	if got := f.ids(t, "wisps"); !reflect.DeepEqual(got, wantWisps) {
		t.Errorf("surviving wisps = %v, want %v", got, wantWisps)
	}

	wantAux := []string{"w-open", "w-recent"}
	for _, table := range []string{"wisp_labels", "wisp_comments", "wisp_events"} {
		if got := f.issueIDs(t, table); !reflect.DeepEqual(got, wantAux) {
			t.Errorf("%s issue_ids = %v, want %v", table, got, wantAux)
		}
	}

	if got := f.ids(t, "wisp_dependencies"); !reflect.DeepEqual(got, []string{"wd-keep"}) {
		t.Errorf("wisp_dependencies = %v, want [wd-keep] — the purged wisp's forward dependency and "+
			"the reverse reference to it must both be cleaned up", got)
	}

	if commits := f.doltCommitMessages(); len(commits) != 1 {
		t.Errorf("DOLT_COMMIT calls = %v, want exactly one", commits)
	}
}

// TestPurgeClosedWispsBatchesBeyondLimit drives the purge loop past
// DefaultBatchSize. The batch SELECT carries a LIMIT, so a loop that stopped
// after one pass would leave rows behind and a loop that failed to shrink its
// candidate set would never terminate.
func TestPurgeClosedWispsBatchesBeyondLimit(t *testing.T) {
	f := newFixture(t, "purge_batches")
	now := time.Now().UTC()
	oldClose := now.Add(-30 * 24 * time.Hour)

	total := DefaultBatchSize*2 + 7
	rows := make([]wispRow, 0, total+1)
	for i := 0; i < total; i++ {
		rows = append(rows, wispRow{
			id:        fmt.Sprintf("w-old-%04d", i),
			status:    "closed",
			createdAt: oldClose,
			closedAt:  &oldClose,
		})
	}
	rows = append(rows, wispRow{id: "w-survivor", status: "open", createdAt: oldClose})
	f.insertWisps(t, rows...)

	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if result.WispsPurged != total {
		t.Errorf("WispsPurged = %d, want %d", result.WispsPurged, total)
	}
	if got := f.ids(t, "wisps"); !reflect.DeepEqual(got, []string{"w-survivor"}) {
		t.Errorf("surviving wisps = %v, want [w-survivor]", got)
	}
}

// TestPurgeOldMailBehaviour covers the other unrecoverable delete: mail beads
// leave the database entirely. The controls are a recently-closed mail bead and
// an OLD CLOSED NON-MAIL bead — the second is what separates "deletes closed
// mail" from "deletes closed issues".
func TestPurgeOldMailBehaviour(t *testing.T) {
	f := newFixture(t, "purge_mail")
	now := time.Now().UTC()
	oldClose := now.Add(-30 * 24 * time.Hour)
	recentClose := now.Add(-1 * time.Hour)

	f.insertIssues(t,
		issueRow{id: "mail-old", status: "closed", priority: 2, updatedAt: oldClose, closedAt: &oldClose, labels: []string{"gt:message"}},
		issueRow{id: "mail-recent", status: "closed", priority: 2, updatedAt: oldClose, closedAt: &recentClose, labels: []string{"gt:message"}},
		issueRow{id: "issue-old-closed", status: "closed", priority: 2, updatedAt: oldClose, closedAt: &oldClose},
		issueRow{id: "mail-open", priority: 2, updatedAt: oldClose, labels: []string{"gt:message"}},
	)

	dryRun, err := Purge(f.db, f.dbName, purgeAge, purgeAge, true)
	if err != nil {
		t.Fatalf("dry-run Purge: %v", err)
	}
	if dryRun.MailPurged != 1 {
		t.Errorf("dry-run MailPurged = %d, want 1", dryRun.MailPurged)
	}
	if got := f.ids(t, "issues"); len(got) != 4 {
		t.Errorf("dry-run deleted issues: %v", got)
	}

	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if result.MailPurged != 1 {
		t.Errorf("MailPurged = %d, want 1", result.MailPurged)
	}

	want := []string{"issue-old-closed", "mail-open", "mail-recent"}
	if got := f.ids(t, "issues"); !reflect.DeepEqual(got, want) {
		t.Errorf("surviving issues = %v, want %v", got, want)
	}
	if got := f.issueIDs(t, "labels"); !reflect.DeepEqual(got, []string{"mail-open", "mail-recent"}) {
		t.Errorf("labels issue_ids = %v, want [mail-open mail-recent]", got)
	}
	for _, table := range []string{"comments", "events"} {
		if got := f.issueIDs(t, table); !reflect.DeepEqual(got, want) {
			t.Errorf("%s issue_ids = %v, want %v", table, got, want)
		}
	}
}

// TestUnreadMailSurvivesAutoCloseThenPurge is the acceptance test for gt-jbn.
//
// Reading a message closes its bead, so an OPEN gt:message bead is unread mail.
// Before the fix, staleness auto-close closed it and stamped closed_at, and the
// mail purge deleted it mailDeleteAge later — so the full sweep silently
// destroyed exactly the messages nobody had read, on the channel CLAUDE.md
// tells agents to use when a message must survive session death.
//
// The test runs the sweep the Dog runs, in order, against a mail bead old
// enough for both windows. Controls: a same-age plain bead must still close
// (an AutoClose that stopped closing anything would pass otherwise), and a
// READ (closed) mail bead of the same age must still be purged (retention on
// read mail is deliberate and must not be disabled by the exemption).
func TestUnreadMailSurvivesAutoCloseThenPurge(t *testing.T) {
	f := newFixture(t, "unread_mail")
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)

	f.insertIssues(t,
		issueRow{id: "mail-unread", title: "HELP: auth bug", priority: 2, updatedAt: old, labels: []string{"gt:message"}},
		issueRow{id: "mail-read", title: "Re: auth bug", status: "closed", priority: 2, updatedAt: old, closedAt: &old, labels: []string{"gt:message"}},
		issueRow{id: "plain-stale", title: "Ordinary stale bead", priority: 2, updatedAt: old},
	)

	autoClose, err := AutoClose(f.db, f.dbName, staleAge, false)
	if err != nil {
		t.Fatalf("AutoClose: %v", err)
	}
	if got := closedEntryIDs(autoClose); !reflect.DeepEqual(got, []string{"plain-stale"}) {
		t.Errorf("ClosedEntries = %v, want [plain-stale] — unread mail must not be stale-closed", got)
	}
	if got := f.issueStatus(t, "mail-unread"); got != "open" {
		t.Errorf("unread mail status = %q, want open — the gt:message exemption is inert", got)
	}

	// Scan must report the same candidate set AutoClose acts on, or the Dog
	// decides from a count that includes beads the sweep will never touch.
	scan, err := Scan(f.db, f.dbName, purgeAge, purgeAge, purgeAge, staleAge)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scan.StaleCandidates != 0 {
		t.Errorf("StaleCandidates after sweep = %d, want 0 — scan counts beads AutoClose skips", scan.StaleCandidates)
	}

	purge, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if purge.MailPurged != 1 {
		t.Errorf("MailPurged = %d, want 1 — read mail must still age out", purge.MailPurged)
	}

	want := []string{"mail-unread", "plain-stale"}
	if got := f.ids(t, "issues"); !reflect.DeepEqual(got, want) {
		t.Errorf("surviving issues = %v, want %v", got, want)
	}
}

// TestClosePluginReceiptsBehaviour pins the label-and-age predicate for plugin
// run receipts. The control is an old open bead WITHOUT the label: closing it
// too would mean the sweep is closing on age alone.
func TestClosePluginReceiptsBehaviour(t *testing.T) {
	f := newFixture(t, "plugin_receipts")
	now := time.Now().UTC()
	old := now.Add(-6 * time.Hour)
	recent := now.Add(-5 * time.Minute)

	f.insertIssues(t,
		issueRow{id: "receipt-old", priority: 2, updatedAt: old, labels: []string{"type:plugin-run"}},
		issueRow{id: "receipt-recent", priority: 2, updatedAt: recent, labels: []string{"type:plugin-run"}},
		issueRow{id: "receipt-closed", status: "closed", priority: 2, updatedAt: old, labels: []string{"type:plugin-run"}},
		issueRow{id: "plain-old", priority: 2, updatedAt: old},
	)

	maxAge := time.Hour
	dryRun, err := ClosePluginReceipts(f.db, f.dbName, maxAge, true)
	if err != nil {
		t.Fatalf("dry-run ClosePluginReceipts: %v", err)
	}
	if dryRun.Closed != 1 {
		t.Errorf("dry-run Closed = %d, want 1", dryRun.Closed)
	}
	if got := f.closedIssueIDs(t); !reflect.DeepEqual(got, []string{"receipt-closed"}) {
		t.Errorf("dry-run mutated the database: closed = %v", got)
	}

	result, err := ClosePluginReceipts(f.db, f.dbName, maxAge, false)
	if err != nil {
		t.Fatalf("ClosePluginReceipts: %v", err)
	}
	if result.Closed != 1 {
		t.Errorf("Closed = %d, want 1", result.Closed)
	}
	if len(result.Anomalies) != 0 {
		t.Errorf("unexpected anomalies: %+v", result.Anomalies)
	}
	want := []string{"receipt-closed", "receipt-old"}
	if got := f.closedIssueIDs(t); !reflect.DeepEqual(got, want) {
		t.Errorf("closed issues = %v, want %v", got, want)
	}
}

// TestClosePluginDispatchesBehaviour pins the three-way predicate (both labels
// plus the "Plugin:" title prefix) and, with it, the doubled %% in that LIKE
// pattern — a Sprintf format string whose escaping no source scan can check.
func TestClosePluginDispatchesBehaviour(t *testing.T) {
	f := newFixture(t, "plugin_dispatches")
	now := time.Now().UTC()
	old := now.Add(-6 * time.Hour)
	recent := now.Add(-5 * time.Minute)

	daemonMail := []string{"gt:message", "from:daemon"}
	f.insertIssues(t,
		issueRow{id: "dispatch-old", title: "Plugin: stuck-agent-dog", priority: 2, updatedAt: old, labels: daemonMail},
		issueRow{id: "dispatch-recent", title: "Plugin: stuck-agent-dog", priority: 2, updatedAt: recent, labels: daemonMail},
		// Same labels, but not a plugin dispatch.
		issueRow{id: "daemon-mail-old", title: "Nudge: witness", priority: 2, updatedAt: old, labels: daemonMail},
		// Right title and age, but not from the daemon.
		issueRow{id: "human-plugin-old", title: "Plugin: written by a person", priority: 2, updatedAt: old, labels: []string{"gt:message"}},
	)

	result, err := ClosePluginDispatches(f.db, f.dbName, time.Hour, false)
	if err != nil {
		t.Fatalf("ClosePluginDispatches: %v", err)
	}
	if result.Closed != 1 {
		t.Errorf("Closed = %d, want 1", result.Closed)
	}
	if got := f.closedIssueIDs(t); !reflect.DeepEqual(got, []string{"dispatch-old"}) {
		t.Errorf("closed issues = %v, want [dispatch-old]", got)
	}
}

// TestReapExcludesAgentBeadsBehaviour replaces the assertion that
// TestReapExcludesAgentBeads never made: it asserts nothing at all, having
// deferred the check to an integration test that was never written. The wisp
// reaper's issue_type='agent' guard is checked here against the engine, with a
// same-age orphan as the control.
func TestReapExcludesAgentBeadsBehaviour(t *testing.T) {
	f := newFixture(t, "reap_agent")
	stale := time.Now().UTC().Add(-48 * time.Hour)

	f.insertWisps(t,
		wispRow{id: "w-agent", status: "open", issueType: "agent", createdAt: stale},
		wispRow{id: "w-orphan", status: "open", issueType: "task", createdAt: stale},
	)

	result, err := Reap(f.db, f.dbName, 24*time.Hour, false)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if result.Reaped != 1 {
		t.Errorf("Reaped = %d, want 1", result.Reaped)
	}
	if got := f.wispStatus(t, "w-agent"); got != "open" {
		t.Errorf("agent wisp status = %q, want open — the issue_type != 'agent' guard is inert", got)
	}
	if got := f.wispStatus(t, "w-orphan"); got != "closed" {
		t.Errorf("control wisp status = %q, want closed — Reap closed nothing, so the exemption "+
			"above proves nothing", got)
	}
	if result.OpenRemain != 1 {
		t.Errorf("OpenRemain = %d, want 1", result.OpenRemain)
	}
}

// oneShotCandidates supplies a batch of candidate IDs exactly once, then
// reports an empty set. closeWispsInBatches loops until its ID query comes back
// empty, and the UPDATE it issues deliberately does NOT change the rows the
// guard protects — so a candidate query that keeps offering an agent wisp would
// offer it forever. In production the loop terminates only because the
// candidate query filters agent wisps itself (reaper.go:465). That is precisely
// the filtering this test has to bypass, so termination is arranged here
// instead.
//
// Only the SUPPLY of candidates is controlled. ExecContext and QueryRowContext
// pass straight through, so the UPDATE under test is the real one, built by
// production code and evaluated by the real engine.
type oneShotCandidates struct {
	runner sqlRunner
	served bool
}

func (o *oneShotCandidates) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	if o.served {
		return o.runner.QueryContext(ctx, "SELECT id FROM wisps WHERE 1 = 0")
	}
	o.served = true
	return o.runner.QueryContext(ctx, query, args...)
}

func (o *oneShotCandidates) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return o.runner.ExecContext(ctx, query, args...)
}

func (o *oneShotCandidates) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return o.runner.QueryRowContext(ctx, query, args...)
}

// TestCloseWispsInBatchesExcludesAgentBeads is the acceptance test for gt-axe:
// the destructive UPDATE at reaper.go:553 had no behavioural coverage, proven by
// mutation — deleting its `AND issue_type != 'agent'` clause left the entire
// suite green, including TestReapExcludesAgentBeadsBehaviour.
//
// The reason that mutation is invisible from Reap() is structural, not an
// oversight in the test above it: every candidate query feeding this UPDATE
// (reaper.go:465 for stale wisps, :464 for molecule steps) already filters
// agent wisps, so an agent ID never reaches the batch by that route and no
// input to Reap can make it. The UPDATE's own clause is the second line of a
// defence-in-depth pair, and a second line is only load-bearing when the first
// one lets something through. This test is the only place that can observe it,
// so it hands the UPDATE the batch directly.
//
// The same-age control is what makes a surviving agent wisp mean anything: an
// UPDATE that closed nothing at all — wrong table, wrong status set, a WHERE
// that matches no row — would otherwise look identical to a working guard.
func TestCloseWispsInBatchesExcludesAgentBeads(t *testing.T) {
	f := newFixture(t, "reap_batch_agent")
	stale := time.Now().UTC().Add(-48 * time.Hour)

	f.insertWisps(t,
		wispRow{id: "w-agent", status: "open", issueType: "agent", createdAt: stale},
		wispRow{id: "w-orphan", status: "open", issueType: "task", createdAt: stale},
	)

	// Deliberately unguarded: this is the compromised first line of defence the
	// UPDATE's clause exists to survive.
	idQuery := "SELECT id FROM wisps WHERE status IN ('open', 'hooked', 'in_progress') ORDER BY id"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runner := &oneShotCandidates{runner: f.db}
	closed, err := closeWispsInBatches(ctx, runner, idQuery, nil, "gt-axe agent guard")
	if err != nil {
		t.Fatalf("closeWispsInBatches: %v", err)
	}

	if got := f.wispStatus(t, "w-agent"); got != "open" {
		t.Errorf("agent wisp status = %q, want open — the issue_type != 'agent' guard on the "+
			"destructive UPDATE (reaper.go:553) is inert; an agent ID reaching a batch is closed", got)
	}
	if got := f.wispStatus(t, "w-orphan"); got != "closed" {
		t.Errorf("control wisp status = %q, want closed — the UPDATE closed nothing, so the agent "+
			"wisp surviving above proves nothing", got)
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1 (the control only)", closed)
	}
}

func closedEntryIDs(result *AutoCloseResult) []string {
	ids := make([]string, 0, len(result.ClosedEntries))
	for _, entry := range result.ClosedEntries {
		ids = append(ids, entry.ID)
	}
	sort.Strings(ids)
	return ids
}
