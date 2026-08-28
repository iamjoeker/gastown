package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/reaper"
)

func cooldownPlugin(name, duration string) *Plugin {
	return &Plugin{Name: name, Gate: &Gate{Type: GateCooldown, Duration: duration}}
}

func TestRetentionPolicyDerivesFromGates(t *testing.T) {
	policy := NewRetentionPolicy([]*Plugin{
		cooldownPlugin("stuck-agent-dog", "5m"),
		cooldownPlugin("git-hygiene", "12h"),
		cooldownPlugin("tool-updater", "168h"),
	})

	tests := []struct {
		plugin string
		want   time.Duration
	}{
		// Short gates land on the floor, not on 2x5m.
		{"stuck-agent-dog", MinReceiptRetention},
		{"git-hygiene", MinReceiptRetention}, // 24h < 48h floor
		// The one gate in the town that exceeds the floor is the whole reason
		// this policy exists: 168h*2. A 24h bucket here deletes the receipt on
		// day 1 of a 7-day cooldown.
		{"tool-updater", 336 * time.Hour},
	}
	for _, tc := range tests {
		if got := policy.For(tc.plugin); got != tc.want {
			t.Errorf("For(%q) = %s, want %s", tc.plugin, got, tc.want)
		}
	}

	// An unknown plugin gets the longest window in town, not the floor: an
	// unrecognised name means the reader is unknown, not that there is none.
	// plugin:quality-review-result — written by the Refinery, read by
	// quality-review's body — is exactly this case.
	if got := policy.For("quality-review-result"); got != 336*time.Hour {
		t.Errorf("For(unknown) = %s, want %s (the fallback)", got, 336*time.Hour)
	}
	if got := policy.Fallback(); got != 336*time.Hour {
		t.Errorf("Fallback() = %s, want %s", got, 336*time.Hour)
	}
}

func TestRetentionPolicyRefusesToGuessUnreadableGates(t *testing.T) {
	// A cooldown gate whose window cannot be read must not fall to the floor:
	// the reader exists and its window is unknown. Both cases resolve to the
	// fallback, which the long gate raises to 336h.
	policy := NewRetentionPolicy([]*Plugin{
		cooldownPlugin("tool-updater", "168h"),
		cooldownPlugin("no-duration", ""),
		cooldownPlugin("bad-duration", "7 days"),
	})

	for _, name := range []string{"no-duration", "bad-duration"} {
		if got := policy.For(name); got != policy.Fallback() {
			t.Errorf("For(%q) = %s, want the fallback %s", name, got, policy.Fallback())
		}
	}
}

func TestRetentionPolicyNonCooldownGatesGetTheFloor(t *testing.T) {
	// No CountRunsSince query reads a cron/manual/condition plugin's receipts,
	// so the floor applies — but the floor still has to cover `gt plugin
	// history` and the 24h body queries.
	policy := NewRetentionPolicy([]*Plugin{
		{Name: "nightly", Gate: &Gate{Type: GateCron, Schedule: "0 9 * * *"}},
		{Name: "by-hand", Gate: &Gate{Type: GateManual}},
		{Name: "gateless"},
	})

	for _, name := range []string{"nightly", "by-hand", "gateless"} {
		if got := policy.For(name); got != MinReceiptRetention {
			t.Errorf("For(%q) = %s, want the floor %s", name, got, MinReceiptRetention)
		}
	}
	if got := policy.Fallback(); got != MinReceiptRetention {
		t.Errorf("Fallback() = %s, want %s", got, MinReceiptRetention)
	}
}

func TestRetentionPolicyEmptySetKeepsTheFloor(t *testing.T) {
	// Discovery returning nothing must never shorten retention below the floor.
	// Callers are expected not to prune on a failed discovery at all; this is
	// the second line of that defence.
	policy := NewRetentionPolicy(nil)
	if got := policy.For("anything"); got != MinReceiptRetention {
		t.Errorf("For on empty policy = %s, want %s", got, MinReceiptRetention)
	}
}

func TestRetentionAlwaysCoversTheGateThatReadsIt(t *testing.T) {
	// The property that matters, stated as a property: for every plugin, the
	// retention window is at least as long as the cooldown window its own gate
	// queries. A receipt deleted before that boundary makes the gate read
	// "never ran".
	durations := []string{"5m", "15m", "30m", "1h", "2h", "6h", "12h", "168h", "720h"}
	plugins := make([]*Plugin, 0, len(durations))
	for i, d := range durations {
		plugins = append(plugins, cooldownPlugin(fmt.Sprintf("p%d", i), d))
	}
	policy := NewRetentionPolicy(plugins)

	for i, d := range durations {
		gate, err := time.ParseDuration(d)
		if err != nil {
			t.Fatalf("bad test duration %q: %v", d, err)
		}
		name := fmt.Sprintf("p%d", i)
		if got := policy.For(name); got < gate {
			t.Errorf("For(%q) = %s, shorter than its own %s gate", name, got, gate)
		}
	}
}

