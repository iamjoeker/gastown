package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/convoy"
	"github.com/steveyegge/gastown/internal/workspace"

	"github.com/spf13/cobra"
)

var closeCmd = &cobra.Command{
	Use:     "close [bead-id...]",
	GroupID: GroupWork,
	Short:   "Close one or more beads",
	Long: `Close one or more beads (wrapper for 'bd close').

This is a convenience command that passes through to 'bd close' with
all arguments and flags preserved.

When an issue is closed, any convoys tracking it are checked for
completion. If all tracked issues in a convoy are closed, the convoy
is auto-closed.

Examples:
  gt close gt-abc              # Close bead gt-abc
  gt close gt-abc gt-def       # Close multiple beads
  gt close --reason "Done"     # Close with reason
  gt close --comment "Done"    # Same as --reason (alias)
  gt close --force             # Force close pinned beads
  gt close gt-abc --cascade    # Close gt-abc and all its children
  gt close -C ~/gt hq-abc      # Close from a specific directory

Unlike 'bd -C', which only sets BEADS_DIR and leaves the working directory
where it was, 'gt close -C <dir>' runs bd in <dir>. An explicit -C overrides
the directory the bead's prefix would otherwise route to.`,
	DisableFlagParsing: true, // Pass all flags through to bd close
	RunE:               runClose,
}

func init() {
	rootCmd.AddCommand(closeCmd)
}

func runClose(cmd *cobra.Command, args []string) error {
	// Handle --help since DisableFlagParsing bypasses Cobra's help handling
	if helped, err := checkHelpFlag(cmd, args); helped || err != nil {
		return err
	}

	// Extract --cascade flag before passing to bd (gt-only flag)
	cascade, filteredArgs := extractCascadeFlag(args)

	// Resolve -C/--directory here instead of forwarding it to bd (gt-d37).
	changeDir, filteredArgs, err := extractChangeDir(filteredArgs)
	if err != nil {
		return err
	}
	if changeDir != "" {
		if changeDir, err = resolveCloseChangeDir(changeDir); err != nil {
			return err
		}
	}

	// Convert --comment to --reason (alias support)
	convertedArgs := make([]string, len(filteredArgs))
	for i, arg := range filteredArgs {
		if arg == "--comment" {
			convertedArgs[i] = "--reason"
		} else if strings.HasPrefix(arg, "--comment=") {
			convertedArgs[i] = "--reason=" + strings.TrimPrefix(arg, "--comment=")
		} else {
			convertedArgs[i] = arg
		}
	}

	// If cascade, close children first (depth-first)
	if cascade {
		beadIDs := extractBeadIDs(filteredArgs)
		visited := make(map[string]bool)
		for _, id := range beadIDs {
			if err := closeChildren(id, changeDir, visited, 0); err != nil {
				return fmt.Errorf("cascade close failed for children of %s: %w", id, err)
			}
		}
	}

	// Build bd close command with all args passed through.
	// Route to the correct rig database via prefix resolution — bd no longer
	// does cross-rig routing internally (removed in beads v0.62). We resolve
	// the bead's prefix to the owning rig's directory and strip BEADS_DIR so
	// bd discovers the database from the working directory.
	bdArgs := append([]string{"close"}, convertedArgs...)
	bdCmd := exec.Command("bd", bdArgs...)
	bdCmd.Stdin = os.Stdin
	bdCmd.Stdout = os.Stdout
	bdCmd.Stderr = os.Stderr
	if dir := closeBeadDir(changeDir, extractBeadIDs(convertedArgs)); dir != "" {
		bdCmd.Dir = dir
		bdCmd.Env = filterEnvKey(os.Environ(), "BEADS_DIR")
	}
	if err := bdCmd.Run(); err != nil {
		return err
	}

	// After successful close, check convoy completion for each closed issue.
	// This is best-effort: failures are silently ignored since the daemon's
	// event polling and deacon patrol serve as backup mechanisms.
	beadIDs := extractBeadIDs(filteredArgs)
	if len(beadIDs) > 0 {
		checkConvoyCompletion(beadIDs)
	}

	return nil
}

