package doctor

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/reaper"
)

// RigWispGCOptInEnv gates the one place gastown runs `bd mol wisp gc` against a
// RIG database. Set it to "1" (or "true") to allow it.
//
// The command deletes wisps outright and wisps are unversioned and unbacked, so
// there is no restore path by any means the town has. Against hq that is merely
// expensive; against a rig database it reaches the gt-/bd- prefixed
// merge-request records, and a merge-request wisp closed WITHOUT merging is the
// only surviving record that work was pushed and never landed — by the time it
// is deleted the authoring polecat is gone and the remote branch may be gone
// too (gt-22s). The deacon's preservation wrapper does not help here: it is
// hq-scoped and never sees a rig wisp, so what makes a deacon-context gc safe
// is its database scope, not the wrapper.
//
// Default off, so this check reports and does not destroy. That is also what
// keeps it consistent with the town-wide hold on the command.
const RigWispGCOptInEnv = "GT_ALLOW_RIG_WISP_GC"

// WispGCCheck detects and cleans orphaned wisps that are older than a threshold.
// Wisps are ephemeral issues (Wisp: true flag) used for patrol cycles and
// operational workflows that shouldn't accumulate.
type WispGCCheck struct {
	FixableCheck
	threshold     time.Duration
	abandonedRigs map[string]int // rig -> count of abandoned wisps
}

// NewWispGCCheck creates a new wisp GC check with 1 hour threshold.
func NewWispGCCheck() *WispGCCheck {
	return &WispGCCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "wisp-gc",
				CheckDescription: "Detect and clean orphaned wisps (>1h old)",
				CheckCategory:    CategoryCleanup,
			},
		},
		threshold:     1 * time.Hour,
		abandonedRigs: make(map[string]int),
	}
}

// Run checks for abandoned wisps in each rig.
func (c *WispGCCheck) Run(ctx *CheckContext) *CheckResult {
	c.abandonedRigs = make(map[string]int)

	rigs, err := discoverRigs(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Failed to discover rigs",
			Details: []string{err.Error()},
		}
	}

	if len(rigs) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs configured",
		}
	}

	var details []string
	totalAbandoned := 0

	for _, rigName := range rigs {
		rigPath := filepath.Join(ctx.TownRoot, rigName)
		count, protectionKnown := c.countAbandonedWisps(rigPath)
		if !protectionKnown {
			details = append(details, fmt.Sprintf("%s: protected-wisp probe failed — counts may include records the fix will refuse to touch", rigName))
		}
		if count > 0 {
			c.abandonedRigs[rigName] = count
			totalAbandoned += count
			details = append(details, fmt.Sprintf("%s: %d abandoned wisp(s)", rigName, count))
		}
	}

	if totalAbandoned > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d abandoned wisp(s) found (>1h old)", totalAbandoned),
			Details: details,
			FixHint: fmt.Sprintf("Deleting rig wisps is irreversible; 'gt doctor --fix' declines unless %s=1 is set", RigWispGCOptInEnv),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "No abandoned wisps found",
		Details: details,
	}
}

// wispListEnvelope is the shape of `bd mol wisp list --json`.
//
// bd wraps the rows in an object with a count; it does not return a bare array.
// Unmarshalling that object into a slice fails, and the failure is silent
// because the caller reports zero on any error — which read as "no abandoned
// wisps" on every rig, forever.
type wispListEnvelope struct {
	Wisps []wispListRow `json:"wisps"`
}

type wispListRow struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

// parseWispList decodes `bd mol wisp list --json` output, accepting either the
// current object envelope or a bare array from an older bd.
func parseWispList(output []byte) ([]wispListRow, error) {
	var envelope wispListEnvelope
	if err := json.Unmarshal(output, &envelope); err == nil {
		return envelope.Wisps, nil
	}

	var rows []wispListRow
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// countAbandonedWisps counts wisps older than the threshold in a rig, excluding
// those the fix is not permitted to delete.
//
// Queries the wisps table via bd mol wisp list (Dolt server is required).
//
// Protected records are subtracted so the number reported is the number the fix
// would act on. A count that includes rows the fix refuses is the scan/act
// divergence the reaper's purge counters already guard against: it reads as
// work outstanding when there is none, and it invites an operator to reach for
// the override to clear a warning that the override cannot clear.
//
// The second return value is false when the protection probe failed, meaning
// the count could not be filtered.
func (c *WispGCCheck) countAbandonedWisps(rigPath string) (int, bool) {
	// Query wisps table via bd CLI
	cmd := exec.Command("bd", "mol", "wisp", "list", "--json")
	cmd.Dir = rigPath

	output, err := cmd.Output()
	if err != nil {
		// Dolt is the only supported backend — no wisps table means 0 abandoned wisps.
		return 0, true
	}

	wisps, err := parseWispList(output)
	if err != nil {
		return 0, true
	}

	protected, protectErr := protectedWispIDs(rigPath)
	protectionKnown := protectErr == nil

	// Use UTC for cutoff: Dolt stores timestamps in UTC (gt-ty4).
	cutoff := time.Now().UTC().Add(-c.threshold)
	count := 0
	for _, w := range wisps {
		if w.Status == "closed" {
			continue
		}
		if protected[w.ID] {
			continue
		}
		updatedAt, err := time.Parse(time.RFC3339, w.UpdatedAt)
		if err != nil {
			continue
		}
		if !updatedAt.IsZero() && updatedAt.Before(cutoff) {
			count++
		}
	}

	return count, protectionKnown
}

// wispGCArgs is the argv this check passes to bd, in one place so the
// reachability probe below can be checked against it.
//
// It must stay a bare gc. --closed and --all pull closed wisps into the delete
// set, and --force removes the preview that makes the closed forms declinable;
// protectedWispsAtRisk only vouches for the rows a bare gc can take.
func wispGCArgs() []string {
	return []string{"mol", "wisp", "gc"}
}

// protectedWispIDs returns the ids in a rig's database carrying a label that no
// deletion path may take.
//
// The list is reaper.ProtectedWispLabels, the town's single list of
// never-delete types, rather than a copy made here — gt doctor's shell-out to
// `bd mol wisp gc` is the fourth deleter to import it, after that package's own
// SQL delete, `bd purge`, and `gt compact`. A second copy would drift.
//
// bd's own gc has held these labels back since v1.2.x, but that is bd's
// promise, made by a binary this process does not pin and a `gc.protected_labels`
// setting any rig can override. This check does not delegate the question.
func protectedWispIDs(rigPath string) (map[string]bool, error) {
	labels := reaper.ProtectedWispLabels
	if len(labels) == 0 {
		return map[string]bool{}, nil
	}

	quoted := make([]string, len(labels))
	for i, label := range labels {
		quoted[i] = "'" + strings.ReplaceAll(label, "'", "''") + "'"
	}
	query := fmt.Sprintf("SELECT DISTINCT issue_id FROM wisp_labels WHERE label IN (%s)", strings.Join(quoted, ", "))

	cmd := exec.Command("bd", "sql", "--csv", query) //nolint:gosec // G204: query is built from a constant label list
	cmd.Dir = rigPath
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("probe protected wisps: %w", err)
	}

	records, err := csv.NewReader(strings.NewReader(string(output))).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse protected wisp probe: %w", err)
	}

	ids := make(map[string]bool)
	for i, rec := range records {
		if i == 0 || len(rec) == 0 { // header
			continue
		}
		if id := strings.TrimSpace(rec[0]); id != "" {
			ids[id] = true
		}
	}
	return ids, nil
}

