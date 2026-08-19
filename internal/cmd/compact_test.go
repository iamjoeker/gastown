package cmd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/reaper"
)

func TestGetTTL(t *testing.T) {
	ttls := defaultTTLs

	tests := []struct {
		wispType string
		want     time.Duration
	}{
		{"heartbeat", 6 * time.Hour},
		{"ping", 6 * time.Hour},
		{"patrol", 24 * time.Hour},
		{"gc_report", 24 * time.Hour},
		{"error", 7 * 24 * time.Hour},
		{"recovery", 7 * 24 * time.Hour},
		{"escalation", 7 * 24 * time.Hour},
		{"default", 24 * time.Hour},
		{"", 24 * time.Hour},        // empty falls back to default
		{"unknown", 24 * time.Hour}, // unknown falls back to default
	}

	for _, tc := range tests {
		t.Run(tc.wispType, func(t *testing.T) {
			got := getTTL(ttls, tc.wispType)
			if got != tc.want {
				t.Errorf("getTTL(%q) = %v, want %v", tc.wispType, got, tc.want)
			}
		})
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
