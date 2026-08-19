package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/style"
)

// MQ next command flags
var (
	mqNextStrategy string // "priority" (default) or "fifo"
	mqNextJSON     bool
	mqNextQuiet    bool
	mqNextVerify   bool
)

var mqNextCmd = &cobra.Command{
	Use:   "next <rig>",
	Short: "Show the highest-priority merge request",
	Long: `Show the next merge request to process based on priority score.

The priority scoring function considers:
  - Convoy age: Older convoys get higher priority (starvation prevention)
  - Issue priority: P0 > P1 > P2 > P3 > P4
  - Retry count: MRs that fail repeatedly get deprioritized
  - MR age: FIFO tiebreaker for same priority/convoy

Use --strategy=fifo for first-in-first-out ordering instead.

Candidates are checked in git before being picked: an MR whose branch carries no
commits over its target is a no-op merge that would still close the source issue,
so it is skipped (and named in the output) rather than handed to the refinery.
Pass --verify=false to select on bead state alone.

Examples:
  gt mq next gastown                    # Show highest-priority MR
  gt mq next gastown --strategy=fifo    # Show oldest MR instead
  gt mq next gastown --quiet            # Just print the MR ID
  gt mq next gastown --json             # Output as JSON
  gt mq next gastown --verify=false     # Skip the git content check`,
	Args: cobra.ExactArgs(1),
	RunE: runMQNext,
}

func init() {
	mqNextCmd.Flags().StringVar(&mqNextStrategy, "strategy", "priority", "Ordering strategy: 'priority' or 'fifo'")
	mqNextCmd.Flags().BoolVar(&mqNextJSON, "json", false, "Output as JSON")
	mqNextCmd.Flags().BoolVarP(&mqNextQuiet, "quiet", "q", false, "Just print the MR ID")
	mqNextCmd.Flags().BoolVar(&mqNextVerify, "verify", true, "Check each candidate's branch in git and skip ones that carry no commits over their target")

	mqCmd.AddCommand(mqNextCmd)
}

// mqNextCandidate is a selectable MR together with what git says about its branch.
type mqNextCandidate struct {
	issue  *beads.Issue
	fields *beads.MRFields
	state  mrBranchState
	ahead  int
}

// selectNextMR picks the MR to process next, and reports the ones excluded
// because their branch carries no work.
//
// Bead state alone cannot answer "is there anything to merge": an MR whose
// branch points at its own target is open, unblocked, and fully populated, so
// every bead-level check passes. Handing it to the refinery produces a
// trivially-successful rehearsal, a green test run over an unchanged tree, a
// no-op merge commit, and a closed source issue whose work was never done
// (gt-9b79). The content check that `gt mq list --verify` already performs on
// the display path (gt-d5u) therefore has to run here too, where the choice is
// actually made.
//
// Only mrBranchStateEmpty excludes a candidate. It is the one state that
// positively proves the merge is a no-op — the branch resolved, the target
// resolved, and the commit count came back zero. MISSING and ERR are also
// unmergeable-looking, but both are reachable from a merely stale or
// unreachable worktree, and excluding on them would let a bad local checkout
// silently drain the queue. Those states are reported, not acted on.
//
// Returns the pick (nil when nothing is selectable), how many other selectable
// MRs remain behind it, and the skipped candidates.
func selectNextMR(issues []*beads.Issue, verify bool, client branchVerifier, strategy string, now time.Time) (pick *mqNextCandidate, others int, skipped []mqNextCandidate) {
	var ready []mqNextCandidate

	for _, issue := range issues {
		if !isMergeRequestReadyForSelection(issue) {
			continue
		}
		fields := beads.ParseMRFields(issue)
		state, ahead := verifyBranch(verify, client, fields)
		candidate := mqNextCandidate{issue: issue, fields: fields, state: state, ahead: ahead}
		if state == mrBranchStateEmpty {
			skipped = append(skipped, candidate)
			continue
		}
		ready = append(ready, candidate)
	}

	if len(ready) == 0 {
		return nil, 0, skipped
	}

	if strategy == "fifo" {
		// FIFO: oldest first by creation time
		sort.SliceStable(ready, func(i, j int) bool {
			ti, _ := time.Parse(time.RFC3339, ready[i].issue.CreatedAt)
			tj, _ := time.Parse(time.RFC3339, ready[j].issue.CreatedAt)
			return ti.Before(tj)
		})
	} else {
		// Priority: highest score first
		scores := make(map[string]float64, len(ready))
		for _, c := range ready {
			scores[c.issue.ID] = calculateMRScore(c.issue, c.fields, now)
		}
		sort.SliceStable(ready, func(i, j int) bool {
			return scores[ready[i].issue.ID] > scores[ready[j].issue.ID]
		})
	}

	return &ready[0], len(ready) - 1, skipped
}

