package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/scratchpad"
	"github.com/steveyegge/gastown/internal/util"
)

var (
	sweepScratchpadsRoot      string
	sweepScratchpadsIdle      time.Duration
	sweepScratchpadsMinAge    time.Duration
	sweepScratchpadsHighWater float64
	sweepScratchpadsTarget    float64
	sweepScratchpadsAll       bool
	sweepScratchpadsApply     bool
	sweepScratchpadsJSON      bool
	sweepScratchpadsVerbose   bool
)

var deaconSweepScratchpadsCmd = &cobra.Command{
	Use:   "sweep-scratchpads",
	Short: "Reclaim scratchpad directories of provably dead agent sessions",
	Long: `Reclaim scratchpad directories of provably dead agent sessions.

Every Claude Code session gets a private working directory at
$TMPDIR/claude-<uid>/<project-slug>/<session-id>/, and nothing removes it when
the session ends. On a busy box they fill the /tmp tmpfs until unrelated work
starts failing on "insufficient disk space" (gt-yb33).

Deleting by age alone is both unsafe and ineffective. Unsafe because a live
session's scratchpad can sit untouched for hours while the agent is idle, and
losing it mid-task is unrecoverable and invisible until the agent trips over a
file it wrote itself. Ineffective because the bulk of the bytes are hours old,
not days: on the box that motivated this, a 24h retention would have reclaimed
2 GB of 14 GB.

So a scratchpad is removed only when every one of these holds:

  - its filesystem birth time is known (unknown birth means possibly live);
  - no live claude process could own it: for every live process attributed to
    the same project, the directory predates that process's start;
  - no live process is working inside it;
  - it is older than --min-age, the forensic floor;
  - nothing in it has been written for --idle;
  - its transcript under ~/.claude*/projects/ is absent or quiet for --idle,
    which is what catches a resumed session reusing an old session id.

Selection is driven by filesystem pressure, not by age: below --high-water
nothing is deleted at all, and above it the oldest dead scratchpads go first,
stopping as soon as usage is projected back under --target. Loose files sitting
directly under the root belong to no session and are only ever reported.

The sweep is a dry run unless --apply is given.

Examples:
  gt deacon sweep-scratchpads                 # report what would be reclaimed
  gt deacon sweep-scratchpads --apply         # reclaim if above the high-water mark
  gt deacon sweep-scratchpads --all --apply   # reclaim every dead scratchpad
  gt deacon sweep-scratchpads --json          # machine-readable report`,
	RunE: runDeaconSweepScratchpads,
}

func init() {
	deaconCmd.AddCommand(deaconSweepScratchpadsCmd)

	f := deaconSweepScratchpadsCmd.Flags()
	f.StringVar(&sweepScratchpadsRoot, "root", "", "Scratchpad root (default $TMPDIR/claude-<uid>)")
	f.DurationVar(&sweepScratchpadsIdle, "idle", 0, "How long a session must be quiet before it counts as dead (default 2h)")
	f.DurationVar(&sweepScratchpadsMinAge, "min-age", 0, "Forensic floor: never sweep a scratchpad younger than this (default 2h)")
	f.Float64Var(&sweepScratchpadsHighWater, "high-water", 0, "Filesystem usage percent that triggers a sweep (default 80)")
	f.Float64Var(&sweepScratchpadsTarget, "target", 0, "Filesystem usage percent to sweep down to (default 60)")
	f.BoolVar(&sweepScratchpadsAll, "all", false, "Sweep every dead scratchpad, ignoring the high-water and target marks")
	f.BoolVar(&sweepScratchpadsApply, "apply", false, "Actually delete (default is a dry run)")
	f.BoolVar(&sweepScratchpadsJSON, "json", false, "Output the report as JSON")
	f.BoolVar(&sweepScratchpadsVerbose, "verbose", false, "List every scratchpad and why it was kept or swept")
}

// sweepScratchpadsReport is the JSON shape of the report.
type sweepScratchpadsReport struct {
	Root           string                     `json:"root"`
	Now            time.Time                  `json:"now"`
	Applied        bool                       `json:"applied"`
	Triggered      bool                       `json:"triggered"`
	UsedPercent    float64                    `json:"used_percent"`
	HighWaterPct   float64                    `json:"high_water_percent"`
	TargetPct      float64                    `json:"target_percent"`
	TotalBytes     uint64                     `json:"filesystem_total_bytes"`
	UsedBytes      uint64                     `json:"filesystem_used_bytes"`
	Sessions       int                        `json:"sessions"`
	SessionBytes   int64                      `json:"session_bytes"`
	Sweepable      int                        `json:"sweepable"`
	SweepableBytes int64                      `json:"sweepable_bytes"`
	Selected       int                        `json:"selected"`
	SelectedBytes  int64                      `json:"selected_bytes"`
	Held           int                        `json:"held_for_forensics"`
	HeldBytes      int64                      `json:"held_for_forensics_bytes"`
	Removed        int                        `json:"removed"`
	RemovedBytes   int64                      `json:"removed_bytes"`
	Skipped        []sweepScratchpadsSkip     `json:"skipped,omitempty"`
	StrayFiles     int                        `json:"stray_files"`
	StrayBytes     int64                      `json:"stray_bytes"`
	LiveProcesses  int                        `json:"live_processes"`
	Unattributable int                        `json:"unattributable_processes"`
	Entries        []sweepScratchpadsEntry    `json:"entries,omitempty"`
	KeepReasons    []sweepScratchpadsCategory `json:"keep_reasons,omitempty"`
}

