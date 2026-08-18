package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/steveyegge/gastown/internal/workspace"
)

func init() {
	townCmd.AddCommand(townRootCmd)
}

// townRootCmd prints the town root. It exists primarily for shell scripts
// (plugins, hooks) that need the workspace root without a Go dependency.
//
// It was previously absent, and three plugins already called `gt town root`
// as a fallback for an unset GT_TOWN_ROOT. Cobra answered an unknown
// subcommand by printing `gt town`'s help to STDOUT and exiting 0, so
// `TOWN_ROOT=$(gt town root 2>/dev/null)` assigned several lines of help text
// to a path variable and every derived path was nonsense — silently (gt-cr2).
// The command now exists, and `gt town` rejects unknown subcommands.
var townRootCmd = &cobra.Command{
	Use:   "root",
	Short: "Print the town root directory",
	Long: `Print the absolute path of the Gas Town workspace root.

Resolution order:
  1. Walk up from the current directory looking for mayor/town.json
     (falling back to a mayor/ directory)
  2. $GT_TOWN_ROOT, then $GT_ROOT, if either points at a real workspace

Prints the path on stdout with a trailing newline. When no workspace can be
resolved, prints nothing on stdout, reports the error on stderr, and exits
non-zero, so scripts can rely on:

    TOWN_ROOT=$(gt town root) || exit 1`,
	Args: cobra.NoArgs,
	// Operational failure ("not in a workspace") is not a usage error; printing
	// the usage block would bury the message for scripts and agents alike.
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := workspace.FindFromCwdOrError()
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), root)
		return nil
	},
}
