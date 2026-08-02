package web

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// writeRigsConfig writes a mayor/rigs.json fixture under townRoot.
func writeRigsConfig(t *testing.T, townRoot, contents string) {
	t.Helper()

	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("creating mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(contents), 0o600); err != nil {
		t.Fatalf("writing rigs.json: %v", err)
	}
}

// mrBead builds a merge-request bead the way `gt mq submit` records one.
func mrBead(id, title string, fields map[string]string) *beads.Issue {
	var sb strings.Builder
	for _, k := range []string{"branch", "target", "source_issue", "worker", "rig", "commit_sha", "pr_url", "pr_number", "retry_count", "convoy_id"} {
		if v, ok := fields[k]; ok {
			fmt.Fprintf(&sb, "%s: %s\n", k, v)
		}
	}
	return &beads.Issue{
		ID:          id,
		Title:       title,
		Description: sb.String(),
		Status:      "open",
		Labels:      []string{"gt:merge-request"},
		CreatedAt:   time.Now().Add(-30 * time.Minute).Format(time.RFC3339),
	}
}

// stubMRs replaces the merge-request query with a per-rig fixture and records
// the rig paths queried.
func stubMRs(t *testing.T, byRigPath map[string][]*beads.Issue, failRigs map[string]bool) *[]string {
	t.Helper()

	original := fetcherListMergeRequests
	t.Cleanup(func() { fetcherListMergeRequests = original })

	var queried []string
	fetcherListMergeRequests = func(rigPath string, opts beads.ListOptions) ([]*beads.Issue, error) {
		queried = append(queried, rigPath)
		if opts.Label != mergeRequestLabel {
			t.Errorf("label = %q, want %q", opts.Label, mergeRequestLabel)
		}
		if opts.Priority != -1 {
			t.Errorf("Priority = %d, want -1 (no priority filter)", opts.Priority)
		}
		rig := filepath.Base(rigPath)
		if failRigs[rig] {
			return nil, fmt.Errorf("dolt unavailable for %s", rig)
		}
		return byRigPath[rig], nil
	}
	return &queried
}

// stubNoGH fails the test if any gh command runs, and records those that do.
func stubNoGH(t *testing.T) *[][]string {
	t.Helper()

	original := fetcherRunCmd
	t.Cleanup(func() { fetcherRunCmd = original })

	var calls [][]string
	fetcherRunCmd = func(_ time.Duration, name string, args ...string) (*bytes.Buffer, error) {
		if name == "gh" {
			calls = append(calls, args)
		}
		return bytes.NewBufferString("{}"), nil
	}
	return &calls
}

const twoRigsConfig = `{
  "version": 1,
  "rigs": {
    "beads": {"git_url": "git@github.com:upstreamorg/beads.git", "push_url": "git@github.com:ourfork/beads.git"},
    "gastown": {"git_url": "git@github.com:upstreamorg/gastown.git", "push_url": "git@github.com:ourfork/gastown.git"}
  }
}`

// TestFetchMergeQueue_ReadsMergeRequestBeads is the core regression test for
// gt-4qp: the panel must read rig-local merge-request beads — the same source
// `gt mq list <rig>` uses — not GitHub PRs.
func TestFetchMergeQueue_ReadsMergeRequestBeads(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, twoRigsConfig)

	queried := stubMRs(t, map[string][]*beads.Issue{
		"beads": {
			mrBead("bd-mr-1", "Fix parser", map[string]string{
				"branch": "polecat/nux/bd-1", "target": "main",
				"source_issue": "bd-1", "worker": "nux", "rig": "beads",
			}),
		},
		"gastown": {
			mrBead("gt-mr-1", "Add dashboard panel", map[string]string{
				"branch": "polecat/chrome/gt-2", "target": "main",
				"source_issue": "gt-2", "worker": "chrome", "rig": "gastown",
			}),
		},
	}, nil)
	ghCalls := stubNoGH(t)

	f := &LiveConvoyFetcher{townRoot: townRoot, ghCmdTimeout: time.Second}
	result, err := f.FetchMergeQueue()
	if err != nil {
		t.Fatalf("FetchMergeQueue: %v", err)
	}

	// Each rig's own beads db is queried, at the rig root.
	want := []string{filepath.Join(townRoot, "beads"), filepath.Join(townRoot, "gastown")}
	if len(*queried) != len(want) {
		t.Fatalf("queried rig paths = %v, want %v", *queried, want)
	}
	for i, p := range want {
		if (*queried)[i] != p {
			t.Errorf("queried[%d] = %q, want %q", i, (*queried)[i], p)
		}
	}

	if len(result.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(result.Rows))
	}
	if len(result.FailedRigs) != 0 {
		t.Errorf("FailedRigs = %v, want empty", result.FailedRigs)
	}

	row := result.Rows[0]
	if row.ID != "bd-mr-1" || row.Repo != "beads" || row.Branch != "polecat/nux/bd-1" ||
		row.Target != "main" || row.Worker != "nux" || row.SourceIssue != "bd-1" {
		t.Errorf("row = %+v, want MR bead fields populated", row)
	}

	// No MR bead recorded a PR, so nothing may be looked up on GitHub.
	if len(*ghCalls) != 0 {
		t.Errorf("gh invoked %v — MR beads with no PR must not trigger lookups", *ghCalls)
	}
}

