package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/testguard"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Polecat command flags
var (
	polecatListJSON  bool
	polecatListAll   bool
	polecatForce     bool
	polecatRemoveAll bool
)

var polecatCmd = &cobra.Command{
	Use:     "polecat",
	Aliases: []string{"polecats"},
	GroupID: GroupAgents,
	Short:   "Manage polecats (persistent identity, ephemeral sessions)",
	RunE:    requireSubcommand,
	Long: `Manage polecat lifecycle in rigs.

Polecats have PERSISTENT IDENTITY but EPHEMERAL SESSIONS. Each polecat has
a permanent agent bead and CV chain that accumulates work history across
assignments. Sessions and sandboxes are ephemeral — spawned for specific
tasks, cleaned up on completion — but the identity persists.

A polecat is either:
  - Working: Actively doing assigned work
  - Stalled: Session crashed mid-work (needs Witness intervention)
  - Zombie: Finished but gt done failed (needs cleanup)
  - Nuked: Session ended, identity persists (ready for next assignment)

Self-cleaning model: When work completes, the polecat runs 'gt done',
which pushes the branch, submits to the merge queue, and exits. The
Witness then nukes the sandbox. The polecat's identity (agent bead)
persists with agent_state=nuked, preserving work history.

Session vs sandbox: The Claude session cycles frequently (handoffs,
compaction). The git worktree (sandbox) persists until nuke. Work
survives session restarts.

Cats build features. Dogs clean up messes.`,
}

var polecatListCmd = &cobra.Command{
	Use:   "list [rig]",
	Short: "List polecats in a rig",
	Long: `List polecats in a rig or all rigs.

In the transient model, polecats exist only while working. The list shows
all polecats with their states:
  - working:    Actively working on an issue
  - done:       Completed work, waiting for cleanup
  - handed-off: Session gone and an OPEN merge request for its branch was found —
                the work is in the refinery's queue. This is a SUCCESS state
  - stalled:    Session died with work still attached and NO open MR for it
  - stuck:      Needs assistance

This surface reads agent beads and one merge-queue listing per rig. It never
runs git. A polecat that nothing blocks therefore reports verdict UNVERIFIED and
reuse_status idle-unverified: nothing is known to be wrong with it, and nothing
was checked. Use 'gt polecat check-recovery <rig>/<name>' for a measured verdict
before acting. (Until gt-49dp this surface printed the same 'idle-preserved'
string the reuse gate prints for polecats it has cleared, including for polecats
'gt sling' went on to refuse.)

Two reuse_status values mean "nobody looked", and they never share a string with
a value that means "somebody looked and found this" (gt-mkpm):
  - idle-unverified:   no git or merge-queue facts were gathered
  - idle-mq-unchecked: gt done recorded that it made no MR, and no merge-queue
                       check ran here to say whether that still matters
Both block exactly as hard as idle-recovery-needed. Neither is a finding about
the polecat, and neither should be quoted into a bead as one.

Examples:
  gt polecat list greenplace
  gt polecat list --all
  gt polecat list greenplace --json`,
	RunE: runPolecatList,
}

var polecatAddCmd = &cobra.Command{
	Use:        "add <rig> <name>",
	Short:      "Add a new polecat to a rig (DEPRECATED)",
	Deprecated: "use 'gt polecat identity add' instead. This command will be removed in v1.0.",
	Long: `Add a new polecat to a rig.

DEPRECATED: Use 'gt polecat identity add' instead. This command will be removed in v1.0.

Creates a polecat directory, clones the rig repo, creates a work branch,
and initializes state.

Example:
  gt polecat identity add greenplace Toast  # Preferred
  gt polecat add greenplace Toast           # Deprecated`,
	Args: cobra.ExactArgs(2),
	RunE: runPolecatAdd,
}

var polecatRemoveCmd = &cobra.Command{
	Use:   "remove <rig>/<polecat>... | <rig> --all",
	Short: "Remove polecats from a rig",
	Long: `Remove one or more polecats from a rig.

Fails if session is running (stop first).
Warns if uncommitted changes exist.
Use --force to bypass checks.

Examples:
  gt polecat remove greenplace/Toast
  gt polecat remove greenplace/Toast greenplace/Furiosa
  gt polecat remove greenplace --all
  gt polecat remove greenplace --all --force`,
	Args: cobra.MinimumNArgs(1),
	RunE: runPolecatRemove,
}

var polecatStatusCmd = &cobra.Command{
	Use:   "status <rig>/<polecat>",
	Short: "Show detailed status for a polecat",
	Long: `Show detailed status for a polecat.

Displays comprehensive information including:
  - Current lifecycle state (working, done, stuck, idle)
  - Assigned issue (if any)
  - Session status (running/stopped, attached/detached)
  - Session creation time
  - Last activity time

NOTE: The argument is <rig>/<polecat> — a single argument with a slash
separator, NOT two separate arguments. For example: greenplace/Toast

Examples:
  gt polecat status greenplace/Toast
  gt polecat status greenplace/Toast --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatStatus,
}

var (
	polecatStatusJSON                    bool
	polecatGitStateJSON                  bool
	polecatGCDryRun                      bool
	polecatNukeAll                       bool
	polecatNukeDryRun                    bool
	polecatNukeForce                     bool
	polecatNukeOverrideRestartFirst      bool
	polecatCheckRecoveryJSON             bool
	polecatCheckRecoveryReconcileCleanup bool
	polecatCheckRecoveryLivenessWindow   time.Duration
	polecatPoolInitDryRun                bool
	polecatPoolInitSize                  int
)

var polecatGCCmd = &cobra.Command{
	Use:   "gc <rig>",
	Short: "Garbage collect stale polecat branches",
	Long: `Garbage collect stale polecat branches in a rig.

Polecats use unique timestamped branches (polecat/<name>-<timestamp>) to
prevent drift issues. Over time, these branches accumulate when stale
polecats are repaired.

This command removes orphaned branches:
  - Branches for polecats that no longer exist
  - Old timestamped branches (keeps only the current one per polecat)

Examples:
  gt polecat gc greenplace
  gt polecat gc greenplace --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatGC,
}

var polecatNukeCmd = &cobra.Command{
	Use:   "nuke <rig>/<polecat>... | <rig> --all",
	Short: "Completely destroy a polecat (session, worktree, branch, agent bead)",
	Long: `Completely destroy a polecat and all its artifacts.

This is the nuclear option for post-merge cleanup. It:
  1. Kills the Claude session (if running)
  2. Deletes the git worktree (bypassing all safety checks)
  3. Deletes the polecat branch
  4. Closes the agent bead (if exists)

WHO MAY RUN THIS: a human or the Mayor. A witness identity is refused
(restart-first policy, gt-dsgp) — the witness restarts a stuck polecat with
'gt session restart', which preserves the worktree and branch, and escalates
when work is at risk. --override-restart-first exists for a human driving a
witness shell.

SAFETY CHECKS: The command refuses to nuke a polecat if:
  - cleanup_status is dirty, unknown, or missing
  - Worktree fallback detects unpushed/uncommitted/stashed changes
  - Polecat has an open merge request (MR bead or active_mr)
  - Polecat has work on its hook

Use --force to bypass safety checks (LOSES WORK).
Use --dry-run to see what would happen and safety check status.

Examples:
  gt polecat nuke greenplace/Toast
  gt polecat nuke greenplace/Toast greenplace/Furiosa
  gt polecat nuke greenplace --all
  gt polecat nuke greenplace --all --dry-run
  gt polecat nuke greenplace/Toast --force  # bypass safety checks`,
	Args: cobra.MinimumNArgs(1),
	RunE: runPolecatNuke,
}

var polecatGitStateCmd = &cobra.Command{
	Use:   "git-state <rig>/<polecat>",
	Short: "Show git state for pre-kill verification",
	Long: `Show git state for a polecat's worktree.

Used by the Witness to verify no work is at risk before restarting a polecat,
and by the Mayor before nuking one. Reports whether the worktree is clean (no
work at risk) or dirty (needs cleanup). A clean verdict is a statement about
git, not authorization to destroy the sandbox.

Checks:
  - Working tree: uncommitted changes
  - Unpushed commits: commits ahead of origin/main
  - Stashes: stashed changes

Examples:
  gt polecat git-state greenplace/Toast
  gt polecat git-state greenplace/Toast --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatGitState,
}

var polecatCheckRecoveryCmd = &cobra.Command{
	Use:   "check-recovery <rig>/<polecat>",
	Short: "Check whether a dormant polecat's work is at risk",
	Long: `Check recovery status of a polecat based on cleanup_status, active_mr, and merge queue state.

Reports whether any work is at risk. It does NOT authorize destroying the polecat:
  - SAFE_TO_NUKE: no work at risk — cleanup_status is 'clean', active_mr is terminal, AND work submitted to merge queue
  - WORKING: the polecat is not finished. Read the 'reason' field for what that
    rests on: 'session-busy' read the pane and found the agent generating;
    'not-idle' read the agent bead and did NOT look at the pane
  - NEEDS_MQ_SUBMIT: git is clean but work was never submitted to the merge queue
  - NEEDS_RECOVERY: cleanup_status, active_mr, or fallback git predicates require recovery
  - PENDING_MR: work is waiting on an active merge request
  - NEEDS_STATE_CLEAR: nothing is at risk, but agent_state names a deliberate pause
    (stuck, awaiting-gate, paused, escalated) that no restart can clear
  - NEEDS_LOGIN: the pane was read and shows an auth wall. Nothing is at risk, and
    no restart can clear this one either — restarting produces another logged-out
    session. A human must run /login in the session (gt-acb1)
  - SUSPECT_STALL: the pane was sampled twice and the agent's turn clock climbed
    while its token counter did not, with no command in flight. Only reachable
    with --liveness-window (gt-y39t)

The 'liveness' field reports what the pane says the agent is DOING, which is not
the same question as whether work is at risk: working, blocking-wait, logged-out,
parked, or turn-in-flight when only one sample was taken. Without
--liveness-window one sample is taken, which settles logged-out and parked for
free; separating working from blocking-wait needs a token delta, so it needs two
samples at least 60s apart and this command BLOCKS for that long when asked.

60s is a floor, not a suggestion. Three captures of a live agent twenty seconds
apart read its token counter as 40.8k, 41.4k, 41.4k while it was demonstrably
generating the whole time — the counter is rendered rounded to a hundred tokens,
so a short window reports a healthy agent as blocked.

Every predicate except WORKING/session-busy and NEEDS_LOGIN is read from the agent
bead and from git, both of which 'gt done' writes BEFORE it pushes, submits the
MR, and exits. The two pane checks are run first, so a polecat that is still
finishing is not reported as finished (gt-5tg) and one that cannot authenticate is
not reported as working (gt-acb1). Both are positive evidence only: a session that
cannot be read is reported as neither busy nor logged out.

WORKING therefore has two roads and they are not the same claim. This command
used to print "The agent's pane shows it mid-turn" for both — a positive claim
about a pane the bead-derived road never consulted, measured wrong twice in one
evening on parked polecats and right once on a live one, with nothing in the
output telling them apart. Each verdict now names the evidence it rests on, and
where that evidence is not the pane it says so and gives you the pane check
(gt-mkpm).

The verdict names a work-at-risk state, not an action for the caller. The
witness_action field names what a witness may do about it: 'restart' to reclaim
the slot (worktree and branch preserved), 'clear-state' to lift a deliberate
pause, 'escalate' when work is at risk or a human /login is needed, or
'leave-alone' while an MR is in flight or the agent is mid-turn. Nuking is never
among them — under the restart-first policy (gt-dsgp) that requires a human or
Mayor identity.

'restart' and 'clear-state' are not interchangeable. No restart path writes
agent_state, so restarting a paused polecat leaves it paused; that is why the
paused case gets its own verdict and its own action rather than being folded into
the restart arm (gt-fbgq). NEEDS_LOGIN gets its own verdict for the same reason
and a stronger one: restarting a logged-out agent does not merely fail to fix it,
it produces another logged-out agent and destroys the context on the way.

--reconcile-cleanup additionally REPAIRS stale completion state on the agent bead
when this command's own measurement contradicts it: a dirty or MISSING
cleanup_status, a push_failed left true by a rebase whose content is already on
the remote, and an agent_state that still claims work in progress over a session
that "tmux has-session" proves is gone. None is written unless the git checks
ran and every OTHER predicate proves safe, and each confirms its write by
re-reading the bead. Without it, push_failed had no clearing path any role could
run and kept a polecat reading idle-recovery-needed over work provably in main
(gt-uapr).

The cleanup_status half asks its SAFE_TO_NUKE precondition of the bead it would
PRODUCE rather than the one it found. Asking it of the bead as found closed the
recovery loop into a circle — a stale cleanup_status is itself a NEEDS_RECOVERY
blocker, so on exactly the polecats this flag exists to repair the precondition
could never hold, the write never ran, and nuke was the only verb left that
changed anything (gt-hm0v, hq-f183o).

The agent_state half exists because that circle then reproduced one level up: the
cleanup repair is gated on agent_state=idle, and agent_state=working is written
only by starting work and cleared only by finishing it, so a polecat that died in
between had no path out and "gt polecat clear-state" declines it by design — a
pause is deliberate, a stale working is not. It shares the cleanup half's
precondition and adds one of its own: the session must be MEASURED absent, never
merely unknown, so a live polecat can never reach the write (gt-xj5d).

It reports what it did to every field it was asked about, on every road, and
EXITS NON-ZERO when a repair it was asked for did not happen. Silence plus exit 0
is the one outcome it must never produce: it reads as "the safe path did not
apply here", which is the reasoning that reaches for the destructive one.

Examples:
  gt polecat check-recovery greenplace/Toast
  gt polecat check-recovery greenplace/Toast --json
  gt polecat check-recovery greenplace/Toast --reconcile-cleanup
  gt polecat check-recovery greenplace/Toast --liveness-window 90s`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatCheckRecovery,
}

var (
	polecatStaleJSON                 bool
	polecatStaleThreshold            int
	polecatStaleCleanup              bool
	polecatStaleDryRun               bool
	polecatStaleOverrideRestartFirst bool
	polecatPruneDryRun               bool
	polecatPruneRemote               bool
)

var polecatStaleCmd = &cobra.Command{
	Use:   "stale <rig>",
	Short: "Detect stale polecats that may need cleanup",
	Long: `Detect stale polecats in a rig that are candidates for cleanup.

A polecat is considered stale if:
  - No active tmux session
  - Way behind main (>threshold commits) OR no agent bead
  - Has no uncommitted work that could be lost

The default threshold is 20 commits behind main.

Use --cleanup to automatically nuke stale polecats that are safe to remove.
Use --dry-run with --cleanup to see what would be cleaned.

Examples:
  gt polecat stale greenplace
  gt polecat stale greenplace --threshold 50
  gt polecat stale greenplace --json
  gt polecat stale greenplace --cleanup
  gt polecat stale greenplace --cleanup --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatStale,
}

var polecatPruneCmd = &cobra.Command{
	Use:   "prune <rig>",
	Short: "Prune stale polecat branches (local and remote)",
	Long: `Prune stale polecat branches in a rig.

Finds and deletes polecat branches that are no longer needed:
  - Branches fully merged to main
  - Branches whose remote tracking branch was deleted (post-merge cleanup)
  - Branches for polecats that no longer exist (orphaned)

Uses safe deletion (git branch -d) — only removes fully merged branches.
Also cleans up remote polecat branches that are fully merged.

A branch checked out in a live polecat worktree is never deletable. Those are
listed as kept, with the worktree holding them — nuke the polecat to release
the branch. --dry-run reports them the same way, so the preview matches what
the real run will do.

Use --dry-run to preview what would be pruned.
Use --remote to also prune remote polecat branches on origin.

Examples:
  gt polecat prune greenplace
  gt polecat prune greenplace --dry-run
  gt polecat prune greenplace --remote`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatPrune,
}