func runMQNext(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	_, r, _, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// Create beads wrapper for the rig
	b := beads.New(r.BeadsPath())

	// Query for open merge-requests (ready to process).
	// Use ListMergeRequests to query both issues and wisps tables,
	// since MRs are created as ephemeral (wisps) by gt mq submit (GH#2446).
	opts := beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "open",
		Priority: -1, // No priority filter
		Rig:      rigName,
	}

	issues, err := b.ListMergeRequests(opts)
	if err != nil {
		return fmt.Errorf("querying merge queue: %w", err)
	}

	// Verify candidates against the refinery's own rig worktree — the same
	// checkout `gt mq list --verify` uses, and the one the merge will run in.
	// Left nil (rather than a typed nil) when verification is off so
	// verifyBranch's nil-client guard actually fires.
	var verifier branchVerifier
	if mqNextVerify {
		verifier = git.NewGit(filepath.Join(r.Path, "refinery", "rig"))
	}

	next, others, skipped := selectNextMR(issues, mqNextVerify, verifier, mqNextStrategy, time.Now())

	if next == nil {
		if mqNextJSON {
			// Valid JSON even with nothing to hand back: a consumer that reads
			// `.id` gets null, and the skip list explains an empty queue that
			// is empty only because every candidate was a no-op.
			return outputJSON(struct {
				SkippedEmpty []string `json:"skipped_empty,omitempty"`
			}{skippedEmptyIDs(skipped)})
		}
		if mqNextQuiet {
			reportSkippedEmptyMRs(os.Stderr, rigName, skipped)
			return nil // Silent exit
		}
		fmt.Printf("%s No ready merge requests in queue\n", style.Dim.Render("ℹ"))
		reportSkippedEmptyMRs(os.Stdout, rigName, skipped)
		return nil
	}

	now := time.Now()
	fields := next.fields

	// Output based on format flags
	if mqNextQuiet {
		// Skips go to stderr so a `$(gt mq next --quiet)` capture stays clean.
		reportSkippedEmptyMRs(os.Stderr, rigName, skipped)
		fmt.Println(next.issue.ID)
		return nil
	}

	if mqNextJSON {
		if !mqNextVerify {
			return outputJSON(next.issue)
		}
		// Additive: the issue's own fields stay at the top level, so existing
		// consumers keep working while a formula can now read git_state and
		// reject rather than merge.
		type verifiedNextMR struct {
			*beads.Issue
			BranchExists *bool    `json:"branch_exists,omitempty"`
			BranchEmpty  *bool    `json:"branch_empty,omitempty"`
			CommitsAhead *int     `json:"commits_ahead,omitempty"`
			GitState     string   `json:"git_state,omitempty"`
			VerifyError  bool     `json:"verify_error,omitempty"`
			SkippedEmpty []string `json:"skipped_empty,omitempty"`
		}
		out := verifiedNextMR{Issue: next.issue, SkippedEmpty: skippedEmptyIDs(skipped)}
		if fields != nil && fields.Branch != "" {
			out.GitState = string(next.state)
			switch next.state {
			case mrBranchStateErr:
				out.VerifyError = true
			case mrBranchStateMissing:
				exists := false
				out.BranchExists = &exists
			case mrBranchStateEmpty, mrBranchStateOK:
				exists := true
				empty := next.state == mrBranchStateEmpty
				ahead := next.ahead
				out.BranchExists = &exists
				out.BranchEmpty = &empty
				out.CommitsAhead = &ahead
			}
		}
		return outputJSON(out)
	}

	// Human-readable output
	fmt.Printf("%s Next MR to process:\n\n", style.Bold.Render("🎯"))

	score := calculateMRScore(next.issue, fields, now)

	fmt.Printf("  ID:       %s\n", next.issue.ID)
	fmt.Printf("  Score:    %.1f\n", score)
	fmt.Printf("  Priority: P%d\n", next.issue.Priority)

	if fields != nil {
		if fields.Branch != "" {
			fmt.Printf("  Branch:   %s\n", fields.Branch)
		}
		if fields.Worker != "" {
			fmt.Printf("  Worker:   %s\n", fields.Worker)
		}
		if fields.ConvoyID != "" {
			fmt.Printf("  Convoy:   %s\n", fields.ConvoyID)
		}
		if fields.RetryCount > 0 {
			fmt.Printf("  Retries:  %d\n", fields.RetryCount)
		}
	}

	fmt.Printf("  Age:      %s\n", formatMRAge(next.issue.CreatedAt))

	if gitState := describeMQNextGitState(next); gitState != "" {
		fmt.Printf("  Git:      %s\n", gitState)
	}

	if others > 0 {
		fmt.Printf("\n  %s\n", style.Dim.Render(fmt.Sprintf("(%d more in queue)", others)))
	}

	reportSkippedEmptyMRs(os.Stdout, rigName, skipped)

	return nil
}

