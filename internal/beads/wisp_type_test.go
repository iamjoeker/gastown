package beads

import (
	"strings"
	"testing"
)

func TestIsValidWispType(t *testing.T) {
	// The vocabulary is bd's, not ours: bd rejects the create outright for a
	// value outside it. Pin the whole set so a local edit that adds a
	// convenient-sounding type (there is no "merge_request") fails here rather
	// than at runtime in a patrol cycle.
	valid := []string{
		"", "heartbeat", "ping", "patrol", "gc_report", "recovery", "error", "escalation",
	}
	for _, v := range valid {
		if !IsValidWispType(v) {
			t.Errorf("IsValidWispType(%q) = false, want true", v)
		}
	}

	invalid := []string{
		"merge_request", "merge-request", "sling_context", "work", "swarm",
		"Patrol", "GC_REPORT", "gc report", "default", "unknown",
	}
	for _, v := range invalid {
		if IsValidWispType(v) {
			t.Errorf("IsValidWispType(%q) = true, want false", v)
		}
	}
}

func TestWispTypeUpdateSQL(t *testing.T) {
	stmt, err := WispTypeUpdateSQL(WispTypePatrol, []string{"gt-wisp-abc", "gt-wisp-def"})
	if err != nil {
		t.Fatalf("WispTypeUpdateSQL: %v", err)
	}

	// The table matters more than the exact text: an UPDATE that reached
	// "issues" would stamp a permanent bead with a TTL classification.
	if !strings.Contains(stmt, "UPDATE wisps SET") {
		t.Errorf("statement does not target the wisps table: %s", stmt)
	}
	if strings.Contains(stmt, "issues") {
		t.Errorf("statement mentions the issues table: %s", stmt)
	}
	if !strings.Contains(stmt, "wisp_type = 'patrol'") {
		t.Errorf("statement does not set the type: %s", stmt)
	}
	for _, id := range []string{"'gt-wisp-abc'", "'gt-wisp-def'"} {
		if !strings.Contains(stmt, id) {
			t.Errorf("statement is missing %s: %s", id, stmt)
		}
	}
	// No WHERE clause means every wisp in the database gets stamped.
	if !strings.Contains(stmt, "WHERE id IN (") {
		t.Errorf("statement has no id predicate: %s", stmt)
	}
}

func TestWispTypeUpdateSQLRejectsBadInput(t *testing.T) {
	cases := []struct {
		name     string
		wispType string
		ids      []string
	}{
		{"empty type", "", []string{"gt-wisp-abc"}},
		{"unknown type", "merge_request", []string{"gt-wisp-abc"}},
		{"no ids", WispTypePatrol, nil},
		{"empty id", WispTypePatrol, []string{"gt-wisp-abc", ""}},
		// A quote in an ID means the caller parsed the wrong token out of
		// command output. Interpolating it would be an injection into a
		// statement that runs with write access to the wisps table.
		{"quoted id", WispTypePatrol, []string{"gt-wisp-abc' OR '1'='1"}},
		{"backtick id", WispTypePatrol, []string{"gt-wisp-`abc`"}},
		{"backslash id", WispTypePatrol, []string{`gt-wisp-a\'bc`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmt, err := WispTypeUpdateSQL(tc.wispType, tc.ids)
			if err == nil {
				t.Fatalf("expected an error, got statement %q", stmt)
			}
			if stmt != "" {
				t.Errorf("expected no statement alongside the error, got %q", stmt)
			}
		})
	}
}

func TestCreateOptionsValidateWispType(t *testing.T) {
	cases := []struct {
		name    string
		opts    CreateOptions
		wantErr string
	}{
		{"unset is fine", CreateOptions{}, ""},
		{"valid ephemeral", CreateOptions{Ephemeral: true, WispType: WispTypeGCReport}, ""},
		{
			// wisp_type is a wisps-table column. On a durable create the value
			// has nowhere to land, so accepting it would be the same silent
			// no-write gt-fqd5 exists to fix.
			"type without ephemeral",
			CreateOptions{WispType: WispTypePatrol},
			"requires Ephemeral",
		},
		{
			"invalid type",
			CreateOptions{Ephemeral: true, WispType: "merge_request"},
			"invalid wisp type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.opts.validateWispType()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}
