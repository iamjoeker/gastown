package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// The Work and Hooks panels used to query the town root alone. Measured against
// a live town, the town root held 521 open beads while the rigs held 65, 7 and 2
// the Work panel never showed, and the Hooks panel rendered 0 while the gastown
// rig held 5. These tests pin the union so a regression to one store fails here
// rather than on a dashboard nobody is checking against bd.
//
// Every case uses at least two non-empty RIG stores: a fixture with one rig
// passes against town-root-only code the moment the town root is empty.

// fakeBdStore is one store's canned answers, keyed by the status bd is asked for.
type fakeBdStore map[string][]map[string]any

// writeFakeBd installs a bd that answers from a per-directory fixture file, so
// each store's reply is decided by the directory the command runs in — the
// property a stubbed callback cannot check. Stores are created under townRoot;
// a store named in failStores exits non-zero with no output, which is how
// runBdCmd tells a real failure from a warning.
func writeFakeBd(t *testing.T, townRoot string, stores map[string]fakeBdStore, failStores ...string) string {
	t.Helper()

	for name, store := range stores {
		dir := townRoot
		if name != townStoreName {
			dir = filepath.Join(townRoot, name)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating store dir %s: %v", name, err)
		}
		for status, beads := range store {
			encoded, err := json.Marshal(beads)
			if err != nil {
				t.Fatalf("encoding fixture %s/%s: %v", name, status, err)
			}
			fixture := filepath.Join(dir, ".fixture-"+status+".json")
			if err := os.WriteFile(fixture, encoded, 0o644); err != nil {
				t.Fatalf("writing fixture %s/%s: %v", name, status, err)
			}
		}
	}
	for _, name := range failStores {
		dir := filepath.Join(townRoot, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating store dir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".fail"), nil, 0o644); err != nil {
			t.Fatalf("marking store %s as failing: %v", name, err)
		}
	}

	// A store with no fixture for the requested status answers with an empty
	// list, the same as a store that genuinely holds nothing of that status.
	script := `#!/bin/sh
if [ -f "$PWD/.fail" ]; then
  exit 1
fi
status=""
for arg in "$@"; do
  case "$arg" in
    --status=*) status=${arg#--status=} ;;
  esac
done
fixture="$PWD/.fixture-$status.json"
if [ -f "$fixture" ]; then
  cat "$fixture"
else
  printf '[]'
fi
`
	bdPath := filepath.Join(t.TempDir(), "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	return bdPath
}

// openBeads builds n ordinary open beads attributed to a store.
//
// The type key is issue_type because that is what `bd list --json` emits. A
// fixture that says "type" instead agrees with a decoder tagged the same way
// and proves nothing about either — which is how the type arm of isInternal sat
// unreachable against live bd while these tests were green.
func openBeads(store string, n int) []map[string]any {
	beads := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		beads = append(beads, map[string]any{
			"id":         fmt.Sprintf("%s-%d", store, i),
			"title":      fmt.Sprintf("work item %d in %s", i, store),
			"issue_type": "task",
			"priority":   2,
			"created_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
		})
	}
	return beads
}

// hookedBeads builds n hooked beads attributed to a store.
func hookedBeads(store string, n int) []map[string]any {
	beads := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		beads = append(beads, map[string]any{
			"id":         fmt.Sprintf("%s-hook-%d", store, i),
			"title":      fmt.Sprintf("hooked item %d in %s", i, store),
			"assignee":   store + "/polecats/worker",
			"updated_at": time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
		})
	}
	return beads
}

// panelFetcher wires a fetcher to a town whose registered rigs are exactly the
// ones the test named, plus the fake bd that answers for them.
func panelFetcher(t *testing.T, stores map[string]fakeBdStore, failStores ...string) *LiveConvoyFetcher {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	rigNames := make([]string, 0, len(stores)+len(failStores))
	for name := range stores {
		if name != townStoreName {
			rigNames = append(rigNames, name)
		}
	}
	rigNames = append(rigNames, failStores...)
	sort.Strings(rigNames)

	entries := make([]string, 0, len(rigNames))
	for _, name := range rigNames {
		entries = append(entries, fmt.Sprintf(`    %q: {"git_url": "git@github.com:upstreamorg/%s.git"}`, name, name))
	}
	rigsConfig := fmt.Sprintf("{\n  \"version\": 1,\n  \"rigs\": {\n%s\n  }\n}", strings.Join(entries, ",\n"))

	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, rigsConfig)
	bdPath := writeFakeBd(t, townRoot, stores, failStores...)

	return &LiveConvoyFetcher{townRoot: townRoot, cmdTimeout: 30 * time.Second, bdBin: bdPath}
}

