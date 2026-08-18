package daemon

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// Pane fixtures transcribed from live agent panes. The prompt character is
// followed by a non-breaking space, and the placeholder text after it is dim
// (ESC[2m) — a ghost the TUI draws, not something anyone typed.
const (
	testBoxLine       = "\x1b[38;5;244m\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\x1b[39m"
	testFooterIdle    = "  \x1b[38;5;211m\u23f5\u23f5 bypass permissions on\x1b[38;5;246m (shift+tab to cycle) \u00b7 \u2190 for agents\x1b[39m"
	testFooterBusy    = "  \x1b[38;5;211m\u23f5\u23f5 bypass permissions on\x1b[38;5;246m (shift+tab to cycle) \u00b7 esc to interrupt \u00b7 \u2190 for age\u2026\x1b[39m"
	testComposerGhost = "\x1b[39m\u276f\u00a0\x1b[2mkeep patrolling\x1b[0m"
	testComposerText  = "\x1b[39m\u276f\u00a0gt patrol report"
	testSpinner       = "\x1b[38;5;246m\u273b\x1b[39m \x1b[38;5;246mWorked for 37s\x1b[39m"
)

// pane assembles a fixture in the layout a real agent pane uses: transcript,
// spinner, then the composer box, then the status line.
func pane(transcript, composer, footer string) string {
	return transcript + "\n\n" + testSpinner + "\n\n" +
		testBoxLine + "\n" + composer + "\n" + testBoxLine + "\n" + footer + "\n"
}

func stoppedPane() string {
	return pane("  Cycle complete, nothing outstanding.", testComposerGhost, testFooterIdle)
}

func workingPane() string {
	return pane("  \u23ce  $ gt mol step await-signal (2m 4s)", testComposerGhost, testFooterBusy)
}

func strandedPane() string {
	return pane("  Cycle complete.", testComposerText, testFooterIdle)
}

// writeFakePaneTmux creates a fake tmux that serves pane fixtures from a
// directory: `capture-pane -t <session>` prints <dir>/<session>.txt, or nothing
// when no fixture exists (which is what a missing session looks like).
func writeFakePaneTmux(t *testing.T, binDir, paneDir string) {
	t.Helper()
	script := `#!/usr/bin/env bash
set -uo pipefail

if [[ "${1:-}" == "-V" ]]; then
  echo "tmux 3.3a"
  exit 0
fi

cmd=""
target=""
want_target=0
skip_next=0
for arg in "$@"; do
  if [[ "$skip_next" -eq 1 ]]; then
    skip_next=0
    continue
  fi
  if [[ "$want_target" -eq 1 ]]; then
    target="$arg"
    want_target=0
    continue
  fi
  case "$arg" in
    -L) skip_next=1 ;;
    -t) want_target=1 ;;
    -*) ;;
    *) if [[ -z "$cmd" ]]; then cmd="$arg"; fi ;;
  esac
done

if [[ "$cmd" == "capture-pane" ]]; then
  f="$PANE_DIR/$target.txt"
  if [[ -f "$f" ]]; then
    cat "$f"
    exit 0
  fi
  exit 1
fi

exit 0
`
	path := filepath.Join(binDir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PANE_DIR", paneDir)
}

func writePaneFixture(t *testing.T, paneDir, sessionName, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(paneDir, sessionName+".txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("write pane fixture: %v", err)
	}
}

// newPatrolWakeDaemon builds a Daemon wired to the fake tmux with a fixed rig
// list, bypassing rigs.json discovery.
func newPatrolWakeDaemon(t *testing.T, townRoot string, rigs []string, logBuf *strings.Builder) *Daemon {
	t.Helper()
	return &Daemon{
		config:              &Config{TownRoot: townRoot},
		logger:              log.New(logBuf, "", 0),
		tmux:                tmux.NewTmux(),
		ctx:                 context.Background(),
		knownRigsCache:      rigs,
		knownRigsCacheValid: true,
		lastPatrolWake:      map[string]time.Time{},
	}
}

func readNudgeLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read nudge log: %v", err)
	}
	return string(data)
}

