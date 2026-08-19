package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/reaper"
)

// Receipt retention (gt-0cja).
//
// A plugin run receipt is not a report, and this is the file that acts on the
// difference. RecordRun writes one closed `type:plugin-run` wisp per dispatch
// and deliberately leaves wisp_type EMPTY, because since gt-ktvs an untyped
// wisp is SKIPPED by `gt compact` while a typed one is deleted on that type's
// TTL — and the receipts ARE the cooldown ledger the daemon gates dispatch on
// (CountRunsSince > 0 means "in cooldown"). Stamping any of bd's seven wisp
// types on them would delete tool-updater's receipts at 24h in the middle of
// its 168h cooldown, and the daemon would then dispatch a brew upgrade on every
// scan for the remaining six days.
//
// The cost of that correct decision is unbounded growth: 5,779 receipts in hq
// on 2026-08-19, all closed, all permanent. So the receipts get a TTL here
// instead — one derived from the gates that READ them rather than borrowed from
// a bucket that means something else. The wisp_type stays empty; compaction
// still keeps its hands off; this package deletes its own records on its own
// window, which is the only place in the town that knows what that window is.

const (
	// MinReceiptRetention is the floor under every plugin's retention.
	//
	// It exists for the readers a declared gate does not describe. `gt plugin
	// history` renders receipts, and plugin bodies query them directly —
	// plugins/quality-review/plugin.md step 1 reads `--created-after=-24h` over
	// receipts the Refinery writes, which no gate declares. 48h covers that with
	// margin and is longer than every non-cooldown reader in the tree today.
	//
	// It is also what makes a short window impossible to reach by accident: the
	// twelve cooldown gates in the town run from 5m to 12h, so for all but one
	// plugin this floor — not the derived value — is the retention.
	MinReceiptRetention = 48 * time.Hour

	// ReceiptRetentionFactor is the safety margin on a derived window.
	//
	// A receipt older than gate.Duration cannot affect that gate: the gate query
	// is `--created-after=now-Duration`, so Duration alone would already be
	// arithmetically sufficient. The doubling buys the things arithmetic does
	// not cover — a gate duration edited upward between two daemon ticks, a
	// receipt whose creation and query straddle a clock adjustment, and an
	// operator reading `gt plugin history` for the run before last. For
	// tool-updater's 168h this yields 336h.
	ReceiptRetentionFactor = 2

	// receiptPruneChunk is how many IDs go to one `bd delete` invocation.
	//
	// Batched because the first run on a live town has thousands of rows to
	// clear and one subprocess per row would take minutes; bounded because a
	// batch is verified by re-reading the IDs it claimed to delete, and a
	// smaller batch localizes which IDs a partial failure stranded.
	receiptPruneChunk = 100

	// receiptDeleteTimeout bounds one batch delete. `bd delete` rewrites
	// dependency links and text references per ID, so a 100-ID batch is well
	// past what constants.BdCommandTimeout is sized for.
	receiptDeleteTimeout = 3 * time.Minute
)

// RetentionPolicy answers "how long must this plugin's receipts be kept".
//
// Built from the discovered plugin set, so it is a function of the gates that
// actually exist rather than a constant that has to be maintained alongside
// them. A receipt whose plugin is not in the set gets Fallback — the longest
// retention in town — because an unrecognised name means the reader is unknown,
// not that there is none. Receipts labelled plugin:quality-review-result are
// exactly that case: they are written by the Refinery under a name no plugin.md
// declares, and read by quality-review's body.
type RetentionPolicy struct {
	byPlugin map[string]time.Duration
	fallback time.Duration
}