// protectedWispsAtRisk returns the protected wisps a bare `bd mol wisp gc`
// could reach in this rig: the ones that are not closed, which is the set the
// age sweep reclaims.
//
// An open merge-request wisp is not an edge case. It is what exists for as long
// as an MR sits in the queue unmerged, and the age threshold here is one hour —
// so the window in which this returns rows is exactly the window in which the
// record is load-bearing.
func protectedWispsAtRisk(rigPath string) ([]string, error) {
	protected, err := protectedWispIDs(rigPath)
	if err != nil {
		return nil, err
	}
	if len(protected) == 0 {
		return nil, nil
	}

	cmd := exec.Command("bd", "sql", "--csv", "SELECT id FROM wisps WHERE status != 'closed'") //nolint:gosec // G204: query is a constant
	cmd.Dir = rigPath
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("probe open wisps: %w", err)
	}

	records, err := csv.NewReader(strings.NewReader(string(output))).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse open wisp probe: %w", err)
	}

	var atRisk []string
	for i, rec := range records {
		if i == 0 || len(rec) == 0 { // header
			continue
		}
		id := strings.TrimSpace(rec[0])
		if protected[id] {
			atRisk = append(atRisk, id)
		}
	}
	sort.Strings(atRisk)
	return atRisk, nil
}

// rigWispGCAllowed reports whether the operator has explicitly opted in.
func rigWispGCAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(RigWispGCOptInEnv))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// Fix runs bd mol wisp gc in each rig with abandoned wisps.
//
// Two guards stand in front of that, both refusing rather than proceeding:
//
//   - the operator must opt in through RigWispGCOptInEnv. This call site runs
//     against rig databases, never hq, which is the case gt-22s asks be gated
//     behind an explicit override rather than left to prose.
//   - even opted in, a rig holding a protected wisp the gc could reach is
//     skipped and named. The probe failing counts as reachable: a Dolt outage
//     is not evidence that there is nothing to lose, and the deletion is the
//     half of this that cannot be retried.
//
// Skipping a rig is reported as an error so the refusal shows up in the fix
// summary. Abandoned wisps accumulating is recoverable; the alternative is not.
func (c *WispGCCheck) Fix(ctx *CheckContext) error {
	if len(c.abandonedRigs) == 0 {
		return nil
	}

	rigs := make([]string, 0, len(c.abandonedRigs))
	for rigName := range c.abandonedRigs {
		rigs = append(rigs, rigName)
	}
	sort.Strings(rigs)

	if !rigWispGCAllowed() {
		return fmt.Errorf("declined to garbage collect rig wisps in %s: `bd mol wisp gc` deletes rig-database wisps outright and wisps are unversioned and unbacked, so there is no restore path; set %s=1 to allow it",
			strings.Join(rigs, ", "), RigWispGCOptInEnv)
	}

	var errs []string

	for _, rigName := range rigs {
		rigPath := filepath.Join(ctx.TownRoot, rigName)

		atRisk, err := protectedWispsAtRisk(rigPath)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: skipped, could not confirm no protected wisp is reachable: %v", rigName, err))
			continue
		}
		if len(atRisk) > 0 {
			errs = append(errs, fmt.Sprintf("%s: skipped, %d protected wisp(s) reachable by the gc (%s)", rigName, len(atRisk), strings.Join(atRisk, ", ")))
			continue
		}

		cmd := exec.Command("bd", wispGCArgs()...)
		cmd.Dir = rigPath
		if output, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v (%s)", rigName, err, string(output)))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}
