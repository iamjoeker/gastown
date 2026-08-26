package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	patrolScanJSON    bool
	patrolScanNotify  bool
	patrolScanRig     string
	patrolScanVerbose bool
	patrolScanDryRun  bool
)

var patrolScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan polecats for zombies, stalls, and completions",
	Long: `Run proactive detection across all polecats in a rig.

This command bridges the witness library detection functions to the CLI,
providing a single command for the survey-workers patrol step.

Detections:
  - Zombies: Dead sessions with active agent state, dead agent processes,
    stuck done-intent, closed beads with live sessions
  - Stalls: Agents stuck at startup prompts
  - Completions: Agent bead metadata indicating gt done was called

Actions taken automatically:
  - Zombie restart: Sessions are restarted (not nuked) to preserve worktrees
  - Cleanup wisps: Created for dirty state tracking
  - Completion routing: MR cleanup wisps created, refinery nudged

Use --notify to send mail when zombies with active work are detected.
Long-running scan phases emit progress diagnostics to stderr so JSON stdout
remains machine-readable while operators can see where a slow patrol is stuck.

Use --dry-run to audit the scan without risking the work it acts on. The sweep
runs whole — every probe, every classification, and specifically the restart
guard — and reports the action it WOULD take per polecat, including the guard's
verdict (proceed / busy / session-gone). Nothing is restarted, nuked, nudged,
written, or mailed.

Examples:
  gt patrol scan                    # Scan current rig
  gt patrol scan --rig gastown      # Scan specific rig
  gt patrol scan --json             # Machine-readable output
  gt patrol scan --notify           # Send mail on zombie detection
  gt patrol scan --dry-run          # Report what it would do, change nothing`,
	RunE: runPatrolScan,
}

func init() {
	patrolScanCmd.Flags().BoolVar(&patrolScanJSON, "json", false, "Output as JSON")
	patrolScanCmd.Flags().BoolVar(&patrolScanNotify, "notify", false, "Send mail to witness/mayor when active-work zombies are detected")
	patrolScanCmd.Flags().StringVar(&patrolScanRig, "rig", "", "Rig to scan (default: infer from cwd or GT_RIG)")
	patrolScanCmd.Flags().BoolVarP(&patrolScanVerbose, "verbose", "v", false, "Verbose output")
	patrolScanCmd.Flags().BoolVar(&patrolScanDryRun, "dry-run", false, "Run the full scan and report the action it would take per polecat, without taking it")

	patrolCmd.AddCommand(patrolScanCmd)
}

var patrolScanProgressInterval = 10 * time.Second

// patrolScanPhaseTimeout bounds each detection phase.
//
// Every phase shells out — to bd, to git, to tmux — and none of those calls is
// individually bounded, so any one of them can park the whole command. A
// witness ran this on an idle rig with nothing to find and never came back out
// of completion discovery; it was killed at 5m0s with Dolt healthy and latency
// at 0s throughout (gt-nof6). Patrols run this every cycle, so a phase that
// hangs does not merely fail — it stops the agent that was supposed to notice.
//
// A timed-out phase is reported as incomplete rather than as empty. That
// distinction is the whole point: a nil result rendered as "no zombies found"
// is indistinguishable from a clean rig, and the clean-rig reading is the one
// that gets believed.
var patrolScanPhaseTimeout = 3 * time.Minute

// PatrolScanOutput is the JSON output format for patrol scan results.
type PatrolScanOutput struct {
	Rig       string `json:"rig"`
	Timestamp string `json:"timestamp"`
	// DryRun says whether every action below was declined rather than taken.
	// It is written unconditionally — an omitempty here would make "this was a
	// live scan" and "this field predates the flag" the same absent key, and the
	// consequence of reading the wrong one is believing work was restarted when
	// it was not, or the reverse (gt-3516).
	DryRun      bool                      `json:"dry_run"`
	Zombies     *PatrolScanZombieOutput   `json:"zombies"`
	Stalls      *PatrolScanStallOutput    `json:"stalls,omitempty"`
	Completions *PatrolScanCompleteOutput `json:"completions,omitempty"`
	Receipts    []witness.PatrolReceipt   `json:"receipts,omitempty"`
	// IncompletePhases names the phases that were abandoned on timeout. When it
	// is non-empty the counts above are a floor, not a total — the corresponding
	// section is absent because nothing was learned, not because nothing is
	// there. Consumers must not read this scan as an all-clear (gt-nof6).
	IncompletePhases []string `json:"incomplete_phases,omitempty"`
}

