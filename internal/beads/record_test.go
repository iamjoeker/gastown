package beads

import (
	"strings"
	"testing"
)

func TestRecordIssueLabel(t *testing.T) {
	tests := []struct {
		label string
		want  bool
	}{
		{"gt:record", true},
		{"gt:ledger", true},
		{"gt:incident", true},
		{"GT:Record", true},    // case-insensitive
		{"  gt:record ", true}, // whitespace-tolerant
		{"gt:keep", false},
		{"gt:merge-request", false},
		{"gt:message", false},
		{"record", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := RecordIssueLabel(tt.label); got != tt.want {
			t.Errorf("RecordIssueLabel(%q) = %v, want %v", tt.label, got, tt.want)
		}
	}
}

// Record labels must stay OUT of the internal/protected vocabularies. A record
// is an ordinary durable bead: treating it as Gas Town runtime state would put
// it back in reach of the very GC paths it was filed to escape (gt-f8td).
func TestRecordLabelIsNotInternalOrProtected(t *testing.T) {
	for _, label := range []string{RecordLabel, LedgerLabel, IncidentLabel} {
		if InternalIssueLabel(label) {
			t.Errorf("InternalIssueLabel(%q) = true; records are not runtime state", label)
		}
		if ProtectedIssueLabel(label) {
			t.Errorf("ProtectedIssueLabel(%q) = true; records need dispatch protection, not close protection", label)
		}
	}
}

func TestIsRecordBead(t *testing.T) {
	tests := []struct {
		name  string
		issue *Issue
		want  bool
	}{
		{"nil", nil, false},
		{"no labels", &Issue{ID: "gt-1"}, false},
		{"unrelated labels", &Issue{ID: "gt-2", Labels: []string{"gt:keep", "p1"}}, false},
		{"record", &Issue{ID: "gt-3", Labels: []string{"gt:record"}}, true},
		{"ledger among others", &Issue{ID: "gt-4", Labels: []string{"p2", "gt:ledger"}}, true},
	}
	for _, tt := range tests {
		if got := IsRecordBead(tt.issue); got != tt.want {
			t.Errorf("%s: IsRecordBead() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestRecordLabelOn(t *testing.T) {
	if got := RecordLabelOn([]string{"p1", "gt:keep"}); got != "" {
		t.Errorf("RecordLabelOn(non-record) = %q, want empty", got)
	}
	if got := RecordLabelOn([]string{"p1", "GT:Incident"}); got != "gt:incident" {
		t.Errorf("RecordLabelOn = %q, want gt:incident (normalized)", got)
	}
}

func TestRecordDispatchRefusal(t *testing.T) {
	if got := RecordDispatchRefusal("gt-abc", []string{"gt:keep"}); got != "" {
		t.Errorf("RecordDispatchRefusal(non-record) = %q, want empty", got)
	}

	msg := RecordDispatchRefusal("gt-8uc", []string{"gt:ledger"})
	if msg == "" {
		t.Fatal("RecordDispatchRefusal(record) returned empty")
	}
	// The message has to name the bead, the label, and the exact escape hatch:
	// an operator who disagrees must not have to guess how to proceed.
	for _, want := range []string{"gt-8uc", "gt:ledger", "bd label remove gt-8uc gt:ledger", "--force"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal message missing %q:\n%s", want, msg)
		}
	}
}