// NewRetentionPolicy derives the retention window for each discovered plugin.
//
// Three cases, and the difference between them is the whole point:
//
//   - a cooldown gate with a parseable duration — derived: max(floor, d*factor).
//   - a cooldown gate whose duration is missing or unparseable — the window that
//     reads these receipts exists and cannot be read, so the policy refuses to
//     guess and hands out Fallback.
//   - any other gate type — no CountRunsSince query reads it, so the floor
//     applies. A plugin whose BODY reads receipts on a window longer than the
//     floor must declare that window as a cooldown gate; nothing else here can
//     see it.
func NewRetentionPolicy(plugins []*Plugin) RetentionPolicy {
	p := RetentionPolicy{
		byPlugin: make(map[string]time.Duration, len(plugins)),
		fallback: MinReceiptRetention,
	}

	for _, pl := range plugins {
		if pl == nil || pl.Name == "" {
			continue
		}
		retention, ok := derivedRetention(pl)
		if !ok {
			// Unknown window. Leave the name unmapped so For() returns the
			// fallback, which is by construction the longest window in town.
			continue
		}
		p.byPlugin[pl.Name] = retention
		if retention > p.fallback {
			p.fallback = retention
		}
	}

	return p
}

// derivedRetention computes one plugin's retention, or ok=false when the gate
// declares a cooldown whose duration cannot be read.
func derivedRetention(pl *Plugin) (time.Duration, bool) {
	if pl.Gate == nil || pl.Gate.Type != GateCooldown {
		return MinReceiptRetention, true
	}
	if pl.Gate.Duration == "" {
		return 0, false
	}
	d, err := time.ParseDuration(pl.Gate.Duration)
	if err != nil || d < 0 {
		return 0, false
	}

	retention := d * ReceiptRetentionFactor
	// time.Duration is an int64 of nanoseconds; ParseDuration accepts values
	// whose double overflows. An overflowed retention is negative and would
	// delete everything, so fall back to the gate duration itself, which is
	// still arithmetically sufficient for the gate.
	if retention < d {
		retention = d
	}
	if retention < MinReceiptRetention {
		retention = MinReceiptRetention
	}
	return retention, true
}

// For returns the retention window for receipts labelled plugin:<name>.
func (p RetentionPolicy) For(pluginName string) time.Duration {
	if d, ok := p.byPlugin[pluginName]; ok {
		return d
	}
	return p.Fallback()
}

// Fallback is the window applied to receipts whose plugin is not in the
// discovered set: the longest retention any known plugin needs.
func (p RetentionPolicy) Fallback() time.Duration {
	if p.fallback < MinReceiptRetention {
		return MinReceiptRetention
	}
	return p.fallback
}

// RetentionEntry is one line of a rendered policy.
type RetentionEntry struct {
	Plugin    string `json:"plugin"`
	Gate      string `json:"gate,omitempty"`
	Retention string `json:"retention"`
}

// Entries renders the per-plugin policy, sorted by name, for `gt plugin prune`
// output. The policy is the part of this feature an operator has to be able to
// check before trusting a delete, so it is printed rather than implied.
//
// Every discovered plugin gets a line, including the ones with no derived
// window: those resolve to the fallback, and a plugin missing from the table
// entirely would look like one this policy does not govern rather than one whose
// gate could not be read.
func (p RetentionPolicy) Entries(plugins []*Plugin) []RetentionEntry {
	entries := make([]RetentionEntry, 0, len(plugins))
	seen := make(map[string]bool, len(plugins))

	for _, pl := range plugins {
		if pl == nil || pl.Name == "" || seen[pl.Name] {
			continue
		}
		seen[pl.Name] = true

		gate := ""
		if pl.Gate != nil {
			gate = string(pl.Gate.Type)
			if pl.Gate.Duration != "" {
				gate += " " + pl.Gate.Duration
			}
		}

		entry := RetentionEntry{Plugin: pl.Name, Gate: gate, Retention: p.For(pl.Name).String()}
		if _, derived := p.byPlugin[pl.Name]; !derived {
			entry.Retention += " (fallback: gate window unreadable)"
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Plugin < entries[j].Plugin })
	return entries
}

