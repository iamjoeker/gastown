package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	patrolBranchesJSON    bool
	patrolBranchesAll     bool
	patrolBranchesRig     string
	patrolBranchesTarget  string
	patrolBranchesRemote  string
	patrolBranchesNoFetch bool
)

var patrolBranchesCmd = &cobra.Command{
	Use:   "branches",
	Short: "Sweep the remote for polecat branches whose work is not in the target",
	Long: `List polecat branches on the remote whose content is not in the target branch.

This is the backward half of stranded-work detection. The refinery reports a
stranding at the moment it creates one; this finds the ones that already exist,
whatever route made them — a bead auto-closed under a pushed branch, a polecat
that closed its own bead before submitting, an MR rejected after the bead
closed, a bead closed with no MR ever created, a gt done that refused to submit
and said nothing. All of them present as the same shape, which is why sweeping
for the shape catches routes nobody has enumerated.

Containment is decided by ancestry first, then by an empty merge, then by patch
identity, because ancestry proves containment ONE WAY ONLY: a branch that is not
an ancestor of the target may still have landed by squash or cherry-pick.

Each branch is classified:

  check     unmerged, no open MR, terminal bead, unreported — needs a decision
  unknown   could not be classified; a question, not an all-clear
  reported  an open stranded-rejection report already names this bead
  queued    an open merge request is holding it — queued, not stranded
  active    the bead is still re-slingable, so nothing is stranded yet
  landed    content is already in the target

Only check and unknown are the short list. The rest are shown with --all.

This command WRITES NOTHING: it files no beads, reopens no issues and deletes
no branches. It cannot tell a prematurely closed bead from a correctly closed
one whose branch is redundant — that needs a rehearsal merge and a human — so
it reports branches to CHECK and never claims work was lost.

Examples:
  gt patrol branches                    # short list for the current rig
  gt patrol branches --all              # every polecat branch, classified
  gt patrol branches --json             # machine-readable
  gt patrol branches --rig gastown --target main`,
	RunE: runPatrolBranches,
}

func init() {
	patrolBranchesCmd.Flags().BoolVar(&patrolBranchesJSON, "json", false, "Output as JSON")
	patrolBranchesCmd.Flags().BoolVar(&patrolBranchesAll, "all", false, "Show every branch, including landed, queued and active ones")
	patrolBranchesCmd.Flags().StringVar(&patrolBranchesRig, "rig", "", "Rig to sweep (default: infer from cwd or GT_RIG)")
	patrolBranchesCmd.Flags().StringVar(&patrolBranchesTarget, "target", "", "Target branch to compare against (default: the rig's default branch)")
	patrolBranchesCmd.Flags().StringVar(&patrolBranchesRemote, "remote", "origin", "Remote to sweep (branches are listed from its PUSH url)")
	patrolBranchesCmd.Flags().BoolVar(&patrolBranchesNoFetch, "no-fetch", false, "Skip refreshing the target ref before comparing (faster, and wrong if the target moved)")

	patrolCmd.AddCommand(patrolBranchesCmd)
}

// PatrolBranchesOutput is the JSON output format for the branch sweep.
type PatrolBranchesOutput struct {
	Rig       string `json:"rig"`
	Timestamp string `json:"timestamp"`
	*witness.BranchSweepResult
	Attention int `json:"attention"`
}

func runPatrolBranches(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigName := patrolBranchesRig
	if rigName == "" {
		rigName = os.Getenv("GT_RIG")
	}
	if rigName == "" {
		rigName, err = inferRigFromCwd(townRoot)
		if err != nil {
			return fmt.Errorf("could not determine rig: %w\nUse --rig to specify", err)
		}
	}

	rigPath := filepath.Join(townRoot, rigName)
	if _, statErr := os.Stat(rigPath); statErr != nil {
		return fmt.Errorf("rig %s not found at %s", rigName, rigPath)
	}

	repoGit := getRepoGitForRig(rigPath)
	remote := strings.TrimSpace(patrolBranchesRemote)
	if remote == "" {
		remote = "origin"
	}

	targets := branchSweepTargets(repoGit, remote, patrolBranchesTarget)
	if len(targets) == 0 {
		return fmt.Errorf("no comparison ref exists for %s: neither %s nor upstream carries the default branch", rigName, remote)
	}

	var warnings []string
	if !patrolBranchesNoFetch {
		for _, targetRemote := range targetRemotes(targets, remote) {
			if fetchErr := repoGit.FetchPrune(targetRemote); fetchErr != nil {
				// A stale target overstates the short list rather than
				// understating it, so this is a warning and not a stop.
				warnings = append(warnings, fmt.Sprintf("could not refresh %s: %v (its ref may be stale, so 'check' may include branches that have since landed)", targetRemote, fetchErr))
			}
		}
	}

	result, err := witness.SweepUnmergedPolecatBranches(
		repoGit,
		beads.New(rigPath),
		witness.BranchSweepOptions{Remote: remote, Targets: targets},
	)
	if err != nil {
		return err
	}
	result.Errors = append(result.Errors, warnings...)

	if patrolBranchesJSON {
		return writePatrolBranchesJSON(cmd.OutOrStdout(), rigName, result)
	}
	return writePatrolBranchesHuman(cmd.OutOrStdout(), rigName, result, patrolBranchesAll)
}