type sweepScratchpadsEntry struct {
	Path      string    `json:"path"`
	Project   string    `json:"project"`
	Session   string    `json:"session"`
	Verdict   string    `json:"verdict"`
	Reason    string    `json:"reason"`
	Bytes     int64     `json:"bytes"`
	LastWrite time.Time `json:"last_write"`
}

type sweepScratchpadsSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type sweepScratchpadsCategory struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	Bytes  int64  `json:"bytes"`
}

func runDeaconSweepScratchpads(cmd *cobra.Command, args []string) error {
	now := time.Now()

	policy := scratchpad.DefaultPolicy()
	if sweepScratchpadsIdle > 0 {
		policy.Idle = sweepScratchpadsIdle
	}
	if sweepScratchpadsMinAge > 0 {
		policy.MinAge = sweepScratchpadsMinAge
	}
	if sweepScratchpadsHighWater > 0 {
		policy.HighWater = sweepScratchpadsHighWater / 100
	}
	if sweepScratchpadsTarget > 0 {
		policy.Target = sweepScratchpadsTarget / 100
	}
	if policy.Target > policy.HighWater {
		return fmt.Errorf("--target (%.0f%%) must not exceed --high-water (%.0f%%)",
			policy.Target*100, policy.HighWater*100)
	}

	root := sweepScratchpadsRoot
	if root == "" {
		root = scratchpad.DefaultRoot()
	}

	survey, err := scratchpad.Take(root, os.Getenv("HOME"), policy, now)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no scratchpad root at %s — nothing to sweep", root)
		}
		return err
	}
	scan, decisions, procs := survey.Scan, survey.Decisions, survey.Processes

	disk, err := util.GetDiskSpace(root)
	if err != nil {
		return fmt.Errorf("reading filesystem usage for %s: %w", root, err)
	}
	selection := scratchpad.Select(decisions, disk.TotalBytes, disk.UsedBytes, policy, sweepScratchpadsAll)

	report := buildSweepScratchpadsReport(root, now, scan, decisions, selection, procs, disk, policy)

	if sweepScratchpadsApply && len(selection.Selected) > 0 {
		report.Applied = true
		for _, r := range scratchpad.Execute(selection.Selected, policy, time.Now()) {
			if r.Removed {
				report.Removed++
				report.RemovedBytes += r.Session.Bytes
				continue
			}
			report.Skipped = append(report.Skipped, sweepScratchpadsSkip{Path: r.Session.Path, Reason: r.Reason})
		}
	}

	if sweepScratchpadsJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	printSweepScratchpadsReport(cmd, report)
	return nil
}

func buildSweepScratchpadsReport(
	root string,
	now time.Time,
	scan *scratchpad.ScanResult,
	decisions []scratchpad.Decision,
	selection scratchpad.Selection,
	procs []scratchpad.Process,
	disk *util.DiskSpaceInfo,
	policy scratchpad.Policy,
) *sweepScratchpadsReport {
	r := &sweepScratchpadsReport{
		Root:          root,
		Now:           now,
		Triggered:     selection.Triggered,
		UsedPercent:   selection.UsedPercent,
		HighWaterPct:  policy.HighWater * 100,
		TargetPct:     policy.Target * 100,
		TotalBytes:    disk.TotalBytes,
		UsedBytes:     disk.UsedBytes,
		Sessions:      len(decisions),
		Selected:      len(selection.Selected),
		SelectedBytes: selection.Bytes,
		Held:          selection.Held,
		HeldBytes:     selection.HeldBytes,
		StrayFiles:    scan.StrayFiles,
		StrayBytes:    scan.StrayBytes,
		LiveProcesses: len(procs),
	}
	for _, p := range procs {
		if !p.Attributable() {
			r.Unattributable++
		}
	}

	keepBuckets := map[string]*sweepScratchpadsCategory{}
	for _, d := range decisions {
		r.SessionBytes += d.Session.Bytes
		if d.Verdict == scratchpad.VerdictSweep {
			r.Sweepable++
			r.SweepableBytes += d.Session.Bytes
		} else {
			key := keepCategory(d.Reason)
			b, ok := keepBuckets[key]
			if !ok {
				b = &sweepScratchpadsCategory{Reason: key}
				keepBuckets[key] = b
			}
			b.Count++
			b.Bytes += d.Session.Bytes
		}
		if sweepScratchpadsVerbose {
			r.Entries = append(r.Entries, sweepScratchpadsEntry{
				Path:      d.Session.Path,
				Project:   d.Session.ProjectSlug,
				Session:   d.Session.ID,
				Verdict:   string(d.Verdict),
				Reason:    d.Reason,
				Bytes:     d.Session.Bytes,
				LastWrite: d.Session.LastWrite,
			})
		}
	}
	for _, b := range keepBuckets {
		r.KeepReasons = append(r.KeepReasons, *b)
	}
	sort.Slice(r.KeepReasons, func(i, j int) bool {
		if r.KeepReasons[i].Count != r.KeepReasons[j].Count {
			return r.KeepReasons[i].Count > r.KeepReasons[j].Count
		}
		return r.KeepReasons[i].Reason < r.KeepReasons[j].Reason
	})
	return r
}

