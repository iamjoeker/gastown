package beads

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestIsMailBead(t *testing.T) {
	tests := []struct {
		name  string
		issue *Issue
		want  bool
	}{
		{"nil", nil, false},
		{"label", &Issue{ID: "hq-1", Type: "task", Labels: []string{"gt:message"}}, true},
		{"label among others", &Issue{ID: "hq-2", Type: "task", Labels: []string{"from:mayor/", "gt:message", "read"}}, true},
		{"legacy type", &Issue{ID: "hq-3", Type: "message"}, true},
		{"type case-insensitive", &Issue{ID: "hq-4", Type: "Message"}, true},
		{"label case-insensitive", &Issue{ID: "hq-5", Type: "task", Labels: []string{"GT:MESSAGE"}}, true},
		{"ordinary work", &Issue{ID: "gt-1", Type: "bug", Labels: []string{"gt:record"}}, false},
		{"no labels at all", &Issue{ID: "gt-2", Type: "task"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsMailBead(tt.issue); got != tt.want {
				t.Errorf("IsMailBead(%+v) = %v, want %v", tt.issue, got, tt.want)
			}
		})
	}
}

// TestIsMailBeadCannotSeeLabelStrippedRows pins the limitation the doc comment
// claims, so the claim stays true rather than merely being written down. A row
// from `bd ready --json` carries no labels field, so a message arrives looking
// exactly like work — which is why the exclusion has to be made by the query.
func TestIsMailBeadCannotSeeLabelStrippedRows(t *testing.T) {
	asStored := &Issue{ID: "hq-br130", Type: "task", Labels: []string{"gt:message"}}
	asReadyReturnsIt := &Issue{ID: "hq-br130", Type: "task"}

	if !IsMailBead(asStored) {
		t.Fatal("stored shape should be recognised as mail")
	}
	if IsMailBead(asReadyReturnsIt) {
		t.Fatal("label-stripped shape is indistinguishable from work; if this " +
			"now passes, the row-level filter can be trusted and readyIssues' " +
			"comment about needing a query-level exclusion needs revisiting")
	}
}

// TestReadyExcludingLabelsPassesExcludeLabel is the guard against the exclusion
// going inert. Building the flag but never putting it on the command line looks
// identical from the caller's side to a store with no mail in it.
func TestReadyExcludingLabelsPassesExcludeLabel(t *testing.T) {
	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")

	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
echo '[]'
exit 0
`
	if err := os.WriteFile(filepath.Join(stubDir, "bd"), []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()

	b := NewIsolated(t.TempDir())
	if _, err := b.ReadyExcludingLabels(MessageLabel); err != nil {
		t.Fatalf("ReadyExcludingLabels: %v", err)
	}

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")

	if !slices.Contains(args, "ready") {
		t.Fatalf("expected a bd ready invocation, got:\n%s", data)
	}
	if !slices.Contains(args, "--exclude-label") || !slices.Contains(args, MessageLabel) {
		t.Errorf("expected --exclude-label %s in bd args, got:\n%s", MessageLabel, data)
	}
}

// TestReadyDoesNotExclude is the control for the test above: without it, a
// build that put --exclude-label on every ready call would pass unnoticed and
// the flag that restores the old listing would be silently dead.
func TestReadyDoesNotExclude(t *testing.T) {
	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")

	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
echo '[]'
exit 0
`
	if err := os.WriteFile(filepath.Join(stubDir, "bd"), []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()

	b := NewIsolated(t.TempDir())
	if _, err := b.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	if strings.Contains(string(data), "--exclude-label") {
		t.Errorf("Ready() must not exclude anything, got:\n%s", data)
	}
}

// TestReadyExcludingLabelsWithNoLabelsIsReady keeps the empty case from
// producing a bare `--exclude-label` with no value, which bd rejects.
func TestReadyExcludingLabelsWithNoLabelsIsReady(t *testing.T) {
	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")

	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
echo '[]'
exit 0
`
	if err := os.WriteFile(filepath.Join(stubDir, "bd"), []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()

	b := NewIsolated(t.TempDir())
	if _, err := b.ReadyExcludingLabels(); err != nil {
		t.Fatalf("ReadyExcludingLabels(): %v", err)
	}

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	if strings.Contains(string(data), "--exclude-label") {
		t.Errorf("no labels means no exclusion flag, got:\n%s", data)
	}
}
