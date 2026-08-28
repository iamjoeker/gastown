package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

func TestGetFormulaNames(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	formulasDir := filepath.Join(tmpDir, "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatalf("creating formulas dir: %v", err)
	}

	// Create some formula files
	formulas := []string{
		constants.MolDeaconPatrol + ".formula.toml",
		constants.MolWitnessPatrol + ".formula.toml",
		"shiny.formula.toml",
	}
	for _, f := range formulas {
		path := filepath.Join(formulasDir, f)
		if err := os.WriteFile(path, []byte("# test"), 0644); err != nil {
			t.Fatalf("writing %s: %v", f, err)
		}
	}

	// Also create a non-formula file (should be ignored)
	if err := os.WriteFile(filepath.Join(formulasDir, ".installed.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("writing .installed.json: %v", err)
	}

	// Test
	names := getFormulaNames(tmpDir)
	if names == nil {
		t.Fatal("getFormulaNames returned nil")
	}

	expected := []string{constants.MolDeaconPatrol, constants.MolWitnessPatrol, "shiny"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected formula name %q not found", name)
		}
	}

	// Should not include the .installed.json file
	if names[".installed"] {
		t.Error(".installed should not be in formula names")
	}

	if len(names) != len(expected) {
		t.Errorf("got %d formula names, want %d", len(names), len(expected))
	}
}

