package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// newDescendantStubTown builds a minimal town with a `bd` stub on PATH whose
// child listings are split across the two tables the way real beads splits
// them: `bd list` serves the durable issues table, `bd query ephemeral=true`
// serves the wisps table. Returns the town root and the close-log path.
func newDescendantStubTown(t *testing.T, bdBody string) (townRoot, closesLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := `{"prefix":"hq-","path":"."}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog = filepath.Join(townRoot, "closes.log")

	script := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
all="$*"
closes_log="%s"
%s
exit 0
`, closesLog, bdBody)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return townRoot, closesLog
}

func readClosedIDs(t *testing.T, closesLog string) []string {
	t.Helper()
	data, err := os.ReadFile(closesLog)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read closes log: %v", err)
	}
	var ids []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			ids = append(ids, line)
		}
	}
	sort.Strings(ids)
	return ids
}

// TestForceCloseDescendantsClosesWispChildren is the gt-u2u regression.
//
// Molecule step children are ephemeral wisps. closeDescendantsImpl used to
// enumerate them with a bare b.List, which defaults to Ephemeral=false and so
// only ever queried the durable issues table. Every wisp child came back
// invisible, nothing was closed, and — worst of all — no error was returned,
// so the caller closed the root on top of still-open steps and printed
// success. The Deacon saw exactly one orphan per cycle: the loop-or-exit step
// it could not close by hand without auto-closing the root.
func TestForceCloseDescendantsClosesWispChildren(t *testing.T) {
	body := `
case "$cmd" in
  list)
    # Durable issues table: the patrol root has no durable children.
    echo '[]'
    ;;
  query)
    # Wisps table: two open step wisps hang off the patrol root.
    case "$all" in
      *'parent="hq-wisp-root"'*)
        echo '[{"id":"hq-wisp-step-1","title":"heartbeat","status":"closed","ephemeral":true},{"id":"hq-wisp-step-2","title":"loop-or-exit","status":"open","ephemeral":true}]'
        ;;
      *) echo '[]' ;;
    esac
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "$closes_log"
    done
    ;;
esac`

	townRoot, closesLog := newDescendantStubTown(t, body)

	b := beads.New(townRoot)
	closed, err := forceCloseDescendants(b, "hq-wisp-root")
	if err != nil {
		t.Fatalf("forceCloseDescendants: %v", err)
	}

	// Only the still-open step needs closing; the already-closed one is skipped.
	if closed != 1 {
		t.Errorf("closed = %d, want 1", closed)
	}
	got := readClosedIDs(t, closesLog)
	want := []string{"hq-wisp-step-2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("closed IDs = %v, want %v", got, want)
	}
}

// TestForceCloseDescendantsClosesChildrenAtAnyPriority covers the second half
// of the same bug: b.List left ListOptions.Priority at its zero value, and
// listIssues treats Priority >= 0 as a filter, so it passed --priority=0 and
// silently dropped every durable child at any other priority.
func TestForceCloseDescendantsClosesChildrenAtAnyPriority(t *testing.T) {
	body := `
case "$cmd" in
  list)
    # Fail loudly if a priority filter is applied — the parent wants ALL children.
    case "$all" in
      *--priority=*) echo 'Error: unexpected priority filter' >&2; exit 1 ;;
    esac
    case "$all" in
      *--parent=hq-wisp-root*)
        echo '[{"id":"hq-child-p1","title":"P1 child","status":"open","priority":1}]'
        ;;
      *) echo '[]' ;;
    esac
    ;;
  query)
    echo '[]'
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "$closes_log"
    done
    ;;
esac`

	townRoot, closesLog := newDescendantStubTown(t, body)

	b := beads.New(townRoot)
	closed, err := forceCloseDescendants(b, "hq-wisp-root")
	if err != nil {
		t.Fatalf("forceCloseDescendants: %v", err)
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1", closed)
	}
	if got := readClosedIDs(t, closesLog); strings.Join(got, ",") != "hq-child-p1" {
		t.Errorf("closed IDs = %v, want [hq-child-p1]", got)
	}
}

// TestForceCloseDescendantsRecursesIntoWispGrandchildren verifies the recursion
// also crosses the table boundary, so a nested step wisp is not left behind.
func TestForceCloseDescendantsRecursesIntoWispGrandchildren(t *testing.T) {
	body := `
case "$cmd" in
  list)
    echo '[]'
    ;;
  query)
    case "$all" in
      *'parent="hq-wisp-root"'*)
        echo '[{"id":"hq-wisp-step-1","title":"step","status":"open","ephemeral":true}]'
        ;;
      *'parent="hq-wisp-step-1"'*)
        echo '[{"id":"hq-wisp-sub-1","title":"substep","status":"open","ephemeral":true}]'
        ;;
      *) echo '[]' ;;
    esac
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "$closes_log"
    done
    ;;
esac`

	townRoot, closesLog := newDescendantStubTown(t, body)

	b := beads.New(townRoot)
	closed, err := forceCloseDescendants(b, "hq-wisp-root")
	if err != nil {
		t.Fatalf("forceCloseDescendants: %v", err)
	}
	if closed != 2 {
		t.Errorf("closed = %d, want 2", closed)
	}
	got := readClosedIDs(t, closesLog)
	if strings.Join(got, ",") != "hq-wisp-step-1,hq-wisp-sub-1" {
		t.Errorf("closed IDs = %v, want [hq-wisp-step-1 hq-wisp-sub-1]", got)
	}
}
