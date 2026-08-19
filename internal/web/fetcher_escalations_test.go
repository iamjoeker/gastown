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
