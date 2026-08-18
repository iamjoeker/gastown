package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/plugin"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Dog command flags
var (
	dogListJSON       bool
	dogListNoDispatch bool
	dogStatusJSON     bool
	dogForce          bool
	dogRemoveAll      bool
	dogCallAll        bool

	// Dispatch flags
	dogDispatchPlugin string
	dogDispatchRig    string
	dogDispatchCreate bool
	dogDispatchDog    string
	dogDispatchJSON   bool
	dogDispatchDryRun bool

	// Health-check flags
	dogHealthJSON          bool
	dogHealthAutoClear     bool
	dogHealthMaxInactivity time.Duration
	dogHealthStaleDispatch time.Duration
	dogHealthAlarmCooldown time.Duration
)

var dogCmd = &cobra.Command{
	Use:     "dog",
	Aliases: []string{"dogs"},
	GroupID: GroupAgents,
	Short:   "Manage dogs (cross-rig infrastructure workers)",
	Long: `Manage dogs - reusable workers for infrastructure and cleanup.

CATS VS DOGS:
  Polecats (cats) build features. One rig. Ephemeral sessions (one task, then nuked).
  Dogs clean up messes. Cross-rig. Reusable (multiple tasks, eventually recycled).

Dogs are managed by the Deacon for town-level work:
  - Infrastructure tasks (rebuilding, syncing, migrations)
  - Cleanup operations (orphan branches, stale files)
  - Cross-rig work that spans multiple projects

Each dog has worktrees into every configured rig, enabling cross-project
operations. Dogs return to idle state after completing work (unlike cats).

The kennel is at ~/gt/deacon/dogs/. The Deacon dispatches work to dogs.`,
}

var dogAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Create a new dog in the kennel",
	Long: `Create a new dog in the kennel with multi-rig worktrees.

Each dog gets a worktree per configured rig (e.g., gastown, beads).
The dog starts in idle state, ready to receive work from the Deacon.

Example:
  gt dog add alpha
  gt dog add bravo`,
	Args: cobra.ExactArgs(1),
	RunE: runDogAdd,
}

var dogRemoveCmd = &cobra.Command{
	Use:   "remove <name>... | --all",
	Short: "Remove dogs from the kennel",
	Long: `Remove one or more dogs from the kennel.

Removes all worktrees and the dog directory.
Use --force to remove even if dog is in working state.

Examples:
  gt dog remove alpha
  gt dog remove alpha bravo
  gt dog remove --all
  gt dog remove alpha --force`,
	Args: func(cmd *cobra.Command, args []string) error {
		if dogRemoveAll {
			return nil
		}
		if len(args) < 1 {
			return fmt.Errorf("requires at least 1 dog name (or use --all)")
		}
		return nil
	},
	RunE: runDogRemove,
}

var dogListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all dogs in the kennel",
	Long: `List all dogs in the kennel with their observed execution state.

Reports execution state, not pool intent. The state in .dog.json says what the
dispatcher meant to happen; this command joins it with live tmux session state
and the dog's open dispatch mail to report what is actually true:

  idle      genuinely available
  working   session alive and executing
  stalled   state=working but the session is gone
  pending   state=idle with dispatch mail no session will read
  orphan    state=idle but a session is still alive

Only idle and working are healthy. The others are counted separately so a dog
holding undelivered dispatches can never be reported as spare capacity.

Use --no-dispatches to skip the mailbox reads (session state only).

Examples:
  gt dog list
  gt dog list --json
  gt dog list --no-dispatches`,
	RunE: runDogList,
}

var dogCallCmd = &cobra.Command{
	Use:   "call [name]",
	Short: "Wake idle dog(s) for work",
	Long: `Wake an idle dog to prepare for work.

With a name, wakes the specific dog.
With --all, wakes all idle dogs.
Without arguments, wakes one idle dog (if available).

This updates the dog's last-active timestamp and can trigger
session creation for the dog's worktrees.

Examples:
  gt dog call alpha
  gt dog call --all
  gt dog call`,
	RunE: runDogCall,
}

var dogDoneCmd = &cobra.Command{
	Use:   "done [name]",
	Short: "Mark dog as done and return to idle",
	Long: `Mark a dog as done with its current work and return to idle state.

Dogs should call this when they complete their work assignment.
This clears the work field and sets state to idle, making the dog
available for new work.

Without a name argument, auto-detects the current dog from the working
directory (must be run from within a dog's worktree).

Examples:
  gt dog done         # Auto-detect from cwd
  gt dog done alpha   # Explicit name`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDogDone,
}

var dogClearCmd = &cobra.Command{
	Use:   "clear <name>",
	Short: "Reset a stuck dog to idle state",
	Long: `Reset a stuck dog to idle state.

Use this when a dog is stuck in "working" state but its session has died.
The Deacon uses this during patrol to clear dogs that have timed out.

By default, refuses to clear a dog if its tmux session still exists.
Use --force to clear even if the session is alive.

Examples:
  gt dog clear alpha           # Clear if session is dead
  gt dog clear alpha --force   # Force clear even if session exists`,
	Args: cobra.ExactArgs(1),
	RunE: runDogClear,
}

var dogStatusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Show detailed dog status",
	Long: `Show detailed status for a specific dog or summary for all dogs.

With a name, shows detailed info including:
  - State (idle/working)
  - Current work assignment
  - Worktree paths per rig
  - Last active timestamp

Without a name, shows pack summary:
  - Total dogs
  - Idle/working counts
  - Pack health

Examples:
  gt dog status alpha
  gt dog status
  gt dog status --json`,
	RunE: runDogStatus,
}

