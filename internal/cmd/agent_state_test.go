package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseStateLabels(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		wantKeys []string
	}{
		{
			name:     "empty labels",
			labels:   []string{},
			wantKeys: []string{},
		},
		{
			name:     "only non-state labels",
			labels:   []string{"role_type", "urgent"},
			wantKeys: []string{},
		},
		{
			name:     "only state labels",
			labels:   []string{"idle:3", "backoff:2m"},
			wantKeys: []string{"idle", "backoff"},
		},
		{
			name:     "mixed labels",
			labels:   []string{"role_type", "idle:5", "urgent", "backoff:30s"},
			wantKeys: []string{"idle", "backoff"},
		},
		{
			name:     "label with multiple colons",
			labels:   []string{"last_activity:2025-01-01T12:00:00Z"},
			wantKeys: []string{"last_activity"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := parseStateLabels(tt.labels)
			if len(labels) != len(tt.wantKeys) {
				t.Errorf("got %d labels, want %d", len(labels), len(tt.wantKeys))
				return
			}
			for _, key := range tt.wantKeys {
				if _, ok := labels[key]; !ok {
					t.Errorf("missing expected key: %s", key)
				}
			}
		})
	}
}

func TestApplyLabelOperations(t *testing.T) {
	tests := []struct {
		name      string
		initial   map[string]string
		setOps    []string
		incrKey   string
		delKeys   []string
		wantKeys  map[string]string
		wantError bool
	}{
		{
			name:     "set new label",
			initial:  map[string]string{},
			setOps:   []string{"idle=0"},
			wantKeys: map[string]string{"idle": "0"},
		},
		{
			name:     "set overwrites existing",
			initial:  map[string]string{"idle": "5"},
			setOps:   []string{"idle=0"},
			wantKeys: map[string]string{"idle": "0"},
		},
		{
			name:     "increment missing key creates with 1",
			initial:  map[string]string{},
			incrKey:  "idle",
			wantKeys: map[string]string{"idle": "1"},
		},
		{
			name:     "increment existing key",
			initial:  map[string]string{"idle": "3"},
			incrKey:  "idle",
			wantKeys: map[string]string{"idle": "4"},
		},
		{
			name:     "delete existing key",
			initial:  map[string]string{"idle": "3", "backoff": "2m"},
			delKeys:  []string{"idle"},
			wantKeys: map[string]string{"backoff": "2m"},
		},
		{
			name:     "delete non-existent key is noop",
			initial:  map[string]string{"idle": "3"},
			delKeys:  []string{"nonexistent"},
			wantKeys: map[string]string{"idle": "3"},
		},
		{
			name:      "invalid set format",
			initial:   map[string]string{},
			setOps:    []string{"invalid"},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := copyMap(tt.initial)
			err := applyLabelOperations(labels, tt.setOps, tt.incrKey, tt.delKeys)

			if tt.wantError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if len(labels) != len(tt.wantKeys) {
				t.Errorf("got %d labels, want %d", len(labels), len(tt.wantKeys))
				return
			}

			for key, wantVal := range tt.wantKeys {
				if gotVal, ok := labels[key]; !ok {
					t.Errorf("missing expected key: %s", key)
				} else if gotVal != wantVal {
					t.Errorf("labels[%s] = %s, want %s", key, gotVal, wantVal)
				}
			}
		})
	}
}

// parseStateLabels extracts state labels (key:value format) from all labels.
// This is a helper for testing that mirrors the logic in getAgentLabels.
func parseStateLabels(allLabels []string) map[string]string {
	labels := make(map[string]string)
	for _, label := range allLabels {
		if idx := indexOf(label, ":"); idx > 0 {
			labels[label[:idx]] = label[idx+1:]
		}
	}
	return labels
}

// indexOf returns the index of the first occurrence of substr in s, or -1 if not found.
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// applyLabelOperations applies set, increment, and delete operations to a label map.
// This mirrors the logic in modifyAgentState.
func applyLabelOperations(labels map[string]string, setOps []string, incrKey string, delKeys []string) error {
	// Apply increment
	if incrKey != "" {
		currentValue := 0
		if valStr, ok := labels[incrKey]; ok {
			for i := 0; i < len(valStr); i++ {
				if valStr[i] >= '0' && valStr[i] <= '9' {
					currentValue = currentValue*10 + int(valStr[i]-'0')
				}
			}
		}
		labels[incrKey] = intToString(currentValue + 1)
	}

	// Apply set operations
	for _, setOp := range setOps {
		idx := indexOf(setOp, "=")
		if idx <= 0 {
			return errors.New("invalid set format: " + setOp)
		}
		labels[setOp[:idx]] = setOp[idx+1:]
	}

	// Apply delete operations
	for _, delKey := range delKeys {
		delete(labels, delKey)
	}

	return nil
}

