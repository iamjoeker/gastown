package daemon

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
)

func TestWispReaperInterval(t *testing.T) {
	// Default (now 1h after Dog-driven refactor)
	if got := wispReaperInterval(nil); got != defaultWispReaperInterval {
		t.Errorf("expected default %v, got %v", defaultWispReaperInterval, got)
	}

	// Custom
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:     true,
				IntervalStr: "2h",
			},
		},
	}
	if got := wispReaperInterval(config); got != 2*time.Hour {
		t.Errorf("expected 2h, got %v", got)
	}

	// Invalid falls back to default
	config.Patrols.WispReaper.IntervalStr = "nope"
	if got := wispReaperInterval(config); got != defaultWispReaperInterval {
		t.Errorf("expected default for invalid, got %v", got)
	}
}

func TestWispReaperMaxAge(t *testing.T) {
	if got := wispReaperMaxAge(nil); got != defaultWispMaxAge {
		t.Errorf("expected default %v, got %v", defaultWispMaxAge, got)
	}

	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:   true,
				MaxAgeStr: "48h",
			},
		},
	}
	if got := wispReaperMaxAge(config); got != 48*time.Hour {
		t.Errorf("expected 48h, got %v", got)
	}
}

func TestWispDeleteAge(t *testing.T) {
	if got := wispDeleteAge(nil); got != defaultWispDeleteAge {
		t.Errorf("expected default %v, got %v", defaultWispDeleteAge, got)
	}

	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:      true,
				DeleteAgeStr: "336h",
			},
		},
	}
	if got := wispDeleteAge(config); got != 14*24*time.Hour {
		t.Errorf("expected 336h, got %v", got)
	}
}

func TestStaleIssueAge(t *testing.T) {
	if got := staleIssueAge(nil); got != defaultStaleIssueAge {
		t.Errorf("expected default %v, got %v", defaultStaleIssueAge, got)
	}

	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:          true,
				StaleIssueAgeStr: "1440h",
			},
		},
	}
	if got := staleIssueAge(config); got != 60*24*time.Hour {
		t.Errorf("expected 1440h, got %v", got)
	}

	// Unparseable and non-positive values fall back rather than acting. A zero
	// or negative staleness would make every open issue eligible for auto-close.
	for _, bad := range []string{"nope", "0h", "-720h", ""} {
		config.Patrols.WispReaper.StaleIssueAgeStr = bad
		if got := staleIssueAge(config); got != defaultStaleIssueAge {
			t.Errorf("StaleIssueAgeStr=%q: expected fallback to %v, got %v", bad, defaultStaleIssueAge, got)
		}
	}
}

func TestMailDeleteAge(t *testing.T) {
	if got := mailDeleteAge(nil); got != defaultMailDeleteAge {
		t.Errorf("expected default %v, got %v", defaultMailDeleteAge, got)
	}

	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:          true,
				MailDeleteAgeStr: "336h",
			},
		},
	}
	if got := mailDeleteAge(config); got != 14*24*time.Hour {
		t.Errorf("expected 336h, got %v", got)
	}
}

// TestReaperFormulaVarsAreConfigurable is the regression guard for the half of
// gt-zjb/gt-7hs that outlived the constant fix: stale_issue_age and
// mail_delete_age were rendered into the sling vars straight from their package
// constants, while max_age and purge_age went through accessors. The formula
// declared all four as vars, so all four looked configurable and two were not.
//
// This asserts the wiring, not the values — it fails if someone reverts an
// accessor back to a bare constant, which is what the original bug was.
func TestReaperFormulaVarsAreConfigurable(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:          true,
				MaxAgeStr:        "1h",
				DeleteAgeStr:     "2h",
				StaleIssueAgeStr: "3h",
				MailDeleteAgeStr: "4h",
			},
		},
	}

	// Every value is distinct and distinct from every default, so a var that is
	// still reading a constant cannot coincidentally match.
	want := map[string]string{
		"max_age":         "1h0m0s",
		"purge_age":       "2h0m0s",
		"stale_issue_age": "3h0m0s",
		"mail_delete_age": "4h0m0s",
	}
	vars := reaperFormulaVars(config)
	for k, exp := range want {
		if got := vars[k]; got != exp {
			t.Errorf("var %s = %q, want %q — configured value did not reach the formula; "+
				"it is probably being rendered from a package constant instead of an accessor", k, got, exp)
		}
	}

	// A nil config must still produce every var, at its default.
	defaults := reaperFormulaVars(nil)
	for _, k := range []string{"max_age", "purge_age", "stale_issue_age", "mail_delete_age", "alert_threshold"} {
		if defaults[k] == "" {
			t.Errorf("var %s missing from nil-config vars; the formula interpolates it into a "+
				"gt reaper flag, so an empty value becomes a malformed command", k)
		}
	}
	if got := defaults["stale_issue_age"]; got != "720h0m0s" {
		t.Errorf("default stale_issue_age var = %q, want \"720h0m0s\"", got)
	}
}

