package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/style"
)

var (
	pruneBranchesDryRun  bool
	pruneBranchesPattern string
)

var pruneBranchesCmd = &cobra.Command{
	Use:     "prune-branches",
	GroupID: GroupWork,
	Short:   "Remove stale local polecat tracking branches",
	Long: `Remove local branches that were created when tracking remote polecat branches.

When polecats push branches to origin, other clones create local tracking
branches via git fetch. After the remote branch is deleted (post-merge),
git fetch --prune removes the remote tracking ref but the local branch
persists indefinitely.

This command finds and removes local branches matching the pattern (default:
polecat/*) that are either:
  - Fully merged to the default branch (main)
  - Have no corresponding remote tracking branch (remote was deleted)

Safety: Uses git branch -d (not -D) so only fully-merged branches are deleted.
Never deletes the current branch or the default branch.

Examples:
  gt prune-branches              # Clean up stale polecat branches
  gt prune-branches --dry-run    # Show what would be deleted
  gt prune-branches --pattern "feature/*"  # Custom pattern`,
	RunE: runPruneBranches,
}

func init() {
	pruneBranchesCmd.Flags().BoolVar(&pruneBranchesDryRun, "dry-run", false, "Show what would be deleted without deleting")
	pruneBranchesCmd.Flags().StringVar(&pruneBranchesPattern, "pattern", "polecat/*", "Branch name pattern to match")

	rootCmd.AddCommand(pruneBranchesCmd)
}

func runPruneBranches(cmd *cobra.Command, args []string) error {
	g := gitpkg.NewGit(".")
	if !g.IsRepo() {
		return fmt.Errorf("not a git repository")
	}

	// Run fetch --prune first to clean up stale remote tracking refs
	if err := g.FetchPrune("origin"); err != nil {
		// Non-fatal: we can still prune based on current state
		fmt.Printf("%s Warning: git fetch --prune failed: %v\n", style.Warning.Render("⚠"), err)
	}

	report, err := g.PruneStaleBranchesReport(pruneBranchesPattern, pruneBranchesDryRun)
	if err != nil {
		return fmt.Errorf("pruning branches: %w", err)
	}

	if report.Candidates() == 0 {
		fmt.Printf("%s No stale branches found matching %q\n", style.Bold.Render("✓"), pruneBranchesPattern)
		return nil
	}

	if pruneBranchesDryRun {
		fmt.Printf("%s Would prune %d branch(es):\n\n", style.Warning.Render("⚠"), len(report.Pruned))
	} else {
		fmt.Printf("%s Pruned %d branch(es):\n\n", style.Bold.Render("✓"), len(report.Pruned))
	}

	for _, b := range report.Pruned {
		fmt.Printf("  %s %s (%s)\n",
			style.Dim.Render("•"),
			b.Name,
			style.Dim.Render(pruneReasonText(b.Reason)))
	}
	fmt.Println()

	if len(report.Skipped) > 0 {
		fmt.Printf("%s Kept %d stale branch(es) that could not be deleted:\n\n",
			style.Warning.Render("⚠"), len(report.Skipped))
		for _, b := range report.Skipped {
			fmt.Printf("  %s %s (%s): %s\n",
				style.Dim.Render("•"),
				b.Name,
				style.Dim.Render(pruneReasonText(b.Reason)),
				b.Detail)
		}
		fmt.Println()
	}

	return nil
}

// pruneReasonText renders a PruneReport reason code for humans.
func pruneReasonText(reason string) string {
	switch reason {
	case "merged":
		return "merged to main"
	case "no-remote":
		return "remote branch deleted"
	case "no-remote-merged":
		return "remote deleted, merged to main"
	}
	return reason
}