// extractCascadeFlag removes --cascade from args and returns whether it was present.
func extractCascadeFlag(args []string) (bool, []string) {
	cascade := false
	var filtered []string
	for _, arg := range args {
		if arg == "--cascade" {
			cascade = true
		} else {
			filtered = append(filtered, arg)
		}
	}
	return cascade, filtered
}

// childBead represents a child bead from bd children --json output.
type childBead struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// maxCascadeDepth is the maximum recursion depth for cascade close.
// Prevents runaway recursion from dependency cycles or deeply nested hierarchies.
const maxCascadeDepth = 50

// closeChildren recursively closes all open children of a bead (depth-first).
// visited tracks already-processed IDs to prevent cycles. depth guards against
// excessively nested hierarchies. changeDir, when non-empty, is the directory
// the user named with -C; it overrides prefix-based routing for every bd call
// in the cascade, so the whole cascade lands in one database.
func closeChildren(parentID, changeDir string, visited map[string]bool, depth int) error {
	if depth > maxCascadeDepth {
		return fmt.Errorf("cascade depth limit (%d) exceeded at %s — possible cycle", maxCascadeDepth, parentID)
	}
	if visited[parentID] {
		return nil // already processed — cycle detected, skip silently
	}
	visited[parentID] = true

	// Query children via bd children --json.
	// Route to the correct rig database via prefix resolution.
	childCmd := exec.Command("bd", "children", parentID, "--json")
	if dir := closeBeadDir(changeDir, []string{parentID}); dir != "" {
		childCmd.Dir = dir
		childCmd.Env = filterEnvKey(os.Environ(), "BEADS_DIR")
	}
	out, err := childCmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 0 {
			fmt.Fprintf(os.Stderr, "Warning: bd children %s failed: %v\n", parentID, err)
		}
		return nil
	}

	var children []childBead
	if err := json.Unmarshal(out, &children); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to parse children of %s: %v\n", parentID, err)
		return nil
	}

	if len(children) == 0 {
		return nil
	}

	// Collect open children and recursively close their children first (depth-first)
	var childIDs []string
	for _, child := range children {
		if child.Status == "closed" {
			continue
		}
		if err := closeChildren(child.ID, changeDir, visited, depth+1); err != nil {
			return err
		}
		childIDs = append(childIDs, child.ID)
	}

	if len(childIDs) == 0 {
		return nil
	}

	reason := fmt.Sprintf("Parent %s closed (cascade)", parentID)

	closeArgs := []string{"close"}
	closeArgs = append(closeArgs, childIDs...)
	closeArgs = append(closeArgs, "--reason", reason, "--force")

	fmt.Fprintf(os.Stderr, "Cascade: closing %d children of %s\n", len(childIDs), parentID)

	closeBd := exec.Command("bd", closeArgs...)
	closeBd.Stdout = os.Stdout
	closeBd.Stderr = os.Stderr
	if dir := closeBeadDir(changeDir, []string{parentID}); dir != "" {
		closeBd.Dir = dir
		closeBd.Env = filterEnvKey(os.Environ(), "BEADS_DIR")
	}
	return closeBd.Run()
}

// bdChangeDirFlags are Beads' -C / --directory globals, which name a directory.
var bdChangeDirFlags = map[string]bool{"-C": true, "--directory": true}

// bdCloseValueFlags are the flags gt close may see that consume a following
// argument. DisableFlagParsing means we identify them by hand, and knowing them
// is what keeps a flag's value from being re-read as a flag or a bead ID.
var bdCloseValueFlags = map[string]bool{
	"--reason": true, "-r": true,
	"--session":          true,
	"--actor":            true,
	"--db":               true,
	"--dolt-auto-commit": true,
	// The --comment alias, seen before conversion to --reason.
	"--comment": true,
	// bd globals may appear anywhere in argv, so a -C/--directory value left in
	// args must not be mistaken for a bead ID (gt-d37).
	"-C": true, "--directory": true,
}

