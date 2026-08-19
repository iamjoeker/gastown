package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// Fixture: a town whose routes.jsonl names a town store, duly_noted, and gastown.
// Mirrors the shape that produced gt-ad32 — dn- beads filed in duly_noted and
// moved to the rig that owns the work.
func newOwnerTown(t *testing.T) string {
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

// stubBeadRows installs both lookup seams from a map of rig name → row.
// A rig absent from the map has no row for the bead. Returns a pointer to a
// counter of scans across non-prefix stores.
func stubBeadRows(t *testing.T, townRoot string, rows map[string]*beadInfo) *int {
	t.Helper()
	beads.ClearBeadStores()
	t.Cleanup(beads.ClearBeadStores)

	origStore, origOwner := readBeadInfoFromStore, readBeadInfoForOwner
	t.Cleanup(func() {
		readBeadInfoFromStore, readBeadInfoForOwner = origStore, origOwner
	})

	scans := 0
	readBeadInfoFromStore = func(store beads.BeadStore, beadID string) (*beadInfo, error) {
		scans++
		info, ok := rows[store.Rig]
		if !ok {
			return nil, fmt.Errorf("bead '%s' not found", beadID)
		}
		return info, nil
	}
	// Prefix routing: whatever the bead's prefix resolves to.
	readBeadInfoForOwner = func(root, beadID string) (*beadInfo, error) {
		info, ok := rows[beads.GetRigNameForPrefix(root, beads.ExtractPrefix(beadID))]
		if !ok {
			return nil, fmt.Errorf("bead '%s' not found", beadID)
		}
		return info, nil
	}
	return &scans
}

func TestResolveBeadOwnerLiveRowInPrefixStore(t *testing.T) {
	townRoot := newOwnerTown(t)
	scans := stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Title: "still filed here", Status: "open"},
	})

	owner, err := resolveBeadOwner(townRoot, "dn-cqu")
	if err != nil {
		t.Fatalf("resolveBeadOwner: %v", err)
	}
	if owner.Moved {
		t.Error("a live row in the prefix store is not a moved bead")
	}
	if owner.Rig != "duly_noted" || owner.PrefixRig != "duly_noted" {
		t.Errorf("Rig=%q PrefixRig=%q, want both %q", owner.Rig, owner.PrefixRig, "duly_noted")
	}
	if *scans != 0 {
		t.Errorf("scanned %d other stores; the happy path must cost nothing extra", *scans)
	}
	if _, ok := beads.LookupBeadStore("dn-cqu"); ok {
		t.Error("no override should be registered when the prefix store is right")
	}
}

// The gt-ad32 case: closed in the rig the prefix names, open in the rig that
// owns the work.
func TestResolveBeadOwnerFindsMovedBead(t *testing.T) {
	townRoot := newOwnerTown(t)
	stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Title: "source copy", Status: "closed"},
		"gastown":    {Title: "the real work", Status: "open"},
	})

	owner, err := resolveBeadOwner(townRoot, "dn-cqu")
	if err != nil {
		t.Fatalf("resolveBeadOwner: %v", err)
	}
	if !owner.Moved {
		t.Fatal("expected the bead to be reported as moved")
	}
	if owner.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", owner.Rig, "gastown")
	}
	if owner.PrefixRig != "duly_noted" {
		t.Errorf("PrefixRig = %q, want %q", owner.PrefixRig, "duly_noted")
	}
	if owner.Info == nil || owner.Info.Status != "open" || owner.Info.Title != "the real work" {
		t.Errorf("Info = %+v, want the open gastown row", owner.Info)
	}

	// The answer must be registered so later reads and writes reach the live row.
	store, ok := beads.LookupBeadStore("dn-cqu")
	if !ok {
		t.Fatal("expected a store override to be registered for the moved bead")
	}
	wantBeadsDir := filepath.Join(townRoot, "gastown/mayor/rig", ".beads")
	if store.BeadsDir != wantBeadsDir {
		t.Errorf("override BeadsDir = %q, want %q", store.BeadsDir, wantBeadsDir)
	}
	if got := beads.ResolveHookDir(townRoot, "dn-cqu", ""); got != filepath.Join(townRoot, "gastown/mayor/rig") {
		t.Errorf("hook dir after resolution = %q, want the gastown rig dir", got)
	}
}

func TestResolveBeadOwnerClosedEverywhere(t *testing.T) {
	townRoot := newOwnerTown(t)
	stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Title: "genuinely done", Status: "closed"},
	})

	owner, err := resolveBeadOwner(townRoot, "dn-cqu")
	if err != nil {
		t.Fatalf("resolveBeadOwner: %v", err)
	}
	if owner.Moved {
		t.Error("no live row anywhere is not a moved bead")
	}
	if owner.Info.Status != "closed" {
		t.Errorf("Status = %q, want closed", owner.Info.Status)
	}
	if _, ok := beads.LookupBeadStore("dn-cqu"); ok {
		t.Error("a genuinely closed bead must not register an override")
	}

	err = closedBeadError("dn-cqu", owner)
	if !strings.Contains(err.Error(), `rig "duly_noted"`) {
		t.Errorf("closed-bead error must name the store it read, got: %v", err)
	}
	if !strings.Contains(err.Error(), "work already completed") {
		t.Errorf("closed-bead error lost its existing wording, got: %v", err)
	}
}

