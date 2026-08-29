package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/holds"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	holdAddThreshold        string
	holdAddReleaseCondition string
	holdAddBy               string
	holdReleaseReason       string
	holdReleaseBy           string
	holdListJSON            bool
	holdListAll             bool
)

var holdCmd = &cobra.Command{
	Use:     "hold",
	GroupID: GroupWork,
	Short:   "Manage durable operational holds (standing 'do not X' directives)",
	Long: `Manage durable, queryable operational holds — standing directives such as
"do not restart polecats until cost review" that must survive session death
and be enumerable in one command before an agent takes a restricted action.

Holds are NOT scheduler pause (gt scheduler pause) — that covers only
scheduler dispatch. A hold can name any scope (e.g. "session restart",
"polecat dispatch", "spend").

Subcommands:
  gt hold add       # Record a new hold
  gt hold list      # List active holds (run this before restarting sessions or spending)
  gt hold show      # Show one hold's full detail
  gt hold release   # Release a hold`,
	RunE: requireSubcommand,
}

var holdAddCmd = &cobra.Command{
	Use:   "add <scope> <reason>",
	Short: "Record a new operational hold",
	Long: `Record a new durable operational hold.

Example:
  gt hold add "session restart" "cost review pending after mass-death escalation" \
    --release-condition "mayor approves after reviewing spend"`,
	Args: cobra.ExactArgs(2),
	RunE: runHoldAdd,
}

var holdListCmd = &cobra.Command{
	Use:   "list",
	Short: "List operational holds (active only, unless --all)",
	RunE:  runHoldList,
}

var holdShowCmd = &cobra.Command{
	Use:   "show <hold-id>",
	Short: "Show full detail for one hold",
	Args:  cobra.ExactArgs(1),
	RunE:  runHoldShow,
}

var holdReleaseCmd = &cobra.Command{
	Use:   "release <hold-id>",
	Short: "Release a hold",
	Args:  cobra.ExactArgs(1),
	RunE:  runHoldRelease,
}

func init() {
	holdAddCmd.Flags().StringVar(&holdAddThreshold, "threshold", "", "Condition that triggers the hold, e.g. 'cost > $50'")
	holdAddCmd.Flags().StringVar(&holdAddReleaseCondition, "release-condition", "", "What must happen before this hold can be released")
	holdAddCmd.Flags().StringVar(&holdAddBy, "by", "", "Who is setting this hold (default: detected actor)")

	holdListCmd.Flags().BoolVar(&holdListJSON, "json", false, "Output as JSON")
	holdListCmd.Flags().BoolVar(&holdListAll, "all", false, "Include released holds")

	holdReleaseCmd.Flags().StringVar(&holdReleaseReason, "reason", "", "Why this hold is being released")
	holdReleaseCmd.Flags().StringVar(&holdReleaseBy, "by", "", "Who is releasing this hold (default: detected actor)")

	holdCmd.AddCommand(holdAddCmd)
	holdCmd.AddCommand(holdListCmd)
	holdCmd.AddCommand(holdShowCmd)
	holdCmd.AddCommand(holdReleaseCmd)

	rootCmd.AddCommand(holdCmd)
}

func runHoldAdd(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	reg, err := holds.Load(townRoot)
	if err != nil {
		return fmt.Errorf("loading holds registry: %w", err)
	}

	by := holdAddBy
	if by == "" {
		by = detectActor()
	}

	h := reg.Add(args[0], holdAddThreshold, args[1], holdAddReleaseCondition, by)

	if err := holds.Save(townRoot, reg); err != nil {
		return fmt.Errorf("saving holds registry: %w", err)
	}

	fmt.Printf("%s Hold %s set: %s\n", style.Bold.Render("⏸"), h.ID, h.Scope)
	fmt.Printf("  Reason: %s\n", h.Reason)
	if h.Threshold != "" {
		fmt.Printf("  Threshold: %s\n", h.Threshold)
	}
	if h.ReleaseCondition != "" {
		fmt.Printf("  Release condition: %s\n", h.ReleaseCondition)
	}
	fmt.Printf("  Set by: %s at %s\n", h.SetBy, h.SetAt)
	return nil
}

func runHoldList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	reg, err := holds.Load(townRoot)
	if err != nil {
		return fmt.Errorf("loading holds registry: %w", err)
	}

	list := reg.Active()
	if holdListAll {
		list = reg.Holds
	}

	if holdListJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	}

	if len(list) == 0 {
		fmt.Println("No active holds.")
		return nil
	}

	fmt.Printf("%s (%d)\n\n", style.Bold.Render("Operational Holds"), len(list))
	for _, h := range list {
		indicator := "⏸"
		status := ""
		if h.Released {
			indicator = "○"
			status = style.Dim.Render(" (released)")
		}
		fmt.Printf("  %s %s: %s%s\n", indicator, h.ID, h.Scope, status)
		fmt.Printf("      Reason: %s\n", h.Reason)
		if h.Threshold != "" {
			fmt.Printf("      Threshold: %s\n", h.Threshold)
		}
		if h.ReleaseCondition != "" {
			fmt.Printf("      Release condition: %s\n", h.ReleaseCondition)
		}
		fmt.Printf("      Set by: %s at %s\n", h.SetBy, h.SetAt)
	}
	return nil
}

func runHoldShow(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	reg, err := holds.Load(townRoot)
	if err != nil {
		return fmt.Errorf("loading holds registry: %w", err)
	}

	h, ok := reg.Find(args[0])
	if !ok {
		return fmt.Errorf("no such hold: %s", args[0])
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(h)
}

func runHoldRelease(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	reg, err := holds.Load(townRoot)
	if err != nil {
		return fmt.Errorf("loading holds registry: %w", err)
	}

	by := holdReleaseBy
	if by == "" {
		by = detectActor()
	}

	if err := reg.Release(args[0], by, holdReleaseReason); err != nil {
		return err
	}

	if err := holds.Save(townRoot, reg); err != nil {
		return fmt.Errorf("saving holds registry: %w", err)
	}

	fmt.Printf("%s Hold %s released\n", style.Bold.Render("▶"), args[0])
	return nil
}