// keepCategory collapses a per-session keep reason, which names pids and
// durations, into the class of reason so the summary counts something.
func keepCategory(reason string) string {
	switch {
	case strings.Contains(reason, "working inside it"):
		return "a live process is working inside it"
	case strings.Contains(reason, "started before this session"):
		return "a live claude process could own it"
	case strings.Contains(reason, "forensic floor"):
		return "younger than the forensic floor"
	case strings.Contains(reason, "idle window"):
		return "written too recently"
	case strings.Contains(reason, "transcript"):
		return "transcript still active (possibly resumed)"
	case strings.Contains(reason, "birth time unavailable"):
		return "birth time unavailable"
	default:
		return reason
	}
}

func printSweepScratchpadsReport(cmd *cobra.Command, r *sweepScratchpadsReport) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Scratchpad retention — %s\n", r.Root)
	fmt.Fprintf(out, "  filesystem: %s of %s used (%.1f%%), high-water %.0f%%, target %.0f%%\n",
		util.FormatBytesHuman(r.UsedBytes), util.FormatBytesHuman(r.TotalBytes),
		r.UsedPercent, r.HighWaterPct, r.TargetPct)
	fmt.Fprintf(out, "  sessions:   %d holding %s\n", r.Sessions, util.FormatBytesHuman(uint64(r.SessionBytes)))
	fmt.Fprintf(out, "  live procs: %d (%d with an unreadable project, treated as owning everything newer)\n",
		r.LiveProcesses, r.Unattributable)
	if r.StrayFiles > 0 {
		fmt.Fprintf(out, "  stray:      %d loose files holding %s — no session owns these, reported only\n",
			r.StrayFiles, util.FormatBytesHuman(uint64(r.StrayBytes)))
	}

	fmt.Fprintf(out, "\nDead: %d scratchpads holding %s\n", r.Sweepable, util.FormatBytesHuman(uint64(r.SweepableBytes)))
	fmt.Fprintf(out, "Kept: %d\n", r.Sessions-r.Sweepable)
	for _, k := range r.KeepReasons {
		fmt.Fprintf(out, "  %5d  %8s  %s\n", k.Count, util.FormatBytesHuman(uint64(k.Bytes)), k.Reason)
	}

	fmt.Fprintln(out)
	switch {
	case r.Selected == 0 && !r.Triggered && r.Sweepable > 0:
		fmt.Fprintf(out, "Below the high-water mark — keeping all %d dead scratchpads (%s) for forensics.\n",
			r.Held, util.FormatBytesHuman(uint64(r.HeldBytes)))
		fmt.Fprintf(out, "Use --all to reclaim them anyway.\n")
	case r.Selected == 0:
		fmt.Fprintf(out, "Nothing to reclaim.\n")
	default:
		verb := "Would reclaim"
		if r.Applied {
			verb = "Reclaiming"
		}
		fmt.Fprintf(out, "%s %d scratchpads holding %s (oldest first), leaving %d (%s) for forensics.\n",
			verb, r.Selected, util.FormatBytesHuman(uint64(r.SelectedBytes)),
			r.Held, util.FormatBytesHuman(uint64(r.HeldBytes)))
	}

	if r.Applied {
		fmt.Fprintf(out, "Removed %d scratchpads, freeing %s.\n", r.Removed, util.FormatBytesHuman(uint64(r.RemovedBytes)))
		for _, s := range r.Skipped {
			fmt.Fprintf(out, "  skipped %s: %s\n", s.Path, s.Reason)
		}
	} else if r.Selected > 0 {
		fmt.Fprintf(out, "Dry run — re-run with --apply to delete.\n")
	}

	if sweepScratchpadsVerbose {
		fmt.Fprintln(out)
		for _, e := range r.Entries {
			fmt.Fprintf(out, "  %-5s %8s  %s/%s: %s\n", e.Verdict, util.FormatBytesHuman(uint64(e.Bytes)),
				e.Project, e.Session, e.Reason)
		}
	}
}
