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
	Short: "Report work beads left open although their work is merged",
	Long: `Report work beads that are still open although their work has landed.

For every non-terminal work bead in the rig, searches the target branch for
commits whose subject names it. A bead with landed commits and an open status
is a ledger inconsistency: the merge happened, the close did not.

Hooked beads are reported separately. Their own in-flight commits live on the
polecat's branch, so a hooked bead with commits already on the target is either
a second round of work or a polecat re-dispatched onto a merged fix.

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

// reconcileReport is the full result of one sweep. Scope is reported alongside
// the findings: a reader cannot tell a clean rig from an empty scan otherwise.
type reconcileReport struct {
	Rig     string `json:"rig"`
	Ref     string `json:"ref"`
	Fetched bool   `json:"fetched"`
	Scanned int    `json:"scanned"`
	Skipped int    `json:"skipped"`
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
		MissedCloses: []reconcileFinding{},
		InFlight:     []reconcileFinding{},
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
		if reconcileSkipReason(issue) != "" {
			report.Skipped++
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

	sortReconcileFindings(report.MissedCloses)
	sortReconcileFindings(report.InFlight)
	return printReconcileReport(report)
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
	fmt.Printf("Ledger reconcile: %s vs %s — %d beads scanned, %d skipped\n",
		report.Rig, report.Ref, report.Scanned, report.Skipped)
	if len(report.Unsearchable) > 0 {
		fmt.Printf("⚠ %d bead(s) could not be searched and are NOT covered by this result: %s\n",
			len(report.Unsearchable), strings.Join(report.Unsearchable, ", "))
	}

	if len(report.MissedCloses) == 0 && len(report.InFlight) == 0 {
		fmt.Println("No beads found with work on the branch and no close.")
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
	return nil
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
