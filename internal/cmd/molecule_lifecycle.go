package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/workspace"
)

// runMoleculeBurn burns (destroys) the current molecule attachment.
func runMoleculeBurn(cmd *cobra.Command, args []string) (retErr error) {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}
	if townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace")
	}

	// Determine target agent
	var target string
	if len(args) > 0 {
		target = args[0]
	} else {
		// Auto-detect using env-aware role detection
		roleInfo, err := GetRoleWithContext(cwd, townRoot)
		if err != nil {
			return fmt.Errorf("detecting role: %w", err)
		}
		roleCtx := RoleContext{
			Role:     roleInfo.Role,
			Rig:      roleInfo.Rig,
			Polecat:  roleInfo.Polecat,
			TownRoot: townRoot,
			WorkDir:  cwd,
		}
		target = buildAgentIdentity(roleCtx)
		if target == "" {
			return fmt.Errorf("cannot determine agent identity (role: %s)", roleCtx.Role)
		}
	}

	// Find beads directory
	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Find agent's pinned bead (handoff bead)
	role := extractRoleFromIdentity(target)

	handoff, err := b.FindHandoffBead(role)
	if err != nil {
		return fmt.Errorf("finding handoff bead: %w", err)
	}
	if handoff == nil {
		return fmt.Errorf("no handoff bead found for %s (looked for %q with pinned status)", target, beads.HandoffBeadTitle(role))
	}

	// Check for attached molecule
	attachment := beads.ParseAttachmentFields(handoff)
	if attachment == nil || attachment.AttachedMolecule == "" {
		fmt.Printf("%s No molecule attached to %s - nothing to burn\n",
			style.Dim.Render("ℹ"), target)
		return nil
	}

	moleculeID := attachment.AttachedMolecule

	// Recursively close all descendant step issues before detaching
	// This prevents orphaned step issues from accumulating (gt-psj76.1)
	childrenClosed := closeDescendants(b, moleculeID)
	defer func() {
		ctx := context.Background()
		if cmd != nil {
			ctx = cmd.Context()
		}
		telemetry.RecordMolBurn(ctx, moleculeID, childrenClosed, retErr)
	}()

	// Detach the molecule with audit logging (this "burns" it by removing the attachment)
	_, err = b.DetachMoleculeWithAudit(handoff.ID, beads.DetachOptions{
		Operation: "burn",
		Agent:     target,
		Reason:    "molecule burned by agent",
	})
	if err != nil {
		return fmt.Errorf("detaching molecule: %w", err)
	}
	// Close the molecule root after detach so the audit sees original status.
	// Without this, the wisp root stays in "hooked" status indefinitely,
	// causing patrol molecule leaks (issue #1828).
	rootClosed := true
	if closeErr := b.ForceCloseWithReason("burned", moleculeID); closeErr != nil {
		style.PrintWarning("could not close molecule root %s: %v", moleculeID, closeErr)
		rootClosed = false
	}

	if moleculeJSON {
		result := map[string]interface{}{
			"burned":          moleculeID,
			"from":            target,
			"handoff_id":      handoff.ID,
			"children_closed": childrenClosed,
			"root_closed":     rootClosed,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	fmt.Printf("%s Burned molecule %s from %s\n",
		style.Bold.Render("🔥"), moleculeID, target)
	if childrenClosed > 0 {
		fmt.Printf("  Closed %d step issues\n", childrenClosed)
	}

	return nil
}

// runMoleculeSquash squashes the current molecule into a digest.
func runMoleculeSquash(cmd *cobra.Command, args []string) (retErr error) {
	// Parse jitter early so invalid flags fail fast, but defer the sleep
	// until after workspace/attachment validation so no-op invocations
	// (wrong directory, no attached molecule) don't wait unnecessarily.
	var jitterMax time.Duration
	if moleculeJitter != "" {
		var err error
		jitterMax, err = time.ParseDuration(moleculeJitter)
		if err != nil {
			return fmt.Errorf("invalid --jitter duration %q: %w", moleculeJitter, err)
		}
		if jitterMax < 0 {
			return fmt.Errorf("--jitter must be non-negative, got %v", jitterMax)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}
	if townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace")
	}

	// Determine target agent
	var target string
	if len(args) > 0 {
		target = args[0]
	} else {
		// Auto-detect using env-aware role detection
		roleInfo, err := GetRoleWithContext(cwd, townRoot)
		if err != nil {
			return fmt.Errorf("detecting role: %w", err)
		}
		roleCtx := RoleContext{
			Role:     roleInfo.Role,
			Rig:      roleInfo.Rig,
			Polecat:  roleInfo.Polecat,
			TownRoot: townRoot,
			WorkDir:  cwd,
		}
		target = buildAgentIdentity(roleCtx)
		if target == "" {
			return fmt.Errorf("cannot determine agent identity (role: %s)", roleCtx.Role)
		}
	}

	// Find beads directory
	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Find agent's pinned bead (handoff bead)
	role := extractRoleFromIdentity(target)

	handoff, err := b.FindHandoffBead(role)
	if err != nil {
		return fmt.Errorf("finding handoff bead: %w", err)
	}
	if handoff == nil {
		return fmt.Errorf("no handoff bead found for %s (looked for %q with pinned status)", target, beads.HandoffBeadTitle(role))
	}

	// Check for attached molecule
	attachment := beads.ParseAttachmentFields(handoff)
	if attachment == nil || attachment.AttachedMolecule == "" {
		fmt.Printf("%s No molecule attached to %s - nothing to squash\n",
			style.Dim.Render("ℹ"), target)
		return nil
	}

	moleculeID := attachment.AttachedMolecule

	var doneSteps, totalSteps int
	defer func() {
		telemetry.RecordMolSquash(cmd.Context(), moleculeID, doneSteps, totalSteps, !moleculeNoDigest, retErr)
	}()

	// Apply jitter before acquiring any Dolt locks.
	// Multiple patrol agents (deacon, witness, refinery) squash concurrently at
	// cycle end, causing exclusive-lock contention. A random pre-sleep
	// desynchronizes them without changing semantics.
	if jitterMax > 0 {
		//nolint:gosec // weak RNG is fine for jitter
		sleep := time.Duration(rand.Int63n(int64(jitterMax)))
		fmt.Fprintf(os.Stderr, "jitter: sleeping %v before squash\n", sleep)
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(sleep):
		}
	}

	// Recursively close all descendant step issues before squashing
	// This prevents orphaned step issues from accumulating (gt-psj76.1)
	childrenClosed := closeDescendants(b, moleculeID)

	// Skip digest creation if --no-digest flag is set (gt-t2bjt).
	// Patrol molecules (deacon, witness, refinery) run frequently and their
	// digests pollute the database with thousands of low-value beads.
	if !moleculeNoDigest {
		progress, _ := getMoleculeProgressInfo(b, moleculeID)
		if progress != nil {
			doneSteps = progress.DoneSteps
			totalSteps = progress.TotalSteps
		}
		if _, err := createMoleculeDigest(b, moleculeID, target, moleculeSummary, progress); err != nil {
			return fmt.Errorf("creating digest: %w", err)
		}
	}

	// Detach the molecule from the handoff bead with audit logging
	detachReason := "molecule squashed (no digest)"
	if !moleculeNoDigest {
		detachReason = "molecule squashed"
	}
	_, err = b.DetachMoleculeWithAudit(handoff.ID, beads.DetachOptions{
		Operation: "squash",
		Agent:     target,
		Reason:    detachReason,
	})
	if err != nil {
		return fmt.Errorf("detaching molecule: %w", err)
	}

	// Close the molecule root after detach so the audit sees original status.
	// Without this, the wisp root stays in "hooked" status indefinitely,
	// causing patrol molecule leaks (issue #1828).
	rootClosed := true
	if closeErr := b.ForceCloseWithReason("squashed", moleculeID); closeErr != nil {
		style.PrintWarning("could not close molecule root %s: %v", moleculeID, closeErr)
		rootClosed = false
	}

	if moleculeJSON {
		result := map[string]interface{}{
			"squashed":        moleculeID,
			"from":            target,
			"handoff_id":      handoff.ID,
			"children_closed": childrenClosed,
			"digest_skipped":  moleculeNoDigest,
			"root_closed":     rootClosed,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	if moleculeNoDigest {
		fmt.Printf("%s Squashed molecule %s (no digest)\n",
			style.Bold.Render("📦"), moleculeID)
	} else {
		fmt.Printf("%s Squashed molecule %s\n",
			style.Bold.Render("📦"), moleculeID)
	}
	if childrenClosed > 0 {
		fmt.Printf("  Closed %d step issues\n", childrenClosed)
	}

	return nil
}

// createMoleculeDigest creates and closes an ephemeral "Digest: <moleculeID>"
// bead recording one molecule's execution. It is shared by `gt mol squash`
// and cleanupMoleculeOnHandoff so that BOTH the CLI squash path and the
// ordinary end-of-cycle `gt handoff` path (which patrol formulas actually
// use to end a cycle, per mol-refinery-patrol.formula.toml) leave a digest
// behind. Before this was shared, only the CLI path created digests, and
// nothing in the patrol lifecycle ever invoked it — `gt patrol digest`
// queried real data correctly (gt-1r3t) but found zero rows because zero
// digests existed to find (gt-5jin).
//
// summary and progress may be empty/nil; both are optional context.
func createMoleculeDigest(b *beads.Beads, moleculeID, actor, summary string, progress *MoleculeProgressInfo) (string, error) {
	digestTitle := fmt.Sprintf("Digest: %s", moleculeID)
	digestDesc := fmt.Sprintf(`Squashed molecule execution.

molecule: %s
agent: %s
squashed_at: %s
`, moleculeID, actor, time.Now().UTC().Format(time.RFC3339))

	if summary != "" {
		digestDesc += fmt.Sprintf("\n## Summary\n%s\n", summary)
	}

	if progress != nil {
		digestDesc += fmt.Sprintf(`
## Execution Summary
- Steps: %d/%d completed
- Status: %s
`, progress.DoneSteps, progress.TotalSteps, func() string {
			if progress.Complete {
				return "complete"
			}
			return "partial"
		}())
	}

	// Create the digest bead (ephemeral to avoid git pollution)
	// Per-cycle digests are aggregated daily by 'gt patrol digest'
	digestIssue, err := b.Create(beads.CreateOptions{
		Title:       digestTitle,
		Description: digestDesc,
		Labels:      []string{"gt:task"},
		Priority:    4, // P4 - backlog priority for digests
		Actor:       actor,
		Ephemeral:   true, // Don't export to JSONL - daily aggregation handles permanent record
		// DELIBERATELY NO WispType (gt-fqd5). A per-cycle digest reads like
		// compaction's "patrol" bucket, but that bucket's TTL is 24h and
		// these digests are closed on creation, so classifying them would
		// make gt compact delete each one 24h later — while the aggregation
		// that consumes them, `gt patrol digest --yesterday`, reads digests
		// that are 24-48h old by the time it runs. The typed version
		// destroys its own input.
		//
		// Since gt-ktvs an untyped wisp is skipped entirely, so leaving this
		// empty is what keeps the digests alive for aggregation.
	})
	if err != nil {
		return "", err
	}

	// Add the digest label (non-fatal: digest works without label)
	_ = b.Update(digestIssue.ID, beads.UpdateOptions{
		AddLabels: []string{"digest"},
	})

	// Close the digest immediately
	closedStatus := "closed"
	if err := b.Update(digestIssue.ID, beads.UpdateOptions{
		Status: &closedStatus,
	}); err != nil {
		style.PrintWarning("Created digest but couldn't close it: %v", err)
	}

	return digestIssue.ID, nil
}

// closeDescendantsPassLimit bounds the repeated close passes
// closeChildrenToFixedPoint makes over one parent's direct children. A pass that
// closes nothing already ends the loop, so this only guards against a close that
// reports progress without ever draining the set. Molecules have far fewer steps
// than this. Mirrors dogClosePassLimit in internal/daemon/dog_molecule.go.
const closeDescendantsPassLimit = 64

// closeDescendants recursively closes all descendant issues of a parent.
// Returns the count of issues closed. Logs warnings on errors but doesn't fail.
func closeDescendants(b *beads.Beads, parentID string) int {
	count, err := closeDescendantsImpl(b, parentID, false)
	if err != nil {
		style.PrintWarning("closing descendants of %s: %v", parentID, err)
	}
	return count
}

// forceCloseDescendants is like closeDescendants but uses force-close,
// which succeeds even for beads in invalid states. Returns the count of
// issues closed and any error encountered. Callers should check the error
// to avoid closing a parent while children survive (gt-7lx3).
func forceCloseDescendants(b *beads.Beads, parentID string) (int, error) {
	return closeDescendantsImpl(b, parentID, true)
}

func closeDescendantsImpl(b *beads.Beads, parentID string, force bool) (int, error) {
	// Enumerate children across BOTH the durable issues table and the wisps
	// table, at every priority. A bare b.List here saw neither: it defaults to
	// Ephemeral=false (so `bd list` never returns wisps) and to Priority=0 (so
	// it passes --priority=0 and drops every child at another priority).
	// Molecule step children are ephemeral wisps, so this listing came back
	// empty, closed nothing, and returned no error — the caller then closed the
	// root over still-open steps and reported success (gt-u2u).
	children, err := listChildrenAcrossTables(b, parentID)
	if err != nil {
		return 0, fmt.Errorf("listing children of %s: %w", parentID, err)
	}

	if len(children) == 0 {
		return 0, nil
	}

	// First, recursively close grandchildren. bd refuses to close an issue that
	// still has open children, so every subtree has to reach its own fixed point
	// before this level starts closing.
	totalClosed := 0
	var errs []error
	for _, child := range children {
		closed, childErr := closeDescendantsImpl(b, child.ID, force)
		totalClosed += closed
		if childErr != nil {
			errs = append(errs, childErr)
		}
	}

	// Then close direct children, repeating while the passes make progress.
	closed, stillOpen, closeErr := closeChildrenToFixedPoint(b, parentID, children, force)
	totalClosed += closed
	if closeErr != nil {
		errs = append(errs, closeErr)
	} else if len(stillOpen) > 0 {
		// Not an error bd reported — the passes simply stopped making progress.
		// Say so anyway: the caller is about to close the root, and this is the
		// only signal that it would be closing over still-open children.
		errs = append(errs, fmt.Errorf("%d child issue(s) of %s still open after close passes: %s",
			len(stillOpen), parentID, strings.Join(stillOpen, " ")))
	}

	if len(errs) > 0 {
		return totalClosed, errors.Join(errs...)
	}
	return totalClosed, nil
}

// closeChildrenToFixedPoint closes the direct children of parentID, repeating
// the close pass while it makes progress. Returns how many children went from
// open to closed, which ones are still open when the passes stop, and any close
// error that ended them.
//
// Molecule steps are SIBLINGS under one root, chained to each other by `blocks`
// edges, and bd correctly refuses to close a blocked issue. `bd close a b c`
// closes what it can and skips the rest, so one pass over the children in
// whatever order the listing returns closes only those whose blockers happen to
// already be closed — the rest are stranded. Recursing down parent-child does
// nothing for that: the ordering constraint runs sideways between siblings, not
// down the tree. This is the same arithmetic gt-g1q1 fixed in the daemon's dog
// molecule path; gt-3xmz is this second site, and it is the worse of the two
// because the daemon's root close is refused over open children (a loud stuck
// root) while gt done's succeeds (a silent orphan).
//
// So the pass repeats: each one closes at least the chain's current head, which
// unblocks the next link, and a pass that closes nothing means nothing closable
// is left. This needs no knowledge of the dependency graph and is bounded by the
// number of children.
//
// Progress is measured by re-reading the children, NOT by the close's exit
// status: `bd close` given several IDs exits 0 when ANY of them settled as
// closed. Trusting it is the second half of this bug — the old single pass
// counted len(idsToClose) as closed whenever the batch exited 0, so a partially
// refused close was reported as a complete one.
//
// Force is deliberately not used to push past the block guard when the caller
// did not ask for it. The guard is the only thing keeping steps from closing out
// of order; force-closing them destroys the ordering information and converts a
// visible stuck molecule into silent damage (the argument recorded on gt-g1q1
// and hq-vfr42).
func closeChildrenToFixedPoint(b *beads.Beads, parentID string, children []*beads.Issue, force bool) (closed int, stillOpen []string, err error) {
	open := openChildIDs(children)
	initialOpen := len(open)

	for pass := 0; pass < closeDescendantsPassLimit && len(open) > 0; pass++ {
		var closeErr error
		if force {
			closeErr = b.ForceCloseWithReason("burned: force-close descendants", open...)
		} else {
			closeErr = b.Close(open...)
		}

		remaining, listErr := listChildrenAcrossTables(b, parentID)
		if listErr != nil {
			return initialOpen - len(open), open, fmt.Errorf("re-listing children of %s: %w", parentID, listErr)
		}
		remainingOpen := openChildIDs(remaining)
		madeProgress := len(remainingOpen) < len(open)
		open = remainingOpen
		if !madeProgress {
			// An identical next pass cannot help: whatever is left is blocked by
			// something outside this set, or the close is failing outright. A
			// close refused mid-chain is expected on earlier passes and is not
			// reported — only the one that ended the passes is.
			if closeErr != nil {
				err = fmt.Errorf("closing children of %s: %w", parentID, closeErr)
			}
			break
		}
	}

	return initialOpen - len(open), open, err
}

// openChildIDs returns the IDs of the children that still need closing.
func openChildIDs(children []*beads.Issue) []string {
	var open []string
	for _, child := range children {
		if child.Status != "closed" {
			open = append(open, child.ID)
		}
	}
	return open
}