var dogDispatchCmd = &cobra.Command{
	Use:   "dispatch --plugin <name>",
	Short: "Dispatch plugin execution to a dog",
	Long: `Dispatch a plugin for execution by a dog worker.

This is the formalized command for sending plugin work to dogs. The Deacon
uses this during patrol cycles to dispatch plugins with open gates.

The command:
1. Finds the plugin definition (plugin.md)
2. Assigns work to an idle dog (marks as working)
3. Sends mail with plugin instructions to the dog
4. Wakes the dog through exactly one delivery path
5. Returns immediately (non-blocking)

Step 4 matters: a dog's pane can be written by the mail notification or by a
new session's startup prompt, and if both fire the second interrupts the first
and destroys the instruction. So when the session is down its startup prompt
is the delivery and the mail rides silently; when the session is already up
the mail notification is the delivery and no session is started. The chosen
path is reported as delivery_path in --json output.

The dog discovers the work via its mail inbox and executes the plugin
instructions. On completion, the dog sends DOG_DONE mail to deacon/.

If the session cannot be started the dispatch is rolled back completely: the
work assignment is cleared AND the dispatch mail is archived, so the dog does
not go back to idle still holding an open dispatch.

Examples:
  gt dog dispatch --plugin rebuild-gt
  gt dog dispatch --plugin rebuild-gt --rig gastown
  gt dog dispatch --plugin rebuild-gt --dog alpha
  gt dog dispatch --plugin rebuild-gt --create
  gt dog dispatch --plugin rebuild-gt --dry-run
  gt dog dispatch --plugin rebuild-gt --json`,
	RunE: runDogDispatch,
}

var dogHealthCheckCmd = &cobra.Command{
	Use:   "health-check [name]",
	Short: "Check dog health (zombies, hung, orphans)",
	Long: `Check dog health and detect problems.

Detects:
  - Zombies: state=working but tmux session or agent process is dead
  - Hung: agent alive but no tmux activity for too long
  - Orphans: dog idle but tmux session still exists
  - Orphaned dispatches: dispatch mail still open for a dog whose session
    is gone — nothing will ever execute it
  - Stale dispatches: dispatch mail open past --stale-dispatch while the
    session is alive, i.e. the dog is holding work it is not doing

With --auto-clear, zombies are returned to idle state and orphaned dispatches
are archived, so session death fails a dispatch instead of stranding it.
Hung dogs are reported only (Deacon decides per ZFC principle). Stale
dispatches on a live session are never auto-archived — the session may still
be mid-execution — they escalate instead.

Dispatch alarms escalate at MEDIUM severity, throttled per dog by
--alarm-cooldown so a persistent problem escalates once per window rather
than once per patrol cycle.

Exit codes:
  0 = all healthy
  1 = error
  2 = needs attention

Examples:
  gt dog health-check
  gt dog health-check alpha
  gt dog health-check --json
  gt dog health-check --auto-clear
  gt dog health-check --max-inactivity 1h
  gt dog health-check --stale-dispatch 15m --alarm-cooldown 1h`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDogHealthCheck,
}

func init() {
	// List flags
	dogListCmd.Flags().BoolVar(&dogListJSON, "json", false, "Output as JSON")
	dogListCmd.Flags().BoolVar(&dogListNoDispatch, "no-dispatches", false, "Skip the dispatch-mail probe (session state only, no mailbox reads)")

	// Remove flags
	dogRemoveCmd.Flags().BoolVarP(&dogForce, "force", "f", false, "Force removal even if working")
	dogRemoveCmd.Flags().BoolVar(&dogRemoveAll, "all", false, "Remove all dogs")

	// Call flags
	dogCallCmd.Flags().BoolVar(&dogCallAll, "all", false, "Wake all idle dogs")

	// Clear flags (reuses dogForce from remove)
	dogClearCmd.Flags().BoolVarP(&dogForce, "force", "f", false, "Force clear even if session exists")

	// Status flags
	dogStatusCmd.Flags().BoolVar(&dogStatusJSON, "json", false, "Output as JSON")

	// Dispatch flags
	dogDispatchCmd.Flags().StringVar(&dogDispatchPlugin, "plugin", "", "Plugin name to dispatch (required)")
	dogDispatchCmd.Flags().StringVar(&dogDispatchRig, "rig", "", "Limit plugin search to specific rig")
	dogDispatchCmd.Flags().StringVar(&dogDispatchDog, "dog", "", "Dispatch to specific dog (default: any idle)")
	dogDispatchCmd.Flags().BoolVar(&dogDispatchCreate, "create", false, "Create a dog if none idle")
	dogDispatchCmd.Flags().BoolVar(&dogDispatchJSON, "json", false, "Output as JSON")
	dogDispatchCmd.Flags().BoolVarP(&dogDispatchDryRun, "dry-run", "n", false, "Show what would be done without doing it")
	_ = dogDispatchCmd.MarkFlagRequired("plugin")

	// Health-check flags
	dogHealthCheckCmd.Flags().BoolVar(&dogHealthJSON, "json", false, "Output as JSON")
	dogHealthCheckCmd.Flags().BoolVar(&dogHealthAutoClear, "auto-clear", false, "Auto-clear zombie dogs")
	dogHealthCheckCmd.Flags().DurationVar(&dogHealthMaxInactivity, "max-inactivity", 10*time.Minute, "Max inactivity before considering hung")
	dogHealthCheckCmd.Flags().DurationVar(&dogHealthStaleDispatch, "stale-dispatch", dog.DefaultStaleDispatchAfter, "Alarm on dispatches open longer than this")
	dogHealthCheckCmd.Flags().DurationVar(&dogHealthAlarmCooldown, "alarm-cooldown", dog.DefaultDispatchAlarmCooldown, "Minimum interval between dispatch alarms for the same dog")

	// Add subcommands
	dogCmd.AddCommand(dogAddCmd)
	dogCmd.AddCommand(dogRemoveCmd)
	dogCmd.AddCommand(dogListCmd)
	dogCmd.AddCommand(dogCallCmd)
	dogCmd.AddCommand(dogClearCmd)
	dogCmd.AddCommand(dogDoneCmd)
	dogCmd.AddCommand(dogStatusCmd)
	dogCmd.AddCommand(dogDispatchCmd)
	dogCmd.AddCommand(dogHealthCheckCmd)

	rootCmd.AddCommand(dogCmd)
}

// getDogManager creates a dog.Manager with the current town root.
//
// Use FindFromCwdOrError so we honor GT_TOWN_ROOT/GT_ROOT env vars when
// invoked from a dog worktree (e.g. ~/gt/deacon/dogs/alpha/<rig>/), where
// FindFromCwd alone might walk up to a non-town ancestor or stop at a path
// without mayor/rigs.json — which previously broke `gt dog done` and
// blocked DOG_DONE delivery (hq-zyvo).
func getDogManager() (*dog.Manager, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, fmt.Errorf("finding town root: %w", err)
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("loading rigs config: %w", err)
	}

	return dog.NewManager(townRoot, rigsConfig), nil
}