// TestWakeStoppedPatrolAgents covers the decision the whole feature turns on:
// wake an agent whose turn ended, and leave every other state alone.
func TestWakeStoppedPatrolAgents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	const rigName = "gastown"
	witnessSession := session.WitnessSessionName(session.PrefixFor(rigName))
	refinerySession := session.RefinerySessionName(session.PrefixFor(rigName))
	deaconSession := session.DeaconSessionName()

	targets := []patrolWakeTarget{
		{role: "witness", rig: rigName, session: witnessSession},
		{role: "refinery", rig: rigName, session: refinerySession},
		{role: "deacon", session: deaconSession},
	}

	tests := []struct {
		name      string
		panes     map[string]string
		wantWoken []string
		wantQuiet []string
	}{
		{
			name: "stopped agents are woken, working ones are not",
			panes: map[string]string{
				witnessSession:  stoppedPane(),
				refinerySession: workingPane(),
				deaconSession:   stoppedPane(),
			},
			wantWoken: []string{witnessSession, deaconSession},
			wantQuiet: []string{refinerySession},
		},
		{
			name: "an agent holding unsent text is left alone",
			panes: map[string]string{
				witnessSession:  strandedPane(),
				refinerySession: workingPane(),
				deaconSession:   workingPane(),
			},
			wantQuiet: []string{witnessSession, refinerySession, deaconSession},
		},
		{
			// A session with no pane at all reads as TurnUnknown, which must
			// never be treated as "stopped": that is also what an unreadable
			// pane returns, and acting on it would send keystrokes blind.
			name: "sessions that cannot be read are not woken",
			panes: map[string]string{
				refinerySession: workingPane(),
			},
			wantQuiet: []string{witnessSession, refinerySession, deaconSession},
		},
		{
			// The pane read this keys on is the ONLY signal. A busy marker that
			// appears in the agent's own transcript rather than its status line must
			// not mask a stopped agent — a whole-pane scan reports "working"
			// here and the loop stays stopped forever.
			name: "busy marker in transcript prose does not suppress the wake",
			panes: map[string]string{
				witnessSession: pane(
					"  the status line would say 'esc to interrupt' if it were working",
					testComposerGhost, testFooterIdle),
				refinerySession: workingPane(),
				deaconSession:   workingPane(),
			},
			wantWoken: []string{witnessSession},
			wantQuiet: []string{refinerySession, deaconSession},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			paneDir := t.TempDir()
			nudgeLog := filepath.Join(t.TempDir(), "nudges.log")

			writeFakePaneTmux(t, binDir, paneDir)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(tmux.TestNudgeLogEnv, nudgeLog)

			for name, content := range tc.panes {
				writePaneFixture(t, paneDir, name, content)
			}

			prevDelay := patrolWakeConfirmDelay
			patrolWakeConfirmDelay = time.Millisecond
			t.Cleanup(func() { patrolWakeConfirmDelay = prevDelay })

			logBuf := &strings.Builder{}
			d := newPatrolWakeDaemon(t, t.TempDir(), []string{rigName}, logBuf)

			d.wakeStoppedTargets(targets)

			got := readNudgeLog(t, nudgeLog)
			for _, want := range tc.wantWoken {
				if !strings.Contains(got, "nudge:"+want+":") {
					t.Errorf("expected %s to be woken\nnudge log:\n%s\ndaemon log:\n%s", want, got, logBuf.String())
				}
			}
			for _, quiet := range tc.wantQuiet {
				if strings.Contains(got, "nudge:"+quiet+":") {
					t.Errorf("expected %s NOT to be woken\nnudge log:\n%s\ndaemon log:\n%s", quiet, got, logBuf.String())
				}
			}
		})
	}
}

// The wake message must name the await step the role actually runs. Witnesses
// run await-signal and refineries run await-event; the two roles do not share a
// loop, and naming the wrong one sends an agent looking for a command its
// formula does not contain.
func TestPatrolWakeMessageNamesTheRolesOwnAwaitStep(t *testing.T) {
	tests := map[string]string{
		"witness":  "await-signal",
		"refinery": "await-event",
	}
	for role, want := range tests {
		got := patrolWakeTarget{role: role}.awaitStep()
		if !strings.Contains(got, want) {
			t.Errorf("awaitStep() for %s = %q, want it to name %q", role, got, want)
		}
	}
	if got := (patrolWakeTarget{role: "deacon"}).awaitStep(); strings.Contains(got, "await-signal") || strings.Contains(got, "await-event") {
		t.Errorf("awaitStep() for deacon = %q, want no role-specific await command", got)
	}
}

