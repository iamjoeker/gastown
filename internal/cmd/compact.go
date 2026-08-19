package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/reaper"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/wisp"
)

var (
	compactDryRun  bool
	compactVerbose bool
	compactJSON    bool
	compactRig     string
)

// Default TTLs per wisp type (from design doc WISP-COMPACTION-POLICY.md).
var defaultTTLs = map[string]time.Duration{
	"heartbeat":  6 * time.Hour,
	"ping":       6 * time.Hour,
	"patrol":     24 * time.Hour,
	"gc_report":  24 * time.Hour,
	"recovery":   7 * 24 * time.Hour,
	"error":      7 * 24 * time.Hour,
	"escalation": 7 * 24 * time.Hour,
	"default":    24 * time.Hour,
}

// compactResult tracks what happened to each wisp during compaction.
type compactResult struct {
	// Scanned is how many wisps the run actually looked at. It exists because
	// gt-ktvs: for as long as listWisps sourced its input from `bd list`, which
	// does not return the wisps table, every run reported
	// {promoted: null, deleted: null, skipped: 0} — output identical to a clean
	// run over a tidy database, while 700 wisps sat unexamined. A zero in any of
	// the outcome fields is only meaningful next to the size of the input that
	// produced it, so the input size is now part of the record.
	Scanned  int             `json:"scanned"`
	Promoted []compactAction `json:"promoted"`
	Deleted  []compactAction `json:"deleted"`
	// Protected lists wisps past TTL that compaction declined to delete because
	// they carry a reaper.ProtectedWispLabels label or are pinned. Reported as
	// its own list rather than folded into Skipped so a run that declines to
	// delete SAYS so: gt-6dp's recurring shape is a count that cannot
	// distinguish "protected N" from "there was less to do".
	Protected []compactAction `json:"protected,omitempty"`
	Skipped   int             `json:"skipped"` // wisps still within TTL
	// Unclassified counts wisps whose wisp_type is empty, for which no TTL
	// policy can be selected. Counted rather than listed: on a real rig this is
	// currently every wisp, and a 700-element list would bloat the daily digest
	// that embeds this struct. A non-zero value here is a defect report about
	// whatever wrote those wisps, not a normal steady state.
	Unclassified     int      `json:"unclassified"`
	OrphanedWispDeps int      `json:"orphaned_wisp_deps"` // stale wisp_dependencies removed
	Errors           []string `json:"errors,omitempty"`
}

type compactAction struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Reason   string `json:"reason"`
	WispType string `json:"wisp_type,omitempty"`
}

var compactCmd = &cobra.Command{
	Use:     "compact",
	GroupID: GroupWork,
	Short:   "Compact expired wisps (TTL-based cleanup)",
	Long: `Apply TTL-based compaction policy to ephemeral wisps.

For non-closed wisps past TTL: promotes to permanent beads (something is stuck).
For closed wisps past TTL: deletes them PERMANENTLY. Wisp tables are dolt-ignored,
so there is no history to read AS OF and no backup to restore from.
Wisps with comments or keep labels are always promoted.
Pinned wisps, and wisps carrying a protected label (merge-request records),
are never deleted.

TTLs by wisp type:
  heartbeat, ping:              6h
  patrol, gc_report:            24h
  recovery, error, escalation:  7d
  any other named type:         24h

A wisp with an EMPTY wisp_type is reported as unclassified and left alone. No
TTL policy can be selected for it, and 24h is not a safe guess for a row that
might be a 7d escalation record. The "Unclassified: N" line is a defect report
about whatever wrote those wisps without a type, not a normal steady state.

Examples:
  gt compact              # Run compaction
  gt compact --dry-run    # Preview what would happen
  gt compact --verbose    # Show each wisp decision
  gt compact --json       # Machine-readable output`,
	RunE: runCompact,
}