// idsOf returns the row IDs, sorted, for comparing unions without depending on
// the panels' display ordering.
func idsOf[T any](rows []T, id func(T) string) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, id(r))
	}
	sort.Strings(ids)
	return ids
}

// TestFetchIssues_UnionsEveryStore is the bead's verification rule: the panel
// total equals the sum of the per-store counts of the rows it queries.
func TestFetchIssues_UnionsEveryStore(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {"open": openBeads("town", 2)},
		"beads":       {"open": openBeads("beads", 3)},
		"gastown":     {"open": openBeads("gastown", 4), "hooked": openBeads("gastownhooked", 1)},
	})

	result, err := f.FetchIssues()
	if err != nil {
		t.Fatalf("FetchIssues() error = %v", err)
	}

	if want := 2 + 3 + 4 + 1; len(result.Rows) != want {
		t.Errorf("rows = %d, want %d (sum of per-store counts)", len(result.Rows), want)
	}

	// Naming the rows, not just counting them, is what catches a union that
	// returns the right total from the wrong stores.
	byStore := map[string]int{}
	for _, row := range result.Rows {
		byStore[strings.SplitN(row.ID, "-", 2)[0]]++
	}
	want := map[string]int{"town": 2, "beads": 3, "gastown": 4, "gastownhooked": 1}
	if !reflect.DeepEqual(byStore, want) {
		t.Errorf("rows by store = %v, want %v", byStore, want)
	}

	if result.Partial() {
		t.Errorf("Partial() = true for a complete union, warning = %q", result.Warning())
	}
}

// TestFetchIssues_NamesStoreThatCouldNotAnswer covers the failure the old panel
// swallowed: it skipped a failed list and rendered the shortfall as backlog.
func TestFetchIssues_NamesStoreThatCouldNotAnswer(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {"open": openBeads("town", 2)},
		"beads":       {"open": openBeads("beads", 3)},
		"gastown":     {"open": openBeads("gastown", 4)},
	}, "broken")

	result, err := f.FetchIssues()
	if err != nil {
		t.Fatalf("FetchIssues() error = %v", err)
	}

	if want := []string{"broken"}; !reflect.DeepEqual(result.FailedStores, want) {
		t.Errorf("FailedStores = %v, want %v", result.FailedStores, want)
	}
	// Named once, though the panel asked the broken store twice.
	if got := strings.Count(result.Warning(), "broken"); got != 1 {
		t.Errorf("Warning() = %q, want %q named exactly once", result.Warning(), "broken")
	}
	if len(result.Rows) != 9 {
		t.Errorf("rows = %d, want 9 (the readable stores still answer)", len(result.Rows))
	}
}

// TestFetchIssues_TruncationCountsBeadsNotVisibleRows pins the ordering the
// panel depends on: the town root is mostly internal beads, so a store can fill
// the whole safety cap and still display almost nothing. Filtering before the
// resolver counts would report that store as complete, and the count beside the
// panel would then be a wrong number rather than a stated floor.
func TestFetchIssues_TruncationCountsBeadsNotVisibleRows(t *testing.T) {
	// A full safety cap of beads the panel hides, plus two it shows.
	internal := make([]map[string]any, 0, issuesPerStoreLimit)
	for i := 0; i < issuesPerStoreLimit; i++ {
		internal = append(internal, map[string]any{
			"id":     fmt.Sprintf("town-msg-%d", i),
			"title":  "mail",
			"labels": []string{"gt:message"},
		})
	}

	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {"open": internal},
		"beads":       {"open": openBeads("beads", 3)},
		"gastown":     {"open": openBeads("gastown", 4)},
	})

	result, err := f.FetchIssues()
	if err != nil {
		t.Fatalf("FetchIssues() error = %v", err)
	}

	if want := []string{townStoreName}; !reflect.DeepEqual(result.TruncatedStores, want) {
		t.Errorf("TruncatedStores = %v, want %v — a store that filled its allowance is truncated even when every bead it returned is hidden", result.TruncatedStores, want)
	}
	if len(result.Rows) != 7 {
		t.Errorf("rows = %d, want 7 (internal beads hidden, rig rows kept)", len(result.Rows))
	}
	if !strings.Contains(result.Warning(), townStoreName) {
		t.Errorf("Warning() = %q, want it to name %q", result.Warning(), townStoreName)
	}
}

