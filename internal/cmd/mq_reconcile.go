package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
)

// Ledger reconciliation for the merge queue (gt-602).
//
// The refinery closes a work bead as part of landing its merge
// (internal/refinery/work_bead_close.go), but that close is a separate step
// from the merge itself and everything that can bypass it — a hand-landed
// merge, an unresolvable work bead, a bd write that failed — leaves the bead
// open with its work already on the target branch. Measured on gastown
// 2026-08-18: 67 beads named in origin/main commits over seven days, 66
// closed, 1 (gt-2uqy) still open.
//
// That is invisible in every other surface. A stranded MR sits in the queue
// looking wrong; a merged-but-open bead looks exactly like ordinary pending
// work and is re-dispatched to a polecat who finds the fix already in main.
// This command is the sweep that surfaces them.
//
// The MR record has the same failure with the opposite symptom (gt-3jx0). The
// whole completion path — closing the MR, closing the source issue, clearing
// the polecat's active_mr, deleting the remote branch — lives behind one call
// (`gt mq post-merge`, refinery.Manager.postMergeMR). Nothing re-runs it. When
// it does not run, and it does not run whenever the merge was landed by hand or
// the refinery session died between the push and the cleanup, the merge is on
// the target while the MR is still open. Measured on the beads rig 2026-08-18:
// bd-wisp-6td's branch merged as be2b70ed0 at 00:09:13Z, the refinery's
// post-merge call never executed, and the MR sat open for 24h until an age
// sweep closed it — which clears the record without running any of the rest.
// After that close the MR looks exactly like every properly merged one, so the
// window in which this is detectable is the window before the sweep.
//
// `gt mq list --verify` cannot report it: a merged branch is no longer ahead of
// its target, so it verifies as EMPTY, and EMPTY's documented disposition is
// rejection — which reopens the source issue and nudges the polecat to resubmit
// merged work.
//
// It reports and does not close. A commit naming a bead is evidence that
// someone touched the bead's subject, not proof the bead is finished:
// gt-2uqy's landed commit ("reuse StateDone polecats, not just StateIdle")
// is a partial fix for a bead whose body describes a wider defect. Auto-closing
// on that evidence would produce exactly the false completion the ledger
// guards in done_ledger.go exist to prevent.

var (
	mqReconcileJSON    bool
	mqReconcileTarget  string
	mqReconcileNoFetch bool
	mqReconcileLimit   int
)

var mqReconcileCmd = &cobra.Command{
	Use:   "reconcile [rig]",
	Short: "Report work beads and MRs left open although their work is merged",
	Long: `Report work beads and merge requests still open although their work has landed.

For every non-terminal work bead in the rig, searches the target branch for
commits whose subject names it. A bead with landed commits and an open status
is a ledger inconsistency: the merge happened, the close did not.

Open MRs are swept the same way. An MR whose recorded commit is already in the
target is a merge whose post-merge completion never ran: the MR record, the
source issue, the polecat's active_mr and the remote branch were all left as
they were. Nothing re-runs that path, and the 24h wisp sweep eventually closes
such an MR by age alone — which clears the record without doing any of it. The
remedy is named per finding: gt mq post-merge <rig> <mr-id>.

Do not reject one. "gt mq list --verify" reports a merged branch as EMPTY,
because a merged branch is no longer ahead of its target, and rejecting reopens
the source issue and nudges the polecat to resubmit work that is already in.

Hooked beads are reported separately. Their own in-flight commits live on the
polecat's branch, so a hooked bead with commits already on the target is either
a second round of work or a polecat re-dispatched onto a merged fix.

Beads excluded from the search are named alongside the reason they were
excluded. The exclusions are correct, but a benign one and a bead wrongly
labelled no_merge produce the same count, and the second is a missed close
hiding inside it.

The target branch is fetched first. A commit that exists on the remote but not
in this clone is invisible to the search and reads exactly like work that was
never done, so a report built on a stale clone is worse than no report.

This command reports only. A commit naming a bead can be a partial fix by
another bead's worker, so closing is a judgment call left to the reader:

  bd show <id>                     # read the bead against the listed commits
  bd close <id> --reason "Merged in <sha>"`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMQReconcile,
}

