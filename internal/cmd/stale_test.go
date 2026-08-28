package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/version"
)

// TestOutputStaleText exercises the pure text renderer for `gt stale`,
// covering the Skipped / Stale / Fresh branches added for GH#4034.
// It asserts on the unstyled literal substrings (style.Render only wraps
// the leading glyph, not the message text) so it is colour-agnostic.
//
// Note: outputStaleText is a plain function; this test never executes the
// cobra command tree, so the macOS unsigned-binary guard in
// persistentPreRun is not tripped. Run targeted (`-run TestOutputStaleText`)
// to avoid sibling tests that do execute commands.
func TestOutputStaleText(t *testing.T) {
	tests := []struct {
		name    string
		output  StaleOutput
		want    []string // substrings that must be present
		notWant []string // substrings that must be absent
	}{
		{
			name: "skipped names the reason and binary",
			output: StaleOutput{
				Skipped:      true,
				SkipReason:   "source worktree not on a build branch",
				BinaryCommit: "abc1234567890",
			},
			want: []string{
				"Binary staleness check skipped",
				"source worktree not on a build branch",
				"abc123456789",
			},
			notWant: []string{"Binary is stale", "Binary is fresh"},
		},
		{
			name: "stale, behind, diverged, off build branch, unsafe",
			output: StaleOutput{
				Stale:         true,
				Forward:       false,
				OnMainBranch:  false,
				SafeToRebuild: false,
				BinaryCommit:  "abc1234567890",
				RepoCommit:    "def4567890123",
				CompareRef:    "main",
				CommitsBehind: 3,
			},
			want: []string{
				"Binary is stale",
				"Build ref (main): def456789012",
				"(3 commits behind main)",
				"main is NOT a descendant of binary commit",
				"source worktree is not on a build branch (compared against main)",
				"NOT safe for automated rebuild (forward=false, build_branch=false)",
			},
			notWant: []string{"Safe to rebuild: run"},
		},
		{
			name: "stale, forward, on build branch, safe to rebuild",
			output: StaleOutput{
				Stale:         true,
				Forward:       true,
				OnMainBranch:  true,
				SafeToRebuild: true,
				BinaryCommit:  "abc1234567890",
				RepoCommit:    "def4567890123",
				CompareRef:    "carry/ops",
			},
			want: []string{
				"Binary is stale",
				"Build ref (carry/ops): def456789012",
				"Safe to rebuild: run 'make build && make install'",
			},
			notWant: []string{
				"commits behind",
				"NOT a descendant",
				"not on a build branch",
				"NOT safe for automated rebuild",
			},
		},
		{
			// "fresh" is the verdict that was wrong for two hours, so the line
			// has to say where the compare ref came from. Reading it from the
			// remote is evidence; reading it from a local checkout nothing
			// updates is not, and the two must not print the same.
			name: "fresh against a refreshed ref says so",
			output: StaleOutput{
				Stale:        false,
				BinaryCommit: "abc1234567890",
				CompareRef:   "origin/main",
				Refreshed:    true,
			},
			want: []string{
				"Binary is fresh",
				"Commit: abc123456789",
				"(compared against origin/main, read from the remote)",
			},
			notWant: []string{"Binary is stale", "skipped", "not read from the remote"},
		},
		{
			name: "fresh against an unrefreshed ref flags the provenance",
			output: StaleOutput{
				Stale:        false,
				BinaryCommit: "abc1234567890",
				CompareRef:   "main",
				Refreshed:    false,
			},
			want: []string{
				"Binary is fresh",
				"(compared against main, from a local ref — not read from the remote)",
			},
			notWant: []string{"Binary is stale", "skipped"},
		},
		{
			name: "fresh without compare ref omits the comparison line",
			output: StaleOutput{
				Stale:        false,
				BinaryCommit: "abc1234567890",
			},
			want:    []string{"Binary is fresh", "Commit: abc123456789"},
			notWant: []string{"compared against"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = outputStaleText(tt.output) })
			if err != nil {
				t.Fatalf("outputStaleText returned error: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q\n--- got ---\n%s", w, out)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(out, nw) {
					t.Errorf("output unexpectedly contains %q\n--- got ---\n%s", nw, out)
				}
			}
		})
	}
}

// TestStaleOutputWireKeys pins the JSON keys the rebuild-gt plugin reads.
//
// The plugin's decision table is only as good as the names it looks up, and a
// missing key does not error there — `d.get("compare_ref_refreshed")` returns
// None, which the plugin correctly treats as "an older gt is answering" and
// falls back to the legacy reading. Renaming this field in Go would therefore
// silently restore the pre-fix behaviour with every test still green.
func TestStaleOutputWireKeys(t *testing.T) {
	raw, err := json.Marshal(StaleOutput{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Always present, even at their zero value: an absent key means something
	// different from false to every consumer.
	for _, key := range []string{"stale", "safe_to_rebuild", "compare_ref_refreshed"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("StaleOutput JSON is missing %q; plugins/rebuild-gt/run.sh reads it", key)
		}
	}

	raw, err = json.Marshal(StaleOutput{Skipped: true, SkipReason: "why", RefreshError: "boom"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"skipped", "skip_reason", "refresh_error"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("StaleOutput JSON is missing %q when set; plugins/rebuild-gt/run.sh reads it", key)
		}
	}
}

// TestStaleReadsTheRemoteByDefault: the remote read is the fix. If --offline
// ever became the default, gt stale would go back to answering from a checkout
// nothing updates and the rebuild loop would silently stop again.
func TestStaleReadsTheRemoteByDefault(t *testing.T) {
	flag := staleCmd.Flags().Lookup("offline")
	if flag == nil {
		t.Fatal("gt stale has no --offline flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("--offline defaults to %q, want false (the remote read must be the default)", flag.DefValue)
	}
}

func TestStaleQuietExitCode(t *testing.T) {
	tests := []struct {
		name string
		info *version.StaleBinaryInfo
		want int
	}{
		{name: "skipped is undetermined", info: &version.StaleBinaryInfo{Skipped: true}, want: 2},
		{name: "stale", info: &version.StaleBinaryInfo{IsStale: true}, want: 0},
		{name: "fresh", info: &version.StaleBinaryInfo{}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := staleQuietExitCode(tt.info); got != tt.want {
				t.Errorf("staleQuietExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}