func runDogAdd(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Validate name
	if strings.ContainsAny(name, "/\\. ") {
		return fmt.Errorf("dog name cannot contain /, \\, ., or spaces")
	}

	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	d, err := mgr.Add(name)
	if err != nil {
		return fmt.Errorf("adding dog %s: %w", name, err)
	}

	fmt.Printf("✓ Created dog %s in kennel\n", style.Bold.Render(name))
	fmt.Printf("  Path: %s\n", d.Path)
	fmt.Printf("  Worktrees:\n")
	for rigName, path := range d.Worktrees {
		fmt.Printf("    %s: %s\n", rigName, path)
	}

	// Create agent bead for the dog
	townRoot, _ := workspace.FindFromCwd()
	if townRoot != "" {
		b := beads.New(townRoot)
		location := filepath.Join("deacon", "dogs", name)

		issue, err := b.CreateDogAgentBead(name, location)
		if err != nil {
			// Non-fatal: warn but don't fail dog creation
			fmt.Printf("  Warning: could not create agent bead: %v\n", err)
		} else {
			fmt.Printf("  Agent bead: %s\n", issue.ID)
		}
	}

	return nil
}

func runDogRemove(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	var names []string
	if dogRemoveAll {
		dogs, err := mgr.List()
		if err != nil {
			return fmt.Errorf("listing dogs: %w", err)
		}
		for _, d := range dogs {
			names = append(names, d.Name)
		}
		if len(names) == 0 {
			fmt.Println("No dogs in kennel")
			return nil
		}
	} else {
		names = args
	}

	// Get beads client for cleanup
	townRoot, _ := workspace.FindFromCwd()
	var b *beads.Beads
	if townRoot != "" {
		b = beads.New(townRoot)
	}

	var removeErrors []string
	removed := 0

	for _, name := range names {
		d, err := mgr.Get(name)
		if err != nil {
			style.PrintWarning("dog %s not found, skipping", name)
			continue
		}

		// Check if working
		if d.State == dog.StateWorking && !dogForce {
			removeErrors = append(removeErrors, fmt.Sprintf("%s: is working (use --force to remove anyway)", name))
			continue
		}

		if err := mgr.Remove(name); err != nil {
			removeErrors = append(removeErrors, fmt.Sprintf("%s: %v", name, err))
			continue
		}

		fmt.Printf("✓ Removed dog %s\n", name)
		removed++

		// Reset agent bead for the dog (preserves persistent identity)
		if b != nil {
			if err := b.ResetDogAgentBead(name); err != nil {
				// Non-fatal: warn but don't fail dog removal
				fmt.Printf("  Warning: could not reset agent bead: %v\n", err)
			}
		}
	}

	if len(removeErrors) > 0 {
		fmt.Printf("\nSome removals failed:\n")
		for _, e := range removeErrors {
			fmt.Printf("  - %s\n", e)
		}
	}

	if removed > 0 {
		fmt.Printf("\n✓ Removed %d dog(s).\n", removed)
	}

	if len(removeErrors) > 0 {
		return fmt.Errorf("%d removal(s) failed", len(removeErrors))
	}

	return nil
}

