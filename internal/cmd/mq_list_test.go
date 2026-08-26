package cmd

import "testing"

func TestBuildMQListColumns_IncludesTarget(t *testing.T) {
	tests := []struct {
		name          string
		verify        bool
		mergeCheck    bool
		wantColumnSeq []string
	}{
		{
			name:   "without verify",
			verify: false,
			wantColumnSeq: []string{
				"ID", "SCORE", "PRI", "CONVOY", "BRANCH", "TARGET", "STATUS", "AGE",
			},
		},
		{
			name:   "with verify",
			verify: true,
			wantColumnSeq: []string{
				"ID", "SCORE", "PRI", "CONVOY", "BRANCH", "TARGET", "STATUS", "GIT", "AGE",
			},
		},
		{
			name:       "with merge check",
			verify:     true,
			mergeCheck: true,
			wantColumnSeq: []string{
				"ID", "SCORE", "PRI", "CONVOY", "BRANCH", "TARGET", "STATUS", "GIT", "MERGE", "AGE",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cols := buildMQListColumns(tt.verify, tt.mergeCheck)
			if len(cols) != len(tt.wantColumnSeq) {
				t.Fatalf("len(columns) = %d, want %d", len(cols), len(tt.wantColumnSeq))
			}
			for i, want := range tt.wantColumnSeq {
				if cols[i].Name != want {
					t.Fatalf("column[%d] = %q, want %q", i, cols[i].Name, want)
				}
			}
		})
	}
}

// TestDescribeMQListScope names what the listing actually queried. The default
// must say so explicitly: an operator who did not choose the scope is the one
// most likely to read its empty result as "no MRs exist" (gt-kb63).
func TestDescribeMQListScope(t *testing.T) {
	tests := []struct {
		name   string
		ready  bool
		status string
		want   string
	}{
		{name: "default is open and says so", want: "status=open (default — closed MRs not shown)"},
		{name: "explicit open", status: "open", want: "status=open"},
		{name: "closed", status: "closed", want: "status=closed"},
		{name: "all", status: "all", want: "status=all"},
		{name: "all is case-insensitive", status: "ALL", want: "status=all"},
		{name: "ready overrides status", ready: true, status: "closed", want: "ready (open, unblocked)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeMQListScope(tt.ready, tt.status); got != tt.want {
				t.Errorf("describeMQListScope(%v, %q) = %q, want %q", tt.ready, tt.status, got, tt.want)
			}
		})
	}
}

// TestMQListExcludesClosed decides when an empty result deserves the "merged
// MRs are closed" hint. Merged and rejected MRs are closed by definition, so
// every scope that hides closed can return zero for MRs that exist.
func TestMQListExcludesClosed(t *testing.T) {
	tests := []struct {
		name   string
		ready  bool
		status string
		want   bool
	}{
		{name: "default hides closed", want: true},
		{name: "ready hides closed", ready: true, want: true},
		{name: "open hides closed", status: "open", want: true},
		{name: "in_progress hides closed", status: "in_progress", want: true},
		{name: "closed shows them", status: "closed", want: false},
		{name: "all shows them", status: "all", want: false},
		{name: "case-insensitive", status: "Closed", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mqListExcludesClosed(tt.ready, tt.status); got != tt.want {
				t.Errorf("mqListExcludesClosed(%v, %q) = %v, want %v", tt.ready, tt.status, got, tt.want)
			}
		})
	}
}
