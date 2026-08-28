package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
)

// This file is compaction's ownership filter (gt-98hh).
//
// THE HOLE IT CLOSES. decideWisp deletes a molecule step past TTL whatever its
// status:
//
//	case isMoleculeStep:
//	    return wispVerdict{action: actionDelete, reason: "molecule step past TTL"}
//
// That branch is reached only when the status is NOT closed — the closed case is
// handled above it — so the one thing it deletes is a step that is still open,
// in_progress, blocked or hooked. Those are the steps of a molecule an agent is
// working right now. Nothing above it asked whose molecule it was, and there is
// no restore: the wisp tables are dolt-ignored, so there is no history to read
// AS OF and no backup (hq-del4).
//
// The Mayor's hold on `gt compact` names exactly this property — a sibling path
// (`bd mol wisp gc --age`) deletes any open wisp past the age window, including
// another agent's records, and compaction reaches the same destination by a
// different road. This file is that road's guard rail.
//
// WHY IT MEASURES AS ZERO TODAY, AND WHY THAT IS NOT REASSURANCE. Measured
// against live hq on 2026-08-23 (14,480 wisps), every one of the 4,411 rows a
// bare `gt compact` would have deleted was closed, and every parent of the 2,905
// that are steps was closed too. The guard would have held nothing. That is a
// statement about one afternoon's data, not about the code: the same query shows
// 3,286 typed step wisps under the dog molecules (mol-dog-doctor,
// mol-dog-checkpoint, mol-dog-jsonl, mol-dog-reaper), all stamped gc_report with
// a 24h TTL by stampMoleculeWispType. A dog whose session dies mid-molecule
// leaves those steps open; 24 hours later they are precisely the rows the branch
// above deletes, and what is destroyed is the evidence of the stuck dog. The
// count is zero because nothing has hung for a day yet, not because the branch
// cannot fire.
//
// WHAT COUNTS AS LIVE. A molecule is finished when its root wisp is closed. Any
// other status — open, in_progress, blocked, hooked, deferred — means something
// may still read or write it, so every wisp under it is held whatever its TTL.
// The walk goes all the way up, because a step of a step of a live molecule is
// just as live as a direct child.
//
// WHAT IS DELIBERATELY NOT HELD: a wisp whose ancestor row is gone. The molecule
// that owned it no longer exists, so it cannot be live, and holding those would
// make the guard mean "hold everything" — a guard that never releases is as
// uninformative as one that never fires.

// finishedWispStatus is the one status that means a molecule is over.
//
// Written as a whitelist of the finished state rather than a blacklist of the
// live ones on purpose: beads can add a status (it has seven today), and a new
// one must default to LIVE. A blacklist would default it to deletable, which is
// the direction that loses records.
const finishedWispStatus = "closed"

// wispOwnershipQuery reads the id, status and parent of EVERY wisp.
//
// It deliberately carries no issue_type exclusion, unlike mutableWispWhere.
// That filter is right for choosing what compaction may mutate and wrong for
// building an ancestry index: an infra-typed wisp compaction must never touch
// can still be the parent of a step it may. Excluding it here would make that
// parent invisible, the walk would find no row, and the step would read as an
// orphan of a swept molecule — released for deletion by the exact fact that
// makes it live.
//
// The parent join is the derived-table form wispSideJoins uses, for the reason
// gt-g60l records: as a correlated subquery this is one execution per row, and
// at hq's row count that is the 60s bd subprocess timeout.
const wispOwnershipQuery = `SELECT w.id, w.status, COALESCE(d.parent, '') AS parent ` +
	`FROM wisps w ` +
	`LEFT JOIN (SELECT issue_id, MIN(depends_on_wisp_id) AS parent ` +
	`FROM wisp_dependencies WHERE type = 'parent-child' AND depends_on_wisp_id IS NOT NULL ` +
	`GROUP BY issue_id) d ON d.issue_id = w.id`

// wispOwnerRow is one row of wispOwnershipQuery.
type wispOwnerRow struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Parent string `json:"parent"`
}

// wispOwnership answers one question: does this wisp belong to a molecule that
// has finished?
//
// It holds the whole wisps table because the answer is not a property of the
// row — it is a property of the chain above it, and the chain can reach rows
// compaction is not allowed to touch.
type wispOwnership struct {
	nodes map[string]wispOwnerRow
	// err records why the index could not be built. It is not discarded,
	// because the guard's answer when it is set is "hold", and an operator
	// reading "Protected: 4411" needs to be told the reason is a failed query
	// and not four thousand live molecules.
	err error
}

// loadWispOwnership builds the ancestry index.
//
// It never returns nil and it never returns an error to the caller: a failure
// is carried INSIDE the returned value, where guard turns it into a hold. The
// alternative — returning an error the caller may or may not check — is how a
// guard ends up disabled by a call site that compiles.
func loadWispOwnership(bd *beads.Beads) *wispOwnership {
	own := &wispOwnership{nodes: map[string]wispOwnerRow{}}
	if bd == nil {
		own.err = fmt.Errorf("no beads handle")
		return own
	}

	out, err := bd.Run("sql", "--json", wispOwnershipQuery)
	if err != nil {
		own.err = fmt.Errorf("querying wisp ancestry: %w", err)
		return own
	}

	var rows []wispOwnerRow
	if err := json.Unmarshal(extractJSONArray(out), &rows); err != nil {
		own.err = fmt.Errorf("parsing wisp ancestry: %w", err)
		return own
	}
	for _, r := range rows {
		own.nodes[r.ID] = r
	}
	return own
}

// guard returns a description of the live molecule that forbids deleting w, or
// "" if nothing does.
//
// A nil receiver is an unbuilt index, not an absent policy, and it HOLDS. That
// is the point of putting the guard on a nilable pointer: a call site added
// later that has no ownership index to pass gets the conservative answer for
// free, where a plain function taking a map would silently see an empty one and
// release every wisp in the run.
func (o *wispOwnership) guard(w *compactIssue) string {
	if o == nil {
		return "molecule ownership unknown (no ancestry index)"
	}
	if o.err != nil {
		return fmt.Sprintf("molecule ownership unknown (%v)", o.err)
	}

	// The wisp's own status comes from the scanned row rather than the index.
	// Same table, same run, and it keeps the self-check answering even for a
	// row the ancestry query somehow missed.
	if w.Status != finishedWispStatus {
		return fmt.Sprintf("live molecule: wisp is %s, not %s", w.Status, finishedWispStatus)
	}

	seen := map[string]bool{w.ID: true}
	for id := w.Parent; id != ""; {
		if seen[id] {
			// A parent cycle is corrupt data, not a finished molecule. Stop
			// walking and hold: the chain cannot be shown to have ended.
			return fmt.Sprintf("molecule ownership unknown (parent cycle at %s)", id)
		}
		seen[id] = true

		node, ok := o.nodes[id]
		if !ok {
			// The ancestor row is gone, so the molecule that owned this wisp
			// no longer exists and cannot be live. This is the release arm; a
			// guard with no release arm holds everything and proves nothing.
			return ""
		}
		if node.Status != finishedWispStatus {
			return fmt.Sprintf("step of live molecule %s (%s)", id, node.Status)
		}
		id = node.Parent
	}
	return ""
}