func init() {
	compactCmd.Flags().BoolVar(&compactDryRun, "dry-run", false, "Preview compaction without making changes")
	compactCmd.Flags().BoolVarP(&compactVerbose, "verbose", "v", false, "Show each wisp decision")
	compactCmd.Flags().BoolVar(&compactJSON, "json", false, "Output results as JSON")
	compactCmd.Flags().StringVar(&compactRig, "rig", "", "Compact a specific rig (default: current rig)")

	rootCmd.AddCommand(compactCmd)
}

// loadTTLConfig loads TTL configuration with layered precedence:
//
//	rig config (wisp layer + bead labels) > hardcoded defaults
func loadTTLConfig(townRoot, rigName string) map[string]time.Duration {
	return loadTTLConfigWithRole(townRoot, rigName)
}

// loadTTLConfigWithRole is the testable version of loadTTLConfig.
func loadTTLConfigWithRole(townRoot, rigName string) map[string]time.Duration {
	// Layer 1: Hardcoded defaults (lowest precedence)
	ttls := make(map[string]time.Duration)
	for k, v := range defaultTTLs {
		ttls[k] = v
	}

	if townRoot == "" {
		return ttls
	}

	// Layer 2: Rig config - wisp layer (middle precedence)
	if rigName != "" {
		cfg := wisp.NewConfig(townRoot, rigName)
		raw := cfg.Get("wisp_ttl")
		if raw != nil {
			// wisp_ttl is stored as map[string]interface{} in JSON config
			if ttlMap, ok := raw.(map[string]interface{}); ok {
				for wispType, val := range ttlMap {
					if s, ok := val.(string); ok {
						if d, err := time.ParseDuration(s); err == nil {
							ttls[wispType] = d
						}
					}
				}
			}
		}

		// Layer 2b: Rig identity bead labels (wisp_ttl_*:value)
		applyRigBeadTTLOverrides(ttls, townRoot, rigName)
	}

	return ttls
}

// applyRigBeadTTLOverrides reads wisp_ttl_* labels from the rig identity bead
// and applies them as overrides.
func applyRigBeadTTLOverrides(ttls map[string]time.Duration, townRoot, rigName string) {
	beadsDir := beads.ResolveBeadsDir(townRoot)
	bd := beads.NewWithBeadsDir(townRoot, beadsDir)

	rigBeadID := beads.RigBeadIDWithPrefix("gt", rigName)
	issue, err := bd.Show(rigBeadID)
	if err != nil {
		return
	}

	for _, label := range issue.Labels {
		colonIdx := strings.Index(label, ":")
		if colonIdx == -1 {
			continue
		}
		key := strings.ToLower(label[:colonIdx])
		value := strings.TrimSpace(label[colonIdx+1:])

		if wispType, ok := beads.ParseWispTTLKey(key); ok {
			if dur, err := time.ParseDuration(value); err == nil {
				ttls[wispType] = dur
			}
		}
	}
}

// getTTL returns the TTL for a wisp based on its wisp_type field, and whether
// the type is classified at all.
//
// An EMPTY wisp_type is NOT the "default" type. It is a wisp whose type was
// never recorded, and no policy can be selected for it — so this reports
// classified=false and the caller must not act on age.
//
// This distinction is load-bearing, not pedantry (gt-ktvs). Measured on the
// gastown rig 2026-08-19, 703 of 703 wisps carry an empty wisp_type. The old
// behaviour — silently reading "" as "default" = 24h — was harmless only for as
// long as listWisps was blind and this function was never reached with real
// data. Repairing the blindness without repairing this would have applied 24h
// to every escalation, recovery and error wisp on the rig, whose configured TTL
// is 7d, and deleted a week of records on the first successful run. The
// unclassified rows are a defect in whatever writes wisps; compaction's job is
// to refuse to guess and say how many there were, not to pick a number.
//
// A non-empty type with no configured TTL still falls back to "default": the
// type was deliberately written, so "default" is the documented policy for it
// rather than a guess about missing data.
func getTTL(ttls map[string]time.Duration, wispType string) (ttl time.Duration, classified bool) {
	if wispType == "" {
		return 0, false
	}
	if d, ok := ttls[wispType]; ok {
		return d, true
	}
	return ttls["default"], true
}

