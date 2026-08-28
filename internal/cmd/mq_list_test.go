package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/style"
)

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
			cols := buildMQListColumns(tt.verify, tt.mergeCheck, mqListWidthsFor(nil))
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

// TestMQListWidthsFor_IdentifiersAreNeverCut is the regression test for gt-2izk.
//
// The defect cut every id to exactly 12 characters — the same number as the
// column width, which is one short of the table's `len > Width` ellipsis rule.
// The cut therefore happened and nothing marked it: `gt mq list gastown
// --status closed` rendered 176 rows whose ids were all the same length, and a
// query against one of them ("gt-wisp-2hfh", really gt-wisp-2hfhc) returned
// zero rows — the same answer as "the row does not exist".
//
// The column must grow to fit the widest identifier, never the reverse.
func TestMQListWidthsFor_IdentifiersAreNeverCut(t *testing.T) {
	tests := []struct {
		name       string
		rows       []mqListRow
		wantID     int
		wantConvoy int
	}{
		{
			name:       "empty listing keeps the floor",
			wantID:     mqListMinIDWidth,
			wantConvoy: mqListMinConvoyWidth,
		},
		{
			name:       "short ids keep the floor",
			rows:       []mqListRow{{id: "gt-abc", convoy: "cv-1"}},
			wantID:     mqListMinIDWidth,
			wantConvoy: mqListMinConvoyWidth,
		},
		{
			// The exact id the old code cut to "gt-wisp-2hfh".
			name:       "13-char id widens the column past the old fixed 12",
			rows:       []mqListRow{{id: "gt-wisp-2hfhc"}},
			wantID:     13,
			wantConvoy: mqListMinConvoyWidth,
		},
		{
			name: "widest row wins",
			rows: []mqListRow{
				{id: "gt-wisp-2hfhc", convoy: "convoy-short"},
				{id: "gt-wisp-longest-of-them-all", convoy: "convoy-considerably-longer"},
				{id: "gt-abc", convoy: "cv-1"},
			},
			wantID:     len("gt-wisp-longest-of-them-all"),
			wantConvoy: len("convoy-considerably-longer"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mqListWidthsFor(tt.rows)
			if got.id != tt.wantID {
				t.Errorf("id width = %d, want %d", got.id, tt.wantID)
			}
			if got.convoy != tt.wantConvoy {
				t.Errorf("convoy width = %d, want %d", got.convoy, tt.wantConvoy)
			}
			// The property that matters, stated directly: every identifier fits.
			for _, r := range tt.rows {
				if len(r.id) > got.id {
					t.Errorf("id %q (%d) does not fit width %d", r.id, len(r.id), got.id)
				}
				if len(r.convoy) > got.convoy {
					t.Errorf("convoy %q (%d) does not fit width %d", r.convoy, len(r.convoy), got.convoy)
				}
			}
		})
	}
}

// TestBuildMQListColumns_IDColumnFitsTheData ties the sized widths to the
// columns actually handed to the renderer. Sizing them correctly and then not
// using them would reproduce the defect exactly.
func TestBuildMQListColumns_IDColumnFitsTheData(t *testing.T) {
	rows := []mqListRow{{id: "gt-wisp-2hfhc", convoy: "convoy-considerably-longer"}}
	cols := buildMQListColumns(false, false, mqListWidthsFor(rows))

	byName := map[string]int{}
	for _, c := range cols {
		byName[c.Name] = c.Width
	}
	if got := byName["ID"]; got < len(rows[0].id) {
		t.Errorf("ID column width = %d, too narrow for %q (%d)", got, rows[0].id, len(rows[0].id))
	}
	if got := byName["CONVOY"]; got < len(rows[0].convoy) {
		t.Errorf("CONVOY column width = %d, too narrow for %q (%d)", got, rows[0].convoy, len(rows[0].convoy))
	}
}

// TestMQListTruncatedColumns reports the columns that really were cut, so the
// listing can disclose it. BRANCH truncation is what turned a Mayor's
// `gt mq list ... | grep wkcz` into "no prior MR, safe to submit" while
// gt-wisp-zhy8 was in the list the whole time (gt-2izk).
func TestMQListTruncatedColumns(t *testing.T) {
	long := strings.Repeat("x", mqListBranchWidth+1)

	tests := []struct {
		name string
		rows []mqListRow
		want []string
	}{
		{name: "nothing cut", rows: []mqListRow{{branch: "main", target: "main"}}},
		{
			name: "exactly at the width is not cut",
			rows: []mqListRow{{branch: strings.Repeat("x", mqListBranchWidth)}},
		},
		{
			name: "long branch",
			rows: []mqListRow{{branch: "main"}, {branch: long}},
			want: []string{"BRANCH"},
		},
		{
			name: "long target",
			rows: []mqListRow{{target: long}},
			want: []string{"TARGET"},
		},
		{
			name: "both, reported once each",
			rows: []mqListRow{{branch: long, target: long}, {branch: long, target: long}},
			want: []string{"BRANCH", "TARGET"},
		},
		{
			// Identifier columns grow to fit, so they can never be reported cut.
			name: "a very long id is never reported as truncated",
			rows: []mqListRow{{id: strings.Repeat("i", 80), convoy: strings.Repeat("c", 80)}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mqListTruncatedColumns(tt.rows)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestMQListTable_RendersEveryIDVerbatim is the end-to-end statement of the
// gt-2izk contract: an id read off this table can be pasted back into a query.
//
// It composes the pieces the render path composes — sized widths, columns, rows
// — and asserts the whole identifier survives. Under the old fixed width of 12
// the 13-character id came out as "gt-wisp-2hfh", which is a well-formed id that
// matches no row.
func TestMQListTable_RendersEveryIDVerbatim(t *testing.T) {
	rows := []mqListRow{
		{id: "gt-wisp-2hfhc", convoy: "convoy-considerably-longer"},
		{id: "gt-wisp-zhy8", convoy: ""},
		{id: "gt-wisp-a-notably-longer-identifier", convoy: "cv-2"},
	}

	table := style.NewTable(buildMQListColumns(false, false, mqListWidthsFor(rows))...)
	for _, r := range rows {
		table.AddRow(r.id, "1.0", "P2", r.convoy, "main", "main", "ready", "1h")
	}
	out := table.Render()

	for _, r := range rows {
		if !strings.Contains(out, r.id) {
			t.Errorf("rendered table does not contain id %q verbatim:\n%s", r.id, out)
		}
		if r.convoy != "" && !strings.Contains(out, r.convoy) {
			t.Errorf("rendered table does not contain convoy %q verbatim:\n%s", r.convoy, out)
		}
	}
	// No identifier may be shortened, so no "..." may appear in this table.
	if strings.Contains(out, "...") {
		t.Errorf("identifier columns were truncated:\n%s", out)
	}
}