// PatrolScanZombieOutput holds zombie detection results.
type PatrolScanZombieOutput struct {
	Checked int `json:"checked"`
	Found   int `json:"found"`
	// RestartDecisions counts the polecats for which the restart guard actually
	// ran. It is written unconditionally, because a zero here is the load-bearing
	// case: it says the sweep reached no restart decision, which is a different
	// claim from "the guard decided not to restart anything" and a very different
	// one from "the guard is not wired up". Distinguishing those without risking a
	// live polecat is the whole point of --dry-run (gt-3516).
	RestartDecisions int                    `json:"restart_decisions"`
	Zombies          []PatrolScanZombieItem `json:"zombies,omitempty"`
	Errors           []string               `json:"errors,omitempty"`
}

// PatrolScanZombieItem is a single zombie detection in scan output.
type PatrolScanZombieItem struct {
	Polecat        string `json:"polecat"`
	Classification string `json:"classification"`
	AgentState     string `json:"agent_state"`
	HookBead       string `json:"hook_bead,omitempty"`
	CleanupStatus  string `json:"cleanup_status,omitempty"`
	Action         string `json:"action"`
	WasActive      bool   `json:"was_active"`
	// RestartVerdict is what the restart guard concluded at decision time:
	// "proceed", "busy", or "session-gone". Absent when this classification
	// reached no restart decision — which is a different thing from "proceed",
	// and the reason it is a field rather than a phrase inside Action.
	RestartVerdict string `json:"restart_verdict,omitempty"`
	Error          string `json:"error,omitempty"`
}

// PatrolScanStallOutput holds stall detection results.
type PatrolScanStallOutput struct {
	Checked int                   `json:"checked"`
	Found   int                   `json:"found"`
	Stalls  []PatrolScanStallItem `json:"stalls,omitempty"`
}

// PatrolScanStallItem is a single stall detection in scan output.
type PatrolScanStallItem struct {
	Polecat   string `json:"polecat"`
	StallType string `json:"stall_type"`
	Action    string `json:"action"`
	Error     string `json:"error,omitempty"`
}

// PatrolScanCompleteOutput holds completion discovery results.
type PatrolScanCompleteOutput struct {
	Checked   int                      `json:"checked"`
	Found     int                      `json:"found"`
	Completed []PatrolScanCompleteItem `json:"completed,omitempty"`
}

