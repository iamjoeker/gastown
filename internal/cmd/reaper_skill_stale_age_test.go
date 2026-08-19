package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// reaperSkillPath is the /reaper slash command. Unlike a doc, it is an
// EXECUTABLE surface: an agent reads the fenced bash blocks and runs them
// verbatim against the production Dolt server, so a wrong literal in this file
// is a wrong literal in a destructive command line.
const reaperSkillPath = ".claude/commands/reaper.md"

// staleAgeFlagRe matches the flag as it appears in the skill's command blocks.
var staleAgeFlagRe = regexp.MustCompile(`--stale-age=(\S+)`)

// staleAgeTableRowRe matches the skill's "Configuration Defaults" row.
var staleAgeTableRowRe = regexp.MustCompile(`(?m)^\|\s*stale_issue_age\s*\|\s*([^|\s]+)\s*\|`)

// TestReaperSkillStaleAgeMatchesCLIDefault is the regression guard for gt-zjb.
//
// The /reaper skill hardcoded --stale-age=168h (7d) while `gt reaper auto-close`
// documents and defaults to 720h (30d). Auto-close mutates real issues, so every
// manual run of /reaper closed live work 4.3x sooner than the town's published
// policy — and it did so silently, because each surface looked correct in
// isolation: the formula var said 720h, --help said 720h, and nothing rendered
// the value the operator actually pasted.
//
// The assertion reads the flag's REGISTERED DEFAULT from the cobra command
// rather than a literal, so this test cannot drift into agreeing with itself.
// Changing the CLI default now forces the skill to be updated in the same
// commit, which is the coupling that was missing.
//
// Source-scanning tests can go inert when the thing they scan moves (gt-am7),
// so the count of occurrences is asserted too: a restructure that removes the
// flag from the skill fails here instead of silently passing. That matters
// especially in this direction — with no --stale-age at all the CLI default
// governs, which is correct today but is a fact this test must state rather
// than assume.
func TestReaperSkillStaleAgeMatchesCLIDefault(t *testing.T) {
	flag := reaperAutoCloseCmd.Flags().Lookup("stale-age")
	if flag == nil {
		t.Fatal("gt reaper auto-close has no --stale-age flag; the skill's command line is stale")
	}
	want := flag.DefValue

	if _, err := time.ParseDuration(want); err != nil {
		t.Fatalf("--stale-age default %q does not parse as a duration: %v", want, err)
	}

	body := readReaperSkill(t)

	matches := staleAgeFlagRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		t.Fatalf("%s no longer passes --stale-age anywhere. If that is deliberate the "+
			"CLI default (%s) governs and this test should assert the new shape — but an "+
			"unnoticed disappearance leaves this guard inert, which is how gt-am7 kept a "+
			"dead check green for seven days.", reaperSkillPath, want)
	}

	for _, m := range matches {
		got := strings.TrimSuffix(m[1], `\`) // command blocks wrap lines with a trailing backslash
		if got == want {
			continue
		}
		gotDur, err := time.ParseDuration(got)
		if err != nil {
			t.Errorf("%s passes --stale-age=%s, which is not a valid duration.", reaperSkillPath, got)
			continue
		}
		wantDur, _ := time.ParseDuration(want)
		t.Errorf("%s passes --stale-age=%s but `gt reaper auto-close` defaults to %s.\n"+
			"An operator running /reaper would auto-close live issues %.1fx %s than the "+
			"documented policy (gt-zjb). Change internal/cmd/reaper.go and the skill together.",
			reaperSkillPath, got, want,
			ratio(wantDur, gotDur), directionWord(gotDur, wantDur))
	}
}

// TestReaperSkillStaleAgeTableMatchesCommands pins the human-readable
// "Configuration Defaults" table to the same value. The table is what a reader
// checks when deciding whether a run is safe; if it disagrees with the command
// two lines below it, the reader is told one policy and the server gets another.
func TestReaperSkillStaleAgeTableMatchesCommands(t *testing.T) {
	flag := reaperAutoCloseCmd.Flags().Lookup("stale-age")
	if flag == nil {
		t.Fatal("gt reaper auto-close has no --stale-age flag")
	}
	want := flag.DefValue

	body := readReaperSkill(t)

	m := staleAgeTableRowRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("%s has no stale_issue_age row in its Configuration Defaults table; "+
			"the operator-facing value is now undocumented.", reaperSkillPath)
	}
	if got := m[1]; got != want {
		t.Errorf("%s documents stale_issue_age as %s but its commands pass (and the CLI "+
			"defaults to) %s. The table is the value operators read before running a "+
			"destructive cycle.", reaperSkillPath, got, want)
	}
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
