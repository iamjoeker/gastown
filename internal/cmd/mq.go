package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
)

// MQ command flags
var (
	// Submit flags
	mqSubmitBranch    string
	mqSubmitIssue     string
	mqSubmitEpic      string
	mqSubmitPriority  int
	mqSubmitNoCleanup bool
	mqSubmitSkipDeps  bool
	mqSubmitResubmit  bool
	// mqSubmitAllowClosedIssue is an operator escape for the gt-7qm gate, for
	// when the source issue's close was itself the mistake.
	mqSubmitAllowClosedIssue bool

	// Retry flags
	mqRetryNow bool

	// Reject flags
	mqRejectReason string
	mqRejectNotify bool
	mqRejectStdin  bool // Read reason from stdin

	// List command flags
	mqListReady      bool
	mqListStatus     string
	mqListWorker     string
	mqListEpic       string
	mqListJSON       bool
	mqListVerify     bool
	mqListMergeCheck bool

	// Status command flags
	mqStatusJSON bool

	// Integration land flags
	mqIntegrationLandForce     bool
	mqIntegrationLandSkipTests bool
	mqIntegrationLandDryRun    bool

	// Integration status flags
	mqIntegrationStatusJSON bool

	// Integration create flags
	mqIntegrationCreateBranch     string
	mqIntegrationCreateBaseBranch string
	mqIntegrationCreateForce      bool
)

var mqCmd = &cobra.Command{
	Use:     "mq",
	Aliases: []string{"mr"},
	GroupID: GroupWork,
	Short:   "Merge queue operations",
	RunE:    requireSubcommand,
	Long: `Manage merge requests and the merge queue for a rig.

Alias: 'gt mr' is equivalent to 'gt mq' (merge request vs merge queue).

The merge queue tracks work branches from polecats waiting to be merged.
Use these commands to view, submit, retry, and manage merge requests.`,
}

var mqSubmitCmd = &cobra.Command{
	Use:   "submit",
	Short: "Submit current branch to the merge queue",
	Long: `Submit the current branch to the merge queue.

Creates a merge-request bead that will be processed by the Refinery.

Auto-detection:
  - Branch: current git branch
  - Issue: parsed from branch name (e.g., polecat/Nux/gp-xyz → gt-xyz)
  - Worker: parsed from branch name
  - Rig: detected from current directory
  - Target: automatically determined (see below)
  - Priority: inherited from source issue

Target branch auto-detection:
  1. If --epic is specified: target the integration branch for <epic> (using configured template)
  2. If source issue has a parent epic with an integration branch: target it
  3. Otherwise: target main

This ensures batch work on epics automatically flows to integration branches.

Polecat auto-cleanup:
  When run from a polecat work branch (polecat/<worker>/<issue>), this command
  automatically triggers polecat shutdown after submitting the MR. The polecat
  sends a lifecycle request to its Witness and waits for termination.

  Use --no-cleanup to disable this behavior (e.g., if you want to submit
  multiple MRs or continue working).

Examples:
  gt mq submit                           # Auto-detect everything + auto-cleanup
  gt mq submit --issue gp-abc            # Explicit issue
  gt mq submit --epic gt-xyz             # Target integration branch explicitly
  gt mq submit --priority 0              # Override priority (P0)
  gt mq submit --no-cleanup              # Submit without auto-cleanup`,
	RunE: runMqSubmit,
}

var mqRetryCmd = &cobra.Command{
	Use:   "retry <rig> <mr-id>",
	Short: "Retry a failed merge request",
	Long: `Retry a failed merge request.

Resets a failed MR so it can be processed again by the refinery.
The MR must be in a failed state (open with an error).

Examples:
  gt mq retry greenplace gp-mr-abc123
  gt mq retry greenplace gp-mr-abc123 --now`,
	Args: cobra.ExactArgs(2),
	RunE: runMQRetry,
}

