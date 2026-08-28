package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/version"
)

var staleJSON bool
var staleQuiet bool
var staleOffline bool

var staleCmd = &cobra.Command{
	Use:     "stale",
	GroupID: GroupDiag,
	Short:   "Check if the gt binary is stale",
	Long: `Check if the gt binary was built from an older commit than a build ref.

This command compares the commit hash embedded in the binary at build time
with the build branch of the gastown repository (main/master/carry/*), read
from the remote so the answer does not depend on when a local checkout was
last updated. --offline skips that read; it can then only under-report
staleness, and reports "skipped" rather than "fresh" when it cannot tell.

Examples:
  gt stale              # Human-readable output
  gt stale --json       # Machine-readable JSON output
  gt stale --offline    # No network; local refs only
  gt stale --quiet      # Exit code only (0=stale, 1=fresh, 2=undetermined)

Exit codes:
  0 - Binary is stale
  1 - Binary is fresh (up to date)
  2 - Error or skipped (could not determine staleness)`,
	RunE: runStale,
}

func init() {
	staleCmd.Flags().BoolVar(&staleJSON, "json", false, "Output as JSON")
	staleCmd.Flags().BoolVarP(&staleQuiet, "quiet", "q", false, "Exit code only (0=stale, 1=fresh, 2=undetermined)")
	staleCmd.Flags().BoolVar(&staleOffline, "offline", false, "Do not read the build branch from the remote (local refs only)")
	rootCmd.AddCommand(staleCmd)
}

// StaleOutput represents the JSON output structure.
type StaleOutput struct {
	Stale         bool   `json:"stale"`
	Forward       bool   `json:"forward"`
	OnMainBranch  bool   `json:"on_main_branch"`
	SafeToRebuild bool   `json:"safe_to_rebuild"`
	BinaryCommit  string `json:"binary_commit"`
	RepoCommit    string `json:"repo_commit"`
	CompareRef    string `json:"compare_ref,omitempty"`
	CommitsBehind int    `json:"commits_behind,omitempty"`
	// Refreshed distinguishes "compared against the remote" from "compared
	// against whatever a local checkout happened to hold". Consumers that act
	// on stale:false should require it.
	Refreshed    bool   `json:"compare_ref_refreshed"`
	RefreshError string `json:"refresh_error,omitempty"`
	Skipped      bool   `json:"skipped,omitempty"`
	SkipReason   string `json:"skip_reason,omitempty"`
	Error        string `json:"error,omitempty"`
}

func runStale(cmd *cobra.Command, args []string) error {
	// Find the gastown repo
	repoRoot, err := version.GetRepoRoot()
	if err != nil {
		if staleQuiet {
			return NewSilentExit(2)
		}
		if staleJSON {
			return outputStaleJSON(StaleOutput{Error: err.Error()})
		}
		return fmt.Errorf("cannot find gastown repo: %w", err)
	}

	// Check staleness. The remote read is the default: this command is what the
	// rebuild-gt dog keys on, and a compare ref nothing updates made it a
	// no-op for hours at a time (gt-ympl).
	info := version.CheckStaleBinaryWithOptions(repoRoot, version.StaleOptions{
		RefreshRemote: !staleOffline,
	})

	// Handle errors
	if info.Error != nil {
		if staleQuiet {
			return NewSilentExit(2)
		}
		if staleJSON {
			return outputStaleJSON(StaleOutput{Error: info.Error.Error()})
		}
		return fmt.Errorf("staleness check failed: %w", info.Error)
	}

	// Quiet mode: just exit with appropriate code
	if staleQuiet {
		return NewSilentExit(staleQuietExitCode(info))
	}

	// Build output
	// SafeToRebuild requires: stale + forward-only + on a build branch.
	safeToRebuild := info.IsStale && info.IsForward && info.OnMainBranch
	output := StaleOutput{
		Stale:         info.IsStale,
		Forward:       info.IsForward,
		OnMainBranch:  info.OnMainBranch,
		SafeToRebuild: safeToRebuild,
		BinaryCommit:  info.BinaryCommit,
		RepoCommit:    info.RepoCommit,
		CompareRef:    info.CompareRef,
		CommitsBehind: info.CommitsBehind,
		Refreshed:     info.Refreshed,
		RefreshError:  info.RefreshError,
		Skipped:       info.Skipped,
		SkipReason:    info.SkipReason,
	}

	if staleJSON {
		return outputStaleJSON(output)
	}

	return outputStaleText(output)
}

func staleQuietExitCode(info *version.StaleBinaryInfo) int {
	if info.Skipped {
		return 2
	}
	if info.IsStale {
		return 0
	}
	return 1
}

func outputStaleJSON(output StaleOutput) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

func outputStaleText(output StaleOutput) error {
	if output.Skipped {
		fmt.Printf("%s Binary staleness check skipped\n", style.Dim.Render("•"))
		fmt.Printf("  %s\n", output.SkipReason)
		fmt.Printf("  Binary: %s\n", version.ShortCommit(output.BinaryCommit))
		return nil
	}
	if output.Stale {
		fmt.Printf("%s Binary is stale\n", style.Warning.Render("⚠"))
		fmt.Printf("  Binary:   %s\n", version.ShortCommit(output.BinaryCommit))
		fmt.Printf("  Build ref (%s): %s\n", output.CompareRef, version.ShortCommit(output.RepoCommit))
		if output.CommitsBehind > 0 {
			fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("(%d commits behind %s)", output.CommitsBehind, output.CompareRef)))
		}
		if !output.Forward {
			fmt.Printf("  %s %s is NOT a descendant of binary commit (diverged or older)\n", style.Error.Render("✗"), output.CompareRef)
		}
		if !output.OnMainBranch {
			fmt.Printf("  %s source worktree is not on a build branch (compared against %s)\n", style.Warning.Render("⚠"), output.CompareRef)
		}
		if output.SafeToRebuild {
			fmt.Printf("\n  Safe to rebuild: run 'make build && make install'\n")
		} else {
			fmt.Printf("\n  %s NOT safe for automated rebuild (forward=%v, build_branch=%v)\n",
				style.Error.Render("✗"), output.Forward, output.OnMainBranch)
		}
	} else {
		fmt.Printf("%s Binary is fresh\n", style.Success.Render("✓"))
		fmt.Printf("  Commit: %s\n", version.ShortCommit(output.BinaryCommit))
		if output.CompareRef != "" {
			fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("(compared against %s%s)",
				output.CompareRef, staleRefProvenance(output))))
		}
	}
	return nil
}

// staleRefProvenance names where the compare ref's commit came from, because
// "fresh" against a local checkout nothing updates and "fresh" against the
// remote are different claims and only one of them is evidence.
func staleRefProvenance(output StaleOutput) string {
	if output.Refreshed {
		return ", read from the remote"
	}
	return ", from a local ref — not read from the remote"
}
