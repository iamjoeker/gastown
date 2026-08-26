package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"

	"github.com/steveyegge/gastown/internal/deacon"
)

// Heartbeat ages named relative to the thresholds the daemon judges against.
// Pinning them to literal minutes is how the 5m miscalibration survived a green
// suite: every case had been picked to straddle 5m, so the table agreed with the
// threshold whatever the threshold was worth (gt-cbd).
var (
	// ageMidCycle is a heartbeat age a healthy Deacon reaches every cycle — the
	// mean mol-deacon-patrol cycle measured in gt-cbd was 604s. Nothing may fire
	// here.
	ageMidCycle  = 604 * time.Second
	ageStaleBand = deacon.HeartbeatStaleThreshold + time.Minute
	ageVeryStale = deacon.HeartbeatVeryStaleThreshold + time.Minute
)

// writeFakeTmuxWithSession creates a fake tmux binary that reports the Deacon
// session as existing (has-session returns 0). Used for deacon idle guard tests
// where the session must be present so checkDeaconHeartbeat reaches the nudge path.
func writeFakeTmuxWithSession(t *testing.T, dir string) {
	t.Helper()
	script := `#!/usr/bin/env bash
set -euo pipefail

cmd=""
skip_next=0
for arg in "$@"; do
  if [[ "$skip_next" -eq 1 ]]; then
    skip_next=0
    continue
  fi
  if [[ "$arg" == "-u" ]]; then
    continue
  fi
  if [[ "$arg" == "-L" ]]; then
    skip_next=1
    continue
  fi
  cmd="$arg"
  break
done

if [[ -n "${TMUX_LOG:-}" ]]; then
  printf "%s %s\n" "$cmd" "$*" >> "$TMUX_LOG"
fi

if [[ "${1:-}" == "-V" ]]; then
  echo "tmux 3.3a"
  exit 0
fi

# Session exists: has-session returns 0 so the nudge path is reachable.
if [[ "$cmd" == "has-session" ]]; then
  exit 0
fi

exit 0
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
}

// TestCheckDeaconHeartbeat_IdleGuard verifies that the nudge is suppressed when
// the Deacon heartbeat is stale but no active work is in flight (idle guard).
func TestCheckDeaconHeartbeat_IdleGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	// Every case pins the process table. The idle guard's suppression is
	// conditional on a live await since gt-ghw7, so leaving the table to whatever
	// happens to be running on the build machine would make these results depend
	// on whether a real Deacon is parked next to the test.
	tests := []struct {
		name             string
		heartbeatAge     time.Duration
		stores           map[string]beadsdk.Storage
		procs            []string
		wantNudgeLog     bool
		wantIdleGuardLog bool
		desc             string
	}{
		{
			name:         "idle: stale heartbeat, no work, await pending — nudge suppressed",
			heartbeatAge: ageStaleBand,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{}},
			},
			procs:            []string{psDeaconAwait},
			wantNudgeLog:     false,
			wantIdleGuardLog: true,
			desc:             "Idle guard must suppress nudge when no work is in flight and the await is really waiting",
		},
		{
			name:         "active work: stale heartbeat, in_progress bead — nudge sent",
			heartbeatAge: ageStaleBand,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
					"in_progress": {{ID: "sc-abc"}},
				}},
			},
			wantNudgeLog:     true,
			wantIdleGuardLog: false,
			desc:             "Nudge must fire when in_progress work exists",
		},
		{
			name:         "hooked only: stale heartbeat, patrol wisp, await pending — nudge suppressed",
			heartbeatAge: ageStaleBand,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
					"hooked": {{ID: "hq-wisp-34zi"}},
				}},
			},
			procs:            []string{psDeaconAwait},
			wantNudgeLog:     false,
			wantIdleGuardLog: true,
			desc:             "Patrol wisps in hooked state do not count as active work; nudge must be suppressed",
		},
		{
			name:         "store error: stale heartbeat, store fails — nudge sent conservatively",
			heartbeatAge: ageStaleBand,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{err: fmt.Errorf("db offline")},
			},
			wantNudgeLog:     true,
			wantIdleGuardLog: false,
			desc:             "Nudge must fire conservatively when work state is unknown",
		},
		{
			name:         "very stale: heartbeat past the very-stale threshold — escalation path, no nudge",
			heartbeatAge: ageVeryStale,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{}},
			},
			wantNudgeLog:     false,
			wantIdleGuardLog: false,
			desc:             "Very stale heartbeat takes escalation path, not nudge path; idle guard not reached",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()
			fakeBinDir := t.TempDir()
			tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
			if err := os.WriteFile(tmuxLog, []byte{}, 0o644); err != nil {
				t.Fatalf("create tmux log: %v", err)
			}

			writeFakeTmuxWithSession(t, fakeBinDir)
			t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TMUX_LOG", tmuxLog)

			writeDeaconHeartbeat(t, townRoot, tc.heartbeatAge)

			defer stubProcessTable(t, tc.procs, nil)()

			d := newTestDaemonWithStores(t, townRoot, tc.stores)

			logBuf := &strings.Builder{}
			d.logger = log.New(logBuf, "", 0)

			d.checkDeaconHeartbeat()

			logOutput := logBuf.String()

			hasIdleGuardLog := strings.Contains(logOutput, "nudge skipped")
			if hasIdleGuardLog != tc.wantIdleGuardLog {
				t.Errorf("%s\nidle guard log present=%v, want=%v\nlog:\n%s",
					tc.desc, hasIdleGuardLog, tc.wantIdleGuardLog, logOutput)
			}

			hasNudgeLog := strings.Contains(logOutput, "nudging session")
			if hasNudgeLog != tc.wantNudgeLog {
				t.Errorf("%s\nnudge log present=%v, want=%v\nlog:\n%s",
					tc.desc, hasNudgeLog, tc.wantNudgeLog, logOutput)
			}
		})
	}
}

// runDeaconHeartbeatCheck drives checkDeaconHeartbeat against a heartbeat of the
// given age with in_progress work in flight, and returns the daemon's log. Work
// is in flight deliberately: the idle guard suppresses the nudge when it is not,
// which would make a quiet log prove nothing about whether the heartbeat was
// judged stale.
func runDeaconHeartbeatCheck(t *testing.T, townRoot string, age time.Duration) string {
	t.Helper()

	fakeBinDir := t.TempDir()
	writeFakeTmuxWithSession(t, fakeBinDir)
	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", filepath.Join(t.TempDir(), "tmux.log"))

	writeDeaconHeartbeat(t, townRoot, age)
	defer stubProcessTable(t, nil, nil)()

	d := newTestDaemonWithStores(t, townRoot, map[string]beadsdk.Storage{
		"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
			"in_progress": {{ID: "sc-abc"}},
		}},
	})

	logBuf := &strings.Builder{}
	d.logger = log.New(logBuf, "", 0)
	d.checkDeaconHeartbeat()
	return logBuf.String()
}

// The gt-cbd regression. A Deacon's heartbeat stamps at fixed points in the
// patrol cycle, so its age ramps from zero to the cycle duration and resets: age
// reports position in the loop, not liveness. Against the 5m threshold this
// daemon declared a healthy Deacon "stuck" and nudged it — logged in the field
// at 6m0s and 7m0s — for more than half of every cycle.
//
// An age a healthy Deacon reaches every single cycle must therefore produce
// nothing at all. The positive control below is what makes that mean something:
// the same harness, the same work in flight, one age further on, does log both.
func TestCheckDeaconHeartbeat_MidCycleAgeIsNotStuck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	if ageMidCycle >= deacon.HeartbeatStaleThreshold {
		t.Fatalf("the mean measured patrol cycle (%s) must fit inside the stale threshold (%s) — "+
			"otherwise a healthy Deacon is reported stuck on most cycles, which is the defect",
			ageMidCycle, deacon.HeartbeatStaleThreshold)
	}

	quiet := runDeaconHeartbeatCheck(t, t.TempDir(), ageMidCycle)
	for _, unwanted := range []string{"heartbeat is stale", "nudging session"} {
		if strings.Contains(quiet, unwanted) {
			t.Errorf("a heartbeat at the mean patrol-cycle age (%s) logged %q — this is the false positive gt-cbd filed\nlog:\n%s",
				ageMidCycle, unwanted, quiet)
		}
	}

	// Positive control: the assertions above can fail.
	loud := runDeaconHeartbeatCheck(t, t.TempDir(), ageStaleBand)
	for _, wanted := range []string{"heartbeat is stale", "nudging session"} {
		if !strings.Contains(loud, wanted) {
			t.Fatalf("control: a heartbeat past the stale threshold (%s) did not log %q, so the quiet run above proves nothing\nlog:\n%s",
				ageStaleBand, wanted, loud)
		}
	}
}

// The stale threshold is documented as overridable under operational.deacon, and
// gt-cbd was read as a local misconfiguration partly on that promise. It was not
// true: the daemon compared against the compiled-in constant, so setting the key
// changed nothing and failed silently. This asserts the override is live.
func TestCheckDeaconHeartbeat_HonoursConfiguredStaleThreshold(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	// An age that is fresh by default and stale under the override, so the two
	// runs below can only differ because the configured value was read.
	shortStale := 2 * time.Minute
	age := 5 * time.Minute
	if age >= deacon.HeartbeatStaleThreshold {
		t.Fatalf("test age %s must be fresh by default (%s) for this to discriminate",
			age, deacon.HeartbeatStaleThreshold)
	}

	if got := runDeaconHeartbeatCheck(t, t.TempDir(), age); strings.Contains(got, "heartbeat is stale") {
		t.Fatalf("baseline: age %s should be fresh with no config present\nlog:\n%s", age, got)
	}

	configured := t.TempDir()
	writeTownSettings(t, configured, `{"operational":{"deacon":{"heartbeat_stale_threshold":"2m"}}}`)

	got := runDeaconHeartbeatCheck(t, configured, age)
	if !strings.Contains(got, "heartbeat is stale") {
		t.Errorf("operational.deacon.heartbeat_stale_threshold=%s was ignored: age %s did not read stale\nlog:\n%s",
			shortStale, age, got)
	}
}

// writeTownSettings writes settings/config.json under a town root.
func writeTownSettings(t *testing.T, townRoot, body string) {
	t.Helper()
	dir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write town settings: %v", err)
	}
}
