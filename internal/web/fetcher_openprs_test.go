package web

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

// stubOpenPRs replaces the `gh pr list` query with a per-repo fixture, records
// the repos queried, and asserts every call passes an explicit --limit (never
// relying on gh's own default of 30 — see gt-4qp).
func stubOpenPRs(t *testing.T, byRepo map[string]string, failRepos map[string]bool) *[]string {
	t.Helper()

	original := fetcherListOpenPRs
	t.Cleanup(func() { fetcherListOpenPRs = original })

	var queried []string
	fetcherListOpenPRs = func(timeout time.Duration, repo string) (*bytes.Buffer, error) {
		queried = append(queried, repo)
		if failRepos[repo] {
			return nil, fmt.Errorf("gh: repo not found or access denied: %s", repo)
		}
		body, ok := byRepo[repo]
		if !ok {
			body = "[]"
		}
		return bytes.NewBufferString(body), nil
	}
	return &queried
}

// TestFetchOpenPRs_ResolvesPushURLOverGitURL is the core regression test: the
// repo queried per rig must be push_url when set, falling back to git_url only
// when push_url is absent. Querying git_url unconditionally is exactly the
// gt-4qp bug (upstream org's unrelated PRs rendered as this town's).
func TestFetchOpenPRs_ResolvesPushURLOverGitURL(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, `{
	  "version": 1,
	  "rigs": {
	    "beads": {"git_url": "git@github.com:upstreamorg/beads.git", "push_url": "git@github.com:ourfork/beads.git"},
	    "duly_noted": {"git_url": "git@github.com:iamjoeker/work_journal.git"}
	  }
	}`)

	queried := stubOpenPRs(t, map[string]string{
		"ourfork/beads": `[{"number":1,"title":"fork PR","url":"https://github.com/ourfork/beads/pull/1","headRefName":"x","createdAt":"2026-01-01T00:00:00Z","isDraft":false,"author":{"login":"a"}}]`,
		"iamjoeker/work_journal": `[
			{"number":166,"title":"Voice jot quick capture","url":"https://github.com/iamjoeker/work_journal/pull/166","headRefName":"feat/voice-jot-capture-api","createdAt":"2026-01-01T00:00:00Z","isDraft":false,"author":{"login":"iamjoeker"}},
			{"number":168,"title":"ci: review verdict","url":"https://github.com/iamjoeker/work_journal/pull/168","headRefName":"ci/x","createdAt":"2026-01-01T00:00:00Z","isDraft":false,"author":{"login":"iamjoeker"}}
		]`,
	}, nil)

	f := &LiveConvoyFetcher{townRoot: townRoot, ghCmdTimeout: time.Second}
	result, err := f.FetchOpenPRs()
	if err != nil {
		t.Fatalf("FetchOpenPRs: %v", err)
	}

	wantQueried := map[string]bool{"ourfork/beads": true, "iamjoeker/work_journal": true}
	if len(*queried) != 2 {
		t.Fatalf("queried repos = %v, want 2 entries", *queried)
	}
	for _, q := range *queried {
		if !wantQueried[q] {
			t.Errorf("queried unexpected repo %q (upstreamorg/beads must never be queried when push_url is set)", q)
		}
	}

	if len(result.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(result.Rows))
	}

	var got166, got168 bool
	for _, row := range result.Rows {
		if row.Repo != "iamjoeker/work_journal" {
			continue
		}
		if row.Number == 166 {
			got166 = true
			if row.Rig != "duly_noted" {
				t.Errorf("PR 166 Rig = %q, want duly_noted", row.Rig)
			}
		}
		if row.Number == 168 {
			got168 = true
		}
	}
	if !got166 || !got168 {
		t.Errorf("expected both work_journal#166 and #168 in rows, got166=%v got168=%v", got166, got168)
	}
}

// TestFetchOpenPRs_ExplicitLimitPassed ensures every `gh pr list` call carries
// an explicit --limit. gh's own default (30) is what let gt-4qp's bug silently
// truncate 151 PRs down to a reported 62; this must never happen again.
func TestFetchOpenPRs_ExplicitLimitPassed(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, `{
	  "version": 1,
	  "rigs": {"gastown": {"git_url": "git@github.com:iamjoeker/gastown.git"}}
	}`)

	original := fetcherRunCmd
	t.Cleanup(func() { fetcherRunCmd = original })

	var sawLimit bool
	fetcherRunCmd = func(_ time.Duration, name string, args ...string) (*bytes.Buffer, error) {
		if name != "gh" {
			return bytes.NewBufferString(""), nil
		}
		for i, a := range args {
			if a == "--limit" && i+1 < len(args) && args[i+1] == fmt.Sprintf("%d", openPRListLimit) {
				sawLimit = true
			}
		}
		return bytes.NewBufferString("[]"), nil
	}

	f := &LiveConvoyFetcher{townRoot: townRoot, ghCmdTimeout: time.Second}
	if _, err := f.FetchOpenPRs(); err != nil {
		t.Fatalf("FetchOpenPRs: %v", err)
	}

	if !sawLimit {
		t.Errorf("gh pr list was not called with an explicit --limit %d", openPRListLimit)
	}
}