func TestDefaultReaperIntervalIsOneHour(t *testing.T) {
	// Verify the default changed from 30m to 1h per issue gt-caf7.
	if defaultWispReaperInterval != 1*time.Hour {
		t.Errorf("expected default interval 1h, got %v", defaultWispReaperInterval)
	}
}

func TestDispatchReaperDogUsesDogPoolSling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock")
	}

	townRoot := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gt-args.log")
	fakeGT := filepath.Join(binDir, "gt")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", logPath)
	if err := os.WriteFile(fakeGT, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gt: %v", err)
	}

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		gtPath: fakeGT,
	}
	if err := d.dispatchReaperDog(map[string]string{"max_age": "1h"}); err != nil {
		t.Fatalf("dispatchReaperDog() error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read gt args log: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	wantPrefix := []string{"sling", constants.MolDogReaper, "deacon/dogs"}
	if len(args) < len(wantPrefix) {
		t.Fatalf("gt args = %v, want prefix %v", args, wantPrefix)
	}
	for i, want := range wantPrefix {
		if args[i] != want {
			t.Fatalf("gt arg %d = %q, want %q (all args: %v)", i, args[i], want, args)
		}
	}
}

func TestDoltServerHostIgnoresStaleBeadsHost(t *testing.T) {
	t.Setenv("GT_DOLT_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-host")

	d := &Daemon{config: &Config{TownRoot: t.TempDir()}}
	if got := d.doltServerHost(); got != "127.0.0.1" {
		t.Fatalf("doltServerHost() = %q, want default localhost", got)
	}
}

func TestDoltServerHostUsesConfiguredTownHost(t *testing.T) {
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")
	t.Setenv("GT_DOLT_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-host")
	townRoot := t.TempDir()
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte("listener:\n  host: 127.0.0.2\n  port: 5507\n"), 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{config: &Config{TownRoot: townRoot}}
	if got := d.doltServerHost(); got != "127.0.0.2" {
		t.Fatalf("doltServerHost() = %q, want configured host", got)
	}
}

// fakeGTAndBD writes stub gt and bd binaries and returns their paths plus the
// file that records every bd invocation.
//
// The gt stub records its arguments too (gtCallLog, alongside bdLog in the same
// directory), because the reaper's dispatch-failure branch now has to be
// checked for what it TELLS somebody, not only for what it pours.
func fakeGTAndBD(t *testing.T, gtExit int) (gtPath, bdPath, bdLog string) {
	t.Helper()
	dir := t.TempDir()
	bdLog = filepath.Join(dir, "bd-calls.log")
	gtLog := filepath.Join(dir, "gt-calls.log")

	gtPath = filepath.Join(dir, "gt")
	gtScript := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexit %d\n", gtLog, gtExit)
	if err := os.WriteFile(gtPath, []byte(gtScript), 0o755); err != nil {
		t.Fatalf("write fake gt: %v", err)
	}

	bdPath = filepath.Join(dir, "bd")
	bdScript := `#!/bin/sh
printf '%s\n' "$*" >> "` + bdLog + `"
case "$1" in
  mol) printf 'Spawned wisp: hq-wisp-reap01 - mol-dog-reaper\n' ;;
  show) printf '{"hq-wisp-reap01":[],"schema_version":1}\n' ;;
  *) : ;;
esac
exit 0
`
	if err := os.WriteFile(bdPath, []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	return gtPath, bdPath, bdLog
}

// reaperTestDaemon points the daemon at stub binaries and a dead Dolt port, so
// the inline fallback fails fast on connect instead of reaching a real server.
//
// It returns the bd call log, the gt call log and the buffer the daemon logger
// writes to. The gt log and the buffer exist so a test can assert what the
// dispatch-failure branch REPORTS; before gt-9tpw that branch was one INFO line
// and there was nothing to assert.
//
// Pointing d.gtPath at the stub is what keeps `gt escalate` inside the test. It
// only works because escalate no longer hard-codes a bare "gt" resolved from
// PATH — with that, this test would have raised a real HIGH escalation against
// whatever town the test host is sitting in, every run.
func reaperTestDaemon(t *testing.T, gtExit int) (*Daemon, string, string, *bytes.Buffer) {
	t.Helper()
	gtPath, bdPath, bdLog := fakeGTAndBD(t, gtExit)
	gtLog := filepath.Join(filepath.Dir(bdLog), "gt-calls.log")
	logBuf := &bytes.Buffer{}
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		gtPath: gtPath,
		bdPath: bdPath,
		logger: log.New(logBuf, "", 0),
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{WispReaper: &WispReaperConfig{
				Enabled: true,
				// A database that cannot be reached: the fallback must not touch a
				// live server just to prove it poured a molecule.
				Databases: []string{"testonly_unreachable"},
			}},
		},
		doltServer: &DoltServerManager{config: &DoltServerConfig{Host: "127.0.0.1", Port: 1}},
	}
	return d, bdLog, gtLog, logBuf
}