// A tombstone in the prefix store must not hide a live row elsewhere.
func TestResolveBeadOwnerTombstoneSourceRow(t *testing.T) {
	townRoot := newOwnerTown(t)
	stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Status: "tombstone"},
		"gastown":    {Status: "open"},
	})

	owner, err := resolveBeadOwner(townRoot, "dn-cqu")
	if err != nil {
		t.Fatalf("resolveBeadOwner: %v", err)
	}
	if !owner.Moved || owner.Rig != "gastown" {
		t.Errorf("owner = %+v, want the open gastown row", owner)
	}
}

// A source row deleted outright, not closed, leaves nothing in the prefix store.
func TestResolveBeadOwnerMissingSourceRow(t *testing.T) {
	townRoot := newOwnerTown(t)
	stubBeadRows(t, townRoot, map[string]*beadInfo{
		"gastown": {Status: "open"},
	})

	owner, err := resolveBeadOwner(townRoot, "dn-cqu")
	if err != nil {
		t.Fatalf("resolveBeadOwner: %v", err)
	}
	if !owner.Moved || owner.Rig != "gastown" {
		t.Errorf("owner = %+v, want the open gastown row", owner)
	}
}

func TestResolveBeadOwnerNotFoundAnywhere(t *testing.T) {
	townRoot := newOwnerTown(t)
	stubBeadRows(t, townRoot, map[string]*beadInfo{})

	if _, err := resolveBeadOwner(townRoot, "dn-cqu"); err == nil {
		t.Fatal("expected an error when the bead exists in no store")
	}
}

// Both halves of the gt-ad32 deadlock, through the guard that produced them.
func TestCrossRigGuardFollowsTheLiveRow(t *testing.T) {
	townRoot := newOwnerTown(t)
	stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Status: "closed"},
		"gastown":    {Status: "open"},
	})

	// Before resolution the guard can only go on the prefix.
	if err := checkCrossRigGuard("dn-cqu", "gastown/polecats/_", townRoot); err == nil {
		t.Fatal("expected the unresolved guard to reject on prefix")
	}

	if _, err := resolveBeadOwner(townRoot, "dn-cqu"); err != nil {
		t.Fatalf("resolveBeadOwner: %v", err)
	}

	// The rig that holds the open row can now take the work.
	if err := checkCrossRigGuard("dn-cqu", "gastown/polecats/_", townRoot); err != nil {
		t.Errorf("guard rejected the rig holding the live row: %v", err)
	}

	// The rig its prefix names — which closed it — still cannot.
	err := checkCrossRigGuard("dn-cqu", "duly_noted/polecats/_", townRoot)
	if err == nil {
		t.Fatal("expected the rig that closed the bead to be rejected")
	}
	if !strings.Contains(err.Error(), `belongs to rig "gastown"`) {
		t.Errorf("mismatch error must name the owning rig, got: %v", err)
	}
	if !strings.Contains(err.Error(), "live row") {
		t.Errorf("mismatch error must say ownership came from the live row, got: %v", err)
	}
}

func TestCrossRigGuardUnmovedBeadsUnchanged(t *testing.T) {
	townRoot := newOwnerTown(t)
	beads.ClearBeadStores()
	t.Cleanup(beads.ClearBeadStores)

	if err := checkCrossRigGuard("gt-abc", "gastown/polecats/_", townRoot); err != nil {
		t.Errorf("same-rig sling must pass: %v", err)
	}
	err := checkCrossRigGuard("dn-abc", "gastown/polecats/_", townRoot)
	if err == nil || !strings.Contains(err.Error(), "cross-rig mismatch") {
		t.Errorf("cross-rig sling must still be rejected, got: %v", err)
	}
	if !strings.Contains(err.Error(), `prefix "dn"`) {
		t.Errorf("unmoved mismatch should attribute ownership to the prefix, got: %v", err)
	}
	err = checkCrossRigGuard("xx-abc", "gastown/polecats/_", townRoot)
	if err == nil || !strings.Contains(err.Error(), "not in routes") {
		t.Errorf("unknown prefix must still be hard-rejected, got: %v", err)
	}
}

func TestClosedBeadErrorNamesTownStore(t *testing.T) {
	owner := &beadOwner{Rig: "", Info: &beadInfo{Status: "closed"}}
	err := closedBeadError("hq-abc", owner)
	if !strings.Contains(err.Error(), "town-level beads store") {
		t.Errorf("expected the town-level store to be named, got: %v", err)
	}
}

func TestBeadStatusIsLive(t *testing.T) {
	for _, s := range []string{"open", "in_progress", "hooked", "pinned", "blocked", "deferred"} {
		if !beadStatusIsLive(s) {
			t.Errorf("status %q should count as live", s)
		}
	}
	for _, s := range []string{"closed", "tombstone"} {
		if beadStatusIsLive(s) {
			t.Errorf("status %q should not count as live", s)
		}
	}
}