// branchSweepRefResolver is the slice of git that target selection needs.
type branchSweepRefResolver interface {
	RemoteDefaultBranch() string
	CleanBaseRef(remote, defaultBranch, target string) string
	RefExists(ref string) (bool, error)
	IsAncestor(ancestor, descendant string) (bool, error)
}

// branchSweepTargets picks the refs a branch must be absent from before it is
// worth anyone's attention.
//
// One trunk is the normal case and two is the fork case, and picking the wrong
// one of two is not a rounding error: on gastown, origin/main is 289 commits
// ahead of upstream/main, and comparing against upstream alone put six branches
// on the short list of which three had demonstrably landed. So when both refs
// exist, both are checked, and containment in either counts.
//
// A fully qualified --target is honoured exactly — an operator naming
// upstream/main is asking about upstream/main. A bare --target names a BRANCH,
// so it expands the same way the default does.
//
// Candidates are ordered most-advanced first, so the ref quoted back in the
// guidance is the one work actually lands on rather than whichever the
// fork-detection heuristic happens to prefer.
func branchSweepTargets(g branchSweepRefResolver, remote, explicit string) []string {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" && (strings.HasPrefix(explicit, "refs/") || git.RemoteForRef(explicit) != "") {
		return []string{explicit}
	}

	branch := strings.TrimSpace(g.RemoteDefaultBranch())
	if explicit != "" {
		branch = explicit
	}
	if branch == "" {
		branch = "main"
	}

	var candidates []string
	for _, candidate := range []string{remote + "/" + branch, "upstream/" + branch} {
		if slicesContains(candidates, candidate) {
			continue
		}
		if ok, err := g.RefExists(candidate); err == nil && ok {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		// Nothing resolved. Fall back to the repo's own notion of a base
		// rather than inventing one, and let the sweep report against it.
		if fallback := strings.TrimSpace(g.CleanBaseRef(remote, branch, "")); fallback != "" {
			return []string{fallback}
		}
		return nil
	}
	return orderTargetsByReach(g, candidates)
}

// orderTargetsByReach sorts candidates so that a ref containing another comes
// first. The count of other candidates a ref contains is a stable sort key, so
// this stays well-defined however many trunks a rig grows.
func orderTargetsByReach(g branchSweepRefResolver, candidates []string) []string {
	if len(candidates) < 2 {
		return candidates
	}
	reach := make(map[string]int, len(candidates))
	for _, a := range candidates {
		for _, b := range candidates {
			if a == b {
				continue
			}
			if contained, err := g.IsAncestor(b, a); err == nil && contained {
				reach[a]++
			}
		}
	}
	ordered := append([]string(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return reach[ordered[i]] > reach[ordered[j]]
	})
	return ordered
}

// targetRemotes lists the remotes that must be refreshed for these targets.
func targetRemotes(targets []string, fallback string) []string {
	var remotes []string
	for _, target := range targets {
		name := git.RemoteForRef(target)
		if name == "" {
			name = fallback
		}
		if name != "" && !slicesContains(remotes, name) {
			remotes = append(remotes, name)
		}
	}
	return remotes
}