var mqListCmd = &cobra.Command{
	Use:   "list <rig>",
	Short: "Show the merge queue",
	Long: `Show the merge queue for a rig.

Lists pending merge requests waiting to be processed. The default scope is
status=open, so merged and rejected MRs — which are closed — are NOT shown.

This is the ONLY correct surface for auditing merge requests, and the header
names the store and status scope it queried so an empty result is falsifiable.
MR records are wisps in the RIG's store, which is why the other obvious probes
answer zero for MRs that exist (gt-kb63):

  bd list --label gt:merge-request   MRs are wisps; bd list reads issues.
                                     No filter on bd list can ever reach them.
  bd mol wisp list                   Reads the store of the CWD, and is
                                     open-only without --all. From the town
                                     root it cannot see rig wisps at all.

To audit merged MRs, ask for the closed ones explicitly:

  gt mq list <rig> --status closed

Output format:
  ID          STATUS       PRIORITY  BRANCH                    WORKER  AGE
  gt-mr-001   ready        P0        polecat/Nux/gp-xyz        Nux     5m
  gt-mr-002   in_progress  P1        polecat/Toast/gt-abc      Toast   12m
  gt-mr-003   blocked      P1        polecat/Capable/gt-def    Capable 8m
              (waiting on gt-mr-001)

--verify answers REACHABILITY AND NON-EMPTINESS ONLY. It is not a merge verdict.
The GIT column reports:
  PRESENT  Branch exists and carries at least one commit over its target
  EMPTY    Branch exists but is not ahead of its target — merging it is a no-op
  MISSING  Neither a local nor an origin ref for the branch exists
  ERR      git could not answer (unresolvable target, unreadable repo)

EMPTY is a rejection, not a merge: an empty branch rehearses and tests cleanly,
so every downstream gate reports success while zero lines change (gt-d5u).

PRESENT is not clearance to merge. The good state used to be spelled OK, and in
a column called GIT beside a STATUS column reading "ready" it was taken for a
merge verdict — two branches so reported conflicted in 17 and 12 files, because
their merge base was 700 commits back (gt-0w2l). Existence and mergeability are
different questions and --verify only asks the first.

To ask the second, use --merge-check. It runs a real three-way merge of each
branch into its target with git merge-tree, in the object store only — no
worktree, no index, nothing to unwind — and adds a MERGE column:
  CLEAN          The merge produces a tree with no conflicts
  CONFLICTS=<n>  The merge stops on n conflicted files
  ERR            git could not rehearse it — UNKNOWN, not clean
  -              Not rehearsed (branch was MISSING, EMPTY or ERR)

Examples:
  gt mq list greenplace
  gt mq list greenplace --ready
  gt mq list greenplace --status=open
  gt mq list greenplace --status=closed    # merged/rejected MRs (audit)
  gt mq list greenplace --status=all       # every MR regardless of status
  gt mq list greenplace --worker=Nux
  gt mq list greenplace --verify           # can it be reached? is it non-empty?
  gt mq list greenplace --merge-check      # will it actually merge?`,
	Args: cobra.ExactArgs(1),
	RunE: runMQList,
}

var mqRejectCmd = &cobra.Command{
	Use:   "reject <rig> <mr-id-or-branch>",
	Short: "Reject a merge request",
	Long: `Manually reject a merge request.

This closes the MR with a 'rejected' status without merging.

A rejection means the work is not done, so the source issue must be able to be
re-slung. Reject never closes it, and if it is already CLOSED — a polecat that
closed its own bead before submitting, or any other route — reject REOPENS it.
Leaving it closed strands the branch on origin with nothing able to re-dispatch
the work (gt-a46b). The Issue: line reports the issue's actual status.

Tombstoned issues and non-work beads (wisps, agent beads) are reported, never
rewritten.

Examples:
  gt mq reject greenplace polecat/Nux/gp-xyz --reason "Does not meet requirements"
  gt mq reject greenplace mr-Nux-12345 --reason "Superseded by other work" --notify`,
	Args: cobra.ExactArgs(2),
	RunE: runMQReject,
}

// Post-merge flags
var mqPostMergeSkipBranchDelete bool

var mqPostMergeCmd = &cobra.Command{
	Use:   "post-merge <rig> <mr-id>",
	Short: "Run post-merge cleanup (close MR, delete branch)",
	Long: `Perform post-merge cleanup after a successful merge.

This command consolidates post-merge steps into a single atomic operation:
	 1. Verify the target branch contains the submitted source head
	 2. Close the MR bead (status: merged)
	 3. Close the source issue
	 4. Delete the remote polecat branch (unless --skip-branch-delete)

The delete is leased against the branch's head as it stands now, not the sha
recorded at submission: a branch refreshed to resolve a conflict has moved on,
and that head is verified contained in the target before the branch goes.

Steps 2-4 are not one transaction. When the branch delete fails, the steps that
did land are printed and the outstanding one is named — re-running reports the
earlier steps as already done and cannot tell you about the branch.

Designed for use by the refinery formula after a successful merge to main.
The branch name is read from the MR bead, so no manual branch argument is needed.

Examples:
  gt mq post-merge gastown gt-mr-abc123
  gt mq post-merge gastown gt-mr-abc123 --skip-branch-delete`,
	Args: cobra.ExactArgs(2),
	RunE: runMQPostMerge,
}