// compactIssue is a wisp as compaction sees it: the shared bead fields plus the
// wisp-table columns bd's issue-shaped output does not carry.
type compactIssue struct {
	beads.Issue
	CommentCount int    `json:"comment_count"`
	WispType     string `json:"wisp_type,omitempty"`
	// Pinned is the wisps.pinned column — the guard an incident responder sets
	// by hand to protect one specific record right now. `bd purge` and the
	// reaper's native delete both honour it. This path could not, because
	// `bd list --json` does not return the column; reading the wisps table
	// directly removes that excuse.
	Pinned bool `json:"pinned,omitempty"`
}

func runCompact(cmd *cobra.Command, args []string) error {
	now := time.Now().UTC()

	// Resolve working directory and town root
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working dir: %w", err)
	}

	townRoot := beads.FindTownRoot(workDir)
	rigName := compactRig
	if rigName == "" {
		rigName = os.Getenv("GT_RIG")
	}

	// Load TTL config
	ttls := loadTTLConfig(townRoot, rigName)

	// Query all ephemeral (wisp) issues via bd list.
	// Role directories (<town>/deacon, <town>/mayor) have no .beads of their
	// own, so cwd alone resolves to a database that does not exist — which is
	// why gt compact used to work only from the town root.
	bd := beads.New(beads.BeadsWorkDirWithTownFallback(workDir, townRoot))
	allWisps, err := listWisps(bd)
	if err != nil {
		return fmt.Errorf("listing wisps: %w", err)
	}

	if !compactJSON && !compactDryRun {
		fmt.Printf("Compacting %d wisps...\n", len(allWisps))
	}

	result := &compactResult{Scanned: len(allWisps)}

	for _, w := range allWisps {
		age, err := wispAge(w, now)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", w.ID, err))
			continue
		}

		verdict := decideWisp(w, age, ttls)
		switch verdict.action {
		case actionPromote:
			promoteWisp(bd, w, verdict.reason, result)
		case actionDelete:
			deleteWisp(bd, w, verdict.reason, result)
		case actionUnclassified:
			result.Unclassified++
			if compactVerbose && !compactJSON {
				fmt.Printf("  %s %s %s (%s)\n",
					style.Dim.Render("untyped"), w.ID, compactTruncate(w.Title, 40), verdict.reason)
			}
		case actionSkip:
			result.Skipped++
			if compactVerbose && !compactJSON {
				fmt.Printf("  skip  %s %s (age: %s, ttl: %s)\n",
					w.ID, compactTruncate(w.Title, 40), age.Round(time.Minute), verdict.ttl)
			}
		}
	}

	// Clean up orphaned wisp_dependencies left behind by deleted wisps.
	// When bd delete removes a wisp, it doesn't cascade-delete dependency
	// records in wisp_dependencies that reference the deleted wisp. Over many
	// compaction cycles these accumulate as dangling refs. We sweep them here.
	if !compactDryRun {
		cleanOrphanedWispDeps(bd, result)
	}

	// Output results
	if compactJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	printCompactSummary(result)
	return nil
}

// wispAction is what compaction has decided to do with one wisp.
type wispAction int

const (
	actionSkip wispAction = iota
	actionUnclassified
	actionPromote
	actionDelete
)

// wispVerdict is decideWisp's answer: what to do, why, and the TTL that decided
// it (zero when no TTL applied).
type wispVerdict struct {
	action wispAction
	reason string
	ttl    time.Duration
}

