package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/style"
)

func runMQList(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	_, r, _, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// Create beads wrapper for the rig - use BeadsPath() to get the git-synced location
	b := beads.New(r.BeadsPath())

	// --merge-check is strictly more work than --verify and needs everything
	// --verify resolves, so asking for it turns --verify on rather than failing
	// on a flag combination the operator had no reason to expect.
	if mqListMergeCheck {
		mqListVerify = true
	}

	// Create git client for branch verification when --verify is set
	var gitClient *git.Git
	if mqListVerify {
		// Use the refinery's rig worktree to check branches
		refineryRigPath := filepath.Join(r.Path, "refinery", "rig")
		gitClient = git.NewGit(refineryRigPath)
	}

	// Build list options - query for merge-request label.
	// Use ListMergeRequests to query both the issues table and wisps table,
	// since MRs are created as ephemeral (wisps) by gt mq submit (GH#2446).
	// Priority -1 means no priority filter (otherwise 0 would filter to P0 only).
	opts := beads.ListOptions{
		Label:    "gt:merge-request",
		Priority: -1,
		Rig:      rigName,
	}

	// Apply status filter if specified
	if mqListStatus != "" {
		opts.Status = mqListStatus
	} else if !mqListReady {
		// Default to open if not showing ready
		opts.Status = "open"
	}

	var issues []*beads.Issue

	if mqListReady {
		// Query all open MRs and filter out blocked ones manually.
		// Cannot use b.Ready() because it excludes ephemeral beads,
		// and MRs are ephemeral by design (see gt-t5t6y).
		opts.Status = "open"
		allOpen, err := b.ListMergeRequests(opts)
		if err != nil {
			return fmt.Errorf("querying ready MRs: %w", err)
		}
		for _, issue := range allOpen {
			if !isMergeRequestReadyForSelection(issue) {
				continue
			}
			issues = append(issues, issue)
		}
	} else {
		issues, err = b.ListMergeRequests(opts)
		if err != nil {
			return fmt.Errorf("querying merge queue: %w", err)
		}
	}

	// Apply additional filters and calculate scores
	now := time.Now()
	type scoredIssue struct {
		issue         *beads.Issue
		fields        *beads.MRFields
		score         float64
		branchState   mrBranchState // git verification result (when --verify is set)
		commitsAhead  int           // commits the branch carries over its target (valid for PRESENT/EMPTY)
		mergeState    mrMergeState  // merge rehearsal result (when --merge-check is set)
		conflictFiles int           // files that conflict (valid for CONFLICTS)
	}
	var scored []scoredIssue

	for _, issue := range issues {
		// Manual status filtering as workaround for bd list not respecting --status filter
		if mqListReady {
			// Ready view should only show open MRs
			if issue.Status != "open" {
				continue
			}
		} else if mqListStatus != "" && !strings.EqualFold(mqListStatus, "all") {
			// Explicit status filter should match exactly
			if !strings.EqualFold(issue.Status, mqListStatus) {
				continue
			}
		} else if mqListStatus == "" && issue.Status != "open" {
			// Default case (no status specified) should only show open
			continue
		}

		// Parse MR fields
		fields := beads.ParseMRFields(issue)

		// Filter by rig — wisps are shared across all rigs in the Dolt server,
		// so we must filter to only show MRs belonging to this rig.
		if fields != nil && fields.Rig != "" && !strings.EqualFold(fields.Rig, rigName) {
			continue
		}

		// Filter by worker
		if mqListWorker != "" {
			worker := ""
			if fields != nil {
				worker = fields.Worker
			}
			if !strings.EqualFold(worker, mqListWorker) {
				continue
			}
		}

		// Filter by epic (target branch)
		if mqListEpic != "" {
			target := ""
			if fields != nil {
				target = fields.Target
			}
			expectedTarget := resolveIntegrationBranchName(b, r.Path, mqListEpic)
			if target != expectedTarget {
				continue
			}
		}

		// Check the branch if --verify is set: does it exist (local + remote-tracking
		// refs), and does it actually carry commits over its target? Neither
		// question is "would it merge" — that is --merge-check, below.
		verification := verifyBranchRefs(mqListVerify, gitClient, fields)
		mergeState, conflictFiles := rehearseMerge(mqListMergeCheck, gitClient, verification)

		// Calculate priority score
		score := calculateMRScore(issue, fields, now)
		scored = append(scored, scoredIssue{
			issue:         issue,
			fields:        fields,
			score:         score,
			branchState:   verification.state,
			commitsAhead:  verification.commitsAhead,
			mergeState:    mergeState,
			conflictFiles: conflictFiles,
		})
	}

	// Sort by score descending (highest priority first)
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// Extract filtered issues for JSON output compatibility
	var filtered []*beads.Issue
	for _, s := range scored {
		filtered = append(filtered, s.issue)
	}

	// JSON output
	if mqListJSON {
		if mqListVerify {
			// Extend JSON with verification results
			type verifiedIssue struct {
				*beads.Issue
				BranchExists  *bool  `json:"branch_exists,omitempty"`
				BranchEmpty   *bool  `json:"branch_empty,omitempty"`
				CommitsAhead  *int   `json:"commits_ahead,omitempty"`
				GitState      string `json:"git_state,omitempty"`
				VerifyError   bool   `json:"verify_error,omitempty"`
				MergeState    string `json:"merge_state,omitempty"`
				MergeClean    *bool  `json:"merge_clean,omitempty"`
				ConflictFiles *int   `json:"conflict_files,omitempty"`
			}
			var verified []verifiedIssue
			for _, s := range scored {
				vi := verifiedIssue{Issue: s.issue}
				if s.fields != nil && s.fields.Branch != "" {
					vi.GitState = string(s.branchState)
					switch s.branchState {
					case mrBranchStateErr:
						vi.VerifyError = true
					case mrBranchStateMissing:
						exists := false
						vi.BranchExists = &exists
					case mrBranchStateEmpty, mrBranchStatePresent:
						exists := true
						empty := s.branchState == mrBranchStateEmpty
						ahead := s.commitsAhead
						vi.BranchExists = &exists
						vi.BranchEmpty = &empty
						vi.CommitsAhead = &ahead
					}
					// merge_clean is a pointer so "not rehearsed" is absent
					// rather than false — the whole defect this fixes was a
					// non-answer reading as a verdict (gt-0w2l).
					switch s.mergeState {
					case mrMergeStateClean:
						clean := true
						files := 0
						vi.MergeState = string(s.mergeState)
						vi.MergeClean = &clean
						vi.ConflictFiles = &files
					case mrMergeStateConflicts:
						clean := false
						files := s.conflictFiles
						vi.MergeState = string(s.mergeState)
						vi.MergeClean = &clean
						vi.ConflictFiles = &files
					case mrMergeStateErr:
						vi.MergeState = string(s.mergeState)
					}
				}
				verified = append(verified, vi)
			}
			return outputJSON(verified)
		}
		return outputJSON(filtered)
	}

	// Human-readable output.
	//
	// Name the store and the status scope. MR wisps live in the RIG's store, and
	// a listing that names neither renders a wrong-store or wrong-status zero
	// identically to a real one — an unfalsifiable empty result that reads as
	// data loss. Three of the four surfaces an operator might reach for return
	// exactly that; this one is the surface that works, so it must show its
	// work (gt-kb63).
	fmt.Printf("%s Merge queue for '%s':\n", style.Bold.Render("📋"), rigName)
	fmt.Printf("  %s\n\n", style.Dim.Render(fmt.Sprintf("store: %s   scope: %s",
		r.BeadsPath(), describeMQListScope(mqListReady, mqListStatus))))

	if len(filtered) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(empty)"))
		if mqListExcludesClosed(mqListReady, mqListStatus) {
			fmt.Printf("\n  %s\n", style.Dim.Render("Merged and rejected MRs are closed, and this scope excludes closed. To audit them:"))
			fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("    gt mq list %s --status closed", rigName)))
		}
		return nil
	}

	// Create styled table - add GIT/MERGE columns when the flags ask for them
	table := style.NewTable(buildMQListColumns(mqListVerify, mqListMergeCheck)...)

	// Add rows using scored items (already sorted by score)
	for _, item := range scored {
		issue := item.issue
		fields := item.fields

		// Determine display status
		displayStatus := issue.Status
		if issue.Status == "open" {
			if beads.HasUnresolvedBlockers(issue) {
				displayStatus = "blocked"
			} else {
				displayStatus = "ready"
			}
		}

		// Format status with styling
		styledStatus := displayStatus
		switch displayStatus {
		case "ready":
			styledStatus = style.Success.Render("ready")
		case "in_progress":
			styledStatus = style.Warning.Render("active")
		case "blocked":
			styledStatus = style.Dim.Render("blocked")
		case "closed":
			styledStatus = style.Dim.Render("closed")
		}

		// Get MR fields
		branch := ""
		target := ""
		convoyID := ""
		if fields != nil {
			branch = fields.Branch
			target = fields.Target
			convoyID = fields.ConvoyID
		}
		if target == "" {
			target = style.Dim.Render("(unset)")
		}

		// Format convoy column
		convoyDisplay := style.Dim.Render("(none)")
		if convoyID != "" {
			// Truncate convoy ID for display
			if len(convoyID) > 12 {
				convoyID = convoyID[:12]
			}
			convoyDisplay = convoyID
		}

		// Format priority with color
		priority := fmt.Sprintf("P%d", issue.Priority)
		if issue.Priority <= 1 {
			priority = style.Error.Render(priority)
		} else if issue.Priority == 2 {
			priority = style.Warning.Render(priority)
		}

		// Format score
		scoreStr := fmt.Sprintf("%.1f", item.score)

		// Format branch status when --verify is set
		gitStatus := ""
		if mqListVerify {
			switch item.branchState {
			case mrBranchStateErr:
				gitStatus = style.Warning.Render("ERR")
			case mrBranchStateMissing:
				gitStatus = style.Error.Render("MISSING")
			case mrBranchStateEmpty:
				// The branch exists but carries no commits over its target:
				// merging it is a no-op. Distinct from PRESENT because the
				// correct disposition is rejection, not merge (gt-d5u).
				gitStatus = style.Error.Render("EMPTY")
			case mrBranchStatePresent:
				// Deliberately NOT styled Success, and deliberately not called
				// OK: this cell says the branch is reachable and non-empty, and
				// a green OK next to a green "ready" reads as merge clearance
				// it cannot give (gt-0w2l). --merge-check fills the MERGE column
				// with the verdict this one does not carry.
				gitStatus = "PRESENT"
			}
		}

		// Format the merge rehearsal when --merge-check is set
		mergeStatus := ""
		if mqListMergeCheck {
			switch item.mergeState {
			case mrMergeStateClean:
				mergeStatus = style.Success.Render("CLEAN")
			case mrMergeStateConflicts:
				mergeStatus = style.Error.Render(fmt.Sprintf("CONFLICTS=%d", item.conflictFiles))
			case mrMergeStateErr:
				mergeStatus = style.Warning.Render("ERR")
			default:
				// Not rehearsed — the branch never reached PRESENT, so its GIT
				// cell already carries the disposition.
				mergeStatus = style.Dim.Render("-")
			}
		}

		// Calculate age
		age := formatMRAge(issue.CreatedAt)

		// Truncate ID if needed
		displayID := issue.ID
		if len(displayID) > 12 {
			displayID = displayID[:12]
		}

		// Build row with conditional GIT/MERGE columns
		row := []string{displayID, scoreStr, priority, convoyDisplay, branch, target, styledStatus}
		if mqListVerify {
			row = append(row, gitStatus)
		}
		if mqListMergeCheck {
			row = append(row, mergeStatus)
		}
		table.AddRow(append(row, style.Dim.Render(age))...)
	}

	fmt.Print(table.Render())

	// Show summary of missing/empty branches when --verify is set
	if mqListVerify {
		missingCount := 0
		emptyCount := 0
		presentCount := 0
		for _, item := range scored {
			switch item.branchState {
			case mrBranchStateMissing:
				missingCount++
			case mrBranchStateEmpty:
				emptyCount++
			case mrBranchStatePresent:
				presentCount++
			}
		}
		// Say what PRESENT does not mean, at the call site, every time it
		// appears. The states are individually accurate and the omission is what
		// misleads: PRESENT beside a "ready" status was read as clearance to
		// merge two branches that conflicted in 17 and 12 files (gt-0w2l).
		if presentCount > 0 && !mqListMergeCheck {
			fmt.Printf("\n  %s\n", style.Dim.Render(fmt.Sprintf(
				"PRESENT means reachable and non-empty. It is NOT a merge verdict — %d of these may still conflict.", presentCount)))
			fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf(
				"To find out, rehearse the merge: gt mq list %s --merge-check", rigName)))
		}
		if missingCount > 0 {
			fmt.Printf("\n  %s %d MR(s) with missing branches\n",
				style.Error.Render("⚠"),
				missingCount)
		}
		if emptyCount > 0 {
			fmt.Printf("\n  %s %d MR(s) whose branch carries no commits over its target\n",
				style.Error.Render("⚠"),
				emptyCount)
			fmt.Printf("  %s\n", style.Dim.Render("Merging one is a no-op that still closes the source issue — reject instead: gt mq reject <rig> <mr-id> --reason \"empty branch\" --notify"))
		}
	}

	// Show the merge rehearsal summary when --merge-check is set
	if mqListMergeCheck {
		conflicted := 0
		mergeErrs := 0
		for _, item := range scored {
			switch item.mergeState {
			case mrMergeStateConflicts:
				conflicted++
			case mrMergeStateErr:
				mergeErrs++
			}
		}
		if conflicted > 0 {
			fmt.Printf("\n  %s %d MR(s) whose branch does not merge cleanly into its target\n",
				style.Error.Render("⚠"),
				conflicted)
			fmt.Printf("  %s\n", style.Dim.Render("Rehearsed with git merge-tree against the same refs the queue merges. Rebase the branch or land it by cherry-pick — a plain merge will stop on conflicts."))
		}
		if mergeErrs > 0 {
			fmt.Printf("\n  %s %d MR(s) whose merge could not be rehearsed — treat as UNKNOWN, not as clean\n",
				style.Warning.Render("⚠"),
				mergeErrs)
		}
	}

	// Show blocking details below table
	for _, item := range scored {
		issue := item.issue
		displayStatus := issue.Status
		if issue.Status == "open" && beads.HasUnresolvedBlockers(issue) {
			displayStatus = "blocked"
		}
		if blockerID := beads.FirstUnresolvedBlockerID(issue); displayStatus == "blocked" && blockerID != "" {
			displayID := issue.ID
			if len(displayID) > 12 {
				displayID = displayID[:12]
			}
			fmt.Printf("  %s %s\n", style.Dim.Render(displayID+":"),
				style.Dim.Render(fmt.Sprintf("waiting on %s", blockerID)))
		}
	}

	return nil
}