// ReceiptPruneOptions controls one prune run.
type ReceiptPruneOptions struct {
	// DryRun decides nothing differently — it only stops short of the delete.
	DryRun bool

	// Limit caps how many receipts one run deletes; <= 0 means no cap. The
	// daemon passes a cap so a first run on a town with thousands of stale
	// receipts cannot hold up a heartbeat tick; the CLI does not, because an
	// operator running `gt plugin prune` wants the whole job. A run that hits
	// the cap reports Deferred rather than finishing quietly.
	Limit int
}

// PrunedReceipt is one receipt and the decision made about it.
type PrunedReceipt struct {
	ID             string  `json:"id"`
	Plugin         string  `json:"plugin,omitempty"`
	CreatedAt      string  `json:"created_at"`
	AgeHours       float64 `json:"age_hours"`
	RetentionHours float64 `json:"retention_hours"`
	Reason         string  `json:"reason,omitempty"`
}

// ReceiptPruneResult accounts for every receipt the run looked at.
//
// Every scanned receipt lands in exactly one of Deleted/Held/Open/Kept/Deferred
// (plus Errors), and the printer says so when they do not add up. This mirrors
// gt compact's accounting for the same reason: a count that cannot distinguish
// "nothing was eligible" from "the query returned nothing" is the failure mode
// this whole area keeps producing.
type ReceiptPruneResult struct {
	Scanned int `json:"scanned"`
	// Deleted lists what was removed — or, in a dry run, what would be.
	Deleted []PrunedReceipt `json:"deleted"`
	// Held lists receipts past retention that were NOT deleted because a guard
	// applies: pinned, a reaper.ProtectedWispLabels label, a keep label, or a
	// comment. Listed rather than counted so a run that declines to delete says
	// which records it declined.
	Held []PrunedReceipt `json:"held,omitempty"`
	// Open counts receipts that are not closed. RecordRun closes a receipt the
	// moment it writes it, so a non-zero value here is a report about a failed
	// close (or a foreign writer), not a normal state — and deleting an open
	// receipt is not this command's job either way.
	Open int `json:"open"`
	// Kept counts receipts still inside their retention window.
	Kept int `json:"kept"`
	// Deferred counts receipts that were eligible but beyond Limit this run.
	Deferred int `json:"deferred"`
	// Remaining is a re-read: how many receipts are in the table after the run.
	// It is the control on the numbers above, which are derived from what this
	// process believes it did.
	Remaining int      `json:"remaining"`
	Errors    []string `json:"errors,omitempty"`
}

// receiptRow is one row of the receipts projection.
type receiptRow struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	Pinned       int    `json:"pinned"`
	CommentCount int    `json:"comment_count"`
	LabelsCSV    string `json:"labels_csv"`
}

// receiptSelectSQL projects every plugin-run receipt with the columns needed to
// judge it.
//
// created_at, not updated_at: the cooldown gate queries `--created-after`, so
// creation time is the age that decides whether a receipt is still load-bearing.
// Receipts are closed within milliseconds of creation, so the two agree today —
// but if a receipt is ever touched again, updated_at would extend its life for
// no reason related to the gate that reads it.
//
// The timestamp is selected BARE. bd serialises a datetime column to RFC3339
// when selected directly and to a space-separated form once an expression wraps
// it (gt-ktvs); parseReceiptTime accepts both, and the projection avoids
// producing the second form in the first place.
//
// The side tables are pre-aggregated into derived tables and LEFT JOINed rather
// than queried as correlated subqueries: on hq, which holds 28k wisps, the
// correlated form is what put `gt compact` past the 60s bd subprocess timeout
// (gt-g60l). The inner JOIN on the type label is what restricts this to
// receipts.
const receiptSelectSQL = `SELECT w.id, w.status, w.created_at, ` +
	`COALESCE(w.pinned, 0) AS pinned, ` +
	`COALESCE(c.comment_count, 0) AS comment_count, ` +
	`COALESCE(l.labels_csv, '') AS labels_csv ` +
	`FROM wisps w ` +
	`JOIN (SELECT DISTINCT issue_id FROM wisp_labels WHERE label = '` + receiptTypeLabel + `') t ON t.issue_id = w.id ` +
	`LEFT JOIN (SELECT issue_id, COUNT(*) AS comment_count FROM wisp_comments GROUP BY issue_id) c ON c.issue_id = w.id ` +
	`LEFT JOIN (SELECT issue_id, GROUP_CONCAT(label) AS labels_csv FROM wisp_labels GROUP BY issue_id) l ON l.issue_id = w.id ` +
	`ORDER BY w.created_at`

