package convoy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// The two live-system lookups Classify needs beyond a bead's own row. They live
// here rather than in a caller because both `gt convoy stranded` and the
// dashboard have to ask them the same way: a polecat whose session died after
// submitting is IN-QUEUE, not abandoned, and a bead with an open sling context
// is SCHEDULED, not idle. A surface that cannot ask ends up inferring
// abandonment from silence, which is the defect this package exists to remove
// (gt-skzk.1).

// HasQueuedMergeRequest reports whether the bead's work is already sitting in
// the merge queue as an open MR. Errors read as "no" — this only ever suppresses
// a stranded flag, so failing closed would restore the false positive it exists
// to remove.
func HasQueuedMergeRequest(townRoot, beadID string) bool {
	bd := mergeRequestBeads(townRoot, beadID)
	if bd == nil {
		return false
	}
	mrs, err := bd.FindMRsForIssue(beadID, false)
	if err != nil {
		return false
	}
	for _, mr := range mrs {
		if mr == nil {
			continue
		}
		// An MR without a branch carries no recoverable work, so it holds no
		// slot in the queue and proves nothing about this bead.
		fields := beads.ParseMRFields(mr)
		if fields == nil || fields.Branch == "" {
			continue
		}
		if !strings.EqualFold(mr.Status, "closed") {
			return true
		}
	}
	return false
}

// mergeRequestBeads returns a Beads handle rooted at the database where the
// bead's merge requests actually live.
//
// This routing is load-bearing (gt-79li). MR beads are wisps in the RIG
// database, but callers hold the TOWN beads dir, and the MR queries run raw SQL
// with no prefix routing. Handing them the town dir queries hq, where no rig MR
// has ever existed.
func mergeRequestBeads(townRoot, beadID string) *beads.Beads {
	beadsDir := beads.ResolveBeadsDirForID(filepath.Join(townRoot, ".beads"), beadID)
	if beadsDir == "" {
		return nil
	}
	return beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
}

// OpenSlingContextWorkBeads returns the set of work-bead IDs that have an open
// sling context somewhere in town — beads already queued for dispatch and
// waiting only for capacity.
//
// It returns an error if ANY store could not be read. Callers must fail closed
// on that: a partial scan that reads as "nothing is scheduled" is what lets a
// scheduler re-dispatch work it could not prove was unscheduled (gt-mji1).
func OpenSlingContextWorkBeads(townRoot string) (map[string]bool, error) {
	scheduled := make(map[string]bool)

	dirs, err := BeadsSearchDirs(townRoot)
	if err != nil {
		return nil, fmt.Errorf("listing sling contexts: %w", err)
	}

	var skipped []string
	for _, dir := range dirs {
		beadsDir := beads.ResolveBeadsDir(dir)
		b := beads.NewWithBeadsDir(dir, beadsDir)
		contexts, err := b.ListOpenSlingContexts()
		if err != nil {
			skipped = append(skipped, beadsDir)
			continue
		}
		for _, ctx := range contexts {
			if ctx == nil {
				continue
			}
			if fields := beads.ParseSlingContextFields(ctx.Description); fields != nil {
				scheduled[fields.WorkBeadID] = true
			}
		}
	}

	if len(skipped) > 0 {
		return scheduled, fmt.Errorf("listing sling contexts: scan could not read %d of %d stores: %s",
			len(skipped), len(dirs), strings.Join(skipped, ", "))
	}
	return scheduled, nil
}

// BeadsSearchDirs lists every directory under the town root that holds a beads
// store: the town itself, each rig, and each rig's mayor/rig sub-store.
func BeadsSearchDirs(townRoot string) ([]string, error) {
	dirs := []string{townRoot}
	seen := map[string]bool{townRoot: true}
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return nil, fmt.Errorf("discovering beads search dirs: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "mayor" || e.Name() == "settings" {
			continue
		}
		rigDir := filepath.Join(townRoot, e.Name())
		beadsDir := filepath.Join(rigDir, ".beads")
		if _, err := os.Stat(beadsDir); err == nil && !seen[rigDir] {
			dirs = append(dirs, rigDir)
			seen[rigDir] = true
		}
		mayorRigDir := filepath.Join(rigDir, "mayor", "rig")
		mayorBeadsDir := filepath.Join(mayorRigDir, ".beads")
		if _, err := os.Stat(mayorBeadsDir); err == nil && !seen[mayorRigDir] {
			dirs = append(dirs, mayorRigDir)
			seen[mayorRigDir] = true
		}
	}
	return dirs, nil
}