func init() {
	mqReconcileCmd.Flags().BoolVar(&mqReconcileJSON, "json", false, "Output JSON")
	mqReconcileCmd.Flags().StringVar(&mqReconcileTarget, "target", "", "Target branch to measure against (default: the rig's default branch)")
	mqReconcileCmd.Flags().BoolVar(&mqReconcileNoFetch, "no-fetch", false, "Skip the fetch and measure against the clone as-is (results may be stale)")
	mqReconcileCmd.Flags().IntVar(&mqReconcileLimit, "commit-limit", 5, "Maximum commits to report per bead")
	mqCmd.AddCommand(mqReconcileCmd)
}

// reconcileFinding is one bead whose work is on the target branch while its
// bead is not closed.
type reconcileFinding struct {
	IssueID string          `json:"issue_id"`
	Status  string          `json:"status"`
	Title   string          `json:"title"`
	Commits []git.CommitRef `json:"commits"`
}

// reconcileSkip is one bead the sweep excluded before searching the branch,
// paired with the exclusion that fired.
type reconcileSkip struct {
	IssueID string `json:"issue_id"`
	Reason  string `json:"reason"`
}

// mrLandedVerdict is what the MR sweep concluded about one open MR.
type mrLandedVerdict string

const (
	// mrLandedNone means nothing says this MR's work is on the target. An MR
	// carrying commits the target does not have is ordinary pending work.
	mrLandedNone mrLandedVerdict = ""
	// mrLandedConfirmed means both independent checks agree: the commit the MR
	// recorded at submission is contained in the target, and the target carries
	// at least one commit whose subject names the source issue.
	mrLandedConfirmed mrLandedVerdict = "landed"
	// mrLandedUnconfirmed means exactly one check fired. Reported, never merged
	// into the confirmed list: each single check has a benign reading that looks
	// identical (see classifyLandedMR).
	mrLandedUnconfirmed mrLandedVerdict = "unconfirmed"
)

// mrLandedFinding is one open MR whose work is on the target branch while the
// MR itself was never completed.
type mrLandedFinding struct {
	MRID        string          `json:"mr_id"`
	Verdict     mrLandedVerdict `json:"verdict"`
	Branch      string          `json:"branch"`
	Target      string          `json:"target"`
	SourceIssue string          `json:"source_issue"`
	Worker      string          `json:"worker,omitempty"`
	CommitSHA   string          `json:"commit_sha,omitempty"`
	// CommitLanded is whether CommitSHA is contained in the target ref. Nil when
	// the MR records no commit, or git could not answer for it — a nil here and
	// a false are different claims and are not collapsed.
	CommitLanded *bool `json:"commit_landed,omitempty"`
	// Commits are target commits whose subject names the source issue: the
	// work's own commit, and the merge subject of the branch that landed it.
	Commits []git.CommitRef `json:"commits"`
	// Evidence states in one line what the verdict rests on, including what was
	// missing when it is unconfirmed.
	Evidence string `json:"evidence"`
}