func TestReceiptProtectionMirrorsTheTownsGuards(t *testing.T) {
	tests := []struct {
		name   string
		row    receiptRow
		labels []string
		want   string
	}{
		{"unprotected", receiptRow{}, []string{receiptTypeLabel, "plugin:rebuild-gt"}, ""},
		{"pinned", receiptRow{Pinned: 1}, nil, "pinned"},
		{"merge request", receiptRow{}, []string{"gt:merge-request"}, "protected label gt:merge-request"},
		{"escalation", receiptRow{}, []string{"gt:escalation"}, "protected label gt:escalation"},
		{"keep", receiptRow{}, []string{"gt:keep"}, "keep label gt:keep"},
		{"commented", receiptRow{CommentCount: 2}, nil, "has comments"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := receiptProtection(tc.row, tc.labels); got != tc.want {
				t.Errorf("receiptProtection = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReceiptPluginName(t *testing.T) {
	if got := receiptPluginName([]string{receiptTypeLabel, "plugin:tool-updater", "result:success"}); got != "tool-updater" {
		t.Errorf("receiptPluginName = %q, want tool-updater", got)
	}
	if got := receiptPluginName([]string{receiptTypeLabel, "result:success"}); got != "" {
		t.Errorf("receiptPluginName with no plugin label = %q, want empty", got)
	}
}

func TestSQLIDListRejectsQuotedIDs(t *testing.T) {
	if _, err := sqlIDList([]string{"gt-ok", "gt-'; DROP TABLE wisps; --"}); err == nil {
		t.Fatal("sqlIDList accepted an ID containing a quote")
	}
	got, err := sqlIDList([]string{"gt-a", "gt-b"})
	if err != nil {
		t.Fatalf("sqlIDList: %v", err)
	}
	if got != "'gt-a', 'gt-b'" {
		t.Errorf("sqlIDList = %q", got)
	}
}

// fakeBDRecorder installs a stub bd on PATH that answers the three queries
// PruneReceipts issues, and returns a recorder pointed at a temp town.
//
// The stub is a script rather than an interface because the whole path under
// test is argv construction plus JSON parsing of bd's output; a Go fake would
// test neither.
func fakeBDRecorder(t *testing.T, rows []map[string]any) (*Recorder, string) {
	t.Helper()

	townRoot := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd-args.log")

	payload, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal rows: %v", err)
	}
	rowsPath := filepath.Join(binDir, "rows.json")
	if err := os.WriteFile(rowsPath, payload, 0644); err != nil {
		t.Fatalf("write rows: %v", err)
	}

	// The stub deletes by rewriting rows.json, so the post-delete survivor check
	// and the remaining-count re-read see the effect of the delete rather than a
	// canned answer. A prune that reports success while nothing was removed is
	// the failure this test exists to catch.
	script := `#!/usr/bin/env python3
import json, os, re, sys

args = sys.argv[1:]
with open(os.environ["BD_ARGS_LOG"], "a") as f:
    f.write(" ".join(args) + "\n")

path = os.environ["BD_ROWS"]
with open(path) as f:
    rows = json.load(f)

if args[:2] == ["sql", "--json"]:
    q = args[2]
    if q.startswith("SELECT COUNT(*) AS n"):
        print(json.dumps([{"n": len(rows)}]))
    elif q.startswith("SELECT id FROM wisps WHERE id IN"):
        wanted = set(re.findall(r"'([^']+)'", q))
        print(json.dumps([{"id": r["id"]} for r in rows if r["id"] in wanted]))
    else:
        print(json.dumps(rows))
elif args[0] == "delete":
    gone = set(a for a in args[1:] if a != "--force")
    with open(path, "w") as f:
        json.dump([r for r in rows if r["id"] not in gone], f)
    print("deleted")
else:
    sys.exit(2)
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_ARGS_LOG", logPath)
	t.Setenv("BD_ROWS", rowsPath)
	// Isolate the durable archive PruneReceipts now writes to before deleting
	// (gt-wg81) from the real user's ~/.gt/wisp-archive.
	t.Setenv(reaper.ArchiveDirEnv, filepath.Join(t.TempDir(), "wisp-archive"))

	return NewRecorder(townRoot), logPath
}

func receiptFixture(id, plugin string, created time.Time) map[string]any {
	return map[string]any{
		"id":            id,
		"status":        "closed",
		"created_at":    created.UTC().Format(time.RFC3339),
		"pinned":        0,
		"comment_count": 0,
		"labels_csv":    receiptTypeLabel + "," + pluginLabelPrefix + plugin + ",result:success",
	}
}

func TestPruneReceiptsRespectsTheLongestGate(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("fake bd stub needs python3")
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	rows := []map[string]any{
		// tool-updater: 168h gate, 336h retention. Both of these are far past
		// gc_report's 24h — the bucket gt-fqd5 nearly stamped on them — and both
		// must survive.
		receiptFixture("gt-tool-1", "tool-updater", now.Add(-25*time.Hour)),
		receiptFixture("gt-tool-2", "tool-updater", now.Add(-300*time.Hour)),
		// Past even 336h: genuinely dead, nothing can read it.
		receiptFixture("gt-tool-3", "tool-updater", now.Add(-400*time.Hour)),
		// 5m gate, floor retention: inside 48h stays, outside goes.
		receiptFixture("gt-dog-1", "stuck-agent-dog", now.Add(-47*time.Hour)),
		receiptFixture("gt-dog-2", "stuck-agent-dog", now.Add(-72*time.Hour)),
		// Unknown plugin: fallback (336h), so a 300h-old one is kept.
		receiptFixture("gt-qr-1", "quality-review-result", now.Add(-300*time.Hour)),
	}

	recorder, logPath := fakeBDRecorder(t, rows)
	policy := NewRetentionPolicy([]*Plugin{
		cooldownPlugin("tool-updater", "168h"),
		cooldownPlugin("stuck-agent-dog", "5m"),
	})

	result, err := recorder.PruneReceipts(policy, now, ReceiptPruneOptions{})
	if err != nil {
		t.Fatalf("PruneReceipts: %v", err)
	}
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}

	deleted := map[string]bool{}
	for _, d := range result.Deleted {
		deleted[d.ID] = true
	}
	for _, id := range []string{"gt-tool-3", "gt-dog-2"} {
		if !deleted[id] {
			t.Errorf("expected %s to be deleted; deleted=%v", id, deleted)
		}
	}
	for _, id := range []string{"gt-tool-1", "gt-tool-2", "gt-dog-1", "gt-qr-1"} {
		if deleted[id] {
			t.Errorf("%s was deleted while a gate can still read it", id)
		}
	}
	if result.Scanned != len(rows) {
		t.Errorf("Scanned = %d, want %d", result.Scanned, len(rows))
	}
	if result.Kept != 4 {
		t.Errorf("Kept = %d, want 4", result.Kept)
	}
	// The control: the re-read, not this process's own bookkeeping.
	if result.Remaining != len(rows)-2 {
		t.Errorf("Remaining = %d, want %d", result.Remaining, len(rows)-2)
	}

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	if !strings.Contains(string(args), "delete gt-tool-3 gt-dog-2 --force") {
		t.Errorf("expected a batched forced delete of exactly the eligible ids; got:\n%s", args)
	}
}

func TestPruneReceiptsDryRunDeletesNothing(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("fake bd stub needs python3")
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	rows := []map[string]any{
		receiptFixture("gt-old-1", "rebuild-gt", now.Add(-400*time.Hour)),
	}

	recorder, logPath := fakeBDRecorder(t, rows)
	policy := NewRetentionPolicy([]*Plugin{cooldownPlugin("rebuild-gt", "1h")})

	result, err := recorder.PruneReceipts(policy, now, ReceiptPruneOptions{DryRun: true})
	if err != nil {
		t.Fatalf("PruneReceipts: %v", err)
	}
	if len(result.Deleted) != 1 || result.Deleted[0].ID != "gt-old-1" {
		t.Fatalf("dry run should report the eligible receipt, got %+v", result.Deleted)
	}
	if result.Remaining != 1 {
		t.Errorf("Remaining = %d, want 1 — a dry run must not remove anything", result.Remaining)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	if strings.Contains(string(args), "delete ") {
		t.Errorf("dry run issued a delete:\n%s", args)
	}
}

func TestPruneReceiptsHoldsProtectedAndOpenReceipts(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("fake bd stub needs python3")
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	pinned := receiptFixture("gt-pinned", "rebuild-gt", now.Add(-400*time.Hour))
	pinned["pinned"] = 1
	escalation := receiptFixture("gt-esc", "rebuild-gt", now.Add(-400*time.Hour))
	escalation["labels_csv"] = receiptTypeLabel + ",plugin:rebuild-gt,gt:escalation"
	openRow := receiptFixture("gt-open", "rebuild-gt", now.Add(-400*time.Hour))
	openRow["status"] = "open"

	recorder, logPath := fakeBDRecorder(t, []map[string]any{pinned, escalation, openRow})
	policy := NewRetentionPolicy([]*Plugin{cooldownPlugin("rebuild-gt", "1h")})

	result, err := recorder.PruneReceipts(policy, now, ReceiptPruneOptions{})
	if err != nil {
		t.Fatalf("PruneReceipts: %v", err)
	}
	if len(result.Deleted) != 0 {
		t.Fatalf("deleted a guarded receipt: %+v", result.Deleted)
	}
	if len(result.Held) != 2 {
		t.Errorf("Held = %d, want 2 (pinned + escalation)", len(result.Held))
	}
	if result.Open != 1 {
		t.Errorf("Open = %d, want 1", result.Open)
	}
	args, _ := os.ReadFile(logPath)
	if strings.Contains(string(args), "delete ") {
		t.Errorf("issued a delete for guarded receipts:\n%s", args)
	}
}

func TestPruneReceiptsReportsTheCapItHit(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("fake bd stub needs python3")
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	var rows []map[string]any
	for i := 0; i < 5; i++ {
		rows = append(rows, receiptFixture(fmt.Sprintf("gt-old-%d", i), "rebuild-gt", now.Add(-time.Duration(400+i)*time.Hour)))
	}

	recorder, _ := fakeBDRecorder(t, rows)
	policy := NewRetentionPolicy([]*Plugin{cooldownPlugin("rebuild-gt", "1h")})

	result, err := recorder.PruneReceipts(policy, now, ReceiptPruneOptions{Limit: 2})
	if err != nil {
		t.Fatalf("PruneReceipts: %v", err)
	}
	if len(result.Deleted) != 2 {
		t.Errorf("Deleted = %d, want 2 (the cap)", len(result.Deleted))
	}
	// A cap that is not reported reads as "that was everything".
	if result.Deferred != 3 {
		t.Errorf("Deferred = %d, want 3", result.Deferred)
	}
	if result.Remaining != 3 {
		t.Errorf("Remaining = %d, want 3", result.Remaining)
	}
}

// failingReceiptArchive is an Archiver that cannot keep a record — the
// property under test is what PruneReceipts does when it meets one (gt-wg81,
// mirrors internal/cmd/compact_archive_test.go's failingArchive).
type failingReceiptArchive struct{ calls int }

func (a *failingReceiptArchive) ArchiveWisps(records []reaper.ArchivedWisp) error {
	a.calls++
	return fmt.Errorf("disk full")
}
func (a *failingReceiptArchive) Location() string { return "/nowhere" }

func TestArchiveReceiptsHoldsEverythingWhenTheRecordCannotBeWritten(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("fake bd stub needs python3")
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	recorder, _ := fakeBDRecorder(t, []map[string]any{
		receiptFixture("gt-old-1", "rebuild-gt", now.Add(-400*time.Hour)),
	})
	eligible := []PrunedReceipt{{ID: "gt-old-1", Plugin: "rebuild-gt"}}
	result := &ReceiptPruneResult{}
	archive := &failingReceiptArchive{}

	if recorder.archiveReceiptsTo(eligible, now, archive, result) {
		t.Fatal("archiveReceiptsTo returned true after the archive failed — the caller " +
			"would then delete a receipt that nothing anywhere records, which is gt-wg81")
	}
	if archive.calls != 1 {
		t.Errorf("ArchiveWisps calls = %d, want 1", archive.calls)
	}
	if result.Archived != 0 || result.ArchivedTo != "" {
		t.Errorf("Archived/ArchivedTo = %d/%q, want 0/\"\" — a failed archive must not "+
			"be reported as a kept record", result.Archived, result.ArchivedTo)
	}
}

func TestPruneReceiptsHoldsEligibleReceiptsWhenArchiveIsUnavailable(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("fake bd stub needs python3")
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	recorder, logPath := fakeBDRecorder(t, []map[string]any{
		receiptFixture("gt-old-1", "rebuild-gt", now.Add(-400*time.Hour)),
	})
	// A file where the archive directory should be forces NewFileArchive to
	// fail (it cannot MkdirAll through a regular file), simulating an
	// unwritable archive without touching the real one.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	t.Setenv(reaper.ArchiveDirEnv, blocked)
	policy := NewRetentionPolicy([]*Plugin{cooldownPlugin("rebuild-gt", "1h")})

	result, err := recorder.PruneReceipts(policy, now, ReceiptPruneOptions{})
	if err != nil {
		t.Fatalf("PruneReceipts: %v", err)
	}
	if len(result.Deleted) != 0 {
		t.Fatalf("deleted %d receipt(s) with no durable record of them — this is gt-wg81's exact "+
			"shape: a destructive cleanup whose own record can be lost", len(result.Deleted))
	}
	if len(result.Held) != 1 || result.Held[0].ID != "gt-old-1" {
		t.Errorf("Held = %+v, want [gt-old-1]", result.Held)
	}
	if len(result.Errors) == 0 {
		t.Error("expected an error naming the unwritable archive")
	}
	args, _ := os.ReadFile(logPath)
	if strings.Contains(string(args), "delete ") {
		t.Errorf("issued a delete despite the archive being unwritable:\n%s", args)
	}
}

// TestPruneReceiptsArchivesBeforeDeleting is the positive half: plant a
// known-doomed receipt, run the prune, and confirm the durable archive names
// it afterwards — the verification CLAUDE.md prescribes for this bug class
// rather than trusting a clean-looking summary.
func TestPruneReceiptsArchivesBeforeDeleting(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("fake bd stub needs python3")
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	recorder, _ := fakeBDRecorder(t, []map[string]any{
		receiptFixture("gt-doomed-1", "rebuild-gt", now.Add(-400*time.Hour)),
	})
	archiveDir := os.Getenv(reaper.ArchiveDirEnv)
	policy := NewRetentionPolicy([]*Plugin{cooldownPlugin("rebuild-gt", "1h")})

	result, err := recorder.PruneReceipts(policy, now, ReceiptPruneOptions{})
	if err != nil {
		t.Fatalf("PruneReceipts: %v", err)
	}
	if len(result.Deleted) != 1 || result.Deleted[0].ID != "gt-doomed-1" {
		t.Fatalf("expected gt-doomed-1 to be deleted, got %+v", result.Deleted)
	}
	if result.Archived != 1 {
		t.Errorf("Archived = %d, want 1", result.Archived)
	}

	scan, err := reaper.ReadArchive(archiveDir, reaper.ArchiveFilter{ID: "gt-doomed-1"})
	if err != nil {
		t.Fatalf("ReadArchive: %v", err)
	}
	if len(scan.Records) != 1 {
		t.Fatalf("the deleted receipt's own record is gone from the place that survives its "+
			"deletion — got %d records, want 1", len(scan.Records))
	}
}

func TestPruneReceiptsFailsLoudlyOnAnUnreadableTable(t *testing.T) {
	townRoot := t.TempDir()
	binDir := t.TempDir()
	// bd that cannot answer: the prune must return an error, not an empty
	// result. An unreadable database and a tidy one reporting the same clean
	// summary is gt-ktvs, and it hid a blind command for months.
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte("#!/usr/bin/env bash\necho 'boom' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	recorder := NewRecorder(townRoot)
	if _, err := recorder.PruneReceipts(NewRetentionPolicy(nil), time.Now(), ReceiptPruneOptions{}); err == nil {
		t.Fatal("PruneReceipts returned no error on an unreadable wisps table")
	}
}

func TestRetentionEntriesShowEveryPlugin(t *testing.T) {
	// A plugin whose gate window cannot be read must still appear, and must be
	// labelled as falling back rather than looking like a derived 336h.
	plugins := []*Plugin{
		cooldownPlugin("tool-updater", "168h"),
		cooldownPlugin("broken", "7 days"),
	}
	entries := NewRetentionPolicy(plugins).Entries(plugins)

	if len(entries) != 2 {
		t.Fatalf("Entries returned %d rows, want 2: %+v", len(entries), entries)
	}
	if entries[0].Plugin != "broken" || !strings.Contains(entries[0].Retention, "fallback") {
		t.Errorf("unreadable gate not marked as a fallback: %+v", entries[0])
	}
	if entries[1].Plugin != "tool-updater" || strings.Contains(entries[1].Retention, "fallback") {
		t.Errorf("derived retention mislabelled: %+v", entries[1])
	}
}
