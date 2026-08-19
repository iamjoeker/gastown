package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmpgc"
	"github.com/steveyegge/gastown/internal/util"
)

var (
	sweepTmpDryRun bool
	sweepTmpJSON   bool
	sweepTmpMinAge time.Duration
	sweepTmpDir    string
)

var deaconSweepTmpCmd = &cobra.Command{
	Use:   "sweep-tmp",
	Short: "Reclaim orphaned build and test scratch directories from TMPDIR",
	Long: `Reclaim orphaned build and test scratch directories from TMPDIR.

Two families strand directories under TMPDIR on a gastown host:

  go-work     Go toolchain work directories (go-build<n>, go-link-<n>). The
              toolchain creates one for every build, test and vet run and
              removes it on normal exit; a KILLED run — OOM killer, test
              timeout, session recycle — leaves it behind at 100-300 MB each.
  beads-test  beads test fixtures (beads-test-dolt-*, beads-bd-tests-*). A
              suite killed mid-flight leaves its Dolt data directory behind.

Where TMPDIR is a RAM-backed tmpfs, this is not a cosmetic leak. On the host
that produced gt-yb33, 34 stranded work directories held 5.35 GB of a 31 GB
tmpfs, and gastown's own disk-space guard began refusing polecat creation:

    AddWithOptions: insufficient disk space: CRITICAL: only 1.2 GB free

while "df /" still reported 1.3 TB free, because /tmp is a different
filesystem. The failure names disk space rather than the test, so it reads as a
code regression to whoever hits it next.

This command removes ONLY directories that pass every check, and refuses
whenever a check cannot be answered:

  - the name is exactly a member of one of the families above
  - it is a real directory, not a symlink, owned by you
  - the whole tree is readable — an unreadable subtree refuses the candidate
  - nothing in the tree was modified within --min-age (default 1h)
  - no running process is using it: naming it in its argv (how a live build
    identifies its work directory), having it as its working directory, or
    holding an open file descriptor inside it (how a live dolt sql-server
    holds a fixture)

If the process table cannot be read, NOTHING is removed however old the
directories are. Absence of evidence is not permission to delete.

Liveness is read from /proc, not lsof. lsof answers through an exit status
that is non-zero for "nothing is open", for "I could not read that", for "I am
not installed", and — on hosts with docker nsfs mounts — for every single
invocation including ones that DID list live handles (gt-32z). /proc has no
status to misread.

Age and process evidence cover each other: the Go driver creates its work
directory before it spawns the first compile, so for the first seconds of a
build no process names it yet. A removing sweep therefore refuses a --min-age
under 5m; --dry-run accepts any value because it deletes nothing.

It never touches /tmp/claude-1000 agent scratchpads, PID files, or anything
else it did not positively identify as a member of a known family.

Examples:
  gt deacon sweep-tmp --dry-run    # report what is stranded, remove nothing
  gt deacon sweep-tmp              # reclaim orphans idle for over an hour
  gt deacon sweep-tmp --min-age 4h # be more conservative
  gt deacon sweep-tmp --json       # machine-readable, for patrol digests`,
	RunE: runDeaconSweepTmp,
}

func init() {
	deaconSweepTmpCmd.Flags().BoolVar(&sweepTmpDryRun, "dry-run", false,
		"Report reclaimable directories without removing them")
	deaconSweepTmpCmd.Flags().BoolVar(&sweepTmpJSON, "json", false, "Output as JSON")
	deaconSweepTmpCmd.Flags().DurationVar(&sweepTmpMinAge, "min-age", tmpgc.DefaultMinAge,
		"How long a directory's tree must be untouched before it is eligible")
	deaconSweepTmpCmd.Flags().StringVar(&sweepTmpDir, "dir", "",
		"Directory to sweep (default: TMPDIR)")

	deaconCmd.AddCommand(deaconSweepTmpCmd)
}

func runDeaconSweepTmp(cmd *cobra.Command, args []string) error {
	res, err := tmpgc.Sweep(tmpgc.Options{
		Dir:    sweepTmpDir,
		MinAge: sweepTmpMinAge,
		DryRun: sweepTmpDryRun,
	})
	if err != nil {
		return fmt.Errorf("sweeping temp directory: %w", err)
	}

	if sweepTmpJSON {
		out, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding result: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	printTmpSweepResult(res)
	return nil
}

func printTmpSweepResult(res *tmpgc.Result) {
	prefix := ""
	if res.DryRun {
		prefix = "[DRY RUN] "
	}

	var live, young, refused int
	for _, c := range res.Candidates {
		switch c.Status {
		case tmpgc.StatusLive:
			live++
		case tmpgc.StatusYoung:
			young++
		case tmpgc.StatusRefused:
			refused++
		}
	}

	fmt.Printf("%sScratch directories in %s: %d (%s)\n", prefix, res.Dir,
		len(res.Candidates), strings.Join(res.Families, ", "))
	for _, c := range res.Candidates {
		switch {
		case c.Removed:
			fmt.Printf("  %s %s (%s, idle %s)\n", style.Success.Render("✓ reclaimed"),
				c.Path, util.FormatBytesHuman(c.SizeBytes), formatSweepAge(c.Age))
		case c.Status == tmpgc.StatusReclaimable:
			fmt.Printf("  %s %s (%s, idle %s)\n", style.Warning.Render("· reclaimable"),
				c.Path, util.FormatBytesHuman(c.SizeBytes), formatSweepAge(c.Age))
		default:
			fmt.Printf("  %s %s (%s): %s\n", style.Dim.Render("- kept"),
				c.Path, util.FormatBytesHuman(c.SizeBytes), c.Reason)
		}
	}

	if res.Inconclusive {
		fmt.Printf("\n%s Removed nothing: liveness evidence unavailable\n",
			style.Warning.Render("⚠"))
	}
	// Naming the gap is the point. A sweep that silently dropped part of its
	// coverage would print the same clean summary as one that saw everything.
	if res.OpaqueProcesses > 0 {
		fmt.Printf("\n%s %d running process(es) would not publish their working directory or\n"+
			"  open files, so only their command lines were searched. The quiet period is\n"+
			"  what covers them.\n",
			style.Dim.Render("note:"), res.OpaqueProcesses)
	}
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "  %s %s\n", style.Warning.Render("⚠"), e)
	}

	fmt.Printf("\n%sSwept: reclaimed=%d bytes=%s reclaimable=%s live=%d young=%d refused=%d\n",
		prefix, res.Removed, util.FormatBytesHuman(res.RemovedBytes),
		util.FormatBytesHuman(res.ReclaimableBytes), live, young, refused)

	if info, err := util.GetDiskSpace(res.Dir); err == nil {
		fmt.Printf("%s: %s free of %s (%.1f%% used)\n", res.Dir,
			info.AvailableHuman(), util.FormatBytesHuman(info.TotalBytes), info.UsedPercent)
	}
}

func formatSweepAge(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}