var polecatPoolInitCmd = &cobra.Command{
	Use:   "pool-init <rig>",
	Short: "Initialize a persistent polecat pool for a rig",
	Long: `Initialize a persistent polecat pool for a rig.

Creates N polecats with identities and worktrees in IDLE state,
ready for immediate work assignment via gt sling.

Pool size is determined by (in priority order):
  1. --size flag
  2. polecat_pool_size in rig config.json
  3. Default: 4

Polecat names come from:
  1. polecat_names in rig config.json (if specified)
  2. The rig's name pool theme (default: mad-max)

Existing polecats are preserved — only new ones are created
to reach the target pool size.

Examples:
  gt polecat pool-init gastown
  gt polecat pool-init gastown --size 6
  gt polecat pool-init gastown --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatPoolInit,
}

func init() {
	// List flags
	polecatListCmd.Flags().BoolVar(&polecatListJSON, "json", false, "Output as JSON")
	polecatListCmd.Flags().BoolVar(&polecatListAll, "all", false, "List polecats in all rigs")

	// Remove flags
	polecatRemoveCmd.Flags().BoolVarP(&polecatForce, "force", "f", false, "Force removal, bypassing checks")
	polecatRemoveCmd.Flags().BoolVar(&polecatRemoveAll, "all", false, "Remove all polecats in the rig")

	// Status flags
	polecatStatusCmd.Flags().BoolVar(&polecatStatusJSON, "json", false, "Output as JSON")

	// Git-state flags
	polecatGitStateCmd.Flags().BoolVar(&polecatGitStateJSON, "json", false, "Output as JSON")

	// GC flags
	polecatGCCmd.Flags().BoolVar(&polecatGCDryRun, "dry-run", false, "Show what would be deleted without deleting")

	// Nuke flags
	polecatNukeCmd.Flags().BoolVar(&polecatNukeAll, "all", false, "Nuke all polecats in the rig")
	polecatNukeCmd.Flags().BoolVar(&polecatNukeDryRun, "dry-run", false, "Show what would be nuked without doing it")
	polecatNukeCmd.Flags().BoolVarP(&polecatNukeForce, "force", "f", false, "Force nuke, bypassing all safety checks (LOSES WORK)")
	polecatNukeCmd.Flags().BoolVar(&polecatNukeOverrideRestartFirst, restartFirstOverrideFlag, false, "Nuke from a witness identity anyway (human-only escape hatch; restart-first policy normally refuses)")

	// Check-recovery flags
	polecatCheckRecoveryCmd.Flags().BoolVar(&polecatCheckRecoveryJSON, "json", false, "Output as JSON")
	polecatCheckRecoveryCmd.Flags().BoolVar(&polecatCheckRecoveryReconcileCleanup, "reconcile-cleanup", false, "Safely rewrite stale completion state (cleanup_status, push_failed, agent_state) on the agent bead when live recovery predicates prove no work is at risk; reports what it did to each field and exits non-zero if a repair was refused")
	polecatCheckRecoveryCmd.Flags().DurationVar(&polecatCheckRecoveryLivenessWindow, "liveness-window", 0,
		"Sample the pane twice this far apart to separate a working agent from one blocked consuming no tokens. BLOCKS for the duration; minimum 60s (shorter windows report healthy agents as blocked). Zero takes one sample, which still settles logged-out and parked")

	// Stale flags
	polecatStaleCmd.Flags().BoolVar(&polecatStaleJSON, "json", false, "Output as JSON")
	polecatStaleCmd.Flags().IntVar(&polecatStaleThreshold, "threshold", 20, "Commits behind main to consider stale")
	polecatStaleCmd.Flags().BoolVar(&polecatStaleCleanup, "cleanup", false, "Automatically nuke stale polecats")
	polecatStaleCmd.Flags().BoolVar(&polecatStaleDryRun, "dry-run", false, "Show what would be cleaned without doing it")
	polecatStaleCmd.Flags().BoolVar(&polecatStaleOverrideRestartFirst, restartFirstOverrideFlag, false, "Run --cleanup from a witness identity anyway (human-only escape hatch; restart-first policy normally refuses)")

	// Prune flags
	polecatPruneCmd.Flags().BoolVar(&polecatPruneDryRun, "dry-run", false, "Show what would be pruned without doing it")
	polecatPruneCmd.Flags().BoolVar(&polecatPruneRemote, "remote", false, "Also prune remote polecat branches on origin")

	// Pool-init flags
	polecatPoolInitCmd.Flags().BoolVar(&polecatPoolInitDryRun, "dry-run", false, "Show what would be created without doing it")
	polecatPoolInitCmd.Flags().IntVar(&polecatPoolInitSize, "size", 0, "Pool size (overrides rig config)")

	// Add subcommands
	polecatCmd.AddCommand(polecatListCmd)
	polecatCmd.AddCommand(polecatAddCmd)
	polecatCmd.AddCommand(polecatRemoveCmd)
	polecatCmd.AddCommand(polecatStatusCmd)
	polecatCmd.AddCommand(polecatGitStateCmd)
	polecatCmd.AddCommand(polecatCheckRecoveryCmd)
	polecatCmd.AddCommand(polecatGCCmd)
	polecatCmd.AddCommand(polecatNukeCmd)
	polecatCmd.AddCommand(polecatStaleCmd)
	polecatCmd.AddCommand(polecatPruneCmd)
	polecatCmd.AddCommand(polecatPoolInitCmd)

	rootCmd.AddCommand(polecatCmd)
}

// PolecatListItem represents a polecat in list output.
type PolecatListItem struct {
	Rig                  string        `json:"rig"`
	Name                 string        `json:"name"`
	State                polecat.State `json:"state"`
	Issue                string        `json:"issue,omitempty"`
	CleanupStatus        string        `json:"cleanup_status,omitempty"`
	ActiveMR             string        `json:"active_mr,omitempty"`
	Branch               string        `json:"branch,omitempty"`
	Verdict              string        `json:"verdict,omitempty"`
	Reason               string        `json:"reason,omitempty"`
	Reusable             bool          `json:"reusable"`
	SafeToNuke           bool          `json:"safe_to_nuke"`
	NeedsRecovery        bool          `json:"needs_recovery"`
	NeedsMQSubmit        bool          `json:"needs_mq_submit"`
	MQStatus             string        `json:"mq_status,omitempty"`
	CountsTowardCapacity bool          `json:"counts_toward_capacity"`
	ReuseStatus          string        `json:"reuse_status,omitempty"`
	Blockers             []string      `json:"blockers,omitempty"`
	SessionRunning       bool          `json:"session_running"`
	Zombie               bool          `json:"zombie,omitempty"`
	SessionName          string        `json:"session_name,omitempty"`
}

// effectivePolecatState returns the observable state used by polecat list output.
// Active work is ground truth for working; tmux liveness alone is not enough
// because persistent polecats may keep a reusable live session after completion.
// Zombie entries are never auto-rewritten.
func effectivePolecatState(item PolecatListItem) polecat.State {
	state := item.State
	// A running session only implies working when there is active work attached.
	// Without an issue, rewriting idle/done to working recreates "Issue: (none)".
	if item.SessionRunning && item.Issue != "" && item.CountsTowardCapacity && (state == polecat.StateDone || state == polecat.StateIdle) {
		return polecat.StateWorking
	}
	// When session is dead but beads still says "working", mark as stalled
	// (not done — work was interrupted, not completed). The manager's loadFromBeads
	// now returns StateStalled for this case, but list reconciliation may override.
	if !item.SessionRunning && !item.Zombie && state == polecat.StateWorking {
		return polecat.StateStalled
	}
	return state
}

type reuseMRShower interface {
	Show(issueID string) (*beads.Issue, error)
}

func activeMRBlocksReuse(bd reuseMRShower, mrID, sourceHint string, requireGitSafe, gitSafe bool) bool {
	assessment := polecat.AssessActiveMR(bd, polecat.ActiveMRInput{ActiveMR: mrID, SourceIssueHint: sourceHint, RequireGitSafe: requireGitSafe, GitSafe: gitSafe})
	return assessment.Pending
}

func polecatReuseStatus(state polecat.State, cleanupStatus, activeMR, branch string, activeMRBlocks, staleCleanupSafe bool) string {
	input := polecat.WorkstateInput{State: state, CleanupStatus: polecat.CleanupStatus(cleanupStatus), ActiveMR: activeMR, Branch: branch}
	if activeMRBlocks {
		input.ActiveMRBlocker = "active_mr=" + activeMR + " status=open"
	}
	if staleCleanupSafe {
		input.IgnoreCleanupStatus = true
	}
	return polecat.DecideWorkstate(input).ReuseStatus
}

// getPolecatManager creates a polecat manager for the given rig.
func getPolecatManager(rigName string) (*polecat.Manager, *rig.Rig, error) {
	_, r, err := getRig(rigName)
	if err != nil {
		return nil, nil, err
	}

	polecatGit := git.NewGit(r.Path)
	t := tmux.NewTmux()
	mgr := polecat.NewManager(r, polecatGit, t)

	return mgr, r, nil
}

func runPolecatList(cmd *cobra.Command, args []string) error {
	var rigs []*rig.Rig

	if polecatListAll {
		// List all rigs
		allRigs, err := getAllRigs()
		if err != nil {
			return err
		}
		rigs = allRigs
	} else {
		// Need a rig name
		if len(args) < 1 {
			return fmt.Errorf("rig name required (or use --all)")
		}
		_, r, err := getPolecatManager(args[0])
		if err != nil {
			return err
		}
		rigs = []*rig.Rig{r}
	}

	// Collect polecats from all rigs
	t := tmux.NewTmux()
	sessionNames, err := t.ListSessions()
	if err != nil {
		return fmt.Errorf("listing tmux sessions: %w", err)
	}
	sessions := newPolecatSessionSet(sessionNames)
	allPolecats := make([]PolecatListItem, 0)
	// Resolved once for the whole listing. An unreadable town root leaves this
	// empty and recording becomes a no-op — the listing itself is unaffected.
	journalRoot, _ := workspace.FindFromCwd()

	for _, r := range rigs {
		bd := beads.New(r.Path)

		polecatNames, err := listPolecatDirectoryNames(r.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to list polecats in %s: %v\n", r.Name, err)
			continue
		}
		agents, agentErr := bd.ListAgentBeads()
		if agentErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to list agent beads in %s: %v\n", r.Name, agentErr)
			agents = nil
		}
		activeWork, activeWorkErr := listActivePolecatWorkByName(bd, r.Name)
		if activeWorkErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to list active polecat work in %s: %v\n", r.Name, activeWorkErr)
			activeWork = nil
		}
		// One merge-queue listing per rig, not one per polecat. On failure the
		// index stays nil and every branch reads as "not consulted", which is
		// what it is — the disposition then degrades to the bead-only answer
		// this surface gave before, rather than to a confident SAFE_TO_NUKE.
		mrIndex, mrIndexErr := newPolecatBranchMRIndex(bd)
		if mrIndexErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to index merge requests in %s: %v\n", r.Name, mrIndexErr)
			mrIndex = nil
		}

		// Track known polecat names from filesystem for zombie detection
		knownNames := make(map[string]bool)
		for _, name := range polecatNames {
			agentBeadID := polecatBeadIDForRig(r, r.Name, name)
			fields := parsePolecatAgentFields(agents[agentBeadID])
			item := buildPolecatInventoryItem(r.Name, name, fields, activeWork[name], sessions, mrIndex)
			if activeWorkErr != nil {
				item = buildPolecatInventoryItemFromEvidence(r.Name, name, fields, polecatActiveWorkLookupError(activeWorkErr), sessions, mrIndex)
			}
			disposition := item.Disposition
			recordNeedsMQSubmitObservation(journalRoot, polecat.MQSubmitObservation{
				Rig:     r.Name,
				Polecat: name,
				Issue:   item.Issue,
				Branch:  item.Branch,
				Source:  "polecat-list",
			}, disposition)
			state := effectivePolecatState(PolecatListItem{
				State:                item.State,
				Issue:                item.Issue,
				SessionRunning:       item.SessionRunning,
				CountsTowardCapacity: disposition.CountsTowardCapacity,
			})
			allPolecats = append(allPolecats, PolecatListItem{
				Rig:                  r.Name,
				Name:                 name,
				State:                state,
				Issue:                item.Issue,
				CleanupStatus:        item.CleanupStatus,
				ActiveMR:             item.ActiveMR,
				Branch:               item.Branch,
				Verdict:              disposition.Verdict,
				Reason:               disposition.Reason,
				Reusable:             disposition.Reusable,
				SafeToNuke:           disposition.SafeToNuke,
				NeedsRecovery:        disposition.NeedsRecovery,
				NeedsMQSubmit:        disposition.NeedsMQSubmit,
				MQStatus:             disposition.MQStatus,
				CountsTowardCapacity: disposition.CountsTowardCapacity,
				ReuseStatus:          disposition.ReuseStatus,
				Blockers:             disposition.Blockers,
				SessionRunning:       item.SessionRunning,
				SessionName:          item.SessionName,
			})
			knownNames[name] = true
		}

		// Discover zombie tmux sessions: sessions without matching worktree directories.
		// These occur when a worktree is deleted but the tmux session persists
		// (incomplete nuke or session naming mismatch).
		zombieSessions := sessions.namesForRig(r.Name)
		for _, sessionName := range zombieSessions {
			_, polecatName, ok := parsePolecatSessionName(sessionName)
			if !ok {
				continue
			}
			if !knownNames[polecatName] {
				allPolecats = append(allPolecats, PolecatListItem{
					Rig:            r.Name,
					Name:           polecatName,
					State:          polecat.StateZombie,
					SessionRunning: true,
					Zombie:         true,
					SessionName:    sessionName,
				})
			}
		}
	}

	// Output
	if polecatListJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(allPolecats)
	}

	if len(allPolecats) == 0 {
		fmt.Println("No polecats found.")
		return nil
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Polecats"))
	for _, p := range allPolecats {
		// Session indicator
		sessionStatus := style.Dim.Render("○")
		if p.SessionRunning {
			sessionStatus = style.Success.Render("●")
		}

		// State color
		stateStr := string(p.State)
		switch p.State {
		case polecat.StateWorking:
			stateStr = style.Info.Render(stateStr)
		case polecat.StateStuck:
			stateStr = style.Warning.Render(stateStr)
		case polecat.StateStalled:
			stateStr = style.Error.Render(stateStr)
		case polecat.StateReviewNeeded:
			stateStr = style.Warning.Render(stateStr)
		case polecat.StateDone:
			stateStr = style.Success.Render(stateStr)
		case polecat.StateHandedOff:
			// Deliberately not error-styled: this polecat succeeded and its work
			// is in the queue. It rendered as red "stalled" for the whole
			// in-flight window before gt-mkpm.
			stateStr = style.Info.Render(stateStr)
		case polecat.StateZombie:
			stateStr = style.Error.Render(stateStr)
		default:
			stateStr = style.Dim.Render(stateStr)
		}

		fmt.Printf("  %s %s/%s  %s\n", sessionStatus, p.Rig, p.Name, stateStr)
		if p.Issue != "" {
			fmt.Printf("    %s\n", style.Dim.Render(p.Issue))
		}
		if p.ReuseStatus != "" {
			details := "reuse: " + p.ReuseStatus
			if p.CleanupStatus != "" {
				details += " cleanup=" + p.CleanupStatus
			}
			if p.ActiveMR != "" {
				details += " active_mr=" + p.ActiveMR
			}
			fmt.Printf("    %s\n", style.Dim.Render(details))
		}
		if p.Zombie && p.SessionName != "" {
			fmt.Printf("    %s\n", style.Dim.Render("session: "+p.SessionName+" (no worktree)"))
		}
	}

	printPolecatListMeasurementFooter(os.Stdout, allPolecats)

	return nil
}

// printPolecatListMeasurementFooter names, in the output itself, how many rows
// carry a blocking-looking status that no measurement stands behind.
//
// This surface runs no git. That is deliberate and documented in the source —
// "now the operative difference between this surface and the reuse gate rather
// than a silent one" — but the awareness lived in a comment and never reached
// stdout, so a reader had no way to tell a measured blocker from an unmeasured
// one without opening the source or running a second command. Two witnesses
// scoped a P1 on a string they read as a finding (gt-mkpm).
func printPolecatListMeasurementFooter(w io.Writer, items []PolecatListItem) {
	unmeasured := 0
	var example PolecatListItem
	for _, p := range items {
		if !polecat.DispositionUnmeasured(polecat.WorkstateDisposition{ReuseStatus: p.ReuseStatus}) {
			continue
		}
		if unmeasured == 0 {
			example = p
		}
		unmeasured++
	}
	if unmeasured == 0 {
		return
	}
	noun := "polecats"
	if unmeasured == 1 {
		noun = "polecat"
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s\n", style.Dim.Render(fmt.Sprintf(
		"%d of %d %s above were decided WITHOUT a git check — this surface runs none.",
		unmeasured, len(items), noun)))
	fmt.Fprintf(w, "%s\n", style.Dim.Render(
		"Nothing is known to be wrong with them and nothing was ruled out. For a measured verdict:"))
	fmt.Fprintf(w, "%s\n", style.Dim.Render(fmt.Sprintf(
		"    gt polecat check-recovery %s/%s", example.Rig, example.Name)))
}

func runPolecatAdd(cmd *cobra.Command, args []string) error {
	// Emit deprecation warning
	fmt.Fprintf(os.Stderr, "%s 'gt polecat add' is deprecated. Use 'gt polecat identity add' instead.\n",
		style.Warning.Render("Warning:"))
	fmt.Fprintf(os.Stderr, "         This command will be removed in v1.0.\n\n")

	rigName := args[0]
	polecatName := args[1]

	mgr, _, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	fmt.Printf("Adding polecat %s to rig %s...\n", polecatName, rigName)

	p, err := mgr.Add(polecatName)
	if err != nil {
		return fmt.Errorf("adding polecat: %w", err)
	}

	fmt.Printf("%s Polecat %s added.\n", style.SuccessPrefix, p.Name)
	fmt.Printf("  %s\n", style.Dim.Render(p.ClonePath))
	fmt.Printf("  Branch: %s\n", style.Dim.Render(p.Branch))

	return nil
}

func runPolecatRemove(cmd *cobra.Command, args []string) error {
	targets, err := resolvePolecatTargets(args, polecatRemoveAll)
	if err != nil {
		return err
	}

	if len(targets) == 0 {
		fmt.Println("No polecats to remove.")
		return nil
	}

	// Remove each polecat
	t := tmux.NewTmux()
	var removeErrors []string
	removed := 0

	for _, p := range targets {
		// Check if session is running
		if !polecatForce {
			polecatMgr := polecat.NewSessionManager(t, p.r)
			running, _ := polecatMgr.IsRunning(p.polecatName)
			if running {
				removeErrors = append(removeErrors, fmt.Sprintf("%s/%s: session is running (stop first or use --force)", p.rigName, p.polecatName))
				continue
			}
		}

		fmt.Printf("Removing polecat %s/%s...\n", p.rigName, p.polecatName)

		if err := p.mgr.Remove(p.polecatName, polecatForce); err != nil {
			if errors.Is(err, polecat.ErrHasChanges) {
				removeErrors = append(removeErrors, fmt.Sprintf("%s/%s: has uncommitted changes (use --force)", p.rigName, p.polecatName))
			} else {
				removeErrors = append(removeErrors, fmt.Sprintf("%s/%s: %v", p.rigName, p.polecatName, err))
			}
			continue
		}

		fmt.Printf("  %s removed\n", style.Success.Render("✓"))
		removed++
	}

	// Report results
	if len(removeErrors) > 0 {
		fmt.Printf("\n%s Some removals failed:\n", style.Warning.Render("Warning:"))
		for _, e := range removeErrors {
			fmt.Printf("  - %s\n", e)
		}
	}

	if removed > 0 {
		fmt.Printf("\n%s Removed %d polecat(s).\n", style.SuccessPrefix, removed)
	}

	if len(removeErrors) > 0 {
		return fmt.Errorf("%d removal(s) failed", len(removeErrors))
	}

	return nil
}

// PolecatStatus represents detailed polecat status for JSON output.
type PolecatStatus struct {
	Rig            string        `json:"rig"`
	Name           string        `json:"name"`
	State          polecat.State `json:"state"`
	Issue          string        `json:"issue,omitempty"`
	ClonePath      string        `json:"clone_path"`
	Branch         string        `json:"branch"`
	SessionRunning bool          `json:"session_running"`
	SessionID      string        `json:"session_id,omitempty"`
	Attached       bool          `json:"attached,omitempty"`
	Windows        int           `json:"windows,omitempty"`
	CreatedAt      string        `json:"created_at,omitempty"`
	LastActivity   string        `json:"last_activity,omitempty"`
}

func runPolecatStatus(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Get polecat info
	p, err := mgr.Get(polecatName)
	if err != nil {
		return fmt.Errorf("polecat '%s' not found in rig '%s'", polecatName, rigName)
	}

	// Get session info
	t := tmux.NewTmux()
	polecatMgr := polecat.NewSessionManager(t, r)
	sessInfo, err := polecatMgr.Status(polecatName)
	if err != nil {
		// Non-fatal - continue without session info
		sessInfo = &polecat.SessionInfo{
			Polecat: polecatName,
			Running: false,
		}
	}

	// JSON output
	if polecatStatusJSON {
		status := PolecatStatus{
			Rig:            rigName,
			Name:           polecatName,
			State:          p.State,
			Issue:          p.Issue,
			ClonePath:      p.ClonePath,
			Branch:         p.Branch,
			SessionRunning: sessInfo.Running,
			SessionID:      sessInfo.SessionID,
			Attached:       sessInfo.Attached,
			Windows:        sessInfo.Windows,
		}
		if !sessInfo.Created.IsZero() {
			status.CreatedAt = sessInfo.Created.Format("2006-01-02 15:04:05")
		}
		if !sessInfo.LastActivity.IsZero() {
			status.LastActivity = sessInfo.LastActivity.Format("2006-01-02 15:04:05")
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}

	// Human-readable output
	fmt.Printf("%s\n\n", style.Bold.Render(fmt.Sprintf("Polecat: %s/%s", rigName, polecatName)))

	// State with color
	stateStr := string(p.State)
	switch p.State {
	case polecat.StateWorking:
		stateStr = style.Info.Render(stateStr)
	case polecat.StateStuck:
		stateStr = style.Warning.Render(stateStr)
	case polecat.StateStalled:
		stateStr = style.Error.Render(stateStr)
	case polecat.StateReviewNeeded:
		stateStr = style.Warning.Render(stateStr)
	case polecat.StateDone:
		stateStr = style.Success.Render(stateStr)
	default:
		stateStr = style.Dim.Render(stateStr)
	}
	fmt.Printf("  State:         %s\n", stateStr)

	// Issue
	if p.Issue != "" {
		fmt.Printf("  Issue:         %s\n", p.Issue)
	} else {
		fmt.Printf("  Issue:         %s\n", style.Dim.Render("(none)"))
	}

	// Clone path and branch
	fmt.Printf("  Clone:         %s\n", style.Dim.Render(p.ClonePath))
	fmt.Printf("  Branch:        %s\n", style.Dim.Render(p.Branch))

	// Session info
	fmt.Println()
	fmt.Printf("%s\n", style.Bold.Render("Session"))

	if sessInfo.Running {
		fmt.Printf("  Status:        %s\n", style.Success.Render("running"))
		fmt.Printf("  Session ID:    %s\n", style.Dim.Render(sessInfo.SessionID))

		if sessInfo.Attached {
			fmt.Printf("  Attached:      %s\n", style.Info.Render("yes"))
		} else {
			fmt.Printf("  Attached:      %s\n", style.Dim.Render("no"))
		}

		if sessInfo.Windows > 0 {
			fmt.Printf("  Windows:       %d\n", sessInfo.Windows)
		}

		if !sessInfo.Created.IsZero() {
			fmt.Printf("  Created:       %s\n", sessInfo.Created.Format("2006-01-02 15:04:05"))
		}

		if !sessInfo.LastActivity.IsZero() {
			// Show relative time for activity
			ago := formatActivityTime(sessInfo.LastActivity)
			fmt.Printf("  Last Activity: %s (%s)\n",
				sessInfo.LastActivity.Format("15:04:05"),
				style.Dim.Render(ago))
		}
	} else {
		fmt.Printf("  Status:        %s\n", style.Dim.Render("not running"))
	}

	return nil
}

// formatActivityTime returns a human-readable relative time string.
func formatActivityTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// GitState represents the git state of a polecat's worktree.
type GitState struct {
	Clean                 bool     `json:"clean"`
	UncommittedFiles      []string `json:"uncommitted_files"`
	UnpushedCommits       int      `json:"unpushed_commits"`
	ComparisonBase        string   `json:"comparison_base,omitempty"`
	UnpreservedPatchCount int      `json:"unpreserved_patch_count"`
	StashCount            int      `json:"stash_count"`                  // Current-branch stashes: per-polecat risk.
	SharedStashCount      int      `json:"shared_stash_count,omitempty"` // Other branch stashes visible through the shared repo.

	// PreservationMeasured records that BranchPreservationStatus actually ran and
	// resolved a comparison. Without it UnpushedCommits==0 is ambiguous — the same
	// zero means "everything is durable on the remote" and "nothing could be
	// compared" — and only the first of those is evidence of anything (gt-3bzt).
	PreservationMeasured bool `json:"preservation_measured"`
}

func runPolecatGitState(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Verify polecat exists
	p, err := mgr.Get(polecatName)
	if err != nil {
		return fmt.Errorf("polecat '%s' not found in rig '%s'", polecatName, rigName)
	}

	// Get git state from the polecat's worktree
	state, err := getGitState(p.ClonePath)
	if err != nil {
		return fmt.Errorf("getting git state: %w", err)
	}

	// JSON output
	if polecatGitStateJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(state)
	}

	// Human-readable output
	fmt.Printf("%s\n\n", style.Bold.Render(fmt.Sprintf("Git State: %s/%s", r.Name, polecatName)))

	// Working tree status
	if len(state.UncommittedFiles) == 0 {
		fmt.Printf("  Working Tree:  %s\n", style.Success.Render("clean"))
	} else {
		fmt.Printf("  Working Tree:  %s\n", style.Warning.Render("dirty"))
		fmt.Printf("  Uncommitted:   %s\n", style.Warning.Render(fmt.Sprintf("%d files", len(state.UncommittedFiles))))
		for _, f := range state.UncommittedFiles {
			fmt.Printf("                 %s\n", style.Dim.Render(f))
		}
	}

	// Unpushed commits
	if state.ComparisonBase != "" {
		fmt.Printf("  Comparison:   %s (%d unpreserved patch(es))\n", style.Dim.Render(state.ComparisonBase), state.UnpreservedPatchCount)
	}
	if state.UnpushedCommits == 0 {
		fmt.Printf("  Unpushed:      %s\n", style.Success.Render("0 commits"))
	} else {
		fmt.Printf("  Unpushed:      %s\n", style.Warning.Render(fmt.Sprintf("%d commits ahead", state.UnpushedCommits)))
	}

	// Stashes
	if state.StashCount == 0 {
		fmt.Printf("  Branch Stashes: %s\n", style.Dim.Render("0"))
	} else {
		fmt.Printf("  Branch Stashes: %s\n", style.Warning.Render(fmt.Sprintf("%d", state.StashCount)))
	}
	if state.SharedStashCount > 0 {
		fmt.Printf("  Shared Stashes: %s\n", style.Dim.Render(fmt.Sprintf("%d (repo-wide, not this branch)", state.SharedStashCount)))
	}

	// Verdict
	fmt.Println()
	if state.Clean {
		// "safe to kill" read as an instruction to the Witness, which may not
		// kill anything (gt-y20). State the git fact; leave the action to the
		// caller's policy.
		fmt.Printf("  Verdict:       %s\n", style.Success.Render("CLEAN (no work at risk)"))
	} else {
		fmt.Printf("  Verdict:       %s\n", style.Error.Render("DIRTY (needs cleanup)"))
	}

	return nil
}

// getGitState checks the git state of a worktree.
func getGitState(worktreePath string) (*GitState, error) {
	return getGitStateWithTargets(worktreePath, nil)
}

func getGitStateWithTargets(worktreePath string, targets []string) (*GitState, error) {
	state := &GitState{
		Clean:            true,
		UncommittedFiles: []string{},
	}

	worktreeGit := git.NewGit(worktreePath)
	workStatus, err := worktreeGit.CheckUncommittedWork()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	if workStatus.HasUncommittedChanges {
		state.UncommittedFiles = workStatus.NonRuntimePaths()
		if len(state.UncommittedFiles) > 0 {
			state.Clean = false
		}
	}
	if workStatus.StashCount > 0 {
		state.StashCount = workStatus.StashCount
		state.Clean = false
	}

	branch, _ := worktreeGit.CurrentBranch()
	if preservation, preserveErr := worktreeGit.BranchPreservationStatus(branch, "origin", targets); preserveErr == nil {
		state.PreservationMeasured = true
		state.ComparisonBase = preservation.ComparisonBase
		state.UnpreservedPatchCount = preservation.UnpreservedPatchCount
		if preservation.UnpreservedPatchCount > 0 {
			state.UnpushedCommits = preservation.UnpreservedPatchCount
			state.Clean = false
		}
	}

	// Check for stashes using Git.StashCount() which filters by current branch.
	// Without branch filtering, worktrees see repo-wide stashes and produce
	// false "NEEDS_RECOVERY" verdicts for worktrees with zero stashes of their own.
	if totalStashes, stashErr := worktreeGit.StashCountAll(); stashErr == nil && totalStashes > state.StashCount {
		state.SharedStashCount = totalStashes - state.StashCount
	}

	return state, nil
}

// RecoveryStatus represents whether a polecat needs recovery or is safe to nuke.
type RecoveryStatus struct {
	Rig     string `json:"rig"`
	Polecat string `json:"polecat"`
	// State is the lifecycle state this verdict was decided from. Reported
	// because the two polecat surfaces disagreed and neither output said what it
	// had decided from, so a reader could not tell which one to believe
	// (gt-mkpm).
	State polecat.State `json:"state,omitempty"`
	// SessionPresence is what `tmux has-session` said about this polecat, and it
	// is reported unconditionally — on every verdict, including the ones it did
	// not decide.
	//
	// It is separate from Liveness below and answers a different question:
	// Liveness says what a session that EXISTS is doing, and every one of its
	// readings presupposes there is a pane to read. This says whether there is
	// one at all.
	//
	// Reported because the complaint in gt-9f67 is not only that the wrong
	// verdict came out; it is that three readings of a polecat with no session
	// produced three different verdicts and NOT ONE of them mentioned the
	// session. A reader had no way to notice the omission from the output, and
	// four of them did not. Whether it is present, absent, or unknown, the answer
	// now travels with the verdict that was reached in spite of it.
	SessionPresence polecat.SessionPresence `json:"session_presence,omitempty"`
	// AgentState is the agent bead's agent_state as this run last saw it —
	// after any repair, so it reports the stored value rather than the one found.
	//
	// It is reported for the reason cleanup_status is: it is a GATE. A stale
	// agent_state=working refuses the cleanup repair, counts against capacity on
	// the inventory surface, and was on neither output. Three polecats sat behind
	// it and the only way to see the field at all was to pass
	// --reconcile-cleanup and read the refusal it produced (gt-xj5d).
	//
	// Not omitempty, on the same reasoning: a bead with no agent_state at all is
	// a fact worth seeing, and a key that vanishes when the value is missing
	// makes the missing case indistinguishable from a caller that never read it.
	AgentState    string                `json:"agent_state"`
	CleanupStatus polecat.CleanupStatus `json:"cleanup_status"`
	NeedsRecovery bool                  `json:"needs_recovery"`
	Verdict       string                `json:"verdict"`        // SAFE_TO_NUKE, PENDING_MR, NEEDS_RECOVERY, NEEDS_MQ_SUBMIT, NEEDS_LOGIN, or SUSPECT_STALL
	WitnessAction string                `json:"witness_action"` // restart, escalate, or leave-alone — never nuke (gt-dsgp)
	Reason        string                `json:"reason,omitempty"`
	Reusable      bool                  `json:"reusable"`
	SafeToNuke    bool                  `json:"safe_to_nuke"`
	NeedsMQSubmit bool                  `json:"needs_mq_submit"`
	NeedsLogin    bool                  `json:"needs_login,omitempty"` // pane shows an auth wall; only a human /login clears it (gt-acb1)

	// Liveness is what the pane says the agent is DOING — working,
	// blocking-wait, logged-out, parked, or turn-in-flight — as opposed to what
	// the verdict says about its work. Reported alongside the verdict rather
	// than folded into it because they answer different questions and used to
	// be conflated: hooked-ness was read as liveness, so an agent that could
	// not execute a turn reported WORKING (gt-y39t).
	Liveness string `json:"liveness,omitempty"`

	// LivenessWindow names how far apart the two samples behind Liveness were.
	// Empty when only one sample was taken, which is the tell that
	// working-vs-blocking-wait was never asked.
	LivenessWindow       string   `json:"liveness_window,omitempty"`
	CountsTowardCapacity bool     `json:"counts_toward_capacity"`
	ReuseStatus          string   `json:"reuse_status,omitempty"`
	Branch               string   `json:"branch,omitempty"`
	Issue                string   `json:"issue,omitempty"`
	MQStatus             string   `json:"mq_status,omitempty"` // "submitted", "not_submitted", "not_required", "unknown"
	ActiveMR             string   `json:"active_mr,omitempty"`
	Blockers             []string `json:"blockers,omitempty"`
	Diagnostics          []string `json:"diagnostics,omitempty"`
	RecoveryActions      []string `json:"recovery_actions,omitempty"`
	Reconciled           bool     `json:"reconciled,omitempty"`

	// Reconcile is what --reconcile-cleanup did to each field it was asked
	// about: populated with one entry per field whenever the flag is passed,
	// and absent otherwise.
	//
	// Both reconcilers append unconditionally, so this is never empty when the
	// flag was passed — which is what makes its absence mean "the flag was not
	// passed" and nothing else. Until gt-hm0v three different situations
	// produced identical output: the flag never passed, the flag passed and
	// repaired nothing, and the flag passed, declined, and told nobody.
	Reconcile []ReconcileOutcome `json:"reconcile,omitempty"`
}

func runPolecatCheckRecovery(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}
	// From here on every error this command returns is a measurement result,
	// not a usage mistake — including the non-zero exit a refused
	// --reconcile-cleanup now carries. Cobra prints usage BELOW the error, so
	// without this the last line an operator reads is a flag description rather
	// than the predicate that stopped the repair.
	cmd.SilenceUsage = true

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Verify polecat exists and get info
	p, err := mgr.Get(polecatName)
	if err != nil {
		return fmt.Errorf("polecat '%s' not found in rig '%s'", polecatName, rigName)
	}

	// Get cleanup_status from agent bead
	// We need to read it directly from beads since manager doesn't expose it
	rigPath := r.Path
	bd := beads.New(rigPath)
	agentBeadID := polecatBeadIDForRig(r, rigName, polecatName)
	agentIssue, fields, err := bd.GetAgentBead(agentBeadID)

	status := RecoveryStatus{
		Rig:     rigName,
		Polecat: polecatName,
		Branch:  p.Branch,
		Issue:   p.Issue,
	}
	// Captured before anything can rewrite status.Issue. mgr.Get answers it by
	// querying the issue store for hooked beads assigned to this polecat, which
	// is the strongest "this polecat still holds work" fact available anywhere —
	// and below, status.Issue can also be FILLED IN from last_source_issue or
	// from an MR's source, neither of which is held work. Reading the field after
	// that point would be reading a different question's answer (gt-9f67).
	heldWorkBead := p.Issue
	beadTerminal := isAssignedBeadTerminal(bd, status.Issue)
	workTerminal := beadTerminal
	targetRefs, targetRefLookupFailed := recoveryTargetRefs(bd, status.Issue, status.ActiveMR, status.Branch)
	// Read session liveness up front. The bead facts gathered below are written
	// early in the completion sequence and lead the session by a minute or two,
	// which is what let this command answer SAFE_TO_NUKE for a polecat still
	// pushing its branch (gt-5tg). DecideWorkstate lets this outrank them.
	input := polecat.WorkstateInput{
		State:       p.State,
		SessionBusy: mgr.SessionBusy(polecatName),
		// Read from the same pane, and for the same reason: a logged-out agent
		// keeps its hooked bead and its working lifecycle state, so every fact
		// gathered below says it is fine. Only the pane says otherwise (gt-acb1).
		SessionLoggedOut: mgr.SessionLoggedOut(polecatName),
		// Both reads above are pane scrapes that answer "is this agent doing
		// something", and both stay silent when the answer is that there is no
		// agent. That left this command with no fact for the plainest question a
		// destructive verdict has to answer, and it reached SAFE_TO_NUKE and
		// PENDING_MR on a polecat whose session was gone at every sample point
		// without ever asking (gt-9f67). `tmux has-session` is the check that was
		// right each time the verdict was not.
		SessionPresence: mgr.SessionPresenceFor(polecatName),
		CleanupStatus:   polecat.CleanupUnknown,
		Branch:          p.Branch,
		// This command exists to gather the git and merge-queue facts (see
		// loadGitState and applyMQFactsToWorkstateInput below), so its verdict
		// is a measured one (gt-49dp).
		ReuseFactsMeasured: true,
	}

	// The third pane read, and the only one that costs anything. The two above
	// are single snapshots; this one samples twice across --liveness-window to
	// see whether the token counter moves, which is the only surface that
	// separates a generating agent from one wedged inside an open turn. Both
	// render the busy marker, so SessionBusy above says "working" for either.
	//
	// With no window this still runs and still reports — logged-out and parked
	// need no delta — but SuspectStall is false by construction, so the verdict
	// machinery is untouched for callers that did not pay for the measurement
	// (gt-y39t).
	//
	// A too-short window is refused OUT LOUD rather than silently degraded to a
	// single sample. Someone who typed --liveness-window asked a specific
	// question, and answering a different one without saying so is how "the
	// check ran and found nothing" becomes indistinguishable from "the check
	// never ran" — the reading that turns a working detector into a false
	// all-clear.
	if polecatCheckRecoveryLivenessWindow > 0 && polecatCheckRecoveryLivenessWindow < tmux.MinLivenessWindow {
		fmt.Fprintf(os.Stderr,
			"note: --liveness-window %s is below the %s minimum; taking one sample instead.\n"+
				"      A shorter window reports healthy agents as blocked — the token counter is\n"+
				"      displayed rounded to a hundred tokens, and a live agent was measured static\n"+
				"      across a 20s window while it was generating throughout.\n",
			polecatCheckRecoveryLivenessWindow, tmux.MinLivenessWindow)
	}
	liveness := mgr.SessionLiveness(polecatName, polecatCheckRecoveryLivenessWindow)
	if liveness.State != tmux.LivenessUnknown {
		status.Liveness = liveness.State.String()
	}
	if liveness.Sampled {
		status.LivenessWindow = liveness.Window.String()
	}
	input.SessionSuspectStall = liveness.SuspectStall()
	input.SessionStallWindow = liveness.Window
	// Set only where an open MR for this polecat was actually looked up and read
	// back open. It promotes a detected "stalled" to "handed-off" below, so it
	// must never be set from a fail-closed "could not rule one out".
	openMRProven := false
	var gitState *GitState
	var gitErr error
	gitStateLoaded := false
	loadGitState := func() {
		if gitStateLoaded {
			return
		}
		gitState, gitErr = getGitStateWithTargets(p.ClonePath, targetRefs)
		gitStateLoaded = true
	}

	if err != nil || fields == nil {
		// No agent bead or no cleanup_status - fall back to git check.
		loadGitState()
		if gitErr != nil {
			input.CleanupStatus = polecat.CleanupUnknown
			input.GitCheckFailed = true
			input.GitCheckFailedReason = fmt.Sprintf("git_state=unknown path=%s: %v", p.ClonePath, gitErr)
		} else if gitState.Clean {
			input.CleanupStatus = polecat.CleanupClean
		} else if gitState.UnpushedCommits > 0 {
			input.CleanupStatus = polecat.CleanupUnpushed
			input.UnpushedCommits = gitState.UnpushedCommits
		} else if gitState.StashCount > 0 {
			input.CleanupStatus = polecat.CleanupStash
			input.StashCount = gitState.StashCount
		} else {
			input.CleanupStatus = polecat.CleanupUncommitted
			input.GitDirty = true
			input.GitDirtyReason = fmt.Sprintf("git_state=has_uncommitted uncommitted_files=%d", len(gitState.UncommittedFiles))
		}
	} else {
		// Use cleanup_status from agent bead, then overlay direct git and MQ facts.
		input.CleanupStatus = polecat.CleanupStatus(fields.CleanupStatus)
		status.ActiveMR = fields.ActiveMR
		input.ActiveMR = fields.ActiveMR
		assignee := fmt.Sprintf("%s/polecats/%s", rigName, polecatName)
		hookBead := agentHookBead(agentIssue, fields)
		hook := assessHookBead(bd, hookBead, assignee)
		// Reported whether or not the hook blocks: the surfaces this predicate
		// read are the fact a reader needs to tell a real hook from a stale
		// association, and the old output named neither (gt-dh3d).
		if hook.Diagnostic != "" {
			status.Diagnostics = append(status.Diagnostics, hook.Diagnostic)
		}
		workTerminal = beadTerminal || hook.Terminal
		sourceHint := agentSourceIssueHint(status.Issue, fields)
		targetRefs, targetRefLookupFailed = recoveryTargetRefs(bd, status.Issue, status.ActiveMR, status.Branch, sourceHint)
		if status.Issue == "" && sourceHint != "" {
			status.Issue = sourceHint
		}
		if !beadTerminal && sourceHint != "" {
			beadTerminal = isAssignedBeadTerminal(bd, sourceHint)
			workTerminal = beadTerminal || hook.Terminal
		}
		if hook.Blocker != "" {
			input.HookBead = hookBead
		}
		input.PushFailed = fields.PushFailed
		input.MRFailed = fields.MRFailed
		input.MRRefused = fields.MRRefused
		// This command builds its input from mgr.Get(), and loadFromBeads maps
		// agent_state onto State for "done" and nothing else — so every paused
		// state arrived here as StateIdle and this surface, the one documented as
		// the one that measures, never saw the pause at all. That is why it
		// answered SAFE_TO_NUKE / witness_action=restart for a polecat `gt polecat
		// list` was simultaneously calling NEEDS_RECOVERY on agent_state=stuck
		// (gt-fbgq). Read straight from the agent bead instead.
		input.PausedAgentState = pausedAgentState(fields)
		partialSpawn, diagnostic := partialSpawnWithoutDurableHook(bd, fields, assignee, status.Issue)
		if diagnostic != "" {
			status.Diagnostics = append(status.Diagnostics, diagnostic)
		}
		activeMRAssessment := polecat.ActiveMRAssessment{}
		if fields.ActiveMR != "" {
			gitSafe := activeMRGitSafeForWorktree(p.ClonePath)
			activeMRAssessment = polecat.AssessActiveMR(bd, polecat.ActiveMRInput{
				ActiveMR:        fields.ActiveMR,
				SourceIssueHint: sourceHint,
				RequireGitSafe:  true,
				GitSafe:         gitSafe,
			})
			if status.Issue == "" && activeMRAssessment.SourceIssue != "" {
				status.Issue = activeMRAssessment.SourceIssue
			}
			if activeMRAssessment.SourceTerminal {
				beadTerminal = true
				workTerminal = true
			}
			if activeMRAssessment.Pending {
				input.ActiveMRBlocker = activeMRAssessment.Reason
				// Proven-open, not merely not-ruled-out: Stale covers the
				// missing/terminal MR, and an unverified or errored lookup never
				// sets MRStatus at all (gt-mkpm).
				openMRProven = openMRProven || (!activeMRAssessment.Stale && activeMRAssessment.MRStatus != "")
			}
		}
		input.PartialSpawnWithoutDurableHook = partialSpawn
		if blocker := cleanupStatusBlockerForRecovery(input.CleanupStatus, partialSpawn); blocker == "" && !input.CleanupStatus.IsSafe() {
			input.IgnoreCleanupStatus = true
		} else if blocker != "" {
			if input.CleanupStatus == polecat.CleanupUnpushed {
				loadGitState()
			}
			gitSafe := activeMRGitSafeForWorktree(p.ClonePath)
			if polecat.CanIgnoreStaleCleanupStatus(input.CleanupStatus, workTerminal, hook.Safe, !activeMRAssessment.Pending, gitSafe) {
				input.IgnoreCleanupStatus = true
				status.Diagnostics = append(status.Diagnostics, fmt.Sprintf("ignored_stale_cleanup_status=%s direct_git_state=safe work_ref=terminal", input.CleanupStatus))
			}
		}
		loadGitState()
		applyGitStateToWorkstateInput(&input, p.ClonePath, gitState, gitErr)
	}

	status.CleanupStatus = input.CleanupStatus
	status.SessionPresence = input.SessionPresence
	// Set once workTerminal is settled: a hooked bead that has since reached a
	// terminal status is not work anybody still holds, and passing it on would
	// escalate a polecat whose bead was closed out from under it. This feeds only
	// the dead-session precondition; it blocks nothing by itself.
	if !workTerminal {
		input.AssignedWorkBead = heldWorkBead
	}
	if applyMQFactsToWorkstateInput(&input, &status, bd, workTerminal, p.ClonePath, targetRefs, targetRefLookupFailed, gitState, gitErr) {
		openMRProven = true
	}
	// AFTER the merge-queue facts, because status.ActiveMR is what this verdict
	// will cite and applyMQFactsToWorkstateInput is where it settles — the agent
	// bead's own active_mr is cleared by `gt done`, so the cited MR is the one
	// found by branch. Matching the record against a field that has not been
	// filled in yet would refuse coverage on every polecat.
	if fields != nil {
		input.CompletionCoverage = polecat.CompletionCoverage(polecat.CompletionRecord{
			ExitType:        fields.ExitType,
			MRID:            fields.MRID,
			LastSourceIssue: fields.LastSourceIssue,
			CompletionTime:  fields.CompletionTime,
		}, status.ActiveMR, input.HookBead, input.AssignedWorkBead)
		if input.CompletionCoverage != "" {
			status.Diagnostics = append(status.Diagnostics, input.CompletionCoverage)
		}
	}
	// Same promotion `gt polecat list` applies, from the same helper and the same
	// bar of evidence. Doing it in one surface and not the other is how the two
	// came to print contradictory dispositions for the same polecat (gt-mkpm).
	input.State = polecat.HandedOffState(input.State, openMRProven)
	status.State = input.State
	// push_failed is set from the exit status of one `git push`, and a rebase makes
	// a non-fast-forward rejection there expected rather than fatal — so this
	// command, which HAS just measured the worktree, gets to say when the flag is
	// contradicted by what it measured. Reported either way: a flag that stopped
	// deciding must still be visible, or the next reader sees a clean polecat and
	// no trace of the field that stranded it (gt-3bzt).
	if input.PushFailed {
		input.PushFailedRefuted = polecat.GitFactsRefutePushFailed(
			gitErr == nil && gitState != nil && gitState.PreservationMeasured,
			input.GitCheckFailed, input.GitDirty, input.StashCount, input.UnpushedCommits)
		if input.PushFailedRefuted {
			status.Diagnostics = append(status.Diagnostics,
				"ignored_stale_push_failed=true direct_git_state=safe (clean tree, no stash, 0 unpreserved patches)")
		}
	}
	disposition := polecat.DecideWorkstate(input)
	applyWorkstateDispositionToRecoveryStatus(&status, disposition)

	if polecatCheckRecoveryReconcileCleanup {
		// Ordered before the cleanup reconcile because push_failed is the field
		// that gates the verdict the cleanup reconcile requires. Both fail closed
		// by turning the verdict into NEEDS_RECOVERY, so a failure in either one
		// stops the other rather than compounding.
		reconcilePushFailedIfRefuted(&status, bd, agentBeadID, input, fields)
		// And this one before the cleanup reconcile because agent_state=idle is
		// the gate the cleanup reconcile checks by name — a stale claim of work
		// in progress refused it on three polecats at once, and nothing anywhere
		// could write the field (gt-xj5d).
		reconcileAgentStateIfStale(&status, bd, agentBeadID, p, fields, input)
		reconcileCleanupStatusIfSafe(&status, bd, agentBeadID, p, fields, input)
	}

	// Read after the reconcile block, so the reported value is the one now
	// stored rather than the one this run found and repaired.
	if fields != nil {
		status.AgentState = strings.TrimSpace(fields.AgentState)
	}

	// "The bead still says push_failed and my own measurement says otherwise."
	// Recomputed AFTER the reconcile, so it is false once the field is actually
	// gone and true when this run was not asked to repair it. The SAFE_TO_NUKE
	// arm below names the command, because a reader who sees the flag ignored and
	// no way to remove it is back where gt-uapr started.
	stalePushFailed := pushFailedReconcileCandidate(&status, input, fields)

	// Derived last: reconcile and the MQ checks can still flip the verdict above,
	// and the permitted witness action must track the verdict actually reported.
	status.WitnessAction = witnessActionFor(status.Verdict, status.State)

	// Recorded from the final status, not from `disposition`: reconcile above can
	// still change the verdict, and the journal must agree with what this command
	// actually reported.
	if journalRoot, rootErr := workspace.FindFromCwd(); rootErr == nil {
		recordNeedsMQSubmitObservation(journalRoot, polecat.MQSubmitObservation{
			Rig:     rigName,
			Polecat: polecatName,
			Issue:   status.Issue,
			Branch:  status.Branch,
			Source:  "check-recovery",
		}, polecat.WorkstateDisposition{
			Verdict:       status.Verdict,
			Reason:        status.Reason,
			NeedsMQSubmit: status.NeedsMQSubmit,
			MQStatus:      status.MQStatus,
			Blockers:      status.Blockers,
		})
	}

	// JSON output
	if polecatCheckRecoveryJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(status); err != nil {
			return err
		}
		return reconcileExitError(status.Reconcile)
	}

	// Human-readable output
	fmt.Printf("%s\n\n", style.Bold.Render(fmt.Sprintf("Recovery Status: %s/%s", rigName, polecatName)))
	if status.State != "" {
		fmt.Printf("  State:           %s\n", status.State)
	}
	// Printed next to State, and never suppressed. State is a bead-and-queue
	// derivation — "handed-off" is assigned from an open MR — and this is the
	// direct measurement it can contradict. Side by side, "handed-off" over
	// "absent" is legible as the conflict it is (gt-9f67).
	fmt.Printf("  Session:         %s\n", recoverySessionPresenceLabel(status.SessionPresence))
	// Printed next to Session for the same reason Session is printed next to
	// State: this is the bead's claim about what the agent is doing, and that is
	// the direct measurement which can contradict it. Side by side,
	// "working" over "absent" is legible as the stale field it is — and it was
	// on no surface at all while it stranded three polecats (gt-xj5d).
	fmt.Printf("  Agent State:     %s\n", orUnknownRecoveryField(status.AgentState))
	// Rendered through cleanupStatusLabel, so a missing field reads as
	// "<missing>" rather than as a blank after the colon. Blank reads as a
	// formatting artifact — and this exact field being absent is the blocker
	// that stranded three polecats while the line that was supposed to report
	// it showed nothing at all (gt-hm0v).
	fmt.Printf("  Cleanup Status:  %s\n", cleanupStatusLabel(status.CleanupStatus))
	if status.Branch != "" {
		fmt.Printf("  Branch:          %s\n", status.Branch)
	}
	if status.Issue != "" {
		fmt.Printf("  Issue:           %s\n", status.Issue)
	}
	if status.ActiveMR != "" {
		fmt.Printf("  Active MR:       %s\n", status.ActiveMR)
	}
	if status.Liveness != "" {
		// Printed next to the verdict, not inside it. The verdict answers "is
		// work at risk"; this answers "is the agent doing anything", and the
		// whole of gt-y39t is that those two were one field. A reader who sees
		// WORKING here also sees whether the pane agrees.
		liveline := status.Liveness
		if status.LivenessWindow != "" {
			liveline += fmt.Sprintf(" (token counter sampled %s apart)", status.LivenessWindow)
		} else {
			liveline += " (one sample; --liveness-window separates working from blocking-wait)"
		}
		fmt.Printf("  Liveness:        %s\n", liveline)
	}
	if len(status.Diagnostics) > 0 {
		fmt.Printf("  Diagnostics:     %s\n", strings.Join(status.Diagnostics, "; "))
	}
	printReconcileOutcomes(os.Stdout, polecatCheckRecoveryReconcileCleanup, status.Reconcile)
	fmt.Println()

	switch status.Verdict {
	case "WORKING":
		// Must be its own case, not the default arm: the default prints
		// SAFE_TO_NUKE, so an unlisted verdict renders as permission to destroy
		// a polecat that is still generating — the exact confusion gt-5tg is about.
		fmt.Printf("  Verdict:         %s\n", style.Warning.Render("WORKING"))
		fmt.Printf("  Witness action:  %s\n", status.WitnessAction)
		fmt.Println()
		printPolecatWorkingEvidence(os.Stdout, status.Reason, rigName, polecatName)
	case polecat.WorkstateVerdictNeedsLogin:
		fmt.Printf("  Verdict:         %s\n", style.Error.Render("NEEDS_LOGIN"))
		fmt.Printf("  Witness action:  %s\n", status.WitnessAction)
		fmt.Println()
		fmt.Printf("  %s The pane was read and the agent is sitting at an auth wall.\n", style.Warning.Render("⚠"))
		fmt.Println("  It holds a slot it cannot work in, and it will stay there until a HUMAN")
		fmt.Println("  runs /login in its session. Attach and log in:")
		fmt.Printf("    gt session at %s/%s\n", rigName, polecatName)
		fmt.Println()
		fmt.Println("  Do NOT restart it. No restart path supplies credentials, so a restart")
		fmt.Println("  produces another logged-out session and destroys the agent's context on")
		fmt.Println("  the way — restart-first is correct for ordinary stalls and wrong here.")
		fmt.Println("  After the login, wake the existing session rather than respawning it:")
		fmt.Printf("    gt nudge %s/%s \"resume your work\"\n", rigName, polecatName)
		fmt.Println()
		fmt.Printf("  %s\n", style.Dim.Render("This verdict used to read WORKING, because a logged-out agent keeps its"))
		fmt.Printf("  %s\n", style.Dim.Render("hooked bead and its lifecycle state — hooked-ness read as liveness (gt-acb1)"))
	case polecat.WorkstateVerdictSuspectStall:
		fmt.Printf("  Verdict:         %s\n", style.Error.Render("SUSPECT_STALL"))
		fmt.Printf("  Witness action:  %s\n", status.WitnessAction)
		fmt.Println()
		for _, blocker := range status.Blockers {
			fmt.Printf("    - %s\n", blocker)
		}
		fmt.Println()
		fmt.Printf("  %s The pane was sampled twice. The agent's turn clock climbed and its\n", style.Warning.Render("⚠"))
		fmt.Println("  token counter did not, and nothing on screen was running that it could")
		fmt.Println("  be waiting on. A turn is open and nothing is moving inside it.")
		fmt.Println()
		fmt.Println("  Look before acting — this is evidence of not-thinking, not proof of death:")
		fmt.Printf("    gt peek %s/%s\n", rigName, polecatName)
		fmt.Println()
		fmt.Println("  Do NOT restart on this alone. An agent legitimately inside a long sleep or")
		fmt.Println("  a slow test produces the same token signature; what separates it is the")
		fmt.Println("  command in flight, and this verdict fires only when none was visible.")
		fmt.Println("  A restart would destroy the agent's context to answer a question a human")
		fmt.Println("  answers by looking at the pane.")
		fmt.Println()
		fmt.Printf("  %s\n", style.Dim.Render("This case used to read WORKING: a wedged agent renders the busy marker"))
		fmt.Printf("  %s\n", style.Dim.Render("exactly like a generating one, so the binary detector could never see it (gt-y39t)"))
	case "NEEDS_MQ_SUBMIT":
		fmt.Printf("  Verdict:         %s\n", style.Warning.Render("NEEDS_MQ_SUBMIT"))
		fmt.Printf("  MQ Status:       %s\n", status.MQStatus)
		fmt.Printf("  Witness action:  %s\n", status.WitnessAction)
		fmt.Println()
		fmt.Printf("  %s Work is pushed but was never submitted to the merge queue.\n", style.Warning.Render("⚠"))
		fmt.Println("  Submit to MQ before cleanup, or the branch will be orphaned.")
	case "PENDING_MR":
		fmt.Printf("  Verdict:         %s\n", style.Warning.Render("PENDING_MR"))
		fmt.Printf("  Witness action:  %s\n", status.WitnessAction)
		fmt.Println()
		fmt.Println("  Work is waiting on an active merge request; preserve this polecat until it lands.")
	case "NEEDS_RECOVERY":
		fmt.Printf("  Verdict:         %s\n", style.Error.Render("NEEDS_RECOVERY"))
		fmt.Printf("  Witness action:  %s\n", status.WitnessAction)
		fmt.Println()
		if len(status.Blockers) > 0 {
			fmt.Printf("  %s Cleanup refused by these predicate(s):\n", style.Warning.Render("⚠"))
			for _, blocker := range status.Blockers {
				fmt.Printf("    - %s\n", blocker)
			}
			if len(status.RecoveryActions) > 0 {
				fmt.Println()
				fmt.Println("  Recovery action(s):")
				for _, action := range status.RecoveryActions {
					fmt.Printf("    - %s\n", action)
				}
			}
		} else {
			// Reachable only if a future predicate returns NEEDS_RECOVERY with no
			// blocker at all. Name the reason rather than calling the predicate
			// unknown: it was never unknown to the code, and an unnamed refusal
			// is unactionable by construction (hq-qm7bt, gt-mkpm).
			fmt.Printf("  %s Cleanup refused, and this verdict carried no blocker to name.\n", style.Warning.Render("⚠"))
			fmt.Printf("    reason=%s state=%s — please report this output; it is a reporting bug.\n",
				orUnknownRecoveryField(status.Reason), orUnknownRecoveryField(string(status.State)))
		}
		fmt.Println("  Escalate to Mayor for recovery before cleanup.")
	case polecat.WorkstateVerdictNeedsStateClear:
		fmt.Printf("  Verdict:         %s\n", style.Warning.Render("NEEDS_STATE_CLEAR"))
		fmt.Printf("  Witness action:  %s\n", status.WitnessAction)
		fmt.Println()
		for _, blocker := range status.Blockers {
			fmt.Printf("    - %s\n", blocker)
		}
		fmt.Println()
		fmt.Println("  No work is at risk. The polecat is parked at a deliberate agent_state that")
		fmt.Println("  outlives every session restart — no restart path writes agent_state at all.")
		fmt.Println("  Lift the pause (agent bead only; worktree and branch untouched):")
		fmt.Printf("    gt polecat clear-state %s/%s\n", rigName, polecatName)
		fmt.Println()
		fmt.Printf("  %s\n", style.Dim.Render("Restarting will NOT change this state — check-recovery used to say it would (gt-fbgq)"))
	case "UNVERIFIED":
		// Unreachable from this command — it measures, so its input carries
		// ReuseFactsMeasured. Listed anyway because the default arm below prints
		// SAFE_TO_NUKE, and "no facts were gathered" is the last verdict that
		// should render as permission to destroy anything (gt-49dp).
		fmt.Printf("  Verdict:         %s\n", style.Warning.Render("UNVERIFIED"))
		fmt.Printf("  Witness action:  %s\n", status.WitnessAction)
		fmt.Println()
		fmt.Println("  No git or merge-queue facts were gathered for this polecat, so nothing")
		fmt.Println("  has been ruled out. Treat it as unknown, not as safe.")
	default:
		// Deliberately NOT success-styled (gt-y20). This verdict says "no work
		// is at risk", not "you may destroy this polecat" — and the Witness is
		// the main caller. A green checkmark next to the word "nuke" is what
		// steered the witness in dn-v29.
		fmt.Printf("  Verdict:         %s\n", style.Dim.Render("SAFE_TO_NUKE (no work at risk)"))
		if status.MQStatus != "" {
			fmt.Printf("  MQ Status:       %s\n", status.MQStatus)
		}
		fmt.Printf("  Witness action:  %s\n", status.WitnessAction)
		fmt.Println()
		if polecat.StateEligibleForPoolReuse(status.State) {
			// No command offered, because there is no action to take. The
			// previous text here named a restart, and a reader following it
			// spent a healthy polecat to reclaim a slot that was never occupied
			// (gt-t6k2).
			fmt.Printf("  No work at risk, and state=%s is ALREADY eligible for pool reuse — the\n", status.State)
			fmt.Println("  next `gt sling` can take this polecat as it stands. Nothing to reclaim,")
			fmt.Println("  so nothing to do.")
			fmt.Println()
			fmt.Println("  Do NOT restart it to \"reclaim the slot\". The reuse gate re-reads this")
			fmt.Println("  state on every sling, so a restart cannot improve it — and the fresh")
			fmt.Println("  session primes, finds an empty hook, and runs `gt done`, which is where a")
			fmt.Println("  fork-mode polecat with an already-closed bead parks at agent_state=stuck")
			fmt.Println("  and needs `gt polecat clear-state` by hand (gt-j9uv, gt-gubw).")
		} else {
			fmt.Println("  No work at risk. Reclaim the slot by restarting — the sandbox is preserved:")
			fmt.Printf("    gt session restart %s/%s\n", rigName, polecatName)
		}
		if stalePushFailed {
			fmt.Println()
			fmt.Println("  The agent bead still records push_failed=true, which this run's own git")
			fmt.Println("  measurement contradicts — a rebase makes a non-fast-forward rejection the")
			fmt.Println("  expected outcome, not a lost push. Restarting will NOT remove it, and every")
			fmt.Println("  surface that runs no git (gt polecat list among them) keeps reporting this")
			fmt.Println("  polecat as idle-recovery-needed until it is gone. Repair the field:")
			fmt.Printf("    gt polecat check-recovery %s/%s --reconcile-cleanup\n", rigName, polecatName)
		}
		fmt.Println()
		fmt.Printf("  %s\n", style.Dim.Render("Nuking is not a witness action — it requires a human or Mayor identity"))
		fmt.Printf("  %s\n", style.Dim.Render("(restart-first policy, gt-dsgp)"))
	}

	return reconcileExitError(status.Reconcile)
}