// TestFetchIssues_PerStoreLimitDoesNotStarveRigs is why the panel uses a
// per-store limit. Under a budget shared across stores, the town root spends it
// all and the rigs land in UnreadStores — the panel stays as blind as it was.
func TestFetchIssues_PerStoreLimitDoesNotStarveRigs(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {"open": openBeads("town", issuesPerStoreLimit)},
		"beads":       {"open": openBeads("beads", 3)},
		"gastown":     {"open": openBeads("gastown", 4)},
	})

	result, err := f.FetchIssues()
	if err != nil {
		t.Fatalf("FetchIssues() error = %v", err)
	}

	if len(result.UnreadStores) != 0 {
		t.Errorf("UnreadStores = %v, want none — a full town root must not cost the rigs their rows", result.UnreadStores)
	}
	byStore := map[string]int{}
	for _, row := range result.Rows {
		byStore[strings.SplitN(row.ID, "-", 2)[0]]++
	}
	if byStore["beads"] != 3 || byStore["gastown"] != 4 {
		t.Errorf("rig rows = beads:%d gastown:%d, want 3 and 4", byStore["beads"], byStore["gastown"])
	}
}

// internalBeads builds n Gas Town plumbing beads the Work panel must hide.
func internalBeads(prefix, label string, n int) []map[string]any {
	beads := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		beads = append(beads, map[string]any{
			"id":     fmt.Sprintf("%s-%d", prefix, i),
			"title":  "plumbing",
			"labels": []string{label},
		})
	}
	return beads
}

// TestFetchIssues_CountFallsWhenWorkIsClosed is gt-eolg stated as a behaviour.
//
// The panel used to fetch 50 beads per store and show the size of that sample
// as the size of the backlog. Measured, the gastown rig held 59 non-internal
// open beads against that cap, so it contributed exactly 50 whether it held 51
// or 500: closing a bead only slid the next one into the sampled window and the
// number could not move until the store fell below 50. Two fixtures one bead
// apart must therefore differ by exactly one.
func TestFetchIssues_CountFallsWhenWorkIsClosed(t *testing.T) {
	// Comfortably above the 50-per-store cap this replaced, so the store is in
	// the region where the old code was pinned.
	const backlog = 120

	count := func(t *testing.T, open int) int {
		t.Helper()
		f := panelFetcher(t, map[string]fakeBdStore{
			townStoreName: {"open": openBeads("town", 2)},
			"beads":       {"open": openBeads("beads", 3)},
			"gastown":     {"open": openBeads("gastown", open)},
		})
		result, err := f.FetchIssues()
		if err != nil {
			t.Fatalf("FetchIssues() error = %v", err)
		}
		if result.Partial() {
			t.Fatalf("Partial() = true for a backlog well under the safety cap, warning = %q", result.Warning())
		}
		return len(result.Rows)
	}

	full := count(t, backlog)
	if want := 2 + 3 + backlog; full != want {
		t.Errorf("rows = %d, want %d — a store above the old cap must contribute all of its beads, not a sample", full, want)
	}

	afterOneClosed := count(t, backlog-1)
	if afterOneClosed != full-1 {
		t.Errorf("closing one bead moved the count %d -> %d, want a drop of exactly 1", full, afterOneClosed)
	}
}

