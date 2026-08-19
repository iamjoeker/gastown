package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func newMovedBeadTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	routes := `{"prefix": "hq-", "path": "."}
{"prefix": "dn-", "path": "duly_noted"}
{"prefix": "gt-", "path": "gastown/mayor/rig"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, beads.RoutesFileName), []byte(routes), 0644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"duly_noted", "gastown/mayor/rig"} {
		if err := os.MkdirAll(filepath.Join(townRoot, p, ".beads"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return townRoot
}

// stubOpenBeads installs the listing seam from a map of rig name → open rows.
func stubOpenBeads(t *testing.T, rows map[string][]beadRow, failures map[string]error) {
	t.Helper()
	orig := listOpenBeadIDs
	t.Cleanup(func() { listOpenBeadIDs = orig })
	listOpenBeadIDs = func(store beads.BeadStore) ([]beadRow, error) {
		if err, ok := failures[store.Rig]; ok {
			return nil, err
		}
		return rows[store.Rig], nil
	}
}

func TestMovedBeadCheckFlagsForeignPrefixes(t *testing.T) {
	townRoot := newMovedBeadTown(t)
	stubOpenBeads(t, map[string][]beadRow{
		"":           {{ID: "hq-1", Title: "town work"}},
		"duly_noted": {{ID: "dn-own", Title: "still filed here"}},
		"gastown": {
			{ID: "gt-1", Title: "native"},
			{ID: "dn-cqu", Title: "Refinery spawned stuck at Claude Code welcome prompt"},
			{ID: "dn-r63", Title: "gt escalate list reports no escalations"},
		},
	}, nil)

	res := NewMovedBeadCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning; message: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "2 open bead(s)") {
		t.Errorf("Message = %q, want a count of 2", res.Message)
	}
	joined := strings.Join(res.Details, "\n")
	for _, want := range []string{"dn-cqu open in rig gastown", "dn-r63 open in rig gastown", "prefix names rig duly_noted"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "gt-1") || strings.Contains(joined, "dn-own") || strings.Contains(joined, "hq-1") {
		t.Errorf("beads sitting in the store their prefix names must not be flagged:\n%s", joined)
	}
}

// Identity and runtime records for every rig live in the town store under that
// rig's prefix by design, and would otherwise swamp the real finding.
func TestMovedBeadCheckIgnoresTownBookkeeping(t *testing.T) {
	townRoot := newMovedBeadTown(t)
	stubOpenBeads(t, map[string][]beadRow{
		"": {
			{ID: "gt-gastown-polecat-fury", Type: "task", Labels: []string{"gt:agent"}},
			{ID: "dn-rig-duly_noted", Type: "rig", Labels: []string{"gt:rig"}},
			{ID: "bd-beads-witness", Type: "task", Labels: []string{"gt:agent"}},
			{ID: "gt-msg", Type: "task", Labels: []string{"gt:message"}},
			{ID: "gt-wisp-1", Type: "wisp"},
		},
	}, nil)

	res := NewMovedBeadCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK; details: %v", res.Status, res.Details)
	}
}

// gt:keep and gt:standing-orders mark real work automation must not close. Such
// a bead in the wrong store is still a finding.
func TestMovedBeadCheckKeepsProtectedWork(t *testing.T) {
	townRoot := newMovedBeadTown(t)
	stubOpenBeads(t, map[string][]beadRow{
		"gastown": {{ID: "dn-fhw", Type: "chore", Labels: []string{"gt:keep"}, Title: "standing watch"}},
	}, nil)

	res := NewMovedBeadCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning; details: %v", res.Status, res.Details)
	}
	if !strings.Contains(strings.Join(res.Details, "\n"), "dn-fhw open in rig gastown") {
		t.Errorf("details = %v, want dn-fhw reported", res.Details)
	}
}

func TestMovedBeadCheckCleanTown(t *testing.T) {
	townRoot := newMovedBeadTown(t)
	stubOpenBeads(t, map[string][]beadRow{
		"":           {{ID: "hq-1"}},
		"duly_noted": {{ID: "dn-1"}},
		"gastown":    {{ID: "gt-1"}},
	}, nil)

	res := NewMovedBeadCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK; message: %s, details: %v", res.Status, res.Message, res.Details)
	}
}

// A prefix absent from routes.jsonl is a different problem; this check must not
// claim those rows.
func TestMovedBeadCheckIgnoresUnknownPrefixes(t *testing.T) {
	townRoot := newMovedBeadTown(t)
	stubOpenBeads(t, map[string][]beadRow{
		"gastown": {{ID: "zz-1"}, {ID: "nohyphen"}},
	}, nil)

	res := NewMovedBeadCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK; details: %v", res.Status, res.Details)
	}
}

// An unreadable store must be reported, not silently counted as empty.
func TestMovedBeadCheckReportsUnreadableStores(t *testing.T) {
	townRoot := newMovedBeadTown(t)
	stubOpenBeads(t, nil, map[string]error{"duly_noted": errors.New("connection refused")})

	res := NewMovedBeadCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning for an unreadable store", res.Status)
	}
	if !strings.Contains(strings.Join(res.Details, "\n"), "connection refused") {
		t.Errorf("details should carry the read failure, got: %v", res.Details)
	}
}

func TestMovedBeadCheckNoRoutes(t *testing.T) {
	res := NewMovedBeadCheck().Run(&CheckContext{TownRoot: t.TempDir()})
	if res.Status != StatusOK {
		t.Errorf("Status = %v, want StatusOK when the town has no routes file", res.Status)
	}
}

func TestParseBeadRowsAcceptsBothShapes(t *testing.T) {
	rows, err := parseBeadRows([]byte(`[{"id":"gt-1","title":"a"}]`))
	if err != nil || len(rows) != 1 || rows[0].ID != "gt-1" {
		t.Errorf("bare array: rows=%v err=%v", rows, err)
	}
	rows, err = parseBeadRows([]byte(`{"count":1,"issues":[{"id":"gt-2","title":"b"}]}`))
	if err != nil || len(rows) != 1 || rows[0].ID != "gt-2" {
		t.Errorf("envelope: rows=%v err=%v", rows, err)
	}
	if rows, err := parseBeadRows([]byte("  ")); err != nil || rows != nil {
		t.Errorf("empty output: rows=%v err=%v", rows, err)
	}
	if _, err := parseBeadRows([]byte("not json")); err == nil {
		t.Error("malformed output must return an error, not an empty result")
	}
}