// printReconcileOutcomes renders what --reconcile-cleanup did, and renders
// something on every road the flag was passed on.
//
// The heading is printed even when nothing was repaired. That is the point:
// the flag's defect was that a run which repaired nothing looked exactly like a
// run that was never asked to, and the remaining output — an ordinary refusal
// report — read as "the safe path did not apply here", which is the reasoning
// that sends the next operator to the destructive one (gt-hm0v).
func printReconcileOutcomes(w io.Writer, requested bool, outcomes []ReconcileOutcome) {
	if !requested {
		return
	}
	fmt.Fprintln(w, "  Reconcile:")
	if len(outcomes) == 0 {
		// Unreachable while both reconcilers report unconditionally, and
		// written out anyway: the failure this replaces WAS a silent road that
		// nobody knew existed, and a heading with nothing under it is at least
		// visible.
		fmt.Fprintln(w, "    - (no field was evaluated — please report this output; it is a reporting bug)")
		return
	}
	for _, o := range outcomes {
		fmt.Fprintf(w, "    - %s: %s (was %s) — %s\n", o.Field, o.Action, o.Previous, o.Detail)
	}
}

// reconcileExitError turns a repair that did not happen into a non-zero exit.
//
// A caller cannot tell "refused" from "done" by reading prose, and the
// diagnostic and the status code disagreeing is what made this silent: exit 0,
// empty stderr, and stdout that looked like an ordinary report. The status code
// is what callers read, so the status code has to carry the answer (gt-hm0v).
func reconcileExitError(outcomes []ReconcileOutcome) error {
	if !reconcileOutcomesActionable(outcomes) {
		return nil
	}
	var unmet []string
	for _, o := range outcomes {
		if o.Action == reconcileActionRefused || o.Action == reconcileActionFailed {
			unmet = append(unmet, fmt.Sprintf("%s %s: %s", o.Field, o.Action, o.Detail))
		}
	}
	return fmt.Errorf("--reconcile-cleanup made no change: %s", strings.Join(unmet, "; "))
}

