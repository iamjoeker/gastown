// Package cmd provides CLI commands for the gt tool.
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

var (
	// Patrol digest flags
	patrolDigestYesterday bool
	patrolDigestDate      string
	patrolDigestDryRun    bool
	patrolDigestVerbose   bool
)

var patrolCmd = &cobra.Command{
	Use:     "patrol",
	GroupID: GroupDiag,
	Short:   "Patrol digest management",
	RunE:    requireSubcommand,
	Long: `Manage patrol cycle digests.

Patrol cycles (Deacon, Witness, Refinery) create ephemeral per-cycle digests.
This command aggregates them into permanent daily summaries.

Examples:
  gt patrol digest --yesterday  # Aggregate yesterday's patrol digests
  gt patrol digest --dry-run    # Preview what would be aggregated`,
}

var patrolDigestCmd = &cobra.Command{
	Use:   "digest",
	Short: "Aggregate patrol cycle digests into a daily summary bead",
	Long: `Aggregate ephemeral patrol cycle digests into a permanent daily summary.

This command is intended to be run by Deacon patrol (daily) or manually.
It queries patrol digests for a target date, creates a single aggregate
"Patrol Report YYYY-MM-DD" bead, then deletes the source digests.

The resulting digest bead is permanent (synced via git) and provides
an audit trail without per-cycle ephemeral pollution.

Examples:
  gt patrol digest --yesterday   # Digest yesterday's patrols (for daily patrol)
  gt patrol digest --date 2026-01-15
  gt patrol digest --yesterday --dry-run`,
	RunE: runPatrolDigest,
}

func init() {
	patrolCmd.AddCommand(patrolDigestCmd)
	patrolCmd.AddCommand(patrolNewCmd)
	patrolCmd.AddCommand(patrolReportCmd)
	rootCmd.AddCommand(patrolCmd)

	// Patrol digest flags
	patrolDigestCmd.Flags().BoolVar(&patrolDigestYesterday, "yesterday", false, "Digest yesterday's patrol cycles")
	patrolDigestCmd.Flags().StringVar(&patrolDigestDate, "date", "", "Digest patrol cycles for specific date (YYYY-MM-DD)")
	patrolDigestCmd.Flags().BoolVar(&patrolDigestDryRun, "dry-run", false, "Preview what would be created without creating")
	patrolDigestCmd.Flags().BoolVarP(&patrolDigestVerbose, "verbose", "v", false, "Verbose output")
}

// PatrolDigest represents the aggregated daily patrol report.
type PatrolDigest struct {
	Date         string                   `json:"date"`
	TotalCycles  int                      `json:"total_cycles"`
	ByRole       map[string]int           `json:"by_role"`        // deacon, witness, refinery
	Cycles       []PatrolCycleEntry       `json:"cycles"`
}