// describeMQNextGitState renders the verified branch state for the picked MR.
// Returns "" when no verification ran, so the line is absent rather than
// claiming an unchecked branch is fine.
func describeMQNextGitState(c *mqNextCandidate) string {
	switch c.state {
	case mrBranchStateOK:
		return style.Success.Render(fmt.Sprintf("OK (%d commit(s) ahead of target)", c.ahead))
	case mrBranchStateMissing:
		return style.Error.Render("MISSING (no local or origin ref — the merge will fail)")
	case mrBranchStateErr:
		return style.Warning.Render("ERR (git could not verify this branch)")
	default:
		return ""
	}
}

// skippedEmptyIDs lists the IDs of MRs excluded by the content check.
func skippedEmptyIDs(skipped []mqNextCandidate) []string {
	if len(skipped) == 0 {
		return nil
	}
	ids := make([]string, 0, len(skipped))
	for _, c := range skipped {
		ids = append(ids, c.issue.ID)
	}
	return ids
}

// reportSkippedEmptyMRs names every MR the content check excluded. Skipping
// silently would turn one failure mode (a no-op merge) into another (an MR that
// never gets picked and never gets explained), so the skip always comes with the
// IDs and the disposition.
func reportSkippedEmptyMRs(w io.Writer, rigName string, skipped []mqNextCandidate) {
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(w, "\n  %s %d MR(s) skipped: branch carries no commits over its target\n",
		style.Error.Render("⚠"),
		len(skipped))
	for _, c := range skipped {
		branch := ""
		if c.fields != nil {
			branch = c.fields.Branch
		}
		fmt.Fprintf(w, "    %s\n", style.Dim.Render(fmt.Sprintf("%s  %s", c.issue.ID, branch)))
	}
	fmt.Fprintf(w, "  %s\n", style.Dim.Render(fmt.Sprintf("Merging one is a no-op that still closes the source issue — reject instead: gt mq reject %s <mr-id> --reason \"empty branch\" --notify", rigName)))
}
