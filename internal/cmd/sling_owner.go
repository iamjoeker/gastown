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

// Ownership in the DEFERRED dispatch path (gt-ygb7).
//
// gt-ad32 taught the sling entry points to follow the live row, but the queue
// behind them was never told. `gt sling` with scheduler.max_polecats > 0 does
// not dispatch: it writes a sling context bead and returns, and a later
// process — the daemon — reads that queue. beads.RegisterBeadStore is a
// per-process table, so the reader starts with prefix routing and nothing else.
//
// Every scheduler surface then reads a moved bead's CLOSED source row:
// cleanupStaleContexts closes the context as "stale-work-bead", the readiness
// filter drops it for not being open, `gt scheduler list` hides it, and the
// cross-rig prefix guard would refuse the dispatch even if the others let it
// through. The operator sees only the "✓ Scheduled" that scheduleBead printed
// while the bead was still, briefly, in the queue.
//
// The two helpers below give the reader the same rule the writer already uses.

// adoptMovedWorkBeadRows replaces prefix-routed rows that are missing or dead
// with the live row from whichever store holds it. Only ids whose prefix store
// has no live row cost anything: a bead that is genuinely closed everywhere
// stays closed, so stale-context cleanup still reaps completed work.
//
// resolveBeadOwner registers what it finds with the beads router, so the
// blocked-dependency query and the hook writes that follow in this process
// reach the live row too.
func adoptMovedWorkBeadRows(townRoot string, ids []string, rows map[string]beadStatusInfo) {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if row, ok := rows[id]; ok && beadStatusIsLive(row.Status) {
			continue
		}
		owner, err := resolveBeadOwner(townRoot, id)
		if err != nil || owner == nil || !owner.Moved || owner.Info == nil {
			continue
		}
		rows[id] = beadStatusInfoFromBeadInfo(owner.Info)
	}
}

// adoptMovedTrackedDeps corrects convoy tracking for tracked issues that moved
// to another rig after being enqueued. getIssueDetailsBatch resolves ids by
// prefix, so a tracked bead moved elsewhere and closed at its filed-in copy
// reads as "closed" here even though its live row, elsewhere, is still open.
// Left uncorrected, a convoy auto-closes the moment the SOURCE copy closes,
// regardless of whether the tracked work ever ran (gt-ju7k).
//
// Mirrors adoptMovedWorkBeadRows: only tracked deps that read closed/tombstone
// via prefix routing pay for the extra lookup, and a bead that is genuinely
// closed everywhere is left alone.
func adoptMovedTrackedDeps(townRoot string, deps []trackedDependency) {
	for i := range deps {
		dep := &deps[i]
		if beadStatusIsLive(dep.Status) {
			continue
		}
		owner, err := resolveBeadOwner(townRoot, dep.ID)
		if err != nil || owner == nil || !owner.Moved || owner.Info == nil {
			continue
		}
		dep.Status = owner.Info.Status
		if owner.Info.Title != "" {
			dep.Title = owner.Info.Title
		}
		dep.IssueType = owner.Info.IssueType
		dep.Assignee = owner.Info.Assignee
		dep.Labels = owner.Info.Labels
	}
}

// beadOwnedByRig reports whether beadID's live row lives in rigName. A non-nil
// owner is used as given; otherwise ownership is resolved on the spot. Returns
// false for an unnamed rig and for a bead that exists in no store — callers use
// this to grant an exception, never to justify one.
func beadOwnedByRig(townRoot, rigName, beadID string, owner *beadOwner) bool {
	if rigName == "" {
		return false
	}
	if owner == nil {
		resolved, err := resolveBeadOwner(townRoot, beadID)
		if err != nil || resolved == nil {
			return false
		}
		owner = resolved
	}
	return owner.Rig == rigName
}

// describeBeadStoreRig renders a rig name for error messages, naming the
// town-level store explicitly rather than as an empty string.
func describeBeadStoreRig(rig string) string {
	if rig == "" {
		return "the town-level beads store"
	}
	return fmt.Sprintf("rig %q", rig)
}
