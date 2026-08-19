package doltserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseCSVCount covers what bdSQLCount does with the bytes it is handed.
//
// The regression this locks: bd prints warnings on stderr (a group-readable
// .beads directory, an unconfigured beads.role), bdSQLCSV read CombinedOutput,
// and the parse took whatever sat on the second line. A warning there yielded
// 0 with a nil error, so a populated table looked empty to MigrateWisps —
// including to the row-count guard that decides whether the pre-migration
// backup of wisp_dependencies can be dropped.
func TestParseCSVCount(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    int
		wantErr bool
	}{
		{
			name:   "count row",
			output: "cnt\n7\n",
			want:   7,
		},
		{
			name:   "genuinely empty table",
			output: "cnt\n0\n",
			want:   0,
		},
		{
			name:   "quoted value",
			output: "\"cnt\"\n\"7\"\n",
			want:   7,
		},
		{
			// The shape CombinedOutput produced: a stderr warning ahead of the
			// CSV. This is the case that used to return 0, nil.
			name:    "warning ahead of the csv",
			output:  "Warning: .beads directory is group-readable\ncnt\n7\n",
			wantErr: true,
		},
		{
			// And with the warning printed between header and value.
			name:    "warning inside the csv",
			output:  "cnt\nWarning: beads.role is not configured\n7\n",
			wantErr: true,
		},
		{
			name:    "header only",
			output:  "cnt\n",
			wantErr: true,
		},
		{
			name:    "no output at all",
			output:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCSVCount(tt.output)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseCSVCount(%q) = %d, nil; want an error", tt.output, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCSVCount(%q): %v", tt.output, err)
			}
			if got != tt.want {
				t.Errorf("parseCSVCount(%q) = %d, want %d", tt.output, got, tt.want)
			}
		})
	}
}

// stubBd puts a shell-script `bd` at the front of PATH.
//
// A stub rather than the real binary: the defect is about which of bd's two
// output streams the CSV parser reads, and a stub is the only way to guarantee
// something is written to stderr. Waiting for bd to warn on its own makes the
// test's premise depend on the machine it runs on.
func stubBd(t *testing.T, script string) {
	t.Helper()
	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("writing bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestBdSQLCSV_StderrStaysOutOfCSV is the end-to-end half of the same defect:
// bdSQLCSV read CombinedOutput, so bd's warnings — printed on every invocation
// when .beads is group-readable or beads.role is unset — were spliced into the
// CSV its callers parse.
func TestBdSQLCSV_StderrStaysOutOfCSV(t *testing.T) {
	stubBd(t, `#!/bin/sh
echo "Warning: .beads is group-readable (0755); tighten it to 0700" >&2
echo "Warning: beads.role is not configured" >&2
echo "cnt"
echo "7"
exit 0
`)

	workDir := t.TempDir()
	output, err := bdSQLCSV(workDir, "SELECT COUNT(*) as cnt FROM issues")
	if err != nil {
		t.Fatalf("bdSQLCSV: %v", err)
	}
	if strings.Contains(output, "Warning") {
		t.Errorf("bdSQLCSV returned stderr in the CSV:\n%s", output)
	}

	cnt, err := bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM issues")
	if err != nil {
		t.Fatalf("bdSQLCount: %v", err)
	}
	if cnt != 7 {
		t.Errorf("bdSQLCount = %d, want 7 — a warning displaced the count", cnt)
	}
}

// TestBdSQLCSV_ErrorKeepsStderr guards the other side of the split: dropping
// CombinedOutput must not drop the diagnostic. bd's failures explain
// themselves on stderr, and an error naming only the exit status would send
// the next reader looking in the wrong place.
func TestBdSQLCSV_ErrorKeepsStderr(t *testing.T) {
	stubBd(t, `#!/bin/sh
echo "Error: no beads database found" >&2
exit 1
`)

	_, err := bdSQLCSV(t.TempDir(), "SELECT 1")
	if err == nil {
		t.Fatal("bdSQLCSV succeeded against a failing bd")
	}
	if !strings.Contains(err.Error(), "no beads database found") {
		t.Errorf("bdSQLCSV error lost stderr: %v", err)
	}
}
