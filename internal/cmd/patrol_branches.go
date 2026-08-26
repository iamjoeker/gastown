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
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	patrolBranchesJSON      bool
	patrolBranchesAll       bool
	patrolBranchesDeletable bool
	patrolBranchesRig       string
	patrolBranchesTarget    string
	patrolBranchesRemote    string
	patrolBranchesNoFetch   bool
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

A landed branch is two situations under one name, and --deletable separates
them. Branch hygiene deletes a remote branch only when it is an ANCESTOR of the
target; this sweep also proves containment by an empty merge and by patch
identity. A branch rebased before landing is contained but is not an ancestor,
so hygiene will not collect it and the short list will not raise it — it is
reported as landed on every future sweep, forever. Those rows carry the
strongest evidence of redundancy in the listing (patch-id EQUALITY, the
reliable direction) and are marked landed* in the table.

This command WRITES NOTHING: it files no beads, reopens no issues and deletes
no branches. It cannot tell a prematurely closed bead from a correctly closed
one whose branch is redundant — that needs a rehearsal merge and a human — so
it reports branches to CHECK and never claims work was lost. --deletable is a
listing too: these are shared remote refs, so it prints the commands and stops.

Examples:
  gt patrol branches                    # short list for the current rig
  gt patrol branches --all              # every polecat branch, classified
  gt patrol branches --deletable        # landed branches hygiene cannot reach
  gt patrol branches --json             # machine-readable
  gt patrol branches --rig gastown --target main`,
	RunE: runPatrolBranches,
}

func init() {
	patrolBranchesCmd.Flags().BoolVar(&patrolBranchesJSON, "json", false, "Output as JSON")
	patrolBranchesCmd.Flags().BoolVar(&patrolBranchesAll, "all", false, "Show every branch, including landed, queued and active ones")
	patrolBranchesCmd.Flags().BoolVar(&patrolBranchesDeletable, "deletable", false, "List only landed branches that are NOT ancestors of the target — contained, but out of branch hygiene's reach (lists them; deletes nothing)")
	patrolBranchesCmd.Flags().StringVar(&patrolBranchesRig, "rig", "", "Rig to sweep (default: GT_RIG, else inferred from cwd, else the only registered rig)")
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
	// HygieneUnreachable is the size of the --deletable list: landed branches
	// that are not ancestors of the target, which nothing collects. It is a
	// separate total from Attention because it asks for a deletion rather than
	// for a decision.
	HygieneUnreachable int `json:"hygiene_unreachable"`
}

func runPatrolBranches(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigName, rigSource, err := resolvePatrolBranchesRig(townRoot, patrolBranchesRig)
	if err != nil {
		return err
	}

	rigPath := filepath.Join(townRoot, rigName)
	if _, statErr := os.Stat(rigPath); statErr != nil {
		return fmt.Errorf("rig %s not found at %s (%s)", rigName, rigPath, rigSource)
	}

	repoGit := getRepoGitForRig(rigPath)
	if err := ensureRigRepoUsable(rigName, rigPath, rigSource, repoGit); err != nil {
		return err
	}
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
				//
				// A deadline kill is said apart from an ordinary refresh
				// failure, because the two ask for different things. Stale
				// means "re-read the list with suspicion". Unresponsive means
				// the remote is not answering ANYONE, and everything below it
				// is about to be unmeasured — the refresh is simply the first
				// place it shows. This is the call that was measured hanging
				// past four minutes on two rigs at once (gt-i9wz); it now
				// returns, and this line is where a reader learns why.
				if git.IsRemoteUnresponsive(fetchErr) {
					warnings = append(warnings, fmt.Sprintf("%s STOPPED RESPONDING while refreshing: %v — it was killed on its deadline rather than waited on, and the classification below is unreliable for the same reason. Re-run when the remote answers", targetRemote, fetchErr))
					continue
				}
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

	// JSON is not filtered by either mode: it carries every branch with
	// hygiene_unreachable on each row, so a consumer selects for itself rather
	// than having to re-run the sweep with a different flag.
	if patrolBranchesJSON {
		return writePatrolBranchesJSON(cmd.OutOrStdout(), rigName, result)
	}
	if patrolBranchesDeletable {
		return writePatrolBranchesDeletable(cmd.OutOrStdout(), rigName, result)
	}
	return writePatrolBranchesHuman(cmd.OutOrStdout(), rigName, result, patrolBranchesAll)
}

// resolvePatrolBranchesRig decides which rig to sweep and reports where the
// answer came from, so a wrong one is traceable rather than merely wrong.
//
// An explicit name — --rig or GT_RIG — is honoured as given: an operator who
// names a rig is asking about that rig, and quietly substituting another would
// answer a question nobody asked.
//
// Inference from cwd is a guess, and a bad guess is the whole of gt-m7cc. It
// returns the first path component under the town root, which is a rig name
// only when cwd is inside a rig; run from the town's own mayor/, warrants/ or
// logs/ and it confidently returns that directory name instead. So an inferred
// name is checked against the registry before it is used, and a town with
// exactly one rig answers the question outright rather than failing on a
// technicality.
func resolvePatrolBranchesRig(townRoot, explicit string) (name, source string, err error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit, "from --rig", nil
	}
	if env := strings.TrimSpace(os.Getenv("GT_RIG")); env != "" {
		return env, "from GT_RIG", nil
	}

	registered := registeredRigNames(townRoot)
	inferred, inferErr := inferRigFromCwd(townRoot)
	if inferErr == nil && (len(registered) == 0 || slicesContains(registered, inferred)) {
		// An unreadable registry cannot veto the guess, only fail to confirm
		// it; ensureRigRepoUsable is what stops an unusable one.
		return inferred, "inferred from the current directory", nil
	}
	if len(registered) == 1 {
		return registered[0], "defaulted to the only registered rig", nil
	}

	detail := "the current directory is not inside a rig"
	if inferErr == nil {
		detail = fmt.Sprintf("%q is not a registered rig", inferred)
	}
	if len(registered) == 0 {
		return "", "", fmt.Errorf("could not determine rig: %s, and no rigs are registered in %s\nUse --rig to specify",
			detail, filepath.Join(townRoot, "mayor", "rigs.json"))
	}
	return "", "", fmt.Errorf("could not determine rig: %s\nUse --rig to specify one of: %s",
		detail, strings.Join(registered, ", "))
}

// registeredRigNames lists the rigs in the town registry, sorted.
//
// An unreadable registry yields nothing, which callers must read as "could not
// check" and never as "there are none" — the two are indistinguishable in the
// return value, so the only safe use of an empty result is to skip the check.
func registeredRigNames(townRoot string) []string {
	cfg, cfgErr := config.LoadRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"))
	if cfgErr != nil || cfg == nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Rigs))
	for rigName := range cfg.Rigs {
		names = append(names, rigName)
	}
	sort.Strings(names)
	return names
}

// ensureRigRepoUsable proves the resolved repository exists before any git is
// asked to run in it.
//
// A git handed a working directory it cannot chdir into fails at exec, and the
// failure names the git BINARY rather than the missing directory — so an
// operator goes looking for a broken git installation, which was never the
// problem (gt-m7cc). The git layer now re-attributes that error, but this is
// the message worth having: it can name the rig that was picked, say where
// that came from, and give the flag that overrides it.
func ensureRigRepoUsable(rigName, rigPath, source string, repoGit *git.Git) error {
	workDir := repoGit.WorkDir()
	if workDir == "" {
		// A bare mirror: git is passed --git-dir and never chdirs.
		return nil
	}
	if _, statErr := os.Stat(workDir); statErr == nil {
		return nil
	}
	bare, worktree := rigRepoCandidates(rigPath)
	return fmt.Errorf("no repository for rig %q at %s (%s)\n"+
		"  looked for a bare mirror at %s and a worktree at %s — neither exists\n"+
		"  pass --rig <name> or run from a rig worktree",
		rigName, rigPath, source, bare, worktree)
}

// branchSweepTargets picks the refs a branch must be absent from before it is
// worth anyone's attention. The logic lives in the witness package because the
// orphan-bead landed-check asks the same containment question and must pick the
// same trunks (gt-e7dd); getting different answers from the two would be worse
// than either answer alone.
func branchSweepTargets(g witness.BranchSweepRefResolver, remote, explicit string) []string {
	return witness.ResolveComparisonTargets(g, remote, explicit)
}

// targetRemotes lists the remotes that must be refreshed for these targets.
func targetRemotes(targets []string, fallback string) []string {
	return witness.TargetRemotes(targets, fallback)
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
		Rig:                rigName,
		Timestamp:          time.Now().UTC().Format(time.RFC3339),
		BranchSweepResult:  result,
		Attention:          result.AttentionCount(),
		HygieneUnreachable: result.HygieneUnreachableCount(),
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// writeBranchSweepHeader names every ref containment was tested against, and
// every sweep-wide failure. A short list is only interpretable next to what it
// was measured against, and on a rig with two trunks the choice moves the
// answer. It reports whether anything was scanned at all: a zero there is about
// the listing, not about the rig's health, and the two read identically unless
// said apart.
func writeBranchSweepHeader(w io.Writer, rigName string, result *witness.BranchSweepResult) (scannedAny bool) {
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
		fmt.Fprintf(w, "%s no polecat branches on %s — nothing to classify.\n",
			style.Dim.Render("○"), result.Remote)
		return false
	}
	return true
}

func writePatrolBranchesHuman(w io.Writer, rigName string, result *witness.BranchSweepResult, showAll bool) error {
	if !writeBranchSweepHeader(w, rigName, result) {
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
		if err := writeBranchSweepTable(w, rows); err != nil {
			return err
		}
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

	writeBranchSweepHygieneNotice(w, result, showAll)
	return nil
}

// writeBranchSweepHygieneNotice raises the landed rows that nothing collects.
//
// It prints in the DEFAULT view, where landed rows are not shown at all, and
// that placement is the point: these branches were previously visible only
// under --all, folded into a dim "landed" that reads as finished. A reader who
// never passes --all never learns they exist, and they accumulate in a listing
// whose worth is its shortness (gt-l65a).
func writeBranchSweepHygieneNotice(w io.Writer, result *witness.BranchSweepResult, showAll bool) {
	unreachable := result.HygieneUnreachableCount()
	if unreachable == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s %d landed branch(es) are NOT ancestors of %s.\n",
		style.Warning.Render("⚠"), unreachable, result.Target)
	if showAll {
		fmt.Fprintln(w, "  Marked landed* in the table above.")
	}
	fmt.Fprintf(w, "  Their content is provably in the target — patch-id EQUALITY, the reliable\n")
	fmt.Fprintf(w, "  direction, and the strongest evidence of redundancy this sweep produces. But\n")
	fmt.Fprintf(w, "  branch hygiene deletes by ancestry alone, so nothing will ever collect them and\n")
	fmt.Fprintf(w, "  every future sweep reports them again.\n")
	fmt.Fprintf(w, "    gt patrol branches --deletable   # the list, with the commands to verify and delete\n")
}

// writeBranchSweepTable renders the shared row format. landed* marks a landed
// branch that is not an ancestor: same class, different route out.
func writeBranchSweepTable(w io.Writer, rows []witness.BranchSweepFinding) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CLASS\tBRANCH\tBEAD\tBEAD STATUS\tMR\tMR STATUS\tNOTE")
	for _, f := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			renderBranchSweepClass(f),
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
	return nil
}

// writePatrolBranchesDeletable renders the short list an operator can act on:
// landed branches that branch hygiene cannot reach.
//
// It is a listing and not an action. These are shared remote refs — another
// worktree may have one checked out, and a delete is not undoable from here —
// and the whole design of this sweep is to emit evidence rather than verdicts.
// So it prints the verification command next to the deletion command, in that
// order, and performs neither.
func writePatrolBranchesDeletable(w io.Writer, rigName string, result *witness.BranchSweepResult) error {
	if !writeBranchSweepHeader(w, rigName, result) {
		return nil
	}

	var rows []witness.BranchSweepFinding
	for _, f := range result.Findings {
		if f.HygieneUnreachable {
			rows = append(rows, f)
		}
	}

	counts := result.CountByClass()
	if len(rows) == 0 {
		// Say what was measured, not just that the answer was zero: "no
		// deletable branches" and "no landed branches at all" are different
		// facts about the rig and must not render identically.
		fmt.Fprintf(w, "%s no landed branch is out of hygiene's reach — of %d scanned, %d landed and every one is an ancestor of %s.\n",
			style.SuccessPrefix, result.Scanned, counts[witness.BranchSweepLanded], result.Target)
		return nil
	}

	if err := writeBranchSweepTable(w, rows); err != nil {
		return err
	}

	fmt.Fprintf(w, "%s  %s\n", style.Bold.Render("Summary:"), branchSweepSummary(result))
	fmt.Fprintf(w, "%s %d of %d landed branch(es) are NOT ancestors of %s, so branch hygiene\n",
		style.Warning.Render("⚠"), len(rows), counts[witness.BranchSweepLanded], result.Target)
	fmt.Fprintf(w, "  will never delete them. Their content is in the target by patch identity, which\n")
	fmt.Fprintf(w, "  is positive evidence of redundancy — stronger than anything on the CHECK list.\n")
	fmt.Fprintf(w, "\n  Verify each one, then delete it:\n")
	for _, f := range rows {
		base := strings.TrimSpace(f.ContainedIn)
		if base == "" {
			base = result.Target
		}
		fmt.Fprintf(w, "    git cherry %s %s/%s\n", base, result.Remote, f.Branch)
		fmt.Fprintf(w, "    git push %s --delete %s\n", result.Remote, f.Branch)
	}
	fmt.Fprintf(w, "  %s\n", style.Dim.Render("A '+' prefixed line from git cherry means a patch is NOT in the target — stop and check."))
	fmt.Fprintf(w, "  %s\n", style.Dim.Render("This command deletes nothing: it writes nothing at all."))
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
		part := fmt.Sprintf("%d %s", counts[class], class)
		// The landed tally is split because the two halves route to different
		// places — one to branch hygiene, one to nobody — and a single number
		// hides the half that accumulates.
		if class == witness.BranchSweepLanded {
			unreachable := result.HygieneUnreachableCount()
			part += fmt.Sprintf(" (%d ancestor, %d not an ancestor)", counts[class]-unreachable, unreachable)
		}
		parts = append(parts, part)
	}
	if !result.MRsMeasured {
		parts = append(parts, "MR column UNMEASURED")
	}
	return strings.Join(parts, ", ")
}

// renderBranchSweepClass renders the class column. A landed branch that is not
// an ancestor is marked landed*: the classification is the same, but nothing
// deletes it, and the table is where a reader first meets that fact.
func renderBranchSweepClass(f witness.BranchSweepFinding) string {
	switch f.Class {
	case witness.BranchSweepCheck:
		return style.Warning.Render(string(f.Class))
	case witness.BranchSweepUnknown:
		return style.Error.Render(string(f.Class))
	case witness.BranchSweepLanded:
		if f.HygieneUnreachable {
			return style.Warning.Render(string(f.Class) + "*")
		}
		return style.Dim.Render(string(f.Class))
	default:
		return string(f.Class)
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
