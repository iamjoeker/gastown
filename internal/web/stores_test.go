package web

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// storeRow stands in for the panel row types, which all need to record which
// store produced them.
type storeRow struct {
	ID  string
	Rig string
}

// threeRigsConfig registers two readable rigs and one that the fake bd refuses
// to answer for. A single-rig fixture would pass against town-root-only code,
// so every union test here uses at least two non-empty rig stores.
const threeRigsConfig = `{
  "version": 1,
  "rigs": {
    "beads": {"git_url": "git@github.com:upstreamorg/beads.git"},
    "broken": {"git_url": "git@github.com:upstreamorg/broken.git"},
    "gastown": {"git_url": "git@github.com:upstreamorg/gastown.git"}
  }
}`

// rowsFrom builds n rows attributed to a store.
func rowsFrom(name string, n int) []storeRow {
	rows := make([]storeRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, storeRow{ID: fmt.Sprintf("%s-%d", name, i), Rig: name})
	}
	return rows
}

// countByRig tallies rows by the store they were stamped with.
func countByRig(rows []storeRow) map[string]int {
	counts := make(map[string]int)
	for _, r := range rows {
		counts[r.Rig]++
	}
	return counts
}

// fixedRows answers each store from a fixture, honoring the row limit the
// resolver hands it the way `bd list --limit` would, and erroring for any store
// named in failStores.
func fixedRows(t *testing.T, byStore map[string]int, failStores map[string]bool) (func(storeSource, int) ([]storeRow, error), *[]storeSource, *[]int) {
	t.Helper()

	var sawSources []storeSource
	var sawLimits []int

	fn := func(src storeSource, limit int) ([]storeRow, error) {
		sawSources = append(sawSources, src)
		sawLimits = append(sawLimits, limit)

		if failStores[src.Name] {
			return nil, fmt.Errorf("dolt unavailable for %s", src.Name)
		}

		rows := rowsFrom(src.Name, byStore[src.Name])
		if limit > 0 && len(rows) > limit {
			rows = rows[:limit]
		}
		return rows, nil
	}

	return fn, &sawSources, &sawLimits
}

func TestForEachStore_UnionsTownRootAndEveryRig(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, threeRigsConfig)

	perStore := map[string]int{townStoreName: 2, "beads": 3, "broken": 5, "gastown": 4}
	fn, sawSources, _ := fixedRows(t, perStore, nil)

	f := &LiveConvoyFetcher{townRoot: townRoot}
	result := forEachStore(f, storeBudgetUnlimited, fn)

	// The union count is the sum of the per-store counts — the property that
	// fails the moment a store is dropped or double-counted.
	wantTotal := 0
	for _, n := range perStore {
		wantTotal += n
	}
	if len(result.Rows) != wantTotal {
		t.Fatalf("union rows = %d, want %d (sum of per-store counts)", len(result.Rows), wantTotal)
	}
	if got := countByRig(result.Rows); !reflect.DeepEqual(got, perStore) {
		t.Errorf("rows by origin = %v, want %v", got, perStore)
	}

	// Town root first, then rigs in sorted order.
	wantSources := []storeSource{
		{Name: townStoreName, Dir: townRoot, IsTown: true},
		{Name: "beads", Dir: filepath.Join(townRoot, "beads")},
		{Name: "broken", Dir: filepath.Join(townRoot, "broken")},
		{Name: "gastown", Dir: filepath.Join(townRoot, "gastown")},
	}
	if !reflect.DeepEqual(*sawSources, wantSources) {
		t.Errorf("queried sources = %+v, want %+v", *sawSources, wantSources)
	}

	if result.Partial() {
		t.Errorf("Partial() = true for a complete union, warning = %q", result.Warning())
	}
	if got := result.Warning(); got != "" {
		t.Errorf("Warning() = %q, want empty for a complete union", got)
	}
}

