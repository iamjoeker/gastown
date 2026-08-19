package doctor

import (
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/gastown/internal/scratchpad"
	"github.com/steveyegge/gastown/internal/tmpgc"
	"github.com/steveyegge/gastown/internal/util"
)

// TmpSpaceCheck verifies that TMPDIR has room, and reports how much of any
// shortfall is stranded Go build scratch space.
//
// DiskSpaceCheck measures the town root, which on a typical host is the root
// filesystem. TMPDIR is frequently a separate, much smaller, RAM-backed tmpfs,
// and it is where every `go build`, `go test`, git worktree operation and test
// fixture lands. When it fills, the symptom surfaces somewhere else entirely:
// polecat creation starts failing its disk-space guard with "insufficient disk
// space" while `df /` reports terabytes free, so the failure reads as a code
// regression rather than as a full filesystem (gt-yb33).
//
// Checking the town root alone cannot see that at all, which is why this is a
// separate check rather than another detail line on the existing one.
type TmpSpaceCheck struct {
	BaseCheck
}

// NewTmpSpaceCheck creates a new TMPDIR space check.
func NewTmpSpaceCheck() *TmpSpaceCheck {
	return &TmpSpaceCheck{
		BaseCheck: BaseCheck{
			CheckName:        "tmp-space",
			CheckDescription: "Check TMPDIR has sufficient free space",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// CanFix reports that this check can reclaim orphaned Go work directories.
func (c *TmpSpaceCheck) CanFix() bool { return true }

// Run checks free space in TMPDIR.
func (c *TmpSpaceCheck) Run(ctx *CheckContext) *CheckResult {
	dir := os.TempDir()

	info, err := util.GetDiskSpace(dir)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not check %s: %v", dir, err),
		}
	}
	level, msg, _ := util.CheckDiskSpace(dir)

	usage := fmt.Sprintf("%s free of %s (%.1f%% used)",
		info.AvailableHuman(), util.FormatBytesHuman(info.TotalBytes), info.UsedPercent)

	if level == util.DiskSpaceOK {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("%s: %s", dir, usage),
		}
	}

	details := []string{
		fmt.Sprintf("%s: %s", dir, usage),
		"A full TMPDIR blocks polecat creation, git worktrees, and every go build",
		"'df /' cannot see this: TMPDIR is often a separate, RAM-backed filesystem",
	}
	fixHint := "Free space in TMPDIR"

	// Name the reclaimable share, so the operator knows whether the sweep is
	// the answer or whether something else is holding the space.
	if scan, err := tmpgc.Scan(tmpgc.Options{}); err == nil {
		switch {
		case scan.ReclaimableBytes > 0:
			details = append(details, fmt.Sprintf(
				"%s is orphaned Go build scratch in %d work directories — run 'gt doctor --fix' or 'gt deacon sweep-tmp'",
				util.FormatBytesHuman(scan.ReclaimableBytes), countReclaimable(scan)))
			fixHint = "gt deacon sweep-tmp"
		case scan.Inconclusive:
			details = append(details, "Could not tell stranded Go build dirs from live ones; nothing is safe to sweep automatically")
		default:
			details = append(details,
				"No orphaned Go build directories: the space is held by something else (agent scratchpads, test fixtures)")
		}
	}

	// The other half of a full TMPDIR is dead agents' scratchpads, and on the
	// host that motivated this it was the larger half by a factor of two. Only
	// reported, never fixed here: proving a session dead is what
	// sweep-scratchpads does, and deleting another agent's working files is an
	// explicit call, not a side effect of `gt doctor --fix` (gt-h0jb).
	if survey, err := scratchpad.Take(scratchpad.DefaultRoot(), os.Getenv("HOME"), scratchpad.DefaultPolicy(), time.Now()); err == nil {
		if count, bytes := survey.Dead(); count > 0 {
			details = append(details, fmt.Sprintf(
				"%s is scratchpad space held by %d dead agent sessions — run 'gt deacon sweep-scratchpads' to review, then --apply",
				util.FormatBytesHuman(uint64(bytes)), count))
		}
	}

	status := StatusWarning
	if level == util.DiskSpaceCritical {
		status = StatusError
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  status,
		Message: msg, // already names the measured path
		Details: details,
		FixHint: fixHint,
	}
}

// Fix reclaims orphaned Go work directories from TMPDIR. It removes only
// directories that pass every one of tmpgc's checks and refuses whenever a
// check cannot be answered; it never touches agent scratchpads or fixtures.
func (c *TmpSpaceCheck) Fix(ctx *CheckContext) error {
	res, err := tmpgc.Sweep(tmpgc.Options{})
	if err != nil {
		return fmt.Errorf("sweeping %s: %w", os.TempDir(), err)
	}
	if res.Inconclusive {
		return fmt.Errorf("refused to sweep %s: liveness evidence unavailable", res.Dir)
	}
	if res.Removed == 0 {
		return fmt.Errorf("no orphaned Go work directories in %s: free space by other means", res.Dir)
	}
	return nil
}

func countReclaimable(res *tmpgc.Result) int {
	n := 0
	for _, c := range res.Candidates {
		if c.Status == tmpgc.StatusReclaimable {
			n++
		}
	}
	return n
}
