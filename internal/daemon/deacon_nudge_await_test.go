package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// The nudge path's skip is the failure gt-ghw7 documents: with a stale
// heartbeat and no work in flight, the daemon declined to nudge the Deacon on
// the premise that "await-signal will fire naturally". When the Deacon had
// parked with no await, nothing fired and nothing else in the town wakes it —
// it is the head of the wake chain. The skip logged no error, so the stall was
// invisible; it was observed five times in one night and cleared each time by a
// manual nudge from a human-driven witness.
//
// The two rows that matter are the pending one and the absent one. Everything
// else about the decision — staleness tiers, crash-loop guard, session
// existence — is unchanged and covered elsewhere.
func TestCheckDeaconHeartbeat_NudgeSkipRequiresALiveAwait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	tests := []struct {
		name      string
		procs     []string
		procsErr  error
		wantNudge bool
		desc      string
	}{
		{
			name:      "await pending — skip, the wait really will fire",
			procs:     []string{psDeaconAwait},
			wantNudge: false,
			desc:      "Nudging here interrupts a healthy backoff, which is why the guard exists",
		},
		{
			name:      "no await — nudge, nothing is going to fire",
			procs:     []string{psOtherRigWitnessAwait, psRefineryAwait},
			wantNudge: true,
			desc:      "gt-ghw7: the parked Deacon the daemon used to leave parked",
		},
		{
			name:      "empty process table — nudge",
			procs:     nil,
			wantNudge: true,
			desc:      "No await anywhere on the host: the premise is false",
		},
		{
			name:      "unreadable process table — skip as before",
			procsErr:  fmt.Errorf("ps: command not found"),
			wantNudge: false,
			desc:      "No evidence either way: pre-check behavior stands",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			paneDir := t.TempDir()
			nudgeLog := filepath.Join(t.TempDir(), "nudges.log")
			townRoot := t.TempDir()

			writeFakePaneTmux(t, binDir, paneDir)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(tmux.TestNudgeLogEnv, nudgeLog)

			// Stale but not very stale: the 5–20 minute band is the one that
			// nudges rather than kills and restarts.
			writeDeaconHeartbeat(t, townRoot, 10*time.Minute)

			// The session has to exist, or the nudge path is never reached and
			// an empty nudge log would prove nothing.
			deaconSession := session.DeaconSessionName()
			writePaneFixture(t, paneDir, deaconSession, stoppedPane())

			defer stubProcessTable(t, tc.procs, tc.procsErr)()

			logBuf := &strings.Builder{}
			d := &Daemon{
				config: &Config{TownRoot: townRoot},
				logger: log.New(logBuf, "", 0),
				tmux:   tmux.NewTmux(),
				ctx:    context.Background(),
				// No in_progress or hooked beads: hasActiveWork is false, which
				// is the branch the skip lives on.
				beadsStores: map[string]beadsdk.Storage{
					"hq": &searchStorage{results: map[string][]*beadsdk.Issue{}},
				},
			}

			d.checkDeaconHeartbeat()

			nudged := strings.Contains(readNudgeLog(t, nudgeLog), "nudge:"+deaconSession+":")
			if nudged != tc.wantNudge {
				t.Errorf("%s\nnudged = %v, want %v\ndaemon log:\n%s", tc.desc, nudged, tc.wantNudge, logBuf.String())
			}
		})
	}
}

// The daemon must not claim an await will fire when it has just established
// that none exists. The log line is the only surface this decision has, and a
// line asserting something untrue is what made the original stall unreadable.
func TestCheckDeaconHeartbeat_DoesNotLogAFalsePremise(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	binDir := t.TempDir()
	paneDir := t.TempDir()
	nudgeLog := filepath.Join(t.TempDir(), "nudges.log")
	townRoot := t.TempDir()

	writeFakePaneTmux(t, binDir, paneDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(tmux.TestNudgeLogEnv, nudgeLog)
	writeDeaconHeartbeat(t, townRoot, 10*time.Minute)
	writePaneFixture(t, paneDir, session.DeaconSessionName(), stoppedPane())

	defer stubProcessTable(t, nil, nil)()

	logBuf := &strings.Builder{}
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(logBuf, "", 0),
		tmux:   tmux.NewTmux(),
		ctx:    context.Background(),
		beadsStores: map[string]beadsdk.Storage{
			"hq": &searchStorage{results: map[string][]*beadsdk.Issue{}},
		},
	}
	d.checkDeaconHeartbeat()

	if strings.Contains(logBuf.String(), "will fire naturally") {
		t.Errorf("daemon logged that an await will fire with no await on the host:\n%s", logBuf.String())
	}
}
