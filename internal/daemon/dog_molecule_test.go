package daemon

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseWispID(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantID string
	}{
		{
			name:   "standard wisp output",
			input:  "✓ Spawned wisp: gt-wisp-abc123 — Reap stale wisps",
			wantID: "gt-wisp-abc123",
		},
		{
			name:   "wisp ID with ANSI codes",
			input:  "\033[32m✓\033[0m Spawned wisp: \033[1mgt-wisp-xyz789\033[0m — Title",
			wantID: "gt-wisp-xyz789",
		},
		{
			name:   "empty output",
			input:  "",
			wantID: "",
		},
		{
			name:   "no wisp ID in output",
			input:  "Error: something went wrong",
			wantID: "",
		},
		{
			name:   "wisp ID at end of line",
			input:  "Created gt-wisp-def456",
			wantID: "gt-wisp-def456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWispID(tt.input)
			if got != tt.wantID {
				t.Errorf("parseWispID(%q) = %q, want %q", tt.input, got, tt.wantID)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no ANSI", "hello", "hello"},
		{"color code", "\033[32mgreen\033[0m", "green"},
		{"bold", "\033[1mbold\033[0m", "bold"},
		{"multiple codes", "\033[32m✓\033[0m \033[1mtext\033[0m", "✓ text"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(tt.input)
			if got != tt.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseChildrenJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantIDs []string
		wantErr bool
	}{
		{
			name:    "bare array",
			input:   `[{"id":"a","title":"Probe","status":"open"}]`,
			wantIDs: []string{"a"},
		},
		{
			name:    "map wrapper from bd show",
			input:   `{"hq-wisp-root":[{"id":"hq-wisp-a","title":"Probe","status":"open"},{"id":"hq-wisp-b","title":"Report","status":"open"}]}`,
			wantIDs: []string{"hq-wisp-a", "hq-wisp-b"},
		},
		{
			name:    "empty map wrapper",
			input:   `{"hq-wisp-root":[]}`,
			wantIDs: []string{},
		},
		{
			name:    "schema metadata with children",
			input:   `{"hq-wisp-root":[{"id":"hq-wisp-a","title":"Probe","status":"open"}],"schema_version":1}`,
			wantIDs: []string{"hq-wisp-a"},
		},
		{
			name:    "schema metadata with empty children",
			input:   `{"hq-wisp-root":[],"schema_version":1}`,
			wantIDs: []string{},
		},
		{
			name:    "multiple child arrays are deterministic",
			input:   `{"hq-wisp-b":[{"id":"b-step","title":"Report","status":"open"}],"schema_version":1,"hq-wisp-a":[{"id":"a-step","title":"Probe","status":"open"}]}`,
			wantIDs: []string{"a-step", "b-step"},
		},
		{
			name:    "schema key is metadata even if array-valued",
			input:   `{"schema_version":[{"id":"metadata","title":"Ignore","status":"open"}],"hq-wisp-root":[{"id":"hq-wisp-a","title":"Probe","status":"open"}]}`,
			wantIDs: []string{"hq-wisp-a"},
		},
		{
			name:    "empty array",
			input:   `[]`,
			wantIDs: []string{},
		},
		{
			name:    "empty input",
			input:   `   `,
			wantErr: true,
		},
		{
			name:    "malformed bare array",
			input:   `[`,
			wantErr: true,
		},
		{
			name:    "malformed object envelope",
			input:   `{"hq-wisp-root":[`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			input:   `not json`,
			wantErr: true,
		},
		{
			name:    "malformed child array",
			input:   `{"hq-wisp-root":[{"id":1}],"schema_version":1}`,
			wantErr: true,
		},
		{
			name:    "non-array child payload",
			input:   `{"hq-wisp-root":1,"schema_version":1}`,
			wantErr: true,
		},
		{
			name:    "metadata only is not silent skip-all",
			input:   `{"schema_version":1}`,
			wantErr: true,
		},
		{
			name:    "empty object is not silent skip-all",
			input:   `{}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChildrenJSON(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			gotIDs := make([]string, 0, len(got))
			for _, child := range got {
				gotIDs = append(gotIDs, child.ID)
			}
			if !reflect.DeepEqual(gotIDs, tt.wantIDs) {
				t.Errorf("got child IDs %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}
}

// captureLogger records formatted log lines for assertions.
type captureLogger struct{ lines []string }

func (l *captureLogger) Printf(format string, args ...interface{}) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *captureLogger) contains(substr string) bool {
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// fakeBd models the part of bd that closeRemainingSteps depends on: a root wisp
// with child steps chained by `blocks` edges, where `bd close` refuses to close
// a child whose blocker is still open, and refuses to close the root while any
// child is open. childOrder is the order `bd show --children --json` returns —
// the variable this bug is sensitive to.
type fakeBd struct {
	root       string
	childOrder []string          // returned in this order, independent of the chain
	blockedBy  map[string]string // child -> the issue that must close first
	closed     map[string]bool

	showCalls  int
	closeCalls map[string]int
}

func newFakeBd(root string, childOrder []string, blockedBy map[string]string) *fakeBd {
	return &fakeBd{
		root:       root,
		childOrder: childOrder,
		blockedBy:  blockedBy,
		closed:     make(map[string]bool),
		closeCalls: make(map[string]int),
	}
}

func (f *fakeBd) run(args ...string) (string, error) {
	switch {
	case len(args) >= 2 && args[0] == "show":
		f.showCalls++
		children := make([]childInfo, 0, len(f.childOrder))
		for _, id := range f.childOrder {
			status := "open"
			if f.closed[id] {
				status = "closed"
			}
			children = append(children, childInfo{ID: id, Title: id, Status: status})
		}
		out, err := json.Marshal(map[string]interface{}{
			f.root:           children,
			"schema_version": 1,
		})
		return string(out), err

	case len(args) >= 2 && args[0] == "close":
		id := args[1]
		f.closeCalls[id]++
		if id == f.root {
			open := 0
			for _, child := range f.childOrder {
				if !f.closed[child] {
					open++
				}
			}
			if open > 0 {
				return "", fmt.Errorf("close root failed: %d open child issue(s)", open)
			}
			f.closed[id] = true
			return "", nil
		}
		if blocker, ok := f.blockedBy[id]; ok && !f.closed[blocker] {
			return "", fmt.Errorf("cannot close blocked issue: %s is blocked by [%s]", id, blocker)
		}
		f.closed[id] = true
		return "", nil
	}
	return "", fmt.Errorf("fakeBd: unexpected args %v", args)
}

func (f *fakeBd) openChildren() []string {
	var open []string
	for _, id := range f.childOrder {
		if !f.closed[id] {
			open = append(open, id)
		}
	}
	return open
}

// chain returns the blockedBy map for steps[0] → steps[1] → ... (each step
// blocked by its predecessor), matching how molecule steps are wired.
func chain(steps ...string) map[string]string {
	blocked := make(map[string]string, len(steps))
	for i := 1; i < len(steps); i++ {
		blocked[steps[i]] = steps[i-1]
	}
	return blocked
}

func newTestDogMol(f *fakeBd, log *captureLogger) *dogMol {
	return &dogMol{
		rootID:  f.root,
		stepIDs: make(map[string]string),
		logger:  log,
		runBdFn: f.run,
	}
}

// A single pass over a blocks chain closes only the steps that happen to follow
// their blocker — this is the defect (gt-g1q1), kept as the control for the
// looping test below. In reverse order that is exactly one step per invocation.
func TestCloseOnePassStrandsReverseOrderedChain(t *testing.T) {
	steps := []string{"w-1", "w-2", "w-3", "w-4"}
	f := newFakeBd("hq-wisp-root", []string{"w-4", "w-3", "w-2", "w-1"}, chain(steps...))
	dm := newTestDogMol(f, &captureLogger{})

	closed, stillOpen, err := dm.closeOnePass()
	if err != nil {
		t.Fatalf("closeOnePass: %v", err)
	}
	if closed != 1 {
		t.Errorf("single pass closed %d steps, want 1 (reverse order strands the rest)", closed)
	}
	if len(stillOpen) != 3 {
		t.Errorf("single pass left %v open, want 3 steps", stillOpen)
	}
}

func TestCloseRemainingStepsDrainsChainInAnyOrder(t *testing.T) {
	steps := []string{"w-1", "w-2", "w-3", "w-4"}
	orders := map[string][]string{
		"reverse dependency order": {"w-4", "w-3", "w-2", "w-1"},
		"dependency order":         {"w-1", "w-2", "w-3", "w-4"},
		"interleaved order":        {"w-3", "w-1", "w-4", "w-2"},
	}

	for name, order := range orders {
		t.Run(name, func(t *testing.T) {
			f := newFakeBd("hq-wisp-root", order, chain(steps...))
			log := &captureLogger{}
			dm := newTestDogMol(f, log)

			dm.close()

			if open := f.openChildren(); len(open) != 0 {
				t.Errorf("steps still open after close(): %v", open)
			}
			if !f.closed[f.root] {
				t.Errorf("root %s not closed; log: %v", f.root, log.lines)
			}
			if log.contains("still open") {
				t.Errorf("unexpected stuck-steps warning: %v", log.lines)
			}
			if !log.contains("closed 4 orphan step wisp(s)") {
				t.Errorf("want a single tally of 4 closed steps, got: %v", log.lines)
			}
		})
	}
}

// Blocked closes are the dependency guard, not a transient error: retrying the
// same close cannot help, so the loop must not burn dogCloseMaxAttempts (and
// the retry sleeps) on each one.
func TestCloseWispDoesNotRetryBlockedClose(t *testing.T) {
	f := newFakeBd("hq-wisp-root", []string{"w-1", "w-2"}, chain("w-1", "w-2"))
	dm := newTestDogMol(f, &captureLogger{})

	if err := dm.closeWisp("w-2"); err == nil {
		t.Fatal("closing a blocked step should fail")
	}
	if got := f.closeCalls["w-2"]; got != 1 {
		t.Errorf("blocked close attempted %d times, want 1", got)
	}
}

// A step blocked by something outside the molecule can never close. The loop
// must terminate rather than spin, and must say what is still open.
func TestCloseRemainingStepsTerminatesOnPermanentlyBlockedStep(t *testing.T) {
	f := newFakeBd("hq-wisp-root", []string{"w-2", "w-1"}, map[string]string{
		"w-2": "w-1",
		"w-1": "hq-external", // never closed
	})
	log := &captureLogger{}
	dm := newTestDogMol(f, log)

	dm.close()

	if got := f.openChildren(); len(got) != 2 {
		t.Errorf("open children = %v, want both still open", got)
	}
	if f.closed[f.root] {
		t.Error("root closed despite open children — the open-children guard must hold")
	}
	if f.showCalls > 2 {
		t.Errorf("made %d children queries, want the loop to stop once a pass makes no progress", f.showCalls)
	}
	if !log.contains("2 step wisp(s) under hq-wisp-root still open") {
		t.Errorf("want the stuck steps reported, got: %v", log.lines)
	}
}

func TestCloseRemainingStepsNoOpWhenAllClosed(t *testing.T) {
	f := newFakeBd("hq-wisp-root", []string{"w-1"}, nil)
	f.closed["w-1"] = true
	log := &captureLogger{}
	dm := newTestDogMol(f, log)

	dm.closeRemainingSteps()

	if f.showCalls != 1 {
		t.Errorf("showCalls = %d, want 1", f.showCalls)
	}
	if len(log.lines) != 0 {
		t.Errorf("want silence when there is nothing to close, got: %v", log.lines)
	}
}

func TestDogMolGracefulDegradation(t *testing.T) {
	// A dogMol with empty rootID should be a no-op for all operations.
	dm := &dogMol{
		rootID:  "",
		stepIDs: make(map[string]string),
	}

	// These should not panic or error — graceful degradation.
	dm.closeStep("scan")
	dm.failStep("scan", "test failure")
	dm.close()
}
