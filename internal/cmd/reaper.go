package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	agentconfig "github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/reaper"
	"github.com/steveyegge/gastown/internal/style"
)

var (
	reaperDB          string
	reaperHost        string
	reaperPort        int
	reaperMaxAge      string
	reaperPurgeAge    string
	reaperMailAge     string
	reaperStaleAge    string
	reaperDBDelay     string
	reaperDryRun      bool
	reaperJSON        bool
	reaperArchiveDir  string
	reaperNoArchive   bool
	reaperArchiveID   string
	reaperArchiveGrep string
	reaperArchiveLmt  int
)

// reaperArchiver resolves the archive purge exports protected wisps to before
// deleting them (gt-6xwt).
//
// FAILING TO OPEN THE ARCHIVE IS NOT A FAILURE OF THE PURGE, it is a return to
// the old contract: a nil Archiver means ProtectedWispLabels protects
// absolutely, exactly as it did before retention existed. So an unwritable
// archive directory costs rows that stay in Dolt and a warning on stderr, never
// a record. The warning is on stderr rather than swallowed because "protected: N"
// climbing forever with no explanation is how this became a bead in the first
// place.
func reaperArchiver() reaper.Archiver {
	if reaperNoArchive {
		return nil
	}
	dir := reaperArchiveDir
	if dir == "" {
		dir = reaper.DefaultArchiveDir()
	}
	archive, err := reaper.NewFileArchive(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: wisp archive unavailable (%v); protected wisps will be kept, not released\n", err)
		return nil
	}
	return archive
}

// reaperArchiveLocation returns the directory `gt reaper archive` reads.
func reaperArchiveLocation() string {
	if reaperArchiveDir != "" {
		return reaperArchiveDir
	}
	return reaper.DefaultArchiveDir()
}

func reaperDatabaseNames() []string {
	if reaperDB == "" {
		return reaper.DiscoverDatabases(reaperHost, reaperPort)
	}
	parts := strings.Split(reaperDB, ",")
	databases := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name != "" {
			databases = append(databases, name)
		}
	}
	return databases
}

func defaultReaperEndpoint() (string, int) {
	host := agentconfig.ResolveDoltHost("")
	port := 0
	if p := os.Getenv("GT_DOLT_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			port = v
		}
	}
	if townRoot, err := findTownRoot(); err == nil {
		if host == "" {
			host = agentconfig.ResolveDoltHost(townRoot)
		}
		if port == 0 {
			port = agentconfig.ResolveDoltPort(townRoot)
		}
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if port == 0 {
		port = 3307
	}
	return host, port
}

func waitBeforeReaperDatabase(index int) error {
	if index == 0 {
		return nil
	}
	delay, err := time.ParseDuration(reaperDBDelay)
	if err != nil {
		return fmt.Errorf("invalid --db-delay: %w", err)
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	return nil
}

var reaperCmd = &cobra.Command{
	Use:     "reaper",
	GroupID: GroupServices,
	Short:   "Wisp and issue cleanup operations (Dog-callable helpers)",
	Long: `Execute wisp reaper operations against Dolt databases.

These subcommands are the callable helper functions for the mol-dog-reaper
formula. They execute SQL operations but leave eligibility decisions to the
Dog agent or daemon orchestrator.

When run by a Dog:
  gt reaper scan --db=gastown          # Discover candidates
  gt reaper reap --db=gastown          # Close stale wisps
  gt reaper purge --db=gastown         # Delete old closed wisps + mail
  gt reaper auto-close --db=gastown    # Close stale issues
  gt reaper archive --grep=gt-abc      # Read records purge archived before deleting`,
	RunE: requireSubcommand,
}

var reaperDatabasesCmd = &cobra.Command{
	Use:   "databases",
	Short: "List databases available for reaping",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbs := reaper.DiscoverDatabases(reaperHost, reaperPort)
		if reaperJSON {
			fmt.Println(reaper.FormatJSON(dbs))
		} else {
			for _, db := range dbs {
				fmt.Println(db)
			}
		}
		return nil
	},
}

var reaperScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan databases for reaper candidates",
	Long: `Count reap, purge, auto-close, and mail candidates in databases.

When --db is provided, scans a single database. When omitted, auto-discovers
all databases on the Dolt server and scans each one, printing a summary.

Returns counts and anomaly detection results without modifying any data.
The Dog uses this to understand the state before deciding what to reap.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		maxAge, err := time.ParseDuration(reaperMaxAge)
		if err != nil {
			return fmt.Errorf("invalid --max-age: %w", err)
		}
		purgeAge, err := time.ParseDuration(reaperPurgeAge)
		if err != nil {
			return fmt.Errorf("invalid --purge-age: %w", err)
		}
		mailAge, err := time.ParseDuration(reaperMailAge)
		if err != nil {
			return fmt.Errorf("invalid --mail-age: %w", err)
		}
		staleAge, err := time.ParseDuration(reaperStaleAge)
		if err != nil {
			return fmt.Errorf("invalid --stale-age: %w", err)
		}

		databases := reaperDatabaseNames()

		var results []*reaper.ScanResult
		for i, dbName := range databases {
			if err := waitBeforeReaperDatabase(i); err != nil {
				return err
			}
			if err := reaper.ValidateDBName(dbName); err != nil {
				fmt.Fprintf(os.Stderr, "skip invalid db: %s\n", dbName)
				continue
			}

			db, err := reaper.OpenDB(reaperHost, reaperPort, dbName, 10*time.Second, 10*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: connect error: %v\n", dbName, err)
				continue
			}

			if ok, err := reaper.HasReaperSchema(db); err != nil {
				fmt.Fprintf(os.Stderr, "%s: schema check error: %v\n", dbName, err)
				db.Close()
				continue
			} else if !ok {
				db.Close()
				continue
			}

			result, err := reaper.Scan(db, dbName, maxAge, purgeAge, mailAge, staleAge)
			db.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: scan error: %v\n", dbName, err)
				continue
			}
			results = append(results, result)
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(results))
		} else {
			var totalReap, totalMoleculeSteps, totalPurge, totalMail, totalStale, totalOpen int
			for _, r := range results {
				fmt.Printf("Database: %s\n", r.Database)
				fmt.Printf("  Reap candidates:  %d\n", r.ReapCandidates)
				if r.MoleculeStepCandidates > 0 {
					fmt.Printf("  Molecule steps:   %d\n", r.MoleculeStepCandidates)
				}
				fmt.Printf("  Purge candidates: %d\n", r.PurgeCandidates)
				if r.ProtectedFromPurge > 0 {
					fmt.Printf("  Purge-protected:  %d\n", r.ProtectedFromPurge)
				}
				// How much of that protection a purge with an archive would
				// release. Without this line the protected count only ever
				// grows and never says what would clear it (gt-6xwt).
				if r.ArchivableFromPurge > 0 {
					fmt.Printf("  Archivable:       %d\n", r.ArchivableFromPurge)
				}
				fmt.Printf("  Mail candidates:  %d\n", r.MailCandidates)
				fmt.Printf("  Stale candidates: %d\n", r.StaleCandidates)
				fmt.Printf("  Open wisps:       %d\n", r.OpenWisps)
				for _, a := range r.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
				totalReap += r.ReapCandidates
				totalMoleculeSteps += r.MoleculeStepCandidates
				totalPurge += r.PurgeCandidates
				totalMail += r.MailCandidates
				totalStale += r.StaleCandidates
				totalOpen += r.OpenWisps
			}
			if len(results) > 1 {
				fmt.Printf("\nScan summary (%d databases):\n", len(results))
				fmt.Printf("  Reap candidates:  %d\n", totalReap)
				if totalMoleculeSteps > 0 {
					fmt.Printf("  Molecule steps:   %d\n", totalMoleculeSteps)
				}
				fmt.Printf("  Purge candidates: %d\n", totalPurge)
				fmt.Printf("  Mail candidates:  %d\n", totalMail)
				fmt.Printf("  Stale candidates: %d\n", totalStale)
				fmt.Printf("  Open wisps:       %d\n", totalOpen)
			}
		}
		return nil
	},
}

var reaperReapCmd = &cobra.Command{
	Use:   "reap",
	Short: "Close stale wisps past max-age",
	Long: `Close wisps that are past the max-age threshold and whose parent
molecule is already closed (or missing/orphaned).

When --db is provided, reaps a single database. When omitted, auto-discovers
all databases on the Dolt server and reaps each one.