// A second wake inside the cooldown is suppressed, so an agent that does not
// come back is not nudged on every heartbeat.
func TestWakePatrolAgentCooldown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	binDir := t.TempDir()
	paneDir := t.TempDir()
	nudgeLog := filepath.Join(t.TempDir(), "nudges.log")

	writeFakePaneTmux(t, binDir, paneDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(tmux.TestNudgeLogEnv, nudgeLog)

	tgt := patrolWakeTarget{role: "witness", rig: "gastown", session: "gt-witness"}
	writePaneFixture(t, paneDir, tgt.session, stoppedPane())

	logBuf := &strings.Builder{}
	d := newPatrolWakeDaemon(t, t.TempDir(), []string{"gastown"}, logBuf)

	d.wakePatrolAgent(tgt)
	d.wakePatrolAgent(tgt)

	if n := strings.Count(readNudgeLog(t, nudgeLog), "nudge:"+tgt.session+":"); n != 1 {
		t.Errorf("wake count = %d, want 1 (second wake should be inside the cooldown)\ndaemon log:\n%s", n, logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "within cooldown") {
		t.Errorf("expected a cooldown log line, got:\n%s", logBuf.String())
	}
}

// A failed wake must not record a cooldown: it did not land, so the next
// heartbeat has to try again rather than treat it as delivered.
func TestFailedWakeDoesNotRecordCooldown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	binDir := t.TempDir()
	paneDir := t.TempDir()

	writeFakePaneTmux(t, binDir, paneDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// No GT_TEST_NUDGE_LOG: the tmux nudge guard refuses delivery from a test
	// binary and returns an error, which is the failure path under test.
	t.Setenv(tmux.TestNudgeLogEnv, "")
	os.Unsetenv(tmux.TestNudgeLogEnv)

	tgt := patrolWakeTarget{role: "witness", rig: "gastown", session: "gt-witness"}
	writePaneFixture(t, paneDir, tgt.session, stoppedPane())

	logBuf := &strings.Builder{}
	d := newPatrolWakeDaemon(t, t.TempDir(), []string{"gastown"}, logBuf)

	d.wakePatrolAgent(tgt)

	if _, recorded := d.lastPatrolWake[tgt.session]; recorded {
		t.Errorf("cooldown recorded for a wake that failed to deliver\nlog:\n%s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "failed to wake") {
		t.Errorf("expected a delivery failure log line, got:\n%s", logBuf.String())
	}
}

// The whole check must be switchable off in config, and off must mean no wakes
// at all. The enabled case is the positive control: without it, an empty nudge
// log proves nothing, since a target set that never formed produces the same
// empty log as a check that was correctly suppressed.
func TestPatrolWakeConfigSwitch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	tests := []struct {
		name     string
		settings string
		wantWake bool
	}{
		{
			name:     "enabled by default: stopped deacon is woken",
			settings: `{}`,
			wantWake: true,
		},
		{
			name:     "disabled: the same stopped deacon is left alone",
			settings: `{"operational":{"daemon":{"patrol_wake_enabled":false}}}`,
			wantWake: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			paneDir := t.TempDir()
			nudgeLog := filepath.Join(t.TempDir(), "nudges.log")

			writeFakePaneTmux(t, binDir, paneDir)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(tmux.TestNudgeLogEnv, nudgeLog)

			prevDelay := patrolWakeConfirmDelay
			patrolWakeConfirmDelay = time.Millisecond
			t.Cleanup(func() { patrolWakeConfirmDelay = prevDelay })

			townRoot := t.TempDir()
			settingsDir := filepath.Join(townRoot, "settings")
			if err := os.MkdirAll(settingsDir, 0o755); err != nil {
				t.Fatalf("mkdir settings: %v", err)
			}
			if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), []byte(tc.settings), 0o644); err != nil {
				t.Fatalf("write settings: %v", err)
			}

			// The Deacon is a town-level target, so it forms without any rig
			// discovery — which is what keeps the positive control meaningful.
			deaconSession := session.DeaconSessionName()
			writePaneFixture(t, paneDir, deaconSession, stoppedPane())

			logBuf := &strings.Builder{}
			d := newPatrolWakeDaemon(t, townRoot, nil, logBuf)

			if got := d.loadOperationalConfig().GetDaemonConfig().PatrolWakeEnabledV(); got != tc.wantWake {
				t.Fatalf("settings fixture did not take effect: PatrolWakeEnabledV() = %v, want %v", got, tc.wantWake)
			}

			d.wakeStoppedPatrolAgents()

			woke := strings.Contains(readNudgeLog(t, nudgeLog), "nudge:"+deaconSession+":")
			if woke != tc.wantWake {
				t.Errorf("woke deacon = %v, want %v\ndaemon log:\n%s", woke, tc.wantWake, logBuf.String())
			}
		})
	}
}

// The default must be on: the failure this guards against is silent, and every
// status surface reports a stopped agent as running.
func TestPatrolWakeEnabledByDefault(t *testing.T) {
	var empty *config.DaemonThresholds
	if !empty.PatrolWakeEnabledV() {
		t.Error("PatrolWakeEnabledV() on nil thresholds = false, want true")
	}
	if got := empty.PatrolWakeCooldownD(); got != config.DefaultPatrolWakeCooldown {
		t.Errorf("PatrolWakeCooldownD() = %v, want %v", got, config.DefaultPatrolWakeCooldown)
	}
}

// A role whose patrol an operator turned off must not be woken: restarting a
// loop that was deliberately stopped is worse than leaving it stopped.
func TestPatrolWakeTargetsRespectDisabledPatrols(t *testing.T) {
	logBuf := &strings.Builder{}
	d := newPatrolWakeDaemon(t, t.TempDir(), nil, logBuf)

	if got := len(d.patrolWakeTargets()); got != 1 {
		t.Fatalf("target count with all patrols enabled = %d, want 1 (the Deacon; no rigs configured)", got)
	}

	d.disabledPatrols = map[string]bool{"deacon": true}
	if got := d.patrolWakeTargets(); len(got) != 0 {
		t.Errorf("targets with deacon patrol disabled = %v, want none", got)
	}
}