// TestFetchMergeQueue_NeverListsPRs pins the actual bug: `gh pr list` returned
// the upstream community's 62 open PRs and rendered them as our merge queue.
// No code path may list PRs, ever.
func TestFetchMergeQueue_NeverListsPRs(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, twoRigsConfig)

	stubMRs(t, map[string][]*beads.Issue{
		"gastown": {
			mrBead("gt-mr-1", "Has a PR", map[string]string{
				"rig": "gastown", "pr_url": "https://github.com/ourfork/gastown/pull/7", "pr_number": "7",
			}),
		},
	}, nil)
	ghCalls := stubNoGH(t)

	f := &LiveConvoyFetcher{townRoot: townRoot, ghCmdTimeout: time.Second}
	if _, err := f.FetchMergeQueue(); err != nil {
		t.Fatalf("FetchMergeQueue: %v", err)
	}

	for _, args := range *ghCalls {
		if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
			t.Fatalf("gh pr list invoked (%v) — lists upstream PRs, not our merge queue", args)
		}
		// Enrichment must be a per-MR lookup by number.
		if len(args) >= 2 && args[0] == "pr" && args[1] != "view" {
			t.Errorf("unexpected gh pr subcommand %q, want per-MR view", args[1])
		}
	}
}

// TestFetchMergeQueue_MatchesMQListFiltering covers the filters `gt mq list`
// applies: non-open beads are excluded, and wisps naming another rig must not
// leak into this rig's queue (wisps are shared across the Dolt server).
func TestFetchMergeQueue_MatchesMQListFiltering(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, `{
  "version": 1,
  "rigs": {"gastown": {"git_url": "git@github.com:ourfork/gastown.git"}}
}`)

	closed := mrBead("gt-mr-closed", "Already merged", map[string]string{"rig": "gastown"})
	closed.Status = "closed"

	otherRig := mrBead("gt-mr-other", "Belongs to beads", map[string]string{"rig": "beads"})

	stubMRs(t, map[string][]*beads.Issue{
		"gastown": {
			mrBead("gt-mr-open", "Real queue entry", map[string]string{"rig": "gastown"}),
			closed,
			otherRig,
			nil,
		},
	}, nil)
	stubNoGH(t)

	f := &LiveConvoyFetcher{townRoot: townRoot, ghCmdTimeout: time.Second}
	result, err := f.FetchMergeQueue()
	if err != nil {
		t.Fatalf("FetchMergeQueue: %v", err)
	}

	if len(result.Rows) != 1 {
		t.Fatalf("rows = %d (%+v), want 1", len(result.Rows), result.Rows)
	}
	if result.Rows[0].ID != "gt-mr-open" {
		t.Errorf("row ID = %q, want gt-mr-open", result.Rows[0].ID)
	}
}

// TestFetchMergeQueue_LargeQueueNotTruncated uses a >30-MR fixture. The old
// implementation shelled out to `gh pr list` with no --limit and silently
// capped at gh's 30-item default; the bead-backed queue has no such cap.
func TestFetchMergeQueue_LargeQueueNotTruncated(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, `{
  "version": 1,
  "rigs": {"gastown": {"git_url": "git@github.com:ourfork/gastown.git"}}
}`)

	const count = 111
	mrs := make([]*beads.Issue, 0, count)
	for i := 1; i <= count; i++ {
		mrs = append(mrs, mrBead(fmt.Sprintf("gt-mr-%d", i), fmt.Sprintf("MR %d", i), map[string]string{
			"rig": "gastown", "branch": fmt.Sprintf("polecat/chrome/gt-%d", i), "target": "main",
		}))
	}

	stubMRs(t, map[string][]*beads.Issue{"gastown": mrs}, nil)
	stubNoGH(t)

	f := &LiveConvoyFetcher{townRoot: townRoot, ghCmdTimeout: time.Second}
	result, err := f.FetchMergeQueue()
	if err != nil {
		t.Fatalf("FetchMergeQueue: %v", err)
	}

	if len(result.Rows) != count {
		t.Errorf("rows = %d, want %d — queue must not be capped at gh's 30 default", len(result.Rows), count)
	}
	// The dashboard total must equal what `gt mq list gastown` would report.
	if got := (&LiveConvoyFetcher{townRoot: townRoot}).getMergeQueueCount(); got != count {
		t.Errorf("getMergeQueueCount() = %d, want %d", got, count)
	}
}

