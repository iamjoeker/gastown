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
	patrolBranchesJSON       bool
	patrolBranchesAll        bool
	patrolBranchesDeletable  bool
	patrolBranchesSuperseded bool
	patrolBranchesRig        string
	patrolBranchesTarget     string
	patrolBranchesRemote     string
	patrolBranchesNoFetch    bool

	patrolBranchesMarkReason string
	patrolBranchesMarkForce  bool
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

The sweep RE-DERIVES every verdict on every run, and that is what 'mark' is
for. A branch settled once — superseded, its substance landed by different
commits, its residual measured at nothing — has no way to say so, so it comes
back on the next cycle looking exactly like a branch nobody has looked at yet.
Measured on gastown: 21 of 36 branches re-reported unchanged across cycles 1, 6
and 16 of one witness session, and two agents independently re-derived the same
verdict on five of them within an hour.

  gt patrol branches mark <branch> -m "why"   # record the settlement, in git
  gt patrol branches unmark <branch>          # take it back
  gt patrol branches marks                    # what has been settled, and by whom

A marked branch is reported as 'superseded' and drops off the short list and out
of --deletable. The marker names the exact commit it settles: push to the branch
again and the marker stops applying, the row comes back, and the sweep says why.

This command WRITES NOTHING: it files no beads, reopens no issues and deletes
no branches. It cannot tell a prematurely closed bead from a correctly closed
one whose branch is redundant — that needs a rehearsal merge and a human — so
it reports branches to CHECK and never claims work was lost. --deletable is a
listing too: these are shared remote refs, so it prints the commands and stops.
(The mark/unmark SUBCOMMANDS do write, and only to a ref of their own.)

Examples:
  gt patrol branches                    # short list for the current rig
  gt patrol branches --all              # every polecat branch, classified
  gt patrol branches --deletable        # landed branches hygiene cannot reach
  gt patrol branches --superseded       # branches already settled by a marker
  gt patrol branches --json             # machine-readable
  gt patrol branches --rig gastown --target main`,
	RunE: runPatrolBranches,
}

// patrolBranchesMarkCmd records a settlement so the next sweep does not
// re-derive it.
//
// It writes ONE ref in the rig's own repository and nothing else: no bead, no
// wisp, no label, no remote write, and it does not touch the branch it marks.
// The storage is a lifetime decision rather than a convenience one — a wisp is
// purged at 168h and takes its labels with it, so any marker held there is
// guaranteed to die before the branch it describes.
var patrolBranchesMarkCmd = &cobra.Command{
	Use:   "mark <branch>",
	Short: "Record that a branch is settled, so the sweep stops re-deriving it",
	Long: `Mark a branch superseded: settled, understood, and not worth re-deriving.

The sweep cannot tell "superseded — correctly closed, branch redundant" from
"closed prematurely — work stranded". Both present identically, so each new
reader re-does the whole classification to learn which it is. That derivation is
the expensive part and it had nowhere to live; this is where it lives.

A reason is REQUIRED. A marker carrying only the verdict reproduces the original
defect one level up — the next reader still cannot tell settled from abandoned,
and re-derives anyway. Write what you checked and what you found.

The marker names the branch tip it settles, read from the remote at mark time.
If the branch is pushed to afterwards the marker stops applying, the branch
returns to the short list, and the sweep says which commit was settled and which
is now the tip. So a marker can never hide work that arrived after it.

Storage: a ref under refs/gt/superseded/ in the rig's repository, pointing at a
blob of JSON. It shares a lifetime with the branch structurally — it cannot be
purged while the branch stands, it survives every Dolt operation, and the sweep
already enumerates refs so it costs no database round trip.

Examples:
  gt patrol branches mark polecat/dust/gt-k3v+aaa \
    -m "substance landed as 7a108237 out of band; git cherry residual is 2 test files, both since deleted"
  gt patrol branches mark polecat/foo/gt-1+bbb -m "superseded by gt-u5c; net contribution measured at zero" --force`,
	Args: cobra.ExactArgs(1),
	RunE: runPatrolBranchesMark,
}

var patrolBranchesUnmarkCmd = &cobra.Command{
	Use:   "unmark <branch>",
	Short: "Remove a branch's superseded marker, putting it back on the sweep",
	Long: `Delete the superseded marker for a branch.