// TestFetchOpenPRs_FailedRepoDoesNotBlankPanel ensures a repo we have no
// access to (404, private, rate-limited) is named in FailedRepos but does not
// prevent other rigs' PRs from rendering.
func TestFetchOpenPRs_FailedRepoDoesNotBlankPanel(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, `{
	  "version": 1,
	  "rigs": {
	    "gastown": {"git_url": "git@github.com:iamjoeker/gastown.git"},
	    "duly_noted": {"git_url": "git@github.com:iamjoeker/work_journal.git"}
	  }
	}`)

	stubOpenPRs(t, map[string]string{
		"iamjoeker/work_journal": `[{"number":166,"title":"t","url":"https://github.com/iamjoeker/work_journal/pull/166","headRefName":"x","createdAt":"2026-01-01T00:00:00Z","isDraft":false,"author":{"login":"a"}}]`,
	}, map[string]bool{"iamjoeker/gastown": true})

	f := &LiveConvoyFetcher{townRoot: townRoot, ghCmdTimeout: time.Second}
	result, err := f.FetchOpenPRs()
	if err != nil {
		t.Fatalf("FetchOpenPRs: %v", err)
	}

	if len(result.Rows) != 1 {
		t.Fatalf("rows = %d, want 1 (healthy repo still renders)", len(result.Rows))
	}
	if len(result.FailedRepos) != 1 || result.FailedRepos[0] != "iamjoeker/gastown" {
		t.Errorf("FailedRepos = %v, want [iamjoeker/gastown]", result.FailedRepos)
	}
}

// TestFetchOpenPRs_TruncatedWhenAtLimit ensures a repo whose result count hits
// openPRListLimit is flagged in TruncatedRepos rather than silently reported
// as a total.
func TestFetchOpenPRs_TruncatedWhenAtLimit(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, `{
	  "version": 1,
	  "rigs": {"gastown": {"git_url": "git@github.com:iamjoeker/gastown.git"}}
	}`)

	var items []string
	for i := 1; i <= openPRListLimit; i++ {
		items = append(items, fmt.Sprintf(`{"number":%d,"title":"t","url":"https://github.com/iamjoeker/gastown/pull/%d","headRefName":"x","createdAt":"2026-01-01T00:00:00Z","isDraft":false,"author":{"login":"a"}}`, i, i))
	}
	body := "[" + joinComma(items) + "]"

	stubOpenPRs(t, map[string]string{"iamjoeker/gastown": body}, nil)

	f := &LiveConvoyFetcher{townRoot: townRoot, ghCmdTimeout: time.Second}
	result, err := f.FetchOpenPRs()
	if err != nil {
		t.Fatalf("FetchOpenPRs: %v", err)
	}

	if len(result.Rows) != openPRListLimit {
		t.Fatalf("rows = %d, want %d", len(result.Rows), openPRListLimit)
	}
	if len(result.TruncatedRepos) != 1 || result.TruncatedRepos[0] != "iamjoeker/gastown" {
		t.Errorf("TruncatedRepos = %v, want [iamjoeker/gastown]", result.TruncatedRepos)
	}
}

func joinComma(items []string) string {
	out := ""
	for i, it := range items {
		if i > 0 {
			out += ","
		}
		out += it
	}
	return out
}

// TestGitURLToRepoPath covers SSH and HTTPS GitHub remote forms, plus
// unsupported inputs.
func TestGitURLToRepoPath(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"git@github.com:iamjoeker/work_journal.git", "iamjoeker/work_journal"},
		{"https://github.com/iamjoeker/work_journal.git", "iamjoeker/work_journal"},
		{"https://github.com/iamjoeker/work_journal", "iamjoeker/work_journal"},
		{"git@gitlab.com:owner/repo.git", ""},
		{"/local/path/to/repo", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := gitURLToRepoPath(tt.url); got != tt.want {
			t.Errorf("gitURLToRepoPath(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}