Returns the count of reaped wisps. Use --dry-run to preview.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		maxAge, err := time.ParseDuration(reaperMaxAge)
		if err != nil {
			return fmt.Errorf("invalid --max-age: %w", err)
		}

		databases := reaperDatabaseNames()

		var results []*reaper.ReapResult
		for i, dbName := range databases {
			if err := waitBeforeReaperDatabase(i); err != nil {
				return err
			}
			if err := reaper.ValidateDBName(dbName); err != nil {
				fmt.Fprintf(os.Stderr, "skip invalid db: %s\n", dbName)
				continue
			}

			db, err := reaper.OpenDB(reaperHost, reaperPort, dbName, 10*time.Second, 10*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: connect error: %v\n", dbName, err)
				continue
			}

			if ok, err := reaper.HasReaperSchema(db); err != nil {
				fmt.Fprintf(os.Stderr, "%s: schema check error: %v\n", dbName, err)
				db.Close()
				continue
			} else if !ok {
				db.Close()
				continue
			}

			result, err := reaper.Reap(db, dbName, maxAge, reaperDryRun)
			db.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: reap error: %v\n", dbName, err)
				continue
			}
			results = append(results, result)
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(results))
		} else {
			var totalReaped, totalMoleculeSteps, totalOpen int
			for _, r := range results {
				prefix := ""
				if r.DryRun {
					prefix = "[DRY RUN] would "
				}
				extra := ""
				if r.MoleculeStepsClosed > 0 {
					extra = fmt.Sprintf(" (+%d closed-molecule steps)", r.MoleculeStepsClosed)
				}
				fmt.Printf("%s: %sreaped %d wisps%s, %d open remain\n",
					r.Database, prefix, r.Reaped, extra, r.OpenRemain)
				totalReaped += r.Reaped
				totalMoleculeSteps += r.MoleculeStepsClosed
				totalOpen += r.OpenRemain
			}
			if len(results) > 1 {
				prefix := ""
				if reaperDryRun {
					prefix = "[DRY RUN] "
				}
				extra := ""
				if totalMoleculeSteps > 0 {
					extra = fmt.Sprintf(" (+%d closed-molecule steps)", totalMoleculeSteps)
				}
				fmt.Printf("\n%sReap summary (%d databases): reaped %d wisps%s, %d open remain\n",
					prefix, len(results), totalReaped, extra, totalOpen)
				if totalOpen > reaper.DefaultAlertThreshold {
					fmt.Fprintf(os.Stderr, "WARNING: %d open wisps exceed alert threshold (%d)\n",
						totalOpen, reaper.DefaultAlertThreshold)
				}
			}
		}
		return nil
	},
}

var reaperPurgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Delete old closed wisps and mail",
	Long: `Delete closed wisps past the purge-age threshold and closed mail
past the mail-age threshold. Irreversible operation.

When --db is provided, purges a single database. When omitted, auto-discovers
all databases on the Dolt server and purges each one.

Wisps whose type must never be lost (merge requests, escalations) are exported
to the durable archive and only then deleted, so protection releases the row
without losing the record. Pinned wisps are never exported or deleted. Use
--no-archive to keep protected wisps in the database instead.

Returns counts of purged rows. Use --dry-run to preview.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		purgeAge, err := time.ParseDuration(reaperPurgeAge)
		if err != nil {
			return fmt.Errorf("invalid --purge-age: %w", err)
		}
		mailAge, err := time.ParseDuration(reaperMailAge)
		if err != nil {
			return fmt.Errorf("invalid --mail-age: %w", err)
		}

		archive := reaperArchiver()
		databases := reaperDatabaseNames()

		var results []*reaper.PurgeResult
		for i, dbName := range databases {
			if err := waitBeforeReaperDatabase(i); err != nil {
				return err
			}
			if err := reaper.ValidateDBName(dbName); err != nil {
				fmt.Fprintf(os.Stderr, "skip invalid db: %s\n", dbName)
				continue
			}

			db, err := reaper.OpenDB(reaperHost, reaperPort, dbName, 30*time.Second, 30*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: connect error: %v\n", dbName, err)
				continue
			}

			if ok, err := reaper.HasReaperSchema(db); err != nil {
				fmt.Fprintf(os.Stderr, "%s: schema check error: %v\n", dbName, err)
				db.Close()
				continue
			} else if !ok {
				db.Close()
				continue
			}

			result, err := reaper.Purge(db, dbName, purgeAge, mailAge, reaperDryRun, reaper.WithArchive(archive))
			db.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: purge error: %v\n", dbName, err)
				continue
			}
			results = append(results, result)
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(results))
		} else {
			var totalWisps, totalMail, totalArchived, totalProtected int
			for _, r := range results {
				prefix := ""
				if r.DryRun {
					prefix = "[DRY RUN] would "
				}
				fmt.Printf("%s: %spurged %d wisps, %d mail\n",
					r.Database, prefix, r.WispsPurged, r.MailPurged)
				// Say WHAT was purged, not only how many. A bare count here is
				// what got read as ~40 destroyed merge-request records on
				// 2026-08-26; this path deletes only rows that are neither
				// pinned nor label-protected, and the breakdown is how the
				// output says so (gt-mkuw).
				if len(r.WispsPurgedByType) > 0 {
					fmt.Printf("  By wisp_type (unprotected only): %s\n",
						reaper.FormatWispTypeDigest(r.WispsPurgedByType))
				}
				// Name the archive alongside the count: a deletion that reports
				// only a number is the shape of the loss this replaced.
				if r.WispsArchived > 0 && archive != nil {
					fmt.Printf("  Archived (released): %d → %s\n", r.WispsArchived, archive.Location())
				}
				// Report the skip rather than just deleting fewer rows: without
				// this a protected purge is indistinguishable from a quiet one.
				if r.WispsProtected > 0 {
					fmt.Printf("  Protected (skipped): %d\n", r.WispsProtected)
				}
				for _, a := range r.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
				totalWisps += r.WispsPurged
				totalMail += r.MailPurged
				totalArchived += r.WispsArchived
				totalProtected += r.WispsProtected
			}
			if len(results) > 1 {
				prefix := ""
				if reaperDryRun {
					prefix = "[DRY RUN] "
				}
				fmt.Printf("\n%sPurge summary (%d databases): purged %d wisps, %d mail, archived %d wisps, protected %d wisps\n",
					prefix, len(results), totalWisps, totalMail, totalArchived, totalProtected)
			}
		}
		return nil
	},
}

var reaperAutoCloseCmd = &cobra.Command{
	Use:   "auto-close",
	Short: "Close stale issues past stale-age",
	Long: `Close issues open with no updates past the stale-age threshold.
Excludes P0/P1 priority, epics, and issues with active dependencies.

When --db is provided, auto-closes in a single database. When omitted,
auto-discovers all databases on the Dolt server and auto-closes in each one.

Returns the count of closed issues. Use --dry-run to preview.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		staleAge, err := time.ParseDuration(reaperStaleAge)
		if err != nil {
			return fmt.Errorf("invalid --stale-age: %w", err)
		}

		databases := reaperDatabaseNames()

		var results []*reaper.AutoCloseResult
		for i, dbName := range databases {
			if err := waitBeforeReaperDatabase(i); err != nil {
				return err
			}
			if err := reaper.ValidateDBName(dbName); err != nil {
				fmt.Fprintf(os.Stderr, "skip invalid db: %s\n", dbName)
				continue
			}

			db, err := reaper.OpenDB(reaperHost, reaperPort, dbName, 10*time.Second, 10*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: connect error: %v\n", dbName, err)
				continue
			}

			if ok, err := reaper.HasReaperSchema(db); err != nil {
				fmt.Fprintf(os.Stderr, "%s: schema check error: %v\n", dbName, err)
				db.Close()
				continue
			} else if !ok {
				db.Close()
				continue
			}

			result, err := reaper.AutoClose(db, dbName, staleAge, reaperDryRun)
			if err != nil {
				db.Close()
				fmt.Fprintf(os.Stderr, "%s: auto-close error: %v\n", dbName, err)
				continue
			}
			results = append(results, result)

			// Acked mail: delivery-acked gt:message beads never read, exempt
			// from the sweep above by AutoCloseExemptLabels (gt-ljun).
			ackedResult, err := reaper.AutoCloseAckedMail(db, dbName, staleAge, reaperDryRun)
			db.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: auto-close acked mail error: %v\n", dbName, err)
				continue
			}
			results = append(results, ackedResult)
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(results))
		} else {
			var totalClosed int
			for _, r := range results {
				prefix := ""
				if r.DryRun {
					prefix = "[DRY RUN] would "
				}
				for _, entry := range r.ClosedEntries {
					fmt.Printf("  %s %s (%dd stale, db:%s)\n",
						entry.ID, entry.Title, entry.AgeDays, entry.Database)
				}
				fmt.Printf("%s: %sauto-closed %d stale issues\n",
					r.Database, prefix, r.Closed)
				totalClosed += r.Closed
			}
			if len(results) > 1 {
				prefix := ""
				if reaperDryRun {
					prefix = "[DRY RUN] "
				}
				fmt.Printf("\n%sAuto-close summary (%d databases): auto-closed %d stale issues\n",
					prefix, len(results), totalClosed)
			}
		}
		return nil
	},
}

var reaperRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run full reaper cycle across all databases",
	Long: `Execute a full reaper cycle: scan → reap → purge → auto-close → report.

This is the inline fallback for when Dog dispatch is unavailable.
Normally the daemon dispatches a Dog to execute the mol-dog-reaper formula.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		databases := reaperDatabaseNames()

		maxAge, err := time.ParseDuration(reaperMaxAge)
		if err != nil {
			return fmt.Errorf("invalid --max-age: %w", err)
		}
		purgeAge, err := time.ParseDuration(reaperPurgeAge)
		if err != nil {
			return fmt.Errorf("invalid --purge-age: %w", err)
		}
		mailAge, err := time.ParseDuration(reaperMailAge)
		if err != nil {
			return fmt.Errorf("invalid --mail-age: %w", err)
		}
		staleAge, err := time.ParseDuration(reaperStaleAge)
		if err != nil {
			return fmt.Errorf("invalid --stale-age: %w", err)
		}

		archive := reaperArchiver()
		var totalReaped, totalMoleculeSteps, totalPurged, totalMailPurged, totalArchived, totalProtected, totalClosed, totalOpen int
		// allAnomalies collects anomalies from every step (scan, reap, purge,
		// auto-close), not just scan's. The formula's report step requires an
		// Anomalies field summarizing the whole cycle — a report that only ever
		// surfaces scan-time anomalies drops the ones reap/purge/auto-close find
		// (dangling refs, dolt commit failures, archive stalls) the same way the
		// prose-driven Dog report did in hq-2u0.
		var allAnomalies []reaper.Anomaly

		for i, dbName := range databases {
			if err := waitBeforeReaperDatabase(i); err != nil {
				return err
			}
			if err := reaper.ValidateDBName(dbName); err != nil {
				fmt.Printf("skip invalid db: %s\n", dbName)
				continue
			}

			db, err := reaper.OpenDB(reaperHost, reaperPort, dbName, 30*time.Second, 30*time.Second)
			if err != nil {
				fmt.Printf("%s: connect error: %v\n", dbName, err)
				continue
			}

			if ok, err := reaper.HasReaperSchema(db); err != nil {
				fmt.Printf("%s: schema check error: %v\n", dbName, err)
				db.Close()
				continue
			} else if !ok {
				fmt.Printf("%s: skipped (no reaper schema)\n", dbName)
				db.Close()
				continue
			}

			// Scan
			scanResult, err := reaper.Scan(db, dbName, maxAge, purgeAge, mailAge, staleAge)
			if err != nil {
				fmt.Printf("%s: scan error: %v\n", dbName, err)
				db.Close()
				continue
			}
			for _, a := range scanResult.Anomalies {
				fmt.Printf("%s: %s %s\n", dbName, style.Warning.Render("ANOMALY:"), a.Message)
			}
			allAnomalies = append(allAnomalies, scanResult.Anomalies...)

			// Reap
			reapResult, err := reaper.Reap(db, dbName, maxAge, reaperDryRun)
			if err != nil {
				fmt.Printf("%s: reap error: %v\n", dbName, err)
			} else {
				totalReaped += reapResult.Reaped
				totalMoleculeSteps += reapResult.MoleculeStepsClosed
				totalOpen += reapResult.OpenRemain
				for _, a := range reapResult.Anomalies {
					fmt.Printf("%s: %s %s\n", dbName, style.Warning.Render("ANOMALY:"), a.Message)
				}
				allAnomalies = append(allAnomalies, reapResult.Anomalies...)
			}

			// Purge
			purgeResult, err := reaper.Purge(db, dbName, purgeAge, mailAge, reaperDryRun, reaper.WithArchive(archive))
			if err != nil {
				fmt.Printf("%s: purge error: %v\n", dbName, err)
			} else {
				totalPurged += purgeResult.WispsPurged
				totalMailPurged += purgeResult.MailPurged
				totalArchived += purgeResult.WispsArchived
				totalProtected += purgeResult.WispsProtected
				for _, a := range purgeResult.Anomalies {
					fmt.Printf("%s: %s %s\n", dbName, style.Warning.Render("ANOMALY:"), a.Message)
				}
				allAnomalies = append(allAnomalies, purgeResult.Anomalies...)
			}

			// Auto-close
			closeResult, err := reaper.AutoClose(db, dbName, staleAge, reaperDryRun)
			if err != nil {
				fmt.Printf("%s: auto-close error: %v\n", dbName, err)
			} else {
				for _, entry := range closeResult.ClosedEntries {
					fmt.Printf("  %s %s (%dd stale, db:%s)\n",
						entry.ID, entry.Title, entry.AgeDays, entry.Database)
				}
				totalClosed += closeResult.Closed
				for _, a := range closeResult.Anomalies {
					fmt.Printf("%s: %s %s\n", dbName, style.Warning.Render("ANOMALY:"), a.Message)
				}
				allAnomalies = append(allAnomalies, closeResult.Anomalies...)
			}

			// Acked mail: delivery-acked gt:message beads never read, exempt
			// from the general sweep above by AutoCloseExemptLabels (gt-ljun).
			ackedMailResult, err := reaper.AutoCloseAckedMail(db, dbName, staleAge, reaperDryRun)
			if err != nil {
				fmt.Printf("%s: auto-close acked mail error: %v\n", dbName, err)
			} else {
				for _, entry := range ackedMailResult.ClosedEntries {
					fmt.Printf("  %s %s (%dd stale, db:%s, acked mail)\n",
						entry.ID, entry.Title, entry.AgeDays, entry.Database)
				}
				totalClosed += ackedMailResult.Closed
			}

			db.Close()
		}

		// Convoy check (formula step 5): close convoys whose tracked beads are
		// all closed. Town-level, not per-database, so it runs once after the
		// database loop rather than inside it.
		totalConvoysClosed := 0
		if townBeads, err := getTownBeadsDir(); err != nil {
			fmt.Printf("convoy check: %v\n", err)
		} else if closed, err := checkAndCloseCompletedConvoys(townBeads, reaperDryRun); err != nil {
			fmt.Printf("convoy check: %v\n", err)
		} else {
			totalConvoysClosed = len(closed)
		}

		// Report. All required fields below are printed unconditionally
		// (including Anomalies and Convoys closed, even when zero) so this
		// fallback report can't degrade into a partial one the way the
		// prose-driven Dog report did in hq-2u0 — a silent cycle must be
		// indistinguishable from a clean one only by its VALUES, never by a
		// missing field.
		prefix := ""
		if reaperDryRun {
			prefix = "[DRY RUN] "
		}
		fmt.Printf("\n%sReaper cycle complete:\n", prefix)
		fmt.Printf("  Databases scanned:      %d\n", len(databases))
		fmt.Printf("  Wisps reaped:           %d", totalReaped)
		if totalMoleculeSteps > 0 {
			fmt.Printf(" (+%d closed-molecule steps)", totalMoleculeSteps)
		}
		fmt.Println()
		fmt.Printf("  Wisps purged:           %d\n", totalPurged)
		fmt.Printf("  Mail purged:            %d\n", totalMailPurged)
		if totalArchived > 0 && archive != nil {
			fmt.Printf("  Archived:               %d wisps → %s\n", totalArchived, archive.Location())
		}
		if totalProtected > 0 {
			fmt.Printf("  Protected:              %d wisps (pinned, or protected label with no archive)\n", totalProtected)
		}
		fmt.Printf("  Issues auto-closed:     %d\n", totalClosed)
		fmt.Printf("  Convoys closed:         %d\n", totalConvoysClosed)
		fmt.Printf("  Open wisps remaining:   %d\n", totalOpen)
		if len(allAnomalies) == 0 {
			fmt.Printf("  Anomalies:              none\n")
		} else {
			fmt.Printf("  Anomalies:              %d\n", len(allAnomalies))
			for _, a := range allAnomalies {
				fmt.Printf("    - %s\n", a.Message)
			}
		}

		return nil
	},
}

// reaperArchiveCmd is the read half of retention. An archive nobody can query
// is a hole with a nicer name: the record was kept so that somebody looking for
// a merge that did not land, months later, can find the rejection rationale.
var reaperArchiveCmd = &cobra.Command{
	Use:   "archive",
	Short: "Search wisps the purge archived before deleting",
	Long: `Read the durable archive of wisps that purge exported before deleting.