// copyMap creates a shallow copy of a string map.
func copyMap(m map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range m {
		result[k] = v
	}
	return result
}

// intToString converts an int to a string without using strconv.
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}

func TestParseAgentBeadLabels(t *testing.T) {
	tests := []struct {
		name       string
		stdout     []byte
		stderr     []byte
		agentBead  string
		wantLabels []string
		wantErr    string
	}{
		{
			name:       "valid response with labels",
			stdout:     []byte(`[{"id":"gt-test","labels":["idle:3","gt:agent"]}]`),
			stderr:     nil,
			agentBead:  "gt-test",
			wantLabels: []string{"idle:3", "gt:agent"},
			wantErr:    "",
		},
		{
			name:       "valid response with no labels",
			stdout:     []byte(`[{"id":"gt-test","labels":[]}]`),
			stderr:     nil,
			agentBead:  "gt-test",
			wantLabels: []string{},
			wantErr:    "",
		},
		{
			name:       "valid response with null labels",
			stdout:     []byte(`[{"id":"gt-test","labels":null}]`),
			stderr:     nil,
			agentBead:  "gt-test",
			wantLabels: nil,
			wantErr:    "",
		},
		{
			name:      "empty stdout with stderr",
			stdout:    []byte{},
			stderr:    []byte("database mismatch: client expects dolt but daemon has different backend"),
			agentBead: "gt-test",
			wantErr:   "database mismatch",
		},
		{
			name:      "empty stdout without stderr",
			stdout:    []byte{},
			stderr:    nil,
			agentBead: "gt-test",
			wantErr:   "agent bead query returned no output: gt-test",
		},
		{
			name:      "empty array response",
			stdout:    []byte(`[]`),
			stderr:    nil,
			agentBead: "gt-test",
			wantErr:   "agent bead not found: gt-test",
		},
		{
			name:      "invalid JSON",
			stdout:    []byte(`{not valid json`),
			stderr:    nil,
			agentBead: "gt-test",
			wantErr:   "parsing agent bead response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels, err := parseAgentBeadLabels(tt.stdout, tt.stderr, tt.agentBead)

			if tt.wantErr != "" {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.wantErr)
					return
				}
				if indexOf(err.Error(), tt.wantErr) < 0 {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if len(labels) != len(tt.wantLabels) {
				t.Errorf("got %d labels, want %d", len(labels), len(tt.wantLabels))
				return
			}

			for i, label := range labels {
				if label != tt.wantLabels[i] {
					t.Errorf("labels[%d] = %q, want %q", i, label, tt.wantLabels[i])
				}
			}
		})
	}
}

func TestLabelSetsEqual(t *testing.T) {
	tests := []struct {
		name    string
		current []string
		next    []string
		want    bool
	}{
		{
			name:    "identical order",
			current: []string{"gt:agent", "idle:0"},
			next:    []string{"gt:agent", "idle:0"},
			want:    true,
		},
		{
			name:    "same labels, different order",
			current: []string{"idle:0", "gt:agent", "heartbeat:17"},
			next:    []string{"heartbeat:17", "gt:agent", "idle:0"},
			want:    true,
		},
		{
			name:    "value differs",
			current: []string{"gt:agent", "idle:3"},
			next:    []string{"gt:agent", "idle:0"},
			want:    false,
		},
		{
			name:    "label added",
			current: []string{"gt:agent"},
			next:    []string{"gt:agent", "idle:0"},
			want:    false,
		},
		{
			name:    "label removed",
			current: []string{"gt:agent", "idle:0"},
			next:    []string{"gt:agent"},
			want:    false,
		},
		{
			name:    "both empty",
			current: nil,
			next:    nil,
			want:    true,
		},
		// A bead carrying the same label twice is not equivalent to one
		// carrying it once: the rebuild collapses the duplicate, and that
		// repair must still be written. A set-membership comparison would
		// call these equal and leave the bead malformed forever.
		{
			name:    "duplicate collapsed to single",
			current: []string{"idle:0", "idle:0", "gt:agent"},
			next:    []string{"idle:0", "gt:agent"},
			want:    false,
		},
		// Same length, disjoint contents — catches a comparator that only
		// checks length.
		{
			name:    "same length, different labels",
			current: []string{"idle:0", "gt:agent"},
			next:    []string{"idle:0", "gt:witness"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := labelSetsEqual(tt.current, tt.next); got != tt.want {
				t.Errorf("labelSetsEqual(%v, %v) = %v, want %v",
					tt.current, tt.next, got, tt.want)
			}
		})
	}
}