// PatrolScanCompleteItem is a single completion discovery in scan output.
type PatrolScanCompleteItem struct {
	Polecat        string `json:"polecat"`
	ExitType       string `json:"exit_type"`
	IssueID        string `json:"issue_id,omitempty"`
	MRID           string `json:"mr_id,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Action         string `json:"action"`
	WispCreated    string `json:"wisp_created,omitempty"`
	CompletionTime string `json:"completion_time,omitempty"`
}

func runPatrolScan(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine rig name
	rigName := patrolScanRig
	if rigName == "" {
		// Try GT_RIG env, then infer from cwd
		rigName = os.Getenv("GT_RIG")
		if rigName == "" {
			rigName, err = inferRigFromCwd(townRoot)
			if err != nil {
				return fmt.Errorf("could not determine rig: %w\nUse --rig to specify", err)
			}
		}
	}

	bd := witness.DefaultBdCli()
	opts := witness.ScanOptions{DryRun: patrolScanDryRun}
	if opts.DryRun {
		// Belt and braces. The sweep's own effects gate is what makes a dry run
		// inert; this wrapper turns any mutation that slipped past it into a
		// visible error instead of a silent write to the production bead store.
		bd = witness.ReadOnlyBdCli(bd)
	}
	router := mail.NewRouter(townRoot)
	workDir := townRoot

	timestamp := time.Now().UTC().Format(time.RFC3339)

	// Run all three detection passes.
	// Note: DetectZombiePolecats takes a router param but does NOT send mail
	// internally — it only uses the router for workspace context. Notifications
	// are sent exclusively below via --notify, avoiding double-send.
	diagnostics := cmd.ErrOrStderr()
	var incomplete []string
	recordPhase := func(reason string) {
		if reason != "" {
			incomplete = append(incomplete, reason)
		}
	}

	zombieResult, reason := runPatrolScanPhase(diagnostics, "zombie detection", func() *witness.DetectZombiePolecatsResult {
		return witness.DetectZombiePolecatsWithOptions(bd, workDir, rigName, router, opts)
	})
	recordPhase(reason)
	stallResult, reason := runPatrolScanPhase(diagnostics, "stall detection", func() *witness.DetectStalledPolecatsResult {
		return witness.DetectStalledPolecatsWithOptions(workDir, rigName, opts)
	})
	recordPhase(reason)
	completionResult, reason := runPatrolScanPhase(diagnostics, "completion discovery", func() *witness.DiscoverCompletionsResult {
		return witness.DiscoverCompletionsWithOptions(bd, workDir, rigName, router, opts)
	})
	recordPhase(reason)

	// Build patrol receipts for zombies
	receipts := witness.BuildPatrolReceipts(rigName, zombieResult)

	// Notify when zombies with active work are detected.
	// Always notify the mayor for active-work zombies (dead polecats with hooked
	// beads) — this is the primary mechanism for detecting failed work. (GH #3584)
	// Use --notify=false to suppress (e.g., in dry-run/testing contexts).
	// Mail is a mutation too: every send is a permanent bead and a Dolt commit,
	// and a dry run that pages the mayor about zombies it did not touch is not a
	// dry run.
	if zombieResult != nil && !opts.DryRun {
		activeZombies := countActiveWorkZombies(zombieResult)
		if activeZombies > 0 {
			sendZombieNotification(router, rigName, zombieResult, activeZombies)
		}
	}

	if patrolScanJSON {
		return outputPatrolScanJSON(rigName, timestamp, opts.DryRun, zombieResult, stallResult, completionResult, receipts, incomplete)
	}

	return outputPatrolScanHuman(rigName, opts.DryRun, zombieResult, stallResult, completionResult, receipts, incomplete)
}

// runPatrolScanPhase runs one detection phase under patrolScanPhaseTimeout,
// reporting progress to diagnostics while it waits.
//
// On timeout it returns the zero value and a non-empty reason. Callers must
// treat that reason as "this phase did not answer" and must not render the zero
// value as a finding of none. The phase's goroutine and whatever subprocess it
// is blocked on are left behind: the command is about to exit, and the child is
// in its own process group either way. Bounding the subprocess is the inner
// fix (see lsRemoteTimeout in internal/git); this is the backstop that keeps
// any future unbounded call from parking a patrol again.
func runPatrolScanPhase[T any](diagnostics io.Writer, name string, fn func() T) (T, string) {
	var zero T
	start := time.Now()
	if diagnostics != nil {
		fmt.Fprintf(diagnostics, "gt patrol scan: starting %s\n", name)
	}

	done := make(chan T, 1)
	go func() {
		done <- fn()
	}()

	deadline := time.NewTimer(patrolScanPhaseTimeout)
	defer deadline.Stop()

	// A nil channel blocks forever in select, so progress reporting switches off
	// by never becoming ready rather than by racing the deadline.
	var progress <-chan time.Time
	if patrolScanProgressInterval > 0 {
		ticker := time.NewTicker(patrolScanProgressInterval)
		defer ticker.Stop()
		progress = ticker.C
	}

	for {
		select {
		case result := <-done:
			if diagnostics != nil {
				fmt.Fprintf(diagnostics, "gt patrol scan: finished %s in %s\n", name, formatPatrolScanElapsed(time.Since(start)))
			}
			return result, ""
		case <-deadline.C:
			reason := fmt.Sprintf("%s timed out after %s", name, formatPatrolScanElapsed(patrolScanPhaseTimeout))
			if diagnostics != nil {
				fmt.Fprintf(diagnostics, "gt patrol scan: ABANDONED %s — results for this phase are unknown, not empty\n", reason)
			}
			return zero, reason
		case <-progress:
			if diagnostics != nil {
				fmt.Fprintf(diagnostics, "gt patrol scan: still running %s after %s\n", name, formatPatrolScanElapsed(time.Since(start)))
			}
		}
	}
}

func formatPatrolScanElapsed(elapsed time.Duration) string {
	if elapsed < time.Second {
		return elapsed.Round(time.Millisecond).String()
	}
	return elapsed.Round(time.Second).String()
}

func countActiveWorkZombies(result *witness.DetectZombiePolecatsResult) int {
	count := 0
	for _, z := range result.Zombies {
		if z.WasActive {
			count++
		}
	}
	return count
}

// countRestartDecisions counts the polecats whose restart guard actually ran.
// Most classifications never reach it — a healthy fleet produces zero — so this
// is what separates "the guard vetoed nothing" from "the guard was never asked".
func countRestartDecisions(result *witness.DetectZombiePolecatsResult) int {
	if result == nil {
		return 0
	}
	count := 0
	for _, z := range result.Zombies {
		if z.RestartVerdict != "" {
			count++
		}
	}
	return count
}

func sendZombieNotification(router *mail.Router, rigName string, result *witness.DetectZombiePolecatsResult, activeCount int) {
	var lines []string
	lines = append(lines, fmt.Sprintf("Patrol scan detected %d zombie(s) with active work in rig %s:", activeCount, rigName))
	lines = append(lines, "")
	for _, z := range result.Zombies {
		if !z.WasActive {
			continue
		}
		line := fmt.Sprintf("- %s: %s (hook=%s, action=%s)",
			z.PolecatName, string(z.Classification), z.HookBead, z.Action)
		if z.Error != nil {
			line += fmt.Sprintf(" [error: %v]", z.Error)
		}
		lines = append(lines, line)
	}

	body := strings.Join(lines, "\n")
	subject := fmt.Sprintf("ZOMBIE_DETECTED: %d active-work zombie(s) in %s", activeCount, rigName)

	// Send to witness (best-effort)
	witMsg := &mail.Message{
		From:    fmt.Sprintf("%s/witness", rigName),
		To:      fmt.Sprintf("%s/witness", rigName),
		Subject: subject,
		Body:    body,
	}
	_ = router.Send(witMsg)

	// Also notify the mayor so dead polecats don't go unnoticed. (GH #3584)
	// The mayor needs to know so work can be reslung.
	mayorBody := strings.Join(lines, "\n") +
		"\n\nResling instructions:\n" +
		"  gt sling <bead-id> <rig> --create --force"
	mayorMsg := &mail.Message{
		From:    fmt.Sprintf("%s/witness", rigName),
		To:      "mayor/",
		Subject: fmt.Sprintf("POLECAT_DIED: %d polecat(s) died with active work in %s", activeCount, rigName),
		Body:    mayorBody,
	}
	_ = router.Send(mayorMsg)
}

func outputPatrolScanJSON(rigName, timestamp string, dryRun bool, zombieResult *witness.DetectZombiePolecatsResult, stallResult *witness.DetectStalledPolecatsResult, completionResult *witness.DiscoverCompletionsResult, receipts []witness.PatrolReceipt, incompletePhases []string) error {
	output := PatrolScanOutput{
		Rig:              rigName,
		Timestamp:        timestamp,
		DryRun:           dryRun,
		Receipts:         receipts,
		IncompletePhases: incompletePhases,
	}

	// Zombies
	if zombieResult != nil {
		zo := &PatrolScanZombieOutput{
			Checked:          zombieResult.Checked,
			Found:            len(zombieResult.Zombies),
			RestartDecisions: countRestartDecisions(zombieResult),
		}
		for _, z := range zombieResult.Zombies {
			item := PatrolScanZombieItem{
				Polecat:        z.PolecatName,
				Classification: string(z.Classification),
				AgentState:     z.AgentState,
				HookBead:       z.HookBead,
				CleanupStatus:  z.CleanupStatus,
				Action:         z.Action,
				WasActive:      z.WasActive,
				RestartVerdict: z.RestartVerdict,
			}
			if z.Error != nil {
				item.Error = z.Error.Error()
			}
			zo.Zombies = append(zo.Zombies, item)
		}
		for _, e := range zombieResult.Errors {
			zo.Errors = append(zo.Errors, e.Error())
		}
		output.Zombies = zo
	}

	// Stalls
	if stallResult != nil {
		so := &PatrolScanStallOutput{
			Checked: stallResult.Checked,
			Found:   len(stallResult.Stalled),
		}
		for _, s := range stallResult.Stalled {
			item := PatrolScanStallItem{
				Polecat:   s.PolecatName,
				StallType: s.StallType,
				Action:    s.Action,
			}
			if s.Error != nil {
				item.Error = s.Error.Error()
			}
			so.Stalls = append(so.Stalls, item)
		}
		output.Stalls = so
	}

	// Completions
	if completionResult != nil {
		co := &PatrolScanCompleteOutput{
			Checked: completionResult.Checked,
			Found:   len(completionResult.Discovered),
		}
		for _, d := range completionResult.Discovered {
			item := PatrolScanCompleteItem{
				Polecat:        d.PolecatName,
				ExitType:       d.ExitType,
				IssueID:        d.IssueID,
				MRID:           d.MRID,
				Branch:         d.Branch,
				Action:         d.Action,
				WispCreated:    d.WispCreated,
				CompletionTime: d.CompletionTime,
			}
			co.Completed = append(co.Completed, item)
		}
		output.Completions = co
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

// patrolScanActionLabel names the "Action:" column. In a dry run every action
// listed is one the scan declined to take, and the label has to say so on every
// line: a reader who scrolls past a one-line banner and then sees
// "Action: restarted" will believe a polecat was restarted.
func patrolScanActionLabel(dryRun bool) string {
	if dryRun {
		return "WOULD"
	}
	return "Action"
}

func outputPatrolScanHuman(rigName string, dryRun bool, zombieResult *witness.DetectZombiePolecatsResult, stallResult *witness.DetectStalledPolecatsResult, completionResult *witness.DiscoverCompletionsResult, _ []witness.PatrolReceipt, incompletePhases []string) error {
	fmt.Printf("%s Patrol scan: %s\n", style.Bold.Render("🔍"), rigName)
	if dryRun {
		fmt.Printf("%s DRY RUN — nothing below was restarted, nuked, nudged, written, or mailed.\n",
			style.Bold.Render("🧪"))
	}
	fmt.Println()
	actionLabel := patrolScanActionLabel(dryRun)

	// Zombies
	if zombieResult != nil {
		fmt.Printf("%s Zombie Detection: checked %d polecat(s)\n",
			style.Bold.Render("👻"), zombieResult.Checked)

		if len(zombieResult.Zombies) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No zombies detected"))
		} else {
			for _, z := range zombieResult.Zombies {
				icon := "⚠"
				if z.WasActive {
					icon = "🚨"
				}
				fmt.Printf("  %s %s: %s\n", icon, z.PolecatName, z.Classification)
				fmt.Printf("    State: %s", z.AgentState)
				if z.HookBead != "" {
					fmt.Printf("  Hook: %s", z.HookBead)
				}
				if z.CleanupStatus != "" {
					fmt.Printf("  Cleanup: %s", z.CleanupStatus)
				}
				fmt.Println()
				// The restart guard's verdict is the thing a dry run exists to
				// surface, so it gets its own line rather than living inside the
				// action prose (gt-3516).
				if z.RestartVerdict != "" {
					fmt.Printf("    Restart guard: %s\n", z.RestartVerdict)
				}
				fmt.Printf("    %s: %s\n", actionLabel, z.Action)
				if z.Error != nil {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", z.Error)))
				}
			}
		}

		// State the guard's reach explicitly. A dry run whose whole purpose is to
		// audit the restart guard must not report a silence that reads the same
		// whether the guard vetoed nothing or was never asked (gt-3516).
		if dryRun {
			decisions := countRestartDecisions(zombieResult)
			if decisions == 0 {
				fmt.Printf("  %s\n", style.Dim.Render(
					"Restart guard: reached 0 restart decisions — no polecat carried a verdict that would restart it"))
			} else {
				fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf(
					"Restart guard: reached %d restart decision(s); each verdict is reported above", decisions)))
			}
		}

		if len(zombieResult.Errors) > 0 && patrolScanVerbose {
			fmt.Printf("  Errors: %d\n", len(zombieResult.Errors))
			for _, e := range zombieResult.Errors {
				fmt.Printf("    - %v\n", e)
			}
		}

		if len(zombieResult.ConvoyFailures) > 0 {
			fmt.Printf("  Convoy failures: %d\n", len(zombieResult.ConvoyFailures))
		}
		fmt.Println()
	}

	// Stalls
	if stallResult != nil && (len(stallResult.Stalled) > 0 || patrolScanVerbose) {
		fmt.Printf("%s Stall Detection: checked %d polecat(s)\n",
			style.Bold.Render("⏳"), stallResult.Checked)

		if len(stallResult.Stalled) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No stalls detected"))
		} else {
			for _, s := range stallResult.Stalled {
				fmt.Printf("  ⚠ %s: %s → %s: %s\n", s.PolecatName, s.StallType, actionLabel, s.Action)
				if s.Error != nil {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", s.Error)))
				}
			}
		}
		fmt.Println()
	}

	// Completions
	if completionResult != nil && (len(completionResult.Discovered) > 0 || patrolScanVerbose) {
		fmt.Printf("%s Completion Discovery: checked %d polecat(s)\n",
			style.Bold.Render("✅"), completionResult.Checked)

		if len(completionResult.Discovered) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No completions discovered"))
		} else {
			for _, d := range completionResult.Discovered {
				fmt.Printf("  ● %s: exit=%s", d.PolecatName, d.ExitType)
				if d.IssueID != "" {
					fmt.Printf("  issue=%s", d.IssueID)
				}
				if d.MRID != "" {
					fmt.Printf("  mr=%s", d.MRID)
				}
				fmt.Println()
				fmt.Printf("    %s: %s\n", actionLabel, d.Action)
			}
		}
		fmt.Println()
	}

	// Summary
	zombieCount := 0
	activeCount := 0
	if zombieResult != nil {
		zombieCount = len(zombieResult.Zombies)
		activeCount = countActiveWorkZombies(zombieResult)
	}
	stallCount := 0
	if stallResult != nil {
		stallCount = len(stallResult.Stalled)
	}
	completionCount := 0
	if completionResult != nil {
		completionCount = len(completionResult.Discovered)
	}

	// An abandoned phase contributes zero to every count above, so the summary
	// must say so before it says anything else. "All clear" is reserved for a
	// scan that actually finished looking (gt-nof6).
	if len(incompletePhases) > 0 {
		fmt.Printf("%s Scan INCOMPLETE — these phases were abandoned and reported nothing:\n",
			style.Bold.Render("⚠"))
		for _, reason := range incompletePhases {
			fmt.Printf("  • %s\n", reason)
		}
		fmt.Printf("  %s\n", style.Dim.Render("Counts below are a floor. Do not read this scan as an all-clear."))
		fmt.Printf("Summary (PARTIAL): %d zombie(s) (%d active-work), %d stall(s), %d completion(s)\n",
			zombieCount, activeCount, stallCount, completionCount)
		return nil
	}

	if zombieCount == 0 && stallCount == 0 && completionCount == 0 {
		fmt.Printf("%s All clear — no issues detected\n", style.Success.Render("✓"))
		return nil
	}

	if dryRun {
		fmt.Printf("Summary (DRY RUN — no action taken): %d zombie(s) (%d active-work), %d stall(s), %d completion(s)\n",
			zombieCount, activeCount, stallCount, completionCount)
		return nil
	}
	fmt.Printf("Summary: %d zombie(s) (%d active-work), %d stall(s), %d completion(s)\n",
		zombieCount, activeCount, stallCount, completionCount)

	return nil
}