// recoverySessionPresenceLabel renders the liveness measurement for a human,
// including the case where there was none.
//
// "unknown" is spelled out rather than left blank on purpose: a blank field
// reads as "fine, nothing to report", and the whole finding in gt-9f67 is that a
// missing liveness answer was invisible to four readers in a row.
func recoverySessionPresenceLabel(l polecat.SessionPresence) string {
	switch l {
	case polecat.SessionPresent:
		return "present (tmux has-session)"
	case polecat.SessionAbsent:
		return "absent (tmux has-session found no session)"
	default:
		return "unknown (tmux has-session did not run or could not answer)"
	}
}

// recoveryPolecatAddress renders the <rig>/<polecat> a refusal should tell the
// reader to run its remedy against, and falls back to the placeholder rather
// than to a bare "/" when the status carries no identity. A command line with an
// empty address in it is one a reader will paste and be confused by.
func recoveryPolecatAddress(status *RecoveryStatus) string {
	if status == nil || status.Rig == "" || status.Polecat == "" {
		return "<rig>/<polecat>"
	}
	return status.Rig + "/" + status.Polecat
}

func orUnknownRecoveryField(s string) string {
	if strings.TrimSpace(s) == "" {
		return "<unset>"
	}
	return s
}

// printPolecatWorkingEvidence renders the WORKING verdict's prose from the
// evidence that verdict actually rests on.
//
// There are two roads to WORKING and they are not the same claim. session-busy
// read the pane (Tmux.IsBusy) and found the agent generating. not-idle read the
// AGENT BEAD and found a lifecycle state of working — it never looked at a pane.
// Both printed "The agent's pane shows it mid-turn", which on the second road is
// a positive claim about a surface the command did not consult.
//
// Measured three times in one evening: false on gastown/crater (parked, no
// interrupt line, empty composer, "Churned for 13m 51s", had exited DEFERRED 14
// minutes earlier), false on gastown/brahmin (parked; a re-run a minute later
// gave NEEDS_RECOVERY and named push_failed), true on gastown/foundation
// (genuinely mid-turn). Same sentence all three times, with nothing in the
// output separating them — and its prescription is leave-alone, which on the
// brahmin instance argued for NOT acting on an unhealthy polecat (gt-mkpm).
func printPolecatWorkingEvidence(w io.Writer, reason, rigName, polecatName string) {
	if reason == polecat.WorkstateReasonSessionBusy {
		fmt.Fprintln(w, "  Evidence: the tmux pane was read and the agent is generating right now.")
		fmt.Fprintln(w, "  Bead state can say 'done' a minute or two before the session actually exits —")
		fmt.Fprintln(w, "  this verdict reflects the session, and outranks the bead.")
		fmt.Fprintln(w, "  Leave it alone and re-check once the pane is quiet.")
		return
	}
	fmt.Fprintf(w, "  Evidence: the AGENT BEAD says working (reason=%s). The pane was NOT measured busy.\n",
		orUnknownRecoveryField(reason))
	fmt.Fprintln(w, "  A polecat that has finished and parked reads exactly like this, so do not take")
	fmt.Fprintln(w, "  leave-alone from this verdict alone. Read the pane yourself:")
	fmt.Fprintf(w, "    tmux capture-pane -p -t %s | grep -c 'esc to interrupt'\n",
		session.PolecatSessionName(session.PrefixFor(rigName), polecatName))
	fmt.Fprintln(w, "  0 = parked (nudge or restart it), >0 = genuinely mid-turn (leave it alone).")
}