Use it when a settlement turns out to be wrong — the work was stranded after all,
or the reason no longer holds. The branch returns to whatever class the sweep
computes for it on the next run.

This deletes only the marker ref. The branch, its commits and its bead are
untouched.`,
	Args: cobra.ExactArgs(1),
	RunE: runPatrolBranchesUnmark,
}

var patrolBranchesMarksCmd = &cobra.Command{
	Use:   "marks",
	Short: "List the superseded markers recorded for this rig",
	Long: `Show every superseded marker: which branch, which commit, who settled it, why.

This reads the markers alone and does not contact the remote, so it answers "what
has been settled" without the cost of a sweep. It cannot say whether a marker
still APPLIES — that needs the current tip, which is what 'gt patrol branches
--superseded' reports.`,
	Args: cobra.NoArgs,
	RunE: runPatrolBranchesMarks,
}

func init() {
	patrolBranchesCmd.Flags().BoolVar(&patrolBranchesJSON, "json", false, "Output as JSON")
	patrolBranchesCmd.Flags().BoolVar(&patrolBranchesAll, "all", false, "Show every branch, including landed, queued and active ones")
	patrolBranchesCmd.Flags().BoolVar(&patrolBranchesDeletable, "deletable", false, "List only landed branches that are NOT ancestors of the target — contained, but out of branch hygiene's reach (lists them; deletes nothing)")
	patrolBranchesCmd.Flags().BoolVar(&patrolBranchesSuperseded, "superseded", false, "List only branches a marker has settled, with the reason each was settled for")
	patrolBranchesCmd.Flags().StringVar(&patrolBranchesRig, "rig", "", "Rig to sweep (default: GT_RIG, else inferred from cwd, else the only registered rig)")
	patrolBranchesCmd.Flags().StringVar(&patrolBranchesTarget, "target", "", "Target branch to compare against (default: the rig's default branch)")
	patrolBranchesCmd.Flags().StringVar(&patrolBranchesRemote, "remote", "origin", "Remote to sweep (branches are listed from its PUSH url)")
	patrolBranchesCmd.Flags().BoolVar(&patrolBranchesNoFetch, "no-fetch", false, "Skip refreshing the target ref before comparing (faster, and wrong if the target moved)")

	for _, sub := range []*cobra.Command{patrolBranchesMarkCmd, patrolBranchesUnmarkCmd, patrolBranchesMarksCmd} {
		sub.Flags().StringVar(&patrolBranchesRig, "rig", "", "Rig to act on (default: GT_RIG, else inferred from cwd, else the only registered rig)")
		sub.Flags().StringVar(&patrolBranchesRemote, "remote", "origin", "Remote the branch lives on (its PUSH url is the one read)")
	}
	patrolBranchesMarkCmd.Flags().StringVarP(&patrolBranchesMarkReason, "reason", "m", "", "Why this branch is settled — REQUIRED; a marker without a derivation is one the next reader has to redo")
	patrolBranchesMarkCmd.Flags().BoolVar(&patrolBranchesMarkForce, "force", false, "Overwrite an existing marker (refused by default: overwriting silently discards the derivation this exists to keep)")
	patrolBranchesMarksCmd.Flags().BoolVar(&patrolBranchesJSON, "json", false, "Output as JSON")

	patrolBranchesCmd.AddCommand(patrolBranchesMarkCmd)
	patrolBranchesCmd.AddCommand(patrolBranchesUnmarkCmd)
	patrolBranchesCmd.AddCommand(patrolBranchesMarksCmd)

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

	// Superseded is how many rows a durable marker settled on this run, and
	// StaleMarks how many carry a marker that no longer applies because the tip
	// moved. Both are emitted unconditionally: a zero here is a measurement,
	// and an absent key would read the same as a build that never looked.
	Superseded int `json:"superseded"`
	StaleMarks int `json:"stale_marks"`
}

