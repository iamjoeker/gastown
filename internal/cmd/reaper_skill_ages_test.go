package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// reaperSkillPath is the /reaper slash command. Unlike a doc, it is an
// EXECUTABLE surface: an agent reads the fenced bash blocks and runs them
// verbatim against the production Dolt server, so a wrong literal in this file
// is a wrong literal in a destructive command line.
const reaperSkillPath = ".claude/commands/reaper.md"

// reaperSkillAge couples one age literal in the skill to the CLI default that
// governs the same operation.
//
// cmd is the subcommand that PERFORMS the operation, not merely one that
// accepts the flag: `gt reaper scan` also registers --purge-age, but scan only
// counts. The default that matters is the one on the command that deletes.
type reaperSkillAge struct {
	flag     string         // CLI flag, as passed in the skill's command blocks
	cmd      *cobra.Command // subcommand whose registered default governs
	cmdName  string         // for messages, e.g. "gt reaper purge"
	tableKey string         // row label in the skill's "Configuration Defaults" table
	effect   string         // what the operation does when an operator pastes a short value
}

// reaperSkillAges is every age the skill hardcodes. All four are listed, not
// just the ones that have drifted: the failure mode is a literal in this file
// falling out of step with the flag it feeds, and that is not specific to any
// one flag. gt-zjb caught --stale-age at 168h vs 720h; fixing it surfaced
// --purge-age and --mail-age at 72h vs 168h in the same file (gt-tu67), which
// a guard written for one flag would not have caught. --max-age agrees today
// and is pinned so it stays that way.
var reaperSkillAges = []reaperSkillAge{
	{
		flag:     "max-age",
		cmd:      reaperReapCmd,
		cmdName:  "gt reaper reap",
		tableKey: "max_age",
		effect:   "closes live wisps out from under the agents holding them",
	},
	{
		flag:     "purge-age",
		cmd:      reaperPurgeCmd,
		cmdName:  "gt reaper purge",
		tableKey: "purge_age",
		effect:   "PERMANENTLY DELETES closed wisps; they are unversioned and unbacked (hq-del4), which is what made gt-5y7 unrecoverable",
	},
	{
		flag:     "mail-age",
		cmd:      reaperPurgeCmd,
		cmdName:  "gt reaper purge",
		tableKey: "mail_delete_age",
		effect:   "PERMANENTLY DELETES closed mail, the durable record agents hand off through",
	},
	{
		flag:     "stale-age",
		cmd:      reaperAutoCloseCmd,
		cmdName:  "gt reaper auto-close",
		tableKey: "stale_issue_age",
		effect:   "auto-closes live issues",
	},
}

