package cmd

import (
	"testing"
	"time"
)

// TestDonePurgeArgsAlwaysBoundByAge is the regression guard for gt-fdj.
//
// `gt done` called `bd purge --force --quiet` with NO age. bd applies its age
// filter only `if olderThan != ""`, so --force without an age deletes EVERY
// closed ephemeral bead in the rig. Two `gt done` invocations on 2026-08-17
// destroyed seven closed MR beads, including a closed-but-UNMERGED refinery
// rejection ~11 minutes old (bd-czf's never-purge category). Wisps are
// unversioned and unbacked (hq-del4), so none of it was recoverable.
//
// THE ASYMMETRY THAT HID IT, and why this test exists separately from the one in
// internal/doltserver: purge.go gates on `DryRun: dryRun || !force`. The other
// call site (doltserver.PurgeClosedEphemerals) passes no --force and has
// therefore only ever PREVIEWED. Bounding that path changes nothing observable,
// so "we fixed the purge and deletions stopped" would have been a false
// confirmation — the fixed path was never the deleting one. This is the site
// that deletes.
//
// The assertion is on the ARGV. A test that greps this file for "--older-than"
// passes with the flag in a comment or a dead branch (gt-am7).
func TestDonePurgeArgsAlwaysBoundByAge(t *testing.T) {
	args := buildDonePurgeArgs()

	// --force is what makes this path destructive at all; if it ever disappears
	// the age becomes moot, but so does the purge. Assert the pair together.
	if !argvContains(args, "--force") {
		t.Fatalf("argv lost --force; this purge no longer deletes. argv=%v", args)
	}

	idx := -1
	for i, a := range args {
		if a == "--older-than" {
			idx = i
			break
		}
	}
	if idx == -1 {
		t.Fatalf("argv has --force but NO --older-than, so `gt done` deletes EVERY "+
			"closed ephemeral bead in the rig regardless of age (gt-fdj). argv=%v", args)
	}
	if idx == len(args)-1 {
		t.Fatalf("--older-than is the final element, so no value reaches bd. argv=%v", args)
	}

	val := args[idx+1]
	d, err := time.ParseDuration(val)
	if err != nil {
		t.Fatalf("--older-than value %q does not parse as a duration: %v", val, err)
	}
	if d < 24*time.Hour {
		t.Errorf("--older-than is %s, under the 24h floor. A short age still destroys "+
			"freshly-closed MR beads — bd-wisp-940 was ELEVEN MINUTES old. Should match "+
			"the reaper purge_age (%s).", d, donePurgeMinAge)
	}
}

// TestDonePurgeAgeMatchesOtherPaths pins the town's destructive paths together.
// The reaper's default purge_age and doltserver.purgeMinAge are both 168h; if
// these drift apart, one path deletes what another still considers live, and
// each looks correct in isolation.
func TestDonePurgeAgeMatchesOtherPaths(t *testing.T) {
	const townPurgeAge = "168h"
	if donePurgeMinAge != townPurgeAge {
		t.Errorf("donePurgeMinAge=%s but the reaper default and doltserver.purgeMinAge "+
			"are %s. Destructive paths must agree on 'old enough to delete'.",
			donePurgeMinAge, townPurgeAge)
	}
}

func argvContains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
