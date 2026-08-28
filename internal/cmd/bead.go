package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var beadCmd = &cobra.Command{
	Use:     "bead",
	Aliases: []string{"bd"},
	GroupID: GroupWork,
	Short:   "Bead management utilities",
	RunE:    requireSubcommand,
	Long: `Utilities for managing beads across repositories.

Provides operations that span multiple beads repositories, such as
moving beads between repos and viewing beads by ID with automatic
prefix-based routing.

Subcommands:
  move    Move a bead from one repository to another
  show    Show details of a bead (routes by prefix)
  read    Alias for show`,
}

var beadMoveCmd = &cobra.Command{
	Use:   "move <bead-id> <target-prefix>",
	Short: "Move a bead to a different repository",
	Long: `Move a bead from one repository to another.

This creates a copy of the bead in the target repository (with the new prefix)
and closes the source bead with a reference to the new location.

The target prefix determines which repository receives the bead.
Common prefixes: gt- (gastown), bd- (beads), hq- (headquarters)

Examples:
  gt bead move gt-abc123 bd-     # Move gt-abc123 to beads repo as bd-*
  gt bead move hq-xyz bd-        # Move hq-xyz to beads repo
  gt bead move bd-123 gt-        # Move bd-123 to gastown repo`,
	Args: cobra.ExactArgs(2),
	RunE: runBeadMove,
}

var beadMoveDryRun bool

var beadShowCmd = &cobra.Command{
	Use:   "show <bead-id> [flags]",
	Short: "Show details of a bead",
	Long: `Displays the full details of a bead by ID.

This is an alias for 'gt show'. All bd show flags are supported.

Examples:
  gt bead show gt-abc123          # Show a gastown issue
  gt bead show hq-xyz789          # Show a town-level bead
  gt bead show bd-def456          # Show a beads issue
  gt bead show gt-abc123 --json   # Output as JSON`,
	DisableFlagParsing: true, // Pass all flags through to bd show
	RunE: func(cmd *cobra.Command, args []string) error {
		return runShow(cmd, args)
	},
}

var beadReadCmd = &cobra.Command{
	Use:   "read <bead-id> [flags]",
	Short: "Show details of a bead (alias for 'show')",
	Long: `Displays the full details of a bead by ID.

This is an alias for 'gt bead show'. All bd show flags are supported.

Examples:
  gt bead read gt-abc123          # Show a gastown issue
  gt bead read hq-xyz789          # Show a town-level bead
  gt bead read bd-def456          # Show a beads issue
  gt bead read gt-abc123 --json   # Output as JSON`,
	DisableFlagParsing: true, // Pass all flags through to bd show
	RunE: func(cmd *cobra.Command, args []string) error {
		return runShow(cmd, args)
	},
}

func init() {
	beadMoveCmd.Flags().BoolVarP(&beadMoveDryRun, "dry-run", "n", false, "Show what would be done")
	beadCmd.AddCommand(beadMoveCmd)
	beadCmd.AddCommand(beadShowCmd)
	beadCmd.AddCommand(beadReadCmd)
	rootCmd.AddCommand(beadCmd)
}

