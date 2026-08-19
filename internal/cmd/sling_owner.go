package cmd

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// Bead ownership after a cross-rig move (gt-ad32).
//
// `gt sling` resolves a bead's store from its id prefix. That is only correct
// while the bead lives where it was filed. Beads filed in one rig and moved to
// the rig that owns the work keep their original id: the destination rig gets a
// live row, the source rig's copy is closed, and prefix routing then points at
// the closed copy. Dispatch deadlocks — the rig the prefix names refuses on
// status ("closed — work already completed"), and the rig holding the open row
// refuses on prefix ("cross-rig mismatch") — and neither message names the store
// it read, so both read as operator error.
//
// Ownership follows the live row. resolveBeadOwner locates it and registers the
// answer with the beads router, so every later read and write in this process
// reaches the row that is actually open. The extra lookups run only when the
// prefix store has no live row, i.e. only on paths that fail today.

// beadOwner records which store holds a bead's authoritative row.
type beadOwner struct {
	Rig       string          // rig whose store holds the row ("" = town-level store)
	PrefixRig string          // rig named by the bead's id prefix ("" = town-level store)
	Store     beads.BeadStore // store the row was read from
	Info      *beadInfo       // the row that was read
	Moved     bool            // live row lives outside the store the prefix names
}

// beadStatusIsLive reports whether a status still represents dispatchable work.
func beadStatusIsLive(status string) bool {
	return status != "closed" && status != "tombstone"
}

// Seams for tests. Production uses the real bd-backed lookups.
var (
	// readBeadInfoFromStore reads a bead's row from one specific store.
	readBeadInfoFromStore = readBeadInfoFromStoreImpl
	// readBeadInfoForOwner reads a bead through normal prefix routing, including
	// bd's own routed fallback.
	readBeadInfoForOwner = getBeadInfoFromTownRoot
)

func readBeadInfoFromStoreImpl(store beads.BeadStore, beadID string) (*beadInfo, error) {
	out, err := BdCmd("show", beadID, "--json").
		AllowStale().
		Dir(store.WorkDir).
		WithBeadsDir(store.BeadsDir).
		StripBeadsDir().
		Stderr(io.Discard).
		Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return nil, fmt.Errorf("bead '%s' not found", beadID)
	}
	return parseBeadInfo(beadID, out)
}

// resolveBeadOwner returns the store holding beadID's authoritative row.
//
// The store the id prefix names is consulted first and is the answer whenever it
// holds a live row. Only when that row is closed, a tombstone, or absent does
// this look for a live row in the town's other stores — the signature of a bead
// moved across rigs. A live row found elsewhere is registered with the beads
// router so subsequent bd calls for this id reach it too.
//
// Returns an error only when the bead exists in no store at all.
func resolveBeadOwner(townRoot, beadID string) (*beadOwner, error) {
	prefixRig := beads.GetRigNameForPrefix(townRoot, beads.ExtractPrefix(beadID))
	prefixBeadsDir := beads.ResolveBeadsDirForID(filepath.Join(townRoot, ".beads"), beadID)
	prefixStore := beads.BeadStore{
		Rig:      prefixRig,
		WorkDir:  filepath.Dir(prefixBeadsDir),
		BeadsDir: prefixBeadsDir,
	}

	owner := &beadOwner{Rig: prefixRig, PrefixRig: prefixRig, Store: prefixStore}
	info, err := readBeadInfoForOwner(townRoot, beadID)
	if err == nil {
		owner.Info = info
		if beadStatusIsLive(info.Status) {
			return owner, nil
		}
	}

	if liveStore, liveInfo := findLiveBeadRow(townRoot, beadID, prefixBeadsDir); liveInfo != nil {
		beads.RegisterBeadStore(beadID, liveStore)
		return &beadOwner{
			Rig:       liveStore.Rig,
			PrefixRig: prefixRig,
			Store:     liveStore,
			Info:      liveInfo,
			Moved:     true,
		}, nil
	}

	if err != nil {
		return nil, err
	}
	return owner, nil
}

// findLiveBeadRow scans the town's beads stores, skipping the one at skipBeadsDir,
// for a live row under beadID. Returns the first match; a nil info means the id
// is closed or absent everywhere else.
func findLiveBeadRow(townRoot, beadID, skipBeadsDir string) (beads.BeadStore, *beadInfo) {
	for _, store := range beads.RouteStores(townRoot) {
		if store.BeadsDir == skipBeadsDir {
			continue
		}
		info, err := readBeadInfoFromStore(store, beadID)
		if err != nil || info == nil {
			continue
		}
		if beadStatusIsLive(info.Status) {
			return store, info
		}
	}
	return beads.BeadStore{}, nil
}

// reportMovedBead tells the operator that ownership was taken from the live row
// rather than the id prefix. Silent adoption would be worse than the bug: the
// id no longer indicates which rig the work lands in.
func reportMovedBead(beadID string, owner *beadOwner) {
	if owner == nil || !owner.Moved {
		return
	}
	fmt.Printf("  %s Bead %s is closed in %s but open in %s — dispatching against the open row (moved bead, see gt-ad32)\n",
		style.Warning.Render("⚠"), beadID, describeBeadStoreRig(owner.PrefixRig), describeBeadStoreRig(owner.Rig))
}

// closedBeadError reports a closed/tombstone bead and names the store the status
// came from. The same id can be closed in one rig and open in another, and a
// bare "bead X is closed" is what made that a dead end rather than a puzzle.
func closedBeadError(beadID string, owner *beadOwner) error {
	if owner == nil || owner.Info == nil {
		return fmt.Errorf("bead %s is closed (work already completed)", beadID)
	}
	return fmt.Errorf("bead %s is %s in %s (work already completed)",
		beadID, owner.Info.Status, describeBeadStoreRig(owner.Rig))
}

// describeBeadStoreRig renders a rig name for error messages, naming the
// town-level store explicitly rather than as an empty string.
func describeBeadStoreRig(rig string) string {
	if rig == "" {
		return "the town-level beads store"
	}
	return fmt.Sprintf("rig %q", rig)
}