func slicesContains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func writePatrolBranchesJSON(w io.Writer, rigName string, result *witness.BranchSweepResult) error {
	out := PatrolBranchesOutput{
		Rig:               rigName,
		Timestamp:         time.Now().UTC().Format(time.RFC3339),
		BranchSweepResult: result,
		Attention:         result.AttentionCount(),
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func writePatrolBranchesHuman(w io.Writer, rigName string, result *witness.BranchSweepResult, showAll bool) error {
	// Name every ref containment was tested against. A short list is only
	// interpretable next to what it was measured against, and on a rig with
	// two trunks the choice moves the answer.
	targets := result.Targets
	if len(targets) == 0 {
		targets = []string{result.Target}
	}
	label := "target"
	if len(targets) > 1 {
		label = "targets"
	}
	fmt.Fprintf(w, "%s %s  %s %s  remote %s\n",
		style.Bold.Render("Branch sweep:"), rigName,
		label, style.Info.Render(strings.Join(targets, " + ")), result.Remote)

	for _, e := range result.Errors {
		fmt.Fprintf(w, "%s %s\n", style.Warning.Render("⚠"), e)
	}

	if result.Scanned == 0 {
		// A zero here is about the listing, not about the rig's health, and
		// the two read identically unless said apart.
		fmt.Fprintf(w, "%s no polecat branches on %s — nothing to classify.\n",
			style.Dim.Render("○"), result.Remote)
		return nil
	}

	rows := result.Findings
	if !showAll {
		rows = nil
		for _, f := range result.Findings {
			if f.Class.NeedsAttention() {
				rows = append(rows, f)
			}
		}
	}

	if len(rows) > 0 {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "CLASS\tBRANCH\tBEAD\tBEAD STATUS\tMR\tMR STATUS\tNOTE")
		for _, f := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				renderBranchSweepClass(f.Class),
				f.Branch,
				orDash(f.IssueID),
				orDash(f.IssueStatus),
				orDash(f.MRID),
				orDash(branchSweepMRStatus(f)),
				f.Note,
			)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "%s  %s\n", style.Bold.Render("Summary:"), branchSweepSummary(result))

	attention := result.AttentionCount()
	switch {
	case attention == 0 && showAll:
		fmt.Fprintf(w, "%s nothing needs a decision.\n", style.SuccessPrefix)
	case attention == 0:
		fmt.Fprintf(w, "%s nothing needs a decision. Re-run with --all to see the classified branches.\n", style.SuccessPrefix)
	default:
		counts := result.CountByClass()
		fmt.Fprintf(w, "%s %d branch(es) need a look: %d to CHECK, %d that could not be classified.\n",
			style.Warning.Render("⚠"), attention,
			counts[witness.BranchSweepCheck], counts[witness.BranchSweepUnknown])
		fmt.Fprintf(w, "  This is a short list, NOT a claim that work was lost. A branch can be unmerged\n")
		fmt.Fprintf(w, "  because it was superseded (correctly closed, branch redundant) or because its bead\n")
		fmt.Fprintf(w, "  closed underneath live work (stranded). Separating them needs a rehearsal merge —\n")
		fmt.Fprintf(w, "  resolve to %s's side and measure the residual — and a person.\n", result.Target)
		if counts[witness.BranchSweepUnknown] > 0 {
			fmt.Fprintf(w, "  An unclassified branch is a question, not an all-clear: a remote tip that moved\n")
			fmt.Fprintf(w, "  mid-sweep reads this way, and so does a store that could not be read. Re-run.\n")
		}
		fmt.Fprintf(w, "\n  For each branch above:\n")
		fmt.Fprintf(w, "    git log --oneline %s..<branch>   # what is on it\n", result.Target)
		fmt.Fprintf(w, "    git cherry %s <branch>           # '-' prefixed lines already landed\n", result.Target)
		fmt.Fprintf(w, "  Then reopen the bead if the work is real, or leave it closed and delete the branch.\n")
		fmt.Fprintf(w, "  %s\n", style.Dim.Render("This command does not do either — it writes nothing."))
	}
	return nil
}

// branchSweepSummary renders the per-class tally, including the classes that
// are zero, so a reader can tell "measured, none" from "not looked at".
func branchSweepSummary(result *witness.BranchSweepResult) string {
	counts := result.CountByClass()
	order := []witness.BranchSweepClass{
		witness.BranchSweepCheck,
		witness.BranchSweepUnknown,
		witness.BranchSweepReported,
		witness.BranchSweepQueued,
		witness.BranchSweepActive,
		witness.BranchSweepLanded,
	}
	parts := make([]string, 0, len(order)+1)
	parts = append(parts, fmt.Sprintf("%d scanned", result.Scanned))
	for _, class := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[class], class))
	}
	if !result.MRsMeasured {
		parts = append(parts, "MR column UNMEASURED")
	}
	return strings.Join(parts, ", ")
}

func renderBranchSweepClass(class witness.BranchSweepClass) string {
	switch class {
	case witness.BranchSweepCheck:
		return style.Warning.Render(string(class))
	case witness.BranchSweepUnknown:
		return style.Error.Render(string(class))
	case witness.BranchSweepLanded:
		return style.Dim.Render(string(class))
	default:
		return string(class)
	}
}

// branchSweepMRStatus folds the close reason into the status column, because
// "closed" alone loses the distinction between a rejected MR and a merged one.
func branchSweepMRStatus(f witness.BranchSweepFinding) string {
	if f.MRStatus == "" {
		return ""
	}
	if f.MRCloseReason != "" {
		return f.MRStatus + ":" + f.MRCloseReason
	}
	return f.MRStatus
}