// reconcileReport is the full result of one sweep. Scope is reported alongside
// the findings: a reader cannot tell a clean rig from an empty scan otherwise.
type reconcileReport struct {
	Rig     string `json:"rig"`
	Ref     string `json:"ref"`
	Fetched bool   `json:"fetched"`
	Scanned int    `json:"scanned"`
	Skipped int    `json:"skipped"`
	// SkippedBeads names every bead behind the Skipped count. The exclusions
	// themselves are correct, but a benign one and a bead wrongly labelled
	// no_merge produce the same increment, and the second hides exactly the
	// missed close this command exists to find. Same reasoning as Unsearchable
	// below: an unattributable count is indistinguishable from a bead that was
	// searched and found clean.
	SkippedBeads []reconcileSkip `json:"skipped_beads"`
	// Unsearchable are beads whose ID cannot be turned into a commit search.
	// Reported rather than dropped: a bead the sweep could not examine is not
	// a bead the sweep found clean.
	Unsearchable []string `json:"unsearchable,omitempty"`
	// MissedCloses are beads nobody is working: their merge landed and their
	// close did not. This is the gt-602 defect.
	MissedCloses []reconcileFinding `json:"missed_closes"`
	// InFlight are hooked beads with landed work. Not necessarily wrong — a
	// bead re-slung for a second round legitimately has earlier commits on the
	// branch — but it is also what a re-dispatch onto already-merged work looks
	// like, which burns a polecat session on a fix that is already in main.
	InFlight []reconcileFinding `json:"in_flight"`
	// ScannedMRs and SkippedMRs scope the MR half of the sweep, for the same
	// reason the bead half reports its own: a clean rig and an MR listing that
	// returned nothing print the same zero otherwise.
	ScannedMRs int             `json:"scanned_mrs"`
	SkippedMRs []reconcileSkip `json:"skipped_mrs"`
	// LandedMRs are open MRs whose work is already on the target: the merge
	// happened, the post-merge completion did not (gt-3jx0).
	LandedMRs []mrLandedFinding `json:"landed_mrs"`
}

// reconcileStatuses are the non-terminal statuses a merged bead can be left in.
//
// hooked is included: a hooked bead's own in-flight commits live on the
// polecat's branch, not on the target, so a hooked bead with commits on the
// target has had work land already. pinned is excluded because pinned beads
// are permanent reference and must never be closed; deferred is excluded
// because deferring is a deliberate decision to leave the bead open.
var reconcileStatuses = []beads.IssueStatus{
	beads.StatusOpen,
	beads.StatusInProgress,
	beads.StatusBlocked,
	beads.IssueStatusHooked,
}

// reconcileSkipReason returns why a bead must not be reported as a merged-but-
// open candidate, or "" when it should be checked against the branch.
func reconcileSkipReason(issue *beads.Issue) string {
	if reason := beads.ConcreteWorkIssueRejectReason(issue); reason != "" {
		return reason
	}
	if beads.IssueStatus(strings.TrimSpace(issue.Status)).IsTerminal() {
		return "terminal"
	}
	// Beads that are never expected to produce a merge cannot be behind one.
	// Same exclusions the refinery's post-merge close applies.
	if fields := beads.ParseAttachmentFields(issue); fields != nil {
		switch {
		case fields.NoMerge:
			return "no_merge"
		case fields.ReviewOnly:
			return "review_only"
		case strings.EqualFold(strings.TrimSpace(fields.MergeStrategy), "local"):
			return "merge_strategy:local"
		}
	}
	return ""
}

// mrLandedGit is the git surface the MR sweep needs, named as an interface so
// the classification can be tested without a repository.
type mrLandedGit interface {
	IsAncestor(ancestor, descendant string) (bool, error)
	CommitsWithSubjectToken(ref, token string, limit int) ([]git.CommitRef, error)
}