// receiptCountSQL counts what is left, for the post-run re-read.
const receiptCountSQL = `SELECT COUNT(*) AS n FROM wisps w ` +
	`JOIN (SELECT DISTINCT issue_id FROM wisp_labels WHERE label = '` + receiptTypeLabel + `') t ON t.issue_id = w.id`

// receiptTypeLabel is the label RecordRun stamps on every receipt. Declared
// here as well so the queries and the writer cannot drift apart silently.
const receiptTypeLabel = "type:plugin-run"

// pluginLabelPrefix identifies which plugin a receipt belongs to.
const pluginLabelPrefix = "plugin:"

// PruneReceipts deletes plugin run receipts that have outlived every gate that
// could read them.
//
// It is deliberately in this package and not in `gt compact`. Compaction
// classifies a wisp by wisp_type and applies one of seven fixed TTLs; the
// window a receipt needs is a function of plugin gate durations, which no
// member of that vocabulary expresses and which compaction has no way to learn.
// Leaving wisp_type empty keeps compaction out (gt-ktvs) and leaves the
// deletion where the knowledge is.
//
// now is passed in rather than read so the policy is testable against fixed
// timestamps.
func (r *Recorder) PruneReceipts(policy RetentionPolicy, now time.Time, opts ReceiptPruneOptions) (*ReceiptPruneResult, error) {
	rows, err := r.listReceiptRows()
	if err != nil {
		// Deliberately fatal, not an empty result. A prune that cannot read the
		// table must not report "nothing to do" — that is the exact shape of
		// gt-ktvs, where an unreadable database and a tidy one printed the same
		// clean summary for months.
		return nil, err
	}

	result := &ReceiptPruneResult{Scanned: len(rows)}
	var eligible []PrunedReceipt

	for _, row := range rows {
		labels := splitLabels(row.LabelsCSV)
		pluginName := receiptPluginName(labels)
		retention := policy.For(pluginName)

		if row.Status != "closed" {
			result.Open++
			continue
		}

		created, parseErr := parseReceiptTime(row.CreatedAt)
		if parseErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", row.ID, parseErr))
			continue
		}
		age := now.Sub(created)

		record := PrunedReceipt{
			ID:             row.ID,
			Plugin:         pluginName,
			CreatedAt:      row.CreatedAt,
			AgeHours:       age.Hours(),
			RetentionHours: retention.Hours(),
		}

		if age <= retention {
			result.Kept++
			continue
		}

		if guard := receiptProtection(row, labels); guard != "" {
			record.Reason = guard
			result.Held = append(result.Held, record)
			continue
		}

		record.Reason = fmt.Sprintf("past retention (%s > %s)", age.Round(time.Hour), retention)
		eligible = append(eligible, record)
	}

	// The listing is ordered by created_at, so a capped run takes the oldest
	// receipts first and the remainder is reported, not dropped silently.
	if opts.Limit > 0 && len(eligible) > opts.Limit {
		result.Deferred = len(eligible) - opts.Limit
		eligible = eligible[:opts.Limit]
	}

	if opts.DryRun {
		result.Deleted = eligible
		result.Remaining = r.countReceipts(result)
		return result, nil
	}

	result.Deleted = r.deleteReceipts(eligible, result)
	result.Remaining = r.countReceipts(result)
	return result, nil
}

