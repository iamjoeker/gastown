package reaper

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// ActiveMRClearer clears an agent bead's active_mr field when it still
// matches the given MR id. Satisfied by *beads.Beads, whose implementation
// locks the agent bead and re-checks the field before writing, so a polecat
// that has since reused active_mr for a newer MR is left alone.
type ActiveMRClearer interface {
	ClearAgentActiveMRIfMatches(id string, expectedMR string) (bool, error)
}

// DanglingActiveMRResult reports what ClearDanglingMRActiveRefs did.
type DanglingActiveMRResult struct {
	Database string `json:"database"`
	// Scanned counts closed MR wisps in this database that name an agent_bead.
	// It is not filtered by whether that bead's active_mr still points back —
	// ClearAgentActiveMRIfMatches makes that check, so this is an upper bound
	// on Cleared, not a prediction of it.
	Scanned int `json:"scanned"`
	// Cleared counts agent beads whose active_mr was actually cleared.
	Cleared int `json:"cleared"`
	// Errors counts candidates ClearAgentActiveMRIfMatches failed to check or
	// clear. Non-fatal: a stuck agent bead is skipped, not a reason to fail
	// the sweep that found it.
	Errors int `json:"errors,omitempty"`
}

// ClearDanglingMRActiveRefs finds MR wisps closed in dbName that still name
// an agent_bead in their description, and clears that agent bead's active_mr
// field when it still points back at the (now terminal) MR.
//
// Reap's age-bound sweep closes an MR wisp the same way it closes any other
// wisp, with no notion of the agent_bead reference an MR wisp's description
// carries. Once closed, a swept MR is never revisited — nothing else updates
// its created_at/closed_at — so an agent bead's active_mr pointer at it
// dangles permanently: nothing ever tells the holder, and
// polecat.AssessActiveMR's terminal-source-issue check only masks the
// stranding symptom in a couple of read paths, it does not repair the field
// (gt-p88i, gt-hx10, beads/slit via gt-3jx0).
//
// This is deliberately independent of what Reap closed on THIS call: it
// re-derives candidates straight from wisp status, so it also heals refs left
// dangling by a PAST sweep (before this existed), not only ones from the
// current run. clearer's own compare-and-clear makes that safe to repeat
// forever — an already-cleared or reused active_mr is left alone.
//
// Agent beads live in the town's hq database regardless of which rig's MR
// wisp names them (see beads.Beads.ForAgentBead), which is why the write goes
// through clearer rather than a query against mrDB.
func ClearDanglingMRActiveRefs(mrDB *sql.DB, dbName string, clearer ActiveMRClearer) (*DanglingActiveMRResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	result := &DanglingActiveMRResult{Database: dbName}

	rows, err := mrDB.QueryContext(ctx,
		"SELECT id, description FROM wisps WHERE wisp_type = 'mr' AND status = 'closed' AND description LIKE '%agent_bead:%'")
	if err != nil {
		return nil, fmt.Errorf("select closed MR wisps: %w", err)
	}

	type candidate struct{ mrID, agentBead string }
	var candidates []candidate
	for rows.Next() {
		var id, desc string
		if err := rows.Scan(&id, &desc); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan closed MR wisp: %w", err)
		}
		fields := beads.ParseMRFields(&beads.Issue{Description: desc})
		if fields == nil {
			continue
		}
		agentBead := strings.TrimSpace(fields.AgentBead)
		if agentBead == "" {
			continue
		}
		candidates = append(candidates, candidate{mrID: id, agentBead: agentBead})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read closed MR wisps: %w", err)
	}
	rows.Close()
	result.Scanned = len(candidates)

	if clearer == nil {
		return result, nil
	}

	for _, c := range candidates {
		cleared, err := clearer.ClearAgentActiveMRIfMatches(c.agentBead, c.mrID)
		if err != nil {
			// A single unreachable or malformed agent bead must not stop the
			// sweep from checking the rest of the candidates.
			result.Errors++
			continue
		}
		if cleared {
			result.Cleared++
		}
	}
	return result, nil
}

// ensure the interface stays satisfied by the real client; a signature drift
// here should fail the build, not surface as a silent no-op at runtime.
var _ ActiveMRClearer = (*beads.Beads)(nil)