// TestFetchIssues_MailDoesNotDisplaceWorkFromTheCount pins the inversion, which
// is the worse half of gt-eolg. 28 of the 50 rows the town root contributed
// were gt:message: fetched, counted against the cap, and only then hidden. The
// store displayed 22 of its 186 real work items, and each new message pushed
// one more work bead out of the sample — so the displayed number FELL while the
// backlog ROSE.
//
// The mail is ordered first so a truncating fetch spends its allowance on rows
// the panel will hide, exactly as the live town root did.
func TestFetchIssues_MailDoesNotDisplaceWorkFromTheCount(t *testing.T) {
	const work = 60

	townOpen := append(internalBeads("town-msg", "gt:message", 400), openBeads("town", work)...)

	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {"open": townOpen},
		"beads":       {"open": openBeads("beads", 3)},
		"gastown":     {"open": openBeads("gastown", 4)},
	})

	result, err := f.FetchIssues()
	if err != nil {
		t.Fatalf("FetchIssues() error = %v", err)
	}

	byStore := map[string]int{}
	for _, row := range result.Rows {
		byStore[strings.SplitN(row.ID, "-", 2)[0]]++
	}
	if byStore["town"] != work {
		t.Errorf("town rows = %d, want %d — mail must not consume the town root's allowance", byStore["town"], work)
	}
	if byStore["beads"] != 3 || byStore["gastown"] != 4 {
		t.Errorf("rig rows = beads:%d gastown:%d, want 3 and 4", byStore["beads"], byStore["gastown"])
	}
}

// TestFetchIssues_InternalBeadsAreRecognisedByBdsTypeKey covers the half of
// isInternal that could never fire. bd emits the type as "issue_type" and has
// no "type" key at all, so a decoder tagged "type" read "" on every bead ever
// fetched: a merge-request or wisp bead carrying no gt: label counted as work.
// These fixtures carry no labels, so only the type key can hide them.
func TestFetchIssues_InternalBeadsAreRecognisedByBdsTypeKey(t *testing.T) {
	plumbing := []map[string]any{
		{"id": "town-mr-1", "title": "merge request", "issue_type": "merge-request"},
		{"id": "town-wisp-1", "title": "heartbeat", "issue_type": "wisp"},
		{"id": "town-agent-1", "title": "identity", "issue_type": "agent"},
	}

	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {"open": append(plumbing, openBeads("town", 2)...)},
		"beads":       {"open": openBeads("beads", 3)},
		"gastown":     {"open": openBeads("gastown", 4)},
	})

	result, err := f.FetchIssues()
	if err != nil {
		t.Fatalf("FetchIssues() error = %v", err)
	}

	if want := 2 + 3 + 4; len(result.Rows) != want {
		t.Errorf("rows = %d, want %d — plumbing beads are not work even when they carry no gt: label", len(result.Rows), want)
	}
	for _, row := range result.Rows {
		for _, hidden := range []string{"town-mr-1", "town-wisp-1", "town-agent-1"} {
			if row.ID == hidden {
				t.Errorf("row %s is Gas Town plumbing and must not be counted as work", hidden)
			}
		}
	}
}

// TestFetchHooks_UnionsEveryStore reproduces the measured defect directly: the
// town root holds no hooked beads and the rigs hold five, so a town-root-only
// query renders "nothing is hooked" over live work.
func TestFetchHooks_UnionsEveryStore(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {"hooked": hookedBeads("town", 0)},
		"beads":       {"hooked": hookedBeads("beads", 1)},
		"gastown":     {"hooked": hookedBeads("gastown", 5)},
	})

	result, err := f.FetchHooks()
	if err != nil {
		t.Fatalf("FetchHooks() error = %v", err)
	}

	if len(result.Rows) != 6 {
		t.Fatalf("rows = %d, want 6 (1 in beads + 5 in gastown); an empty town root must not decide the panel", len(result.Rows))
	}

	got := idsOf(result.Rows, func(r HookRow) string { return r.ID })
	want := []string{"beads-hook-0", "gastown-hook-0", "gastown-hook-1", "gastown-hook-2", "gastown-hook-3", "gastown-hook-4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hook ids = %v, want %v", got, want)
	}
	if result.Partial() {
		t.Errorf("Partial() = true for a complete union, warning = %q", result.Warning())
	}
}

