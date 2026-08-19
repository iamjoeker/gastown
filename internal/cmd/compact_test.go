package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/reaper"
)

func TestGetTTL(t *testing.T) {
	ttls := defaultTTLs

	tests := []struct {
		name          string
		wispType      string
		want          time.Duration
		wantClassifed bool
	}{
		{"heartbeat", "heartbeat", 6 * time.Hour, true},
		{"ping", "ping", 6 * time.Hour, true},
		{"patrol", "patrol", 24 * time.Hour, true},
		{"gc_report", "gc_report", 24 * time.Hour, true},
		{"error", "error", 7 * 24 * time.Hour, true},
		{"recovery", "recovery", 7 * 24 * time.Hour, true},
		{"escalation", "escalation", 7 * 24 * time.Hour, true},
		{"default", "default", 24 * time.Hour, true},
		// A deliberately-written type with no configured TTL takes the
		// documented default: the writer named a type, so "default" is policy.
		{"unknown type falls back to default", "unknown", 24 * time.Hour, true},
		// An EMPTY type is not a type. gt-ktvs: 703 of 703 wisps on the gastown
		// rig carry one, and reading it as "default" = 24h would delete the 7d
		// escalation/recovery/error records on the first run that could see them.
		{"empty type is unclassified", "", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, classified := getTTL(ttls, tc.wispType)
			if classified != tc.wantClassifed {
				t.Errorf("getTTL(%q) classified = %v, want %v", tc.wispType, classified, tc.wantClassifed)
			}
			if got != tc.want {
				t.Errorf("getTTL(%q) = %v, want %v", tc.wispType, got, tc.want)
			}
		})
	}
}

// TestGetTTLEmptyTypeIgnoresConfiguredDefault pins the part of gt-ktvs that a
// table test cannot: it is not enough that the hardcoded default happens not to
// be applied to untyped wisps. Configuring a default must not reach them either,
// or the refusal is one `wisp_ttl.default` config entry away from evaporating.
func TestGetTTLEmptyTypeIgnoresConfiguredDefault(t *testing.T) {
	ttls := map[string]time.Duration{"default": 999 * time.Hour, "patrol": 3 * time.Hour}

	if got, classified := getTTL(ttls, ""); classified || got != 0 {
		t.Errorf("getTTL(ttls, \"\") = (%v, %v), want (0, false) even with a configured default",
			got, classified)
	}
	// Control: the same map must still classify a real type, or this test would
	// also pass against a getTTL that refuses everything.
	if got, classified := getTTL(ttls, "patrol"); !classified || got != 3*time.Hour {
		t.Errorf("getTTL(ttls, \"patrol\") = (%v, %v), want (3h, true)", got, classified)
	}
}

func TestWispAge(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		updatedAt string
		wantAge   time.Duration
		wantErr   bool
	}{
		{
			name:      "RFC3339",
			updatedAt: "2026-02-07T06:00:00Z",
			wantAge:   6 * time.Hour,
		},
		{
			name:      "one day old",
			updatedAt: "2026-02-06T12:00:00Z",
			wantAge:   24 * time.Hour,
		},
		{
			name:      "invalid",
			updatedAt: "not-a-date",
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{
				Issue: beads.Issue{UpdatedAt: tc.updatedAt},
			}
			got, err := wispAge(w, now)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantAge {
				t.Errorf("wispAge = %v, want %v", got, tc.wantAge)
			}
		})
	}
}

func TestHasKeepLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"no labels", nil, false},
		{"other labels", []string{"bug", "urgent"}, false},
		{"keep label", []string{"keep"}, true},
		{"gt:keep label", []string{"bug", "gt:keep"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{
				Issue: beads.Issue{Labels: tc.labels},
			}
			if got := hasKeepLabel(w); got != tc.want {
				t.Errorf("hasKeepLabel = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasComments(t *testing.T) {
	tests := []struct {
		name  string
		count int
		want  bool
	}{
		{"no comments", 0, false},
		{"has comments", 3, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{CommentCount: tc.count}
			if got := hasComments(w); got != tc.want {
				t.Errorf("hasComments = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsReferenced(t *testing.T) {
	tests := []struct {
		name    string
		depCnt  int
		deptCnt int
		want    bool
	}{
		{"no refs", 0, 0, false},
		{"has dependents", 0, 1, true},
		{"has dependencies", 1, 0, true},
		{"both", 2, 3, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{
				Issue: beads.Issue{
					DependencyCount: tc.depCnt,
					DependentCount:  tc.deptCnt,
				},
			}
			if got := isReferenced(w); got != tc.want {
				t.Errorf("isReferenced = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompactTruncate(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{"short ASCII", "short", 10, "short"},
		{"exact length", "exactly10!", 10, "exactly10!"},
		{"ASCII too long", "this is too long", 10, "this is..."},
		{"short maxLen", "ab", 3, "ab"},
		{"maxLen 3", "abcdef", 3, "abc"},
		// Multi-byte UTF-8: emoji is 1 rune, not 4 bytes
		{"emoji within limit", "🤝 HANDOFF", 10, "🤝 HANDOFF"},
		{"emoji truncated", "🤝 HANDOFF: Routine cycle for witness", 15, "🤝 HANDOFF: R..."},
		// CJK characters: each is 1 rune, 3 bytes
		{"CJK within limit", "日本語テスト", 10, "日本語テスト"},
		{"CJK truncated", "日本語テストデータ", 6, "日本語..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactTruncate(tc.s, tc.maxLen); got != tc.want {
				t.Errorf("compactTruncate(%q, %d) = %q, want %q", tc.s, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestExtractJSONArray(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			"clean JSON array",
			`[{"id":"test"}]`,
			`[{"id":"test"}]`,
		},
		{
			"warning prefix before JSON",
			"Warning: no route found for prefix \"gt-\"\n[{\"id\":\"test\"}]",
			`[{"id":"test"}]`,
		},
		{
			"unicode warning prefix",
			"⚠ Warning: something with 🤝 emoji\n[{\"id\":\"test\"}]",
			`[{"id":"test"}]`,
		},
		{
			"no array in data",
			"just some text without json",
			"just some text without json",
		},
		{
			"empty data",
			"",
			"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(extractJSONArray([]byte(tc.data)))
			if got != tc.want {
				t.Errorf("extractJSONArray(%q) = %q, want %q", tc.data, got, tc.want)
			}
		})
	}
}

func TestLoadTTLConfigDefaults(t *testing.T) {
	// With empty town root, should return defaults
	ttls := loadTTLConfig("", "")

	if ttls["heartbeat"] != 6*time.Hour {
		t.Errorf("heartbeat TTL = %v, want 6h", ttls["heartbeat"])
	}
	if ttls["patrol"] != 24*time.Hour {
		t.Errorf("patrol TTL = %v, want 24h", ttls["patrol"])
	}
	if ttls["error"] != 7*24*time.Hour {
		t.Errorf("error TTL = %v, want 168h", ttls["error"])
	}
}

func TestLoadTTLConfigWithRoleDefaults(t *testing.T) {
	// With empty town root, should return hardcoded defaults
	ttls := loadTTLConfigWithRole("", "")

	for k, want := range defaultTTLs {
		if got := ttls[k]; got != want {
			t.Errorf("loadTTLConfigWithRole TTLs[%q] = %v, want %v", k, got, want)
		}
	}
}

func TestLoadTTLConfigWithRoleSkipsInvalidPaths(t *testing.T) {
	// With nonexistent paths, rig bead lookup should gracefully skip
	ttls := loadTTLConfigWithRole("/nonexistent/town", "myrig")

	// Should still have defaults even though lookups failed
	if ttls["patrol"] != defaultTTLs["patrol"] {
		t.Errorf("patrol TTL = %v, want %v", ttls["patrol"], defaultTTLs["patrol"])
	}
	if ttls["error"] != defaultTTLs["error"] {
		t.Errorf("error TTL = %v, want %v", ttls["error"], defaultTTLs["error"])
	}
}

func TestCleanOrphanedWispDepsUsesTypedTargets(t *testing.T) {
	data, err := os.ReadFile("compact.go")
	if err != nil {
		t.Fatalf("read compact.go: %v", err)
	}
	body := compactSourceBetween(t, string(data), "func cleanOrphanedWispDeps(", "// listWisps")
	if strings.Contains(body, "depends_on_id") {
		t.Fatalf("cleanOrphanedWispDeps should not use legacy depends_on_id:\n%s", body)
	}
	for _, want := range []string{
		"depends_on_wisp_id IS NOT NULL AND NOT EXISTS",
		"wisps WHERE id = wisp_dependencies.depends_on_wisp_id",
		"depends_on_issue_id IS NOT NULL AND NOT EXISTS",
		"issues WHERE id = wisp_dependencies.depends_on_issue_id",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("cleanOrphanedWispDeps missing %q:\n%s", want, body)
		}
	}
}

func compactSourceBetween(t *testing.T, source, startMarker, endMarker string) string {
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

// TestProtectedWispLabel covers the predicate that keeps `gt compact` from
// deleting the one wisp type gt-6dp says must survive closure.
func TestProtectedWispLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"no labels", nil, ""},
		{"unrelated labels", []string{"bug", "urgent"}, ""},
		{"merge-request", []string{"gt:merge-request"}, "gt:merge-request"},
		{"merge-request among others", []string{"gt:sling-context", "gt:merge-request"}, "gt:merge-request"},
		// The bd-side config key is "merge-request"; the label the rig actually
		// writes is "gt:merge-request". Asserting the unprefixed form does NOT
		// match records which of the two this path keys on, so a future rename
		// cannot quietly half-apply.
		{"unprefixed form is not the label", []string{"merge-request"}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{Issue: beads.Issue{Labels: tc.labels}}
			if got := protectedWispLabel(w); got != tc.want {
				t.Errorf("protectedWispLabel(%v) = %q, want %q", tc.labels, got, tc.want)
			}
		})
	}
}

// TestDeleteWispRefusesProtectedLabel is positive-controlled both ways: the
// protected wisp must NOT be deleted AND the otherwise-identical unprotected
// wisp must be, or the test would also pass against a deleteWisp that deletes
// nothing at all.
//
// The guard sits above the dry-run branch in deleteWisp, so exercising it in
// dry-run mode covers the live path too — there is no second decision to make
// once the function has returned. Dry-run keeps the test free of a bd binary.
func TestDeleteWispRefusesProtectedLabel(t *testing.T) {
	oldDryRun, oldVerbose, oldJSON := compactDryRun, compactVerbose, compactJSON
	compactDryRun, compactVerbose, compactJSON = true, false, true
	t.Cleanup(func() { compactDryRun, compactVerbose, compactJSON = oldDryRun, oldVerbose, oldJSON })

	protectedWisp := &compactIssue{
		Issue: beads.Issue{
			ID:     "w-mr",
			Title:  "MR: polecat/slit/bd-6jp",
			Status: "closed",
			Labels: []string{"gt:merge-request"},
		},
	}
	plainWisp := &compactIssue{
		Issue: beads.Issue{
			ID:     "w-patrol",
			Title:  "patrol cycle",
			Status: "closed",
		},
	}

	result := &compactResult{}
	deleteWisp(nil, protectedWisp, "TTL expired", result)
	deleteWisp(nil, plainWisp, "TTL expired", result)

	if len(result.Protected) != 1 || result.Protected[0].ID != "w-mr" {
		t.Errorf("Protected = %+v, want exactly [w-mr] — a closed gt:merge-request wisp "+
			"past TTL is the record gt-6dp exists to preserve", result.Protected)
	}
	if len(result.Deleted) != 1 || result.Deleted[0].ID != "w-patrol" {
		t.Errorf("Deleted = %+v, want exactly [w-patrol] — the control must still be "+
			"deletable, or this test passes against a compact that deletes nothing",
			result.Deleted)
	}
	if !strings.Contains(result.Protected[0].Reason, "gt:merge-request") {
		t.Errorf("Protected reason = %q, want it to name the protecting label",
			result.Protected[0].Reason)
	}
}

// TestDeleteWispSharesReaperProtectedList proves compact reads
// reaper.ProtectedWispLabels rather than an equal-looking private copy.
// A duplicate list would pass every test above and still drift the moment
// someone adds a type on one side only — the exact failure gt-6dp records.
func TestDeleteWispSharesReaperProtectedList(t *testing.T) {
	oldDryRun, oldJSON := compactDryRun, compactJSON
	compactDryRun, compactJSON = true, true
	t.Cleanup(func() { compactDryRun, compactJSON = oldDryRun, oldJSON })

	w := &compactIssue{
		Issue: beads.Issue{ID: "w-new", Status: "closed", Labels: []string{"gt:test-protected"}},
	}

	before := &compactResult{}
	deleteWisp(nil, w, "TTL expired", before)
	if len(before.Deleted) != 1 {
		t.Fatalf("control failed: gt:test-protected is not in the list yet, so the wisp "+
			"must be deletable; Deleted = %+v", before.Deleted)
	}

	original := reaper.ProtectedWispLabels
	reaper.ProtectedWispLabels = append(append([]string{}, original...), "gt:test-protected")
	t.Cleanup(func() { reaper.ProtectedWispLabels = original })

	after := &compactResult{}
	deleteWisp(nil, w, "TTL expired", after)
	if len(after.Protected) != 1 || len(after.Deleted) != 0 {
		t.Errorf("after adding gt:test-protected to reaper.ProtectedWispLabels: "+
			"Protected = %+v, Deleted = %+v; want the wisp held. compact is not "+
			"reading the shared list.", after.Protected, after.Deleted)
	}
}

// ---------------------------------------------------------------------------
// gt-ktvs: compaction reads the wisps table
// ---------------------------------------------------------------------------

// TestWispQueryReadsWispsTable pins the defect gt-ktvs records. listWisps used
// to run `bd list --json --all` and keep rows with ephemeral=true; bd list does
// not query the wisps table, so the filter could never match and every run
// processed zero wisps while reporting a clean result. Measured on the gastown
// rig 2026-08-19: 222 issue rows from bd list, none of which even carried an
// `ephemeral` key, against 703 rows in `wisps`.
func TestWispQueryReadsWispsTable(t *testing.T) {
	query := wispQuery(mutableWispWhere())

	if !strings.Contains(query, "FROM wisps w") {
		t.Errorf("compaction query does not read the wisps table:\n%s", query)
	}
	// The fields the old path silently read as zero values off an issue-shaped
	// row. Each one changes a decision, so each must come from the wisps table.
	for _, want := range []string{
		"w.wisp_type",       // decides which TTL, or none
		"w.pinned",          // decides whether delete is allowed at all
		"wisp_labels",       // decides protection and keep
		"wisp_comments",     // decides promotion
		"wisp_dependencies", // decides molecule-step handling
	} {
		if !strings.Contains(query, want) {
			t.Errorf("compaction query is missing %q — that field would be read as its zero value:\n%s", want, query)
		}
	}
}

// ---------------------------------------------------------------------------
// gt-g60l: the wisps query has to survive the town database
// ---------------------------------------------------------------------------

// TestWispQueryHasNoCorrelatedSubqueries pins the defect gt-g60l records. The
// repair for gt-ktvs fetched comment_count, labels_csv and parent as three
// correlated subqueries, so the query cost scaled with rows × subqueries: fine
// on a rig (1005 wisps, ~1s) and fatal on hq (28.5k wisps, ~85k subquery runs),
// where `gt compact` died on the 60s bd subprocess timeout and town-level
// compaction could not run at all. The database holding 28k of the ~29.5k wisps
// town-wide was the one the command could not open.
//
// A correlated subquery is exactly one that mentions the outer alias `w` inside
// its parentheses, so that is what this checks — a derived table joined on
// c.issue_id = w.id names `w` only in its ON clause, outside the subquery.
func TestWispQueryHasNoCorrelatedSubqueries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"listWisps", wispQuery(mutableWispWhere())},
		{"listReportWisps", wispQuery("")},
	} {
		for _, sub := range parenthesizedSubqueries(tc.query) {
			if strings.Contains(sub, "w.") {
				t.Errorf("%s: correlated subquery %q references the outer row; at hq's row "+
					"count that is one execution per wisp and the query times out:\n%s",
					tc.name, sub, tc.query)
			}
		}
	}
}

// TestWispQueryFiltersAfterJoining guards the ordering the rewrite depends on:
// the WHERE clause has to follow the joins or the statement is a syntax error,
// which no unit test that only inspects strings would otherwise notice.
func TestWispQueryFiltersAfterJoining(t *testing.T) {
	query := wispQuery(mutableWispWhere())

	join := strings.Index(query, "LEFT JOIN")
	where := strings.Index(query, "WHERE w.issue_type")
	order := strings.Index(query, "ORDER BY")
	if join == -1 || where == -1 || order == -1 {
		t.Fatalf("query is missing a clause the ordering check needs:\n%s", query)
	}
	if !(join < where && where < order) {
		t.Errorf("clause order is JOIN@%d WHERE@%d ORDER BY@%d, want ascending:\n%s",
			join, where, order, query)
	}
	// Control: the unfiltered form the digest uses must keep the joins and the
	// ordering while carrying no outer filter, so this test cannot be satisfied
	// by a query that simply always has a WHERE.
	report := wispQuery("")
	if !strings.Contains(report, "LEFT JOIN") || !strings.HasSuffix(report, "ORDER BY w.id") {
		t.Errorf("unfiltered query lost its joins or its ordering:\n%s", report)
	}
	if strings.Contains(report, "WHERE w.") {
		t.Errorf("unfiltered query filters the outer table — the digest counts everything:\n%s", report)
	}
}

// parenthesizedSubqueries returns the body of every balanced `(SELECT ...)`
// group in q, innermost text included, so a correlation can be detected without
// depending on how the query happens to be spaced.
func parenthesizedSubqueries(q string) []string {
	var subs []string
	for i := 0; i < len(q); i++ {
		if q[i] != '(' || !strings.HasPrefix(strings.TrimLeft(q[i+1:], " "), "SELECT") {
			continue
		}
		depth := 0
		for j := i; j < len(q); j++ {
			switch q[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					subs = append(subs, q[i+1:j])
					j = len(q)
				}
			}
		}
	}
	return subs
}

// TestMutableWispWhereExcludesInfraTypes covers a hazard the repair introduces
// rather than one it inherits. bd list's default view hid infra beads, so the
// old blind path was shielded from them by accident. Reading the table removes
// the shield: agent beads are persistent identity — the reaper refuses to touch
// them for the same reason — and a compaction pass that deleted one would be
// deleting an agent, not a record of one.
func TestMutableWispWhereExcludesInfraTypes(t *testing.T) {
	where := mutableWispWhere()

	for _, infra := range constants.BeadsInfraTypesList() {
		if !strings.Contains(where, "'"+infra+"'") {
			t.Errorf("mutable wisp scope does not exclude infra type %q: %s", infra, where)
		}
	}
	if !strings.Contains(where, "w.issue_type NOT IN") {
		t.Errorf("mutable wisp scope is not an issue_type exclusion: %s", where)
	}
	// Control: the types compaction exists to sweep must NOT be excluded, or
	// this test would pass against a scope that excludes everything.
	if strings.Contains(where, "'task'") || strings.Contains(where, "'molecule'") {
		t.Errorf("mutable wisp scope excludes the types compaction is for: %s", where)
	}
}

// TestParseWispRows decodes rows captured verbatim from the gastown rig on
// 2026-08-19, so the field mapping is pinned against the shape bd actually
// emits rather than against an assumption about it.
func TestParseWispRows(t *testing.T) {
	const captured = `[
  {
    "comment_count": 0,
    "created_at": "2026-08-18T02:35:51Z",
    "id": "gt-wisp-04ya",
    "issue_type": "task",
    "labels_csv": "gt:merge-request",
    "parent": "",
    "pinned": 0,
    "status": "closed",
    "title": "Merge: gt-u7z",
    "updated_at": "2026-08-18T02:43:01Z",
    "wisp_type": ""
  },
  {
    "comment_count": 3,
    "created_at": "2026-08-18T00:49:24Z",
    "id": "gt-wisp-00c",
    "issue_type": "task",
    "labels_csv": "gt:keep,gt:step",
    "parent": "gt-wisp-v4m",
    "pinned": 1,
    "status": "closed",
    "title": "Set up working branch",
    "updated_at": "2026-08-19T02:29:03Z",
    "wisp_type": "patrol"
  }
]`

	wisps, err := parseWispRows([]byte(captured))
	if err != nil {
		t.Fatalf("parseWispRows: %v", err)
	}
	if len(wisps) != 2 {
		t.Fatalf("parsed %d wisps, want 2", len(wisps))
	}

	mr := wisps[0]
	if mr.ID != "gt-wisp-04ya" || mr.Status != "closed" || mr.Type != "task" {
		t.Errorf("row 0 identity = %+v", mr.Issue)
	}
	if got := mr.Labels; len(got) != 1 || got[0] != "gt:merge-request" {
		t.Errorf("row 0 labels = %v, want [gt:merge-request] — the protection guard reads this", got)
	}
	if mr.Pinned {
		t.Errorf("row 0 pinned = true, want false (pinned column was 0)")
	}
	if !mr.Ephemeral {
		t.Errorf("row 0 Ephemeral = false; everything from the wisps table is a wisp")
	}

	step := wisps[1]
	if step.WispType != "patrol" {
		t.Errorf("row 1 wisp_type = %q, want patrol", step.WispType)
	}
	if step.Parent != "gt-wisp-v4m" {
		t.Errorf("row 1 parent = %q, want gt-wisp-v4m — molecule-step handling turns on this", step.Parent)
	}
	if step.CommentCount != 3 {
		t.Errorf("row 1 comment_count = %d, want 3", step.CommentCount)
	}
	if !step.Pinned {
		t.Errorf("row 1 pinned = false, want true (pinned column was 1)")
	}
	if got := step.Labels; len(got) != 2 || got[0] != "gt:keep" || got[1] != "gt:step" {
		t.Errorf("row 1 labels = %v, want [gt:keep gt:step] — labels_csv must split", got)
	}
}

func TestParseWispRowsEmptyResult(t *testing.T) {
	wisps, err := parseWispRows([]byte("[]"))
	if err != nil {
		t.Fatalf("parseWispRows on empty result: %v", err)
	}
	if len(wisps) != 0 {
		t.Errorf("parsed %d wisps from an empty result", len(wisps))
	}
}

// ---------------------------------------------------------------------------
// gt-ktvs second defect: an empty wisp_type is not a 24h wisp
// ---------------------------------------------------------------------------

// TestDecideWispNeverDeletesUntyped is the regression test for the defect the
// bead says must be fixed BEFORE the blindness, not after: every wisp on the
// rig carries an empty wisp_type, and 24h-defaulting them would have deleted a
// week of escalation, recovery and error records on the first run that could
// see them.
//
// The typed control is what makes this test mean anything. Without it, a
// decideWisp that refuses to delete anything at all would pass.
func TestDecideWispNeverDeletesUntyped(t *testing.T) {
	untyped := &compactIssue{
		Issue:    beads.Issue{ID: "w-untyped", Status: "closed"},
		WispType: "",
	}
	// Same wisp, same age, but classified as an escalation — 7d TTL, and 8 days
	// old, so it is genuinely expired.
	typed := &compactIssue{
		Issue:    beads.Issue{ID: "w-escalation", Status: "closed"},
		WispType: "escalation",
	}
	age := 8 * 24 * time.Hour

	if got := decideWisp(untyped, age, defaultTTLs); got.action != actionUnclassified {
		t.Errorf("untyped closed wisp %s old: action = %v (%q), want actionUnclassified. "+
			"Reading an empty wisp_type as the 24h default is gt-ktvs' second defect.",
			age, got.action, got.reason)
	}
	if got := decideWisp(typed, age, defaultTTLs); got.action != actionDelete {
		t.Errorf("control failed: a closed escalation wisp %s past its 7d TTL should be "+
			"deleted, got %v (%q). Without this, the assertion above passes against a "+
			"compact that never deletes anything.", age, got.action, got.reason)
	}
}

// TestDecideWispUntypedKeepIsStillPromoted checks that refusing to guess a TTL
// does not also strand the wisps worth keeping. Proven value is a property of
// the wisp, not of its age, so it must survive the unclassified branch.
func TestDecideWispUntypedKeepIsStillPromoted(t *testing.T) {
	kept := &compactIssue{
		Issue: beads.Issue{ID: "w-keep", Status: "closed", Labels: []string{"gt:keep"}},
	}
	commented := &compactIssue{
		Issue:        beads.Issue{ID: "w-comment", Status: "open"},
		CommentCount: 2,
	}

	for _, w := range []*compactIssue{kept, commented} {
		got := decideWisp(w, 30*24*time.Hour, defaultTTLs)
		if got.action != actionPromote || got.reason != "proven value" {
			t.Errorf("%s: action = %v (%q), want promote/proven value even with an empty wisp_type",
				w.ID, got.action, got.reason)
		}
	}
}

// TestDecideWispTypedPolicy covers the pre-existing decision table, so that
// repairing the input source cannot quietly change what compaction does with
// the wisps it can now finally see.
func TestDecideWispTypedPolicy(t *testing.T) {
	const patrolTTL = 24 * time.Hour

	tests := []struct {
		name       string
		status     string
		parent     string
		age        time.Duration
		wantAction wispAction
		wantReason string
	}{
		{"closed within TTL", "closed", "", patrolTTL - time.Hour, actionSkip, "within TTL"},
		{"open within TTL", "open", "", patrolTTL - time.Hour, actionSkip, "within TTL"},
		{"closed past TTL is deleted", "closed", "", patrolTTL + time.Hour, actionDelete, "TTL expired"},
		{"closed molecule step past TTL is deleted", "closed", "mol-1", patrolTTL + time.Hour, actionDelete, "TTL expired"},
		{"open molecule step past TTL is deleted, not promoted", "open", "mol-1", patrolTTL + time.Hour, actionDelete, "molecule step past TTL"},
		{"open past TTL is promoted", "open", "", patrolTTL + time.Hour, actionPromote, "open past TTL"},
		{"in_progress past TTL names being stuck", "in_progress", "", patrolTTL + time.Hour, actionPromote, "stuck in_progress past TTL"},
		{"hooked past TTL is promoted", "hooked", "", patrolTTL + time.Hour, actionPromote, "open past TTL"},
		// Exactly at the TTL is not past it.
		{"age equal to TTL is within", "closed", "", patrolTTL, actionSkip, "within TTL"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{
				Issue:    beads.Issue{ID: "w-1", Status: tc.status, Parent: tc.parent},
				WispType: "patrol",
			}
			got := decideWisp(w, tc.age, defaultTTLs)
			if got.action != tc.wantAction || got.reason != tc.wantReason {
				t.Errorf("decideWisp = %v (%q), want %v (%q)",
					got.action, got.reason, tc.wantAction, tc.wantReason)
			}
		})
	}
}

// TestDecideWispMoleculeStepIsNotPromotedForProvenValue keeps the pre-existing
// carve-out: a step with a parent is a subordinate step, never a permanent bead.
func TestDecideWispMoleculeStepIsNotPromotedForProvenValue(t *testing.T) {
	step := &compactIssue{
		Issue:        beads.Issue{ID: "w-step", Status: "closed", Parent: "mol-1", Labels: []string{"gt:keep"}},
		WispType:     "patrol",
		CommentCount: 5,
	}
	got := decideWisp(step, 48*time.Hour, defaultTTLs)
	if got.action != actionDelete {
		t.Errorf("kept+commented molecule step past TTL: action = %v (%q), want delete — "+
			"steps must not be elevated into the issues table", got.action, got.reason)
	}
}

// ---------------------------------------------------------------------------
// gt-ktvs / gt-6dp: the guards that make deletion safe
// ---------------------------------------------------------------------------

// TestDeleteWispRefusesPinnedWisp covers the guard that only became reachable
// once compaction read the wisps table. `pinned` is what an incident responder
// sets by hand; bd purge and the reaper both honour it, and this path could not
// while it read bd list output, which does not return the column.
func TestDeleteWispRefusesPinnedWisp(t *testing.T) {
	oldDryRun, oldVerbose, oldJSON := compactDryRun, compactVerbose, compactJSON
	compactDryRun, compactVerbose, compactJSON = true, false, true
	t.Cleanup(func() { compactDryRun, compactVerbose, compactJSON = oldDryRun, oldVerbose, oldJSON })

	pinned := &compactIssue{
		Issue:  beads.Issue{ID: "w-pinned", Title: "held by a responder", Status: "closed"},
		Pinned: true,
	}
	unpinned := &compactIssue{
		Issue: beads.Issue{ID: "w-plain", Title: "held by a responder", Status: "closed"},
	}

	result := &compactResult{}
	deleteWisp(nil, pinned, "TTL expired", result)
	deleteWisp(nil, unpinned, "TTL expired", result)

	if len(result.Protected) != 1 || result.Protected[0].ID != "w-pinned" {
		t.Errorf("Protected = %+v, want exactly [w-pinned]", result.Protected)
	}
	if len(result.Deleted) != 1 || result.Deleted[0].ID != "w-plain" {
		t.Errorf("Deleted = %+v, want exactly [w-plain] — the control must stay deletable",
			result.Deleted)
	}
	if !strings.Contains(result.Protected[0].Reason, "pinned") {
		t.Errorf("Protected reason = %q, want it to name the pin", result.Protected[0].Reason)
	}
}

// ---------------------------------------------------------------------------
// gt-ktvs first-order defect: a zero that cannot be told from a clean run
// ---------------------------------------------------------------------------

// TestCompactResultReportsScanned covers the property that would have exposed
// this bug on day one. `{promoted: null, deleted: null, skipped: 0}` was the
// output of a run over 703 invisible wisps and the output of a run over a tidy
// database, and nothing in it distinguished them.
func TestCompactResultReportsScanned(t *testing.T) {
	encoded, err := json.Marshal(&compactResult{})
	if err != nil {
		t.Fatalf("marshal compactResult: %v", err)
	}
	if !strings.Contains(string(encoded), `"scanned"`) {
		t.Errorf("compactResult JSON omits the input size, so an unreadable database "+
			"still encodes the same as an empty one: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"unclassified"`) {
		t.Errorf("compactResult JSON omits the unclassified count, which is currently "+
			"every wisp on the rig: %s", encoded)
	}
}