// listReceiptRows reads every plugin-run receipt from the wisps table.
//
// The wisps table, not `bd list`: bd list does not query it, which is why the
// same mistake made `gt compact` blind for the life of the command (gt-ktvs).
// The labels, the pinned column and the comment count that decide protection
// are likewise absent from bd list's issue-shaped output.
func (r *Recorder) listReceiptRows() ([]receiptRow, error) {
	out, err := r.runBD(constants.BdCommandTimeout, beads.ReadOnlyPinned, "sql", "--json", receiptSelectSQL)
	if err != nil {
		return nil, fmt.Errorf("querying plugin receipts: %w", err)
	}

	var rows []receiptRow
	if err := json.Unmarshal(extractJSONArray(out), &rows); err != nil {
		return nil, fmt.Errorf("parsing plugin receipts: %w", err)
	}
	return rows, nil
}

// countReceipts re-reads the receipt count after the run. Errors are recorded
// and reported as -1 rather than failing the run: the deletes already happened,
// and a failed control is worth saying out loud, not worth discarding the
// result over.
func (r *Recorder) countReceipts(result *ReceiptPruneResult) int {
	out, err := r.runBD(constants.BdCommandTimeout, beads.ReadOnlyPinned, "sql", "--json", receiptCountSQL)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("counting remaining receipts: %v", err))
		return -1
	}
	var counts []struct {
		N int `json:"n"`
	}
	if err := json.Unmarshal(extractJSONArray(out), &counts); err != nil || len(counts) == 0 {
		result.Errors = append(result.Errors, fmt.Sprintf("parsing remaining receipt count: %v", err))
		return -1
	}
	return counts[0].N
}

// deleteReceipts removes the eligible receipts in batches and returns the ones
// that are genuinely gone.
//
// Every batch is verified by re-reading its own IDs. `bd delete` takes many IDs
// and reports one exit status for the lot, so a nil error is not evidence that
// each ID went: counting the batch as deleted because the command succeeded is
// the same mistake as counting `bd close a b c` (gt-3xmz), which exits 0 when
// ANY id closed. What survives the batch is reported as an error against the
// specific IDs, so the next run retries exactly those.
func (r *Recorder) deleteReceipts(eligible []PrunedReceipt, result *ReceiptPruneResult) []PrunedReceipt {
	deleted := make([]PrunedReceipt, 0, len(eligible))

	for start := 0; start < len(eligible); start += receiptPruneChunk {
		end := start + receiptPruneChunk
		if end > len(eligible) {
			end = len(eligible)
		}
		batch := eligible[start:end]

		ids := make([]string, 0, len(batch))
		for _, rec := range batch {
			ids = append(ids, rec.ID)
		}

		args := append([]string{"delete"}, ids...)
		args = append(args, "--force")
		if _, err := r.runBD(receiptDeleteTimeout, beads.MutationPinned, args...); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("deleting %d receipt(s) starting at %s: %v", len(ids), ids[0], err))
			// Fall through to the survivor check anyway: a batch can fail
			// partway and still have removed rows, and the check is what says
			// which.
		}

		survivors, err := r.survivingReceipts(ids)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("verifying deletion of %d receipt(s): %v", len(ids), err))
			continue // Claim nothing this batch — unverified is not deleted.
		}
		for _, rec := range batch {
			if _, alive := survivors[rec.ID]; alive {
				result.Errors = append(result.Errors, fmt.Sprintf("receipt %s survived delete", rec.ID))
				continue
			}
			deleted = append(deleted, rec)
		}
	}

	return deleted
}

// survivingReceipts returns which of the given IDs are still in the wisps table.
func (r *Recorder) survivingReceipts(ids []string) (map[string]struct{}, error) {
	list, err := sqlIDList(ids)
	if err != nil {
		return nil, err
	}
	out, err := r.runBD(constants.BdCommandTimeout, beads.ReadOnlyPinned, "sql", "--json",
		"SELECT id FROM wisps WHERE id IN ("+list+")")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(extractJSONArray(out), &rows); err != nil {
		return nil, err
	}
	survivors := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		survivors[row.ID] = struct{}{}
	}
	return survivors, nil
}