// decideWisp is the whole compaction policy, as a pure function.
//
// It is separated from runCompact because the policy is the part worth testing
// and runCompact is the part that needs a database, a bd binary and a live
// wisps table to reach. Everything below is decided from the wisp, its age and
// the TTL table alone.
func decideWisp(w *compactIssue, age time.Duration, ttls map[string]time.Duration) wispVerdict {
	// Molecule step wisps (those with a Parent) should never be promoted.
	// They are subordinate steps of a molecule and should be deleted when
	// past TTL, not elevated to permanent beads. This prevents patrol
	// molecule steps from polluting the issues table.
	isMoleculeStep := w.Parent != ""

	// Proven value is a property of the wisp, not of its age, so it is decided
	// before any TTL is consulted — which is also what keeps a kept-or-commented
	// wisp reachable when its type is unclassified.
	if (hasComments(w) || hasKeepLabel(w)) && !isMoleculeStep {
		return wispVerdict{action: actionPromote, reason: "proven value"}
	}

	ttl, classified := getTTL(ttls, w.WispType)
	if !classified {
		// No TTL policy applies, so there is no "past TTL" to be past. Leaving
		// the wisp alone is the only safe action; see getTTL.
		return wispVerdict{action: actionUnclassified, reason: "empty wisp_type — no TTL policy applies"}
	}

	if age <= ttl {
		return wispVerdict{action: actionSkip, reason: "within TTL", ttl: ttl}
	}

	switch {
	case w.Status == "closed":
		return wispVerdict{action: actionDelete, reason: "TTL expired", ttl: ttl}
	case isMoleculeStep:
		return wispVerdict{action: actionDelete, reason: "molecule step past TTL", ttl: ttl}
	case w.Status == "in_progress":
		return wispVerdict{action: actionPromote, reason: "stuck in_progress past TTL", ttl: ttl}
	default:
		return wispVerdict{action: actionPromote, reason: "open past TTL", ttl: ttl}
	}
}

// cleanOrphanedWispDeps removes wisp_dependencies rows where either side no
// longer exists in the wisps table. This happens when bd delete removes a wisp
// but leaves behind its dependency records (bd delete has no cascade logic for
// the wisp-level tables). Runs as a post-compact sweep.
func cleanOrphanedWispDeps(bd *beads.Beads, result *compactResult) {
	columns, err := bd.Run("sql", "--csv", "SHOW COLUMNS FROM wisp_dependencies")
	if err != nil || !strings.Contains(string(columns), "\ndepends_on_wisp_id,") || !strings.Contains(string(columns), "\ndepends_on_issue_id,") {
		return
	}
	const q = `DELETE FROM wisp_dependencies WHERE ` +
		`NOT EXISTS (SELECT 1 FROM wisps WHERE id = wisp_dependencies.issue_id) ` +
		`OR (depends_on_wisp_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM wisps WHERE id = wisp_dependencies.depends_on_wisp_id)) ` +
		`OR (depends_on_issue_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM issues WHERE id = wisp_dependencies.depends_on_issue_id))`
	out, err := bd.Run("sql", q)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("orphaned wisp_deps cleanup: %v", err))
		return
	}
	// bd sql reports "OK, N rows affected" for non-SELECT statements.
	// Parse the count if present; a non-zero result means refs were cleaned.
	var n int
	if _, scanErr := fmt.Sscanf(strings.TrimSpace(string(out)), "OK, %d rows affected", &n); scanErr == nil {
		result.OrphanedWispDeps = n
	}
}

// wispSelectColumns is the projection both wisp listers share.
//
// Timestamps are selected BARE and never wrapped. bd serialises a datetime
// column to RFC3339 ("2026-08-19T02:29:03Z") when it is selected directly, but
// to a space-separated form ("2026-08-19 02:29:03") once an expression such as
// COALESCE() forces it through string conversion. wispAge accepts both, but
// only one of them is what the rest of gastown means by a timestamp.
//
// comment_count, labels and parent come from the pre-aggregated derived tables
// in wispSideJoins, one row per issue_id, so joining them still yields exactly
// one row per wisp without a GROUP BY over every selected column.
//
// They used to be three correlated subqueries instead, which is the same result
// at ~3 executions per output row. That is invisible on a rig and fatal on the
// town database (gt-g60l): at 28.5k rows it is ~85k subquery runs, and the whole
// of `gt compact` died on the 60s bd subprocess timeout — so hq, which holds 28k
// of the ~29.5k wisps town-wide, was the one database compaction could not open.
// Measured against live hq on 2026-08-19: correlated >120s (killed), joined
// 0.18s, byte-identical output on every database that could run both.
const wispSelectColumns = `w.id, w.title, w.status, w.issue_type, ` +
	`COALESCE(w.wisp_type, '') AS wisp_type, ` +
	`w.created_at, w.updated_at, ` +
	`COALESCE(w.pinned, 0) AS pinned, ` +
	`COALESCE(c.comment_count, 0) AS comment_count, ` +
	`COALESCE(l.labels_csv, '') AS labels_csv, ` +
	`COALESCE(d.parent, '') AS parent`

