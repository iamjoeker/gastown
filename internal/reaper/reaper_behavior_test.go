package reaper

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
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

// TestAutoCloseAckedMailBehaviour is the acceptance test for gt-ljun.
//
// `gt mail check --inject` writes `delivery:acked` on every unread message
// purely to record delivery, without reading or closing it. AutoClose's
// blanket gt:message exemption (AutoCloseExemptLabels) then keeps that bead
// open forever, inflating every ready-work count derived from status=open.
// AutoCloseAckedMail must close the acked-and-stale one while leaving a
// same-age mail bead that was never acked (still awaiting a reader) alone —
// that one really is "unread mail" in the sense the original exemption meant.
func TestAutoCloseAckedMailBehaviour(t *testing.T) {
	f := newFixture(t, "autoclose_acked_mail")
	stale := time.Now().UTC().Add(-30 * 24 * time.Hour)
	fresh := time.Now().UTC().Add(-1 * time.Hour)

	f.insertIssues(t,
		issueRow{id: "hq-acked-stale", priority: 2, updatedAt: stale, labels: []string{"gt:message", "delivery:acked"}},
		issueRow{id: "hq-unacked-stale", priority: 2, updatedAt: stale, labels: []string{"gt:message"}},
		issueRow{id: "hq-acked-fresh", priority: 2, updatedAt: fresh, labels: []string{"gt:message", "delivery:acked"}},
	)

	// The blanket sweep must still leave every gt:message bead alone —
	// AutoCloseAckedMail is a separate, narrower pass, not a replacement.
	autoCloseResult, err := AutoClose(f.db, f.dbName, staleAge, false)
	if err != nil {
		t.Fatalf("AutoClose: %v", err)
	}
	if autoCloseResult.Closed != 0 {
		t.Errorf("AutoClose.Closed = %d, want 0 — it must not touch gt:message beads itself", autoCloseResult.Closed)
	}

	result, err := AutoCloseAckedMail(f.db, f.dbName, staleAge, false)
	if err != nil {
		t.Fatalf("AutoCloseAckedMail: %v", err)
	}

	if got := f.issueStatus(t, "hq-acked-stale"); got != "closed" {
		t.Errorf("hq-acked-stale status = %q, want closed — acked-and-stale mail must close", got)
	}
	if got := f.issueStatus(t, "hq-unacked-stale"); got != "open" {
		t.Errorf("hq-unacked-stale status = %q, want open — never-acked mail is still unread and must stay open", got)
	}
	if got := f.issueStatus(t, "hq-acked-fresh"); got != "open" {
		t.Errorf("hq-acked-fresh status = %q, want open — not stale yet", got)
	}
	if got := closedEntryIDs(result); !reflect.DeepEqual(got, []string{"hq-acked-stale"}) {
		t.Errorf("ClosedEntries = %v, want [hq-acked-stale]", got)
	}
	if result.Closed != 1 {
		t.Errorf("Closed = %d, want 1", result.Closed)
	}
	if commits := f.doltCommitMessages(); len(commits) != 1 {
		t.Errorf("DOLT_COMMIT calls = %v, want exactly one", commits)
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

// TestPurgeProtectsMergeRequestWispsBehaviour is the acceptance test for gt-nmg.
//
// A gt:merge-request wisp CLOSED WITHOUT MERGING is the only record that the
// work did not land, and it carries the rejection rationale. Wisps are
// unversioned and unbacked, so deleting one is unrecoverable by any means the
// town has. On 2026-08-17 seven closed MR beads were deleted on the beads rig,
// including a rejection eleven minutes old (gt-6dp).
//
// This path is a NATIVE SQL DELETE — it is not the `bd purge` shell-out that
// gt-fdj bounded with --older-than, and it is not covered by whatever bd purge
// grows. Age alone was never the guard: a merge queue that stalls for a week
// produces a closed unmerged MR older than any purge_age we would set. That is
// what the w-mr-ancient row below stands for.
//
// The NEGATIVE CONTROL (w-ordinary) is closed by the SAME margin and differs
// only in its label. A purge that stopped deleting anything — the way a guard
// applied too broadly fails — leaves it behind and fails here.
func TestPurgeProtectsMergeRequestWispsBehaviour(t *testing.T) {
	f := newFixture(t, "purge_protect_mr")
	now := time.Now().UTC()
	oldClose := now.Add(-30 * 24 * time.Hour)
	ancientClose := now.Add(-365 * 24 * time.Hour)

	f.insertWisps(t,
		// Protected by type. Same age and status as the control.
		wispRow{id: "w-mr", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			labels: []string{"gt:merge-request"}},
		// A year past the cutoff: no age bound rescues this one, only the type does.
		wispRow{id: "w-mr-ancient", status: "closed", createdAt: ancientClose, closedAt: &ancientClose,
			labels: []string{"gt:merge-request"}},
		// Protected by the pinned column an incident responder sets by hand.
		wispRow{id: "w-pinned", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			pinned: boolPtr(true)},
		// Explicitly unpinned, and NULL-pinned: both must still be purged. The
		// NULL row is why the guard COALESCEs — `pinned = 0` alone would skip it
		// and quietly stop purging most of the table.
		wispRow{id: "w-unpinned", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			pinned: boolPtr(false)},
		wispRow{id: "w-null-pinned", status: "closed", createdAt: oldClose, closedAt: &oldClose},
		// NEGATIVE CONTROL: same age, same status, ordinary label.
		wispRow{id: "w-ordinary", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			labels: []string{"gt:wisp"}},
	)
	// Auxiliary rows for a protected wisp: protecting the wisp row while its
	// labels and comments are deleted would leave a husk, not a record.
	f.insertWispAux(t, "w-mr")

	scan, err := Scan(f.db, f.dbName, purgeAge, purgeAge, purgeAge, staleAge)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scan.PurgeCandidates != 3 {
		t.Errorf("Scan.PurgeCandidates = %d, want 3 (w-unpinned, w-null-pinned, w-ordinary) — "+
			"scan must count what purge will actually delete, or it advertises rows purge declines", scan.PurgeCandidates)
	}
	if scan.ProtectedFromPurge != 3 {
		t.Errorf("Scan.ProtectedFromPurge = %d, want 3 (w-mr, w-mr-ancient, w-pinned)", scan.ProtectedFromPurge)
	}

	dry, err := Purge(f.db, f.dbName, purgeAge, purgeAge, true)
	if err != nil {
		t.Fatalf("dry-run Purge: %v", err)
	}
	if dry.WispsPurged != 3 || dry.WispsProtected != 3 {
		t.Errorf("dry-run WispsPurged/WispsProtected = %d/%d, want 3/3", dry.WispsPurged, dry.WispsProtected)
	}

	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if result.WispsPurged != 3 {
		t.Errorf("WispsPurged = %d, want 3 — the control must still be deleted; a purge that "+
			"deletes nothing would satisfy every protection assertion below", result.WispsPurged)
	}
	if result.WispsProtected != 3 {
		t.Errorf("WispsProtected = %d, want 3 — the skip has to be reported, or a protected "+
			"purge is indistinguishable from a quiet one", result.WispsProtected)
	}

	wantWisps := []string{"w-mr", "w-mr-ancient", "w-pinned"}
	if got := f.ids(t, "wisps"); !reflect.DeepEqual(got, wantWisps) {
		t.Errorf("surviving wisps = %v, want %v", got, wantWisps)
	}

	// The protected wisp keeps its record, not just its row.
	for _, table := range []string{"wisp_labels", "wisp_comments", "wisp_events"} {
		found := false
		for _, id := range f.issueIDs(t, table) {
			if id == "w-mr" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s lost w-mr's rows — the wisp survived but its record did not", table)
		}
	}
}

// TestPurgeProtectionIsNotHardcodedToOneLabel guards the mechanism rather than
// the current list: protection is driven by ProtectedWispLabels, so a future
// entry takes effect everywhere without a second edit. A guard that inlined
// 'gt:merge-request' into the SQL passes the test above and fails this one.
func TestPurgeProtectionIsNotHardcodedToOneLabel(t *testing.T) {
	original := ProtectedWispLabels
	ProtectedWispLabels = append(append([]string{}, original...), "gt:test-protected")
	t.Cleanup(func() { ProtectedWispLabels = original })

	f := newFixture(t, "purge_protect_list")
	oldClose := time.Now().UTC().Add(-30 * 24 * time.Hour)
	f.insertWisps(t,
		wispRow{id: "w-added", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			labels: []string{"gt:test-protected"}},
		wispRow{id: "w-control", status: "closed", createdAt: oldClose, closedAt: &oldClose,
			labels: []string{"gt:wisp"}},
	)

	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if result.WispsPurged != 1 {
		t.Errorf("WispsPurged = %d, want 1 (the control) — a purge that deleted nothing "+
			"would pass the survival check below for the wrong reason", result.WispsPurged)
	}
	if got := f.ids(t, "wisps"); !reflect.DeepEqual(got, []string{"w-added"}) {
		t.Errorf("surviving wisps = %v, want [w-added] — adding a label to ProtectedWispLabels "+
			"must protect it, without editing any query", got)
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

// TestPurgeReportsDoltCommitFailureOnBothHalves is the acceptance test for
// gt-u5c: the mail half of Purge swallowed DOLT_COMMIT failure entirely, so the
// deletion could go unversioned while `gt reaper purge --json` reported a clean
// result. The operator check the purge step prescribes — "check for
// dolt_commit_failed anomalies" — could never fire on that half.
//
// Both halves are seeded and asserted together, because the defect was an
// ASYMMETRY: the wisp half already reported. A test that only injected the fault
// on the mail path could not tell "the mail path now reports" from "the
// injection reached the wisp path instead".
//
// Control: the same fixture shape without the injected failure must produce no
// anomalies. Without it, a purge that reported dolt_commit_failed
// unconditionally would pass.
func TestPurgeReportsDoltCommitFailureOnBothHalves(t *testing.T) {
	now := time.Now().UTC()
	oldClose := now.Add(-30 * 24 * time.Hour)

	seed := func(f *fixture) {
		f.insertWisps(t, wispRow{id: "w-old", status: "closed", wispType: "step", createdAt: oldClose, closedAt: &oldClose})
		f.insertIssues(t, issueRow{id: "mail-old", status: "closed", priority: 2,
			updatedAt: oldClose, closedAt: &oldClose, labels: []string{"gt:message"}})
	}

	// Control: DOLT_COMMIT succeeds — a clean purge must stay clean.
	ok := newFixture(t, "purge_commit_ok")
	seed(ok)
	okResult, err := Purge(ok.db, ok.dbName, purgeAge, purgeAge, false)
	if err != nil {
		t.Fatalf("control Purge: %v", err)
	}
	if len(okResult.Anomalies) != 0 {
		t.Fatalf("control purge reported anomalies: %+v", okResult.Anomalies)
	}
	if okResult.WispsPurged != 1 || okResult.MailPurged != 1 {
		t.Fatalf("control purged wisps=%d mail=%d, want 1 and 1 — the halves must both run",
			okResult.WispsPurged, okResult.MailPurged)
	}

	f := newFixture(t, "purge_commit_fail")
	seed(f)
	f.failDoltCommits(t)

	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}

	// The rows are gone and the SQL commit landed; only the versioning failed.
	// That is precisely why the anomaly is the sole signal available.
	if got := f.ids(t, "wisps"); len(got) != 0 {
		t.Errorf("surviving wisps = %v, want none", got)
	}
	if got := f.ids(t, "issues"); len(got) != 0 {
		t.Errorf("surviving issues = %v, want none", got)
	}
	if result.MailPurged != 1 {
		t.Errorf("MailPurged = %d, want 1", result.MailPurged)
	}

	var wispReported, mailReported bool
	for _, a := range result.Anomalies {
		if a.Type != "dolt_commit_failed" {
			continue
		}
		switch {
		case strings.Contains(a.Message, "mail"):
			mailReported = true
		default:
			wispReported = true
		}
	}
	if !wispReported {
		t.Errorf("wisp half reported no dolt_commit_failed anomaly: %+v", result.Anomalies)
	}
	if !mailReported {
		t.Errorf("mail half reported no dolt_commit_failed anomaly — the operator check is inert: %+v",
			result.Anomalies)
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
// reports an empty set. The UPDATE closeWispsInBatches issues deliberately does
// NOT change the rows the guard protects, so a candidate query that keeps
// offering an agent wisp keeps offering it after the UPDATE declines it. The
// no-progress break added for gt-m46 stops that loop, but termination here does
// not depend on it: cutting the supply keeps this test measuring the UPDATE's
// guard and nothing else. TestCloseWispsInBatchesStopsWithoutProgress covers
// the break itself.
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
	closed, stalled, err := closeWispsInBatches(ctx, runner, idQuery, nil, "gt-axe agent guard")
	if err != nil {
		t.Fatalf("closeWispsInBatches: %v", err)
	}
	if stalled != 0 {
		t.Errorf("stalled = %d, want 0 — the first batch closed the control, so it made progress", stalled)
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

// countingCandidates passes every statement straight through to the real
// engine and counts the candidate queries. An unguarded candidate query is
// naturally repeating — nothing the batch loop does changes which rows it
// selects — so no rigging is needed to reproduce the livelock; only the count
// is added, to tell "stopped because it noticed no progress" apart from
// "stopped for some unrelated reason".
type countingCandidates struct {
	runner sqlRunner
	calls  int
}

func (c *countingCandidates) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	c.calls++
	return c.runner.QueryContext(ctx, query, args...)
}

func (c *countingCandidates) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return c.runner.ExecContext(ctx, query, args...)
}

func (c *countingCandidates) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return c.runner.QueryRowContext(ctx, query, args...)
}

// TestCloseWispsInBatchesStopsWithoutProgress is the regression test for gt-m46:
// closeWispsInBatches terminated only on an empty candidate set, never on "the
// UPDATE closed nothing". A candidate query that offers a row the UPDATE's
// `issue_type != 'agent'` clause declines therefore produced a hot
// SELECT+UPDATE loop against Dolt until Reap's 2-minute context expired.
//
// Termination is what is under test, so a livelocking loop must FAIL rather
// than hang: the context below is short, and a spinning loop surfaces as a
// context error out of QueryContext instead of the nil error asserted here.
//
// The second case is the control. A break placed one line too early — before
// any UPDATE, or after the first iteration unconditionally — passes the first
// case while reaping nothing, which is how this guard would go inert.
func TestCloseWispsInBatchesStopsWithoutProgress(t *testing.T) {
	stale := time.Now().UTC().Add(-48 * time.Hour)
	// Deliberately unguarded, the way a de-duplicated candidate query would be:
	// this is the edit whose blast radius gt-m46 is about.
	const idQuery = "SELECT id FROM wisps WHERE status IN ('open', 'hooked', 'in_progress') ORDER BY id"

	t.Run("every candidate declined", func(t *testing.T) {
		f := newFixture(t, "reap_batch_stall")
		f.insertWisps(t,
			wispRow{id: "w-agent-1", status: "open", issueType: "agent", createdAt: stale},
			wispRow{id: "w-agent-2", status: "open", issueType: "agent", createdAt: stale},
		)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		runner := &countingCandidates{runner: f.db}
		closed, stalled, err := closeWispsInBatches(ctx, runner, idQuery, nil, "gt-m46 stall")
		if err != nil {
			t.Fatalf("closeWispsInBatches: %v — a batch the UPDATE declines must end the loop, "+
				"not spin until the context expires", err)
		}

		if runner.calls != 1 {
			t.Errorf("candidate queries = %d, want 1 — the loop re-selected a batch it had already "+
				"failed to close", runner.calls)
		}
		if closed != 0 {
			t.Errorf("closed = %d, want 0 — the UPDATE declines agent wisps", closed)
		}
		if stalled != 2 {
			t.Errorf("stalled = %d, want 2 — the declined batch must be reported, not silently dropped", stalled)
		}
		for _, id := range []string{"w-agent-1", "w-agent-2"} {
			if got := f.wispStatus(t, id); got != "open" {
				t.Errorf("agent wisp %s status = %q, want open", id, got)
			}
		}
	})

	t.Run("stops only after progress stops", func(t *testing.T) {
		f := newFixture(t, "reap_batch_stall_control")
		f.insertWisps(t,
			wispRow{id: "w-agent", status: "open", issueType: "agent", createdAt: stale},
			wispRow{id: "w-task", status: "open", issueType: "task", createdAt: stale},
		)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		runner := &countingCandidates{runner: f.db}
		closed, stalled, err := closeWispsInBatches(ctx, runner, idQuery, nil, "gt-m46 stall control")
		if err != nil {
			t.Fatalf("closeWispsInBatches: %v", err)
		}

		// First batch closes w-task; the second offers only w-agent, which the
		// UPDATE declines. Stopping before that second batch would mean the break
		// fires on any iteration, not on the absence of progress.
		if runner.calls != 2 {
			t.Errorf("candidate queries = %d, want 2 — the loop must keep going while rows are "+
				"still being closed", runner.calls)
		}
		if closed != 1 {
			t.Errorf("closed = %d, want 1 (the control wisp)", closed)
		}
		if stalled != 1 {
			t.Errorf("stalled = %d, want 1 (the declined agent wisp)", stalled)
		}
		if got := f.wispStatus(t, "w-task"); got != "closed" {
			t.Errorf("control wisp status = %q, want closed — the loop stopped before closing anything, "+
				"so the termination assertions above prove nothing", got)
		}
		if got := f.wispStatus(t, "w-agent"); got != "open" {
			t.Errorf("agent wisp status = %q, want open", got)
		}
	})
}

// TestWritePathsPinTheirSession is the acceptance test for gt-gjh.
//
// Every reaper write path disables autocommit, runs its UPDATE/DELETE batches,
// COMMITs to flush them into the Dolt working set, then CALLs DOLT_COMMIT.
// @@autocommit is SESSION-scoped, so all four steps have to run on one session.
// Five of the six paths issued them on the *sql.DB pool instead, which hands out
// whatever connection is free.
//
// The row-level outcome does not distinguish the two: statements that run with
// autocommit still on commit themselves one at a time, so the wisps still vanish
// and the issues still close. That is why the bug survived a green suite. What
// differs is INVISIBLE from the client — which session each statement landed on
// — so the assertion is made from inside the engine: the DOLT_COMMIT stub
// records @@autocommit as its OWN session reports it. 0 means the procedure ran
// on the session that was prepared for it; 1 means the preparation happened
// somewhere else and the flushing COMMIT before it flushed nothing.
//
// The pool is put in fresh-session-per-statement mode first, because a
// sequential caller on a normal pool keeps getting the same connection back and
// cannot observe the difference at all (see freshSessionPerStatement).
//
// Reap is included even though it was already correct: it is the positive
// control. If the instrument could not report 0 for a path that does pin its
// connection, a 0 from the repaired paths would prove nothing. Each case also
// asserts its row-level effect, so a path that silently stopped doing anything
// cannot pass by issuing no commits at all.
func TestWritePathsPinTheirSession(t *testing.T) {
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	oldPtr := old
	hour := time.Hour

	cases := []struct {
		name  string
		db    string
		seed  func(t *testing.T, f *fixture)
		run   func(t *testing.T, f *fixture)
		check func(t *testing.T, f *fixture)
	}{
		{
			name: "Reap",
			db:   "pin_reap",
			seed: func(t *testing.T, f *fixture) {
				f.insertWisps(t, wispRow{id: "w-stale", wispType: "step", createdAt: old})
			},
			run: func(t *testing.T, f *fixture) {
				if _, err := Reap(f.db, f.dbName, staleAge, false); err != nil {
					t.Fatalf("Reap: %v", err)
				}
			},
			check: func(t *testing.T, f *fixture) {
				if got := f.wispStatus(t, "w-stale"); got != "closed" {
					t.Errorf("w-stale status = %q, want closed", got)
				}
			},
		},
		{
			name: "purgeClosedWisps",
			db:   "pin_purge_wisps",
			seed: func(t *testing.T, f *fixture) {
				f.insertWisps(t, wispRow{id: "w-purge", status: "closed", wispType: "step", createdAt: old, closedAt: &oldPtr})
			},
			run: func(t *testing.T, f *fixture) {
				if _, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false); err != nil {
					t.Fatalf("Purge: %v", err)
				}
			},
			check: func(t *testing.T, f *fixture) {
				if got := f.ids(t, "wisps"); len(got) != 0 {
					t.Errorf("surviving wisps = %v, want none", got)
				}
			},
		},
		{
			name: "purgeOldMail",
			db:   "pin_purge_mail",
			seed: func(t *testing.T, f *fixture) {
				f.insertIssues(t, issueRow{id: "mail-old", status: "closed", priority: 2,
					updatedAt: old, closedAt: &oldPtr, labels: []string{"gt:message"}})
			},
			run: func(t *testing.T, f *fixture) {
				if _, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false); err != nil {
					t.Fatalf("Purge: %v", err)
				}
			},
			check: func(t *testing.T, f *fixture) {
				if got := f.ids(t, "issues"); len(got) != 0 {
					t.Errorf("surviving issues = %v, want none", got)
				}
			},
		},
		{
			name: "AutoClose",
			db:   "pin_autoclose",
			seed: func(t *testing.T, f *fixture) {
				f.insertIssues(t, issueRow{id: "hq-stale", priority: 2, updatedAt: old})
			},
			run: func(t *testing.T, f *fixture) {
				if _, err := AutoClose(f.db, f.dbName, staleAge, false); err != nil {
					t.Fatalf("AutoClose: %v", err)
				}
			},
			check: func(t *testing.T, f *fixture) {
				if got := f.issueStatus(t, "hq-stale"); got != "closed" {
					t.Errorf("hq-stale status = %q, want closed", got)
				}
			},
		},
		{
			name: "ClosePluginReceipts",
			db:   "pin_receipts",
			seed: func(t *testing.T, f *fixture) {
				f.insertIssues(t, issueRow{id: "receipt-old", priority: 2, updatedAt: old,
					labels: []string{"type:plugin-run"}})
			},
			run: func(t *testing.T, f *fixture) {
				if _, err := ClosePluginReceipts(f.db, f.dbName, hour, false); err != nil {
					t.Fatalf("ClosePluginReceipts: %v", err)
				}
			},
			check: func(t *testing.T, f *fixture) {
				if got := f.issueStatus(t, "receipt-old"); got != "closed" {
					t.Errorf("receipt-old status = %q, want closed", got)
				}
			},
		},
		{
			name: "ClosePluginDispatches",
			db:   "pin_dispatches",
			seed: func(t *testing.T, f *fixture) {
				f.insertIssues(t, issueRow{id: "dispatch-old", title: "Plugin: stuck-agent-dog", priority: 2,
					updatedAt: old, labels: []string{"gt:message", "from:daemon"}})
			},
			run: func(t *testing.T, f *fixture) {
				if _, err := ClosePluginDispatches(f.db, f.dbName, hour, false); err != nil {
					t.Fatalf("ClosePluginDispatches: %v", err)
				}
			},
			check: func(t *testing.T, f *fixture) {
				if got := f.issueStatus(t, "dispatch-old"); got != "closed" {
					t.Errorf("dispatch-old status = %q, want closed", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.db)
			tc.seed(t, f)
			f.freshSessionPerStatement()

			tc.run(t, f)
			tc.check(t, f)

			calls := f.doltCommitCalls()
			if len(calls) != 1 {
				t.Fatalf("DOLT_COMMIT calls = %+v, want exactly one — with no commit the "+
					"autocommit assertion below would be vacuous", calls)
			}
			if calls[0].autocommit != "0" {
				t.Errorf("DOLT_COMMIT ran with @@autocommit = %q, want \"0\" — %s issued its "+
					"session setup on the pool, so the commit landed on a session that was "+
					"never switched and the COMMIT before it flushed nothing (gt-gjh)",
					calls[0].autocommit, tc.name)
			}
		})
	}
}

// TestReapReachesFixedPointInOneCall is the acceptance test for gt-r1b: a single
// Reap() must finish the cascade its own closes release, not leave it for a
// caller that has no reason to suspect there is more.
//
// The fixture is built so that the cascade is the ONLY thing that can close the
// four nested wisps. mol-a is the sole stale (past max-age) molecule; every wisp
// below it is well inside max-age, so the stale pass can never select any of
// them at any point. They are reachable only through closedMoleculeStepSubquery,
// which requires their parent molecule to be ALREADY closed — a state pass 1
// creates and only a later pass can act on:
//
//	pass 1  steps: -                                    stale: mol-a, orphan
//	pass 2  steps: step-a1, step-a2, mol-b, step-b1     stale: -
//	pass 3  steps: -                                    stale: -   (fixed point)
//
// Before the fix Reap ran pass 1 only: it returned Reaped=2, MoleculeStepsClosed=0
// and left all four nested wisps open while reporting success.
//
// The chain is deliberately two molecules deep even though that costs no extra
// pass, and the reason is worth recording: closeWispsInBatches re-runs its own
// candidate query until it comes back empty, so a cascade that stays within one
// pass (mol-b closing releases step-b1, both step-pass work) drains inside that
// pass. It is only the edge BETWEEN the two passes that a single round cannot
// cross — the stale pass closing a molecule, which is precisely what the hq run
// in gt-r1b hit. Depth here guards the intra-pass half of that claim: if the
// batch loop ever stops re-querying, step-b1 is left open and this test says so.
//
// The two open-parent rows are the control that keeps a passing run meaningful.
// A Reap that simply closed everything it could see — the failure mode a loop
// makes easy — closes them too; they must survive, because mol-open is neither
// stale nor closed.
func TestReapReachesFixedPointInOneCall(t *testing.T) {
	f := newFixture(t, "reap_fixed_point")
	now := time.Now().UTC()
	stale := now.Add(-48 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	f.insertWisps(t,
		// The one stale root, and a plain stale orphan as the "reap works at all" control.
		wispRow{id: "mol-a", status: "open", issueType: "molecule", createdAt: stale},
		wispRow{id: "orphan", status: "open", issueType: "task", createdAt: stale},
		// Depth 1: steps of mol-a. mol-b is itself a molecule, so its own steps
		// cannot be released until it closes.
		wispRow{id: "step-a1", status: "open", issueType: "task", createdAt: recent},
		wispRow{id: "step-a2", status: "open", issueType: "task", createdAt: recent},
		wispRow{id: "mol-b", status: "open", issueType: "molecule", createdAt: recent},
		// Depth 2: step of mol-b.
		wispRow{id: "step-b1", status: "open", issueType: "task", createdAt: recent},
		// Control: a molecule that never closes, and its child.
		wispRow{id: "mol-open", status: "open", issueType: "molecule", createdAt: recent},
		wispRow{id: "step-open", status: "open", issueType: "task", createdAt: recent},
	)
	f.insertWispDependency(t, "d-a1", "step-a1", "mol-a", "", "parent-child")
	f.insertWispDependency(t, "d-a2", "step-a2", "mol-a", "", "parent-child")
	f.insertWispDependency(t, "d-b", "mol-b", "mol-a", "", "parent-child")
	f.insertWispDependency(t, "d-b1", "step-b1", "mol-b", "", "parent-child")
	f.insertWispDependency(t, "d-open", "step-open", "mol-open", "", "parent-child")

	result, err := Reap(f.db, f.dbName, 24*time.Hour, false)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	for _, id := range []string{"mol-a", "orphan", "step-a1", "step-a2", "mol-b", "step-b1"} {
		if got := f.wispStatus(t, id); got != "closed" {
			t.Errorf("%s status = %q, want closed — one Reap call did not finish the cascade "+
				"its own closes released (gt-r1b)", id, got)
		}
	}
	for _, id := range []string{"mol-open", "step-open"} {
		if got := f.wispStatus(t, id); got != "open" {
			t.Errorf("%s status = %q, want open — the fixed-point loop is closing wisps no single "+
				"pass was ever entitled to close", id, got)
		}
	}

	if result.Reaped != 2 {
		t.Errorf("Reaped = %d, want 2 (mol-a and orphan — the only wisps past max-age)", result.Reaped)
	}
	if result.MoleculeStepsClosed != 4 {
		t.Errorf("MoleculeStepsClosed = %d, want 4 (step-a1, step-a2, mol-b, step-b1)", result.MoleculeStepsClosed)
	}
	if result.Passes != 3 {
		t.Errorf("Passes = %d, want 3 — two rounds of closing (the second crossing the stale->step "+
			"edge) plus the round that observed the fixed point", result.Passes)
	}
	if result.OpenRemain != 2 {
		t.Errorf("OpenRemain = %d, want 2 (mol-open and step-open)", result.OpenRemain)
	}
	if len(result.Anomalies) != 0 {
		t.Errorf("Anomalies = %+v, want none", result.Anomalies)
	}

	// A second call must be a no-op. This is the property the formula's exit
	// criteria ("all stale wisps closed") actually depends on, and the one an
	// operator checks by re-running: if it still closes something, the first call
	// did not reach a fixed point after all.
	second, err := Reap(f.db, f.dbName, 24*time.Hour, false)
	if err != nil {
		t.Fatalf("second Reap: %v", err)
	}
	if second.Reaped != 0 || second.MoleculeStepsClosed != 0 {
		t.Errorf("second Reap closed %d stale + %d steps, want 0 + 0 — the first call was not a fixed point",
			second.Reaped, second.MoleculeStepsClosed)
	}
	if second.Passes != 1 {
		t.Errorf("second Reap Passes = %d, want 1 — a run with nothing to close proves it in one round", second.Passes)
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

// TestEscalationRecordSurvivesTheWholeReaper is the acceptance test for gt-nhp:
// no reaper path — close by age, close as a molecule step, or delete by age —
// may take an escalation record.
//
// The four defects gt-nhp describes compound here, so a partial guard is worse
// than an obvious gap. Protecting only the DELETE leaves reap closing the record,
// and a closed record makes `gt escalate list` hide every delivered copy of a
// still-live escalation (partitionResolvedEscalations) — the escalation is gone from
// the operator's only surface while still needing attention. Protecting only the
// CLOSE leaves the row in the delete set the moment anything else closes it.
//
// Every protected row here has a same-age, same-status negative control with an
// ordinary label. Without them a reaper that did nothing at all — wrong table,
// wrong cutoff, a WHERE matching no row — would satisfy every survival
// assertion below.
func TestEscalationRecordSurvivesTheWholeReaper(t *testing.T) {
	f := newFixture(t, "escalation_survives")
	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)

	f.insertWisps(t,
		// The unattended escalation: open, orphan (no parent molecule), and old.
		// Being ignored is exactly what makes it age-eligible first.
		wispRow{id: "w-esc-open", status: "open", issueType: "task", createdAt: old,
			labels: []string{"gt:escalation"}},
		// One that has been resolved. Its closed_by/closed_reason fields are the
		// only record of how the escalation ended, and wisps are unversioned and
		// unbacked, so a delete here is unrecoverable.
		wispRow{id: "w-esc-closed", status: "closed", issueType: "task", createdAt: old, closedAt: &old,
			labels: []string{"gt:escalation"}},
		// NEGATIVE CONTROLS: identical but for the label.
		wispRow{id: "w-open-control", status: "open", issueType: "task", createdAt: old},
		wispRow{id: "w-closed-control", status: "closed", issueType: "task", createdAt: old, closedAt: &old},
	)
	// The record is a record only if its labels and events go with it.
	f.insertWispAux(t, "w-esc-closed")

	scan, err := Scan(f.db, f.dbName, 24*time.Hour, purgeAge, purgeAge, staleAge)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scan.ReapCandidates != 1 {
		t.Errorf("Scan.ReapCandidates = %d, want 1 (w-open-control) — scan must not advertise "+
			"a wisp reap will decline, or the Dog acts on a count that never lands", scan.ReapCandidates)
	}
	if scan.PurgeCandidates != 1 {
		t.Errorf("Scan.PurgeCandidates = %d, want 1 (w-closed-control)", scan.PurgeCandidates)
	}
	if scan.ProtectedFromPurge != 1 {
		t.Errorf("Scan.ProtectedFromPurge = %d, want 1 (w-esc-closed) — the skip has to be "+
			"reported, or unbounded accumulation is invisible", scan.ProtectedFromPurge)
	}

	reap, err := Reap(f.db, f.dbName, 24*time.Hour, false)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if reap.Reaped != 1 {
		t.Errorf("Reaped = %d, want 1 — a reap that closed nothing would satisfy the "+
			"survival check below for the wrong reason", reap.Reaped)
	}
	if len(reap.Anomalies) != 0 {
		t.Errorf("Reap reported anomalies %+v — excluding protected rows from SELECTION "+
			"(not declining them at UPDATE time) is what keeps the batch loop from stalling",
			reap.Anomalies)
	}
	if got := f.wispStatus(t, "w-esc-open"); got != "open" {
		t.Errorf("escalation wisp status = %q, want open — reap closed it, and a closed record "+
			"hides every delivered copy from `gt escalate list`", got)
	}
	if got := f.wispStatus(t, "w-open-control"); got != "closed" {
		t.Errorf("control wisp status = %q, want closed — reap is inert, so the exemption "+
			"above proves nothing", got)
	}

	purge, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if purge.WispsPurged != 1 {
		t.Errorf("WispsPurged = %d, want 1 (w-closed-control)", purge.WispsPurged)
	}
	if purge.WispsProtected != 1 {
		t.Errorf("WispsProtected = %d, want 1 (w-esc-closed)", purge.WispsProtected)
	}

	want := []string{"w-esc-closed", "w-esc-open", "w-open-control"}
	if got := f.ids(t, "wisps"); !reflect.DeepEqual(got, want) {
		t.Errorf("surviving wisps = %v, want %v", got, want)
	}
	for _, table := range []string{"wisp_labels", "wisp_comments", "wisp_events"} {
		if !slices.Contains(f.issueIDs(t, table), "w-esc-closed") {
			t.Errorf("%s lost w-esc-closed's rows — the wisp survived but its record did not", table)
		}
	}
}

// TestMergeRequestSurvivesReapClose is the acceptance test for gt-ojk1
// (mirroring hq-lrfm): an MR wisp waiting on a human decision must be both
// auditable (queue-visible, status "open") and durable (protected from
// deletion), never forced to trade one for the other.
//
// w-mr-pending stands for exactly that case: open, pinned (as every MR wisp is
// pinned at creation, gt-31nn), and old enough that nothing has touched it
// since it was opened — the state a merge queue produces while it waits on a
// human ruling. Before gt:merge-request was added to ReapProtectedWispLabels,
// reap closed this row purely for being idle past max_age, and
// isMergeRequestReadyForSelection (mq_ready.go) filters the queue view on
// status=="open", so the closed MR vanished from `gt mq list` even though the
// row itself survived (pinned + label-protected from purge). A durable record
// invisible to the only surface an operator reads is not auditable.
//
// The NEGATIVE CONTROL, w-open-control, is the same age and status and differs
// only in its label. A reaper that stopped closing anything would satisfy the
// survival assertion below for the wrong reason; the control is what catches
// that.
func TestMergeRequestSurvivesReapClose(t *testing.T) {
	f := newFixture(t, "mr_survives_reap")
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)

	f.insertWisps(t,
		// Waiting on a human decision: open, pinned, untouched since creation.
		wispRow{id: "w-mr-pending", status: "open", issueType: "task", createdAt: old,
			pinned: boolPtr(true), labels: []string{"gt:merge-request"}},
		// NEGATIVE CONTROL: identical but for the label.
		wispRow{id: "w-open-control", status: "open", issueType: "task", createdAt: old},
	)

	scan, err := Scan(f.db, f.dbName, 24*time.Hour, purgeAge, purgeAge, staleAge)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scan.ReapCandidates != 1 {
		t.Errorf("Scan.ReapCandidates = %d, want 1 (w-open-control) — scan must not advertise "+
			"a wisp reap will decline, or the Dog acts on a count that never lands", scan.ReapCandidates)
	}

	reap, err := Reap(f.db, f.dbName, 24*time.Hour, false)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if reap.Reaped != 1 {
		t.Errorf("Reaped = %d, want 1 — a reap that closed nothing would satisfy the "+
			"survival check below for the wrong reason", reap.Reaped)
	}
	if got := f.wispStatus(t, "w-mr-pending"); got != "open" {
		t.Errorf("MR wisp status = %q, want open — reap closed it, which removes it from "+
			"`gt mq list` (isMergeRequestReadyForSelection filters on status==open) even though "+
			"the row itself survives", got)
	}
	if got := f.wispStatus(t, "w-open-control"); got != "closed" {
		t.Errorf("control wisp status = %q, want closed — reap is inert, so the exemption "+
			"above proves nothing", got)
	}
}

// TestReapProtectionIsNotHardcodedToOneLabel guards the mechanism rather than
// the current list, mirroring TestPurgeProtectionIsNotHardcodedToOneLabel: reap
// protection is driven by ReapProtectedWispLabels, so a future entry takes
// effect on every candidate path without a second edit. A guard that inlined
// 'gt:escalation' into the SQL passes the test above and fails this one.
func TestReapProtectionIsNotHardcodedToOneLabel(t *testing.T) {
	original := ReapProtectedWispLabels
	ReapProtectedWispLabels = append(append([]string{}, original...), "gt:test-unreapable")
	t.Cleanup(func() { ReapProtectedWispLabels = original })

	f := newFixture(t, "reap_protect_list")
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	f.insertWisps(t,
		wispRow{id: "w-added", status: "open", issueType: "task", createdAt: old,
			labels: []string{"gt:test-unreapable"}},
		wispRow{id: "w-control", status: "open", issueType: "task", createdAt: old,
			labels: []string{"gt:wisp"}},
	)

	result, err := Reap(f.db, f.dbName, 24*time.Hour, false)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if result.Reaped != 1 {
		t.Errorf("Reaped = %d, want 1 (the control) — a reap that closed nothing "+
			"would pass the check below for the wrong reason", result.Reaped)
	}
	if got := f.wispStatus(t, "w-added"); got != "open" {
		t.Errorf("w-added status = %q, want open — adding a label to ReapProtectedWispLabels "+
			"must protect it, without editing any query", got)
	}
}

// TestReapProtectsEscalationsOnTheMoleculeStepPath covers the reaper's other
// route into closeWispsInBatches. The closed-molecule-step path has NO age bound
// at all — it closes eligible wisps immediately — so a guard applied only to the
// max-age query would be inert for anything arriving this way.
func TestReapProtectsEscalationsOnTheMoleculeStepPath(t *testing.T) {
	f := newFixture(t, "reap_step_escalation")
	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)

	f.insertWisps(t,
		wispRow{id: "m-closed", status: "closed", issueType: "molecule", createdAt: old, closedAt: &old},
		wispRow{id: "w-step-esc", status: "open", issueType: "task", createdAt: old,
			labels: []string{"gt:escalation"}},
		wispRow{id: "w-step-control", status: "open", issueType: "task", createdAt: old},
	)
	f.insertWispDependency(t, "wd1", "w-step-esc", "m-closed", "", "parent-child")
	f.insertWispDependency(t, "wd2", "w-step-control", "m-closed", "", "parent-child")

	// maxAge far in the future of both rows, so ONLY the molecule-step path can
	// select anything here: a pass would otherwise prove nothing about it.
	result, err := Reap(f.db, f.dbName, 365*24*time.Hour, false)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if result.MoleculeStepsClosed != 1 {
		t.Errorf("MoleculeStepsClosed = %d, want 1 (w-step-control)", result.MoleculeStepsClosed)
	}
	if result.Reaped != 0 {
		t.Errorf("Reaped = %d, want 0 — the max-age path must not be what closed anything here, "+
			"or this test is not measuring the molecule-step query", result.Reaped)
	}
	if got := f.wispStatus(t, "w-step-esc"); got != "open" {
		t.Errorf("escalation step wisp status = %q, want open — the molecule-step candidate "+
			"query has no age bound, so an unguarded one closes escalations on sight", got)
	}
	if got := f.wispStatus(t, "w-step-control"); got != "closed" {
		t.Errorf("control step wisp status = %q, want closed — the molecule-step path is "+
			"inert, so the exemption above proves nothing", got)
	}
}

// TestAutoCloseSparesOpenEscalations covers the durable half of gt-nhp. The
// escalation RECORD is a wisp; the delivered COPY is an ordinary issue, and it
// is the copy `gt escalate list` and the Mayor's queue render.
//
// The P0/P1 exclusion already in AutoClose does not cover it: only critical and
// high map to P0/P1, so a medium or low escalation sits at P2/P3 and is closed
// purely for having been ignored. That is the inversion gt-nhp is about —
// updated_at stops moving BECAUSE nobody attended to it, so the unattended
// escalation reaches the staleness window before the attended one.
func TestAutoCloseSparesOpenEscalations(t *testing.T) {
	f := newFixture(t, "autoclose_escalation")
	stale := time.Now().UTC().Add(-30 * 24 * time.Hour)

	f.insertIssues(t,
		// Medium and low severity copies: P2 and P3, both past the P0/P1 guard.
		issueRow{id: "keep-esc-medium", priority: 2, updatedAt: stale,
			labels: []string{"gt:escalation", "severity:medium", "escalation:hq-wisp-rec1"}},
		issueRow{id: "keep-esc-low", priority: 3, updatedAt: stale,
			labels: []string{"gt:escalation", "severity:low", "escalation:hq-wisp-rec2"}},
		// NEGATIVE CONTROL: same age, same priority, no escalation label.
		issueRow{id: "close-control", priority: 2, updatedAt: stale},
	)

	scan, err := Scan(f.db, f.dbName, staleAge, purgeAge, purgeAge, staleAge)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scan.StaleCandidates != 1 {
		t.Errorf("Scan.StaleCandidates = %d, want 1 (close-control) — scan and AutoClose must "+
			"exempt the same set, or the Dog reads a count the sweep will not act on",
			scan.StaleCandidates)
	}

	result, err := AutoClose(f.db, f.dbName, staleAge, false)
	if err != nil {
		t.Fatalf("AutoClose: %v", err)
	}
	if got := closedEntryIDs(result); !reflect.DeepEqual(got, []string{"close-control"}) {
		t.Errorf("ClosedEntries = %v, want [close-control]", got)
	}
	if got := f.closedIssueIDs(t); !reflect.DeepEqual(got, []string{"close-control"}) {
		t.Errorf("closed issues = %v, want [close-control] — an AutoClose that closed nothing "+
			"would satisfy the exemption assertion for the wrong reason", got)
	}
}

// TestAutoCloseSparesStandingWatchProse is the acceptance test for gt-7kzo,
// mirroring hq-pl6c8: dn-fhw was an accepted "STANDING WATCH OBLIGATION" whose
// permanence was declared only in its title/description prose, carried no
// labels at all, and was closed by AutoClose with "stale:auto-closed by
// reaper" — the exact mechanism the bead existed to defeat.
//
// Both watch-marker beads are stale by the same margin as the control and
// carry NO protective label — the only thing that can save them is the text
// match itself. The title-only and description-only cases are both covered
// because dn-fhw's own report showed the marker in both places and a fix
// keyed on just one field would still have missed half the shape.
func TestAutoCloseSparesStandingWatchProse(t *testing.T) {
	f := newFixture(t, "autoclose_standing_watch")
	stale := time.Now().UTC().Add(-30 * 24 * time.Hour)

	f.insertIssues(t,
		issueRow{id: "keep-watch-title", title: "Standing watch: reap regression", priority: 2, updatedAt: stale},
		issueRow{id: "keep-watch-desc", priority: 2, updatedAt: stale,
			description: "STANDING WATCH OBLIGATION accepted by duly_noted/witness from hq deacon."},
		// NEGATIVE CONTROL: same age, same priority, no marker anywhere.
		issueRow{id: "close-control", priority: 2, updatedAt: stale},
	)

	scan, err := Scan(f.db, f.dbName, staleAge, purgeAge, purgeAge, staleAge)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scan.StaleCandidates != 1 {
		t.Errorf("Scan.StaleCandidates = %d, want 1 (close-control) — scan and AutoClose must "+
			"exempt the same set, or the Dog reads a count the sweep will not act on",
			scan.StaleCandidates)
	}

	result, err := AutoClose(f.db, f.dbName, staleAge, false)
	if err != nil {
		t.Fatalf("AutoClose: %v", err)
	}
	if got := closedEntryIDs(result); !reflect.DeepEqual(got, []string{"close-control"}) {
		t.Errorf("ClosedEntries = %v, want [close-control] — a standing-watch bead was closed "+
			"despite declaring its permanence in prose", got)
	}
	if got := f.closedIssueIDs(t); !reflect.DeepEqual(got, []string{"close-control"}) {
		t.Errorf("closed issues = %v, want [close-control] — an AutoClose that closed nothing "+
			"would satisfy the exemption assertion for the wrong reason", got)
	}
}

// TestStrandedMoleculeProbeBehaviour runs the stranded-molecule probe's real SQL
// against a real engine. It is the acceptance test for gt-id8x.
//
// The unit tests in reaper_test.go go through a fake driver that re-implements
// the predicate in Go, so they cannot fail when the SQL stops matching that
// logic. This one can: the engine evaluates the query the daemon actually
// sends, against a table whose `issues.description` column is where the dispatch
// record lives.
//
// Both controls are present and both are load-bearing:
//   - mol-attached is a healthy in-flight polecat molecule — open, hours old,
//     assignee NULL (root-only molecules never carry one; the assignment sits on
//     the issue) and named by a hook bead's attached_molecule line. Reporting it
//     is the bug: the old predicate flagged every one of these, escalated five
//     times in a day, and sent three agents hunting an emitter that was just
//     `gt mol attach`.
//   - mol-orphan differs from it in exactly one respect — nothing names it — and
//     must still be reported, or a probe that went silent would pass.
func TestStrandedMoleculeProbeBehaviour(t *testing.T) {
	f := newFixture(t, "stranded_molecule")
	now := time.Now().UTC()
	old := now.Add(-4 * time.Hour)

	f.insertIssues(t,
		// The hook bead. This is the shape gt sling writes for mol-polecat-work.
		issueRow{id: "gt-id8x", status: "hooked", priority: 1, updatedAt: old,
			description: "attached_molecule: mol-attached\nattached_formula: mol-polecat-work\nattached_at: 2026-08-25T23:41:04Z"},
		// CONTAMINATION CONTROL: a bug report that quotes the field inline without
		// recording an attachment. The LIKE prefilter matches it, so it reaches the
		// parser, and a naive `description LIKE '%attached_molecule: <id>%'` would
		// launder mol-orphan straight out of the anomaly. Parsing the line as a
		// field rejects it: the key here is "Quoting the reaper", not
		// attached_molecule. Text ABOUT a thing must not satisfy a search FOR it.
		issueRow{id: "gt-report", status: "open", priority: 2, updatedAt: old,
			description: "Quoting the reaper: it says attached_molecule: mol-orphan, but no bead records that."},
	)
	f.insertWisps(t,
		// Dispatched by attachment. Healthy work in flight.
		wispRow{id: "mol-attached", status: "open", issueType: "molecule", createdAt: old},
		// NEGATIVE CONTROL: identical but for the dispatch record.
		wispRow{id: "mol-orphan", status: "open", issueType: "molecule", createdAt: old},
		// Too young to judge — a dispatcher may still be on its way.
		wispRow{id: "mol-fresh", status: "open", issueType: "molecule", createdAt: now.Add(-time.Minute)},
		// Dispatched the other way: the wisp itself carries the assignee.
		wispRow{id: "mol-assigned", status: "hooked", issueType: "molecule", createdAt: old, assignee: "deacon/dogs/alpha"},
		// A hook bead that is itself a wisp. Attachment records live in both
		// tables, and a molecule attached from either one was dispatched.
		wispRow{id: "hq-wisp-hook", status: "hooked", createdAt: old,
			description: "attached_molecule: mol-wisp-attached"},
		wispRow{id: "mol-wisp-attached", status: "open", issueType: "molecule", createdAt: old},
	)

	scan, err := Scan(f.db, f.dbName, staleAge, purgeAge, purgeAge, staleAge)
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
		t.Fatalf("mol-orphan has no dispatch record and must be reported; anomalies: %+v", scan.Anomalies)
	}
	if found.Count != 1 {
		t.Errorf("stranded_molecules Count = %d, want 1 (mol-orphan alone). Anything higher means a "+
			"dispatched molecule was called stranded — the gt-id8x defect. Message: %q", found.Count, found.Message)
	}
	if strings.Contains(found.Message, "never dispatched") {
		t.Errorf("the probe observes the absence of a dispatch RECORD, not the absence of a "+
			"dispatch; got %q", found.Message)
	}

	// The probe must also survive a database with no issues table — the reaper
	// reaches servers where beads keeps issues elsewhere (that is what
	// isTableNotFound exists for). Losing one attachment source must degrade the
	// probe, not silence it: the wisp-side records must still be read.
	//
	// strandedMoleculeIDs is called directly here because dropping the table also
	// breaks Scan's unrelated molecule-step count, which would mask this.
	f.exec(t, "DROP TABLE issues")
	stranded, err := strandedMoleculeIDs(context.Background(), f.db, now.Add(-StrandedMoleculeAge))
	if err != nil {
		t.Fatalf("strandedMoleculeIDs with no issues table: %v", err)
	}
	sort.Strings(stranded)
	// mol-attached loses its dispatch record along with the table, so it joins
	// mol-orphan. mol-wisp-attached keeps its wisp-side record and must not.
	if want := []string{"mol-attached", "mol-orphan"}; !reflect.DeepEqual(stranded, want) {
		t.Errorf("stranded = %v, want %v — with issues gone the wisp-side attachment must still "+
			"be read, and the probe must not go silent", stranded, want)
	}
}

// TestPurgeReportsWhatItPurgedNotJustHowMany is the acceptance test for gt-mkuw.
//
// The purge already computed a wisp_type digest and threw everything but the
// total away, so the entire record one of these deletions left behind was
// `reaper: purge 29 closed wisps from beads`. Wisp tables are in dolt_ignore,
// so the DOLT_COMMIT is empty and its MESSAGE is the only artifact — there is
// no diff to read afterwards, ever.
//
// On 2026-08-26 three such lines on the beads database (5 + 29 + 7 = 41 rows in
// 45 minutes) were read as ~40 destroyed merge-request records and filed as a
// P1 second-deleter incident. Nothing was missing. The rows were molecule steps
// and sling-context wisps, and purgeProtectWhere makes this path structurally
// incapable of taking a merge-request row at all. The count could not say so.
//
// The NEGATIVE CONTROL is w-mr: same status, same age, protected only by its
// label. It must appear in NEITHER the total NOR the breakdown — a breakdown
// that named it would be describing candidates rather than deletions, which is
// the failure this test exists to catch.
func TestPurgeReportsWhatItPurgedNotJustHowMany(t *testing.T) {
	f := newFixture(t, "purge_digest")
	now := time.Now().UTC()
	oldClose := now.Add(-30 * 24 * time.Hour)

	f.insertWisps(t,
		wispRow{id: "w-patrol-1", status: "closed", wispType: "patrol", createdAt: oldClose, closedAt: &oldClose},
		wispRow{id: "w-patrol-2", status: "closed", wispType: "patrol", createdAt: oldClose, closedAt: &oldClose},
		wispRow{id: "w-patrol-3", status: "closed", wispType: "patrol", createdAt: oldClose, closedAt: &oldClose},
		wispRow{id: "w-hb", status: "closed", wispType: "heartbeat", createdAt: oldClose, closedAt: &oldClose},
		// wisp_type NULL. Real rows reach this state in bulk — measured on the
		// gastown rig, 703 of 703 carried no type — so "unknown" is the label
		// most of a real digest wears, not an edge case.
		wispRow{id: "w-untyped", status: "closed", createdAt: oldClose, closedAt: &oldClose},
		// NEGATIVE CONTROL: identical window, held back by its label alone.
		wispRow{id: "w-mr", status: "closed", wispType: "merge_request", createdAt: oldClose, closedAt: &oldClose,
			labels: []string{"gt:merge-request"}},
	)

	result, err := Purge(f.db, f.dbName, purgeAge, purgeAge, false)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if result.WispsPurged != 5 {
		t.Fatalf("WispsPurged = %d, want 5", result.WispsPurged)
	}

	want := map[string]int{"patrol": 3, "heartbeat": 1, "unknown": 1}
	if !reflect.DeepEqual(result.WispsPurgedByType, want) {
		t.Errorf("WispsPurgedByType = %v, want %v", result.WispsPurgedByType, want)
	}
	if _, named := result.WispsPurgedByType["merge_request"]; named {
		t.Errorf("WispsPurgedByType names merge_request (%v) — the protected control was not "+
			"deleted, so a breakdown that counts it describes candidates rather than deletions",
			result.WispsPurgedByType)
	}

	// The breakdown must sum to the total it accompanies. A partition that does
	// not add up is what the purge_digest_mismatch anomaly is for; here there is
	// nothing to mismatch, so the run must be clean.
	sum := 0
	for _, n := range result.WispsPurgedByType {
		sum += n
	}
	if sum != result.WispsPurged {
		t.Errorf("breakdown sums to %d but WispsPurged = %d", sum, result.WispsPurged)
	}
	if len(result.Anomalies) != 0 {
		t.Errorf("unexpected anomalies: %+v", result.Anomalies)
	}

	// The commit message is the only durable trace. It must name the population
	// and say the rows were unprotected, so the reading that produced gt-mkuw
	// cannot survive contact with it.
	commits := f.doltCommitMessages()
	if len(commits) != 1 {
		t.Fatalf("DOLT_COMMIT messages = %v, want exactly one", commits)
	}
	msg := commits[0]
	for _, want := range []string{"unprotected", "patrol 3", "heartbeat 1", "unknown 1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("DOLT_COMMIT message %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "merge_request") {
		t.Errorf("DOLT_COMMIT message %q names merge_request, which this path cannot delete", msg)
	}

	if got := f.ids(t, "wisps"); !reflect.DeepEqual(got, []string{"w-mr"}) {
		t.Errorf("surviving wisps = %v, want [w-mr] — the label-protected control must remain", got)
	}
}

// TestFormatWispTypeDigestIsStableAndOrdered pins the rendering itself.
//
// Commonest first with ties broken by name, so the same population always
// renders the same string: a message that reorders itself between runs cannot
// be compared across two purges, which is most of what reading a digest is for.
func TestFormatWispTypeDigestIsStableAndOrdered(t *testing.T) {
	digest := map[string]int{"unknown": 1, "patrol": 12, "heartbeat": 12, "step": 40}
	const want = "step 40, heartbeat 12, patrol 12, unknown 1"
	for i := 0; i < 8; i++ {
		if got := FormatWispTypeDigest(digest); got != want {
			t.Fatalf("FormatWispTypeDigest run %d = %q, want %q", i, got, want)
		}
	}

	// An empty digest must say so rather than render as "()" in the commit
	// message, where an empty parenthesis reads as a truncated line.
	if got := FormatWispTypeDigest(nil); got != "no types recorded" {
		t.Errorf("FormatWispTypeDigest(nil) = %q, want %q", got, "no types recorded")
	}

	// A quote would end the SQL string literal the commit message is
	// interpolated into. No production wisp_type carries one; the guard is here
	// so that stays true by construction rather than by luck.
	if got := FormatWispTypeDigest(map[string]int{"it's": 2}); strings.Contains(got, "'") {
		t.Errorf("FormatWispTypeDigest = %q, want the single quote stripped", got)
	}
}