type mqPostMergeManager interface {
	FindMRForPostMerge(idOrBranch string) (*refinery.MergeRequest, error)
	PostMergeMR(mr *refinery.MergeRequest) (*refinery.PostMergeResult, error)
}

type mqPostMergeGit interface {
	VerifyPushedCommitReachableFromPushTarget(remote, branch, commit string) error
	ResolveMergedBranchDeleteHead(remote, branch, target, recordedHead string) (string, error)
	HasOpenPullRequest(ref git.PullRequestRef) bool
	Rev(ref string) (string, error)
	DeleteRemoteBranchIfAt(remote, branch, expectedHash string) error
	DeleteBranch(branch string, force bool) error
}

type mqPostMergeBranchCleanup struct {
	// Attempted records that the branch-cleanup step ran at all, which is what
	// separates a cleanup failure (MR and issue already closed, branch left
	// behind) from a failure before either was touched.
	Attempted     bool
	Branch        string
	NoBranch      bool
	Skipped       bool
	Disabled      bool
	OpenPR        bool
	AlreadyGone   bool
	RemoteDeleted bool
	LocalDeleted  bool
	// RefreshedHead is the branch head the delete was leased against when it
	// differs from the MR's recorded commit_sha — the branch was refreshed
	// after submission and that head was verified contained in the target.
	RefreshedHead string
}

var mqStatusCmd = &cobra.Command{
	Use:   "status <id>",
	Short: "Show detailed merge request status",
	Long: `Display detailed information about a merge request.

Shows all MR fields, current status with timestamps, dependencies,
blockers, and processing history.

Example:
  gt mq status gp-mr-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runMqStatus,
}

var mqIntegrationCmd = &cobra.Command{
	Use:   "integration",
	Short: "Manage integration branches for epics",
	RunE:  requireSubcommand,
	Long: `Manage integration branches for batch work on epics.

Integration branches allow multiple MRs for an epic to target a shared
branch instead of main. After all epic work is complete, the integration
branch is landed to main as a single atomic unit.

Commands:
  create  Create an integration branch for an epic
  land    Merge integration branch to main
  status  Show integration branch status`,
}

var mqIntegrationCreateCmd = &cobra.Command{
	Use:   "create <epic-id>",
	Short: "Create an integration branch for an epic",
	Long: `Create an integration branch for batch work on an epic.

Creates a branch from main and pushes it to origin. Future MRs for this
epic's children can target this branch.

Branch naming:
  Default: integration/<sanitized-title> (e.g., integration/add-user-auth)
  Config:  Set merge_queue.integration_branch_template in rig settings
  Override: Use --branch flag for one-off customization

Template variables:
  {title}  - Sanitized epic title (e.g., "add-user-authentication")
  {epic}   - Full epic ID (e.g., "RA-123")
  {prefix} - Epic prefix before first hyphen (e.g., "RA")
  {user}   - Git user.name (e.g., "klauern")

If two epics produce the same branch name, a numeric suffix from the
epic ID is appended automatically (e.g., integration/add-auth-123).

Actions:
  1. Verify epic exists
  2. Create branch from main (using template or --branch)
  3. Push to origin
  4. Store actual branch name in epic metadata

Examples:
  gt mq integration create gt-auth-epic
  # Creates integration/add-user-authentication (from epic title)

  gt mq integration create RA-123 --branch "klauern/PROJ-1234/{epic}"
  # Creates klauern/PROJ-1234/RA-123`,
	Args: cobra.ExactArgs(1),
	RunE: runMqIntegrationCreate,
}

var mqIntegrationLandCmd = &cobra.Command{
	Use:   "land <epic-id>",
	Short: "Merge integration branch to main",
	Long: `Merge an epic's integration branch to main.

Lands all work for an epic by merging its integration branch to main
as a single atomic merge commit.

Actions:
  1. Verify all MRs targeting integration/<epic> are merged
  2. Verify integration branch exists
  3. Merge integration/<epic> to main (--no-ff)
  4. Run tests on main
  5. Push to origin
  6. Delete integration branch
  7. Update epic status

Options:
  --force       Land even if some MRs still open
  --skip-tests  Skip test run
  --dry-run     Preview only, make no changes

Examples:
  gt mq integration land gt-auth-epic
  gt mq integration land gt-auth-epic --dry-run
  gt mq integration land gt-auth-epic --force --skip-tests`,
	Args: cobra.ExactArgs(1),
	RunE: runMqIntegrationLand,
}