// wispSideJoins supplies the three aggregate columns above.
//
// Each derived table groups its side table by issue_id before the join, so it
// is scanned once per query rather than once per wisp. LEFT JOIN keeps wisps
// with no comments, no labels and no parent, which the COALESCEs above then
// render as the same zero values the correlated form produced.
//
// parent is MIN() rather than the old `LIMIT 1`: an aggregate is required to
// collapse the group, and where the old form picked an arbitrary row of a
// multi-parent wisp this picks a deterministic one. No wisp in any live
// database has more than one parent-child dependency, so the two agree today
// and MIN only makes the tie-break stable if that ever changes.
const wispSideJoins = `LEFT JOIN (SELECT issue_id, COUNT(*) AS comment_count ` +
	`FROM wisp_comments GROUP BY issue_id) c ON c.issue_id = w.id ` +
	`LEFT JOIN (SELECT issue_id, GROUP_CONCAT(label) AS labels_csv ` +
	`FROM wisp_labels GROUP BY issue_id) l ON l.issue_id = w.id ` +
	`LEFT JOIN (SELECT issue_id, MIN(depends_on_wisp_id) AS parent ` +
	`FROM wisp_dependencies WHERE type = 'parent-child' AND depends_on_wisp_id IS NOT NULL ` +
	`GROUP BY issue_id) d ON d.issue_id = w.id`