func readBDCalls(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read bd call log: %v", err)
	}
	return string(data)
}

// TestReapWispsDoesNotPourWhenDispatchSucceeds is the gt-bnpw duplicate-pour
// regression.
//
// dispatchReaperDog slings the FORMULA NAME, so `gt sling` pours the molecule
// the Dog actually runs. A molecule poured here as well is a second, redundant
// one — and reapWisps closed it while the Dog was still working on its own.
// Measured on hq 2026-08-24: every cycle emitted two mol-dog-reaper roots 2-3
// seconds apart, one assigned to deacon/dogs/alpha and one unassigned carrying
// six step children.
func TestReapWispsDoesNotPourWhenDispatchSucceeds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	d, bdLog, _, _ := reaperTestDaemon(t, 0)

	d.reapWisps()

	if calls := readBDCalls(t, bdLog); strings.Contains(calls, "mol wisp") {
		t.Fatalf("a successful sling must not also pour a molecule here, got bd calls:\n%s", calls)
	}
}

// TestReapWispsPoursOnInlineFallback: the molecule is not merely deleted. When
// the sling fails, nothing else records the run and reapWispsInline closes each
// step as it goes, so there it is the only trace of the work.
func TestReapWispsPoursOnInlineFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	d, bdLog, _, _ := reaperTestDaemon(t, 1) // gt sling fails

	d.reapWisps()

	calls := readBDCalls(t, bdLog)
	if !strings.Contains(calls, "mol wisp "+constants.MolDogReaper) {
		t.Fatalf("inline fallback must pour the observability molecule, got bd calls:\n%s", calls)
	}
}

// TestReapWispsEscalatesWhenDispatchFailureDivertsToDestructivePath is the
// gt-9tpw failure-path regression, instance 6.
//
// The behaviour under test is NOT "the fallback runs" — that already worked and
// is what TestReapWispsPoursOnInlineFallback covers. It is that taking the
// fallback is REPORTED. A dispatch failure diverts execution onto a path that
// deletes unversioned rows; on 2026-08-24 that happened on every cycle because
// an unrelated binary was broken, 229 wisps were purged, and the only trace was
// an INFO line indistinguishable in level from the routine success line one
// branch over.
//
// Written against the unfixed reapWisps first, where it fails: the gt call log
// holds the `sling` invocation and nothing else, because no escalation was ever
// attempted.
func TestReapWispsEscalatesWhenDispatchFailureDivertsToDestructivePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	d, _, gtLog, _ := reaperTestDaemon(t, 1) // gt sling fails

	d.reapWisps()

	calls := readGTCalls(t, gtLog)

	// Control: the stub records invocations at all, and the branch under test
	// was really entered. Without this an empty log would read as "no escalation"
	// whether the escalation was missing or the stub never ran.
	if !strings.Contains(calls, "sling") {
		t.Fatalf("fixture never reached dispatch — no `gt sling` in the call log, so this test proves nothing:\n%s", calls)
	}

	if !strings.Contains(calls, "escalate") {
		t.Fatalf("a dispatch failure diverted reaping onto the destructive inline path without escalating.\ngt calls were:\n%s", calls)
	}
	// The escalation has to name what is about to happen, not just that something
	// failed. "Dog dispatch failed" alone reads as a scheduling hiccup.
	if !strings.Contains(calls, "DELETES") {
		t.Errorf("escalation does not say the fallback deletes rows; a reader cannot tell this is destructive.\ngt calls were:\n%s", calls)
	}
}

// TestReapWispsReportsWhenItsOwnEscalationCouldNotBeSent covers the case the fix
// above cannot solve, only disclose.
//
// The escalation runs `gt`. The branch that raises it was entered because a `gt`
// invocation failed. So the most likely single cause — a broken gt binary, which
// is exactly what happened on 2026-08-24 — takes out the alarm along with the
// safe path. Escalating and assuming it landed would be this bead's defect
// committed inside its own fix, so the daemon log must say the destructive
// fallback ran unreported.
//
// The stub exits non-zero for EVERY subcommand, which is that scenario.
func TestReapWispsReportsWhenItsOwnEscalationCouldNotBeSent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	d, _, gtLog, logBuf := reaperTestDaemon(t, 1) // every gt subcommand fails

	d.reapWisps()

	// Control: the escalation was attempted and did fail. If it had succeeded,
	// the assertion below would be checking for a line that correctly is absent.
	if calls := readGTCalls(t, gtLog); !strings.Contains(calls, "escalate") {
		t.Fatalf("escalation was never attempted, so this test is not exercising a failed one:\n%s", calls)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "UNREPORTED DESTRUCTIVE FALLBACK") {
		t.Fatalf("escalation failed and the daemon log does not say the destructive fallback went unreported.\ndaemon log:\n%s", logs)
	}
}

func readGTCalls(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read gt call log: %v", err)
	}
	return string(data)
}