var mqIntegrationStatusCmd = &cobra.Command{
	Use:   "status <epic-id>",
	Short: "Show integration branch status for an epic",
	Long: `Display the status of an integration branch.

Shows:
  - Integration branch name and creation date
  - Number of commits ahead of main
  - Merged MRs (closed, targeting integration branch)
  - Pending MRs (open, targeting integration branch)

Example:
  gt mq integration status gt-auth-epic`,
	Args: cobra.ExactArgs(1),
	RunE: runMqIntegrationStatus,
}

func init() {
	// Submit flags
	mqSubmitCmd.Flags().StringVar(&mqSubmitBranch, "branch", "", "Source branch (default: current branch)")
	mqSubmitCmd.Flags().StringVar(&mqSubmitIssue, "issue", "", "Source issue ID (default: parse from branch name)")
	mqSubmitCmd.Flags().StringVar(&mqSubmitEpic, "epic", "", "Target epic's integration branch instead of main")
	mqSubmitCmd.Flags().IntVarP(&mqSubmitPriority, "priority", "p", -1, "Override priority (0-4, default: inherit from issue)")
	mqSubmitCmd.Flags().BoolVar(&mqSubmitNoCleanup, "no-cleanup", false, "Don't auto-cleanup after submit (for polecats)")
	mqSubmitCmd.Flags().BoolVar(&mqSubmitSkipDeps, "skip-deps", false, "Skip molecule step dependency check")
	mqSubmitCmd.Flags().BoolVar(&mqSubmitResubmit, "resubmit", false, "Resubmit after a fix (skips dependency check)")
	mqSubmitCmd.Flags().BoolVar(&mqSubmitAllowClosedIssue, "allow-closed-issue", false, "Create the MR even though the source issue is closed (operator override)")

	// Retry flags
	mqRetryCmd.Flags().BoolVar(&mqRetryNow, "now", false, "Immediately process instead of waiting for refinery loop")

	// List flags
	mqListCmd.Flags().BoolVar(&mqListReady, "ready", false, "Show only ready-to-merge (no blockers)")
	mqListCmd.Flags().StringVar(&mqListStatus, "status", "", "Filter by status (open, in_progress, closed)")
	mqListCmd.Flags().StringVar(&mqListWorker, "worker", "", "Filter by worker name")
	mqListCmd.Flags().StringVar(&mqListEpic, "epic", "", "Show MRs targeting integration/<epic>")
	mqListCmd.Flags().BoolVar(&mqListJSON, "json", false, "Output as JSON")
	mqListCmd.Flags().BoolVar(&mqListVerify, "verify", false, "Check branch REACHABILITY and non-emptiness only — PRESENT is not a merge verdict (MISSING for deleted branches, EMPTY for branches with no commits over their target). Use --merge-check for mergeability")
	mqListCmd.Flags().BoolVar(&mqListMergeCheck, "merge-check", false, "Rehearse each branch's merge into its target with git merge-tree and report CLEAN or CONFLICTS=<n> (implies --verify)")

	// Reject flags
	mqRejectCmd.Flags().StringVarP(&mqRejectReason, "reason", "r", "", "Reason for rejection (required unless --stdin)")
	mqRejectCmd.Flags().BoolVar(&mqRejectNotify, "notify", false, "Send mail notification to worker")
	mqRejectCmd.Flags().BoolVar(&mqRejectStdin, "stdin", false, "Read reason from stdin (avoids shell quoting issues)")

	// Status flags
	mqStatusCmd.Flags().BoolVar(&mqStatusJSON, "json", false, "Output as JSON")

	// Post-merge flags
	mqPostMergeCmd.Flags().BoolVar(&mqPostMergeSkipBranchDelete, "skip-branch-delete", false, "Skip remote branch deletion")

	// Blocker-priority flags
	mqBlockerPriorityCmd.Flags().BoolVar(&mqBlockerPriorityJSON, "json", false, "Show the derivation as JSON")

	// Add subcommands
	mqCmd.AddCommand(mqSubmitCmd)
	mqCmd.AddCommand(mqRetryCmd)
	mqCmd.AddCommand(mqListCmd)
	mqCmd.AddCommand(mqRejectCmd)
	mqCmd.AddCommand(mqStatusCmd)
	mqCmd.AddCommand(mqPostMergeCmd)
	mqCmd.AddCommand(mqBlockerPriorityCmd)

	// Integration branch subcommands
	mqIntegrationCreateCmd.Flags().StringVar(&mqIntegrationCreateBranch, "branch", "", "Override branch name template (supports {title}, {epic}, {prefix}, {user})")
	mqIntegrationCreateCmd.Flags().StringVar(&mqIntegrationCreateBaseBranch, "base-branch", "", "Create integration branch from this branch instead of main")
	mqIntegrationCreateCmd.Flags().BoolVar(&mqIntegrationCreateForce, "force", false, "Recreate integration branch even if one already exists")
	mqIntegrationCmd.AddCommand(mqIntegrationCreateCmd)

	// Integration land flags
	mqIntegrationLandCmd.Flags().BoolVar(&mqIntegrationLandForce, "force", false, "Land even if some MRs still open")
	mqIntegrationLandCmd.Flags().BoolVar(&mqIntegrationLandSkipTests, "skip-tests", false, "Skip test run")
	mqIntegrationLandCmd.Flags().BoolVar(&mqIntegrationLandDryRun, "dry-run", false, "Preview only, make no changes")
	mqIntegrationCmd.AddCommand(mqIntegrationLandCmd)

	// Integration status flags
	mqIntegrationStatusCmd.Flags().BoolVar(&mqIntegrationStatusJSON, "json", false, "Output as JSON")
	mqIntegrationCmd.AddCommand(mqIntegrationStatusCmd)

	mqCmd.AddCommand(mqIntegrationCmd)

	rootCmd.AddCommand(mqCmd)
}

