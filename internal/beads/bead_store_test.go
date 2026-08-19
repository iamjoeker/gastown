package beads

import (
	"os"
	"path/filepath"
	"testing"
)

// newRouteFixture builds a town root with routes.jsonl and the rig .beads
// directories those routes name.
func newRouteFixture(t *testing.T, routes string, rigPaths ...string) string {
	t.Helper()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, RoutesFileName), []byte(routes), 0644); err != nil {
		t.Fatal(err)
	}
	for _, p := range rigPaths {
		if err := os.MkdirAll(filepath.Join(townRoot, p, ".beads"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return townRoot
}

func TestRouteStores(t *testing.T) {
	routes := `{"prefix": "hq-", "path": "."}
{"prefix": "hq-cv-", "path": "."}
{"prefix": "dn-", "path": "duly_noted"}
{"prefix": "gt-", "path": "gastown/mayor/rig"}
`
	townRoot := newRouteFixture(t, routes, "duly_noted", "gastown/mayor/rig")

	stores := RouteStores(townRoot)
	if len(stores) != 3 {
		t.Fatalf("expected 3 distinct stores (hq- and hq-cv- share one), got %d: %+v", len(stores), stores)
	}

	want := []BeadStore{
		{Rig: "", WorkDir: townRoot, BeadsDir: filepath.Join(townRoot, ".beads")},
		{Rig: "duly_noted", WorkDir: filepath.Join(townRoot, "duly_noted"), BeadsDir: filepath.Join(townRoot, "duly_noted", ".beads")},
		{Rig: "gastown", WorkDir: filepath.Join(townRoot, "gastown/mayor/rig"), BeadsDir: filepath.Join(townRoot, "gastown/mayor/rig", ".beads")},
	}
	for i, w := range want {
		if stores[i] != w {
			t.Errorf("store %d = %+v, want %+v", i, stores[i], w)
		}
	}
}

func TestRouteStoresNoRoutes(t *testing.T) {
	if stores := RouteStores(t.TempDir()); stores != nil {
		t.Errorf("expected nil for a town with no routes.jsonl, got %+v", stores)
	}
	if stores := RouteStores(""); stores != nil {
		t.Errorf("expected nil for an empty town root, got %+v", stores)
	}
}

// A moved bead keeps the id it was filed under, so prefix routing points at the
// rig that closed it. A registered store override must win for reads, writes,
// and rig ownership alike (gt-ad32).
func TestBeadStoreOverrideWinsOverPrefix(t *testing.T) {
	routes := `{"prefix": "hq-", "path": "."}
{"prefix": "dn-", "path": "duly_noted"}
{"prefix": "gt-", "path": "gastown/mayor/rig"}
`
	townRoot := newRouteFixture(t, routes, "duly_noted", "gastown/mayor/rig")
	townBeadsDir := filepath.Join(townRoot, ".beads")
	dnBeadsDir := filepath.Join(townRoot, "duly_noted", ".beads")
	gtWorkDir := filepath.Join(townRoot, "gastown/mayor/rig")
	gtBeadsDir := filepath.Join(gtWorkDir, ".beads")

	t.Cleanup(ClearBeadStores)
	ClearBeadStores()

	// Baseline: prefix routing sends dn-cqu to duly_noted.
	if got := ResolveBeadsDirForID(townBeadsDir, "dn-cqu"); got != dnBeadsDir {
		t.Fatalf("baseline ResolveBeadsDirForID = %q, want %q", got, dnBeadsDir)
	}
	if got := RigNameForBead(townRoot, "dn-cqu"); got != "duly_noted" {
		t.Fatalf("baseline RigNameForBead = %q, want %q", got, "duly_noted")
	}
	if got := ResolveHookDir(townRoot, "dn-cqu", ""); got != filepath.Join(townRoot, "duly_noted") {
		t.Fatalf("baseline ResolveHookDir = %q, want %q", got, filepath.Join(townRoot, "duly_noted"))
	}

	RegisterBeadStore("dn-cqu", BeadStore{Rig: "gastown", WorkDir: gtWorkDir, BeadsDir: gtBeadsDir})

	if got := ResolveBeadsDirForID(townBeadsDir, "dn-cqu"); got != gtBeadsDir {
		t.Errorf("ResolveBeadsDirForID after override = %q, want %q", got, gtBeadsDir)
	}
	if got := RigNameForBead(townRoot, "dn-cqu"); got != "gastown" {
		t.Errorf("RigNameForBead after override = %q, want %q", got, "gastown")
	}
	if got := ResolveHookDir(townRoot, "dn-cqu", ""); got != gtWorkDir {
		t.Errorf("ResolveHookDir after override = %q, want %q", got, gtWorkDir)
	}

	// Unrelated beads keep prefix routing.
	if got := ResolveBeadsDirForID(townBeadsDir, "dn-other"); got != dnBeadsDir {
		t.Errorf("unregistered bead ResolveBeadsDirForID = %q, want %q", got, dnBeadsDir)
	}
	if got := RigNameForBead(townRoot, "dn-other"); got != "duly_noted" {
		t.Errorf("unregistered bead RigNameForBead = %q, want %q", got, "duly_noted")
	}

	ClearBeadStores()
	if got := ResolveBeadsDirForID(townBeadsDir, "dn-cqu"); got != dnBeadsDir {
		t.Errorf("ResolveBeadsDirForID after clear = %q, want %q", got, dnBeadsDir)
	}
}

func TestRegisterBeadStoreIgnoresIncompleteEntries(t *testing.T) {
	t.Cleanup(ClearBeadStores)
	ClearBeadStores()

	RegisterBeadStore("", BeadStore{Rig: "gastown", BeadsDir: "/somewhere/.beads"})
	RegisterBeadStore("dn-cqu", BeadStore{Rig: "gastown"}) // no BeadsDir

	if _, ok := LookupBeadStore(""); ok {
		t.Error("empty bead ID should not register an override")
	}
	if _, ok := LookupBeadStore("dn-cqu"); ok {
		t.Error("store without a BeadsDir should not register an override")
	}
}