// runBD executes one bd command against the town database.
//
// Same route as RecordRun: BEADS_DIR pinned to the town's beads directory
// rather than inherited from cwd. Receipts are written to the town database
// from whatever directory a dog or the daemon happens to be in, and a `-C`-style
// working-directory change does not set write routing (it fails toward apparent
// success), so the pinned env is the only route that reliably opens the same
// database the writer used.
func (r *Recorder) runBD(timeout time.Duration, mode beads.SubprocessEnvMode, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := beads.CommandContext(ctx, r.townRoot, beads.ResolveBeadsDir(r.townRoot), mode, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("bd %s: %s: %w", args[0], detail, err)
	}
	return stdout.Bytes(), nil
}

// receiptProtection returns the guard forbidding deletion of a receipt, or ""
// if none applies.
//
// The first two mirror reaper.purgeProtectWhere and gt compact's wispProtection
// — the same guards in the same order, reading the same exported label list, so
// the town's deleters cannot disagree about what is undeletable. gt-6dp is the
// record of what a guard that holds on one delete path and is inert on the next
// costs; this is the fourth path, and it imports the list rather than copying it.
//
// The last two are compaction's "proven value" test. A receipt with a comment or
// a keep label has been touched by somebody deliberately, and a routine TTL is
// not entitled to overrule that.
func receiptProtection(row receiptRow, labels []string) string {
	if row.Pinned != 0 {
		return "pinned"
	}
	for _, label := range labels {
		for _, protected := range reaper.ProtectedWispLabels {
			if label == protected {
				return "protected label " + protected
			}
		}
	}
	for _, label := range labels {
		if label == "keep" || label == "gt:keep" {
			return "keep label " + label
		}
	}
	if row.CommentCount > 0 {
		return "has comments"
	}
	return ""
}

// receiptPluginName extracts the plugin:<name> label value, or "" when the
// receipt carries none. An empty name resolves to the policy's fallback, which
// is the longest window in town.
func receiptPluginName(labels []string) string {
	for _, label := range labels {
		if strings.HasPrefix(label, pluginLabelPrefix) {
			return strings.TrimPrefix(label, pluginLabelPrefix)
		}
	}
	return ""
}

// splitLabels turns the GROUP_CONCAT csv back into labels.
func splitLabels(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	labels := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			labels = append(labels, p)
		}
	}
	return labels
}

// parseReceiptTime accepts both layouts bd emits for a datetime column.
func parseReceiptTime(ts string) (time.Time, error) {
	if ts == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parsing timestamp %q: no known layout matches", ts)
}

// sqlIDList renders IDs as a SQL IN(...) body.
//
// The IDs come from the wisps table this process just read, so a quote in one
// means the caller parsed the wrong token out of command output; interpolating
// it would be an injection, so it is an error instead (same rule as
// beads.WispTypeUpdateSQL).
func sqlIDList(ids []string) (string, error) {
	if len(ids) == 0 {
		return "", fmt.Errorf("no receipt IDs given")
	}
	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			return "", fmt.Errorf("empty receipt ID")
		}
		if strings.ContainsAny(id, "'\"\\`") {
			return "", fmt.Errorf("refusing to build SQL for receipt ID %q: contains a quote", id)
		}
		quoted = append(quoted, "'"+id+"'")
	}
	return strings.Join(quoted, ", "), nil
}

// extractJSONArray strips any non-JSON prefix (bd warnings, notices) emitted to
// stdout before the payload.
func extractJSONArray(data []byte) []byte {
	if idx := bytes.IndexByte(data, '['); idx >= 0 {
		return data[idx:]
	}
	return data
}