// TestFetchHooks_NamesStoreThatCouldNotAnswer covers the swallowed error: the
// old query returned (nil, nil) on any bd failure, so an unreachable store and
// an empty one rendered identically.
func TestFetchHooks_NamesStoreThatCouldNotAnswer(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {"hooked": hookedBeads("town", 1)},
		"beads":       {"hooked": hookedBeads("beads", 2)},
		"gastown":     {"hooked": hookedBeads("gastown", 3)},
	}, "broken")

	result, err := f.FetchHooks()
	if err != nil {
		t.Fatalf("FetchHooks() error = %v", err)
	}

	if want := []string{"broken"}; !reflect.DeepEqual(result.FailedStores, want) {
		t.Errorf("FailedStores = %v, want %v", result.FailedStores, want)
	}
	if !result.Partial() {
		t.Error("Partial() = false with an unreadable store, want true")
	}
	if len(result.Rows) != 6 {
		t.Errorf("rows = %d, want 6 (the readable stores still answer)", len(result.Rows))
	}
}

// TestFetchHooks_EmptyEverywhereIsNotPartial is the control for the tests
// above: zero rows must stay distinguishable from zero readable stores.
func TestFetchHooks_EmptyEverywhereIsNotPartial(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {"hooked": hookedBeads("town", 0)},
		"beads":       {"hooked": hookedBeads("beads", 0)},
		"gastown":     {"hooked": hookedBeads("gastown", 0)},
	})

	result, err := f.FetchHooks()
	if err != nil {
		t.Fatalf("FetchHooks() error = %v", err)
	}
	if len(result.Rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(result.Rows))
	}
	if result.Partial() {
		t.Errorf("Partial() = true for an empty but fully readable town, warning = %q", result.Warning())
	}
	if got := result.Warning(); got != "" {
		t.Errorf("Warning() = %q, want empty", got)
	}
}

// The Polecats panel was filed as safe because FetchWorkers loads the rigs
// config — but it loads it to filter tmux session names, and its bead lookup
// asked the town root for status=in_progress. Measured against a live town, not
// one assigned bead in any store was at in_progress while eleven sat at hooked,
// all of them in rig stores. So the lookup returned nothing, every worker got an
// empty IssueID, and calculateWorkerWorkStatus reports empty as "idle": ten
// working polecats rendered idle. These tests pin both halves — the store and
// the status — because fixing either alone still returns an empty map.

// assignedBeads builds n beads assigned to distinct workers of a rig.
func assignedBeads(rig string, n int) []map[string]any {
	beads := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		beads = append(beads, map[string]any{
			"id":       fmt.Sprintf("%s-work-%d", rig, i),
			"title":    fmt.Sprintf("assigned item %d in %s", i, rig),
			"assignee": fmt.Sprintf("%s/polecats/worker%d", rig, i),
		})
	}
	return beads
}

// TestGetAssignedIssuesMap_UnionsEveryStore pins the store half. The town root
// is deliberately empty of assigned work, as the live town's was: a fixture that
// puts rows in the town root passes against the town-root-only code it replaced.
func TestGetAssignedIssuesMap_UnionsEveryStore(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {},
		"beads":       {"hooked": assignedBeads("beads", 2)},
		"gastown":     {"hooked": assignedBeads("gastown", 3)},
	})

	result := f.getAssignedIssuesMap()
	if result.Partial() {
		t.Fatalf("Partial() = true for a complete union, warning = %q", result.Warning())
	}

	got := assignedIssuesByAssignee(result.Rows)
	if len(got) != 5 {
		t.Errorf("assignees = %d, want 5 (2 in beads + 3 in gastown)", len(got))
	}
	// Naming an assignee from each rig is what catches a union that returns the
	// right total from the wrong stores.
	for _, assignee := range []string{"beads/polecats/worker1", "gastown/polecats/worker2"} {
		if _, ok := got[assignee]; !ok {
			t.Errorf("assignee %q missing from the union: %v", assignee, got)
		}
	}
}