// moveBeadInfo holds the essential fields we need to copy when moving beads
type moveBeadInfo struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Type        string   `json:"issue_type"`
	Priority    int      `json:"priority"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	Assignee    string   `json:"assignee"`
	Status      string   `json:"status"`
}

// beadMoveTarget names the store a move will file its copy into.
//
// bd has no flag that targets a database: `bd create` files into whatever store
// its working directory resolves to. The working directory IS the targeting
// mechanism, so a move that cannot resolve one has no target at all (gt-ecff).
type beadMoveTarget struct {
	townRoot string
	// workDir is the directory bd must run in for the copy to land in the
	// store that owns targetPrefix.
	workDir string
}

// resolveBeadMoveTarget resolves the target store for a prefix, or explains why
// it cannot. An unroutable prefix is an error rather than a fallback: falling
// back to the caller's cwd would file the copy in a silently wrong database,
// which is worse than refusing the move.
func resolveBeadMoveTarget(targetPrefix string) (beadMoveTarget, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return beadMoveTarget{}, fmt.Errorf("resolving town root for prefix %s: %w", targetPrefix, err)
	}
	return resolveBeadMoveTargetIn(townRoot, targetPrefix)
}

// resolveBeadMoveTargetIn is resolveBeadMoveTarget with the town root supplied.
func resolveBeadMoveTargetIn(townRoot, targetPrefix string) (beadMoveTarget, error) {
	if targetPrefix == "" || targetPrefix == "-" {
		return beadMoveTarget{}, fmt.Errorf("target prefix is required (e.g. gt-, bd-, hq-)")
	}
	if beads.GetRigPathForPrefix(townRoot, targetPrefix) == "" {
		return beadMoveTarget{}, fmt.Errorf("no route for prefix %q in %s: bd files by working directory, so an unrouted prefix names no database",
			targetPrefix, filepath.Join(townRoot, ".beads", "routes.jsonl"))
	}
	return beadMoveTarget{
		townRoot: townRoot,
		workDir:  resolveBeadDirFromTownRoot(townRoot, targetPrefix),
	}, nil
}

// beadMoveCreateArgs builds the `bd create` argv for the copy.
//
// Every flag here must be one `bd create` actually accepts. There is no
// database-targeting flag among them — `--prefix` is a `bd init` flag, and
// passing it made every move fail (gt-ecff). Targeting is done by the working
// directory the command runs in, not by argv.
func beadMoveCreateArgs(source moveBeadInfo) []string {
	args := []string{
		"create",
		"--title=" + source.Title,
		"--type", source.Type,
		"--priority", fmt.Sprintf("%d", source.Priority),
		"--silent", // Only output the ID
	}
	if source.Description != "" {
		args = append(args, "--description", source.Description)
	}
	if source.Assignee != "" {
		args = append(args, "--assignee", source.Assignee)
	}
	for _, label := range source.Labels {
		args = append(args, "--label", label)
	}
	return args
}

func runBeadMove(cmd *cobra.Command, args []string) error {
	sourceID := args[0]
	targetPrefix := args[1]

	// Normalize prefix (ensure it ends with -)
	if !strings.HasSuffix(targetPrefix, "-") {
		targetPrefix = targetPrefix + "-"
	}

	// Resolve the target store before anything reports the move as possible.
	// The dry run below must not certify a path the real move cannot take.
	target, err := resolveBeadMoveTarget(targetPrefix)
	if err != nil {
		return err
	}

	// Get source bead details — resolve rig directory from prefix so that
	// rig-prefixed beads are found in their rig database (GH#2126).
	sourceDir := resolveBeadDir(sourceID)
	output, err := BdCmd("show", sourceID, "--json").
		Dir(sourceDir).
		StripBeadsDir().
		Output()
	if err != nil {
		return fmt.Errorf("getting bead %s: %w", sourceID, err)
	}

	// bd show --json returns an array
	var sources []moveBeadInfo
	if err := json.Unmarshal(output, &sources); err != nil {
		return fmt.Errorf("parsing bead data: %w", err)
	}
	if len(sources) == 0 {
		return fmt.Errorf("bead %s not found", sourceID)
	}
	source := sources[0]

	// Don't move closed beads
	if source.Status == "closed" {
		return fmt.Errorf("cannot move closed bead %s", sourceID)
	}

	fmt.Printf("%s Moving %s to %s...\n", style.Bold.Render("→"), sourceID, targetPrefix)
	fmt.Printf("  Title: %s\n", source.Title)
	fmt.Printf("  Type: %s\n", source.Type)
	fmt.Printf("  Source store: %s\n", sourceDir)
	fmt.Printf("  Target store: %s\n", target.workDir)

	// Guard against flag-like titles propagating during move (gt-e0kx5)
	if beads.IsFlagLikeTitle(source.Title) {
		return fmt.Errorf("refusing to move bead: title %q looks like a CLI flag", source.Title)
	}

	if beadMoveDryRun {
		fmt.Printf("\nDry run - would:\n")
		fmt.Printf("  1. Create a copy of %s in %s (prefix %s)\n", sourceID, target.workDir, targetPrefix)
		fmt.Printf("  2. Close %s in %s with a reference to the new bead\n", sourceID, sourceDir)
		return nil
	}

	// Create the new bead in the target store.
	newIDBytes, err := BdCmd(beadMoveCreateArgs(source)...).
		Dir(target.workDir).
		StripBeadsDir().
		WithAutoCommit().
		Output()
	if err != nil {
		return fmt.Errorf("creating new bead in %s: %w", target.workDir, err)
	}
	newID := strings.TrimSpace(string(newIDBytes))
	if newID == "" {
		return fmt.Errorf("bd create in %s reported success but returned no bead ID", target.workDir)
	}

	// Confirm the copy landed where it was aimed. Routing by working directory
	// fails silently — a copy filed in the wrong store still returns an ID and a
	// zero exit — so check where the new ID routes back to (gt-ecff).
	if landed := resolveBeadDirFromTownRoot(target.townRoot, newID); landed != target.workDir {
		return fmt.Errorf("created %s, but it landed in %s instead of %s (prefix %s); %s was left open and unchanged",
			newID, landed, target.workDir, targetPrefix, sourceID)
	}

	fmt.Printf("%s Created %s\n", style.Bold.Render("✓"), newID)

	// Close the source bead with reference
	closeReason := fmt.Sprintf("Moved to %s", newID)
	if err := BdCmd("close", sourceID, "--reason", closeReason).
		Dir(sourceDir).
		StripBeadsDir().
		WithAutoCommit().
		Run(); err != nil {
		// Clean up the new bead since we couldn't close the source
		fmt.Fprintf(os.Stderr, "Warning: failed to close source bead: %v\n", err)
		cleanupErr := BdCmd("close", newID, "--reason", "Cleanup: source bead close failed during move").
			Dir(target.workDir).
			StripBeadsDir().
			WithAutoCommit().
			Run()
		if cleanupErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: also failed to clean up new bead %s: %v\n", newID, cleanupErr)
			fmt.Fprintf(os.Stderr, "Both %s and %s remain open - manual cleanup needed\n", sourceID, newID)
		} else {
			fmt.Fprintf(os.Stderr, "Cleaned up new bead %s\n", newID)
		}
		return err
	}

	fmt.Printf("%s Closed %s (moved to %s)\n", style.Bold.Render("✓"), sourceID, newID)
	fmt.Printf("\nBead moved: %s → %s\n", sourceID, newID)

	return nil
}