func applyGitStateToWorkstateInput(input *polecat.WorkstateInput, worktreePath string, gitState *GitState, gitErr error) {
	if gitErr != nil {
		input.GitCheckFailed = true
		input.GitCheckFailedReason = recoveryGitStateBlocker(worktreePath, gitState, gitErr)
		return
	}
	if gitState == nil || gitState.Clean {
		return
	}
	if gitState.UnpushedCommits > 0 {
		input.UnpushedCommits = gitState.UnpushedCommits
	}
	if gitState.StashCount > 0 {
		input.StashCount = gitState.StashCount
	}
	if len(gitState.UncommittedFiles) > 0 {
		input.GitDirty = true
		input.GitDirtyReason = fmt.Sprintf("git_state=has_uncommitted uncommitted_files=%d", len(gitState.UncommittedFiles))
	}
}

// applyMQFactsToWorkstateInput folds the merge-queue facts into the input and
// reports whether it PROVED an open MR exists for this polecat's branch — the
// one fact that separates a handed-off polecat from a stalled one (gt-mkpm).
func applyMQFactsToWorkstateInput(input *polecat.WorkstateInput, status *RecoveryStatus, bd *beads.Beads, beadTerminal bool, worktreePath string, targetRefs []string, targetRefLookupFailed bool, gitState *GitState, gitErr error) (openMRProven bool) {
	if status.Branch == "" {
		return false
	}
	input.MQCheckRequired = true
	input.AssignedBeadTerminal = beadTerminal
	input.HasSubmittableWork = hasSubmittableWorkForRecovery(worktreePath, targetRefs, gitState, gitErr)
	input.MQNotRequired = isMQNotRequiredSource(bd, status.Issue)
	input.SourceCloseDischargesMQ = sourceCloseDischargesMQForRecovery(bd, status.Issue)
	if targetRefLookupFailed {
		input.MQLookupFailed = true
	}
	if !input.HasSubmittableWork || input.MQNotRequired {
		return false
	}
	// A terminal source bead used to skip this lookup, on the theory that a
	// closed bead has nothing left to submit. But the closed bead is exactly the
	// stranding signature (gt-46rk): gt done refuses the MR precisely BECAUSE
	// the bead is closed, so skipping the lookup blinded the check to the only
	// case it needed to see. The policy layer already treats a terminal source
	// as no proof of submission; skipping the lookup meant the opposite fact —
	// that a rescue MR now exists — could not be seen either, in either
	// direction. The lookup is cheap and truthful, so run it.
	mr, mrErr := bd.FindMRForBranchAny(status.Branch)
	if mrErr != nil {
		input.MQLookupFailed = true
		return false
	}
	if mr == nil {
		return false
	}
	mrOpen := !beads.IssueStatus(mr.Status).IsTerminal()
	polecat.ApplyBranchMRToWorkstateInput(input, mr.ID, mrOpen)
	status.ActiveMR = input.ActiveMR
	return mrOpen
}

// recordNeedsMQSubmitObservation journals the needs_mq_submit transitions for
// one polecat. The verdict itself stays computed-on-read; this is what makes the
// EPISODE durable, so that a fired check is still answerable minutes later and a
// check that silently stops firing stops leaving lines (gt-7i07).
//
// Failures are reported on stderr and never returned: the caller was asked to
// report on a polecat, not to maintain the journal, and a listing that aborts
// because a log line could not be written is a worse outcome than a gap. The
// warning exists because a silent gap is exactly the failure mode being fixed.
// A test binary's refusal to touch a live town is expected, not a defect.
func recordNeedsMQSubmitObservation(townRoot string, obs polecat.MQSubmitObservation, disposition polecat.WorkstateDisposition) {
	if townRoot == "" {
		return
	}
	if _, err := polecat.RecordNeedsMQSubmit(townRoot, obs, disposition); err != nil && !errors.Is(err, testguard.ErrRefused) {
		fmt.Fprintf(os.Stderr, "warning: failed to record needs_mq_submit for %s/%s: %v\n", obs.Rig, obs.Polecat, err)
	}
}

func applyWorkstateDispositionToRecoveryStatus(status *RecoveryStatus, disposition polecat.WorkstateDisposition) {
	status.Verdict = disposition.Verdict
	status.Reason = disposition.Reason
	status.Reusable = disposition.Reusable
	status.SafeToNuke = disposition.SafeToNuke
	status.NeedsRecovery = disposition.NeedsRecovery
	status.NeedsMQSubmit = disposition.NeedsMQSubmit
	status.NeedsLogin = disposition.NeedsLogin
	status.CountsTowardCapacity = disposition.CountsTowardCapacity
	status.ReuseStatus = disposition.ReuseStatus
	status.MQStatus = disposition.MQStatus
	status.Blockers = disposition.Blockers
	status.RecoveryActions = recoveryActionsForBlockers(disposition.Blockers, status.Rig, status.Polecat)
}

type issueShower interface {
	Show(issueID string) (*beads.Issue, error)
}

func cleanupStatusBlocker(status polecat.CleanupStatus) string {
	switch status {
	case polecat.CleanupClean:
		return ""
	case "":
		return "cleanup_status=<missing>"
	case polecat.CleanupUnknown:
		return "cleanup_status=unknown"
	default:
		return fmt.Sprintf("cleanup_status=%s", status)
	}
}

func cleanupStatusBlockerForRecovery(status polecat.CleanupStatus, partialSpawnWithoutHook bool) string {
	if partialSpawnWithoutHook && (status == "" || status == polecat.CleanupUnknown) {
		return ""
	}
	return cleanupStatusBlocker(status)
}

func agentHookBead(agentIssue *beads.Issue, fields *beads.AgentFields) string {
	if agentIssue != nil && agentIssue.HookBead != "" {
		return agentIssue.HookBead
	}
	if fields != nil {
		return fields.HookBead
	}
	return ""
}

func activeMRGitSafeForWorktree(worktreePath string) bool {
	g := git.NewGit(worktreePath)
	branch, err := g.CurrentBranch()
	if err != nil || branch == "" {
		return false
	}
	status, err := g.CheckUncommittedWork()
	if err != nil || !status.CleanExcludingRuntime() || status.StashCount > 0 || status.UnpushedCommits > 0 {
		return false
	}
	pushed, unpushed, err := g.BranchPushedToRemote(branch, "origin")
	if err != nil {
		return false
	}
	return pushed && unpushed == 0
}

// hookBeadDisposition is what reading an agent bead's hook slot actually
// established. Diagnostic names the surfaces that were read, so a caller can
// report a disagreement between them instead of presenting one surface's answer
// as the fact (gt-dh3d).
type hookBeadDisposition struct {
	Safe       bool // the hook slot does not block cleanup
	Terminal   bool // the work bead it names has reached a terminal status
	Unverified bool // the work bead could not be read, so nothing was established
	Blocker    string
	Diagnostic string
}

// assessHookBead decides whether the hook slot recorded on an agent bead still
// names work this polecat holds, and reports which surfaces said so.
//
// check-recovery refused cleanup on "has work on hook (gt-2uqy)" for a bead
// that was simultaneously open, unassigned, and sitting in `bd ready` — see
// beads.HookSlotReleased for why the slot outlives the assignment and why the
// work bead settles it (gt-dh3d).
func assessHookBead(bd issueShower, hookBead, assignee string) hookBeadDisposition {
	if hookBead == "" {
		return hookBeadDisposition{Safe: true}
	}
	if bd == nil {
		return hookBeadDisposition{Unverified: true, Blocker: fmt.Sprintf("hook_bead=%s status=unverified", hookBead)}
	}
	issue, err := bd.Show(hookBead)
	if err != nil {
		return hookBeadDisposition{Unverified: true, Blocker: fmt.Sprintf("hook_bead=%s status=lookup_error: %v", hookBead, err)}
	}
	if issue == nil {
		return hookBeadDisposition{Unverified: true, Blocker: fmt.Sprintf("hook_bead=%s status=missing", hookBead)}
	}

	status := beads.IssueStatus(issue.Status)
	beadAssignee := strings.TrimSpace(issue.Assignee)
	surface := fmt.Sprintf("hook_bead=%s source=agent_bead.hook_bead store_status=%s store_assignee=%s",
		hookBead, issue.Status, hookBeadAssigneeForDiagnostic(beadAssignee))

	if status.IsTerminal() {
		return hookBeadDisposition{Safe: true, Terminal: true, Diagnostic: surface + " hook=released (work terminal)"}
	}
	if beads.HookSlotReleased(issue, assignee) {
		return hookBeadDisposition{Safe: true, Diagnostic: surface + " hook=stale (issue store does not hold it for this polecat)"}
	}
	return hookBeadDisposition{
		Blocker:    fmt.Sprintf("hook_bead=%s status=%s", hookBead, issue.Status),
		Diagnostic: surface + " hook=held",
	}
}

func hookBeadAssigneeForDiagnostic(beadAssignee string) string {
	if beadAssignee == "" {
		return "<none>"
	}
	return beadAssignee
}

// cleanupStatusUpdater is the write half of the cleanup_status reconcile, plus
// the reader it confirms itself with. The read-back is not optional: this
// reconcile exists because a field nothing clears kept polecats out of the
// pool, and a write that reported success without landing would put them right
// back there with a diagnostic saying otherwise (gt-hm0v).
type cleanupStatusUpdater interface {
	UpdateAgentCleanupStatus(id string, cleanupStatus string) error
	GetAgentBead(id string) (*beads.Issue, *beads.AgentFields, error)
}

// Actions a single field reconcile can end in. Every road out of a reconcile
// lands on exactly one of these — including the roads that used to `return`
// with nothing written and nothing said (gt-hm0v).
const (
	// reconcileActionWritten: the field was rewritten and the write was
	// confirmed by re-reading the bead.
	reconcileActionWritten = "written"
	// reconcileActionNoChange: there was nothing to repair. Not a refusal, and
	// not a failure — the requested end state already holds.
	reconcileActionNoChange = "no_change"
	// reconcileActionRefused: a predicate stopped the write. Detail names it.
	reconcileActionRefused = "refused"
	// reconcileActionFailed: the write was attempted and did not land.
	reconcileActionFailed = "failed"
)

// ReconcileOutcome is what --reconcile-cleanup DID to one field, reported
// whether or not anything changed.
//
// It exists because the flag's failure mode was silence. Asked to repair a
// stale cleanup_status, it evaluated a predicate, returned false, wrote
// nothing, said nothing, and exited 0 — so the caller could not tell "repaired"
// from "declined" from "never looked", and the output that remained read as an
// ordinary refusal report rather than as a report that the requested action was
// skipped. Exit code, stderr and stdout all agreed on success while the field
// stayed exactly as it was (gt-hm0v).
//
// So: one outcome per field per run, always appended, always printed, and
// `refused`/`failed` carry the exit status with them.
type ReconcileOutcome struct {
	Field    string `json:"field"`              // cleanup_status, push_failed
	Action   string `json:"action"`             // written, no_change, refused, failed
	Previous string `json:"previous,omitempty"` // the value found before the run
	Detail   string `json:"detail"`             // why — never empty, on any road
}

// reconcileOutcomesActionable reports whether any reconcile this run was asked
// to perform did not happen. It is what turns the exit status non-zero, so a
// caller can tell "refused" from "done" without parsing prose.
func reconcileOutcomesActionable(outcomes []ReconcileOutcome) bool {
	for _, o := range outcomes {
		if o.Action == reconcileActionRefused || o.Action == reconcileActionFailed {
			return true
		}
	}
	return false
}

// cleanupStatusLabel renders a cleanup_status for a human, including the empty
// case. Never blank: a blank value here is the one this bug was made of, and
// "cleanup_status: " with nothing after it reads as a formatting artifact
// rather than as the missing field it is.
func cleanupStatusLabel(status polecat.CleanupStatus) string {
	if status == "" {
		return "<missing>"
	}
	return string(status)
}

// blockerSummary renders a disposition's blockers for a one-line refusal.
func blockerSummary(blockers []string) string {
	if len(blockers) == 0 {
		return "no blocker was recorded"
	}
	return "still blocked by: " + strings.Join(blockers, "; ")
}

// pushFailedUpdater is the write half of the push_failed reconcile. It is its own
// interface rather than an addition to cleanupStatusUpdater so the two reconciles
// stay independently fakeable, and it demands the reader as well as the writer
// because this reconcile confirms its own write (see below).
type pushFailedUpdater interface {
	UpdateAgentDescriptionFields(id string, updates beads.AgentFieldUpdates) error
	GetAgentBead(id string) (*beads.Issue, *beads.AgentFields, error)
}

// reconcilePushFailedIfRefuted writes push_failed=false when this command has
// MEASURED the worktree and found nothing a failed push could have lost.
//
// gt-3bzt taught the three measuring surfaces to IGNORE a contradicted
// push_failed. That unblocked reuse but left the field itself set forever, and
// the field is what the bead-only surfaces read: `gt polecat list` runs no git,
// so it cannot refute anything, and went on reporting idle-recovery-needed for a
// polecat whose work was provably in main. Nothing else clears it either —
// elapsed time, the MR merging, a session restart, and a clean exit-0 park were
// each measured ineffective, and `gt polecat clear-state` writes agent_state and
// deliberately nothing else. So the flag outlived every remedy any role could
// run (gt-uapr).
//
// This is that clearing path, and the witness already reaches it: the SLOT_OPEN
// handler runs `gt polecat check-recovery --json --reconcile-cleanup` seconds
// after a polecat exits (internal/witness/handlers.go), which is exactly when a
// rebase-then-push rejection has just set the flag.
//
// It writes only on the same evidence that entitles this command to disregard
// the flag in the first place — PushFailedRefuted, which requires the git checks
// to have RUN, not merely to have returned zeros — plus a verdict of
// SAFE_TO_NUKE, so a polecat blocked by anything else keeps its flag and its
// recovery. An unmeasured or failed git check leaves PushFailedRefuted false and
// nothing is written.
func reconcilePushFailedIfRefuted(status *RecoveryStatus, updater pushFailedUpdater, agentBeadID string, input polecat.WorkstateInput, fields *beads.AgentFields) {
	out, ok := pushFailedReconcilePlan(status, input, fields)
	if !ok {
		status.Reconcile = append(status.Reconcile, out)
		return
	}
	if updater == nil {
		pushFailedReconcileFailed(status, "updater unavailable")
		return
	}
	cleared := false
	if err := updater.UpdateAgentDescriptionFields(agentBeadID, beads.AgentFieldUpdates{PushFailed: &cleared}); err != nil {
		pushFailedReconcileFailed(status, err.Error())
		return
	}
	// Read back. This whole function exists because a field nothing clears kept
	// a polecat out of the pool; reporting a clear that did not land would put it
	// right back there with a diagnostic saying otherwise. The write path is a bd
	// subprocess against Dolt and a nil error is not evidence the row changed.
	_, after, err := updater.GetAgentBead(agentBeadID)
	if err != nil {
		pushFailedReconcileFailed(status, fmt.Sprintf("write reported success but the bead could not be re-read: %v", err))
		return
	}
	if after == nil || after.PushFailed {
		pushFailedReconcileFailed(status, "write reported success but the bead still reads push_failed=true")
		return
	}
	// Keep the caller's in-memory view in step with the store it just changed.
	// Anything downstream that re-reads these fields — including this command's
	// own "is the flag still stale" check — would otherwise go on describing a
	// polecat that no longer exists.
	fields.PushFailed = false
	status.Reconciled = true
	status.Reconcile = append(status.Reconcile, ReconcileOutcome{
		Field:    "push_failed",
		Action:   reconcileActionWritten,
		Previous: "true",
		Detail:   "cleared and confirmed by re-reading the agent bead; direct git state is safe (clean tree, no stash, 0 unpreserved patches)",
	})
	status.Diagnostics = append(status.Diagnostics,
		"reconciled_push_failed=false previous=true direct_git_state=safe (clean tree, no stash, 0 unpreserved patches)")
}