// TestGetAssignedIssuesMap_FindsHookedWork pins the status half. The old query
// asked only for in_progress, which no bead in the measured town held.
func TestGetAssignedIssuesMap_FindsHookedWork(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {},
		"gastown":     {"hooked": assignedBeads("gastown", 2)},
	})

	got := assignedIssuesByAssignee(f.getAssignedIssuesMap().Rows)
	if len(got) != 2 {
		t.Fatalf("assignees = %d, want 2 — hooked work must be found, not just in_progress", len(got))
	}
	if issue := got["gastown/polecats/worker0"]; issue.ID != "gastown-work-0" {
		t.Errorf("issue for worker0 = %+v, want ID gastown-work-0", issue)
	}
}

// TestGetAssignedIssuesMap_FindsInProgressWork is the other side of that pair:
// the older assignment path still writes in_progress, so widening the query must
// not have narrowed it.
func TestGetAssignedIssuesMap_FindsInProgressWork(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {},
		"gastown":     {"in_progress": assignedBeads("gastown", 2)},
	})

	if got := assignedIssuesByAssignee(f.getAssignedIssuesMap().Rows); len(got) != 2 {
		t.Fatalf("assignees = %d, want 2 — in_progress work must still be found", len(got))
	}
}

// TestGetAssignedIssuesMap_NamesStoreThatCouldNotAnswer covers the swallowed
// error. This is the failure that matters most here: an unreadable rig store
// drops its workers' issues, and a worker with no issue is rendered idle. The
// panel must say it could not look rather than call them idle.
func TestGetAssignedIssuesMap_NamesStoreThatCouldNotAnswer(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {},
		"gastown":     {"hooked": assignedBeads("gastown", 2)},
	}, "broken")

	result := f.getAssignedIssuesMap()
	if want := []string{"broken"}; !reflect.DeepEqual(result.FailedStores, want) {
		t.Errorf("FailedStores = %v, want %v", result.FailedStores, want)
	}
	// Named once, though the store is asked for two statuses.
	if !result.Partial() {
		t.Error("Partial() = false with an unreadable store, want true")
	}
	if len(result.Rows) != 2 {
		t.Errorf("rows = %d, want 2 (the readable store still answers)", len(result.Rows))
	}
}

// TestGetAssignedIssuesMap_EmptyEverywhereIsNotPartial is the control: a town
// where nobody is assigned anything must not raise the caveat, or the caveat
// stops meaning anything.
func TestGetAssignedIssuesMap_EmptyEverywhereIsNotPartial(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {},
		"beads":       {},
		"gastown":     {},
	})

	result := f.getAssignedIssuesMap()
	if len(result.Rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(result.Rows))
	}
	if result.Partial() {
		t.Errorf("Partial() = true for an empty but fully readable town, warning = %q", result.Warning())
	}
}

// TestFetchWorkers_CarriesAssignmentCaveat checks the caveat survives the trip
// out of FetchWorkers, which is where the panel reads it. Without this the
// resolver can name a failed store into a value nobody carries.
func TestFetchWorkers_CarriesAssignmentCaveat(t *testing.T) {
	f := panelFetcher(t, map[string]fakeBdStore{
		townStoreName: {},
		"gastown":     {"hooked": assignedBeads("gastown", 1)},
	}, "broken")

	// tmux answering with no sessions keeps this test on the bead lookup: the
	// worker rows come from tmux, but the caveat comes from the stores.
	original := fetcherRunCmd
	t.Cleanup(func() { fetcherRunCmd = original })
	fetcherRunCmd = func(_ time.Duration, _ string, _ ...string) (*bytes.Buffer, error) {
		return bytes.NewBufferString(""), nil
	}

	result, err := f.FetchWorkers()
	if err != nil {
		t.Fatalf("FetchWorkers() error = %v", err)
	}
	if want := []string{"broken"}; !reflect.DeepEqual(result.FailedStores, want) {
		t.Errorf("FailedStores = %v, want %v", result.FailedStores, want)
	}
	if got := result.Warning(); got == "" {
		t.Error("Warning() = \"\" with an unreadable store — the panel would show workers idle with no caveat")
	}
}