// PatrolCycleEntry represents a single patrol cycle in the digest.
type PatrolCycleEntry struct {
	ID          string    `json:"id"`
	Role        string    `json:"role"`         // deacon, witness, refinery
	Title       string    `json:"title"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	ClosedAt    time.Time `json:"closed_at,omitempty"`
}

// runPatrolDigest aggregates patrol cycle digests into a daily digest bead.
func runPatrolDigest(cmd *cobra.Command, args []string) error {
	// Determine target date
	var targetDate time.Time

	if patrolDigestDate != "" {
		parsed, err := time.Parse("2006-01-02", patrolDigestDate)
		if err != nil {
			return fmt.Errorf("invalid date format (use YYYY-MM-DD): %w", err)
		}
		targetDate = parsed
	} else if patrolDigestYesterday {
		// Use UTC: Dolt stores timestamps in UTC, so date comparisons
		// must use UTC dates to avoid evening PDT mismatches (gt-ty4).
		targetDate = time.Now().UTC().AddDate(0, 0, -1)
	} else {
		return fmt.Errorf("specify --yesterday or --date YYYY-MM-DD")
	}

	dateStr := targetDate.Format("2006-01-02")

	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}
	b := beads.New(workDir)

	// Idempotency check: see if digest already exists for this date
	existingID, err := findExistingPatrolDigest(dateStr)
	if err != nil {
		// Non-fatal: continue with creation attempt
		if patrolDigestVerbose {
			fmt.Fprintf(os.Stderr, "[patrol] warning: failed to check existing digest: %v\n", err)
		}
	} else if existingID != "" {
		fmt.Printf("%s Patrol digest already exists for %s (bead: %s)\n",
			style.Dim.Render("○"), dateStr, existingID)
		return nil
	}

	// Query ephemeral patrol digest beads for target date
	cycles, err := queryPatrolDigests(b, targetDate)
	if err != nil {
		return fmt.Errorf("querying patrol digests: %w", err)
	}

	if len(cycles) == 0 {
		fmt.Printf("%s No patrol digests found for %s\n", style.Dim.Render("○"), dateStr)
		return nil
	}

	// Build digest
	digest := PatrolDigest{
		Date:   dateStr,
		Cycles: cycles,
		ByRole: make(map[string]int),
	}

	for _, c := range cycles {
		digest.TotalCycles++
		digest.ByRole[c.Role]++
	}

	if patrolDigestDryRun {
		fmt.Printf("%s [DRY RUN] Would create Patrol Report %s:\n", style.Bold.Render("📊"), dateStr)
		fmt.Printf("  Total cycles: %d\n", digest.TotalCycles)
		fmt.Printf("  By Role:\n")
		roles := make([]string, 0, len(digest.ByRole))
		for role := range digest.ByRole {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			fmt.Printf("    %s: %d cycles\n", role, digest.ByRole[role])
		}
		return nil
	}

	// Create permanent digest bead
	digestID, err := createPatrolDigestBead(digest)
	if err != nil {
		return fmt.Errorf("creating digest bead: %w", err)
	}

	// Delete source digests (they're ephemeral)
	deletedCount, deleteErr := deletePatrolDigests(b, targetDate)
	if deleteErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to delete some source digests: %v\n", deleteErr)
	}

	fmt.Printf("%s Created Patrol Report %s (bead: %s)\n", style.Success.Render("✓"), dateStr, digestID)
	fmt.Printf("  Total: %d cycles\n", digest.TotalCycles)
	for role, count := range digest.ByRole {
		fmt.Printf("    %s: %d\n", role, count)
	}
	if deletedCount > 0 {
		fmt.Printf("  Deleted %d source digests\n", deletedCount)
	}

	return nil
}

// queryPatrolDigests queries ephemeral patrol digest beads for a target date.
//
// Per-cycle digests are created with Ephemeral: true (molecule_lifecycle.go),
// which routes them into the wisps table, not the issues table (GH#2446).
// They also carry the label "gt:task", not "digest" (they were never given a
// dedicated label). "bd list --label=digest" queried the wrong table for the
// wrong label, so it always returned zero rows, silently and successfully —
// the same failure mode as gt-ktvs in gt compact. Use beads.List with
// Ephemeral: true (which shells out to "bd query ephemeral=true ...", the
// wisps-table-aware path) and filter by title prefix and date client-side,
// same as compaction does for its own wisp scans.
func queryPatrolDigests(b *beads.Beads, targetDate time.Time) ([]PatrolCycleEntry, error) {
	issues, err := b.List(beads.ListOptions{
		Status:    "closed",
		Ephemeral: true,
		Priority:  -1, // -1 = no priority filter; 0 would filter to P0 only
		Limit:     0,  // Get all
	})
	if err != nil {
		return nil, fmt.Errorf("bd query (wisps, ephemeral=true, status=closed) failed: %w", err)
	}

	if patrolDigestVerbose {
		fmt.Fprintf(os.Stderr, "[patrol] scanned %d closed ephemeral wisps\n", len(issues))
	}

	// Compare dates in UTC: Dolt stores timestamps in UTC (gt-ty4).
	targetDay := targetDate.UTC().Format("2006-01-02")
	var patrolDigests []PatrolCycleEntry
	titleMatches := 0

	for _, issue := range issues {
		// Must be a patrol digest. createMoleculeDigest (molecule_lifecycle.go)
		// titles these "Digest: <moleculeID>", and moleculeID is a wisp ID like
		// "gt-wisp-3i6l" — never "mol-<role>-patrol" — so a "Digest: mol-"
		// prefix filter matched real digests never, the same zero-rows failure
		// this function's own doc comment describes fixing (gt-5jin).
		if !strings.HasPrefix(issue.Title, "Digest: ") {
			continue
		}
		titleMatches++

		createdAt := parseBeadsTimestamp(issue.CreatedAt)
		if createdAt.IsZero() {
			if patrolDigestVerbose {
				fmt.Fprintf(os.Stderr, "[patrol] skipping %s: unparseable created_at %q\n", issue.ID, issue.CreatedAt)
			}
			continue
		}

		// Check if created on target date (both in UTC)
		if createdAt.UTC().Format("2006-01-02") != targetDay {
			continue
		}

		closedAt := parseBeadsTimestamp(issue.ClosedAt)

		// Extract role from title (e.g., "Digest: mol-deacon-patrol" -> "deacon")
		role := extractPatrolRole(issue.Title)

		patrolDigests = append(patrolDigests, PatrolCycleEntry{
			ID:          issue.ID,
			Role:        role,
			Title:       issue.Title,
			Description: issue.Description,
			CreatedAt:   createdAt,
			ClosedAt:    closedAt,
		})
	}

	if patrolDigestVerbose {
		fmt.Fprintf(os.Stderr, "[patrol] %d wisps matched title prefix %q, %d matched date %s\n",
			titleMatches, "Digest: ", len(patrolDigests), targetDay)
	}

	return patrolDigests, nil
}

// extractPatrolRole extracts the role from a patrol digest title.
// "Digest: mol-deacon-patrol" -> "deacon"
// "Digest: mol-witness-patrol" -> "witness"
// "Digest: gt-wisp-abc123" -> "unknown"
func extractPatrolRole(title string) string {
	// Remove "Digest: " prefix
	title = strings.TrimPrefix(title, "Digest: ")

	// Extract role from "mol-<role>-patrol" or "gt-wisp-<id>"
	if strings.HasPrefix(title, "mol-") && strings.HasSuffix(title, "-patrol") {
		// "mol-deacon-patrol" -> "deacon"
		role := strings.TrimPrefix(title, "mol-")
		role = strings.TrimSuffix(role, "-patrol")
		return role
	}

	// For wisp digests, try to extract from description or return generic
	return "patrol"
}

// createPatrolDigestBead creates a permanent bead for the daily patrol digest.
func createPatrolDigestBead(digest PatrolDigest) (string, error) {
	// Build description with aggregate data
	var desc strings.Builder
	desc.WriteString(fmt.Sprintf("Daily patrol aggregate for %s.\n\n", digest.Date))
	desc.WriteString(fmt.Sprintf("**Total Cycles:** %d\n\n", digest.TotalCycles))

	if len(digest.ByRole) > 0 {
		desc.WriteString("## By Role\n")
		roles := make([]string, 0, len(digest.ByRole))
		for role := range digest.ByRole {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			desc.WriteString(fmt.Sprintf("- %s: %d cycles\n", role, digest.ByRole[role]))
		}
		desc.WriteString("\n")
	}

	// Build payload JSON with cycle details
	payloadJSON, err := json.Marshal(digest)
	if err != nil {
		return "", fmt.Errorf("marshaling digest payload: %w", err)
	}

	// Create the digest bead (NOT ephemeral - this is permanent)
	title := fmt.Sprintf("Patrol Report %s", digest.Date)
	bdArgs := []string{
		"create",
		"--type=event",
		"--title=" + title,
		"--event-category=patrol.digest",
		"--event-payload=" + string(payloadJSON),
		"--description=" + desc.String(),
		"--silent",
	}

	bdCmd := exec.Command("bd", bdArgs...)
	output, err := bdCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("creating digest bead: %w\nOutput: %s", err, string(output))
	}

	digestID := strings.TrimSpace(string(output))

	// Auto-close the digest (it's an audit record, not work)
	closeCmd := exec.Command("bd", "close", digestID, "--reason=daily patrol digest")
	_ = closeCmd.Run() // Best effort

	return digestID, nil
}

// findExistingPatrolDigest checks if a patrol digest already exists for the given date.
// Returns the bead ID if found, empty string if not found.
func findExistingPatrolDigest(dateStr string) (string, error) {
	expectedTitle := fmt.Sprintf("Patrol Report %s", dateStr)

	// Query event beads with patrol.digest category
	listCmd := exec.Command("bd", "list",
		"--type=event",
		"--json",
		"--limit=50", // Recent events only
	)
	listOutput, err := listCmd.Output()
	if err != nil {
		return "", err
	}

	var events []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}

	if err := json.Unmarshal(listOutput, &events); err != nil {
		return "", err
	}

	for _, evt := range events {
		if evt.Title == expectedTitle {
			return evt.ID, nil
		}
	}

	return "", nil
}

// deletePatrolDigests deletes ephemeral patrol digest beads for a target date.
func deletePatrolDigests(b *beads.Beads, targetDate time.Time) (int, error) {
	// Query patrol digests for the target date
	cycles, err := queryPatrolDigests(b, targetDate)
	if err != nil {
		return 0, err
	}

	if len(cycles) == 0 {
		return 0, nil
	}

	// Collect IDs to delete
	var idsToDelete []string
	for _, cycle := range cycles {
		idsToDelete = append(idsToDelete, cycle.ID)
	}

	// Delete in batch
	deleteArgs := append([]string{"delete", "--force"}, idsToDelete...)
	deleteCmd := exec.Command("bd", deleteArgs...)
	if err := deleteCmd.Run(); err != nil {
		return 0, fmt.Errorf("deleting patrol digests: %w", err)
	}

	return len(idsToDelete), nil
}
