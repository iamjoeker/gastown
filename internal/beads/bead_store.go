package beads

import (
	"path/filepath"
	"strings"
	"sync"
)

// BeadStore identifies one beads store: the `.beads` directory that holds rows,
// the directory `bd` must run from to reach it, and the rig that owns it.
type BeadStore struct {
	Rig      string // rig name that owns the store ("" for the town-level store)
	WorkDir  string // directory to run bd from (the parent of BeadsDir)
	BeadsDir string // resolved .beads directory
}

// RouteStores returns the distinct beads stores named by routes.jsonl, ordered
// as the file lists them and de-duplicated by resolved .beads directory (several
// prefixes routinely share one store, e.g. "hq-" and "hq-cv-").
//
// Returns nil when the town has no routes file.
func RouteStores(townRoot string) []BeadStore {
	if townRoot == "" {
		return nil
	}
	townBeadsDir := filepath.Join(townRoot, ".beads")
	routes, err := LoadRoutes(townBeadsDir)
	if err != nil || len(routes) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(routes))
	stores := make([]BeadStore, 0, len(routes))
	for _, r := range routes {
		store := BeadStore{WorkDir: townRoot, BeadsDir: townBeadsDir}
		if r.Path != "." {
			store.Rig = strings.SplitN(r.Path, "/", 2)[0]
			store.BeadsDir = ResolveBeadsDir(filepath.Join(townRoot, r.Path))
			store.WorkDir = filepath.Dir(store.BeadsDir)
		}
		if seen[store.BeadsDir] {
			continue
		}
		seen[store.BeadsDir] = true
		stores = append(stores, store)
	}
	return stores
}

// Moved-bead store overrides (gt-ad32).
//
// A bead's id prefix records where it was FILED, not which rig owns it. When a
// bead is moved to the rig that owns the work it keeps its original id, so the
// destination rig holds the live row while the source rig's row is closed —
// and prefix routing sends every subsequent read AND write to the stale source
// row. Symptoms range from misleading ("bead is closed — work already
// completed") to silently wrong (a hook written to the closed copy).
//
// Ownership follows the live row. Callers that have positively identified the
// store holding it register that answer here, and prefix resolution honours it
// for the rest of the process. The table stays empty unless a move has actually
// been detected, so ordinary routing is untouched.
var (
	beadStoreOverrideMu sync.RWMutex
	beadStoreOverrides  = map[string]BeadStore{}
)

// RegisterBeadStore records the store that holds beadID's live row, overriding
// prefix routing for that id. Only register a store you have read the row from.
func RegisterBeadStore(beadID string, store BeadStore) {
	if beadID == "" || store.BeadsDir == "" {
		return
	}
	beadStoreOverrideMu.Lock()
	defer beadStoreOverrideMu.Unlock()
	beadStoreOverrides[beadID] = store
}

// LookupBeadStore returns a registered store override for beadID, if any.
func LookupBeadStore(beadID string) (BeadStore, bool) {
	beadStoreOverrideMu.RLock()
	defer beadStoreOverrideMu.RUnlock()
	store, ok := beadStoreOverrides[beadID]
	return store, ok
}

// ClearBeadStores drops all registered overrides. Intended for tests.
func ClearBeadStores() {
	beadStoreOverrideMu.Lock()
	defer beadStoreOverrideMu.Unlock()
	beadStoreOverrides = map[string]BeadStore{}
}

// RigNameForBead returns the rig that owns a bead, preferring a registered
// store override over the rig its id prefix names. Returns empty string for
// town-level beads and for prefixes absent from routes.jsonl — same contract as
// GetRigNameForPrefix, which it falls back to.
func RigNameForBead(townRoot, beadID string) string {
	if store, ok := LookupBeadStore(beadID); ok {
		return store.Rig
	}
	return GetRigNameForPrefix(townRoot, ExtractPrefix(beadID))
}