Protected wisp types (merge requests, escalations) are written here as JSON
Lines before their rows are removed from Dolt, so the record outlives the row.

  gt reaper archive                        # most recent records
  gt reaper archive --db gastown           # one database
  gt reaper archive --grep gt-6xwt         # id, title, description, close reason
  gt reaper archive --id gt-wisp-abc123    # one record in full
  gt reaper archive --json                 # machine-readable`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := reaperArchiveLocation()
		scan, err := reaper.ReadArchive(dir, reaper.ArchiveFilter{
			Database: reaperDB,
			ID:       reaperArchiveID,
			Contains: reaperArchiveGrep,
			Limit:    reaperArchiveLmt,
		})
		if err != nil {
			return err
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(scan))
			return nil
		}

		// A corrupt archive must not read as an empty one.
		if scan.Malformed > 0 {
			fmt.Fprintf(os.Stderr, "WARNING: %d unreadable line(s) in %s\n", scan.Malformed, dir)
		}
		if len(scan.Records) == 0 {
			fmt.Printf("No archived wisps in %s (%d file(s) read)\n", dir, scan.Files)
			return nil
		}

		// A single record is a lookup, and the description is the point of it:
		// an MR's branch, target and source_issue live in there as key: value
		// lines, not in columns.
		if reaperArchiveID != "" {
			for _, rec := range scan.Records {
				printArchivedWisp(rec)
			}
			return nil
		}

		for _, rec := range scan.Records {
			closed := "-"
			if rec.ClosedAt != nil {
				closed = rec.ClosedAt.Format("2006-01-02")
			}
			fmt.Printf("%s  %-24s  %-10s  %s\n", closed, rec.ID, rec.Database, rec.Title)
		}
		fmt.Printf("\n%s %d record(s) from %d file(s) in %s\n",
			style.Dim.Render("Archive:"), len(scan.Records), scan.Files, dir)
		return nil
	},
}

