package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/events"
)

func TestCalculateEffectiveTimeout(t *testing.T) {
	tests := []struct {
		name        string
		timeout     string
		backoffBase string
		backoffMult int
		backoffMax  string
		idleCycles  int
		want        time.Duration
		wantErr     bool
	}{
		{
			name:    "simple timeout 60s",
			timeout: "60s",
			want:    60 * time.Second,
		},
		{
			name:    "simple timeout 5m",
			timeout: "5m",
			want:    5 * time.Minute,
		},
		{
			name:        "backoff base only, idle=0",
			timeout:     "60s",
			backoffBase: "30s",
			idleCycles:  0,
			want:        30 * time.Second,
		},
		{
			name:        "backoff with idle=1, mult=2",
			timeout:     "60s",
			backoffBase: "30s",
			backoffMult: 2,
			idleCycles:  1,
			want:        60 * time.Second,
		},
		{
			name:        "backoff with idle=2, mult=2",
			timeout:     "60s",
			backoffBase: "30s",
			backoffMult: 2,
			idleCycles:  2,
			want:        2 * time.Minute,
		},
		{
			name:        "backoff with max cap",
			timeout:     "60s",
			backoffBase: "30s",
			backoffMult: 2,
			backoffMax:  "5m",
			idleCycles:  10, // Would be 30s * 2^10 = ~8.5h but capped at 5m
			want:        5 * time.Minute,
		},
		{
			name:        "backoff overflow guard: idle=34 with max cap",
			timeout:     "60s",
			backoffBase: "30s",
			backoffMult: 2,
			backoffMax:  "5m",
			idleCycles:  34, // 30s * 2^34 overflows int64; must clamp to 5m
			want:        5 * time.Minute,
		},
		{
			name:        "backoff base exceeds max",
			timeout:     "60s",
			backoffBase: "15m",
			backoffMax:  "10m",
			want:        10 * time.Minute,
		},
		{
			name:    "invalid timeout",
			timeout: "invalid",
			wantErr: true,
		},
		{
			name:        "invalid backoff base",
			timeout:     "60s",
			backoffBase: "invalid",
			wantErr:     true,
		},
		{
			name:        "invalid backoff max",
			timeout:     "60s",
			backoffBase: "30s",
			backoffMax:  "invalid",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set package-level variables
			awaitSignalTimeout = tt.timeout
			awaitSignalBackoffBase = tt.backoffBase
			awaitSignalBackoffMult = tt.backoffMult
			if tt.backoffMult == 0 {
				awaitSignalBackoffMult = 2 // default
			}
			awaitSignalBackoffMax = tt.backoffMax

			got, err := calculateEffectiveTimeout(tt.idleCycles)
			if (err != nil) != tt.wantErr {
				t.Errorf("calculateEffectiveTimeout() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("calculateEffectiveTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAwaitSignalResult(t *testing.T) {
	// Test that result struct marshals correctly
	result := AwaitSignalResult{
		Reason:  "signal",
		Elapsed: 5 * time.Second,
		Signal:  "[12:34:56] + gt-abc created · New issue",
	}

	if result.Reason != "signal" {
		t.Errorf("expected reason 'signal', got %q", result.Reason)
	}
	if result.Signal == "" {
		t.Error("expected signal to be set")
	}
}

func TestWaitForEventsFile_MissingFile(t *testing.T) {
	// When the events file doesn't exist, waitForEventsFile creates it and
	// waits for new events. With no events, it should return timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result, err := waitForEventsFile(ctx, filepath.Join(t.TempDir(), "nonexistent.jsonl"), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "timeout" {
		t.Errorf("expected reason 'timeout', got %q", result.Reason)
	}
}

func TestWaitForEventsFile_Timeout(t *testing.T) {
	// When no new events are appended, waitForEventsFile should return timeout.
	eventsPath := filepath.Join(t.TempDir(), ".events.jsonl")
	if err := os.WriteFile(eventsPath, []byte(`{"ts":"2024-01-01","type":"test"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result, err := waitForEventsFile(ctx, eventsPath, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "timeout" {
		t.Errorf("expected reason 'timeout', got %q", result.Reason)
	}
}

func TestWaitForEventsFile_Signal(t *testing.T) {
	// When a new event is appended, waitForEventsFile should return signal.
	eventsPath := filepath.Join(t.TempDir(), ".events.jsonl")
	// Write initial content (will be skipped — we seek to end)
	if err := os.WriteFile(eventsPath, []byte(`{"ts":"old","type":"ignore"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Append a new line after a short delay
	go func() {
		time.Sleep(300 * time.Millisecond)
		f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.WriteString(`{"ts":"new","type":"sling","actor":"test"}` + "\n")
	}()

	result, err := waitForEventsFile(ctx, eventsPath, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "signal" {
		t.Errorf("expected reason 'signal', got %q", result.Reason)
	}
	if result.Signal == "" {
		t.Error("expected signal line to be set")
	}
}

func TestWaitForActivitySignal_PathWiring(t *testing.T) {
	// Verify waitForActivitySignal constructs the correct events path from
	// townRoot. The events file should be at <townRoot>/.events.jsonl.
	townRoot := t.TempDir()
	eventsPath := filepath.Join(townRoot, ".events.jsonl")
	if err := os.WriteFile(eventsPath, []byte(`{"ts":"old","type":"ignore"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Append a new event after a short delay
	go func() {
		time.Sleep(200 * time.Millisecond)
		f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.WriteString(`{"ts":"new","type":"sling"}` + "\n")
	}()

	result, err := waitForActivitySignal(ctx, townRoot, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "signal" {
		t.Errorf("expected reason 'signal', got %q", result.Reason)
	}
}

func TestBackoffWindowResumption(t *testing.T) {
	// Test the backoff window resumption logic that makes await-signal
	// resilient to interrupts. When a backoff-until timestamp is in the
	// future and remaining time <= full timeout, use remaining time.
	now := time.Now()

	tests := []struct {
		name           string
		fullTimeout    time.Duration
		backoffUntil   time.Time
		wantResumed    bool
		wantApproxTime time.Duration // approximate expected timeout
	}{
		{
			name:           "no stored window - use full timeout",
			fullTimeout:    5 * time.Minute,
			backoffUntil:   time.Time{}, // zero value
			wantResumed:    false,
			wantApproxTime: 5 * time.Minute,
		},
		{
			name:           "window in future - resume with remaining",
			fullTimeout:    5 * time.Minute,
			backoffUntil:   now.Add(2 * time.Minute),
			wantResumed:    true,
			wantApproxTime: 2 * time.Minute,
		},
		{
			name:           "window expired - use full timeout",
			fullTimeout:    5 * time.Minute,
			backoffUntil:   now.Add(-1 * time.Minute), // in the past
			wantResumed:    false,
			wantApproxTime: 5 * time.Minute,
		},
		{
			name:           "window exceeds full timeout (stale) - use full timeout",
			fullTimeout:    2 * time.Minute,
			backoffUntil:   now.Add(10 * time.Minute), // remaining > full
			wantResumed:    false,
			wantApproxTime: 2 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			timeout := tt.fullTimeout
			resumed := false

			if !tt.backoffUntil.IsZero() && tt.backoffUntil.After(now) {
				remaining := tt.backoffUntil.Sub(now)
				if remaining <= tt.fullTimeout {
					timeout = remaining
					resumed = true
				}
			}

			if resumed != tt.wantResumed {
				t.Errorf("resumed = %v, want %v", resumed, tt.wantResumed)
			}

			// Allow 2s tolerance for timing
			diff := timeout - tt.wantApproxTime
			if diff < 0 {
				diff = -diff
			}
			if diff > 2*time.Second {
				t.Errorf("timeout = %v, want ~%v (diff: %v)", timeout, tt.wantApproxTime, diff)
			}
		})
	}
}

func TestRunMoleculeAwaitSignalAgentBeadUsesCwdRigBeadsDirWhenBeadsDirPointsTown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	tmp := t.TempDir()
	townRoot := filepath.Join(tmp, "gt")
	townBeads := filepath.Join(townRoot, ".beads")
	rigWorkDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	rigRedirect := filepath.Join(rigWorkDir, ".beads")
	rigBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")

	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		townBeads,
		rigRedirect,
		rigBeads,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigRedirect, "redirect"), []byte("../../mayor/rig/.beads"), 0o644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}
	metadata := []byte(`{"dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`)
	if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), metadata, 0o644); err != nil {
		t.Fatalf("write rig metadata: %v", err)
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath := filepath.Join(tmp, "bd.log")
	bdScript := `#!/bin/sh
printf 'cmd=%s BEADS_DIR=%s DB=%s READONLY=%s AUTO=%s\n' "$1" "${BEADS_DIR-}" "${BEADS_DOLT_SERVER_DATABASE-}" "${BD_READONLY-}" "${BD_DOLT_AUTO_COMMIT-}" >> "$BD_LOG"
case "$1" in
  show)
    printf '[{"labels":["gt:agent","idle:0"]}]\n'
    ;;
  update)
    ;;
  *)
    printf 'unexpected bd command: %s\n' "$1" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	t.Setenv("BEADS_DIR", townBeads)
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "town")
	t.Setenv("BD_READONLY", "true")
	t.Setenv("BD_DOLT_AUTO_COMMIT", "off")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(rigWorkDir); err != nil {
		t.Fatalf("chdir rig work dir: %v", err)
	}

	oldTimeout := awaitSignalTimeout
	oldBackoffBase := awaitSignalBackoffBase
	oldBackoffMult := awaitSignalBackoffMult
	oldBackoffMax := awaitSignalBackoffMax
	oldQuiet := awaitSignalQuiet
	oldAgentBead := awaitSignalAgentBead
	oldJSON := moleculeJSON
	t.Cleanup(func() {
		awaitSignalTimeout = oldTimeout
		awaitSignalBackoffBase = oldBackoffBase
		awaitSignalBackoffMult = oldBackoffMult
		awaitSignalBackoffMax = oldBackoffMax
		awaitSignalQuiet = oldQuiet
		awaitSignalAgentBead = oldAgentBead
		moleculeJSON = oldJSON
	})

	awaitSignalTimeout = "1ms"
	awaitSignalBackoffBase = ""
	awaitSignalBackoffMult = 2
	awaitSignalBackoffMax = ""
	awaitSignalQuiet = true
	awaitSignalAgentBead = "gt-gastown-refinery"
	moleculeJSON = false

	if err := runMoleculeAwaitSignal(nil, nil); err != nil {
		t.Fatalf("runMoleculeAwaitSignal() error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	log := strings.TrimSpace(string(data))
	if log == "" {
		t.Fatal("fake bd was not invoked")
	}

	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, "BEADS_DIR="+rigBeads) {
			t.Fatalf("bd call was not pinned to rig beads %q: %s\nfull log:\n%s", rigBeads, line, log)
		}
		if strings.Contains(line, "BEADS_DIR="+townBeads) {
			t.Fatalf("bd call used inherited town BEADS_DIR %q: %s\nfull log:\n%s", townBeads, line, log)
		}
		if !strings.Contains(line, "DB=rigdb") {
			t.Fatalf("bd call was not pinned to rig database: %s\nfull log:\n%s", line, log)
		}
		if strings.Contains(line, "DB=town") {
			t.Fatalf("bd call used inherited town database: %s\nfull log:\n%s", line, log)
		}
		if strings.Contains(line, "cmd=show") {
			if !strings.Contains(line, "READONLY=true") || !strings.Contains(line, "AUTO=off") {
				t.Fatalf("bd read was not read-only pinned: %s\nfull log:\n%s", line, log)
			}
		}
		if strings.Contains(line, "cmd=update") {
			if !strings.Contains(line, "READONLY= ") && !strings.HasSuffix(line, "READONLY= AUTO=on") {
				t.Fatalf("bd mutation inherited read-only mode: %s\nfull log:\n%s", line, log)
			}
			if !strings.Contains(line, "AUTO=on") {
				t.Fatalf("bd mutation was not auto-commit pinned: %s\nfull log:\n%s", line, log)
			}
		}
	}
}

// awaitSignalIdleRun is the observable result of one runMoleculeAwaitSignal
// invocation against the fake bd: every bd argv line it saw, and the label set
// left on the agent bead afterwards.
type awaitSignalIdleRun struct {
	log         string
	finalLabels []string
}

// updateCalls returns the bd update invocations from the run's log.
func (r awaitSignalIdleRun) updateCalls() []string {
	var updates []string
	for _, line := range strings.Split(r.log, "\n") {
		if strings.HasPrefix(line, "update ") {
			updates = append(updates, line)
		}
	}
	return updates
}

// idleLabel returns the idle:N value left on the agent bead, or "" if absent.
func (r awaitSignalIdleRun) idleLabel() string {
	for _, l := range r.finalLabels {
		if strings.HasPrefix(l, "idle:") {
			return strings.TrimPrefix(l, "idle:")
		}
	}
	return ""
}

// runAwaitSignalIdle drives runMoleculeAwaitSignal against a stateful fake bd
// and a throwaway town root.
//
// The harness is deliberately self-contained: the only reachable bd is the
// stub, so nothing touches the production Dolt server, and no delivery path
// (tmux or otherwise) is exercised.
//
// The stub is stateful — update --set-labels replaces the label set that the
// next show returns — because await-signal issues several read-modify-write
// label updates per invocation and a fixed-response stub would make their
// ordering unobservable.
//
// With sendSignal, a line is appended to .events.jsonl shortly after the wait
// begins so waitForActivitySignal returns reason "signal"; otherwise the wait
// runs to timeout.
func runAwaitSignalIdle(t *testing.T, labels []string, sendSignal bool) awaitSignalIdleRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "mayor", "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Chdir(root)

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath := filepath.Join(root, "bd.log")
	statePath := filepath.Join(root, "labels.txt")
	if err := os.WriteFile(statePath, []byte(strings.Join(labels, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write initial labels: %v", err)
	}

	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
STATE=%q
case "$1" in
show)
  out=
  while IFS= read -r l; do
    [ -z "$l" ] && continue
    if [ -z "$out" ]; then out="\"$l\""; else out="$out,\"$l\""; fi
  done < "$STATE"
  printf '[{"labels":[%%s]}]\n' "$out"
  ;;
update)
  : > "$STATE.tmp"
  for a in "$@"; do
    case "$a" in
      --set-labels=?*) printf '%%s\n' "${a#--set-labels=}" >> "$STATE.tmp" ;;
    esac
  done
  mv "$STATE.tmp" "$STATE"
  ;;
esac
exit 0
`, logPath, statePath)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	eventsPath := filepath.Join(root, events.EventsFile)
	if err := os.WriteFile(eventsPath, nil, 0o644); err != nil {
		t.Fatalf("create events file: %v", err)
	}

	oldTimeout := awaitSignalTimeout
	oldBackoffBase := awaitSignalBackoffBase
	oldBackoffMult := awaitSignalBackoffMult
	oldBackoffMax := awaitSignalBackoffMax
	oldQuiet := awaitSignalQuiet
	oldAgentBead := awaitSignalAgentBead
	oldJSON := moleculeJSON
	t.Cleanup(func() {
		awaitSignalTimeout = oldTimeout
		awaitSignalBackoffBase = oldBackoffBase
		awaitSignalBackoffMult = oldBackoffMult
		awaitSignalBackoffMax = oldBackoffMax
		awaitSignalQuiet = oldQuiet
		awaitSignalAgentBead = oldAgentBead
		moleculeJSON = oldJSON
	})

	awaitSignalBackoffBase = ""
	awaitSignalBackoffMult = 2
	awaitSignalBackoffMax = ""
	awaitSignalQuiet = true
	awaitSignalAgentBead = "gt-test-witness"
	moleculeJSON = false

	if sendSignal {
		// Generous timeout: the appended event, not the clock, must end the wait.
		awaitSignalTimeout = "10s"
		done := make(chan struct{})
		go func() {
			defer close(done)
			time.Sleep(300 * time.Millisecond)
			f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return
			}
			defer f.Close()
			_, _ = f.WriteString(`{"type":"test_activity"}` + "\n")
		}()
		t.Cleanup(func() { <-done })
	} else {
		awaitSignalTimeout = "80ms"
	}

	if err := runMoleculeAwaitSignal(nil, nil); err != nil {
		t.Fatalf("runMoleculeAwaitSignal: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read final labels: %v", err)
	}

	var final []string
	for _, l := range strings.Split(string(stateData), "\n") {
		if l != "" {
			final = append(final, l)
		}
	}
	return awaitSignalIdleRun{log: string(logData), finalLabels: final}
}

// TestAwaitSignalResetsIdleOnSignalReceived is the regression test for gt-609:
// a received signal must walk the idle counter back to 0 in the binary rather
// than delegating that to the caller's patrol formula. Before the fix, idle
// survived a signal and ratcheted upward until the agent parked at the backoff
// cap and slept through real work.
func TestAwaitSignalResetsIdleOnSignalReceived(t *testing.T) {
	run := runAwaitSignalIdle(t, []string{"gt:agent", "idle:3"}, true)

	if len(run.updateCalls()) == 0 {
		t.Fatalf("expected bd update calls, log:\n%s", run.log)
	}
	if got := run.idleLabel(); got != "0" {
		t.Fatalf("idle after signal = %q, want %q (a signal must reset the counter)\nlabels: %v\nlog:\n%s",
			got, "0", run.finalLabels, run.log)
	}
}

// TestAwaitSignalTimeoutStillIncrementsIdle guards the other half of the
// backoff: the reset must not disturb the timeout-side increment.
func TestAwaitSignalTimeoutStillIncrementsIdle(t *testing.T) {
	run := runAwaitSignalIdle(t, []string{"gt:agent", "idle:3"}, false)

	if len(run.updateCalls()) == 0 {
		t.Fatalf("expected bd update calls, log:\n%s", run.log)
	}
	if got := run.idleLabel(); got != "4" {
		t.Fatalf("idle after timeout = %q, want %q\nlabels: %v\nlog:\n%s",
			got, "4", run.finalLabels, run.log)
	}
}

// TestAwaitSignalResetPreservesOtherLabels checks the reset uses the same
// read-modify-write path as the increment and does not drop unrelated labels.
func TestAwaitSignalResetPreservesOtherLabels(t *testing.T) {
	run := runAwaitSignalIdle(t, []string{"gt:agent", "role:witness", "idle:2"}, true)

	for _, want := range []string{"gt:agent", "role:witness"} {
		found := false
		for _, l := range run.finalLabels {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("label %q was dropped by the reset; final labels: %v\nlog:\n%s",
				want, run.finalLabels, run.log)
		}
	}
}

// TestAwaitSignalSkipsIdleWriteWhenAlreadyZero checks the idleCycles > 0 guard.
// Every bd mutation is a permanent Dolt commit, so an agent woken repeatedly
// while already at idle:0 must not write the label it would not change. The
// assertion is relative — one fewer update than the same run from idle:3 —
// so it does not hard-code the total number of label writes per invocation.
func TestAwaitSignalSkipsIdleWriteWhenAlreadyZero(t *testing.T) {
	fromZero := len(runAwaitSignalIdle(t, []string{"gt:agent", "idle:0"}, true).updateCalls())
	fromThree := len(runAwaitSignalIdle(t, []string{"gt:agent", "idle:3"}, true).updateCalls())

	if fromZero != fromThree-1 {
		t.Errorf("update calls from idle:0 = %d, from idle:3 = %d; want exactly one fewer "+
			"(the reset write should be skipped when the counter is already 0)", fromZero, fromThree)
	}
}

// TestBackoffAtCap covers the health signal for an agent parked at the backoff
// cap, which is otherwise indistinguishable from a healthy briefly-idle one.
func TestBackoffAtCap(t *testing.T) {
	oldBase := awaitSignalBackoffBase
	oldMax := awaitSignalBackoffMax
	t.Cleanup(func() {
		awaitSignalBackoffBase = oldBase
		awaitSignalBackoffMax = oldMax
	})

	tests := []struct {
		name        string
		base        string
		max         string
		fullTimeout time.Duration
		want        bool
	}{
		{"simple timeout mode is never at cap", "", "", time.Hour, false},
		{"backoff without a max is never at cap", "30s", "", time.Hour, false},
		{"below cap", "30s", "15m", 4 * time.Minute, false},
		{"exactly at cap", "30s", "15m", 15 * time.Minute, true},
		{"clamped to cap", "30s", "15m", 30 * time.Minute, true},
		{"unparseable max is not at cap", "30s", "not-a-duration", time.Hour, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			awaitSignalBackoffBase = tt.base
			awaitSignalBackoffMax = tt.max
			if got := backoffAtCap(tt.fullTimeout); got != tt.want {
				t.Errorf("backoffAtCap(%v) = %v, want %v", tt.fullTimeout, got, tt.want)
			}
		})
	}
}

// --- rig scoping (gt-p54t) ---
//
// The events feed is town-wide. Before scoping, an idle rig's patrol agent
// returned from await-signal on every event any rig produced, so the
// exponential backoff could never grow. These tests exercise the real tailing
// loop, and they pin BOTH halves: an event confined to another rig must stop
// waking, and a cross-rig mail addressed to this rig must keep waking. A test
// that only asserted the suppression would pass just as well if the filter had
// gone deaf.

// appendEvents writes lines to an events file the way the feed does.
func appendEvents(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		t.Fatalf("opening events file: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("appending event: %v", err)
		}
	}
}

const (
	// An event wholly confined to the beads rig.
	evtBeadsSpawn = `{"ts":"2026-08-23T01:45:06Z","source":"gt","type":"spawn","actor":"gt","payload":{"polecat":"ace","rig":"beads"},"visibility":"feed"}`
	// Mail sent from beads to gastown. Mail wakes are town-wide by design.
	evtBeadsMailToGastown = `{"ts":"2026-08-23T01:45:07Z","source":"gt","type":"mail","actor":"beads/polecats/ace","payload":{"subject":"cross-rig","to":"gastown/witness"},"visibility":"feed"}`
	// Deacon-to-mayor traffic: town-scoped, confined to no rig.
	evtTownMail = `{"ts":"2026-08-23T01:45:08Z","source":"gt","type":"mail","actor":"deacon/","payload":{"subject":"digest","to":"mayor/"},"visibility":"feed"}`
	// Gastown's own dispatch.
	evtGastownSling = `{"ts":"2026-08-23T01:45:09Z","source":"gt","type":"sling","actor":"unknown","payload":{"bead":"gt-p54t","target":"gastown/polecats/dust"},"visibility":"feed"}`
)

// newEventsFile returns the path to an empty events file with one historical
// line, matching the live file's state when a wait begins (the wait seeks to
// end, so the historical line must never be read).
func newEventsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".events.jsonl")
	if err := os.WriteFile(path, []byte(`{"ts":"old","type":"ignore"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWaitForEventsFile_ForeignRigEventDoesNotWake(t *testing.T) {
	// The defect: this event, confined to the beads rig, used to wake gastown.
	eventsPath := newEventsFile(t)

	go func() {
		time.Sleep(200 * time.Millisecond)
		appendEvents(t, eventsPath, evtBeadsSpawn)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := waitForEventsFile(ctx, eventsPath, "gastown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "timeout" {
		t.Errorf("reason = %q, want %q: an event confined to beads must not wake gastown",
			result.Reason, "timeout")
	}
	if result.Suppressed != 1 {
		t.Errorf("Suppressed = %d, want 1: the wake that did not happen must be counted, "+
			"or a filter that never saw the event is indistinguishable from one that dropped it",
			result.Suppressed)
	}
}

func TestWaitForEventsFile_ForeignRigEventStillWakesItsOwnRig(t *testing.T) {
	// Positive control for the test above: the same line, the same tailing
	// loop, a watcher on the rig the event belongs to. Without this, a filter
	// that woke nobody at all would pass the suppression test.
	eventsPath := newEventsFile(t)

	go func() {
		time.Sleep(200 * time.Millisecond)
		appendEvents(t, eventsPath, evtBeadsSpawn)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := waitForEventsFile(ctx, eventsPath, "beads")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "signal" {
		t.Fatalf("reason = %q, want %q: beads must still wake on its own event",
			result.Reason, "signal")
	}
	if result.Suppressed != 0 {
		t.Errorf("Suppressed = %d, want 0", result.Suppressed)
	}
}

func TestWaitForEventsFile_CrossRigMailStillWakesAddressee(t *testing.T) {
	// The half a rig filter is most likely to break: mail from another rig
	// addressed to this one. It must keep waking, or the fix trades a cost
	// defect for a correctness one.
	eventsPath := newEventsFile(t)

	go func() {
		time.Sleep(200 * time.Millisecond)
		appendEvents(t, eventsPath, evtBeadsMailToGastown)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := waitForEventsFile(ctx, eventsPath, "gastown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "signal" {
		t.Fatalf("reason = %q, want %q: mail from beads addressed to gastown/witness "+
			"must wake gastown", result.Reason, "signal")
	}
	if !strings.Contains(result.Signal, "gastown/witness") {
		t.Errorf("Signal = %q, want the cross-rig mail line", result.Signal)
	}
}

func TestWaitForEventsFile_TownScopedEventWakesEveryRig(t *testing.T) {
	// Deacon-to-mayor traffic is not "confined to another rig", so it is not
	// suppressed. This is the deliberate fail-open path.
	eventsPath := newEventsFile(t)

	go func() {
		time.Sleep(200 * time.Millisecond)
		appendEvents(t, eventsPath, evtTownMail)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := waitForEventsFile(ctx, eventsPath, "duly_noted")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "signal" {
		t.Errorf("reason = %q, want %q: town-scoped events wake every rig",
			result.Reason, "signal")
	}
}

// TestWaitForEventsFile_BurstMeasuresWakes replays one burst through the same
// tailing loop at three scopes and reports the wake count for each. This is the
// before/after measurement over identical event volume: town-wide is the
// "before", the rig scopes are the "after".
func TestWaitForEventsFile_BurstMeasuresWakes(t *testing.T) {
	// Three beads events then one gastown event. No town-scoped line here on
	// purpose: those wake every rig, so including one would end the wait early
	// and the burst would measure the fail-open path instead of the filter.
	burst := []string{
		evtBeadsSpawn,
		evtBeadsSpawn,
		evtBeadsSpawn,
		evtGastownSling,
	}

	// A burst arriving inside one 200ms poll tick must not cost one tick per
	// foreign line before the matching line is seen.
	t.Run("gastown wakes on its own line after skipping the foreign ones", func(t *testing.T) {
		eventsPath := newEventsFile(t)
		go func() {
			time.Sleep(200 * time.Millisecond)
			appendEvents(t, eventsPath, burst...)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := waitForEventsFile(ctx, eventsPath, "gastown")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Reason != "signal" {
			t.Fatalf("reason = %q, want %q", result.Reason, "signal")
		}
		if !strings.Contains(result.Signal, "gt-p54t") {
			t.Errorf("Signal = %q, want the gastown sling", result.Signal)
		}
		t.Logf("gastown: suppressed %d of %d burst events before waking",
			result.Suppressed, len(burst))
		if result.Suppressed != 3 {
			t.Errorf("Suppressed = %d, want 3 (the three leading beads events, all "+
				"drained inside one poll tick)", result.Suppressed)
		}
	})

	t.Run("an idle rig sleeps through the whole burst", func(t *testing.T) {
		eventsPath := newEventsFile(t)
		go func() {
			time.Sleep(200 * time.Millisecond)
			appendEvents(t, eventsPath, burst...)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		result, err := waitForEventsFile(ctx, eventsPath, "duly_noted")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		t.Logf("duly_noted: reason=%s suppressed=%d of %d", result.Reason, result.Suppressed, len(burst))
		if result.Reason != "timeout" {
			t.Errorf("reason = %q, want %q: none of these events concerns duly_noted",
				result.Reason, "timeout")
		}
		if result.Suppressed != len(burst) {
			t.Errorf("Suppressed = %d, want %d: every one is a wake an idle rig used to pay for",
				result.Suppressed, len(burst))
		}
	})

	t.Run("town-wide is the unfiltered before-state", func(t *testing.T) {
		eventsPath := newEventsFile(t)
		go func() {
			time.Sleep(200 * time.Millisecond)
			appendEvents(t, eventsPath, burst[0])
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		result, err := waitForEventsFile(ctx, eventsPath, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Reason != "signal" {
			t.Errorf("reason = %q, want %q: --all-rigs must behave exactly as before",
				result.Reason, "signal")
		}
		if result.Suppressed != 0 {
			t.Errorf("Suppressed = %d, want 0: a town-wide wait filters nothing", result.Suppressed)
		}
	})
}

func TestWaitForEventsFile_PartialLineIsNotAWake(t *testing.T) {
	// A line that has not landed whole yet is a JSON fragment. Evaluating it
	// would fail to parse and fail open into exactly the spurious wake this
	// filter exists to prevent, so fragments are held until complete.
	eventsPath := newEventsFile(t)

	go func() {
		f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		defer f.Close()
		time.Sleep(200 * time.Millisecond)
		_, _ = f.WriteString(evtBeadsSpawn[:40]) // no newline: torn write
		time.Sleep(500 * time.Millisecond)
		_, _ = f.WriteString(evtBeadsSpawn[40:] + "\n")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := waitForEventsFile(ctx, eventsPath, "gastown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "timeout" {
		t.Errorf("reason = %q, want %q: a torn beads event must not wake gastown "+
			"once its halves are rejoined", result.Reason, "timeout")
	}
	if result.Suppressed != 1 {
		t.Errorf("Suppressed = %d, want 1: the rejoined line should be evaluated exactly once",
			result.Suppressed)
	}
}

func TestWaitForEventsFile_EventJustBeforeDeadlineStillWakes(t *testing.T) {
	// The race: select does not favor ctx.Done() or ticker.C when both are
	// ready, and an event written after the last poll but before the deadline
	// fires sits unread in the file at the instant ctx.Done() is chosen.
	// Lengthening the poll interval past the ctx timeout forces that instant
	// deterministically — no ticker fire happens at all, so this exercises only
	// the ctx.Done() path (gt-5sxz).
	oldInterval := awaitSignalPollInterval
	awaitSignalPollInterval = time.Hour
	t.Cleanup(func() { awaitSignalPollInterval = oldInterval })

	eventsPath := newEventsFile(t)

	go func() {
		time.Sleep(20 * time.Millisecond)
		appendEvents(t, eventsPath, evtGastownSling)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	result, err := waitForEventsFile(ctx, eventsPath, "gastown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "signal" {
		t.Fatalf("reason = %q, want %q: the event landed 30ms before the deadline "+
			"but with no ticker fire in between it was reported as a timeout", result.Reason, "signal")
	}
	if !strings.Contains(result.Signal, "gt-p54t") {
		t.Errorf("Signal = %q, want the gastown sling", result.Signal)
	}
}

func TestResolveAwaitSignalRig(t *testing.T) {
	oldRig, oldAll := awaitSignalRig, awaitSignalAllRigs
	t.Cleanup(func() {
		awaitSignalRig, awaitSignalAllRigs = oldRig, oldAll
	})

	// A town with one real rig, so cwd inference has something to find.
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "gastown", "witness")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "gastown", "config.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	deaconDir := filepath.Join(townRoot, "deacon", "dogs", "boot")
	if err := os.MkdirAll(deaconDir, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		rig     string
		allRigs bool
		envRig  string
		chdir   string
		want    string
		wantErr bool
		why     string
	}{
		{name: "explicit --rig wins", rig: "beads", envRig: "gastown", chdir: rigDir,
			want: "beads", why: "the most explicit source"},
		{name: "--all-rigs is town-wide", allRigs: true, envRig: "gastown", chdir: rigDir,
			want: "", why: "Deacon and Mayor opt in explicitly"},
		{name: "--rig with --all-rigs is a contradiction", rig: "beads", allRigs: true,
			chdir: rigDir, wantErr: true, why: "silently picking one would hide the mistake"},
		{name: "GT_RIG when no flag", envRig: "beads", chdir: rigDir,
			want: "beads", why: "the session harness sets it"},
		{name: "cwd inference inside a rig", chdir: rigDir,
			want: "gastown", why: "a witness in its own rig defaults to that rig"},
		{name: "deacon dir infers no rig", chdir: deaconDir,
			want: "", why: "the Deacon is town-scoped and must keep waking town-wide"},
		{name: "town root infers no rig", chdir: townRoot,
			want: "", why: "not inside any rig"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			awaitSignalRig, awaitSignalAllRigs = tt.rig, tt.allRigs
			t.Setenv("GT_RIG", tt.envRig)
			if tt.chdir != "" {
				t.Chdir(tt.chdir)
			}

			got, err := resolveAwaitSignalRig(townRoot)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error (%s)", tt.why)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveAwaitSignalRig() = %q, want %q (%s)", got, tt.want, tt.why)
			}
		})
	}
}