func runDogList(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	dogs, err := mgr.List()
	if err != nil {
		return fmt.Errorf("listing dogs: %w", err)
	}

	if len(dogs) == 0 {
		if dogListJSON {
			fmt.Println("[]")
		} else {
			fmt.Println("No dogs in kennel")
		}
		return nil
	}

	// Observe reality rather than reporting pool intent. .dog.json says what
	// the dispatcher meant to happen; the tmux session and the dog's open
	// dispatch mail say what is actually happening. This list read "3 idle"
	// for three dogs each holding 19 undelivered dispatches, which is why the
	// probes below are not optional.
	obs := dogObserveAll(dogs, !dogListNoDispatch)

	if dogListJSON {
		type DogListItem struct {
			Name           string            `json:"name"`
			State          dog.State         `json:"state"`
			ExecState      dog.ExecState     `json:"exec_state"`
			SessionRunning bool              `json:"session_running"`
			OpenDispatches int               `json:"open_dispatches,omitempty"`
			Work           string            `json:"work,omitempty"`
			WorkStartedAt  *time.Time        `json:"work_started_at,omitempty"`
			LastActive     time.Time         `json:"last_active"`
			Worktrees      map[string]string `json:"worktrees,omitempty"`
		}

		var items []DogListItem
		for _, d := range dogs {
			o := obs[d.Name]
			item := DogListItem{
				Name:           d.Name,
				State:          d.State,
				ExecState:      o.exec,
				SessionRunning: o.sessionRunning,
				OpenDispatches: o.openDispatches,
				Work:           d.Work,
				LastActive:     d.LastActive,
				Worktrees:      d.Worktrees,
			}
			if !d.WorkStartedAt.IsZero() {
				t := d.WorkStartedAt
				item.WorkStartedAt = &t
			}
			items = append(items, item)
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	}

	// Pretty print
	fmt.Println(style.Bold.Render("The Pack"))
	fmt.Println()

	counts := map[dog.ExecState]int{}

	for _, d := range dogs {
		o := obs[d.Name]
		counts[o.exec]++

		stateIcon := "○"
		stateStyle := style.Dim
		switch o.exec {
		case dog.ExecWorking:
			stateIcon, stateStyle = "●", style.Bold
		case dog.ExecStalled, dog.ExecOrphan:
			stateIcon, stateStyle = "✗", style.Bold
		case dog.ExecPending:
			stateIcon, stateStyle = "◐", style.Bold
		}

		line := fmt.Sprintf("  %s %s [%s]", stateIcon, stateStyle.Render(d.Name), o.exec)
		if d.Work != "" {
			line += fmt.Sprintf(" → %s", style.Dim.Render(d.Work))
		}
		if o.openDispatches > 0 {
			line += style.Dim.Render(fmt.Sprintf(" (%d open dispatch)", o.openDispatches))
		}
		fmt.Println(line)
	}

	fmt.Println()
	fmt.Printf("  %d idle, %d working", counts[dog.ExecIdle], counts[dog.ExecWorking])
	// Unhealthy states are named separately so they can never be folded into
	// the idle count and read as capacity.
	for _, s := range []dog.ExecState{dog.ExecStalled, dog.ExecPending, dog.ExecOrphan} {
		if counts[s] > 0 {
			fmt.Printf(", %d %s", counts[s], s)
		}
	}
	fmt.Println()

	return nil
}

// dogObservation is a dog's observed execution state plus the evidence for it.
type dogObservation struct {
	exec           dog.ExecState
	sessionRunning bool
	openDispatches int
}

// dogObserveAll probes live session state (and, unless skipped, open dispatch
// mail) for each dog and derives its execution state.
//
// Probe failures degrade rather than fail: a mailbox that cannot be read
// contributes zero dispatches, which is the same view the old intent-only
// output gave. Session liveness is always probed — it is a cheap tmux call and
// it is what separates "working" from "stalled".
func dogObserveAll(dogs []*dog.Dog, withDispatch bool) map[string]dogObservation {
	obs := make(map[string]dogObservation, len(dogs))

	tm := tmux.NewTmux()
	var insp *mailDispatchInspector
	if withDispatch {
		if townRoot, err := workspace.FindFromCwd(); err == nil {
			insp = newMailDispatchInspector(townRoot)
		}
	}

	for _, d := range dogs {
		o := dogObservation{}
		if running, err := tm.HasSession(fmt.Sprintf("hq-dog-%s", d.Name)); err == nil {
			o.sessionRunning = running
		}
		if insp != nil {
			if scan, err := insp.Scan(d.Name); err == nil {
				o.openDispatches = scan.Open
			}
		}
		o.exec = dog.DeriveExecState(d.State, o.sessionRunning, o.openDispatches)
		obs[d.Name] = o
	}
	return obs
}

func runDogCall(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	if dogCallAll {
		// Wake all idle dogs
		dogs, err := mgr.List()
		if err != nil {
			return fmt.Errorf("listing dogs: %w", err)
		}

		woken := 0
		for _, d := range dogs {
			if d.State == dog.StateIdle {
				if err := mgr.SetState(d.Name, dog.StateIdle); err != nil {
					style.PrintWarning("failed to wake %s: %v", d.Name, err)
					continue
				}
				woken++
				fmt.Printf("✓ Called %s\n", d.Name)
			}
		}

		if woken == 0 {
			fmt.Println("No idle dogs to call")
		} else {
			fmt.Printf("\n%d dog(s) ready\n", woken)
		}
		return nil
	}

	if len(args) > 0 {
		// Wake specific dog
		name := args[0]
		d, err := mgr.Get(name)
		if err != nil {
			return fmt.Errorf("getting dog %s: %w", name, err)
		}

		if d.State == dog.StateWorking {
			fmt.Printf("Dog %s is already working (use 'gt dog done %s' when complete)\n", name, name)
			return nil
		}

		if err := mgr.SetState(name, dog.StateIdle); err != nil {
			return fmt.Errorf("waking dog %s: %w", name, err)
		}

		fmt.Printf("✓ Called %s - ready for work\n", name)
		return nil
	}

	// Wake one idle dog
	d, err := mgr.GetIdleDog()
	if err != nil {
		return fmt.Errorf("getting idle dog: %w", err)
	}

	if d == nil {
		fmt.Println("No idle dogs available")
		return nil
	}

	if err := mgr.SetState(d.Name, dog.StateIdle); err != nil {
		return fmt.Errorf("waking dog %s: %w", d.Name, err)
	}

	fmt.Printf("✓ Called %s - ready for work\n", d.Name)
	return nil
}

func runDogClear(cmd *cobra.Command, args []string) error {
	name := args[0]

	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	d, err := mgr.Get(name)
	if err != nil {
		return fmt.Errorf("getting dog %s: %w", name, err)
	}

	// Check if already idle
	if d.State == dog.StateIdle && d.Work == "" {
		fmt.Printf("Dog %s is already idle\n", name)
		return nil
	}

	// Check for live tmux session
	if !dogForce {
		sessionName := fmt.Sprintf("hq-dog-%s", name)
		tm := tmux.NewTmux()
		if has, _ := tm.HasSession(sessionName); has {
			return fmt.Errorf("dog %s has an active session (%s)\nUse --force to clear anyway", name, sessionName)
		}
	}

	// Clear work and return to idle
	if err := mgr.ClearWork(name); err != nil {
		return fmt.Errorf("clearing work for dog %s: %w", name, err)
	}

	// The dispatch that this work came from is still open in the dog's inbox.
	// Returning the dog to idle without archiving it leaves a dispatch
	// assigned to a dog that is no longer executing it — an orphan by
	// construction. Fail the dispatch with the work.
	closePluginMails(name)
	mgr.ClearDispatchAlarm(name)

	fmt.Printf("✓ Cleared dog %s (now idle)\n", name)
	if d.Work != "" {
		fmt.Printf("  Previous work: %s\n", d.Work)
	}
	return nil
}

func runDogDone(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	var name string
	if len(args) > 0 {
		name = args[0]
	} else {
		// Auto-detect dog from cwd
		// Dog worktrees are at ~/gt/deacon/dogs/<name>/<rig>/
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting cwd: %w", err)
		}

		// Look for /deacon/dogs/<name>/ in path
		parts := splitPathComponents(cwd)
		for i := 0; i < len(parts)-1; i++ {
			if parts[i] == "dogs" && i > 0 && parts[i-1] == "deacon" {
				name = parts[i+1]
				break
			}
		}

		if name == "" {
			return fmt.Errorf("could not detect dog name from cwd: %s\nRun from a dog worktree or specify name: gt dog done <name>", cwd)
		}
	}

	d, err := mgr.Get(name)
	if err != nil {
		return fmt.Errorf("getting dog %s: %w", name, err)
	}

	// Always close accumulated plugin mails, even if dog is already idle.
	// Plugin dispatch mails accumulate across sessions and must be cleaned up
	// regardless of current work state.
	closePluginMails(name)

	if d.State == dog.StateIdle && d.Work == "" {
		fmt.Printf("Dog %s is already idle with no work\n", name)
		return nil
	}

	if err := mgr.ClearWork(name); err != nil {
		return fmt.Errorf("clearing work for dog %s: %w", name, err)
	}

	fmt.Printf("✓ Dog %s returned to kennel (idle)\n", name)

	// Auto-terminate the tmux session after a short delay.
	// Dogs run inside tmux sessions (hq-dog-<name>). Without this, the
	// Claude agent idles at the prompt indefinitely after completing work,
	// wasting resources until the stale-working detector kills it (2 hours).
	// The delay lets the agent see the success output before termination.
	//
	// We disable remain-on-exit first — otherwise kill-session leaves a
	// dead pane that the deacon's health-check reports as an orphan.
	sessionID := fmt.Sprintf("hq-dog-%s", name)
	t := tmux.NewTmux()
	_ = t.SetRemainOnExit(sessionID, false)
	fmt.Printf("  Session %s will terminate in 3s\n", sessionID)

	// Kill the tmux session after a short delay using a goroutine.
	// Previous approach used bash -c "sleep 3 && tmux kill-session" which
	// fails silently on Windows. The goroutine is cross-platform and uses
	// the tmux package which handles the socket name automatically.
	go func() {
		time.Sleep(3 * time.Second)
		if err := t.KillSession(sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to kill session %s: %v\n", sessionID, err)
		}
	}()

	// Wait for the goroutine to finish (the process will exit after kill).
	time.Sleep(4 * time.Second)

	return nil
}