func pushFailedReconcileFailed(status *RecoveryStatus, detail string) {
	status.NeedsRecovery = true
	status.Verdict = "NEEDS_RECOVERY"
	status.Blockers = append(status.Blockers, fmt.Sprintf("push_failed_reconcile_failed: %s", detail))
	status.Reconcile = append(status.Reconcile, ReconcileOutcome{
		Field:    "push_failed",
		Action:   reconcileActionFailed,
		Previous: "true",
		Detail:   detail,
	})
}

// pushFailedReconcilePlan is pushFailedReconcileCandidate plus the sentence it
// never said. Same predicates, same order, same answers — the difference is
// that a false now arrives attached to the reason for it, so a run that
// declined to clear push_failed is distinguishable from a run that cleared it
// (gt-hm0v).
func pushFailedReconcilePlan(status *RecoveryStatus, input polecat.WorkstateInput, fields *beads.AgentFields) (ReconcileOutcome, bool) {
	out := ReconcileOutcome{Field: "push_failed", Action: reconcileActionRefused, Previous: "true"}
	if status == nil || fields == nil {
		out.Previous = "<unread>"
		out.Detail = "no agent bead was loaded, so push_failed was never read"
		return out, false
	}
	if !fields.PushFailed {
		out.Action = reconcileActionNoChange
		out.Previous = "false"
		out.Detail = "not set; nothing to reconcile"
		return out, false
	}
	if !input.PushFailedRefuted {
		// Refused, not no_change: the flag IS set, this run was asked to clear
		// it, and it is still there. Reporting that as "nothing to do" is what
		// let a stranded polecat read as a healthy one.
		out.Detail = "this run's git measurement did not refute it (a failed push may still have lost work); " +
			"clearing it requires a measured worktree that is clean, unstashed and fully pushed"
		return out, false
	}
	if status.Verdict != "SAFE_TO_NUKE" {
		out.Detail = fmt.Sprintf("verdict=%s — %s", status.Verdict, blockerSummary(status.Blockers))
		return out, false
	}
	out.Action = ""
	out.Detail = ""
	return out, true
}

// pushFailedReconcileCandidate answers "is push_failed still set and still
// contradicted by this run's own measurement" — the condition the SAFE_TO_NUKE
// output arm names the repair command for.
//
// It delegates to pushFailedReconcilePlan so the two can never drift: the
// predicates are refuted-by-measurement, never by silence (the same bar
// DecideWorkstate applies before it stops letting the flag block, gt-3bzt), and
// a verdict of SAFE_TO_NUKE, because a polecat that still has a hook, a dirty
// tree, or work outside the merge queue needs recovery whatever push_failed
// says, and clearing the field there would shrink its blocker list without
// changing its situation.
func pushFailedReconcileCandidate(status *RecoveryStatus, input polecat.WorkstateInput, fields *beads.AgentFields) bool {
	_, ok := pushFailedReconcilePlan(status, input, fields)
	return ok
}

// agentStateUpdater is the write half of the agent_state reconcile. It is its
// own interface rather than a reuse of pushFailedUpdater so the two stay
// independently fakeable, and it demands the reader as well as the writer
// because this reconcile confirms its own write.
type agentStateUpdater interface {
	UpdateAgentDescriptionFields(id string, updates beads.AgentFieldUpdates) error
	GetAgentBead(id string) (*beads.Issue, *beads.AgentFields, error)
}

// reconcileAgentStateIfStale writes agent_state=idle when the agent bead claims
// the agent is doing something and this run MEASURED that there is no session
// for it to be doing anything in.
//
// gt-hm0v gave --reconcile-cleanup a working repair for cleanup_status, and the
// trap reproduced one level up: that repair is gated on agent_state=idle, and
// agent_state=working is written only by starting work and cleared only by
// finishing it. A polecat that dies in between has no path out. Measured
// identically on beads/capable, gastown/chrome and gastown/deathclaw — the same
// refusal on all three, and the field named in it was one no verb could write
// (gt-xj5d).
//
// `gt polecat clear-state` is the obvious candidate and declines by design. Its
// scope is the PAUSED states (stuck, awaiting-gate, paused, escalated), which
// are deliberate and must be lifted deliberately. working is not deliberate; it
// is simply stale. And clearing it safely needs the git and merge-queue
// measurement that clear-state deliberately does not make — which is why the
// repair lives here, on the command that measures, and runs where the witness
// already reaches it: the SLOT_OPEN handler runs `gt polecat check-recovery
// --json --reconcile-cleanup` seconds after a polecat exits.
//
// A LIVE polecat can never reach the write. The state is only stale if there is
// no session, so this demands SessionPresence == SessionAbsent — the tri-state,
// where absent means the check RAN and found nothing. Unknown refuses: a
// surface that did not look, or looked and could not tell, must not have its
// silence read as proof the agent is gone (gt-9f67). Everything else that could
// make the claim true — a hook still held, a dirty tree, unpushed commits, an
// open MR, work outside the queue — is refused by the shared safety bar, which
// is the same one the cleanup repair passes.
//
// Ordered BEFORE the cleanup reconcile because it is what unblocks it. If it
// refuses, the cleanup reconcile refuses too ("not compounding it") and the
// reason is on the line above.
func reconcileAgentStateIfStale(status *RecoveryStatus, updater agentStateUpdater, agentBeadID string, p *polecat.Polecat, fields *beads.AgentFields, input polecat.WorkstateInput) {
	previous, out, ok := agentStateReconcilePlan(status, p, fields, input)
	if !ok {
		status.Reconcile = append(status.Reconcile, out)
		return
	}
	if updater == nil {
		status.Reconcile = append(status.Reconcile, agentStateReconcileFailed(status, previous, "updater unavailable"))
		return
	}
	idle := string(beads.AgentStateIdle)
	if err := updater.UpdateAgentDescriptionFields(agentBeadID, beads.AgentFieldUpdates{AgentState: &idle}); err != nil {
		status.Reconcile = append(status.Reconcile, agentStateReconcileFailed(status, previous, err.Error()))
		return
	}
	// Read back, on the same reasoning the other two reconciles do it: the write
	// path is a bd subprocess against Dolt and a nil error is not evidence the
	// row changed. The bead this fix came from asks specifically that the STORED
	// agent_state be confirmed changed rather than the command's rendered output
	// believed — so this command goes and reads the stored field itself.
	_, after, err := updater.GetAgentBead(agentBeadID)
	if err != nil {
		status.Reconcile = append(status.Reconcile, agentStateReconcileFailed(status, previous,
			fmt.Sprintf("write reported success but the bead could not be re-read: %v", err)))
		return
	}
	if after == nil || strings.TrimSpace(after.AgentState) != idle {
		stored := "<unparsable>"
		if after != nil {
			stored = orUnknownRecoveryField(after.AgentState)
		}
		status.Reconcile = append(status.Reconcile, agentStateReconcileFailed(status, previous,
			"write reported success but the stored agent_state still reads "+stored))
		return
	}

	// Keep the caller's in-memory view in step with the store it just changed.
	// The cleanup reconcile below reads this very field as its gate, so a stale
	// copy here would refuse the repair this write exists to enable.
	fields.AgentState = idle
	status.AgentState = idle
	status.Reconciled = true

	out.Action = reconcileActionWritten
	out.Detail = "rewritten to idle and confirmed by re-reading the agent bead; session_presence=absent, " +
		"so the claim of work in progress had no session to be true in"
	status.Reconcile = append(status.Reconcile, out)
	status.Diagnostics = append(status.Diagnostics,
		fmt.Sprintf("reconciled_agent_state=idle previous=%s session_presence=absent", previous))
	// Durable record of what the state was, the same one `gt polecat
	// clear-state` writes. The bead now says "idle" and carries no memory of the
	// claim it used to make, so without this the fact that a polecat sat at
	// working with no session — and when, and what lifted it — is gone.
	recordAgentStateCleared(status.Rig, status.Polecat, previous,
		"(gt polecat check-recovery --reconcile-cleanup: session_presence=absent)")
}

// agentStateReconcileFailed records an agent_state repair that was attempted and
// did not land, and fails the verdict closed.
func agentStateReconcileFailed(status *RecoveryStatus, previous, detail string) ReconcileOutcome {
	status.NeedsRecovery = true
	status.Verdict = "NEEDS_RECOVERY"
	status.Blockers = append(status.Blockers, fmt.Sprintf("agent_state_reconcile_failed: %s", detail))
	return ReconcileOutcome{
		Field:    "agent_state",
		Action:   reconcileActionFailed,
		Previous: orUnknownRecoveryField(previous),
		Detail:   detail,
	}
}

// agentStateReconcilePlan decides whether --reconcile-cleanup may rewrite
// agent_state, and names why whenever it may not. Every road returns an outcome
// a caller can read.
//
// The predicates are ordered so that the most specific true sentence is the one
// reported: "this is a deliberate pause, and clear-state is its verb" beats
// "this is not a stale activity claim", which beats "the session might still be
// there".
func agentStateReconcilePlan(status *RecoveryStatus, p *polecat.Polecat, fields *beads.AgentFields, input polecat.WorkstateInput) (string, ReconcileOutcome, bool) {
	out := ReconcileOutcome{Field: "agent_state", Action: reconcileActionRefused}
	if status == nil || p == nil || fields == nil {
		out.Previous = "<unread>"
		out.Detail = "no agent bead or no polecat record was loaded, so agent_state was never read"
		return "", out, false
	}

	previous := strings.TrimSpace(fields.AgentState)
	state := beads.AgentState(previous)
	out.Previous = orUnknownRecoveryField(previous)

	if state == beads.AgentStateIdle {
		out.Action = reconcileActionNoChange
		out.Detail = "already idle; nothing to reconcile"
		return previous, out, false
	}
	// The two out-of-scope roads below are no_change, NOT refused, and the
	// difference is the exit status. `refused` is what turns the exit non-zero,
	// and it must mean "this flag was asked to repair a stale field and a
	// predicate stopped it" — never "the field was never this flag's business".
	//
	// Measured before the distinction was drawn: almost every polecat in the town
	// rests at agent_state=done, and the witness runs this flag on every slot
	// that opens. Reporting `refused` there made a healthy run exit 1, which is
	// the reading that makes a MEANINGFUL non-zero unreadable — the same defect
	// as the silent exit 0, pointed the other way.

	// A pause is somebody's decision, not a stale field, and it has its own verb.
	// Rewriting it here would discard a pause that was set on purpose — the
	// failure `gt polecat clear-state` was built to avoid rather than cause
	// (gt-fbgq).
	if state.IsPaused() {
		out.Action = reconcileActionNoChange
		out.Detail = fmt.Sprintf("agent_state=%s is a deliberate pause, not stale completion state, so it is out of scope here; "+
			"lifting it is a decision, and its verb is `gt polecat clear-state %s`",
			previous, recoveryPolecatAddress(status))
		return previous, out, false
	}
	// IsActive, not a hand-written list of one: every state that CLAIMS the agent
	// is doing something (working, running, spawning, patrolling) is stale under
	// a measured-absent session by the same argument, and a list here would go
	// out of step with the one that defines the claim. done and nuked are not
	// claims of activity — they are legitimate resting states — and rewriting
	// them would be a lifecycle transition rather than a repair.
	if !state.IsActive() {
		out.Action = reconcileActionNoChange
		out.Detail = fmt.Sprintf("agent_state=%s does not claim work in progress, so there is nothing stale to repair; "+
			"this reconcile only rewrites a claim of activity that a measured-absent session contradicts",
			orUnknownRecoveryField(previous))
		return previous, out, false
	}
	// THE discriminator between a stale claim and a true one. Only SessionAbsent
	// decides: it means `tmux has-session` RAN and found no session. Unknown is
	// what a caller gets when it never looked or when tmux itself failed, and
	// reading either as "the agent is gone" would clear the state of a polecat
	// that is working right now.
	if input.SessionPresence != polecat.SessionAbsent {
		out.Detail = fmt.Sprintf("session_presence=%s — clearing a claim of work in progress requires a MEASURED absent session "+
			"(`tmux has-session` ran and found none); an unknown session is not evidence the agent is gone",
			orUnknownRecoveryField(string(input.SessionPresence)))
		return previous, out, false
	}
	if p.State != polecat.StateIdle {
		out.Detail = "polecat_state=" + string(p.State) + " (reconcile requires an idle polecat)"
		return previous, out, false
	}
	if reconcileOutcomesActionable(status.Reconcile) {
		out.Detail = "an earlier field reconcile on this run did not land; not compounding it"
		return previous, out, false
	}
	if blocker := staleFieldReconcileBlocker(status, input); blocker != "" {
		out.Detail = blocker
		return previous, out, false
	}

	out.Action = ""
	out.Detail = ""
	return previous, out, true
}

func reconcileCleanupStatusIfSafe(status *RecoveryStatus, updater cleanupStatusUpdater, agentBeadID string, p *polecat.Polecat, fields *beads.AgentFields, input polecat.WorkstateInput) {
	previous, out, ok := cleanupStatusReconcilePlan(status, p, fields, input)
	if !ok {
		status.Reconcile = append(status.Reconcile, out)
		return
	}
	if updater == nil {
		status.Reconcile = append(status.Reconcile, cleanupReconcileFailed(status, previous, "updater unavailable"))
		return
	}
	if err := updater.UpdateAgentCleanupStatus(agentBeadID, string(polecat.CleanupClean)); err != nil {
		status.Reconcile = append(status.Reconcile, cleanupReconcileFailed(status, previous, err.Error()))
		return
	}
	// Read back, on the same reasoning the push_failed reconcile does it: the
	// write path is a bd subprocess against Dolt and a nil error is not evidence
	// the row changed. The bead this fix came from says explicitly not to trust
	// this command's own rendered output over the stored field — so this command
	// now goes and reads the stored field itself.
	_, after, err := updater.GetAgentBead(agentBeadID)
	if err != nil {
		status.Reconcile = append(status.Reconcile, cleanupReconcileFailed(status, previous,
			fmt.Sprintf("write reported success but the bead could not be re-read: %v", err)))
		return
	}
	if after == nil || polecat.CleanupStatus(after.CleanupStatus) != polecat.CleanupClean {
		stored := polecat.CleanupStatus("")
		if after != nil {
			stored = polecat.CleanupStatus(after.CleanupStatus)
		}
		status.Reconcile = append(status.Reconcile, cleanupReconcileFailed(status, previous,
			"write reported success but the stored cleanup_status still reads "+cleanupStatusLabel(stored)))
		return
	}

	// Keep the caller's in-memory view in step with the store it just changed,
	// then re-derive the verdict from it. Reporting the pre-repair verdict after
	// a successful repair is how a caller ends up reading NEEDS_RECOVERY over a
	// polecat this very run made safe — and the witness's slot-open check reads
	// exactly that field.
	fields.CleanupStatus = string(polecat.CleanupClean)
	status.CleanupStatus = polecat.CleanupClean
	status.Reconciled = true
	repaired := input
	repaired.CleanupStatus = polecat.CleanupClean
	applyWorkstateDispositionToRecoveryStatus(status, polecat.DecideWorkstate(repaired))

	out.Action = reconcileActionWritten
	out.Detail = "rewritten to clean and confirmed by re-reading the agent bead"
	status.Reconcile = append(status.Reconcile, out)
	status.Diagnostics = append(status.Diagnostics,
		fmt.Sprintf("reconciled_cleanup_status=clean previous=%s", cleanupStatusLabel(previous)))
}

// cleanupReconcileFailed records a reconcile that was attempted and did not
// land, and fails the verdict closed.
func cleanupReconcileFailed(status *RecoveryStatus, previous polecat.CleanupStatus, detail string) ReconcileOutcome {
	status.NeedsRecovery = true
	status.Verdict = "NEEDS_RECOVERY"
	status.Blockers = append(status.Blockers, fmt.Sprintf("cleanup_reconcile_failed: %s", detail))
	return ReconcileOutcome{
		Field:    "cleanup_status",
		Action:   reconcileActionFailed,
		Previous: cleanupStatusLabel(previous),
		Detail:   detail,
	}
}

// cleanupStatusReconcilePlan decides whether --reconcile-cleanup may rewrite
// cleanup_status, and — the half that was missing — names why whenever it may
// not. Every road returns an outcome a caller can read.
//
// The verdict precondition is asked of the bead this flag would PRODUCE, not of
// the one it found, and that is the whole fix. Demanding SAFE_TO_NUKE of the
// bead as found closed the recovery loop into a circle: a stale or missing
// cleanup_status is itself a NEEDS_RECOVERY blocker, so on exactly the polecats
// this flag exists to repair the precondition could never hold, the write never
// ran, and the only verb left that changed anything was nuke — which destroys a
// worktree, a branch and an agent bead to clear a field that was never written
// (gt-hm0v, hq-f183o).
//
// Re-deciding with the field clean is strictly stronger evidence than the
// blocker list, because it runs the merge-queue tail that a NEEDS_RECOVERY
// return never reaches. SAFE_TO_NUKE out of it means every OTHER predicate this
// command measured — hook, git, active MR, merge queue, session — proved safe,
// which is precisely what the flag documents as its bar.
func cleanupStatusReconcilePlan(status *RecoveryStatus, p *polecat.Polecat, fields *beads.AgentFields, input polecat.WorkstateInput) (polecat.CleanupStatus, ReconcileOutcome, bool) {
	out := ReconcileOutcome{Field: "cleanup_status", Action: reconcileActionRefused}
	if status == nil || p == nil || fields == nil {
		out.Previous = cleanupStatusLabel("")
		out.Detail = "no agent bead or no polecat record was loaded, so nothing about this polecat was measured"
		return "", out, false
	}

	previous := polecat.CleanupStatus(fields.CleanupStatus)
	out.Previous = cleanupStatusLabel(previous)

	// A missing field is NOT excluded here. It used to be, and it was the exact
	// value every polecat this bug stranded carried: the one blocker standing
	// between them and the pool was `cleanup_status=<missing>`, and the flag
	// that exists to clear it skipped it by name (gt-hm0v).
	if previous == polecat.CleanupClean {
		out.Action = reconcileActionNoChange
		out.Detail = "already clean; nothing to reconcile"
		return previous, out, false
	}
	if reconcileOutcomesActionable(status.Reconcile) {
		out.Detail = "an earlier field reconcile on this run did not land; not compounding it"
		return previous, out, false
	}
	if p.State != polecat.StateIdle {
		out.Detail = "polecat_state=" + string(p.State) + " (reconcile requires an idle polecat)"
		return previous, out, false
	}
	if beads.AgentState(fields.AgentState) != beads.AgentStateIdle {
		out.Detail = "agent_state=" + orUnknownRecoveryField(fields.AgentState) + " (reconcile requires agent_state=idle)"
		return previous, out, false
	}

	if blocker := staleFieldReconcileBlocker(status, input); blocker != "" {
		out.Detail = blocker
		return previous, out, false
	}

	out.Action = ""
	out.Detail = ""
	return previous, out, true
}