func printArchivedWisp(rec reaper.ArchivedWisp) {
	fmt.Printf("%s  (%s)\n", rec.ID, rec.Database)
	fmt.Printf("  Title:        %s\n", rec.Title)
	fmt.Printf("  Status:       %s\n", rec.Status)
	if rec.WispType != "" {
		fmt.Printf("  Wisp type:    %s\n", rec.WispType)
	}
	if len(rec.Labels) > 0 {
		fmt.Printf("  Labels:       %s\n", strings.Join(rec.Labels, ", "))
	}
	if rec.Assignee != "" {
		fmt.Printf("  Assignee:     %s\n", rec.Assignee)
	}
	if rec.ClosedAt != nil {
		fmt.Printf("  Closed:       %s\n", rec.ClosedAt.Format(time.RFC3339))
	}
	fmt.Printf("  Archived:     %s\n", rec.ArchivedAt.Format(time.RFC3339))
	if rec.CloseReason != "" {
		fmt.Printf("  Close reason: %s\n", rec.CloseReason)
	}
	if rec.Description != "" {
		fmt.Printf("\n%s\n", rec.Description)
	}
	for _, c := range rec.Comments {
		fmt.Printf("\n  --- comment ---\n  %s\n", strings.ReplaceAll(c, "\n", "\n  "))
	}
	printArchivedEvents(rec)
	fmt.Println()
}

// printArchivedEvents renders the event history, and says so when there is none
// to render.
//
// The empty and the missing case print DIFFERENT lines on purpose (gt-wv8h).
// Records written before events were archived carry no key at all, and their
// history is gone with the row — an operator reading such a record has to know
// that "no events" is a fact about the writer, not about the wisp. Since the
// JSON decodes both to a nil slice, the distinction is not recoverable per
// record after the fact, so the line names both readings rather than picking
// one.
func printArchivedEvents(rec reaper.ArchivedWisp) {
	if len(rec.Events) == 0 {
		fmt.Printf("\n  --- events ---\n  none recorded (this wisp had none, or the record " +
			"predates gt-wv8h and its history was deleted with the row)\n")
		return
	}
	fmt.Printf("\n  --- events (%d) ---\n", len(rec.Events))
	for _, e := range rec.Events {
		when := "-"
		if e.CreatedAt != nil {
			when = e.CreatedAt.Format(time.RFC3339)
		}
		// old -> new is the whole content of a status_changed row, and the wisps
		// row it was deleted alongside kept only the final value.
		transition := ""
		if e.OldValue != "" || e.NewValue != "" {
			transition = fmt.Sprintf("  %s -> %s", e.OldValue, e.NewValue)
		}
		fmt.Printf("  %s  %-16s %s%s\n", when, e.EventType, e.Actor, transition)
		if e.Comment != "" {
			fmt.Printf("      %s\n", strings.ReplaceAll(e.Comment, "\n", "\n      "))
		}
	}
}