// TestReaperSkillAgesMatchCLIDefaults is the regression guard for gt-zjb
// (--stale-age) and gt-tu67 (--purge-age, --mail-age).
//
// The /reaper skill hardcoded --purge-age=72h and --mail-age=72h while every
// other destructive path in the town — the flag defaults, the mol-dog-reaper
// formula vars, donePurgeMinAge, doltserver.purgeMinAge — used 168h. A manual
// /reaper run therefore deleted closed wisps and closed mail 2.3x sooner than
// the town's own policy, irreversibly. It stayed invisible because each surface
// looked correct in isolation: --help said 168h, the formula said 168h, and
// nothing rendered the value the operator actually pasted.
//
// The assertion reads each flag's REGISTERED DEFAULT from the cobra command
// rather than a literal, so this test cannot drift into agreeing with itself.
// Changing a CLI default now forces the skill to be updated in the same commit,
// which is the coupling that was missing.
//
// Source-scanning tests can go inert when the thing they scan moves (gt-am7),
// so the presence of each flag is asserted too: a restructure that drops a flag
// from the skill fails here instead of silently passing. That matters
// especially in this direction — with no flag at all the CLI default governs,
// which is correct today but is a fact this test must state rather than assume.
func TestReaperSkillAgesMatchCLIDefaults(t *testing.T) {
	body := readReaperSkill(t)

	for _, age := range reaperSkillAges {
		t.Run(age.flag, func(t *testing.T) {
			want := reaperFlagDefault(t, age)

			re := regexp.MustCompile(`--` + regexp.QuoteMeta(age.flag) + `=(\S+)`)
			matches := re.FindAllStringSubmatch(body, -1)
			if len(matches) == 0 {
				t.Fatalf("%s no longer passes --%s anywhere. If that is deliberate the "+
					"CLI default (%s) governs and this test should assert the new shape — but an "+
					"unnoticed disappearance leaves this guard inert, which is how gt-am7 kept a "+
					"dead check green for seven days.", reaperSkillPath, age.flag, want)
			}

			for _, m := range matches {
				got := strings.TrimSuffix(m[1], `\`) // command blocks wrap lines with a trailing backslash
				if got == want {
					continue
				}
				gotDur, err := time.ParseDuration(got)
				if err != nil {
					t.Errorf("%s passes --%s=%s, which is not a valid duration.",
						reaperSkillPath, age.flag, got)
					continue
				}
				wantDur, _ := time.ParseDuration(want)
				t.Errorf("%s passes --%s=%s but `%s` defaults to %s.\n"+
					"An operator running /reaper %s %.1fx %s than the documented policy. "+
					"Change internal/cmd/reaper.go and the skill together.",
					reaperSkillPath, age.flag, got, age.cmdName, want,
					age.effect, ratio(wantDur, gotDur), directionWord(gotDur, wantDur))
			}
		})
	}
}

// TestReaperSkillAgeTableMatchesCLIDefaults pins the human-readable
// "Configuration Defaults" table to the same values. The table is what a reader
// checks when deciding whether a run is safe; if it disagrees with the commands
// below it, the reader is told one policy and the server gets another. Before
// gt-tu67 the table published 72h for both purge ages, so it was not merely
// stale — it was the document an operator would have cited to justify the run.
func TestReaperSkillAgeTableMatchesCLIDefaults(t *testing.T) {
	body := readReaperSkill(t)

	for _, age := range reaperSkillAges {
		t.Run(age.tableKey, func(t *testing.T) {
			want := reaperFlagDefault(t, age)

			re := regexp.MustCompile(`(?m)^\|\s*` + regexp.QuoteMeta(age.tableKey) + `\s*\|\s*([^|\s]+)\s*\|`)
			m := re.FindStringSubmatch(body)
			if m == nil {
				t.Fatalf("%s has no %s row in its Configuration Defaults table; "+
					"the operator-facing value is now undocumented.", reaperSkillPath, age.tableKey)
			}
			if got := m[1]; got != want {
				t.Errorf("%s documents %s as %s but its commands pass (and `%s` defaults to) %s. "+
					"The table is the value operators read before running a destructive cycle.",
					reaperSkillPath, age.tableKey, got, age.cmdName, want)
			}
		})
	}
}

// TestReaperAgeFlagDefaultsAgreeAcrossSubcommands guards the assumption the two
// tests above rest on: that "the CLI default" is a single value. Each age flag
// is registered on several subcommands (scan counts what purge deletes), today
// from one shared literal per loop in reaper.go. If that loop is ever split,
// `gt reaper scan` could report candidates by one threshold while `gt reaper
// purge` deletes by another, and comparing the skill against either one would
// look fine.
func TestReaperAgeFlagDefaultsAgreeAcrossSubcommands(t *testing.T) {
	subcommands := []struct {
		name string
		cmd  *cobra.Command
	}{
		{"gt reaper scan", reaperScanCmd},
		{"gt reaper reap", reaperReapCmd},
		{"gt reaper purge", reaperPurgeCmd},
		{"gt reaper auto-close", reaperAutoCloseCmd},
		{"gt reaper run", reaperRunCmd},
	}

	for _, age := range reaperSkillAges {
		t.Run(age.flag, func(t *testing.T) {
			want := reaperFlagDefault(t, age)

			found := 0
			for _, sub := range subcommands {
				f := sub.cmd.Flags().Lookup(age.flag)
				if f == nil {
					continue
				}
				found++
				if f.DefValue != want {
					t.Errorf("`%s --%s` defaults to %s but `%s --%s` defaults to %s. "+
						"Two reaper subcommands disagree about what 'old enough' means, so a "+
						"scan and the destructive command it gates measure different things.",
						sub.name, age.flag, f.DefValue, age.cmdName, age.flag, want)
				}
			}
			if found < 2 {
				t.Errorf("--%s is registered on %d subcommand(s); expected the scan/act pair "+
					"at minimum. If the flag set was restructured, update this list.", age.flag, found)
			}
		})
	}
}

// reaperFlagDefault returns the registered default for an age flag, failing if
// the flag is gone (the skill's command line would then be stale) or if the
// default does not parse as a duration.
func reaperFlagDefault(t *testing.T, age reaperSkillAge) string {
	t.Helper()
	f := age.cmd.Flags().Lookup(age.flag)
	if f == nil {
		t.Fatalf("`%s` has no --%s flag; the skill's command line is stale", age.cmdName, age.flag)
	}
	if _, err := time.ParseDuration(f.DefValue); err != nil {
		t.Fatalf("`%s --%s` default %q does not parse as a duration: %v",
			age.cmdName, age.flag, f.DefValue, err)
	}
	return f.DefValue
}

func readReaperSkill(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	path := filepath.Join(repoRoot, filepath.FromSlash(reaperSkillPath))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func ratio(a, b time.Duration) float64 {
	if a > b {
		return float64(a) / float64(b)
	}
	if b == 0 {
		return 0
	}
	return float64(b) / float64(a)
}

func directionWord(got, want time.Duration) string {
	if got < want {
		return "sooner"
	}
	return "later"
}