func TestForEachStore_NamesFailedStoreAndKeepsTheRest(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, threeRigsConfig)

	perStore := map[string]int{townStoreName: 2, "beads": 3, "broken": 5, "gastown": 4}
	fn, _, _ := fixedRows(t, perStore, map[string]bool{"broken": true})

	f := &LiveConvoyFetcher{townRoot: townRoot}
	result := forEachStore(f, storeBudgetUnlimited, fn)

	if want := []string{"broken"}; !reflect.DeepEqual(result.FailedStores, want) {
		t.Fatalf("FailedStores = %v, want %v", result.FailedStores, want)
	}

	// The other three stores still returned in full — one bad store does not
	// abort the union.
	wantRows := map[string]int{townStoreName: 2, "beads": 3, "gastown": 4}
	if got := countByRig(result.Rows); !reflect.DeepEqual(got, wantRows) {
		t.Errorf("rows by origin = %v, want %v", got, wantRows)
	}

	if !result.Partial() {
		t.Error("Partial() = false with a failed store, want true")
	}
	if got := result.Warning(); !strings.Contains(got, "broken") {
		t.Errorf("Warning() = %q, want it to name the unreadable store", got)
	}
}

// TestForEachStore_DistinguishesEmptyFromUnreadable is the whole point of
// StoreResult: both cases return zero rows, and they must not look alike.
func TestForEachStore_DistinguishesEmptyFromUnreadable(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, threeRigsConfig)
	f := &LiveConvoyFetcher{townRoot: townRoot}

	t.Run("every store empty", func(t *testing.T) {
		fn, _, _ := fixedRows(t, nil, nil)
		result := forEachStore(f, storeBudgetUnlimited, fn)

		if len(result.Rows) != 0 {
			t.Fatalf("rows = %d, want 0", len(result.Rows))
		}
		if result.Partial() {
			t.Errorf("Partial() = true for genuinely empty stores, warning = %q", result.Warning())
		}
		if len(result.FailedStores) != 0 {
			t.Errorf("FailedStores = %v, want none", result.FailedStores)
		}
	})

	t.Run("every store unreadable", func(t *testing.T) {
		fn, _, _ := fixedRows(t, map[string]int{townStoreName: 2, "beads": 3, "gastown": 4}, map[string]bool{
			townStoreName: true, "beads": true, "broken": true, "gastown": true,
		})
		result := forEachStore(f, storeBudgetUnlimited, fn)

		if len(result.Rows) != 0 {
			t.Fatalf("rows = %d, want 0", len(result.Rows))
		}
		if !result.Partial() {
			t.Fatal("Partial() = false when nothing could be read, want true")
		}
		want := []string{townStoreName, "beads", "broken", "gastown"}
		if !reflect.DeepEqual(result.FailedStores, want) {
			t.Errorf("FailedStores = %v, want %v", result.FailedStores, want)
		}
	})
}

func TestForEachStore_BudgetCapsUnionAndNamesWhatItCut(t *testing.T) {
	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, threeRigsConfig)
	f := &LiveConvoyFetcher{townRoot: townRoot}

	// Four stores holding four rows each: the shape that silently returns 16
	// under a "limit 10" the moment the limit is applied per store.
	perStore := map[string]int{townStoreName: 4, "beads": 4, "broken": 4, "gastown": 4}

	t.Run("unlimited returns everything", func(t *testing.T) {
		fn, _, limits := fixedRows(t, perStore, nil)
		result := forEachStore(f, storeBudgetUnlimited, fn)

		if len(result.Rows) != 16 {
			t.Fatalf("rows = %d, want 16", len(result.Rows))
		}
		if result.Partial() {
			t.Errorf("Partial() = true without a budget, warning = %q", result.Warning())
		}
		for i, l := range *limits {
			if l != storeBudgetUnlimited {
				t.Errorf("limit[%d] = %d, want %d (unlimited)", i, l, storeBudgetUnlimited)
			}
		}
	})

	t.Run("budget is a total, not a per-store limit", func(t *testing.T) {
		fn, _, limits := fixedRows(t, perStore, nil)
		result := forEachStore(f, 10, fn)

		if len(result.Rows) != 10 {
			t.Fatalf("rows = %d, want 10 (the total budget, not 4 stores x 10)", len(result.Rows))
		}

		// Each store is offered only what the budget has left.
		if want := []int{10, 6, 2}; !reflect.DeepEqual(*limits, want) {
			t.Errorf("limits offered = %v, want %v", *limits, want)
		}

		// "broken" filled its whole remaining allowance; "gastown" never ran.
		if want := []string{"broken"}; !reflect.DeepEqual(result.TruncatedStores, want) {
			t.Errorf("TruncatedStores = %v, want %v", result.TruncatedStores, want)
		}
		if want := []string{"gastown"}; !reflect.DeepEqual(result.UnreadStores, want) {
			t.Errorf("UnreadStores = %v, want %v", result.UnreadStores, want)
		}

		// A truncated union must never present as complete.
		if !result.Partial() {
			t.Error("Partial() = false for a truncated union, want true")
		}
		warning := result.Warning()
		for _, name := range []string{"broken", "gastown"} {
			if !strings.Contains(warning, name) {
				t.Errorf("Warning() = %q, want it to name %q", warning, name)
			}
		}
	})
}