// classifyLandedMR decides what an open MR's two pieces of evidence mean.
//
// Two checks are required to confirm, because each one alone has a benign
// reading that produces identical output:
//
//   - Containment alone. An MR whose branch never carried a commit records the
//     base commit it branched from, and the target contains that commit by
//     definition. So "the recorded commit is in the target" is equally the
//     signature of a merged MR and of an empty one (gt-d5u), and the two want
//     opposite actions: post-merge for the first, reject for the second.
//   - A naming commit alone. The bead sweep's own caveat applies: a commit
//     naming the issue can be a partial fix landed by someone else's branch, or
//     an earlier round of this bead's work.
//
// A false containment answer is decisive on its own: an MR carrying commits the
// target does not have is pending work, whatever else names the issue there.
// Reporting it as landed would invite a post-merge that closes the bead and
// deletes the branch out from under unmerged commits.
func classifyLandedMR(commitSHA string, commitLanded *bool, commits []git.CommitRef) (mrLandedVerdict, string) {
	named := len(commits)
	switch {
	case commitLanded != nil && !*commitLanded:
		// Pending work. Say so even when commits name the issue: that is the
		// second-round shape, not a missed completion.
		if named > 0 {
			return mrLandedNone, fmt.Sprintf("commit %s is not in the target, though %d commit(s) there name the issue — a later round of the same bead", shortReconcileSHA(commitSHA), named)
		}
		return mrLandedNone, ""
	case commitLanded != nil && named > 0:
		return mrLandedConfirmed, fmt.Sprintf("commit %s is in the target and %d commit(s) there name the issue", shortReconcileSHA(commitSHA), named)
	case commitLanded != nil:
		return mrLandedUnconfirmed, fmt.Sprintf("commit %s is in the target, but no commit there names the issue — a branch that never carried work reads exactly the same way", shortReconcileSHA(commitSHA))
	case named > 0:
		return mrLandedUnconfirmed, fmt.Sprintf("%d commit(s) on the target name the issue, but the MR records no commit_sha to check containment against", named)
	default:
		return mrLandedNone, ""
	}
}

// sweepLandedMRs classifies every open MR against the target branch. The error
// return is reserved for a git failure, which invalidates the sweep; a single
// MR that cannot be examined is reported as a skip, never dropped.
func sweepLandedMRs(mrs []*beads.Issue, rigName, defaultTarget string, g mrLandedGit, limit int) ([]mrLandedFinding, []reconcileSkip, int, error) {
	findings := []mrLandedFinding{}
	skips := []reconcileSkip{}
	scanned := 0

	for _, mr := range mrs {
		if mr == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(mr.Status), string(beads.StatusOpen)) {
			continue
		}
		fields := beads.ParseMRFields(mr)
		if fields == nil {
			skips = append(skips, reconcileSkip{IssueID: mr.ID, Reason: "unparseable-mr"})
			continue
		}
		// Wisps are shared across every rig on the Dolt server, so a listing for
		// one rig can carry another's MRs (mq_list.go applies the same filter).
		if fields.Rig != "" && !strings.EqualFold(fields.Rig, rigName) {
			continue
		}
		sourceIssue := strings.TrimSpace(fields.SourceIssue)
		if sourceIssue == "" {
			skips = append(skips, reconcileSkip{IssueID: mr.ID, Reason: "no-source-issue"})
			continue
		}
		if !git.SupportedCommitToken(sourceIssue) {
			skips = append(skips, reconcileSkip{IssueID: mr.ID, Reason: "unsearchable-source-issue"})
			continue
		}

		target := strings.TrimSpace(fields.Target)
		if target == "" {
			target = defaultTarget
		}
		ref := "origin/" + target
		scanned++

		commits, err := g.CommitsWithSubjectToken(ref, sourceIssue, limit)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("searching %s for %s (MR %s): %w", ref, sourceIssue, mr.ID, err)
		}

		// A commit the clone cannot resolve is an unanswered question, not a
		// "no": an unfetched branch tip and a genuinely unmerged one both fail
		// here. Nil records that, and classifyLandedMR treats it as unknown.
		var landed *bool
		if sha := strings.TrimSpace(fields.CommitSHA); sha != "" {
			if contained, ancErr := g.IsAncestor(sha, ref); ancErr == nil {
				landed = &contained
			}
		}

		verdict, evidence := classifyLandedMR(fields.CommitSHA, landed, commits)
		if verdict == mrLandedNone {
			continue
		}
		findings = append(findings, mrLandedFinding{
			MRID:         mr.ID,
			Verdict:      verdict,
			Branch:       fields.Branch,
			Target:       target,
			SourceIssue:  sourceIssue,
			Worker:       fields.Worker,
			CommitSHA:    fields.CommitSHA,
			CommitLanded: landed,
			Commits:      commits,
			Evidence:     evidence,
		})
	}

	return findings, skips, scanned, nil
}

func shortReconcileSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 8 {
		return sha[:8]
	}
	if sha == "" {
		return "(none)"
	}
	return sha
}