// TestFetchMergeQueue_PREnrichmentOnlyWhenRecorded asserts PR link and CI
// status appear only for MR beads that actually carry PRURL/PRNumber, and that
// the lookup is a per-MR view by number.
func TestFetchMergeQueue_PREnrichmentOnlyWhenRecorded(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, `{
  "version": 1,
  "rigs": {"gastown": {"git_url": "git@github.com:ourfork/gastown.git"}}
}`)

	stubMRs(t, map[string][]*beads.Issue{
		"gastown": {
			mrBead("gt-mr-nopr", "No PR recorded", map[string]string{"rig": "gastown", "branch": "b1", "target": "main"}),
			mrBead("gt-mr-pr", "Has PR", map[string]string{
				"rig": "gastown", "branch": "b2", "target": "main",
				"pr_url": "https://github.com/ourfork/gastown/pull/42", "pr_number": "42",
			}),
		},
	}, nil)

	original := fetcherRunCmd
	t.Cleanup(func() { fetcherRunCmd = original })
	var ghArgs [][]string
	fetcherRunCmd = func(_ time.Duration, name string, args ...string) (*bytes.Buffer, error) {
		if name != "gh" {
			return nil, fmt.Errorf("unexpected command %s", name)
		}
		ghArgs = append(ghArgs, args)
		return bytes.NewBufferString(`{"number":42,"mergeable":"MERGEABLE","statusCheckRollup":[{"conclusion":"success"}]}`), nil
	}

	f := &LiveConvoyFetcher{townRoot: townRoot, ghCmdTimeout: time.Second}
	result, err := f.FetchMergeQueue()
	if err != nil {
		t.Fatalf("FetchMergeQueue: %v", err)
	}

	if len(result.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(result.Rows))
	}

	noPR := result.Rows[0]
	if noPR.HasPR {
		t.Error("MR without pr_url must not claim a PR")
	}
	if noPR.CIStatus != "" {
		t.Errorf("CIStatus = %q for an MR with no PR, want empty", noPR.CIStatus)
	}

	withPR := result.Rows[1]
	if !withPR.HasPR || withPR.Number != 42 {
		t.Errorf("row = %+v, want HasPR with number 42", withPR)
	}
	if withPR.CIStatus != "pass" || withPR.Mergeable != "ready" {
		t.Errorf("CIStatus=%q Mergeable=%q, want pass/ready", withPR.CIStatus, withPR.Mergeable)
	}

	// Exactly one lookup, and it must be a per-MR view of PR 42.
	if len(ghArgs) != 1 {
		t.Fatalf("gh calls = %d (%v), want exactly 1 per-MR lookup", len(ghArgs), ghArgs)
	}
	got := strings.Join(ghArgs[0], " ")
	if !strings.HasPrefix(got, "pr view 42 --repo ourfork/gastown") {
		t.Errorf("gh args = %q, want a per-MR view of ourfork/gastown#42", got)
	}
}

// TestFetchMergeQueue_ReportsFailedRigs ensures a rig whose query fails is
// surfaced rather than silently shrinking the count.
func TestFetchMergeQueue_ReportsFailedRigs(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, twoRigsConfig)

	stubMRs(t, map[string][]*beads.Issue{
		"gastown": {mrBead("gt-mr-1", "Fine", map[string]string{"rig": "gastown"})},
	}, map[string]bool{"beads": true})
	stubNoGH(t)

	f := &LiveConvoyFetcher{townRoot: townRoot, ghCmdTimeout: time.Second}
	result, err := f.FetchMergeQueue()
	if err != nil {
		t.Fatalf("FetchMergeQueue: %v", err)
	}

	if len(result.Rows) != 1 {
		t.Errorf("rows = %d, want 1 (healthy rig still renders)", len(result.Rows))
	}
	if len(result.FailedRigs) != 1 || result.FailedRigs[0] != "beads" {
		t.Errorf("FailedRigs = %v, want [beads]", result.FailedRigs)
	}
}

func TestParsePRURL(t *testing.T) {
	tests := []struct {
		url        string
		wantRepo   string
		wantNumber int
		wantOK     bool
	}{
		{"https://github.com/ourfork/gastown/pull/42", "ourfork/gastown", 42, true},
		{"http://github.com/o/r/pull/1", "o/r", 1, true},
		{"https://github.com/o/r/issues/5", "", 0, false},
		{"not a url", "", 0, false},
		{"", "", 0, false},
	}

	for _, tt := range tests {
		repo, number, ok := parsePRURL(tt.url)
		if ok != tt.wantOK || repo != tt.wantRepo || number != tt.wantNumber {
			t.Errorf("parsePRURL(%q) = (%q, %d, %v), want (%q, %d, %v)",
				tt.url, repo, number, ok, tt.wantRepo, tt.wantNumber, tt.wantOK)
		}
	}
}