// TestForEachStore_MissingRigsConfigStillReadsTown covers the source that is not
// a store: without mayor/rigs.json no rig can be enumerated, and dropping the
// whole union would hide that behind an empty panel.
func TestForEachStore_MissingRigsConfigStillReadsTown(t *testing.T) {
	townRoot := t.TempDir() // no mayor/rigs.json

	fn, sawSources, _ := fixedRows(t, map[string]int{townStoreName: 2}, nil)
	f := &LiveConvoyFetcher{townRoot: townRoot}
	result := forEachStore(f, storeBudgetUnlimited, fn)

	if len(*sawSources) != 1 || (*sawSources)[0].Name != townStoreName {
		t.Fatalf("queried sources = %+v, want the town root only", *sawSources)
	}
	if len(result.Rows) != 2 {
		t.Errorf("rows = %d, want 2 (the town root still answers)", len(result.Rows))
	}
	if want := []string{rigsConfigStoreName}; !reflect.DeepEqual(result.FailedStores, want) {
		t.Errorf("FailedStores = %v, want %v", result.FailedStores, want)
	}
	if !result.Partial() {
		t.Error("Partial() = false with an unreadable rigs config, want true")
	}
}

// TestForEachStore_QueriesEachStoreDirectory drives the resolver through the
// real command path with a fake bd, proving storeSource.Dir is the directory the
// query actually runs in — a stubbed callback cannot catch a wrong path.
func TestForEachStore_QueriesEachStoreDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, threeRigsConfig)
	for _, rig := range []string{"beads", "broken", "gastown"} {
		if err := os.MkdirAll(filepath.Join(townRoot, rig), 0o755); err != nil {
			t.Fatalf("creating rig dir %s: %v", rig, err)
		}
	}

	// Answers with one row named after the directory it was run in. The
	// "broken" rig exits non-zero with no output, which is how runBdCmd
	// recognizes a real failure rather than a warning.
	bdPath := filepath.Join(t.TempDir(), "bd")
	script := `#!/bin/sh
if [ "$(basename "$PWD")" = "broken" ]; then
  exit 1
fi
printf '[{"id":"%s"}]' "$(basename "$PWD")"
`
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	f := &LiveConvoyFetcher{townRoot: townRoot, cmdTimeout: 30 * time.Second, bdBin: bdPath}
	result := forEachStore(f, storeBudgetUnlimited, func(src storeSource, _ int) ([]storeRow, error) {
		stdout, err := f.runBdCmd(src.Dir, "list", "--status=open", "--json")
		if err != nil {
			return nil, err
		}
		var listed []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil {
			return nil, fmt.Errorf("parsing store %s: %w", src.Name, err)
		}
		rows := make([]storeRow, 0, len(listed))
		for _, l := range listed {
			rows = append(rows, storeRow{ID: l.ID, Rig: src.Name})
		}
		return rows, nil
	})

	// Two non-empty rig stores plus the town root answered; the third is named.
	wantIDs := []string{filepath.Base(townRoot), "beads", "gastown"}
	gotIDs := make([]string, 0, len(result.Rows))
	for _, r := range result.Rows {
		gotIDs = append(gotIDs, r.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("row ids = %v, want %v", gotIDs, wantIDs)
	}

	wantRigs := []string{townStoreName, "beads", "gastown"}
	gotRigs := make([]string, 0, len(result.Rows))
	for _, r := range result.Rows {
		gotRigs = append(gotRigs, r.Rig)
	}
	if !reflect.DeepEqual(gotRigs, wantRigs) {
		t.Errorf("row origins = %v, want %v", gotRigs, wantRigs)
	}

	if want := []string{"broken"}; !reflect.DeepEqual(result.FailedStores, want) {
		t.Errorf("FailedStores = %v, want %v", result.FailedStores, want)
	}
}