// staleFieldReconcileBlocker is the safety bar every stale-field repair on this
// command shares: nothing is at risk once the stale fields this run would
// rewrite are treated as rewritten. It returns the reason when that does not
// hold, and "" when it does.
//
// It is one function rather than a copy per field because the two reconciles
// are a CHAIN — cleanup_status is gated on agent_state=idle, so agent_state is
// repaired first and only in order to let the cleanup repair run. Two copies of
// this bar could disagree, and a disagreement here means agent_state gets
// rewritten on a polecat the cleanup repair then refuses: a field changed for a
// repair that never happened.
//
// The verdict precondition is asked of the bead the repairs would PRODUCE, not
// of the one they found. That is gt-hm0v's whole lesson: a stale cleanup_status
// is itself a NEEDS_RECOVERY blocker, so demanding SAFE_TO_NUKE of the bead as
// found closed the loop into a circle on exactly the polecats the flag exists to
// repair.
//
// Re-deciding with the field clean is strictly stronger evidence than the
// blocker list, because it runs the merge-queue tail that a NEEDS_RECOVERY
// return never reaches. SAFE_TO_NUKE out of it means every OTHER predicate this
// command measured — hook, git, active MR, merge queue, session — proved safe.
func staleFieldReconcileBlocker(status *RecoveryStatus, input polecat.WorkstateInput) string {
	repaired := input
	repaired.CleanupStatus = polecat.CleanupClean
	if disposition := polecat.DecideWorkstate(repaired); disposition.Verdict != "SAFE_TO_NUKE" {
		return fmt.Sprintf("a clean cleanup_status would still leave verdict=%s — %s",
			disposition.Verdict, blockerSummary(disposition.Blockers))
	}
	// Not a whitelist of acceptable mq_status values. The merge-queue tail just
	// ran inside DecideWorkstate and came out SAFE_TO_NUKE, and that IS the
	// discharge — a second, narrower list of statuses beside it is only a way
	// for the two to disagree. The old one read status.MQStatus, which the
	// pre-repair NEEDS_RECOVERY return leaves EMPTY because it never reaches
	// that tail, so it refused every polecat that had a branch.
	//
	// What is worth asserting on its own is that anybody looked at all: a
	// surface too cheap to run the queue check returns the same zeros as a
	// branch with nothing left to submit (gt-49dp).
	if status.Branch != "" && !repaired.MQCheckRequired {
		return "branch " + status.Branch + " exists and no merge-queue check was run for it, " +
			"so whether it still holds unsubmitted work is unknown"
	}
	return ""
}

// pausedAgentState returns the agent bead's agent_state when it names a
// deliberate pause, and "" otherwise. It is the single reader every surface
// that classifies a polecat should use, so none of them can go blind to the
// field again the way check-recovery and the reuse gate both had (gt-fbgq).
func pausedAgentState(fields *beads.AgentFields) string {
	if fields == nil {
		return ""
	}
	state := beads.AgentState(strings.TrimSpace(fields.AgentState))
	if !state.IsPaused() {
		return ""
	}
	return string(state)
}

func agentSourceIssueHint(currentIssue string, fields *beads.AgentFields) string {
	if currentIssue != "" {
		return currentIssue
	}
	if fields == nil {
		return ""
	}
	if fields.LastSourceIssue != "" {
		return fields.LastSourceIssue
	}
	return fields.HookBead
}

func partialSpawnWithoutDurableHook(bd issueShower, fields *beads.AgentFields, assignee, currentIssue string) (bool, string) {
	if bd == nil || fields == nil || fields.AgentState != "spawning" || fields.HookBead == "" || currentIssue != "" {
		return false, ""
	}
	issue, err := bd.Show(fields.HookBead)
	if err != nil || issue == nil {
		return false, ""
	}
	// The hook is durable as long as the bead names this agent, whatever form
	// the writer chose for the address. Comparing raw strings here reported a
	// partial spawn for a perfectly hooked bead whenever the two sides had
	// picked different conventions (gt-gbv4).
	if issue.Assignee == assignee || beads.SameAgentAddress(issue.Assignee, assignee) {
		return false, ""
	}
	return true, fmt.Sprintf("partial_spawn_without_durable_hook agent_state=%s hook_bead=%s hook_status=%s hook_assignee=%q", fields.AgentState, fields.HookBead, issue.Status, issue.Assignee)
}

func recoveryGitStateBlocker(worktreePath string, gitState *GitState, gitErr error) string {
	if gitErr != nil {
		return fmt.Sprintf("git_state=unknown path=%s: %v", worktreePath, gitErr)
	}
	if gitState == nil || gitState.Clean {
		return ""
	}
	if gitState.UnpushedCommits > 0 {
		return fmt.Sprintf("git_state=has_unpushed unpushed_commits=%d", gitState.UnpushedCommits)
	}
	if gitState.StashCount > 0 {
		return fmt.Sprintf("git_state=has_stash stash_count=%d", gitState.StashCount)
	}
	return fmt.Sprintf("git_state=has_uncommitted uncommitted_files=%d", len(gitState.UncommittedFiles))
}

func recoveryActionsForBlockers(blockers []string, rigName, polecatName string) []string {
	var actions []string
	for _, blocker := range blockers {
		switch {
		case strings.HasPrefix(blocker, "git_state=has_stash"):
			actions = append(actions, "preserve branch-owned stash entries to auditable recovery refs before cleanup, then rerun check-recovery")
		case strings.HasPrefix(blocker, "has work on hook ("):
			// The escalation used to end here, at "escalate to Mayor", with no
			// operable next step: the Mayor's tool is `gt unsling`, and it
			// resolved its target through tmux, so it could not run for any
			// agent this verdict names (gt-dh3d). It resolves through the store
			// now, so the escalation can carry the command that clears the hook.
			hookBead := strings.TrimSuffix(strings.TrimPrefix(blocker, "has work on hook ("), ")")
			actions = append(actions, fmt.Sprintf("release the hook once the work is accounted for: gt unsling %s %s/%s (the Diagnostics line names the surfaces this hook was read from)",
				hookBead, rigName, polecatName))
		}
	}
	return actions
}

func activeMRBlocker(bd issueShower, mrID, sourceHint string, requireGitSafe, gitSafe bool) string {
	assessment := polecat.AssessActiveMR(bd, polecat.ActiveMRInput{
		ActiveMR:        mrID,
		SourceIssueHint: sourceHint,
		RequireGitSafe:  requireGitSafe,
		GitSafe:         gitSafe,
	})
	if assessment.Pending {
		return assessment.Reason
	}
	return ""
}

func hasSubmittableWorkForRecovery(worktreePath string, targetRefs []string, gitState *GitState, gitErr error) bool {
	g := git.NewGit(worktreePath)
	branch, _ := g.CurrentBranch()
	if status, err := g.BranchTargetStatus(branch, "origin", targetRefs); err == nil {
		return status.UnpreservedPatchCount > 0
	}
	if branch, err := g.CurrentBranch(); err == nil && branch != "" && !isRecoveryBaseBranch(branch) {
		if pushed, _, err := g.BranchPushedToRemote(branch, "origin"); err == nil && pushed {
			return true
		}
	}
	return gitErr != nil || (gitState != nil && gitState.UnpushedCommits > 0)
}

func isRecoveryBaseBranch(branch string) bool {
	return branch == "main" || branch == "master" || strings.HasPrefix(branch, "integration/")
}

func recoveryTargetRefs(bd *beads.Beads, issueID, activeMR, branch string, extraIssueIDs ...string) ([]string, bool) {
	var refs []string
	lookupFailed := false
	appendMRTarget := func(issue *beads.Issue) {
		if fields := beads.ParseMRFields(issue); fields != nil && fields.Target != "" {
			refs = append(refs, fields.Target)
		}
	}
	if bd != nil {
		if activeMR != "" {
			if issue, err := bd.Show(activeMR); err == nil {
				appendMRTarget(issue)
			} else if !errors.Is(err, beads.ErrNotFound) {
				lookupFailed = true
			}
		}
		if branch != "" {
			if issue, err := bd.FindMRForBranchAny(branch); err == nil {
				appendMRTarget(issue)
			} else if !errors.Is(err, beads.ErrNotFound) {
				lookupFailed = true
			}
		}
		for _, candidateIssueID := range append([]string{issueID}, extraIssueIDs...) {
			if candidateIssueID == "" {
				continue
			}
			if issue, err := bd.Show(candidateIssueID); err == nil {
				appendAttachmentTargets(&refs, bd, issue)
			} else {
				lookupFailed = true
			}
		}
	}
	return uniqueStrings(refs), lookupFailed
}

func appendAttachmentTargets(refs *[]string, bd *beads.Beads, issue *beads.Issue) {
	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil {
		return
	}
	appendBaseBranchVars(refs, attachment.FormulaVars)
	for _, value := range attachment.AttachedVars {
		appendBaseBranchVars(refs, value)
	}
	if attachment.ConvoyID != "" && bd != nil {
		if convoy, err := bd.Show(attachment.ConvoyID); err == nil {
			if fields := beads.ParseConvoyFields(convoy); fields != nil && fields.BaseBranch != "" {
				*refs = append(*refs, fields.BaseBranch)
			}
		}
	}
}