// findCurrentRig determines the current rig from the working directory.
// Returns the rig name and rig object, or an error if not in a rig.
func findCurrentRig(townRoot string) (string, *rig.Rig, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, fmt.Errorf("getting current directory: %w", err)
	}

	// Get relative path from town root to cwd
	relPath, err := filepath.Rel(townRoot, cwd)
	if err != nil {
		return "", nil, fmt.Errorf("computing relative path: %w", err)
	}

	// The first component of the relative path should be the rig name
	parts := strings.Split(relPath, string(filepath.Separator))
	rigName := ""
	if len(parts) > 0 && parts[0] != "" && parts[0] != "." {
		rigName = parts[0]
	}

	// When gt is invoked via shell alias (cd ~/gt && gt), cwd is the town
	// root and relPath is ".". Fall back to GT_RIG env var.
	if rigName == "" {
		rigName = os.Getenv("GT_RIG")
	}
	if rigName == "" {
		return "", nil, fmt.Errorf("not inside a rig directory (and GT_RIG not set)")
	}

	// Load rig manager and get the rig
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return "", nil, fmt.Errorf("rig '%s' not found: %w", rigName, err)
	}

	return rigName, r, nil
}

func runMQRetry(cmd *cobra.Command, args []string) error {
	rigName := args[0]
	mrID := args[1]

	mgr, _, _, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// Get the MR first to show info
	mr, err := mgr.GetMR(mrID)
	if err != nil {
		if err == refinery.ErrMRNotFound {
			return fmt.Errorf("merge request '%s' not found in rig '%s'", mrID, rigName)
		}
		return fmt.Errorf("getting merge request: %w", err)
	}

	// Show what we're retrying
	fmt.Printf("Retrying merge request: %s\n", mrID)
	fmt.Printf("  Branch: %s\n", mr.Branch)
	fmt.Printf("  Worker: %s\n", mr.Worker)
	if mr.Error != "" {
		fmt.Printf("  Previous error: %s\n", style.Dim.Render(mr.Error))
	}

	// Perform the retry
	if err := mgr.Retry(mrID, mqRetryNow); err != nil {
		if err == refinery.ErrMRNotFailed {
			return fmt.Errorf("merge request '%s' has not failed (status: %s)", mrID, mr.Status)
		}
		return fmt.Errorf("retrying merge request: %w", err)
	}

	if mqRetryNow {
		fmt.Printf("%s Merge request processed\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("%s Merge request queued for retry\n", style.Bold.Render("✓"))
		fmt.Printf("  %s\n", style.Dim.Render("Will be processed on next refinery cycle"))
	}

	return nil
}

func runMQReject(cmd *cobra.Command, args []string) error {
	// Handle --stdin: read reason from stdin (avoids shell quoting issues)
	if mqRejectStdin {
		if mqRejectReason != "" {
			return fmt.Errorf("cannot use --stdin with --reason/-r")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		mqRejectReason = strings.TrimRight(string(data), "\n")
	}

	// Require reason via --reason or --stdin
	if mqRejectReason == "" {
		return fmt.Errorf("required flag \"reason\" not set (use --reason/-r or --stdin)")
	}

	rigName := args[0]
	mrIDOrBranch := args[1]

	mgr, _, _, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	result, err := mgr.RejectMR(mrIDOrBranch, mqRejectReason, mqRejectNotify)
	if err != nil {
		return fmt.Errorf("rejecting MR: %w", err)
	}

	fmt.Printf("%s Rejected: %s\n", style.Bold.Render("✗"), result.MR.Branch)
	fmt.Printf("  Worker: %s\n", result.MR.Worker)
	fmt.Printf("  Reason: %s\n", mqRejectReason)
	printRejectedIssueLine(result)

	if mqRejectNotify {
		fmt.Printf("  %s\n", style.Dim.Render("Worker notified via mail"))
	}

	return nil
}

// rejectedIssueReport is the source-issue outcome of a rejection, as plain text.
type rejectedIssueReport struct {
	// Note follows the issue ID on the Issue: line.
	Note string
	// Action is an operator instruction, empty when nothing needs doing by hand.
	Action string
}

// rejectedIssueReportFor describes the source issue's ACTUAL state after a
// rejection.
//
// The old wording ("not closed - work not done") described reject's own
// behaviour but read as an assertion that the bead was open, so a bead that was
// already closed looked available for re-dispatch when nothing could re-sling
// it (gt-a46b).
func rejectedIssueReportFor(result *refinery.RejectResult) rejectedIssueReport {
	id := result.SourceIssueID
	switch {
	case result.SourceIssueErr != nil:
		return rejectedIssueReport{
			Note: fmt.Sprintf("(status unknown: %v)", result.SourceIssueErr),
			Action: fmt.Sprintf(
				"Check it by hand: bd show %s - if closed, run: bd reopen %s", id, id),
		}
	case result.SourceIssueReopened:
		return rejectedIssueReport{
			Note: "(was closed - reopened: rejection means the work is not done)",
		}
	case result.SourceIssueSkipReason != "":
		return rejectedIssueReport{
			Note: fmt.Sprintf("(status: %s - not reopened: %s)",
				issueStatusLabel(result.SourceIssueStatus), result.SourceIssueSkipReason),
			Action: fmt.Sprintf(
				"This issue cannot be re-slung as-is. Resolve by hand: bd show %s", id),
		}
	default:
		return rejectedIssueReport{
			Note: fmt.Sprintf("(status: %s - reject did not close it)",
				issueStatusLabel(result.SourceIssueStatus)),
		}
	}
}

func printRejectedIssueLine(result *refinery.RejectResult) {
	if result.SourceIssueID == "" {
		return
	}
	report := rejectedIssueReportFor(result)
	fmt.Printf("  Issue:  %s %s\n", result.SourceIssueID, style.Dim.Render(report.Note))
	if report.Action != "" {
		fmt.Printf("  %s\n", style.Warning.Render(report.Action))
	}
}

func issueStatusLabel(status string) string {
	if strings.TrimSpace(status) == "" {
		return "unknown"
	}
	return status
}

func runMQPostMerge(_ *cobra.Command, args []string) error {
	rigName := args[0]
	mrID := args[1]

	mgr, r, _, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}
	rigGit, err := getRigGit(r.Path)
	if err != nil {
		return fmt.Errorf("post-merge proof: %w", err)
	}

	result, branchCleanup, err := runVerifiedMQPostMerge(mgr, r.Path, rigGit, mrID, mqPostMergeSkipBranchDelete)
	return reportMQPostMerge(os.Stdout, result, branchCleanup, err)
}

// reportMQPostMerge prints the per-step outcome of a post-merge run and returns
// the error the command should exit with.
//
// Branch cleanup runs after the MR and the source issue are closed, and is not
// part of that transaction: a failure there leaves cleanup half done. Returning
// only the error hid that, because re-running reports "already closed" for the
// finished steps and says nothing at all about the branch — the one outstanding
// step was visible only by going and looking at the ref. So print the steps that
// did land even on failure, and name the one that did not. (gt-yog2)
func reportMQPostMerge(w io.Writer, result *refinery.PostMergeResult, branchCleanup mqPostMergeBranchCleanup, cleanupErr error) error {
	if result == nil || result.MR == nil {
		if cleanupErr != nil {
			return fmt.Errorf("post-merge cleanup: %w", cleanupErr)
		}
		return nil
	}

	mr := result.MR
	marker := style.Bold.Render("✓")
	if cleanupErr != nil {
		marker = style.Warning.Render("⚠")
	}
	fmt.Fprintf(w, "%s Post-merge: %s\n", marker, mr.ID)
	fmt.Fprintf(w, "  Branch: %s\n", mr.Branch)
	fmt.Fprintf(w, "  Worker: %s\n", mr.Worker)

	if result.MRClosed {
		fmt.Fprintf(w, "  %s MR closed (merged)\n", style.Success.Render("✓"))
	}
	if result.SourceIssueClosed {
		fmt.Fprintf(w, "  %s Source issue closed: %s\n", style.Success.Render("✓"), result.SourceIssueID)
	} else if result.SourceIssueNotFound {
		fmt.Fprintf(w, "  %s Source issue: %s %s\n", style.Dim.Render("○"), result.SourceIssueID, style.Dim.Render("(already closed or not found)"))
	}

	branch := strings.TrimSpace(branchCleanup.Branch)
	if branch == "" {
		branch = strings.TrimSpace(mr.Branch)
	}

	switch {
	case cleanupErr != nil && !branchCleanup.Attempted:
		// Failed before branch cleanup ran (a merge-proof or MR-close failure):
		// the branch is untouched by design, so do not claim it is outstanding.
		return fmt.Errorf("post-merge cleanup: %w", cleanupErr)
	case cleanupErr != nil:
		fmt.Fprintf(w, "  %s Remote branch NOT deleted: %s\n", style.Error.Render("✗"), branch)
		fmt.Fprintf(w, "  %s\n", style.Warning.Render(fmt.Sprintf(
			"Outstanding: %s still needs deleting (the steps above are done — re-running will not report this).", branch)))
		return fmt.Errorf("post-merge cleanup incomplete for %s (MR and source issue closed; remote branch %s outstanding): %w", mr.ID, branch, cleanupErr)
	case branchCleanup.NoBranch:
		fmt.Fprintf(w, "  %s No branch name in MR (skipping branch delete)\n", style.Dim.Render("○"))
	case branchCleanup.Skipped:
		fmt.Fprintf(w, "  %s Branch delete skipped (--skip-branch-delete)\n", style.Dim.Render("○"))
	case branchCleanup.Disabled:
		fmt.Fprintf(w, "  %s Branch delete disabled by config\n", style.Dim.Render("○"))
	case branchCleanup.OpenPR:
		fmt.Fprintf(w, "  %s Skipping remote branch delete for %s: open PR exists (gas-fk4)\n", style.Dim.Render("○"), branch)
	case branchCleanup.AlreadyGone:
		fmt.Fprintf(w, "  %s Remote branch already absent: %s\n", style.Dim.Render("○"), branch)
	case branchCleanup.RemoteDeleted && branchCleanup.RefreshedHead != "":
		fmt.Fprintf(w, "  %s Deleted remote branch: %s %s\n", style.Success.Render("✓"), branch,
			style.Dim.Render(fmt.Sprintf("(refreshed to %s after submission; verified contained in %s)",
				shortSHA(branchCleanup.RefreshedHead), strings.TrimSpace(mr.TargetBranch))))
	case branchCleanup.RemoteDeleted:
		fmt.Fprintf(w, "  %s Deleted remote branch: %s\n", style.Success.Render("✓"), branch)
	}

	if branchCleanup.LocalDeleted {
		fmt.Fprintf(w, "  %s Deleted local branch: %s\n", style.Success.Render("✓"), branch)
	}

	return nil
}

func runVerifiedMQPostMerge(mgr mqPostMergeManager, rigPath string, rigGit mqPostMergeGit, mrID string, skipBranchDelete bool) (*refinery.PostMergeResult, mqPostMergeBranchCleanup, error) {
	mr, err := mgr.FindMRForPostMerge(mrID)
	if err != nil {
		return nil, mqPostMergeBranchCleanup{}, err
	}
	if err := verifyMQPostMergeProof(rigGit, mr); err != nil {
		return nil, mqPostMergeBranchCleanup{}, err
	}

	result, err := mgr.PostMergeMR(mr)
	if err != nil {
		return result, mqPostMergeBranchCleanup{}, err
	}

	branchCleanup, err := cleanupMQPostMergeBranch(rigPath, rigGit, result.MR, skipBranchDelete)
	return result, branchCleanup, err
}

func verifyMQPostMergeProof(rigGit mqPostMergeGit, mr *refinery.MergeRequest) error {
	if mr == nil {
		return fmt.Errorf("merge proof failed: merge request is missing")
	}
	target := strings.TrimSpace(mr.TargetBranch)
	if target == "" {
		return fmt.Errorf("merge proof failed for MR %s: missing target branch", mr.ID)
	}
	if source := strings.TrimSpace(mr.Branch); source != "" && source == target {
		return fmt.Errorf("merge proof failed for MR %s: source branch %s matches target branch", mr.ID, source)
	}
	commit := strings.TrimSpace(mr.CommitSHA)
	if commit == "" {
		return fmt.Errorf("merge proof failed for MR %s: missing submitted commit_sha", mr.ID)
	}
	if err := rigGit.VerifyPushedCommitReachableFromPushTarget("origin", target, commit); err != nil {
		return fmt.Errorf("merge proof failed for MR %s: target %s does not contain submitted head %s: %w", mr.ID, target, commit, err)
	}
	return nil
}

func cleanupMQPostMergeBranch(rigPath string, rigGit mqPostMergeGit, mr *refinery.MergeRequest, skipBranchDelete bool) (mqPostMergeBranchCleanup, error) {
	cleanup := mqPostMergeBranchCleanup{Attempted: true}
	if mr == nil {
		return cleanup, fmt.Errorf("remote branch delete: merge request is missing")
	}

	cleanup.Branch = strings.TrimSpace(mr.Branch)
	if cleanup.Branch == "" {
		cleanup.NoBranch = true
		return cleanup, nil
	}
	if skipBranchDelete {
		cleanup.Skipped = true
		return cleanup, nil
	}
	if !mqDeleteMergedBranchesEnabled(rigPath) {
		cleanup.Disabled = true
		return cleanup, nil
	}

	expectedHead := strings.TrimSpace(mr.CommitSHA)
	if expectedHead == "" {
		return cleanup, fmt.Errorf("remote branch delete %s: missing submitted commit_sha", cleanup.Branch)
	}

	// Deleting a branch with an open PR causes GitHub to auto-close the PR as
	// "closed" (not "merged"), destroying the PR audit trail. (gas-fk4)
	leaseHead := expectedHead
	if rigGit.HasOpenPullRequest(git.PullRequestRef{URL: mr.PRURL, Number: mr.PRNumber, Branch: cleanup.Branch, HeadSHA: expectedHead}) {
		cleanup.OpenPR = true
	} else {
		// Lease against the branch's head as it stands now, not the sha recorded
		// at submission: a branch refreshed to resolve a conflict has moved on,
		// and leasing against the stale sha makes git reject the delete as
		// "stale info". The resolver proves the current head is contained in the
		// target before returning it, which is the safety property the recorded
		// sha stood in for. (gt-yog2)
		resolvedHead, err := rigGit.ResolveMergedBranchDeleteHead("origin", cleanup.Branch, strings.TrimSpace(mr.TargetBranch), expectedHead)
		if err != nil {
			return cleanup, fmt.Errorf("remote branch delete %s: %w", cleanup.Branch, err)
		}
		if resolvedHead == "" {
			cleanup.AlreadyGone = true
		} else {
			leaseHead = resolvedHead
			if resolvedHead != expectedHead {
				cleanup.RefreshedHead = resolvedHead
			}
			if err := rigGit.DeleteRemoteBranchIfAt("origin", cleanup.Branch, leaseHead); err != nil {
				return cleanup, fmt.Errorf("remote branch delete %s at %s: %w", cleanup.Branch, leaseHead, err)
			}
			cleanup.RemoteDeleted = true
		}
	}

	if deleteMQPostMergeLocalBranchIfAt(rigGit, cleanup.Branch, leaseHead) {
		cleanup.LocalDeleted = true
	}
	return cleanup, nil
}

func deleteMQPostMergeLocalBranchIfAt(rigGit mqPostMergeGit, branch, expectedHead string) bool {
	localHead, err := rigGit.Rev("refs/heads/" + branch + "^{commit}")
	if err != nil || strings.TrimSpace(localHead) != strings.TrimSpace(expectedHead) {
		return false
	}
	return rigGit.DeleteBranch(branch, false) == nil
}

func mqDeleteMergedBranchesEnabled(rigPath string) bool {
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil || settings.MergeQueue == nil {
		return true
	}
	return settings.MergeQueue.IsDeleteMergedBranchesEnabled()
}