// resolvePatrolBranchesRepo answers "which rig, and which repository" once, for
// the sweep and for the marker subcommands alike. A marker written against a
// different repository from the one the sweep reads would be a marker that
// never suppresses anything, so the two must not resolve the rig by different
// routes.
func resolvePatrolBranchesRepo() (rigName, rigPath string, repoGit *git.Git, err error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", "", nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigName, rigSource, err := resolvePatrolBranchesRig(townRoot, patrolBranchesRig)
	if err != nil {
		return "", "", nil, err
	}

	rigPath = filepath.Join(townRoot, rigName)
	if _, statErr := os.Stat(rigPath); statErr != nil {
		return "", "", nil, fmt.Errorf("rig %s not found at %s (%s)", rigName, rigPath, rigSource)
	}

	repoGit = getRepoGitForRig(rigPath)
	if err := ensureRigRepoUsable(rigName, rigPath, rigSource, repoGit); err != nil {
		return "", "", nil, err
	}
	return rigName, rigPath, repoGit, nil
}

func runPatrolBranches(cmd *cobra.Command, args []string) error {
	rigName, rigPath, repoGit, err := resolvePatrolBranchesRepo()
	if err != nil {
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

	// Markers are read before the sweep and passed in, so the sweep stays a
	// classifier over gathered facts. A failure here over-reports rather than
	// under-reports — every settled branch comes back as though nobody had
	// settled it — which is the safe direction, and the warning is what stops
	// that reading as a rig where nothing has been marked.
	marks, marksErr := repoGit.SupersededMarks()
	if marksErr != nil {
		marks = nil
		warnings = append(warnings, fmt.Sprintf(
			"could not read superseded markers (%s): %v — every branch below is classified as if UNMARKED, so settled work will be re-reported",
			git.SupersededRefPrefix, marksErr))
	}

	result, err := witness.SweepUnmergedPolecatBranches(
		repoGit,
		beads.New(rigPath),
		witness.BranchSweepOptions{Remote: remote, Targets: targets, Superseded: marks},
	)
	if err != nil {
		return err
	}
	result.MarksMeasured = marksErr == nil
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
	if patrolBranchesSuperseded {
		return writePatrolBranchesSuperseded(cmd.OutOrStdout(), rigName, result)
	}
	return writePatrolBranchesHuman(cmd.OutOrStdout(), rigName, result, patrolBranchesAll)
}

func runPatrolBranchesMark(cmd *cobra.Command, args []string) error {
	branch := strings.TrimPrefix(strings.TrimSpace(args[0]), "refs/heads/")
	reason := strings.TrimSpace(patrolBranchesMarkReason)
	if reason == "" {
		return fmt.Errorf("a marker needs a reason: pass -m \"why this branch is settled\"\n" +
			"  A marker recording only the verdict is one the next reader has to re-derive,\n" +
			"  which is the whole defect this exists to fix. Say what you checked and found.")
	}

	rigName, _, repoGit, err := resolvePatrolBranchesRepo()
	if err != nil {
		return err
	}
	remote := strings.TrimSpace(patrolBranchesRemote)
	if remote == "" {
		remote = "origin"
	}

	// The tip is read from the PUSH url, the same side the sweep lists from.
	// Reading the fetch side on a split-remote rig would record a commit the
	// sweep never sees, and the marker would be permanently stale — present,
	// correct in substance, and suppressing nothing.
	tip, tipErr := repoGit.PushRemoteBranchTip(remote, branch)
	if tipErr != nil {
		return fmt.Errorf("reading %s/%s to find the commit to settle: %w", remote, branch, tipErr)
	}
	if strings.TrimSpace(tip) == "" {
		// Not a technicality. A marker for a branch that is not there settles
		// nothing and will never be consulted, so reporting success would be
		// the same lie the read-back checks exist to catch.
		return fmt.Errorf("branch %q does not exist on %s, so there is nothing to settle\n"+
			"  gt patrol branches --all --rig %s   # the branches that do exist",
			branch, remote, rigName)
	}

	existing, err := repoGit.SupersededMarkFor(branch)
	if err != nil {
		return fmt.Errorf("checking for an existing marker on %s: %w", branch, err)
	}
	if existing != nil && !patrolBranchesMarkForce {
		// Refusing rather than overwriting: the stored reason is a derivation
		// somebody paid for, and silently replacing it destroys exactly the
		// artifact this feature exists to keep.
		var b strings.Builder
		fmt.Fprintf(&b, "%s already carries a superseded marker; pass --force to replace it\n", branch)
		fmt.Fprintf(&b, "  settled at: %s\n", orDash(shortCommit(existing.Commit)))
		if existing.MarkedBy != "" || existing.MarkedAt != "" {
			fmt.Fprintf(&b, "  settled by: %s %s\n", orDash(existing.MarkedBy), existing.MarkedAt)
		}
		fmt.Fprintf(&b, "  reason:     %s\n", orDash(existing.Reason))
		if existing.StaleFor(tip) {
			fmt.Fprintf(&b, "  NOTE: the tip is now %s, so that marker no longer applies — re-settling is probably right,\n", shortCommit(tip))
			fmt.Fprintf(&b, "        but check what arrived on the branch first: git log %s..%s\n", shortCommit(existing.Commit), shortCommit(tip))
		}
		return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
	}

	mark := git.SupersededMark{
		Branch:   branch,
		Commit:   tip,
		Reason:   reason,
		MarkedBy: markerActor(),
		MarkedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := repoGit.MarkSuperseded(mark); err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%s %s is settled at %s\n", style.SuccessPrefix, branch, shortCommit(tip))
	fmt.Fprintf(w, "  reason: %s\n", reason)
	fmt.Fprintf(w, "  marker: %s\n", git.SupersededRef(branch))
	fmt.Fprintf(w, "  %s\n", style.Dim.Render("The branch, its commits and its bead are untouched — only a ref was written."))
	fmt.Fprintf(w, "  %s\n", style.Dim.Render("Push to this branch again and the marker stops applying, by design."))
	return nil
}

func runPatrolBranchesUnmark(cmd *cobra.Command, args []string) error {
	branch := strings.TrimPrefix(strings.TrimSpace(args[0]), "refs/heads/")
	_, _, repoGit, err := resolvePatrolBranchesRepo()
	if err != nil {
		return err
	}

	existing, err := repoGit.SupersededMarkFor(branch)
	if err != nil {
		return fmt.Errorf("reading the marker on %s: %w", branch, err)
	}
	removed, err := repoGit.UnmarkSuperseded(branch)
	if err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	if !removed {
		// "Nothing to remove" is a different fact from "removed", and printing
		// the success line for both is how a caller comes to believe a marker
		// on another rig has been cleared.
		fmt.Fprintf(w, "%s %s carries no superseded marker — nothing was removed.\n", style.Dim.Render("○"), branch)
		return nil
	}
	fmt.Fprintf(w, "%s removed the superseded marker on %s\n", style.SuccessPrefix, branch)
	if existing != nil && strings.TrimSpace(existing.Reason) != "" {
		// Echo the derivation on the way out. It is about to stop existing, and
		// the terminal is the last place it can be caught if the removal was a
		// mistake.
		fmt.Fprintf(w, "  it had said: %s\n", existing.Reason)
	}
	fmt.Fprintf(w, "  %s\n", style.Dim.Render("The branch will be classified from scratch on the next sweep."))
	return nil
}

func runPatrolBranchesMarks(cmd *cobra.Command, args []string) error {
	rigName, _, repoGit, err := resolvePatrolBranchesRepo()
	if err != nil {
		return err
	}
	marks, err := repoGit.SupersededMarks()
	if err != nil {
		return err
	}

	branches := make([]string, 0, len(marks))
	for branch := range marks {
		branches = append(branches, branch)
	}
	sort.Strings(branches)

	w := cmd.OutOrStdout()
	if patrolBranchesJSON {
		out := struct {
			Rig   string               `json:"rig"`
			Ref   string               `json:"ref_prefix"`
			Count int                  `json:"count"`
			Marks []git.SupersededMark `json:"marks"`
		}{Rig: rigName, Ref: git.SupersededRefPrefix, Count: len(branches)}
		for _, branch := range branches {
			out.Marks = append(out.Marks, marks[branch])
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	if len(branches) == 0 {
		fmt.Fprintf(w, "%s no superseded markers on %s (looked in %s).\n",
			style.Dim.Render("○"), rigName, git.SupersededRefPrefix)
		fmt.Fprintf(w, "  %s\n", style.Dim.Render("gt patrol branches mark <branch> -m \"why\"   # record one"))
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "BRANCH\tSETTLED AT\tBY\tWHEN\tREASON")
	for _, branch := range branches {
		mark := marks[branch]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			branch, orDash(shortCommit(mark.Commit)), orDash(mark.MarkedBy), orDash(mark.MarkedAt), orDash(mark.Reason))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%s %d marker(s) in %s\n", style.Bold.Render("Total:"), len(branches), git.SupersededRefPrefix)
	fmt.Fprintf(w, "  %s\n", style.Dim.Render("This lists markers, not whether they still apply — a branch pushed to since being"))
	fmt.Fprintf(w, "  %s\n", style.Dim.Render("marked is no longer settled. gt patrol branches --superseded compares against the tips."))
	return nil
}

// markerActor names who settled a branch, for provenance.
//
// BD_ACTOR first because it is the identity every other gt write is attributed
// to, then GT_ROLE, then git's configured user. An empty string is returned
// rather than a placeholder: "unknown" reads as a recorded value and this is an
// absent one.
func markerActor() string {
	for _, key := range []string{"BD_ACTOR", "GT_ROLE"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
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
		Superseded:         result.SupersededCount(),
		StaleMarks:         result.StaleMarkCount(),
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
		if stale := result.StaleMarkCount(); stale > 0 {
			// Printed BEFORE the count, because a reader who marked these
			// branches will otherwise read the list as the marker failing.
			fmt.Fprintf(w, "%s %d branch(es) below carry a superseded marker that NO LONGER APPLIES:\n",
				style.Warning.Render("⚠"), stale)
			fmt.Fprintf(w, "  the branch was pushed to after it was settled, so the marker describes a commit\n")
			fmt.Fprintf(w, "  that is no longer the tip. They are listed on purpose — see the note on each row.\n\n")
		}
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
		fmt.Fprintf(w, "  Settled one? Record it, so the next sweep does not make you derive it again:\n")
		fmt.Fprintf(w, "    gt patrol branches mark <branch> -m \"what you checked and what you found\"\n")
	}

	writeBranchSweepSupersededNotice(w, result)
	writeBranchSweepHygieneNotice(w, result, showAll)
	return nil
}

// writeBranchSweepSupersededNotice accounts for the rows a marker removed.
//
// It prints even when the short list is empty, and especially then: a sweep
// reporting "nothing needs a decision" over 36 branches means one thing when
// none was settled by hand and quite another when 21 were, and the difference
// is exactly whether a reader should trust the quiet. A suppression that leaves
// no trace in the summary is how a marker turns from a record into a blindfold.
func writeBranchSweepSupersededNotice(w io.Writer, result *witness.BranchSweepResult) {
	settled := result.SupersededCount()
	if settled == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s %d branch(es) are held by a superseded marker and are not listed above.\n",
		style.Dim.Render("○"), settled)
	fmt.Fprintf(w, "  Each was settled once, with a reason, and stays settled while its tip is unchanged.\n")
	fmt.Fprintf(w, "    gt patrol branches --superseded   # what was settled, by whom, and why\n")
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

	// A settled branch is left out even though HygieneUnreachable is still true
	// of it. The field is a measurement — hygiene genuinely cannot delete it —
	// but this list is a request to ACT, and the marker is a recorded decision
	// not to. On the rig this was built for, that decision was explicit: the
	// branch commits are the only surviving copy of the work as originally
	// authored, because the substance landed via different commits.
	var rows []witness.BranchSweepFinding
	for _, f := range result.Findings {
		if f.HygieneUnreachable && f.Class != witness.BranchSweepSuperseded {
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
		if settled := result.SupersededCount(); settled > 0 {
			// Without this line a zero here is ambiguous between "nothing was
			// out of reach" and "everything that was, has been settled" — and
			// the second is the state this command's own remedy produces.
			fmt.Fprintf(w, "  %s\n", style.Dim.Render(fmt.Sprintf(
				"%d further branch(es) are held by a superseded marker and are not listed: gt patrol branches --superseded", settled)))
		}
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

// writePatrolBranchesSuperseded lists the branches a marker has settled, with
// the derivation each was settled by.
//
// It exists so that suppression is auditable. A marker that quietly removes
// rows and cannot be reviewed is the same failure as a sweep that cannot
// distinguish settled from stranded, arriving from the other side: the reader
// no longer sees the branch AND no longer sees the reasoning, so a wrong
// settlement becomes permanent. This is the view that makes one reversible.
func writePatrolBranchesSuperseded(w io.Writer, rigName string, result *witness.BranchSweepResult) error {
	if !writeBranchSweepHeader(w, rigName, result) {
		return nil
	}

	var settled, stale []witness.BranchSweepFinding
	for _, f := range result.Findings {
		switch {
		case f.Class == witness.BranchSweepSuperseded:
			settled = append(settled, f)
		case f.SupersededStale:
			stale = append(stale, f)
		}
	}

	if len(settled) == 0 && len(stale) == 0 {
		fmt.Fprintf(w, "%s no branch on %s carries a superseded marker — of %d scanned, none has been settled.\n",
			style.SuccessPrefix, result.Remote, result.Scanned)
		fmt.Fprintf(w, "  %s\n", style.Dim.Render("gt patrol branches mark <branch> -m \"why\"   # record one"))
		return nil
	}

	if len(settled) > 0 {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "BRANCH\tWAS\tSETTLED AT\tBY\tWHEN\tREASON")
		for _, f := range settled {
			mark := f.Superseded
			if mark == nil {
				mark = &git.SupersededMark{}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				f.Branch,
				orDash(string(f.UnderlyingClass)),
				orDash(shortCommit(mark.Commit)),
				orDash(mark.MarkedBy),
				orDash(mark.MarkedAt),
				orDash(mark.Reason),
			)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintln(w)
	}

	if len(stale) > 0 {
		fmt.Fprintf(w, "%s %d marker(s) NO LONGER APPLY — the branch was pushed to after it was settled:\n",
			style.Warning.Render("⚠"), len(stale))
		for _, f := range stale {
			fmt.Fprintf(w, "    %s  (now %s)\n", f.Branch, f.Class)
		}
		fmt.Fprintf(w, "  These are on the short list, not off it. Re-settle one only after checking what\n")
		fmt.Fprintf(w, "  arrived on it:  gt patrol branches mark <branch> -m \"...\" --force\n\n")
	}

	fmt.Fprintf(w, "%s  %s\n", style.Bold.Render("Summary:"), branchSweepSummary(result))
	fmt.Fprintf(w, "  %s\n", style.Dim.Render("Markers live at "+git.SupersededRefPrefix+"<branch> in the rig repository."))
	fmt.Fprintf(w, "  %s\n", style.Dim.Render("gt patrol branches unmark <branch>   # if a settlement was wrong"))
	return nil
}

// shortCommit abbreviates a SHA for a table column without hiding that a
// missing one is missing.
func shortCommit(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
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
		witness.BranchSweepSuperseded,
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
	if stale := result.StaleMarkCount(); stale > 0 {
		// Said in the summary rather than only on the row, because a stale
		// marker is the one state in which the feature is actively NOT doing
		// what its owner believes it is doing.
		parts = append(parts, fmt.Sprintf("%d marker(s) STALE", stale))
	}
	if !result.MRsMeasured {
		parts = append(parts, "MR column UNMEASURED")
	}
	if !result.MarksMeasured {
		parts = append(parts, "superseded markers UNMEASURED")
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
	case witness.BranchSweepSuperseded:
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
