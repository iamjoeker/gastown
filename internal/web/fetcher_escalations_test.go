package web

// The escalation panel is the one panel whose job is to report trouble, so a
// query it could not run must never look like a town with nothing to report.
// These tests assert on the returned ERROR, not on the returned slice: the old
// bug returned (nil, nil), so any assertion about the rows alone passes against
// it (gt-edty).

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeBdFetcher builds a fetcher whose bd is the given shell script.
func fakeBdFetcher(t *testing.T, script string) *LiveConvoyFetcher {
	t.Helper()

	bdPath := filepath.Join(t.TempDir(), "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	return &LiveConvoyFetcher{
		townRoot:   t.TempDir(),
		cmdTimeout: 30 * time.Second,
		bdBin:      bdPath,
	}
}

func TestFetchEscalations_BdFailureIsAnErrorNotAnEmptyList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	// Output on stderr only: runBdCmd forgives a non-zero exit that still
	// produced stdout, so a hard failure must produce none.
	f := fakeBdFetcher(t, `#!/bin/sh
echo "dial tcp 127.0.0.1:3307: connect: connection refused" >&2
exit 1
`)

	rows, err := f.FetchEscalations()
	if err == nil {
		t.Fatal("a failed bd query must return an error, not an empty escalation list")
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the query failed", rows)
	}
	if !strings.Contains(err.Error(), "listing escalations") {
		t.Errorf("error should say what failed, got: %v", err)
	}
}

func TestFetchEscalations_UnparseableOutputIsAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	f := fakeBdFetcher(t, `#!/bin/sh
echo "not json"
`)

	if _, err := f.FetchEscalations(); err == nil {
		t.Fatal("unparseable bd output must return an error")
	}
}

func TestFetchEscalations_QuietTownReturnsEmptyWithoutError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	// The other half of the contract: zero really does mean zero when bd
	// answers. Without this, "always error" would pass the test above.
	f := fakeBdFetcher(t, `#!/bin/sh
echo "[]"
`)

	rows, err := f.FetchEscalations()
	if err != nil {
		t.Fatalf("a successful empty query must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

func TestFetchEscalations_ParsesRows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	f := fakeBdFetcher(t, `#!/bin/sh
cat <<'JSON'
[
  {"id":"hq-1","title":"Dolt unreachable","created_by":"gastown/witness","labels":["gt:escalation","severity:critical"]},
  {"id":"hq-2","title":"Stalled polecat","created_by":"gastown/witness","labels":["gt:escalation","acked"]}
]
JSON
`)

	rows, err := f.FetchEscalations()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	// Critical sorts ahead of the unlabeled (medium) row.
	if rows[0].ID != "hq-1" || rows[0].Severity != "critical" {
		t.Errorf("first row = %+v, want critical hq-1", rows[0])
	}
	if rows[1].Severity != "medium" || !rows[1].Acked {
		t.Errorf("second row = %+v, want acked medium", rows[1])
	}
}

// The panel must show pinned escalations. `bd list --status=open` is silently
// `--no-pinned` and offers no include-pinned flag, so a single default query
// drops them — and pinning is what an operator does to an escalation to keep it
// in view. Measured on hq 2026-08-26 this query returned 1 of the 4 open
// escalations; the 3 it could not see were pinned, two of them HIGH (gt-z5h7).
//
// The fake bd answers the two halves differently, so a fetcher that asks once
// cannot pass: it sees only hq-unpinned, whichever half it asks for.
func TestFetchEscalations_IncludesPinnedEscalations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	f := fakeBdFetcher(t, `#!/bin/sh
pinned=0
for arg in "$@"; do
  if [ "$arg" = "--pinned" ]; then pinned=1; fi
done
if [ "$pinned" = "1" ]; then
  echo '[{"id":"hq-pinned","title":"Agent logged out","created_by":"gastown/witness","labels":["gt:escalation","severity:high"]}]'
else
  echo '[{"id":"hq-unpinned","title":"Test isolation guard","created_by":"beads/witness","labels":["gt:escalation","severity:high"]}]'
fi
`)

	rows, err := f.FetchEscalations()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.ID] = true
	}
	if !seen["hq-pinned"] {
		t.Errorf("rows = %+v, want the pinned escalation included: pinning must not delete it from the panel", rows)
	}
	if !seen["hq-unpinned"] {
		t.Errorf("rows = %+v, want the unpinned escalation still included", rows)
	}
	if len(rows) != 2 {
		t.Errorf("len(rows) = %d, want 2: the two halves are unioned, not double-counted", len(rows))
	}
}
