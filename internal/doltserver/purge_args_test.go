package doltserver

import (
	"strings"
	"testing"
	"time"
)

// TestPurgeArgsAlwaysBoundByAge is the regression guard for gt-5y7.
//
// On 2026-08-17 this call site issued `bd purge --json` with NO age argument.
// `bd purge` treats --older-than as OPTIONAL and, without it, deletes EVERY
// closed ephemeral bead regardless of age. Because PurgeClosedEphemerals runs on
// ordinary polecat lifecycle operations (nukePolecatFullWithOptions, dolt sync,
// maintain), starting eleven polecats destroyed seven closed MR beads on the
// beads rig — including a closed-but-UNMERGED refinery rejection ~11 minutes old,
// the one category bd-czf says must never be purged. Wisps are unversioned and
// unbacked (hq-del4), so none of it was recoverable.
//
// The assertion is on the ARGV, not on the source text. A test that greps this
// package for the string "--older-than" would pass while the flag sat in a
// comment, in dead code, or in a branch that never executes — which is the exact
// failure mode catalogued in gt-am7, where a suite of source-scanning tests kept
// an inert guard green for seven days across two incidents.
func TestPurgeArgsAlwaysBoundByAge(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		args := buildPurgeArgs(dryRun)

		idx := -1
		for i, a := range args {
			if a == "--older-than" {
				idx = i
				break
			}
		}
		if idx == -1 {
			t.Fatalf("dryRun=%v: argv has NO --older-than, so this purge is UNBOUNDED "+
				"and deletes every closed ephemeral bead regardless of age (gt-5y7). argv=%v",
				dryRun, args)
		}
		if idx == len(args)-1 {
			t.Fatalf("dryRun=%v: --older-than is the last element, so it carries no value "+
				"and bd will not receive an age. argv=%v", dryRun, args)
		}

		// The value must parse AND be non-trivial. "0" or "1s" would satisfy a
		// presence-only check while still deleting a rejection closed moments ago.
		val := args[idx+1]
		d, err := time.ParseDuration(val)
		if err != nil {
			t.Fatalf("dryRun=%v: --older-than value %q does not parse as a duration: %v",
				dryRun, val, err)
		}
		if d < 24*time.Hour {
			t.Errorf("dryRun=%v: --older-than is %s, under the 24h floor. A short age "+
				"still destroys freshly-closed MR beads, which is the gt-5y7 incident. "+
				"It should match the reaper's purge_age (%s).", dryRun, d, purgeMinAge)
		}

		if dryRun && !argvHas(args, "--dry-run") {
			t.Errorf("dryRun=true but --dry-run absent from argv=%v", args)
		}
		if !dryRun && argvHas(args, "--dry-run") {
			t.Errorf("dryRun=false but --dry-run present in argv=%v", args)
		}
	}
}

// TestPurgeMinAgeMatchesReaper pins the two destructive paths together. The
// reaper's default purge_age is 168h; if these ever disagree, one path deletes
// records the other still considers live, and the disagreement is invisible
// because each looks correct on its own.
func TestPurgeMinAgeMatchesReaper(t *testing.T) {
	const reaperDefaultPurgeAge = "168h"
	if purgeMinAge != reaperDefaultPurgeAge {
		t.Errorf("purgeMinAge=%s but the reaper's default purge_age is %s "+
			"(internal/cmd/reaper.go, --purge-age). Two destructive paths must not "+
			"disagree about what 'old enough to delete' means.", purgeMinAge, reaperDefaultPurgeAge)
	}
}

func argvHas(hay []string, needle string) bool {
	for _, s := range hay {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}