// wispRow is one row of the wisps-table projection above. It is separate from
// compactIssue because the SQL column names (labels_csv, pinned as 0/1) do not
// match the bead JSON shape compactIssue is built from.
type wispRow struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	IssueType    string `json:"issue_type"`
	WispType     string `json:"wisp_type"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	Pinned       int    `json:"pinned"`
	CommentCount int    `json:"comment_count"`
	LabelsCSV    string `json:"labels_csv"`
	Parent       string `json:"parent"`
}

// listWisps returns the wisps compaction is allowed to act on.
//
// It reads the wisps table directly. It used to read `bd list --json --all` and
// keep the rows with ephemeral=true, which returned nothing on any current
// database and had done so silently for the life of the command (gt-ktvs):
// wisps live in their own table and bd list does not query it, so the filter
// could not match. Measured on the gastown rig 2026-08-19, that path returned
// 222 issue rows — none carrying an `ephemeral` key at all — against 703 wisps.
// The failure was worse than a missing filter: wisp_type, labels and parent are
// likewise absent from bd list's output, so even a matching row would have been
// judged on zero values.
//
// Infra types are excluded. bd list's default view hides them, so the old code
// was shielded from them by accident; reading the table removes that shield and
// the exclusion has to be deliberate. Agent beads in particular are persistent
// identity, which is why the reaper refuses to touch them too — a compaction
// pass that deleted one would be deleting an agent, not a record of one.
// listReportWisps deliberately keeps them, because counting them is harmless.
func listWisps(bd *beads.Beads) ([]*compactIssue, error) {
	return queryWisps(bd, mutableWispWhere())
}

// mutableWispWhere restricts compaction's input to wisps it may mutate: not the
// infra types, whose rows are live identity and routing state rather than
// records of past activity. Built from constants.BeadsInfraTypesList so adding
// an infra type protects it here without a second edit.
func mutableWispWhere() string {
	types := constants.BeadsInfraTypesList()
	quoted := make([]string, len(types))
	for i, t := range types {
		quoted[i] = "'" + t + "'"
	}
	return "WHERE w.issue_type NOT IN (" + strings.Join(quoted, ", ") + ")"
}

// wispQuery assembles the shared projection with an optional WHERE clause.
//
// The WHERE goes after the joins, which is where it has to be and also where it
// is harmless: every caller filters on columns of w alone, so it prunes the left
// side and cannot turn a LEFT JOIN back into an inner one.
func wispQuery(where string) string {
	q := "SELECT " + wispSelectColumns + " FROM wisps w " + wispSideJoins + " "
	if where != "" {
		q += where + " "
	}
	return q + "ORDER BY w.id"
}

// queryWisps runs the shared projection with an optional WHERE clause.
func queryWisps(bd *beads.Beads, where string) ([]*compactIssue, error) {
	out, err := bd.Run("sql", "--json", wispQuery(where))
	if err != nil {
		// Deliberately fatal. The whole of gt-ktvs is that this command reported
		// an unreadable database and an empty one with the same output; swapping
		// a hard failure for a silent zero here would reintroduce it by a
		// different route.
		return nil, fmt.Errorf("querying wisps table: %w", err)
	}

	// Strip any non-JSON prefix (warnings, notices) that bd may emit to
	// stdout before the JSON array. Without this, unicode characters like
	// emoji in wisp subjects can trigger "invalid character looking for
	// beginning of value" errors when a warning line contains non-ASCII.
	return parseWispRows(extractJSONArray(out))
}

// parseWispRows converts the wisps-table JSON into compaction's view of a wisp.
func parseWispRows(data []byte) ([]*compactIssue, error) {
	var rows []wispRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("parsing wisp list: %w", err)
	}

	wisps := make([]*compactIssue, 0, len(rows))
	for _, row := range rows {
		w := &compactIssue{
			Issue: beads.Issue{
				ID:        row.ID,
				Title:     row.Title,
				Status:    row.Status,
				Type:      row.IssueType,
				CreatedAt: row.CreatedAt,
				UpdatedAt: row.UpdatedAt,
				Parent:    row.Parent,
				Ephemeral: true,
			},
			CommentCount: row.CommentCount,
			WispType:     row.WispType,
			Pinned:       row.Pinned != 0,
		}
		if row.LabelsCSV != "" {
			w.Labels = strings.Split(row.LabelsCSV, ",")
		}
		wisps = append(wisps, w)
	}

	return wisps, nil
}

// extractJSONArray finds the first '[' byte in data and returns from that
// point onward. This strips any non-JSON prefix (warning messages, notices)
// that a subprocess may emit to stdout before the actual JSON payload.
// Returns the original data unchanged if no '[' is found.
func extractJSONArray(data []byte) []byte {
	idx := bytes.IndexByte(data, '[')
	if idx < 0 {
		return data
	}
	return data[idx:]
}

// promoteWisp makes a wisp permanent by setting --persistent and adding a comment.
func promoteWisp(bd *beads.Beads, w *compactIssue, reason string, result *compactResult) {
	action := compactAction{ID: w.ID, Title: w.Title, Reason: reason, WispType: w.WispType}

	if compactDryRun {
		result.Promoted = append(result.Promoted, action)
		if !compactJSON {
			fmt.Printf("  %s promote %s %s (%s)\n",
				style.Dim.Render("[dry-run]"), w.ID, compactTruncate(w.Title, 40), reason)
		}
		return
	}

	// bd update --persistent sets ephemeral=false
	_, err := bd.Run("update", w.ID, "--persistent")
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("promote %s: %v", w.ID, err))
		return
	}

	// Add comment noting the promotion
	_, _ = bd.Run("comments", "add", w.ID, fmt.Sprintf("Promoted from Level 0: %s", reason))

	result.Promoted = append(result.Promoted, action)

	if compactVerbose && !compactJSON {
		fmt.Printf("  %s %s %s (%s)\n",
			style.Success.Render("promote"), w.ID, compactTruncate(w.Title, 40), reason)
	}
}

// deleteWisp removes a closed wisp that has expired past its TTL.
func deleteWisp(bd *beads.Beads, w *compactIssue, reason string, result *compactResult) {
	// The guard lives in the callee, not at the three call sites, deliberately.
	// gt-6dp's post-mortem names reading the callee instead of the caller as
	// "the whole lesson": the unbounded `bd purge --force` survived review
	// because each caller looked reasonable in isolation. A call site added
	// later inherits this check for free; it would not inherit a copy of the
	// check written above each existing call.
	if guard := wispProtection(w); guard != "" {
		protected := compactAction{
			ID:       w.ID,
			Title:    w.Title,
			Reason:   fmt.Sprintf("%s (would have been: %s)", guard, reason),
			WispType: w.WispType,
		}
		result.Protected = append(result.Protected, protected)
		if compactVerbose && !compactJSON {
			fmt.Printf("  %s %s %s (%s)\n",
				style.Dim.Render("protect"), w.ID, compactTruncate(w.Title, 40), protected.Reason)
		}
		return
	}

	action := compactAction{ID: w.ID, Title: w.Title, Reason: reason, WispType: w.WispType}

	if compactDryRun {
		result.Deleted = append(result.Deleted, action)
		if !compactJSON {
			fmt.Printf("  %s delete  %s %s (%s)\n",
				style.Dim.Render("[dry-run]"), w.ID, compactTruncate(w.Title, 40), reason)
		}
		return
	}

	// bd delete --force. NOT recoverable for wisps: wisp tables are dolt-ignored
	// (hq-del4), so there is no history to read AS OF and no backup to restore
	// from. --force is also the documented deliberate override that BYPASSES the
	// gc.protected_labels skip `bd purge` applies, which is why this path needs
	// protectedWispLabel above rather than inheriting bd's protection.
	_, err := bd.Run("delete", w.ID, "--force")
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("delete %s: %v", w.ID, err))
		return
	}

	result.Deleted = append(result.Deleted, action)

	if compactVerbose && !compactJSON {
		fmt.Printf("  %s %s %s (%s)\n",
			style.Warning.Render("delete "), w.ID, compactTruncate(w.Title, 40), reason)
	}
}

func printCompactSummary(result *compactResult) {
	promoted := len(result.Promoted)
	deleted := len(result.Deleted)
	protected := len(result.Protected)

	if compactDryRun {
		fmt.Printf("\n%s Dry run complete: %d wisps scanned\n",
			style.Dim.Render("ℹ"), result.Scanned)
	} else {
		fmt.Printf("\n%s Compaction complete\n", style.Success.Render("✓"))
	}

	// Scanned is printed unconditionally, including when it is 0. "Scanned: 0"
	// is the line that would have exposed gt-ktvs on its first run instead of
	// after months of clean-looking output.
	fmt.Printf("  Scanned:  %d\n", result.Scanned)
	fmt.Printf("  Promoted: %d\n", promoted)
	fmt.Printf("  Deleted:  %d\n", deleted)
	fmt.Printf("  Skipped:  %d (within TTL)\n", result.Skipped)
	if result.Unclassified > 0 {
		fmt.Printf("  %s %d (empty wisp_type — no TTL policy applies, left untouched)\n",
			style.Warning.Render("Unclassified:"), result.Unclassified)
	}
	if protected > 0 {
		fmt.Printf("  Protected: %d (past TTL, held by pin or label)\n", protected)
	}
	if accounted := promoted + deleted + protected + result.Skipped + result.Unclassified + len(result.Errors); accounted != result.Scanned {
		// Every scanned wisp must land in exactly one bucket. If it does not,
		// the summary is under-reporting and the counts above cannot be trusted
		// as a description of what the run did.
		fmt.Printf("  %s %d scanned but %d accounted for — summary is incomplete\n",
			style.Warning.Render("⚠"), result.Scanned, accounted)
	}
	if result.OrphanedWispDeps > 0 {
		fmt.Printf("  Cleaned:  %d orphaned wisp dependency ref(s)\n", result.OrphanedWispDeps)
	}

	if len(result.Errors) > 0 {
		fmt.Printf("\n%s %d errors:\n", style.Warning.Render("⚠"), len(result.Errors))
		for _, e := range result.Errors {
			fmt.Printf("  - %s\n", e)
		}
	}

	// Show promotions if any
	if promoted > 0 && !compactDryRun {
		fmt.Printf("\nPromotions:\n")
		for _, p := range result.Promoted {
			fmt.Printf("  %s: %s (%s)\n", p.ID, compactTruncate(p.Title, 50), p.Reason)
		}
	}
}

// compactTruncate shortens a string to maxLen runes, adding "..." if truncated.
// Uses rune count instead of byte length so multi-byte UTF-8 characters
// (emoji, CJK, etc.) are never split mid-sequence.
func compactTruncate(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string([]rune(s)[:maxLen])
	}
	return string([]rune(s)[:maxLen-3]) + "..."
}

// hasComments checks the comment_count on the compactIssue.
func hasComments(w *compactIssue) bool {
	return w.CommentCount > 0
}

// isReferenced checks dependency counts.
func isReferenced(w *compactIssue) bool {
	return w.DependentCount > 0 || w.DependencyCount > 0
}

// wispProtection returns a description of the guard that forbids deleting w, or
// "" if none applies. It mirrors reaper.purgeProtectWhere: the same two guards,
// in the same order.
//
//   - pinned — the column an incident responder sets by hand
//     (`bd sql "update wisps set pinned=1 where id=..."`) to protect one
//     specific record right now. This path could not honour it while it read
//     `bd list --json`, which does not return the column, so a responder who
//     pinned a record was protected from `bd purge` and the reaper and NOT from
//     here. Reading the wisps table (gt-ktvs) removed that constraint; leaving
//     the gap in place afterwards would have been a choice.
//   - a protected label — protection by type, which needs nobody to have
//     anticipated the specific record.
func wispProtection(w *compactIssue) string {
	if w.Pinned {
		return "pinned"
	}
	if label := protectedWispLabel(w); label != "" {
		return "protected label " + label
	}
	return ""
}

// protectedWispLabel returns the label that forbids deleting w, or "" if none.
//
// The list is reaper.ProtectedWispLabels — the same variable the reaper's native
// SQL delete consults — so the two paths cannot disagree about what is
// undeletable. A private copy here would be a second list to keep in sync, and
// gt-6dp is a record of what happens when one deleter is protected and another
// is not.
func protectedWispLabel(w *compactIssue) string {
	for _, label := range w.Labels {
		for _, protected := range reaper.ProtectedWispLabels {
			if label == protected {
				return protected
			}
		}
	}
	return ""
}

// hasKeepLabel checks for keep labels.
func hasKeepLabel(w *compactIssue) bool {
	for _, label := range w.Labels {
		if label == "keep" || label == "gt:keep" {
			return true
		}
	}
	return false
}

// wispAge returns the age of a compactIssue.
func wispAge(w *compactIssue, now time.Time) (time.Duration, error) {
	ts := w.UpdatedAt
	if ts == "" {
		ts = w.CreatedAt
	}
	// The space-separated layout is not hypothetical: bd renders a datetime
	// column as RFC3339 when it is selected bare and as "2006-01-02 15:04:05"
	// once an expression wraps it, so the format depends on how the query was
	// written. wispSelectColumns selects timestamps bare, and this accepts the
	// other form so that a later edit to the projection cannot turn every wisp
	// into a parse error.
	layouts := []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, ts); err == nil {
			return now.Sub(t), nil
		}
	}
	return 0, fmt.Errorf("parsing timestamp %q: no known layout matches", ts)
}