func init() {
	// Shared flags
	// GH#2601: Default host/port from GT/town config for non-localhost setups.
	// BEADS_DOLT_* aliases are intentionally ignored because they are derived bd
	// client outputs, not endpoint authority.
	defaultHost, defaultPort := defaultReaperEndpoint()

	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperReapCmd, reaperPurgeCmd, reaperAutoCloseCmd, reaperRunCmd, reaperDatabasesCmd} {
		cmd.Flags().StringVar(&reaperDB, "db", "", "Database name (required for single-db commands)")
		cmd.Flags().StringVar(&reaperHost, "host", defaultHost, "Dolt server host (env: GT_DOLT_HOST)")
		cmd.Flags().IntVar(&reaperPort, "port", defaultPort, "Dolt server port (env: GT_DOLT_PORT)")
		cmd.Flags().BoolVar(&reaperDryRun, "dry-run", false, "Report what would happen without acting")
	}
	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperReapCmd, reaperPurgeCmd, reaperAutoCloseCmd, reaperRunCmd} {
		cmd.Flags().StringVar(&reaperDBDelay, "db-delay", "250ms", "Delay between databases to reduce Dolt load")
	}

	// JSON output flag for single-db commands
	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperReapCmd, reaperPurgeCmd, reaperAutoCloseCmd, reaperDatabasesCmd} {
		cmd.Flags().BoolVar(&reaperJSON, "json", false, "Output as JSON")
	}

	// Threshold flags
	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperReapCmd, reaperRunCmd} {
		cmd.Flags().StringVar(&reaperMaxAge, "max-age", "24h", "Max wisp age before reaping")
	}
	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperPurgeCmd, reaperRunCmd} {
		cmd.Flags().StringVar(&reaperPurgeAge, "purge-age", "168h", "Max closed wisp age before purging (7d)")
		cmd.Flags().StringVar(&reaperMailAge, "mail-age", "168h", "Max closed mail age before purging (7d)")
	}
	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperAutoCloseCmd, reaperRunCmd} {
		cmd.Flags().StringVar(&reaperStaleAge, "stale-age", "720h", "Max issue staleness before auto-close (30d)")
	}

	// Retention flags (gt-6xwt). --archive-dir is shared with the read command
	// so a non-default archive is read back from where it was written.
	for _, cmd := range []*cobra.Command{reaperPurgeCmd, reaperRunCmd, reaperArchiveCmd} {
		cmd.Flags().StringVar(&reaperArchiveDir, "archive-dir", "",
			fmt.Sprintf("Directory for archived wisp records (default %s, env: %s)",
				reaper.DefaultArchiveDir(), reaper.ArchiveDirEnv))
	}
	for _, cmd := range []*cobra.Command{reaperPurgeCmd, reaperRunCmd} {
		cmd.Flags().BoolVar(&reaperNoArchive, "no-archive", false,
			"Keep protected wisps in the database instead of archiving and releasing them")
	}
	reaperArchiveCmd.Flags().StringVar(&reaperDB, "db", "", "Only records from this database")
	reaperArchiveCmd.Flags().StringVar(&reaperArchiveID, "id", "", "Show one archived wisp in full")
	reaperArchiveCmd.Flags().StringVar(&reaperArchiveGrep, "grep", "", "Substring match on id, title, description, close reason")
	reaperArchiveCmd.Flags().IntVar(&reaperArchiveLmt, "limit", 50, "Maximum records to show (0 = no limit)")
	reaperArchiveCmd.Flags().BoolVar(&reaperJSON, "json", false, "Output as JSON")

	reaperCmd.AddCommand(reaperDatabasesCmd)
	reaperCmd.AddCommand(reaperScanCmd)
	reaperCmd.AddCommand(reaperReapCmd)
	reaperCmd.AddCommand(reaperPurgeCmd)
	reaperCmd.AddCommand(reaperAutoCloseCmd)
	reaperCmd.AddCommand(reaperRunCmd)
	reaperCmd.AddCommand(reaperArchiveCmd)

	rootCmd.AddCommand(reaperCmd)
}