func runMQReconcile(_ *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}
	_, r, rigName, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	target := strings.TrimSpace(mqReconcileTarget)
	if target == "" {
		target = r.DefaultBranch()
	}

	rigGit, err := getRigGit(r.Path)
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}

	// Empty slices, not nil: a JSON consumer reading `.missed_closes[]` should
	// see an empty list on a clean rig, not null.
	report := reconcileReport{
		Rig:          rigName,
		Ref:          "origin/" + target,
		SkippedBeads: []reconcileSkip{},
		MissedCloses: []reconcileFinding{},
		InFlight:     []reconcileFinding{},
		SkippedMRs:   []reconcileSkip{},
		LandedMRs:    []mrLandedFinding{},
	}
	if !mqReconcileNoFetch {
		if err := rigGit.FetchBranch("origin", target); err != nil {
			return fmt.Errorf("reconcile: fetching origin/%s: %w\n"+
				"A sweep over a stale clone reports merged work as missing. "+
				"Fix the fetch, or pass --no-fetch to accept that risk explicitly", target, err)
		}
		report.Fetched = true
	}

	bd := beads.New(r.BeadsPath())
	issues, err := bd.ListIssueStatuses(reconcileStatuses...)
	if err != nil {
		return fmt.Errorf("reconcile: listing open beads: %w", err)
	}

	seen := make(map[string]bool, len(issues))
	for _, issue := range issues {
		if issue == nil || seen[issue.ID] {
			continue
		}
		seen[issue.ID] = true
		if reason := reconcileSkipReason(issue); reason != "" {
			report.SkippedBeads = append(report.SkippedBeads, reconcileSkip{IssueID: issue.ID, Reason: reason})
			continue
		}
		// One malformed ID must not abort the sweep, but it must not vanish
		// from it either: an unreported skip is indistinguishable from a bead
		// that was searched and found clean.
		if !git.SupportedCommitToken(issue.ID) {
			report.Unsearchable = append(report.Unsearchable, issue.ID)
			continue
		}
		report.Scanned++

		commits, err := rigGit.CommitsWithSubjectToken(report.Ref, issue.ID, mqReconcileLimit)
		if err != nil {
			return fmt.Errorf("reconcile: searching %s for %s: %w", report.Ref, issue.ID, err)
		}
		if len(commits) == 0 {
			continue
		}
		finding := reconcileFinding{
			IssueID: issue.ID,
			Status:  issue.Status,
			Title:   issue.Title,
			Commits: commits,
		}
		if beads.IssueStatus(strings.TrimSpace(issue.Status)) == beads.IssueStatusHooked {
			report.InFlight = append(report.InFlight, finding)
		} else {
			report.MissedCloses = append(report.MissedCloses, finding)
		}
	}

	// The MR half. Open MRs only: a closed MR has either been completed or been
	// rejected, and this sweep is about the completion that never ran.
	mrs, err := bd.ListMergeRequests(beads.ListOptions{
		Label:    "gt:merge-request",
		Priority: -1,
		Rig:      rigName,
		Status:   string(beads.StatusOpen),
	})
	if err != nil {
		return fmt.Errorf("reconcile: listing open MRs: %w", err)
	}
	landed, mrSkips, scannedMRs, err := sweepLandedMRs(mrs, rigName, target, rigGit, mqReconcileLimit)
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	report.LandedMRs = landed
	report.SkippedMRs = mrSkips
	report.ScannedMRs = scannedMRs

	report.finalize()
	return printReconcileReport(report)
}

