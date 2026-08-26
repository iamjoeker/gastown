package cmd

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/gcprotect"
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

// ---------------------------------------------------------------------------
// gt-x6yk: the purge must not be issued without bd's label guard in place
// ---------------------------------------------------------------------------

// recordingBD answers `bd config get`/`config set` and records every argv it is
// handed, so a test can assert what was run AND what was not.
type recordingBD struct {
	calls   [][]string
	value   string
	set     bool
	failGet bool
}

func (r *recordingBD) run(args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	switch {
	case len(args) >= 3 && args[0] == "config" && args[1] == "get":
		if r.failGet {
			return nil, fmt.Errorf("dolt: connection refused")
		}
		if !r.set {
			return []byte(args[2] + " (not set)\n"), nil
		}
		return []byte(r.value + "\n"), nil
	case len(args) >= 4 && args[0] == "config" && args[1] == "set":
		r.value, r.set = args[3], true
		return []byte("Set " + args[2] + " = " + args[3] + "\n"), nil
	}
	return []byte("0\n"), nil
}

func (r *recordingBD) ranPurge() bool {
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "purge" {
			return true
		}
	}
	return false
}

// TestPurgeIsGuardedBeforeItRuns pins the wiring, not the source text.
//
// `bd purge --force` spares only the labels in the target database's
// `gc.protected_labels`, which by default omits gt:escalation — the label
// reaper.ProtectedWispLabels holds and `gt compact` honours. On the deployed bd
// 1.2.2 an unpinned closed escalation wisp on hq probed purge_count 1 while a
// gt:message control on the same probe came back label_protected_skipped 1.
// Escalation wisps are unversioned, dolt-ignored and unarchived, so the guard
// has to be established BEFORE the argv is issued, not merely somewhere in the
// file.
func TestPurgeIsGuardedBeforeItRuns(t *testing.T) {
	r := &recordingBD{}
	purgeClosedEphemeralBeadsWith(r.run)

	if !r.ranPurge() {
		t.Fatalf("control failed: the purge was never issued at all, so the ordering "+
			"assertion below proves nothing. calls=%v", r.calls)
	}

	guardIdx, purgeIdx := -1, -1
	for i, c := range r.calls {
		if guardIdx == -1 && len(c) >= 3 && c[0] == "config" && c[2] == gcprotect.ConfigKey {
			guardIdx = i
		}
		if purgeIdx == -1 && len(c) > 0 && c[0] == "purge" {
			purgeIdx = i
		}
	}
	if guardIdx == -1 {
		t.Fatalf("`gt done` issued `bd purge --force` without ever touching %s, so "+
			"whichever database it lands in spares only bd's defaults and deletes "+
			"escalation records (gt-x6yk). calls=%v", gcprotect.ConfigKey, r.calls)
	}
	if guardIdx > purgeIdx {
		t.Errorf("%s was configured AFTER the purge ran (guard at %d, purge at %d); "+
			"the protection arrives too late to spare anything this run deleted. "+
			"calls=%v", gcprotect.ConfigKey, guardIdx, purgeIdx, r.calls)
	}

	if len(missing(parseProtected(r.value), gcprotect.Required())) > 0 {
		t.Errorf("guard installed %q, which does not cover %v",
			r.value, gcprotect.Required())
	}
}

// TestPurgeIsSkippedWhenTheGuardCannotBeConfirmed. Failing OPEN here would be
// the worst outcome available: the purge deletes, its protection was never
// established, and nothing in the output distinguishes that from a guarded run.
// Skipping costs accumulated wisp rows, which are visible and reversible.
func TestPurgeIsSkippedWhenTheGuardCannotBeConfirmed(t *testing.T) {
	r := &recordingBD{failGet: true}
	purgeClosedEphemeralBeadsWith(r.run)

	if r.ranPurge() {
		t.Errorf("the guard could not read %s and the purge ran anyway: %v",
			gcprotect.ConfigKey, r.calls)
	}
}

func parseProtected(v string) []string {
	var out []string
	for _, f := range strings.Split(v, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func missing(have, want []string) []string {
	present := make(map[string]bool, len(have))
	for _, h := range have {
		present[h] = true
	}
	var out []string
	for _, w := range want {
		if !present[w] {
			out = append(out, w)
		}
	}
	return out
}