// describeMQListScope names the status scope a listing actually queried, for
// display alongside the store. "open (default)" is spelled out because the
// default is the one an operator did not choose and so the one they are most
// likely to mistake for "everything" (gt-kb63).
func describeMQListScope(ready bool, status string) string {
	if ready {
		return "ready (open, unblocked)"
	}
	switch {
	case status == "":
		return "status=open (default — closed MRs not shown)"
	case strings.EqualFold(status, "all"):
		return "status=all"
	default:
		return "status=" + strings.ToLower(status)
	}
}

// mqListExcludesClosed reports whether the requested scope hides closed MRs.
// Merged and rejected MRs are closed, so an empty result under such a scope is
// not evidence that they never existed.
func mqListExcludesClosed(ready bool, status string) bool {
	if ready {
		return true
	}
	if status == "" {
		return true
	}
	return !strings.EqualFold(status, "all") && !strings.EqualFold(status, "closed")
}

// formatMRAge formats the age of an MR from its created_at timestamp.
func formatMRAge(createdAt string) string {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		// Try other formats
		t, err = time.Parse("2006-01-02T15:04:05Z", createdAt)
		if err != nil {
			return "?"
		}
	}

	d := time.Since(t)

	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// outputJSON outputs data as JSON.
func outputJSON(data interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

func buildMQListColumns(verify, mergeCheck bool) []style.Column {
	columns := []style.Column{
		{Name: "ID", Width: 12},
		{Name: "SCORE", Width: 7, Align: style.AlignRight},
		{Name: "PRI", Width: 4},
		{Name: "CONVOY", Width: 12},
		{Name: "BRANCH", Width: 24},
		{Name: "TARGET", Width: 24},
		{Name: "STATUS", Width: 10},
	}
	if verify {
		// Wide enough for PRESENT and MISSING. The header stays GIT because the
		// values now say what they mean on their own (gt-0w2l).
		columns = append(columns, style.Column{Name: "GIT", Width: 8})
	}
	if mergeCheck {
		// Wide enough for CONFLICTS=nnn.
		columns = append(columns, style.Column{Name: "MERGE", Width: 14})
	}
	return append(columns, style.Column{Name: "AGE", Width: 6, Align: style.AlignRight})
}

// calculateMRScore computes the priority score for an MR using the refinery scoring function.
// Higher scores mean higher priority (process first).
func calculateMRScore(issue *beads.Issue, fields *beads.MRFields, now time.Time) float64 {
	// Parse MR creation time
	mrCreatedAt, err := time.Parse(time.RFC3339, issue.CreatedAt)
	if err != nil {
		mrCreatedAt, err = time.Parse("2006-01-02T15:04:05Z", issue.CreatedAt)
		if err != nil {
			mrCreatedAt = now // Fallback to now if parsing fails
		}
	}

	// Build score input
	input := refinery.ScoreInput{
		Priority:    issue.Priority,
		MRCreatedAt: mrCreatedAt,
		Now:         now,
	}

	// Add fields from MR metadata if available
	if fields != nil {
		input.RetryCount = fields.RetryCount

		// Parse convoy created at if available
		if fields.ConvoyCreatedAt != "" {
			if convoyTime, err := time.Parse(time.RFC3339, fields.ConvoyCreatedAt); err == nil {
				input.ConvoyCreatedAt = &convoyTime
			}
		}
	}

	return refinery.ScoreMRWithDefaults(input)
}

// mrBranchState is the result of verifying an MR's branch in git.
//
// EXISTS and HAS-WORK are separate questions: a branch that was never committed
// to still resolves to its base, so existence alone cannot distinguish "mergeable
// work is waiting" from "merging this changes nothing" (gt-d5u). The states are
// kept distinct because their dispositions differ — an EMPTY MR is rejected, not
// merged and not retried.
//
// WILL-IT-MERGE is a THIRD question, and none of these states answer it. That is
// why the good state is spelled PRESENT and not OK (gt-0w2l): rendered as OK, in
// a column called GIT, beside a STATUS column reading "ready", it was read as
// merge clearance by the Mayor, who instructed the refinery to merge two
// branches that conflicted in 17 and 12 files. Both were correctly PRESENT — they
// existed and carried 700 commits each over a merge base 700 commits back. For an
// actual merge verdict see mrMergeState, which runs a three-way merge.
type mrBranchState string

const (
	// mrBranchStateSkipped means no verification ran (--verify off, no client,
	// or the MR records no branch).
	mrBranchStateSkipped mrBranchState = ""
	// mrBranchStatePresent means the branch exists and carries at least one
	// commit over its target branch. It says NOTHING about whether the merge
	// would succeed.
	mrBranchStatePresent mrBranchState = "PRESENT"
	// mrBranchStateMissing means neither a local nor a remote-tracking ref exists.
	mrBranchStateMissing mrBranchState = "MISSING"
	// mrBranchStateEmpty means the branch exists but is not ahead of its target:
	// merging it is a no-op.
	mrBranchStateEmpty mrBranchState = "EMPTY"
	// mrBranchStateErr means git could not answer (corrupt repo, permissions,
	// unresolvable target ref).
	mrBranchStateErr mrBranchState = "ERR"
)

// mrMergeState is the result of rehearsing an MR's merge with `--merge-check`.
//
// This is the verdict mrBranchState is not. It comes from a real three-way merge
// (git merge-tree --write-tree) against the same refs the queue would merge, so
// CONFLICTS is measured rather than inferred.
type mrMergeState string

const (
	// mrMergeStateSkipped means no merge rehearsal ran (--merge-check off, or
	// the branch never resolved far enough to rehearse).
	mrMergeStateSkipped mrMergeState = ""
	// mrMergeStateClean means the three-way merge produced a tree with no
	// conflicts.
	mrMergeStateClean mrMergeState = "CLEAN"
	// mrMergeStateConflicts means the merge conflicts; the count of conflicted
	// files travels alongside.
	mrMergeStateConflicts mrMergeState = "CONFLICTS"
	// mrMergeStateErr means git could not rehearse the merge.
	mrMergeStateErr mrMergeState = "ERR"
)

// branchVerifier abstracts the git checks --verify performs, for testability.
type branchVerifier interface {
	BranchExists(branch string) (bool, error)
	RemoteTrackingBranchExists(remote, branch string) (bool, error)
	CommitsAhead(base, branch string) (int, error)
}

// mergeRehearser abstracts the extra git check --merge-check performs. It is
// separate from branchVerifier because the two flags are separate: --verify
// answers reachability, --merge-check answers mergeability, and a caller that
// wants only the first should not have to supply the second.
type mergeRehearser interface {
	MergeConflicts(base, branch string) ([]string, error)
}

// defaultMRTargetBranch is the target assumed when an MR records none.
const defaultMRTargetBranch = "main"

// mrVerification is everything --verify (and optionally --merge-check) learned
// about one MR's branch.
type mrVerification struct {
	state        mrBranchState
	commitsAhead int
	// branchRef and targetRef are the refs the checks actually resolved to —
	// origin/<branch> when it exists, the local ref otherwise. A merge rehearsal
	// must use these exact refs, not the branch names, or it answers about a
	// different commit than the queue will merge.
	branchRef string
	targetRef string
}

// verifyBranch checks that an MR's branch both exists and contains work.
// Returns the resulting state and, when it is PRESENT or EMPTY, the number of
// commits the branch carries over its target.
func verifyBranch(verify bool, client branchVerifier, fields *beads.MRFields) (mrBranchState, int) {
	v := verifyBranchRefs(verify, client, fields)
	return v.state, v.commitsAhead
}

// verifyBranchRefs is verifyBranch, keeping the refs it resolved so a caller can
// run further checks against the same commits.
func verifyBranchRefs(verify bool, client branchVerifier, fields *beads.MRFields) mrVerification {
	if !verify || client == nil || fields == nil || fields.Branch == "" {
		return mrVerification{state: mrBranchStateSkipped}
	}

	branchRef, found, err := resolveVerifyRef(client, fields.Branch)
	if err != nil {
		return mrVerification{state: mrBranchStateErr}
	}
	if !found {
		return mrVerification{state: mrBranchStateMissing}
	}

	// Content check: does the branch carry anything the target does not already
	// have? A branch pointing at its own base answers "yes" to existence and
	// "no" here.
	target := strings.TrimSpace(fields.Target)
	if target == "" {
		target = defaultMRTargetBranch
	}
	targetRef, targetFound, err := resolveVerifyRef(client, target)
	if err != nil || !targetFound {
		// Existence held but the target could not be resolved, so "does this
		// contain work" is unanswered. Report that rather than implying PRESENT.
		return mrVerification{state: mrBranchStateErr}
	}

	ahead, err := client.CommitsAhead(targetRef, branchRef)
	if err != nil {
		return mrVerification{state: mrBranchStateErr, branchRef: branchRef, targetRef: targetRef}
	}
	if ahead == 0 {
		return mrVerification{state: mrBranchStateEmpty, branchRef: branchRef, targetRef: targetRef}
	}
	return mrVerification{state: mrBranchStatePresent, commitsAhead: ahead, branchRef: branchRef, targetRef: targetRef}
}

// rehearseMerge runs a real three-way merge of the MR's branch into its target
// and reports whether it conflicts, and in how many files.
//
// It only runs for a PRESENT branch. MISSING and EMPTY have their own
// dispositions that a merge verdict cannot change, and ERR means the refs were
// never established — rehearsing against refs we could not resolve would produce
// a verdict about nothing.
func rehearseMerge(check bool, client mergeRehearser, v mrVerification) (mrMergeState, int) {
	if !check || client == nil || v.state != mrBranchStatePresent {
		return mrMergeStateSkipped, 0
	}
	if v.branchRef == "" || v.targetRef == "" {
		return mrMergeStateSkipped, 0
	}
	conflicts, err := client.MergeConflicts(v.targetRef, v.branchRef)
	if err != nil {
		return mrMergeStateErr, 0
	}
	if len(conflicts) == 0 {
		return mrMergeStateClean, 0
	}
	return mrMergeStateConflicts, len(conflicts)
}

// resolveVerifyRef finds a usable ref for a branch name, preferring the
// remote-tracking ref because MRs are submitted by pushing to origin — a stale
// local ref would answer for a different commit than the queue will merge.
// Returns (ref, found, err).
func resolveVerifyRef(client branchVerifier, branch string) (string, bool, error) {
	remoteExists, err := client.RemoteTrackingBranchExists("origin", branch)
	if err != nil {
		return "", false, err
	}
	if remoteExists {
		return "origin/" + branch, true, nil
	}
	localExists, err := client.BranchExists(branch)
	if err != nil {
		return "", false, err
	}
	if localExists {
		return branch, true, nil
	}
	return "", false, nil
}