func splitPathComponents(path string) []string {
	if path == "" {
		return nil
	}

	return strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
}

// closePluginMails archives all open "Plugin: " dispatch mails from a dog's inbox.
// Plugin dispatch mails sent by the daemon accumulate because gt dog done never
// closed them. On every UserPromptSubmit hook, gt mail check --inject re-injects
// ALL open mails, causing context to balloon. This function cleans up eagerly.
// It is best-effort: failures are logged but do not prevent dog from going idle.
func closePluginMails(dogName string) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return // not in a Gas Town workspace, skip cleanup
	}

	insp := newMailDispatchInspector(townRoot)
	closed, err := insp.Reclaim(dogName)
	if err != nil && closed == 0 {
		return
	}
	if closed > 0 {
		fmt.Printf("  Closed %d stale plugin mail(s) from inbox\n", closed)
	}
}

// mailDispatchInspector implements dog.DispatchInspector over the mail router.
type mailDispatchInspector struct {
	townRoot string
}

func newMailDispatchInspector(townRoot string) *mailDispatchInspector {
	return &mailDispatchInspector{townRoot: townRoot}
}

// mailbox resolves a dog's inbox.
func (i *mailDispatchInspector) mailbox(dogName string) (*mail.Mailbox, string, error) {
	address := dog.DogAddress(dogName)
	router := mail.NewRouterWithTownRoot(i.townRoot, i.townRoot)
	mb, err := router.GetMailbox(address)
	if err != nil {
		return nil, address, fmt.Errorf("opening mailbox for %s: %w", address, err)
	}
	return mb, address, nil
}

// Scan reports the open dispatch mail for a dog.
func (i *mailDispatchInspector) Scan(dogName string) (dog.DispatchScan, error) {
	mb, address, err := i.mailbox(dogName)
	if err != nil {
		return dog.DispatchScan{}, err
	}
	return dog.ScanDispatchMail(mb, address, time.Now())
}

// Reclaim archives every open dispatch for a dog.
func (i *mailDispatchInspector) Reclaim(dogName string) (int, error) {
	mb, address, err := i.mailbox(dogName)
	if err != nil {
		return 0, err
	}
	return dog.ReclaimDispatchMail(mb, address)
}

func runDogStatus(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	if len(args) > 0 {
		// Show specific dog status
		name := args[0]
		return showDogStatus(mgr, name)
	}

	// Show pack summary
	return showPackStatus(mgr)
}

func showDogStatus(mgr *dog.Manager, name string) error {
	d, err := mgr.Get(name)
	if err != nil {
		return fmt.Errorf("getting dog %s: %w", name, err)
	}

	if dogStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(d)
	}

	fmt.Printf("Dog: %s\n\n", style.Bold.Render(d.Name))
	fmt.Printf("  State:       %s\n", d.State)
	if d.Work != "" {
		fmt.Printf("  Work:        %s\n", d.Work)
	} else {
		fmt.Printf("  Work:        %s\n", style.Dim.Render("(none)"))
	}
	fmt.Printf("  Path:        %s\n", d.Path)
	fmt.Printf("  Last Active: %s\n", dogFormatTimeAgo(d.LastActive))
	fmt.Printf("  Created:     %s\n", d.CreatedAt.Format("2006-01-02 15:04"))

	if len(d.Worktrees) > 0 {
		fmt.Println("\nWorktrees:")
		for rigName, path := range d.Worktrees {
			// Check if worktree exists
			exists := "✓"
			if _, err := os.Stat(path); os.IsNotExist(err) {
				exists = "✗"
			}
			fmt.Printf("  %s %s: %s\n", exists, rigName, path)
		}
	}

	// Check for tmux session
	sessionName := fmt.Sprintf("hq-dog-%s", name)
	tm := tmux.NewTmux()
	if has, _ := tm.HasSession(sessionName); has {
		fmt.Printf("\nSession: %s (running)\n", sessionName)
	}

	return nil
}

func showPackStatus(mgr *dog.Manager) error {
	dogs, err := mgr.List()
	if err != nil {
		return fmt.Errorf("listing dogs: %w", err)
	}

	if dogStatusJSON {
		type PackStatus struct {
			Total     int    `json:"total"`
			Idle      int    `json:"idle"`
			Working   int    `json:"working"`
			KennelDir string `json:"kennel_dir"`
		}

		townRoot, _ := workspace.FindFromCwd()
		status := PackStatus{
			Total:     len(dogs),
			KennelDir: filepath.Join(townRoot, "deacon", "dogs"),
		}
		for _, d := range dogs {
			if d.State == dog.StateIdle {
				status.Idle++
			} else {
				status.Working++
			}
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}

	fmt.Println(style.Bold.Render("Pack Status"))
	fmt.Println()

	if len(dogs) == 0 {
		fmt.Println("  No dogs in kennel")
		fmt.Println()
		fmt.Println("  Use 'gt dog add <name>' to add a dog")
		return nil
	}

	idleCount := 0
	workingCount := 0
	for _, d := range dogs {
		if d.State == dog.StateIdle {
			idleCount++
		} else {
			workingCount++
		}
	}

	fmt.Printf("  Total:   %d\n", len(dogs))
	fmt.Printf("  Idle:    %d\n", idleCount)
	fmt.Printf("  Working: %d\n", workingCount)

	if idleCount > 0 {
		fmt.Println()
		fmt.Println(style.Dim.Render("  Ready for work. Use 'gt dog call' to wake."))
	}

	return nil
}

// dogFormatTimeAgo formats a time as a relative string like "2 hours ago".
func dogFormatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "(unknown)"
	}

	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}