func issueIDs(issues []*beads.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

func TestGetFormulaNames_NonexistentDir(t *testing.T) {
	names := getFormulaNames("/nonexistent/path")
	if names != nil {
		t.Error("expected nil for nonexistent directory")
	}
}

func TestFilterFormulaScaffolds(t *testing.T) {
	formulaNames := map[string]bool{
		constants.MolDeaconPatrol:  true,
		constants.MolWitnessPatrol: true,
	}

	issues := []*beads.Issue{
		{ID: constants.MolDeaconPatrol, Title: constants.MolDeaconPatrol},
		{ID: constants.MolDeaconPatrol + ".inbox-check", Title: "Handle callbacks"},
		{ID: constants.MolDeaconPatrol + ".health-scan", Title: "Check health"},
		{ID: constants.MolWitnessPatrol, Title: constants.MolWitnessPatrol},
		{ID: constants.MolWitnessPatrol + ".loop-or-exit", Title: "Loop or exit"},
		{ID: "hq-123", Title: "Real work item"},
		{ID: "hq-wisp-abc", Title: "Actual wisp"},
		{ID: "gt-456", Title: "Project issue"},
	}

	filtered := filterFormulaScaffolds(issues, formulaNames)

	// Should only have the non-scaffold issues
	if len(filtered) != 3 {
		t.Errorf("got %d filtered issues, want 3", len(filtered))
	}

	expectedIDs := map[string]bool{
		"hq-123":      true,
		"hq-wisp-abc": true,
		"gt-456":      true,
	}
	for _, issue := range filtered {
		if !expectedIDs[issue.ID] {
			t.Errorf("unexpected issue in filtered result: %s", issue.ID)
		}
	}
}

func TestFilterFormulaScaffolds_NilFormulaNames(t *testing.T) {
	issues := []*beads.Issue{
		{ID: "hq-123", Title: "Real work"},
		{ID: constants.MolDeaconPatrol, Title: "Would be filtered"},
	}

	// With nil formula names, should return all issues unchanged
	filtered := filterFormulaScaffolds(issues, nil)
	if len(filtered) != len(issues) {
		t.Errorf("got %d issues, want %d (nil formulaNames should return all)", len(filtered), len(issues))
	}
}

func TestFilterFormulaScaffolds_EmptyFormulaNames(t *testing.T) {
	issues := []*beads.Issue{
		{ID: "hq-123", Title: "Real work"},
		{ID: constants.MolDeaconPatrol, Title: "Would be filtered"},
	}

	// With empty formula names, should return all issues unchanged
	filtered := filterFormulaScaffolds(issues, map[string]bool{})
	if len(filtered) != len(issues) {
		t.Errorf("got %d issues, want %d (empty formulaNames should return all)", len(filtered), len(issues))
	}
}

func TestFilterFormulaScaffolds_EmptyIssues(t *testing.T) {
	formulaNames := map[string]bool{constants.MolDeaconPatrol: true}
	filtered := filterFormulaScaffolds([]*beads.Issue{}, formulaNames)
	if len(filtered) != 0 {
		t.Errorf("got %d issues, want 0", len(filtered))
	}
}

func TestGetWispIDsUsesBdMolWispList(t *testing.T) {
	beadsPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(`{"id":"stale-jsonl-wisp"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	bdPath := filepath.Join(binDir, "bd")
	bdScript := `#!/bin/sh
if [ "$1" = "mol" ] && [ "$2" = "wisp" ] && [ "$3" = "list" ] && [ "$4" = "--json" ]; then
  printf '{"wisps":[{"id":"dolt-wisp-1"},{"id":"dolt-wisp-2"}],"count":2}\n'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ids := getWispIDs(beadsPath)
	if !ids["dolt-wisp-1"] || !ids["dolt-wisp-2"] {
		t.Fatalf("expected IDs from bd mol wisp list, got %#v", ids)
	}
	if ids["stale-jsonl-wisp"] {
		t.Fatalf("getWispIDs read stale issues.jsonl; got %#v", ids)
	}
}

func TestFilterFormulaScaffolds_DotInNonScaffold(t *testing.T) {
	// Issue ID has a dot but prefix is not a formula name
	formulaNames := map[string]bool{constants.MolDeaconPatrol: true}

	issues := []*beads.Issue{
		{ID: "hq-cv.synthesis-step", Title: "Convoy synthesis"},
		{ID: "some.other.thing", Title: "Random dotted ID"},
	}

	filtered := filterFormulaScaffolds(issues, formulaNames)
	if len(filtered) != 2 {
		t.Errorf("got %d issues, want 2 (non-formula dots should not filter)", len(filtered))
	}
}

func TestFilterReadyIssuesByRoute(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("creating town beads dir: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"hq-","path":"."}`,
		`{"prefix":"hq-cv-","path":"."}`,
		`{"prefix":"bds-","path":"bd_symphony/mayor/rig"}`,
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("writing routes: %v", err)
	}

	issues := []*beads.Issue{
		{ID: "hq-123", Title: "town work"},
		{ID: "hq-cv-123", Title: "town convoy"},
		{ID: "bds-town-stale", Title: "wrongly-created town bds row"},
		{ID: "unknown-123", Title: "unknown route"},
	}
	filtered := filterReadyIssuesByRoute(townRoot, "town", issues)
	if got, want := issueIDs(filtered), []string{"hq-123", "hq-cv-123"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("town filtered IDs = %v, want %v", got, want)
	}

	issues = []*beads.Issue{
		{ID: "bds-123", Title: "bd_symphony work"},
		{ID: "hq-123", Title: "town work in rig result"},
		{ID: "gt-123", Title: "other rig work"},
		{ID: "unknown-123", Title: "unknown route"},
	}
	filtered = filterReadyIssuesByRoute(townRoot, "bd_symphony", issues)
	if got, want := issueIDs(filtered), []string{"bds-123"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rig filtered IDs = %v, want %v", got, want)
	}
}

// --- Mail exclusion (gt-cw1u) ------------------------------------------------
//
// Mail is filed as an ordinary bead so it survives a session death, which also
// puts every unread message in the work queue. Measured on the hq store
// 2026-08-25: `bd ready -n 0` returned 605 rows, 358 of them (59%) gt:message,
// and all eleven that reached this command's town section were P0 — eleven of
// the seventeen slots at the top of the queue.

func TestFilterMailBeads(t *testing.T) {
	issues := []*beads.Issue{
		{ID: "gt-work", Type: "bug", Title: "Real bug"},
		{ID: "hq-mail", Type: "task", Title: "MAYOR RULING", Labels: []string{"gt:message", "from:mayor/"}},
		{ID: "hq-typed", Type: "message", Title: "Legacy typed message"},
		{ID: "gt-work2", Type: "task", Title: "Real task", Labels: []string{"area:refinery"}},
	}

	got := filterMailBeads(issues)

	var ids []string
	for _, issue := range got {
		ids = append(ids, issue.ID)
	}
	want := []string{"gt-work", "gt-work2"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("filterMailBeads kept %v, want %v", ids, want)
	}
}

func TestFilterMailBeadsIncludeMail(t *testing.T) {
	prev := readyIncludeMail
	readyIncludeMail = true
	t.Cleanup(func() { readyIncludeMail = prev })

	issues := []*beads.Issue{
		{ID: "gt-work", Type: "bug"},
		{ID: "hq-mail", Type: "task", Labels: []string{"gt:message"}},
	}

	if got := filterMailBeads(issues); len(got) != 2 {
		t.Errorf("--include-mail must keep mail: kept %d of 2", len(got))
	}
}

// TestFilterMailBeadsSpares keeps the exclusion from widening past mail. The
// other gt: labels on an ordinary bead are not a reason to hide it from the
// work queue, and a filter that matched any gt: prefix would empty the listing.
func TestFilterMailBeadsSpares(t *testing.T) {
	issues := []*beads.Issue{
		{ID: "gt-esc", Type: "bug", Labels: []string{"gt:escalation"}},
		{ID: "gt-keep", Type: "task", Labels: []string{"gt:keep"}},
		{ID: "gt-conv", Type: "task", Labels: []string{"gt:convoy"}},
	}

	if got := filterMailBeads(issues); len(got) != 3 {
		t.Errorf("filterMailBeads dropped a non-mail bead: kept %d of 3", len(got))
	}
}

// TestReadyIncludeMailFlagRegistered pins the escape hatch. Excluding mail by
// default is only defensible while the old listing is one flag away.
func TestReadyIncludeMailFlagRegistered(t *testing.T) {
	flag := readyCmd.Flags().Lookup("include-mail")
	if flag == nil {
		t.Fatal("gt ready must offer --include-mail to restore the unfiltered listing")
	}
	if flag.DefValue != "false" {
		t.Errorf("--include-mail default = %q, want \"false\" (mail excluded unless asked for)", flag.DefValue)
	}
	if !strings.Contains(readyCmd.Long, "--include-mail") {
		t.Error("gt ready --help must say how to get mail back")
	}
}