// extractChangeDir removes Beads' -C/--directory global from args and returns
// the directory it named ("" when absent). The last occurrence wins, matching
// pflag.
//
// Beads documents -C as "change to this directory before running the command
// (like git -C)", but it never chdirs — it only sets BEADS_DIR. Beads then
// resolves a write target from two inputs: the store opened via BEADS_DIR, and
// the maintainer/contributor role detected from the process working directory,
// which -C does not move. Forwarding -C to `bd close` would set one input and
// leave the other wherever gt happened to be, so the close routes on a pair
// matching no directory the user named (gt-d37).
//
// gt close already runs bd from a resolved directory with BEADS_DIR stripped,
// so it can honour -C the way the help text describes: by running bd there.
func extractChangeDir(args []string) (string, []string, error) {
	dir := ""
	found := false
	var kept []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		switch {
		case bdChangeDirFlags[name] && hasValue:
			dir, found = value, true
		case bdChangeDirFlags[arg]:
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("flag needs an argument: %s", arg)
			}
			dir, found = args[i+1], true
			i++
		case strings.HasPrefix(arg, "-C") && !strings.HasPrefix(arg, "--") && len(arg) > 2:
			// pflag also accepts a shorthand value with no separator: -C/path.
			dir, found = arg[2:], true
		case bdCloseValueFlags[arg]:
			// Carry another flag's value through untouched; a --reason of
			// "-C/tmp" is a reason, not a directory.
			kept = append(kept, arg)
			if i+1 < len(args) {
				kept = append(kept, args[i+1])
				i++
			}
		default:
			kept = append(kept, arg)
		}
	}
	if found && dir == "" {
		return "", nil, fmt.Errorf("flag needs an argument: -C")
	}
	return dir, kept, nil
}

// resolveCloseChangeDir turns a -C argument into an absolute directory. A
// directory that does not exist is an error rather than a silent fallback to
// prefix routing: gt-d37 is a case study in target selectors that appear to be
// honoured and are not.
func resolveCloseChangeDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("-C %q: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("-C %q: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("-C %q: not a directory", dir)
	}
	return abs, nil
}

// closeBeadDir picks the working directory for a bd subprocess. An explicit
// -C wins; otherwise the first bead's prefix routes it. Returns "" when neither
// yields a usable directory, leaving bd to discover the database from gt's own
// working directory and inherited BEADS_DIR.
func closeBeadDir(changeDir string, beadIDs []string) string {
	if changeDir != "" {
		return changeDir
	}
	if len(beadIDs) == 0 {
		return ""
	}
	if dir := resolveBeadDir(beadIDs[0]); dir != "" && dir != "." {
		return dir
	}
	return ""
}

// extractBeadIDs extracts bead IDs from raw args, skipping flags and flag values.
// Since DisableFlagParsing is true, we get unparsed args and must identify flags manually.
func extractBeadIDs(args []string) []string {
	var ids []string
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(arg, "-") {
			// Check for --flag=value (consumed in one token)
			if strings.Contains(arg, "=") {
				continue
			}
			// Check if this flag takes a value argument
			if bdCloseValueFlags[arg] {
				skipNext = true
			}
			continue
		}
		ids = append(ids, arg)
	}
	return ids
}

// checkConvoyCompletion checks if any closed issues are tracked by convoys
// and triggers convoy completion checks. This implements the ZFC principle:
// the closure event propagates at the source (bd close) rather than relying
// solely on daemon event polling.
//
// This is best-effort. If the workspace or hq store is unavailable, the
// daemon's event polling and deacon patrol serve as backup mechanisms.
func checkConvoyCompletion(beadIDs []string) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return
	}

	hqBeadsDir := filepath.Join(townRoot, ".beads")
	ctx := context.Background()

	store, err := beadsdk.Open(ctx, hqBeadsDir)
	if err != nil {
		return
	}
	defer func() { _ = store.Close() }()

	gtPath, err := os.Executable()
	if err != nil {
		gtPath, _ = exec.LookPath("gt")
		if gtPath == "" {
			return
		}
	}

	for _, beadID := range beadIDs {
		convoy.CheckConvoysForIssue(ctx, store, townRoot, beadID, "Close", nil, gtPath, nil)
	}
}