func runDogHealthCheck(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	tm := tmux.NewTmux()
	hc := dog.NewHealthChecker(mgr, tm)

	// Dispatch inspection turns "the session is gone" into "and here are the
	// N dispatches it was holding". Without a town root we can't reach the
	// mailboxes, so the check degrades to session-only rather than failing.
	if townRoot, trErr := workspace.FindFromCwd(); trErr == nil {
		hc = hc.WithDispatch(newMailDispatchInspector(townRoot), dogHealthStaleDispatch, dogHealthAlarmCooldown)
	}

	var results []dog.DogHealthResult

	if len(args) > 0 {
		// Single dog
		d, err := mgr.Get(args[0])
		if err != nil {
			return fmt.Errorf("getting dog %s: %w", args[0], err)
		}
		r := hc.Check(d, dogHealthMaxInactivity, dogHealthAutoClear)
		results = []dog.DogHealthResult{r}
	} else {
		// All dogs
		results, err = hc.CheckAll(dogHealthMaxInactivity, dogHealthAutoClear)
		if err != nil {
			return err
		}
	}

	attention := dog.NeedsAttentionCount(results)

	// Raise the alarm that was missing: a dispatch stranded past threshold now
	// escalates instead of waiting for someone to notice. The checker applies a
	// per-dog cooldown, so a persistent problem escalates once per window
	// rather than once per patrol cycle.
	alarms := dogRaiseDispatchAlarms(results)

	if dogHealthJSON {
		type HealthReport struct {
			Dogs           []dog.DogHealthResult `json:"dogs"`
			NeedsAttention int                   `json:"needs_attention"`
			Alarms         []string              `json:"alarms,omitempty"`
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(HealthReport{Dogs: results, NeedsAttention: attention, Alarms: alarms}); err != nil {
			return err
		}
	} else {
		if len(results) == 0 {
			fmt.Println("No dogs in kennel")
			return nil
		}

		fmt.Println(style.Bold.Render("Dog Health Check"))
		fmt.Println()

		for _, r := range results {
			icon := "✓"
			if r.NeedsAttention {
				icon = "✗"
			}
			line := fmt.Sprintf("  %s %s [%s] session=%s", icon, r.Name, r.State, r.SessionStatus)
			if r.ExecState != "" && string(r.ExecState) != string(r.State) {
				line += fmt.Sprintf(" exec=%s", r.ExecState)
			}
			if r.WorkDuration > 0 {
				line += fmt.Sprintf(" duration=%s", r.WorkDuration.Truncate(time.Second))
			}
			if r.OpenDispatches > 0 {
				line += fmt.Sprintf(" dispatches=%d", r.OpenDispatches)
			}
			if r.DispatchesReclaimed > 0 {
				line += fmt.Sprintf(" reclaimed=%d", r.DispatchesReclaimed)
			}
			if r.AutoCleared {
				line += " (auto-cleared)"
			}
			fmt.Println(line)
			if r.Recommendation != "" && r.NeedsAttention {
				fmt.Printf("    → %s\n", r.Recommendation)
			}
		}

		fmt.Println()
		if len(alarms) > 0 {
			fmt.Printf("  %d dispatch alarm(s) escalated\n", len(alarms))
		}
		if attention > 0 {
			fmt.Printf("  %d dog(s) need attention\n", attention)
		} else {
			fmt.Println("  All dogs healthy")
		}
	}

	// Exit code 2 for needs-attention
	if attention > 0 {
		os.Exit(2)
	}

	return nil
}

// dispatchDelivery records which single path will deliver a dispatch to a dog.
type dispatchDelivery struct {
	// suppressMailNotify stops router.Send from firing its async tmux
	// notification, leaving the session's startup prompt as the sole delivery.
	suppressMailNotify bool
	// reason explains the choice, for the dispatch result and for humans
	// reading a failure after the fact.
	reason string
}

// planDispatchDelivery picks exactly one delivery path for a dispatch.
//
// Two mechanisms can put text into a dog's pane: router.Send's background
// notification goroutine, and the startup prompt handed to a newly launched
// agent. They are independent and unsynchronised — the nudge lock serialises
// nudge against nudge and cannot see a startup prompt at all. When both fire
// at one pane the notification's idle probe reads a just-booted agent as idle
// and types into a turn that is already in flight; the Enter interrupts it and
// the instruction is destroyed. That is not a prompt-wording problem and no
// wording can fix it.
//
// The fix is to let exactly one path deliver:
//   - session down: we are about to start it, so its startup prompt is the
//     delivery and the mail rides silently.
//   - session up: the mail's notification is the delivery and we start nothing.
//
// A failed liveness probe is treated as "up", because the two errors are not
// symmetric: a redundant nudge at a dead session is a no-op, while a
// suppressed nudge at a live one strands the dispatch until it is reaped.
func planDispatchDelivery(sessionRunning bool, probeErr error) dispatchDelivery {
	if probeErr != nil {
		return dispatchDelivery{
			suppressMailNotify: false,
			reason:             fmt.Sprintf("session liveness unknown (%v) — notifying via mail", probeErr),
		}
	}
	if sessionRunning {
		return dispatchDelivery{
			suppressMailNotify: false,
			reason:             "session already running — mail notification delivers",
		}
	}
	return dispatchDelivery{
		suppressMailNotify: true,
		reason:             "session will be started — startup prompt delivers",
	}
}

// dogRaiseDispatchAlarms escalates every dispatch alarm carried by the health
// results and returns the messages escalated.
//
// The checker has already applied the per-dog cooldown, so anything reaching
// here is due. Escalation is best-effort: a failed escalation is reported to
// stderr rather than swallowed, because a silently-dropped alarm reproduces
// the exact defect this alarm exists to fix.
func dogRaiseDispatchAlarms(results []dog.DogHealthResult) []string {
	var raised []string
	for _, r := range results {
		if r.DispatchAlarm == "" {
			continue
		}
		msg := "dog dispatch stranded: " + r.DispatchAlarm
		if err := dogEscalateBestEffort(msg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: dispatch alarm escalation failed (%v) — escalate manually: gt escalate --severity medium %q\n", err, msg)
			continue
		}
		raised = append(raised, msg)
	}
	return raised
}

// runDogDispatch dispatches plugin execution to a dog worker.
func runDogDispatch(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	// Get rig names for plugin scanner
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return fmt.Errorf("loading rigs config: %w", err)
	}

	var rigNames []string
	for rigName := range rigsConfig.Rigs {
		rigNames = append(rigNames, rigName)
	}

	// If --rig specified, search only that rig
	if dogDispatchRig != "" {
		rigNames = []string{dogDispatchRig}
	}

	// Find the plugin using scanner
	scanner := plugin.NewScanner(townRoot, rigNames)
	p, err := scanner.GetPlugin(dogDispatchPlugin)
	if err != nil {
		return fmt.Errorf("finding plugin: %w", err)
	}

	// Get dog manager (reuse rigsConfig from above)
	mgr := dog.NewManager(townRoot, rigsConfig)

	// Find target dog
	var targetDog *dog.Dog
	var dogCreated bool
	if dogDispatchDog != "" {
		// Specific dog requested
		targetDog, err = mgr.Get(dogDispatchDog)
		if err != nil {
			return fmt.Errorf("getting dog %s: %w", dogDispatchDog, err)
		}
		if targetDog.State == dog.StateWorking {
			return fmt.Errorf("dog %s is already working", dogDispatchDog)
		}
	} else {
		// Find idle dog from pool
		targetDog, err = mgr.GetIdleDog()
		if err != nil {
			return fmt.Errorf("finding idle dog: %w", err)
		}

		if targetDog == nil {
			if dogDispatchCreate {
				// Create a new dog (reuse generateDogName from sling_dog.go)
				newName := generateDogName(mgr)
				if dogDispatchDryRun {
					targetDog = &dog.Dog{Name: newName, State: dog.StateIdle}
					dogCreated = true
				} else {
					targetDog, err = mgr.Add(newName)
					if err != nil {
						return fmt.Errorf("creating dog %s: %w", newName, err)
					}
					dogCreated = true

					// Create agent bead for the dog
					b := beads.New(townRoot)
					location := filepath.Join("deacon", "dogs", newName)
					if _, beadErr := b.CreateDogAgentBead(newName, location); beadErr != nil {
						// Non-fatal warning
						if !dogDispatchJSON {
							fmt.Printf("  Warning: could not create agent bead: %v\n", beadErr)
						}
					}
				}
			} else {
				return fmt.Errorf("no idle dogs available (use --create to add one)")
			}
		}
	}

	// Prepare dispatch result for JSON output
	workDesc := fmt.Sprintf("plugin:%s", p.Name)
	result := dogDispatchResult{
		Plugin:     p.Name,
		PluginPath: p.Path,
		Dog:        targetDog.Name,
		DogCreated: dogCreated,
		Work:       workDesc,
		DryRun:     dogDispatchDryRun,
	}
	if p.RigName != "" {
		result.PluginRig = p.RigName
	}

	// Dry-run mode: show what would happen and exit
	if dogDispatchDryRun {
		if dogDispatchJSON {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		fmt.Printf("Dry run - would dispatch:\n")
		fmt.Printf("  Plugin: %s\n", p.Name)
		if p.RigName != "" {
			fmt.Printf("  Location: %s/plugins/%s\n", p.RigName, p.Name)
		} else {
			fmt.Printf("  Location: plugins/%s (town-level)\n", p.Name)
		}
		fmt.Printf("  Dog: %s%s\n", targetDog.Name, ifStr(dogCreated, " (would create)", ""))
		fmt.Printf("  Work: %s\n", workDesc)
		return nil
	}

	// Ensure dog has an agent bead before sending mail.
	// Dogs created before agent beads were added, or whose bead creation
	// failed silently, won't have one. The mail router requires agent beads
	// to validate recipients.
	b := beads.New(townRoot)
	if existing, _ := b.FindDogAgentBead(targetDog.Name); existing == nil {
		location := filepath.Join("deacon", "dogs", targetDog.Name)
		if _, beadErr := b.CreateDogAgentBead(targetDog.Name, location); beadErr != nil {
			if !dogDispatchJSON {
				fmt.Printf("  Warning: could not create agent bead: %v\n", beadErr)
			}
		}
	}

	// Assign work FIRST (before sending mail) to prevent race condition
	// If this fails, we haven't sent any mail yet
	if err := mgr.AssignWork(targetDog.Name, workDesc); err != nil {
		return fmt.Errorf("assigning work to dog: %w", err)
	}

	// Decide the delivery path BEFORE sending, because sending is what starts
	// the race.
	//
	// router.Send fires its tmux notification on a background goroutine, and
	// session startup delivers the dog's instructions as the agent's initial
	// prompt. When both happen for the same pane, the notification's
	// WaitForIdle sees a freshly-booted agent as idle and types into a turn
	// that is already in flight — the second delivery interrupts the first and
	// the instruction is destroyed. The nudge lock serialises nudge against
	// nudge; it cannot see a startup prompt.
	//
	// So exactly one path delivers: if we are about to start the session, its
	// startup prompt is the delivery and the mail rides silently; if the
	// session is already up, the mail's notification is the delivery and we
	// start nothing.
	t := tmux.NewTmux()
	sessMgr := dog.NewSessionManager(t, townRoot, mgr)
	plan := planDispatchDelivery(sessMgr.IsRunning(targetDog.Name))

	// Create and send mail message with plugin instructions
	dogAddress := dog.DogAddress(targetDog.Name)
	subject := dog.DispatchSubjectPrefix + p.Name
	body := p.FormatMailBody()

	router := mail.NewRouterWithTownRoot(townRoot, townRoot)
	defer router.WaitPendingNotifications()
	msg := &mail.Message{
		From:      "deacon/",
		To:        dogAddress,
		Subject:   subject,
		Body:      body,
		Timestamp: time.Now(),
		// Suppress the async nudge when the startup prompt will carry the
		// wakeup. See planDispatchDelivery.
		SuppressNotify: plan.suppressMailNotify,
	}
	result.NotifiedViaMail = !plan.suppressMailNotify
	result.DeliveryPath = plan.reason

	if err := router.Send(msg); err != nil {
		// Rollback: clear work assignment since mail failed
		if clearErr := mgr.ClearWork(targetDog.Name); clearErr != nil {
			// Log rollback failure but return original error
			if !dogDispatchJSON {
				fmt.Printf("  Warning: rollback failed: %v\n", clearErr)
			}
		}
		return fmt.Errorf("sending plugin mail to dog: %w", err)
	}

	// Ensure dog session is running so it can read the mail.
	// Without this, dispatched work sits in mail with no session to read it.
	sessOpts := dog.SessionStartOptions{
		WorkDesc: workDesc,
	}
	result.SessionStarted = true
	if _, sessErr := sessMgr.EnsureRunning(targetDog.Name, sessOpts); sessErr != nil {
		result.SessionStarted = false
		// Roll back the work assignment: without a running session the dog
		// cannot read its mail, leaving it stuck in StateWorking (zombie).
		// Clearing work returns it to idle so it can be re-dispatched.
		// See: github.com/steveyegge/gastown/issues/2748
		if clearErr := mgr.ClearWork(targetDog.Name); clearErr != nil {
			warn := fmt.Sprintf("session start failed AND rollback failed for dog %s — dog stuck in StateWorking, run: gt dog health-check --auto-clear: %v", targetDog.Name, clearErr)
			result.Warnings = append(result.Warnings, warn)
			if !dogDispatchJSON {
				style.PrintWarning("%s", warn)
			}
		}
		// The mail is already durable but no session will ever read it.
		// Archive it here rather than leaving an open dispatch assigned to a
		// dog that is going back to idle — that orphan is mechanism B.
		if mailbox, mbErr := router.GetMailbox(dogAddress); mbErr == nil {
			if n, reclaimErr := dog.ReclaimDispatchMail(mailbox, dogAddress); reclaimErr != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf(
					"dispatch mail reclaim incomplete for %s (%d archived): %v", targetDog.Name, n, reclaimErr))
			} else {
				result.DispatchesReclaimed = n
			}
		}
		warn := fmt.Sprintf("dog dispatch: session start failed for %s (work rolled back, re-dispatch with: gt dog dispatch --plugin %s): %v", targetDog.Name, p.Name, sessErr)
		result.Warnings = append(result.Warnings, warn)
		if !dogDispatchJSON {
			style.PrintWarning("%s", warn)
		}
		if escErr := dogEscalateBestEffort(warn); escErr != nil {
			if !dogDispatchJSON {
				style.PrintWarning("escalation also failed (%v) — escalate manually: gt escalate --severity medium %q", escErr, warn)
			}
		}
	}

	// Verify the work state write is readable. A read-back failure here
	// indicates state corruption, not a timing race.
	// See: github.com/steveyegge/gastown/issues/2748
	result.WorkConfirmed = false
	if d, getErr := mgr.Get(targetDog.Name); getErr != nil {
		warn := fmt.Sprintf("dog dispatch: could not verify work assignment for %s: %v", targetDog.Name, getErr)
		result.Warnings = append(result.Warnings, warn)
		if !dogDispatchJSON {
			style.PrintWarning("%s", warn)
		}
		_ = dogEscalateBestEffort(warn)
	} else if d.Work != "" {
		result.WorkConfirmed = true
	} else {
		warn := fmt.Sprintf("dog dispatch: work assignment cleared for %s between dispatch and verify — re-dispatch required", targetDog.Name)
		result.Warnings = append(result.Warnings, warn)
		if !dogDispatchJSON {
			style.PrintWarning("%s", warn)
		}
		_ = dogEscalateBestEffort(warn)
	}

	// Success - output result
	if dogDispatchJSON {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	fmt.Printf("%s Found plugin: %s\n", style.Bold.Render("✓"), p.Name)
	if p.RigName != "" {
		fmt.Printf("  Location: %s/plugins/%s\n", p.RigName, p.Name)
	} else {
		fmt.Printf("  Location: plugins/%s (town-level)\n", p.Name)
	}
	if dogCreated {
		fmt.Printf("%s Created dog %s (pool was empty)\n", style.Bold.Render("✓"), targetDog.Name)
	}
	fmt.Printf("%s Dispatching to dog: %s\n", style.Bold.Render("🐕"), targetDog.Name)
	fmt.Printf("%s Plugin dispatched (non-blocking)\n", style.Bold.Render("✓"))
	fmt.Printf("  Dog: %s\n", targetDog.Name)
	fmt.Printf("  Work: %s\n", workDesc)
	fmt.Printf("  Delivery: %s\n", plan.reason)

	return nil
}