func appendBaseBranchVars(refs *[]string, vars string) {
	for _, line := range strings.Split(vars, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) != "base_branch" {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			*refs = append(*refs, value)
		}
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// isAssignedBeadTerminal reports whether the polecat's assigned bead (if any)
// is in a terminal status (closed/tombstone). Returns false on any lookup
// failure — callers must only use this to *skip* further escalation, never to
// escalate, so a false negative is safe.
func isAssignedBeadTerminal(bd *beads.Beads, issueID string) bool {
	if issueID == "" || bd == nil {
		return false
	}
	issue, err := bd.Show(issueID)
	if err != nil || issue == nil {
		return false
	}
	return beads.IssueStatus(issue.Status).IsTerminal()
}

// sourceCloseDischargesMQForRecovery reads the source bead's CLOSE REASON,
// which isAssignedBeadTerminal above deliberately does not. Both a merged bead
// and one closed as a duplicate are terminal, and only the second declares that
// the branch still in the sandbox is unwanted — the distinction that decides
// whether the slot is stranded forever (gt-xm6w).
func sourceCloseDischargesMQForRecovery(bd issueShower, issueID string) bool {
	if issueID == "" || bd == nil {
		return false
	}
	issue, err := bd.Show(issueID)
	if err != nil || issue == nil {
		return false
	}
	if !beads.IssueStatus(issue.Status).IsTerminal() {
		return false
	}
	return polecat.CloseReasonDischargesMergeQueue(issue.CloseReason)
}

// isMQNotRequiredSource reports whether the source bead intentionally bypasses
// the internal merge queue. The caller still gates this on SAFE_TO_NUKE so dirty
// or unpushed local work is never hidden by source metadata.
func isMQNotRequiredSource(bd issueShower, issueID string) bool {
	if issueID == "" || bd == nil {
		return false
	}
	issue, err := bd.Show(issueID)
	if err != nil || issue == nil {
		return false
	}
	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil {
		return false
	}
	if attachment.NoMerge || attachment.ReviewOnly {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(attachment.MergeStrategy), "local")
}

func runPolecatGC(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	fmt.Printf("Garbage collecting stale polecat branches in %s...\n\n", r.Name)

	if polecatGCDryRun {
		// Dry run - list branches that would be deleted
		repoGit := git.NewGit(r.Path)

		// List all polecat branches
		branches, err := repoGit.ListBranches("polecat/*")
		if err != nil {
			return fmt.Errorf("listing branches: %w", err)
		}

		if len(branches) == 0 {
			fmt.Println("No polecat branches found.")
			return nil
		}

		// Get current branches
		polecats, err := mgr.List()
		if err != nil {
			return fmt.Errorf("listing polecats: %w", err)
		}

		currentBranches := make(map[string]bool)
		for _, p := range polecats {
			currentBranches[p.Branch] = true
		}

		// Show what would be deleted
		toDelete := 0
		for _, branch := range branches {
			if !currentBranches[branch] {
				fmt.Printf("  Would delete: %s\n", style.Dim.Render(branch))
				toDelete++
			} else {
				fmt.Printf("  Keep (in use): %s\n", style.Success.Render(branch))
			}
		}

		fmt.Printf("\nWould delete %d branch(es), keep %d\n", toDelete, len(branches)-toDelete)
		return nil
	}

	// Actually clean up
	deleted, err := mgr.CleanupStaleBranches()
	if err != nil {
		return fmt.Errorf("cleanup failed: %w", err)
	}

	if deleted == 0 {
		fmt.Println("No stale branches to clean up.")
	} else {
		fmt.Printf("%s Deleted %d stale branch(es).\n", style.SuccessPrefix, deleted)
	}

	return nil
}

// splitLines splits a string into non-empty lines.
func splitLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func runPolecatNuke(cmd *cobra.Command, args []string) error {
	// Restart-first policy (gt-dsgp) — enforced before anything else, including
	// --dry-run, because a dry run that prints "Would nuke ..." is itself a
	// steering surface for an identity that may not nuke at all.
	if err := checkRestartFirstNukePolicy("gt polecat nuke", polecatNukeOverrideRestartFirst); err != nil {
		return err
	}

	targets, err := resolvePolecatTargets(args, polecatNukeAll)
	if err != nil {
		return err
	}

	if len(targets) == 0 {
		fmt.Println("No polecats to nuke.")
		return nil
	}

	// Safety checks: refuse to nuke polecats with active work unless --force is set
	if !polecatNukeForce && !polecatNukeDryRun {
		var blocked []*SafetyCheckResult
		for _, p := range targets {
			result := checkPolecatSafety(p)
			if result.Blocked {
				blocked = append(blocked, result)
			}
		}

		if len(blocked) > 0 {
			displaySafetyCheckBlocked(blocked)
			return fmt.Errorf("blocked: %d polecat(s) failed nuke safety checks: %s", len(blocked), formatSafetyCheckBlockers(blocked))
		}
	}

	// Nuke each polecat
	var nukeErrors []string
	nuked := 0
	batchPurge := !polecatNukeDryRun && len(targets) > 1
	purgeRigs := make(map[string]*rig.Rig)
	dryRunBlocked := 0

	for _, p := range targets {
		if polecatNukeDryRun {
			blocked := !polecatNukeForce && checkPolecatSafety(p).Blocked
			if blocked {
				fmt.Printf("Would refuse to nuke %s/%s without --force:\n", p.rigName, p.polecatName)
				dryRunBlocked++
			} else {
				fmt.Printf("Would nuke %s/%s:\n", p.rigName, p.polecatName)
			}
			fmt.Printf("  - Kill session: gt-%s-%s\n", p.rigName, p.polecatName)
			fmt.Printf("  - Delete worktree: %s/polecats/%s\n", p.r.Path, p.polecatName)
			fmt.Printf("  - Delete branch (if exists)\n")
			fmt.Printf("  - Reset agent bead: %s\n", polecatBeadIDForRig(p.r, p.rigName, p.polecatName))

			if displayDryRunSafetyCheck(p) && !blocked {
				dryRunBlocked++
			}
			fmt.Println()
			continue
		}

		if polecatNukeForce {
			fmt.Printf("%s Nuking %s/%s (--force)...\n", style.Warning.Render("⚠"), p.rigName, p.polecatName)
		} else {
			fmt.Printf("Nuking %s/%s...\n", p.rigName, p.polecatName)
		}

		if err := nukePolecatFullWithOptions(p.polecatName, p.rigName, p.mgr, p.r, nukePolecatOptions{Force: polecatNukeForce, PurgeClosedEphemerals: !batchPurge}); err != nil {
			nukeErrors = append(nukeErrors, fmt.Sprintf("%s/%s: %v", p.rigName, p.polecatName, err))
			continue
		}

		nuked++
		if batchPurge {
			purgeRigs[p.r.Path] = p.r
		}
	}
	if batchPurge && len(purgeRigs) > 0 {
		for _, r := range purgeRigs {
			purgeClosedEphemeralBeads(beads.New(r.Path))
		}
	}

	// Report results
	if polecatNukeDryRun {
		if dryRunBlocked > 0 {
			fmt.Printf("\n%s %s\n", style.Warning.Render("⚠"), dryRunNukeSummary(len(targets), dryRunBlocked))
		} else {
			fmt.Printf("\n%s %s\n", style.Info.Render("ℹ"), dryRunNukeSummary(len(targets), dryRunBlocked))
		}
		return nil
	}

	if len(nukeErrors) > 0 {
		fmt.Printf("\n%s Some nukes failed:\n", style.Warning.Render("Warning:"))
		for _, e := range nukeErrors {
			fmt.Printf("  - %s\n", e)
		}
	}

	if nuked > 0 {
		fmt.Printf("\n%s Nuked %d polecat(s).\n", style.SuccessPrefix, nuked)
	}

	// Final cleanup: Kill any orphaned Claude processes that escaped the session termination.
	// This catches processes that called setsid() or were reparented during session shutdown.
	if !polecatNukeDryRun {
		cleanupOrphanedProcesses()
	}

	if len(nukeErrors) > 0 {
		return fmt.Errorf("%d nuke(s) failed", len(nukeErrors))
	}

	return nil
}

func dryRunNukeSummary(total, blocked int) string {
	if blocked > 0 {
		return fmt.Sprintf("Would refuse to nuke %d of %d polecat(s) without --force.", blocked, total)
	}
	return fmt.Sprintf("Would nuke %d polecat(s).", total)
}

// nukePolecatFull performs the complete cleanup sequence for a single polecat:
// 1. Kill tmux session
// 2. Delete worktree (via RemoveWithOptions with nuclear=true, which also resets and closes the agent bead)
// 3. Delete git branch
// This is the canonical cleanup path used by both `polecat nuke` and `polecat stale --cleanup`.
func nukePolecatFull(polecatName, rigName string, mgr *polecat.Manager, r *rig.Rig) error {
	return nukePolecatFullWithOptions(polecatName, rigName, mgr, r, nukePolecatOptions{PurgeClosedEphemerals: true})
}

type nukePolecatOptions struct {
	Force                 bool
	PurgeClosedEphemerals bool
}

func nukePolecatFullWithOptions(polecatName, rigName string, mgr *polecat.Manager, r *rig.Rig, opts nukePolecatOptions) error {
	if err := checkNukeActiveMRSafety(mgr, polecatName, rigName, opts.Force); err != nil {
		return err
	}

	t := tmux.NewTmux()

	// Step 1: Kill tmux session unconditionally to prevent ghost sessions
	// when IsRunning fails to detect the session.
	sessMgr := polecat.NewSessionManager(t, r)
	if err := sessMgr.Stop(polecatName, true); err != nil {
		if !errors.Is(err, polecat.ErrSessionNotFound) {
			fmt.Printf("  %s session kill failed: %v\n", style.Warning.Render("⚠"), err)
		}
	} else {
		fmt.Printf("  %s killed session\n", style.Success.Render("✓"))
	}

	// Step 2: Get polecat info before deletion (for branch name + hooked work bead)
	polecatInfo, getErr := mgr.Get(polecatName)
	var branchToDelete string
	if getErr == nil && polecatInfo != nil {
		branchToDelete = polecatInfo.Branch
	}

	// Step 2.5: Burn any molecule attached to the polecat's hooked work bead.
	// Without this, nuked polecats leave orphan molecule refs that block re-sling.
	// The stale attached_molecule in the work bead's description causes sling to
	// fail with "bead already has N attached molecule(s)" on re-dispatch (gt-npzy).
	if getErr == nil && polecatInfo != nil && polecatInfo.Issue != "" {
		nukeCleanupMolecules(polecatInfo.Issue, r)
	}

	// Step 2.75: Best-effort push before nuke (gt-4vr guardrail).
	// Try to preserve any unpushed commits on the branch. Push failures are
	// non-fatal because this cleanup path already passed its safety gates.
	if branchToDelete != "" {
		var pushGit *git.Git
		// Try worktree first (may still exist), then bare repo fallback.
		// Use ClonePath from the polecat record — the worktree lives at
		// <rig>/polecats/<name>/<rigName>/, not <rig>/polecats/<name>/.
		if polecatInfo != nil && polecatInfo.ClonePath != "" {
			if _, statErr := os.Stat(polecatInfo.ClonePath); statErr == nil {
				pushGit = git.NewGit(polecatInfo.ClonePath)
			}
		}
		if pushGit == nil {
			bareRepoPath := filepath.Join(r.Path, ".repo.git")
			if info, statErr := os.Stat(bareRepoPath); statErr == nil && info.IsDir() {
				pushGit = git.NewGitWithDir(bareRepoPath, "")
			}
		}
		if pushGit != nil {
			refspec := branchToDelete + ":" + branchToDelete
			if err := pushGit.Push("origin", refspec, false); err != nil {
				fmt.Printf("  %s best-effort push failed (proceeding): %v\n", style.Dim.Render("○"), err)
			} else {
				fmt.Printf("  %s pushed branch %s before nuke\n", style.Success.Render("✓"), branchToDelete)
			}
		}
	}

	// Step 3: Delete worktree (nuclear=true to bypass safety checks for stale polecats)
	if err := mgr.RemoveWithOptions(polecatName, opts.Force, true, false); err != nil {
		if errors.Is(err, polecat.ErrPolecatNotFound) {
			fmt.Printf("  %s worktree already gone\n", style.Dim.Render("○"))
			resetPolecatAgentBeadForReuse(r, rigName, polecatName)
		} else {
			return fmt.Errorf("worktree removal failed: %w", err)
		}
	} else {
		fmt.Printf("  %s deleted worktree\n", style.Success.Render("✓"))
	}

	// Step 4: Delete local branch (if we know it)
	// Local branch can always be deleted (worktree is already gone).
	// Remote branch is never deleted during nuke — the refinery owns
	// remote branch cleanup after successful merge (gt mq post-merge).
	// This prevents the race where nuke deletes the branch before the
	// refinery has a chance to merge it. (gt-v5ku)
	if branchToDelete != "" {
		repoGit := getRepoGitForRig(r.Path)
		if err := repoGit.DeleteBranch(branchToDelete, true); err != nil {
			fmt.Printf("  %s branch delete: %v\n", style.Dim.Render("○"), err)
		} else {
			fmt.Printf("  %s deleted local branch %s\n", style.Success.Render("✓"), branchToDelete)
		}
		fmt.Printf("  %s remote branch preserved for refinery merge\n", style.Dim.Render("○"))
	}

	// Step 5: Purge closed ephemeral beads (wisps) accumulated during sessions.
	// Without this, closed wisps from mol-polecat-work steps, mol-witness-patrol
	// cycles, etc. accumulate across sessions and pollute bd ready/list (hq-6161m).
	if opts.PurgeClosedEphemerals {
		purgeClosedEphemeralBeads(beads.New(r.Path))
	}

	return nil
}

type activeMRRemovalChecker interface {
	ActiveMRRemovalBlocker(name string) (activeMR, blocker string)
}

func checkNukeActiveMRSafety(checker activeMRRemovalChecker, polecatName, rigName string, force bool) error {
	if force || checker == nil {
		return nil
	}
	if activeMR, blocker := checker.ActiveMRRemovalBlocker(polecatName); blocker != "" {
		return fmt.Errorf("cannot nuke %s/%s: MR %s is still pending in merge queue (%s)\nRefinery will process the MR and clean up after merge\nUse --force to override (risks data loss)", rigName, polecatName, activeMR, blocker)
	}
	return nil
}

func resetPolecatAgentBeadForReuse(r *rig.Rig, rigName, polecatName string) {
	agentBeadID := polecatBeadIDForRig(r, rigName, polecatName)
	bd := beads.New(r.Path).ForAgentBead()
	if err := bd.ResetAgentBeadForReuse(agentBeadID, "nuked"); err != nil {
		fmt.Printf("  %s agent bead not found or already cleaned\n", style.Dim.Render("○"))
		return
	}
	fmt.Printf("  %s reset agent bead %s\n", style.Success.Render("✓"), agentBeadID)

	// The worktree was already gone, so Manager.RemoveWithOptions bailed with
	// ErrPolecatNotFound and never retired the bead. Close it here or this
	// polecat stays a permanent "dead" entry in gt feed's problems pane —
	// exactly the tombstone this path exists to clean up (gt-qvx7).
	if err := bd.CloseAgentBead(agentBeadID, "polecat nuked"); err != nil {
		fmt.Printf("  %s agent bead close failed (proceeding): %v\n", style.Dim.Render("○"), err)
	} else {
		fmt.Printf("  %s closed agent bead %s\n", style.Success.Render("✓"), agentBeadID)
	}
}

// nukeCleanupMolecules burns any molecule attached to a work bead during polecat nuke.
// This prevents stale attached_molecule references from blocking re-dispatch (gt-npzy).
// Best-effort: failures are logged but don't abort the nuke.
func nukeCleanupMolecules(workBeadID string, r *rig.Rig) {
	// Use mayor/rig as workDir so ResolveBeadsDir finds the Dolt-backed
	// .beads/ directory, not the gitignored rig-root .beads/. Without this,
	// detach/close operations route to the wrong database and the stale
	// molecule attachment persists on the work bead. (gt--1up)
	bd := beads.New(filepath.Join(r.Path, "mayor", "rig"))

	// Fetch the work bead to check for attached molecules
	issue, err := bd.Show(workBeadID)
	if err != nil {
		fmt.Printf("  %s molecule cleanup: could not fetch work bead %s: %v\n",
			style.Dim.Render("○"), workBeadID, err)
		return
	}

	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil || attachment.AttachedMolecule == "" {
		return // No molecule attached — nothing to clean up
	}

	moleculeID := attachment.AttachedMolecule

	// Force-close descendant steps before detaching (prevents orphaned step beads).
	// Uses force variant since nuke is destructive — must succeed even for beads in
	// invalid states. Best-effort — log but proceed in nuke path.
	if _, err := forceCloseDescendants(bd, moleculeID); err != nil {
		style.PrintWarning("nuke: could not close descendants of %s: %v", moleculeID, err)
	}

	// Detach the molecule with audit trail
	if _, detachErr := bd.DetachMoleculeWithAudit(workBeadID, beads.DetachOptions{
		Operation: "burn",
		Reason:    "polecat nuked: cleaning stale molecule",
	}); detachErr != nil {
		fmt.Printf("  %s molecule detach failed for %s: %v\n",
			style.Warning.Render("⚠"), moleculeID, detachErr)
		return
	}

	// Remove dependency bonds so stale molecule discovery does not block re-dispatch.
	removeMoleculeBonds(bd, workBeadID, moleculeID)

	// Force-close the orphaned wisp root so it doesn't linger
	if closeErr := bd.ForceCloseWithReason("burned: polecat nuked", moleculeID); closeErr != nil {
		fmt.Printf("  %s molecule root close failed for %s: %v\n",
			style.Warning.Render("⚠"), moleculeID, closeErr)
	} else {
		fmt.Printf("  %s burned stale molecule %s from work bead %s\n",
			style.Success.Render("✓"), moleculeID, workBeadID)
	}
}

// cleanupOrphanedProcesses kills Claude processes that survived session termination.
// Uses aggressive zombie detection via tmux session verification.
func cleanupOrphanedProcesses() {
	results, err := util.CleanupZombieClaudeProcesses()
	if err != nil {
		// Non-fatal: log and continue
		fmt.Printf("  %s orphan cleanup check failed: %v\n", style.Dim.Render("○"), err)
		return
	}

	if len(results) == 0 {
		return
	}

	// Report what was cleaned up
	var killed, escalated int
	for _, r := range results {
		switch r.Signal {
		case "SIGTERM", "SIGKILL":
			killed++
		case "UNKILLABLE":
			escalated++
		}
	}

	if killed > 0 {
		fmt.Printf("  %s cleaned up %d orphaned process(es)\n", style.Success.Render("✓"), killed)
	}
	if escalated > 0 {
		fmt.Printf("  %s %d process(es) survived SIGKILL (unkillable)\n", style.Warning.Render("⚠"), escalated)
	}
}

func runPolecatStale(cmd *cobra.Command, args []string) error {
	rigName := args[0]
	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	fmt.Printf("Detecting stale polecats in %s (threshold: %d commits behind main)...\n\n", r.Name, polecatStaleThreshold)

	staleInfos, err := mgr.DetectStalePolecats(polecatStaleThreshold)
	if err != nil {
		return fmt.Errorf("detecting stale polecats: %w", err)
	}

	if len(staleInfos) == 0 {
		fmt.Println("No polecats found.")
		return nil
	}

	// JSON output
	if polecatStaleJSON {
		return json.NewEncoder(os.Stdout).Encode(staleInfos)
	}

	// Summary counts
	var staleCount, safeCount int
	for _, info := range staleInfos {
		if info.IsStale {
			staleCount++
		} else {
			safeCount++
		}
	}

	// Display results
	for _, info := range staleInfos {
		statusIcon := style.Success.Render("●")
		statusText := "active"
		if info.IsStale {
			statusIcon = style.Warning.Render("○")
			statusText = "stale"
		}

		fmt.Printf("%s %s (%s)\n", statusIcon, style.Bold.Render(info.Name), statusText)

		// Session status
		if info.HasActiveSession {
			fmt.Printf("    Session: %s\n", style.Success.Render("running"))
		} else {
			fmt.Printf("    Session: %s\n", style.Dim.Render("stopped"))
		}

		// Commits behind
		if info.CommitsBehind > 0 {
			behindStyle := style.Dim
			if info.CommitsBehind >= polecatStaleThreshold {
				behindStyle = style.Warning
			}
			fmt.Printf("    Behind main: %s\n", behindStyle.Render(fmt.Sprintf("%d commits", info.CommitsBehind)))
		}

		// Agent state
		if info.AgentState != "" {
			fmt.Printf("    Agent state: %s\n", info.AgentState)
		} else {
			fmt.Printf("    Agent state: %s\n", style.Dim.Render("no bead"))
		}

		// Uncommitted work
		if info.HasUncommittedWork {
			fmt.Printf("    Uncommitted: %s\n", style.Error.Render("yes"))
		}

		// Reason
		fmt.Printf("    Reason: %s\n", info.Reason)
		fmt.Println()
	}

	// Summary
	fmt.Printf("Summary: %d stale, %d active\n", staleCount, safeCount)

	// Cleanup if requested
	if polecatStaleCleanup && staleCount > 0 {
		// --cleanup nukes. Same policy gate as `gt polecat nuke` (gt-y20).
		if err := checkRestartFirstNukePolicy("gt polecat stale --cleanup", polecatStaleOverrideRestartFirst); err != nil {
			return err
		}
		fmt.Println()
		if polecatStaleDryRun {
			fmt.Printf("Would clean up %d stale polecat(s):\n", staleCount)
			for _, info := range staleInfos {
				if info.IsStale {
					fmt.Printf("  - %s: %s\n", info.Name, info.Reason)
				}
			}
		} else {
			fmt.Printf("Cleaning up %d stale polecat(s)...\n", staleCount)
			nuked := 0
			batchPurge := staleCount > 1
			for _, info := range staleInfos {
				if !info.IsStale {
					continue
				}
				fmt.Printf("Nuking %s...\n", info.Name)
				if err := nukePolecatFullWithOptions(info.Name, rigName, mgr, r, nukePolecatOptions{PurgeClosedEphemerals: !batchPurge}); err != nil {
					fmt.Printf("  %s (%v)\n", style.Error.Render("failed"), err)
				} else {
					nuked++
				}
			}
			if batchPurge && nuked > 0 {
				purgeClosedEphemeralBeads(beads.New(r.Path))
			}
			fmt.Printf("\n%s Nuked %d stale polecat(s).\n", style.SuccessPrefix, nuked)

			// Clean up any orphaned processes that survived session termination
			cleanupOrphanedProcesses()
		}
	}

	return nil
}

func runPolecatPrune(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	_, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Use the mayor/rig clone (or bare repo) for branch operations
	var repoGit *git.Git
	bareRepoPath := filepath.Join(r.Path, ".repo.git")
	if info, statErr := os.Stat(bareRepoPath); statErr == nil && info.IsDir() {
		repoGit = git.NewGitWithDir(bareRepoPath, "")
	} else {
		repoGit = git.NewGit(filepath.Join(r.Path, "mayor", "rig"))
	}

	fmt.Printf("Pruning stale polecat branches in %s...\n", r.Name)

	// First, prune stale remote-tracking refs so we detect deleted remote branches
	if err := repoGit.FetchPrune("origin"); err != nil {
		if polecatPruneRemote {
			return fmt.Errorf("refreshing origin before remote prune: %w", err)
		}
		fmt.Printf("  %s fetch --prune: %v (continuing anyway)\n", style.Warning.Render("⚠"), err)
	}

	// Prune local branches that are merged or have no remote
	report, err := repoGit.PruneStaleBranchesReport("polecat/*", polecatPruneDryRun)
	if err != nil {
		return fmt.Errorf("pruning local branches: %w", err)
	}

	if report.Candidates() == 0 {
		fmt.Println("No stale local polecat branches found.")
	} else {
		verb := "Pruned"
		if polecatPruneDryRun {
			verb = "Would prune"
		}
		for _, b := range report.Pruned {
			fmt.Printf("  %s %s (%s)\n", style.Success.Render("✓"), b.Name, b.Reason)
		}
		for _, b := range report.Skipped {
			fmt.Printf("  %s %s (%s): %s\n", style.Warning.Render("⚠"), b.Name, b.Reason, b.Detail)
		}
		fmt.Printf("\n%s %d local branch(es).\n", verb, len(report.Pruned))
		if len(report.Skipped) > 0 {
			fmt.Printf("Kept %d stale local branch(es) that could not be deleted.\n", len(report.Skipped))
		}
	}

	// Optionally prune remote polecat branches
	if polecatPruneRemote {
		fmt.Println()
		fmt.Println("Pruning remote polecat branches...")

		remotePruned, remoteErr := pruneRemotePolecatBranches(repoGit, polecatPruneDryRun)
		if remoteErr != nil {
			return remoteErr
		}

		if remotePruned == 0 {
			fmt.Println("No stale remote polecat branches found.")
		} else {
			verb := "Pruned"
			if polecatPruneDryRun {
				verb = "Would prune"
			}
			fmt.Printf("\n%s %d remote branch(es).\n", verb, remotePruned)
		}
	}

	return nil
}

func pruneRemotePolecatBranches(repoGit *git.Git, dryRun bool) (int, error) {
	defaultBranch := repoGit.RemoteDefaultBranch()
	target := repoGit.CleanDefaultBranchBaseRef("origin", defaultBranch)
	if targetRemote := git.RemoteForRef(target); targetRemote != "" && targetRemote != "origin" {
		if err := repoGit.FetchPrune(targetRemote); err != nil {
			return 0, fmt.Errorf("refreshing %s before remote prune: %w", targetRemote, err)
		}
	}
	remoteRefs, lsErr := repoGit.ListPushRemoteRefsWithHashes("origin", "refs/heads/polecat/")
	if lsErr != nil {
		return 0, fmt.Errorf("listing remote refs: %w", lsErr)
	}

	remotePruned := 0
	for _, ref := range remoteRefs {
		if !strings.HasPrefix(ref.Name, "refs/heads/") {
			continue
		}
		branch := strings.TrimPrefix(ref.Name, "refs/heads/")
		status, statusErr := repoGit.PushRemoteRefTargetStatus("origin", ref, target)
		if statusErr != nil || !status.Preserved {
			continue
		}

		if dryRun {
			fmt.Printf("  Would delete remote: %s\n", style.Dim.Render(branch))
			remotePruned++
			continue
		}
		if delErr := repoGit.DeleteRemoteBranchIfAt("origin", branch, ref.Hash); delErr != nil {
			fmt.Printf("  %s remote %s: %v\n", style.Warning.Render("⚠"), branch, delErr)
			continue
		}
		fmt.Printf("  %s deleted remote %s\n", style.Success.Render("✓"), branch)
		remotePruned++
	}

	return remotePruned, nil
}

// runPolecatPoolInit creates a persistent polecat pool for a rig.
// Creates N polecats with identities and worktrees in IDLE state.
// Existing polecats are preserved — only new ones are created.
func runPolecatPoolInit(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Determine pool size: flag > rig config > default
	poolSize := 4 // default
	rigCfg, cfgErr := rig.LoadRigConfig(r.Path)
	if cfgErr == nil && rigCfg.PolecatPoolSize > 0 {
		poolSize = rigCfg.PolecatPoolSize
	}
	if polecatPoolInitSize > 0 {
		poolSize = polecatPoolInitSize
	}

	// Determine names: rig config > name pool theme
	var fixedNames []string
	if cfgErr == nil && len(rigCfg.PolecatNames) > 0 {
		fixedNames = rigCfg.PolecatNames
	}

	// List existing polecats to avoid recreating them
	existing, err := mgr.List()
	if err != nil {
		return fmt.Errorf("listing existing polecats: %w", err)
	}
	existingNames := make(map[string]bool)
	for _, p := range existing {
		existingNames[p.Name] = true
	}

	fmt.Printf("Initializing persistent polecat pool for %s (target size: %d)\n", rigName, poolSize)
	if len(existing) > 0 {
		fmt.Printf("  Existing polecats: %d\n", len(existing))
	}

	// Build the list of names to create
	var namesToCreate []string
	if len(fixedNames) > 0 {
		// Use configured names, skip ones that already exist
		for _, name := range fixedNames {
			if len(namesToCreate)+len(existingNames) >= poolSize {
				break
			}
			if !existingNames[name] {
				namesToCreate = append(namesToCreate, name)
			}
		}
	} else {
		// Use name pool allocation for new names
		namePool := mgr.GetNamePool()
		namePool.Reconcile(existingNamesList(existing))
		for len(namesToCreate)+len(existingNames) < poolSize {
			name, allocErr := namePool.Allocate()
			if allocErr != nil {
				return fmt.Errorf("allocating polecat name: %w", allocErr)
			}
			if !existingNames[name] {
				namesToCreate = append(namesToCreate, name)
			}
		}
	}

	if len(namesToCreate) == 0 {
		fmt.Printf("\n%s Pool already at target size (%d polecats).\n", style.Bold.Render("✓"), len(existing))
		return nil
	}

	if polecatPoolInitDryRun {
		fmt.Printf("\nWould create %d polecat(s):\n", len(namesToCreate))
		for _, name := range namesToCreate {
			fmt.Printf("  %s %s\n", style.Dim.Render("→"), name)
		}
		return nil
	}

	// Create each polecat
	fmt.Printf("\nCreating %d polecat(s)...\n", len(namesToCreate))
	created := 0
	for _, name := range namesToCreate {
		fmt.Printf("  %s Creating %s...", style.Dim.Render("→"), name)
		p, addErr := mgr.Add(name)
		if addErr != nil {
			fmt.Printf(" %s %v\n", style.Warning.Render("FAILED"), addErr)
			continue
		}
		// Set agent state to idle (polecat was created without work).
		// Use the retry variant: createAgentBeadWithRetry above leaves a brief
		// Dolt MVCC visibility window where the just-committed bead isn't yet
		// readable by the next UpdateAgentState query, surfacing as "issue not
		// found". Retries with backoff close that window — same pattern as
		// SetAgentStateWithRetry's other call site in polecat_spawn.go.
		if stateErr := mgr.SetAgentStateWithRetry(name, "idle"); stateErr != nil {
			fmt.Printf(" %s (created but couldn't set idle state: %v)\n", style.Warning.Render("⚠"), stateErr)
		} else {
			fmt.Printf(" %s (%s)\n", style.Success.Render("✓"), style.Dim.Render(p.ClonePath))
		}
		created++
	}

	fmt.Printf("\n%s Pool initialized: %d created, %d total (target: %d)\n",
		style.Bold.Render("✓"), created, created+len(existing), poolSize)

	// Sync hooks so all polecat settings.json files reflect current defaults.
	// Pool-init may run long after rig-add, when gt defaults have changed.
	townRoot, twErr := workspace.FindFromCwdOrError()
	if twErr == nil {
		ensureHooksBase()
		if err := syncRigHooks(townRoot, rigName); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to sync hooks after pool-init: %v\n", err)
		}
	}

	return nil
}

// existingNamesList extracts polecat names from a slice of Polecat pointers.
func existingNamesList(polecats []*polecat.Polecat) []string {
	names := make([]string, len(polecats))
	for i, p := range polecats {
		names[i] = p.Name
	}
	return names
}
