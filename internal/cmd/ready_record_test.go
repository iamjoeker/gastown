package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// `bd ready --json` omits the labels field entirely (measured 2026-08-19), so
// the ready surface cannot filter records on issue.Labels — it needs the ID set
// from a separate labelled query. This test pins that: a record row that
// arrives label-free must still be filtered out.
func TestFilterRecordBeads_LabelFreeRowsNeedTheIDSet(t *testing.T) {
	issues := []*beads.Issue{
		{ID: "gt-8uc", Title: "Refinery merge ledger"},
		{ID: "gt-real", Title: "Actual work"},
	}

	// Control: with no ID set, the label-free record survives. If this ever
	// starts passing, bd ready gained labels and the extra query can go.
	if got := filterRecordBeads(issues, nil); len(got) != 2 {
		t.Fatalf("control: expected label-free record to survive an empty ID set, got %d rows", len(got))
	}

	recordIDs := map[string]bool{"gt-8uc": true}
	got := filterRecordBeads(issues, recordIDs)
	if len(got) != 1 || got[0].ID != "gt-real" {
		t.Fatalf("expected only gt-real to survive, got %+v", got)
	}
}

func TestFilterRecordBeads_LabelsFilterToo(t *testing.T) {
	issues := []*beads.Issue{
		{ID: "gt-inc", Labels: []string{"gt:incident"}},
		{ID: "gt-led", Labels: []string{"p2", "gt:ledger"}},
		{ID: "gt-work", Labels: []string{"gt:keep"}},
	}
	got := filterRecordBeads(issues, nil)
	if len(got) != 1 || got[0].ID != "gt-work" {
		t.Fatalf("expected only gt-work to survive, got %+v", got)
	}
}

func TestParseReadyBeadIDs_BothShapes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"bare array", `[{"id":"gt-1"},{"id":"gt-2"}]`, []string{"gt-1", "gt-2"}},
		{"issues envelope", `{"issues":[{"id":"gt-3"}],"count":1}`, []string{"gt-3"}},
		{"empty array", `[]`, nil},
		{"empty output", ``, nil},
		{"malformed", `not json`, nil},
		{"row without id", `[{"title":"x"}]`, nil},
	}
	for _, tt := range tests {
		got := parseReadyBeadIDs([]byte(tt.in))
		if len(got) != len(tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
				break
			}
		}
	}
}