// finalize derives the counts a reader sees from the lists behind them and puts
// every list in a stable order. Skipped is derived rather than incremented
// alongside the append: two independently maintained tallies of the same thing
// drift, and the count is the number an operator reads first.
func (report *reconcileReport) finalize() {
	report.Skipped = len(report.SkippedBeads)
	sort.Slice(report.SkippedBeads, func(i, j int) bool {
		return report.SkippedBeads[i].IssueID < report.SkippedBeads[j].IssueID
	})
	sortReconcileFindings(report.MissedCloses)
	sortReconcileFindings(report.InFlight)
	sort.Slice(report.SkippedMRs, func(i, j int) bool {
		return report.SkippedMRs[i].IssueID < report.SkippedMRs[j].IssueID
	})
	// Confirmed findings first: an unconfirmed one needs a human check before it
	// can be acted on, and burying the actionable rows under it inverts the read.
	sort.SliceStable(report.LandedMRs, func(i, j int) bool {
		left, right := report.LandedMRs[i], report.LandedMRs[j]
		if (left.Verdict == mrLandedConfirmed) != (right.Verdict == mrLandedConfirmed) {
			return left.Verdict == mrLandedConfirmed
		}
		return left.MRID < right.MRID
	})
}

func sortReconcileFindings(findings []reconcileFinding) {
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].IssueID < findings[j].IssueID
	})
}

