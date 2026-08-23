package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/refinery"
)

// blockerPriorityFlags for gt mq blocker-priority.
var mqBlockerPriorityJSON bool

// BlockerPriorityOutput is the JSON output structure for
// gt mq blocker-priority.
type BlockerPriorityOutput struct {
	// Priority is the priority the blocker must be created at.
	Priority int `json:"priority"`
	// Blocked lists the items the blocker gates, with their own priorities.
	Blocked []BlockedItem `json:"blocked"`
	// MostUrgent is the priority of the most urgent blocked item — the input
	// the rule is derived from.
	MostUrgent int `json:"most_urgent"`
}

// BlockedItem is one item a blocker gates.
type BlockedItem struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
	Title    string `json:"title,omitempty"`
}

var mqBlockerPriorityCmd = &cobra.Command{
	Use:   "blocker-priority <blocked-id>...",
	Short: "Print the priority a blocker task must be created at",
	Long: `Print the priority a task must carry in order to block the given items.

A blocker that ranks below what it blocks can never be scheduled: it queues
behind every higher-priority item in the rig, including the very item it is
holding up, so the queue deadlocks by construction (gt-ofb0).

The rule is one band more urgent than the most urgent item blocked, clamped
at P0. It is relative, not absolute — a P3 item's blocker is P2, not P0.

Use it wherever a blocker bead is filed by hand, so the priority is derived
rather than guessed:

  bd create --type=task --priority=$(gt mq blocker-priority gt-wisp-faoi) ...
  bd update gt-wisp-faoi --add-blockedby "$TASK_ID"

Examples:
  gt mq blocker-priority gt-wisp-faoi          # one blocked MR
  gt mq blocker-priority gt-epic1 gt-epic2     # derives from the most urgent
  gt mq blocker-priority gt-wisp-faoi --json   # show the derivation`,
	Args: cobra.MinimumNArgs(1),
	RunE: runMqBlockerPriority,
}

func runMqBlockerPriority(cmd *cobra.Command, args []string) error {
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	bd := beads.New(workDir)

	out, err := blockerPriorityFor(bd.Show, args)
	if err != nil {
		return err
	}
	return writeBlockerPriority(cmd.OutOrStdout(), out, mqBlockerPriorityJSON)
}

// blockerPriorityFor resolves every blocked ID and derives the blocker's
// priority. show is injected so this is testable without a beads store.
//
// Every ID must resolve. A blocker filed at the wrong priority is exactly the
// failure this command exists to prevent, so a lookup that silently skipped an
// unreadable bead could hand back a number that is too weak — the same defect
// wearing a helper's clothes.
func blockerPriorityFor(show func(string) (*beads.Issue, error), ids []string) (*BlockerPriorityOutput, error) {
	out := &BlockerPriorityOutput{}
	var priorities []int

	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, fmt.Errorf("empty blocked-item ID")
		}
		issue, err := show(id)
		if err != nil {
			return nil, fmt.Errorf("reading blocked item %s: %w", id, err)
		}
		if issue == nil {
			return nil, fmt.Errorf("reading blocked item %s: not found", id)
		}
		out.Blocked = append(out.Blocked, BlockedItem{
			ID:       issue.ID,
			Priority: issue.Priority,
			Title:    issue.Title,
		})
		priorities = append(priorities, issue.Priority)
	}

	out.MostUrgent = refinery.MostUrgentPriority(priorities...)
	out.Priority = refinery.BlockerPriority(out.MostUrgent)
	return out, nil
}

func writeBlockerPriority(w io.Writer, out *BlockerPriorityOutput, asJSON bool) error {
	if asJSON {
		encoded, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding JSON: %w", err)
		}
		_, err = fmt.Fprintln(w, string(encoded))
		return err
	}
	// Bare number on stdout: this is built to be substituted straight into a
	// --priority flag, so anything else on the line would corrupt the command
	// that consumes it.
	_, err := fmt.Fprintln(w, out.Priority)
	return err
}
