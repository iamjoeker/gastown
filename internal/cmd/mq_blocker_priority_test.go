package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func showFrom(issues map[string]*beads.Issue) func(string) (*beads.Issue, error) {
	return func(id string) (*beads.Issue, error) {
		issue, ok := issues[id]
		if !ok {
			return nil, beads.ErrNotFound
		}
		return issue, nil
	}
}

func TestBlockerPriorityForSingleBlockedItem(t *testing.T) {
	tests := []struct {
		name         string
		mrPriority   int
		wantPriority int
	}{
		{"P0 MR", 0, 0},
		{"P1 MR", 1, 0},
		{"P2 MR", 2, 1},
		{"P4 MR", 4, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			show := showFrom(map[string]*beads.Issue{
				"gt-wisp-faoi": {ID: "gt-wisp-faoi", Title: "MR", Priority: tc.mrPriority},
			})
			out, err := blockerPriorityFor(show, []string{"gt-wisp-faoi"})
			if err != nil {
				t.Fatalf("blockerPriorityFor: %v", err)
			}
			if out.Priority != tc.wantPriority {
				t.Fatalf("priority = %d, want %d", out.Priority, tc.wantPriority)
			}
			if out.MostUrgent != tc.mrPriority {
				t.Errorf("most_urgent = %d, want %d", out.MostUrgent, tc.mrPriority)
			}
			if out.Priority > tc.mrPriority {
				t.Errorf("blocker P%d ranks below the P%d item it blocks", out.Priority, tc.mrPriority)
			}
		})
	}
}

// TestBlockerPriorityDerivesFromTheMostUrgentBlockedItem covers the wording of
// the rule: a task gating several items must outrank the most urgent of them,
// not the first one listed or the last one read.
func TestBlockerPriorityDerivesFromTheMostUrgentBlockedItem(t *testing.T) {
	show := showFrom(map[string]*beads.Issue{
		"gt-a": {ID: "gt-a", Priority: 3},
		"gt-b": {ID: "gt-b", Priority: 1},
		"gt-c": {ID: "gt-c", Priority: 2},
	})
	// Order the args so neither the first nor the last is the most urgent — a
	// take-the-first or take-the-last bug would otherwise pass.
	out, err := blockerPriorityFor(show, []string{"gt-a", "gt-b", "gt-c"})
	if err != nil {
		t.Fatalf("blockerPriorityFor: %v", err)
	}
	if out.MostUrgent != 1 {
		t.Fatalf("most_urgent = %d, want 1", out.MostUrgent)
	}
	if out.Priority != 0 {
		t.Fatalf("priority = %d, want 0 (must outrank the P1)", out.Priority)
	}
	for _, item := range out.Blocked {
		if out.Priority > item.Priority {
			t.Errorf("blocker P%d ranks below blocked item %s at P%d", out.Priority, item.ID, item.Priority)
		}
	}
}

// TestBlockerPriorityFailsOnUnreadableBlockedItem: skipping a bead it could not
// read would let the command hand back a number derived from a subset, which is
// the same defect it exists to prevent, wearing a helper's clothes.
func TestBlockerPriorityFailsOnUnreadableBlockedItem(t *testing.T) {
	show := showFrom(map[string]*beads.Issue{
		"gt-a": {ID: "gt-a", Priority: 3},
	})
	_, err := blockerPriorityFor(show, []string{"gt-a", "gt-missing"})
	if err == nil {
		t.Fatal("expected an error when a blocked item cannot be read")
	}
	if !strings.Contains(err.Error(), "gt-missing") {
		t.Errorf("error should name the unreadable bead, got: %v", err)
	}
}

func TestBlockerPriorityFailsOnEmptyID(t *testing.T) {
	show := func(string) (*beads.Issue, error) { return nil, errors.New("should not be called") }
	if _, err := blockerPriorityFor(show, []string{"  "}); err == nil {
		t.Fatal("expected an error for an empty blocked-item ID")
	}
}

// TestBlockerPriorityPlainOutputIsSubstitutable: the whole point of the plain
// form is $(gt mq blocker-priority ...) inside a --priority flag, so the line
// must be a bare integer with nothing else on it.
func TestBlockerPriorityPlainOutputIsSubstitutable(t *testing.T) {
	var buf bytes.Buffer
	out := &BlockerPriorityOutput{Priority: 0, MostUrgent: 1}
	if err := writeBlockerPriority(&buf, out, false); err != nil {
		t.Fatalf("writeBlockerPriority: %v", err)
	}
	if got := buf.String(); got != "0\n" {
		t.Fatalf("plain output = %q, want %q", got, "0\n")
	}
}

func TestBlockerPriorityJSONOutput(t *testing.T) {
	var buf bytes.Buffer
	out := &BlockerPriorityOutput{
		Priority:   0,
		MostUrgent: 1,
		Blocked:    []BlockedItem{{ID: "gt-wisp-faoi", Priority: 1, Title: "MR"}},
	}
	if err := writeBlockerPriority(&buf, out, true); err != nil {
		t.Fatalf("writeBlockerPriority: %v", err)
	}
	var decoded BlockerPriorityOutput
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decoding JSON output: %v", err)
	}
	if decoded.Priority != 0 || decoded.MostUrgent != 1 || len(decoded.Blocked) != 1 {
		t.Fatalf("round-trip mismatch: %+v", decoded)
	}
}