func printReconcileReport(report reconcileReport) error {
	if mqReconcileJSON {
		data, err := json.Marshal(report)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	if !report.Fetched {
		fmt.Printf("⚠ --no-fetch: measured against this clone as-is; commits pushed since the last fetch are invisible\n")
	}
	fmt.Printf("Ledger reconcile: %s vs %s — %d beads scanned, %d skipped; %d open MR(s) scanned, %d skipped\n",
		report.Rig, report.Ref, report.Scanned, report.Skipped, report.ScannedMRs, len(report.SkippedMRs))
	if len(report.Unsearchable) > 0 {
		fmt.Printf("⚠ %d bead(s) could not be searched and are NOT covered by this result: %s\n",
			len(report.Unsearchable), strings.Join(report.Unsearchable, ", "))
	}
	printReconcileSkips(report.SkippedBeads)
	printReconcileMRSkips(report.SkippedMRs)

	if len(report.MissedCloses) == 0 && len(report.InFlight) == 0 && len(report.LandedMRs) == 0 {
		fmt.Println("No beads or MRs found with work on the branch and no close.")
		return nil
	}

	if len(report.MissedCloses) > 0 {
		fmt.Printf("\n%d bead(s) still open with work on %s:\n", len(report.MissedCloses), report.Ref)
		printReconcileFindings(report.MissedCloses)
		fmt.Printf("\nA commit naming a bead is not proof the bead is finished — read each one\n" +
			"before closing: bd show <id>, then bd close <id> --reason \"Merged in <sha>\".\n")
	}

	if len(report.InFlight) > 0 {
		fmt.Printf("\n%d hooked bead(s) already have work on %s:\n", len(report.InFlight), report.Ref)
		printReconcileFindings(report.InFlight)
		fmt.Printf("\nThese are dispatched to a live polecat. A second round of work on the same\n" +
			"bead looks like this too, so check the bead against the commits before acting —\n" +
			"if the landed commit is the whole fix, the polecat is redoing merged work.\n")
	}

	printLandedMRs(report)
	return nil
}

// printLandedMRs reports open MRs whose work is on the target. The remedy is
// named per finding rather than described once, because the wrong remedy —
// rejecting an MR that reads EMPTY in the queue — is the one an operator
// reaches for and it reopens the source issue (gt-3jx0).
func printLandedMRs(report reconcileReport) {
	if len(report.LandedMRs) == 0 {
		return
	}

	confirmed := 0
	for _, finding := range report.LandedMRs {
		if finding.Verdict == mrLandedConfirmed {
			confirmed++
		}
	}

	fmt.Printf("\n%d open MR(s) whose work is already on %s:\n", len(report.LandedMRs), report.Ref)
	for _, finding := range report.LandedMRs {
		label := "MERGED, completion never ran"
		if finding.Verdict != mrLandedConfirmed {
			label = "UNCONFIRMED — check before acting"
		}
		fmt.Printf("\n  %s [%s] %s\n", finding.MRID, label, finding.Branch)
		fmt.Printf("    source %s → %s", finding.SourceIssue, finding.Target)
		if finding.Worker != "" {
			fmt.Printf("   worker %s", finding.Worker)
		}
		fmt.Println()
		if finding.Evidence != "" {
			fmt.Printf("    %s\n", finding.Evidence)
		}
		for _, commit := range finding.Commits {
			sha := commit.SHA
			if len(sha) > 8 {
				sha = sha[:8]
			}
			fmt.Printf("    %s  %s  %s\n", sha, commit.Date, commit.Subject)
		}
		if finding.Verdict == mrLandedConfirmed {
			fmt.Printf("    gt mq post-merge %s %s\n", report.Rig, finding.MRID)
		}
	}

	fmt.Printf("\nPost-merge is the whole completion path — it closes the MR, closes the source\n" +
		"issue, clears the polecat's active_mr and deletes the remote branch. Nothing\n" +
		"else runs it, and the 24h wisp sweep closes these by age alone, which clears\n" +
		"the record without doing any of the rest. Add --skip-branch-delete to keep the\n" +
		"branch.\n")
	fmt.Printf("Do not reject one: a merged branch is no longer ahead of its target, so it\n" +
		"verifies as EMPTY in gt mq list --verify, and rejecting reopens the source issue\n" +
		"and nudges the polecat to resubmit work that is already in.\n")
	if confirmed < len(report.LandedMRs) {
		fmt.Printf("\nUNCONFIRMED means one of the two checks fired, not both. Containment alone is\n" +
			"also what an MR whose branch never carried a commit looks like — its recorded\n" +
			"commit is the base the target already had. Read the branch before post-merging:\n" +
			"git log --oneline <target>..<branch>\n")
	}
}

// printReconcileMRSkips names the MRs the sweep could not examine, for the same
// reason the bead half names its skips: an MR that was never checked and one
// that was checked and found clean are otherwise the same absence.
func printReconcileMRSkips(skips []reconcileSkip) {
	if len(skips) == 0 {
		return
	}
	byReason := make(map[string][]string, len(skips))
	for _, skip := range skips {
		byReason[skip.Reason] = append(byReason[skip.Reason], skip.IssueID)
	}
	reasons := make([]string, 0, len(byReason))
	for reason := range byReason {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)

	fmt.Printf("%d open MR(s) could not be examined:\n", len(skips))
	for _, reason := range reasons {
		ids := byReason[reason]
		sort.Strings(ids)
		fmt.Printf("  %s: %s\n", reason, strings.Join(ids, ", "))
	}
}

// printReconcileSkips names the beads behind the skipped count, grouped by the
// exclusion that fired. Every reason here is a correct exclusion, so this is
// not a warning — but the reader has to be able to check that, and a bare "1
// skipped" on a report that runs every patrol cycle trains its reader to skip
// the line.
func printReconcileSkips(skips []reconcileSkip) {
	if len(skips) == 0 {
		return
	}
	byReason := make(map[string][]string, len(skips))
	for _, skip := range skips {
		byReason[skip.Reason] = append(byReason[skip.Reason], skip.IssueID)
	}
	reasons := make([]string, 0, len(byReason))
	for reason := range byReason {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)

	fmt.Printf("%d bead(s) skipped before the branch search:\n", len(skips))
	for _, reason := range reasons {
		ids := byReason[reason]
		sort.Strings(ids)
		fmt.Printf("  %s: %s\n", reason, strings.Join(ids, ", "))
	}
}

func printReconcileFindings(findings []reconcileFinding) {
	for _, finding := range findings {
		fmt.Printf("\n  %s [%s] %s\n", finding.IssueID, finding.Status, truncateReconcileTitle(finding.Title))
		for _, commit := range finding.Commits {
			sha := commit.SHA
			if len(sha) > 8 {
				sha = sha[:8]
			}
			fmt.Printf("    %s  %s  %s\n", sha, commit.Date, commit.Subject)
		}
	}
}

func truncateReconcileTitle(title string) string {
	title = strings.TrimSpace(title)
	const max = 80
	if len(title) <= max {
		return title
	}
	return title[:max-1] + "…"
}
