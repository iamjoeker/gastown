package doctor

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// MovedBeadCheck surfaces beads that are open in a store their id prefix does
// not name — work that is invisible on every surface an operator reads.
//
// A bead filed in one rig and moved to the rig that owns the work keeps its
// original id. The destination rig then holds a live row under a foreign
// prefix, and prefix routing points every lookup at the source copy. The bead
// looks healthy: it appears in `bd ready` in its owning rig, with the right
// priority, and nothing marks it as anomalous. gt sling now resolves ownership
// from the live row (gt-ad32), but the split rows remain a data inconsistency
// worth naming — a second `bd close` or a re-file will hit the wrong copy.
type MovedBeadCheck struct {
	BaseCheck
}

// NewMovedBeadCheck creates a new moved-bead check.
func NewMovedBeadCheck() *MovedBeadCheck {
	return &MovedBeadCheck{
		BaseCheck: BaseCheck{
			CheckName:        "moved-beads",
			CheckDescription: "Check for open beads whose id prefix names a different rig",
			CheckCategory:    CategoryRig,
		},
	}
}

// movedBead is one open row sitting in a store its prefix does not name.
type movedBead struct {
	ID        string
	HeldBy    string // rig whose store holds the open row ("" = town-level store)
	PrefixRig string // rig the id prefix names ("" = town-level store)
	Title     string
}

// listOpenBeadIDs shells out to bd for the open rows in one store. It is a seam
// for tests; production uses listOpenBeadIDsImpl.
var listOpenBeadIDs = listOpenBeadIDsImpl

func listOpenBeadIDsImpl(store beads.BeadStore) ([]beadRow, error) {
	cmd := exec.Command("bd", "list", "--json", "--brief", "--limit", "0")
	cmd.Dir = store.WorkDir
	cmd.Env = append(cmd.Environ(), "BEADS_DIR="+store.BeadsDir)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseBeadRows(out)
}

// beadRow is the subset of a bd list row this check reads.
type beadRow struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Type   string   `json:"issue_type"`
	Labels []string `json:"labels"`
}

// identityLabels mark records of who exists, not work to be done. Every rig's
// agent, role, and rig beads live in the town-level store under that rig's own
// prefix by design; flagging them would bury the real finding under dozens of
// rows working exactly as intended.
//
// This is deliberately narrower than beads.ProtectedIssueLabel: gt:keep and
// gt:standing-orders mark real work that automation must not close, and such a
// bead sitting in the wrong store is still worth reporting.
var identityLabels = map[string]bool{"gt:agent": true, "gt:role": true, "gt:rig": true}

// isTownBookkeeping reports whether a row is Gas Town's own runtime state rather
// than dispatchable work.
func isTownBookkeeping(row beadRow) bool {
	if beads.InternalIssueType(row.Type) {
		return true
	}
	for _, label := range row.Labels {
		if identityLabels[strings.ToLower(strings.TrimSpace(label))] || beads.InternalIssueLabel(label) {
			return true
		}
	}
	return false
}

// parseBeadRows accepts both a bare array and bd's {"issues": [...]} envelope.
// Decoding straight into a slice returns zero rows for the envelope shape, and a
// check that turns a decode failure into an empty result reports OK on every rig
// forever.
func parseBeadRows(out []byte) ([]beadRow, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var rows []beadRow
		if err := json.Unmarshal(out, &rows); err != nil {
			return nil, fmt.Errorf("parsing bd list output: %w", err)
		}
		return rows, nil
	}
	var envelope struct {
		Issues []beadRow `json:"issues"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}
	return envelope.Issues, nil
}

// Run scans every store named by routes.jsonl for open rows under a foreign prefix.
func (c *MovedBeadCheck) Run(ctx *CheckContext) *CheckResult {
	stores := beads.RouteStores(ctx.TownRoot)
	if len(stores) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No routes.jsonl — nothing to cross-check",
		}
	}

	var found []movedBead
	var failures []string
	for _, store := range stores {
		rows, err := listOpenBeadIDs(store)
		if err != nil {
			// Report the failure. A store this check could not read is not a
			// store with nothing in it.
			failures = append(failures, fmt.Sprintf("%s: %v", describeStore(store), err))
			continue
		}
		for _, row := range rows {
			if isTownBookkeeping(row) {
				continue
			}
			prefix := beads.ExtractPrefix(row.ID)
			if prefix == "" {
				continue
			}
			if beads.GetRigPathForPrefix(ctx.TownRoot, prefix) == "" {
				continue // prefix not in routes — outside this check's remit
			}
			owner := beads.GetRigNameForPrefix(ctx.TownRoot, prefix)
			if owner == store.Rig {
				continue
			}
			found = append(found, movedBead{
				ID:        row.ID,
				HeldBy:    store.Rig,
				PrefixRig: owner,
				Title:     row.Title,
			})
		}
	}

	if len(failures) > 0 && len(found) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not read %d of %d beads stores", len(failures), len(stores)),
			Details: failures,
			FixHint: "Check that each store in .beads/routes.jsonl is reachable, then re-run gt doctor",
		}
	}

	if len(found) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("No open beads with foreign prefixes across %d stores", len(stores)),
		}
	}

	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	details := make([]string, 0, len(found)+len(failures))
	for _, b := range found {
		title := b.Title
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		details = append(details, fmt.Sprintf("%s open in %s, prefix names %s — %s",
			b.ID, describeRig(b.HeldBy), describeRig(b.PrefixRig), title))
	}
	details = append(details, failures...)

	return &CheckResult{
		Name:   c.Name(),
		Status: StatusWarning,
		Message: fmt.Sprintf("%d open bead(s) sit in a store their id prefix does not name",
			len(found)),
		Details: details,
		FixHint: "These are moved beads: the rig holding the open row owns the work, and the rig " +
			"the prefix names most likely holds a closed copy of the same id. Sling them to the rig " +
			"holding the open row, and close or re-file the stale copy (see gt-ad32)",
	}
}

func describeRig(rig string) string {
	if rig == "" {
		return "the town-level store"
	}
	return "rig " + rig
}

func describeStore(store beads.BeadStore) string {
	return describeRig(store.Rig)
}
