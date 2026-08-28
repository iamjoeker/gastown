package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWispPinUpdateSQL(t *testing.T) {
	got, err := WispPinUpdateSQL([]string{"gt-wisp-abc", "gt-wisp-def"})
	if err != nil {
		t.Fatalf("WispPinUpdateSQL: %v", err)
	}
	want := "UPDATE wisps SET pinned = 1 WHERE id IN ('gt-wisp-abc', 'gt-wisp-def')"
	if got != want {
		t.Fatalf("WispPinUpdateSQL =\n  %s\nwant\n  %s", got, want)
	}
	// The issues table holds permanent beads, where a pin would block the close
	// rather than protect a record from retention.
	if strings.Contains(got, "issues") {
		t.Fatalf("pin statement must target the wisps table only: %s", got)
	}
}

func TestWispPinnedCountSQL(t *testing.T) {
	got, err := WispPinnedCountSQL([]string{"gt-wisp-abc"})
	if err != nil {
		t.Fatalf("WispPinnedCountSQL: %v", err)
	}
	want := "SELECT COUNT(*) AS n FROM wisps WHERE COALESCE(pinned, 0) = 1 AND id IN ('gt-wisp-abc')"
	if got != want {
		t.Fatalf("WispPinnedCountSQL =\n  %s\nwant\n  %s", got, want)
	}
}

// TestWispPinSQLRejectsUnusableIDs covers both builders: an ID that could not
// have come from bd must not be interpolated into a statement.
func TestWispPinSQLRejectsUnusableIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{"no IDs", nil},
		{"empty ID", []string{""}},
		{"single quote", []string{"gt-wisp-abc'; DROP TABLE wisps; --"}},
		{"backtick", []string{"gt-wisp-`abc`"}},
		{"one bad ID among good ones", []string{"gt-wisp-abc", "gt-wisp-'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if stmt, err := WispPinUpdateSQL(tc.ids); err == nil {
				t.Fatalf("WispPinUpdateSQL(%q) built %q, want an error", tc.ids, stmt)
			}
			if stmt, err := WispPinnedCountSQL(tc.ids); err == nil {
				t.Fatalf("WispPinnedCountSQL(%q) built %q, want an error", tc.ids, stmt)
			}
		})
	}
}

func TestParsePinnedCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		out     string
		want    int
		wantErr bool
	}{
		{name: "count", out: `[{"n":2}]`, want: 2},
		{name: "real zero", out: `[{"n":0}]`, want: 0},
		{name: "notice before the payload", out: "warning: stale read\n[{\"n\":1}]", want: 1},
		// A COUNT(*) always returns one row, so no rows means the output was not
		// the answer to this query. Reading it as zero would report the pin
		// missing when what actually happened is that nothing was measured.
		{name: "no rows is not a zero", out: `[]`, wantErr: true},
		{name: "no array at all", out: "Error: unknown flag", wantErr: true},
		{name: "empty output", out: "", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePinnedCount([]byte(tc.out))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parsePinnedCount(%q) = %d, want an error", tc.out, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePinnedCount(%q): %v", tc.out, err)
			}
			if got != tc.want {
				t.Fatalf("parsePinnedCount(%q) = %d, want %d", tc.out, got, tc.want)
			}
		})
	}
}

// installFakeSQLBD installs a POSIX-shell `bd` on PATH that logs the database it
// was pointed at alongside its argv, and answers a COUNT query with pinnedCount.
func installFakeSQLBD(t *testing.T, pinnedCount string) (logPath string) {
	t.Helper()

	binDir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "bd.log")
	script := `#!/bin/sh
printf 'argv=%s\n' "$*" >> "$BD_LOG"
printf 'beads_dir=%s\n' "$BEADS_DIR" >> "$BD_LOG"
case "$*" in
  *"COUNT(*)"*) printf '%s\n' '[{"n":` + pinnedCount + `}]' ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	return logPath
}

// TestPinWispsRoutesToRigDatabase is the routing half of gt-31nn.
//
// The MR wisp lives in the rig's database because Create routed it there. A
// wisp UPDATE gets no prefix routing from bd, so a pin issued from a polecat
// worktree must be redirected the same way — otherwise it runs against whatever
// database the worktree resolves to, matches nothing, and exits 0.
func TestPinWispsRoutesToRigDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell fake bd")
	}

	townRoot, rigRoot, polecatDir := newMRRoutingTown(t)
	logPath := installFakeSQLBD(t, "1")

	if err := NewIsolated(polecatDir).PinWisps("gastown", "gt-wisp-abc"); err != nil {
		t.Fatalf("PinWisps: %v", err)
	}

	log := string(mustReadFile(t, logPath))
	if !strings.Contains(log, "UPDATE wisps SET pinned = 1 WHERE id IN ('gt-wisp-abc')") {
		t.Fatalf("no pin statement was issued:\n%s", log)
	}
	rigBeads := filepath.Join(rigRoot, ".beads")
	if !strings.Contains(log, "beads_dir="+rigBeads) {
		t.Fatalf("pin did not reach the rig database %s:\n%s", rigBeads, log)
	}
	if townBeads := filepath.Join(townRoot, ".beads"); strings.Contains(log, "beads_dir="+townBeads) {
		t.Fatalf("pin reached the town database, where the MR wisp does not exist:\n%s", log)
	}
}

// TestPinWispsReportsAnUpdateThatMatchedNothing is the control half of gt-31nn.
//
// `bd sql` exits 0 for an UPDATE that matches no rows, so a successful command
// is not evidence the column was written. Without the read-back, a pin aimed at
// the wrong database is indistinguishable from one that worked, and the caller
// goes on believing the record is protected.
func TestPinWispsReportsAnUpdateThatMatchedNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell fake bd")
	}

	_, _, polecatDir := newMRRoutingTown(t)
	installFakeSQLBD(t, "0")

	err := NewIsolated(polecatDir).PinWisps("gastown", "gt-wisp-abc")
	if err == nil {
		t.Fatal("PinWisps succeeded against a database where nothing was pinned")
	}
	if !strings.Contains(err.Error(), "gt-wisp-abc") {
		t.Fatalf("error does not name the wisp that stayed unpinned: %v", err)
	}
}