// dogDispatchResult is the JSON output for gt dog dispatch.
type dogDispatchResult struct {
	Plugin         string   `json:"plugin"`
	PluginRig      string   `json:"plugin_rig,omitempty"`
	PluginPath     string   `json:"plugin_path"`
	Dog            string   `json:"dog"`
	DogCreated     bool     `json:"dog_created,omitempty"`
	Work           string   `json:"work"`
	DryRun         bool     `json:"dry_run,omitempty"`
	SessionStarted bool     `json:"session_started"`
	WorkConfirmed  bool     `json:"work_confirmed"`
	Warnings       []string `json:"warnings,omitempty"`

	// NotifiedViaMail reports which path delivered the dispatch. True means
	// the session was already up and the mail notification woke it; false
	// means the session was started and its startup prompt was the delivery.
	// Exactly one path delivers — see planDispatchDelivery.
	NotifiedViaMail bool `json:"notified_via_mail"`

	// DeliveryPath explains the delivery choice in words.
	DeliveryPath string `json:"delivery_path,omitempty"`

	// DispatchesReclaimed counts dispatch mails archived during rollback.
	DispatchesReclaimed int `json:"dispatches_reclaimed,omitempty"`
}

// dogEscalateBestEffort fires a MEDIUM escalation via gt escalate.
func dogEscalateBestEffort(msg string) error {
	cmd := exec.Command("gt", "escalate", "--severity", "medium", msg)
	return cmd.Run()
}

// ifStr returns ifTrue if cond is true, otherwise ifFalse.
func ifStr(cond bool, ifTrue, ifFalse string) string {
	if cond {
		return ifTrue
	}
	return ifFalse
}