// agentStateModifyRun is the observable result of one modifyAgentState call
// against a stateful fake bd: every bd argv line it saw, and the labels left
// on the agent bead afterwards.
type agentStateModifyRun struct {
	log         string
	finalLabels []string
}

// updateCalls returns the bd update invocations from the run's log.
func (r agentStateModifyRun) updateCalls() []string {
	var updates []string
	for _, line := range strings.Split(r.log, "\n") {
		if strings.HasPrefix(line, "update ") {
			updates = append(updates, line)
		}
	}
	return updates
}

// runAgentStateModify drives modifyAgentState against a throwaway town root
// and a stateful fake bd, so nothing reaches the production Dolt server.
//
// The stub is stateful — update --set-labels replaces the label set the next
// show returns — because modifyAgentState is a read-modify-write and a
// fixed-response stub could not show whether the write landed.
func runAgentStateModify(t *testing.T, labels []string, setOps []string) agentStateModifyRun {
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

	oldSet, oldIncr, oldDel := agentStateSet, agentStateIncr, agentStateDel
	t.Cleanup(func() {
		agentStateSet, agentStateIncr, agentStateDel = oldSet, oldIncr, oldDel
	})
	agentStateSet, agentStateIncr, agentStateDel = setOps, "", nil

	if err := modifyAgentState("gt-test-witness", beadsDir, false); err != nil {
		t.Fatalf("modifyAgentState() error = %v", err)
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
	for _, l := range strings.Split(strings.TrimSpace(string(stateData)), "\n") {
		if l != "" {
			final = append(final, l)
		}
	}
	return agentStateModifyRun{log: strings.TrimSpace(string(logData)), finalLabels: final}
}

// TestAgentStateSkipsNoOpWrite is the gt-i7g9 guard. Every bd update is a Dolt
// commit that lives forever, and the busiest caller of this path is the "reset
// idle" step in the witness, refinery and deacon patrol formulas — which runs
// on every signal wake against a counter await-signal already reset in-process
// (gt-609). Writing idle:0 over idle:0 buys nothing and costs a commit per
// wake, per patrol agent, forever.
func TestAgentStateSkipsNoOpWrite(t *testing.T) {
	run := runAgentStateModify(t, []string{"gt:agent", "idle:0"}, []string{"idle=0"})

	if got := run.updateCalls(); len(got) != 0 {
		t.Errorf("setting idle=0 on a bead already at idle:0 issued %d bd update(s), want 0: %v",
			len(got), got)
	}
	// The bead must still hold what it held — skipping the write must not be
	// implemented by skipping the state.
	if !labelSetsEqual(run.finalLabels, []string{"gt:agent", "idle:0"}) {
		t.Errorf("labels after no-op = %v, want [gt:agent idle:0]", run.finalLabels)
	}
}

// TestAgentStateWritesRealChange is the positive control for the test above: a
// zero update count is only evidence of the skip if the same harness records a
// write when the value genuinely changes.
func TestAgentStateWritesRealChange(t *testing.T) {
	run := runAgentStateModify(t, []string{"gt:agent", "idle:3"}, []string{"idle=0"})

	if got := run.updateCalls(); len(got) != 1 {
		t.Fatalf("setting idle=0 on a bead at idle:3 issued %d bd update(s), want 1: %v\nlog:\n%s",
			len(got), got, run.log)
	}
	if !labelSetsEqual(run.finalLabels, []string{"gt:agent", "idle:0"}) {
		t.Errorf("labels after reset = %v, want [gt:agent idle:0]", run.finalLabels)
	}
}
