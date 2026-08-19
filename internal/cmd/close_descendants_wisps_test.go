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

state_log="$closes_log.state"

# mark_closed / is_closed / status_of let a body's child listings reflect the
# closes this stub has already performed. A stub that replays its initial state
# forever cannot tell a close that worked from one that was refused, which is
# exactly the distinction closeDescendantsImpl now has to get right (gt-3xmz).
# The state is kept separate from closes_log so bodies are free to log calls in
# whatever format their assertions want.
mark_closed() {
  for arg in "$@"; do
    case "$arg" in --*) continue ;; esac
    echo "$arg" >> "$state_log"
  done
}
is_closed() {
  [ -f "$state_log" ] && grep -qx "$1" "$state_log"
}
status_of() {
  if is_closed "$1"; then echo closed; else echo "$2"; fi
}
log_closes() {
  for arg in "$@"; do
    case "$arg" in --*) continue ;; esac
    echo "$arg" >> "$closes_log"
  done
  mark_closed "$@"
}
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
        printf '[{"id":"hq-wisp-step-1","title":"heartbeat","status":"closed","ephemeral":true},{"id":"hq-wisp-step-2","title":"loop-or-exit","status":"%s","ephemeral":true}]\n' "$(status_of hq-wisp-step-2 open)"
        ;;
      *) echo '[]' ;;
    esac
    ;;
  close)
    log_closes "$@"
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
        printf '[{"id":"hq-child-p1","title":"P1 child","status":"%s","priority":1}]\n' "$(status_of hq-child-p1 open)"
        ;;
      *) echo '[]' ;;
    esac
    ;;
  query)
    echo '[]'
    ;;
  close)
    log_closes "$@"
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
        printf '[{"id":"hq-wisp-step-1","title":"step","status":"%s","ephemeral":true}]\n' "$(status_of hq-wisp-step-1 open)"
        ;;
      *'parent="hq-wisp-step-1"'*)
        printf '[{"id":"hq-wisp-sub-1","title":"substep","status":"%s","ephemeral":true}]\n' "$(status_of hq-wisp-sub-1 open)"
        ;;
      *) echo '[]' ;;
    esac
    ;;
  close)
    log_closes "$@"
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

// chainStubBody builds a stub whose N step wisps under hq-wisp-root form a
// `blocks` chain — step-K cannot close until step-(K-1) is closed — and which
// lists them in REVERSE chain order, the worst case for a single pass. `close`
// mirrors bd: it walks the IDs in the order given, closes the ones whose
// blocker is already closed, skips the rest, and exits non-zero only when it
// closed nothing at all. unclosable, if non-empty, names a step that refuses
// forever no matter what precedes it.
func chainStubBody(n int, unclosable string) string {
	var listed []string
	for i := n; i >= 1; i-- {
		listed = append(listed, fmt.Sprintf(
			`{"id":"hq-wisp-step-%d","title":"step %d","status":"'"$(status_of hq-wisp-step-%d open)"'","ephemeral":true}`, i, i, i))
	}
	return fmt.Sprintf(`
case "$cmd" in
  list)
    echo '[]'
    ;;
  query)
    case "$all" in
      *'parent="hq-wisp-root"'*)
        echo '[%s]'
        ;;
      *) echo '[]' ;;
    esac
    ;;
  close)
    closed_any=0
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "$closes_log.attempts"
      if [ -n "%s" ] && [ "$arg" = "%s" ]; then
        echo "cannot close blocked issue: $arg is blocked by [external]" >&2
        continue
      fi
      n=${arg##*-}
      prev=$((n - 1))
      if [ "$prev" -ge 1 ] && ! is_closed "hq-wisp-step-$prev"; then
        echo "cannot close blocked issue: $arg is blocked by [hq-wisp-step-$prev]" >&2
        continue
      fi
      echo "$arg" >> "$closes_log"
      mark_closed "$arg"
      closed_any=1
    done
    [ "$closed_any" = 1 ] || exit 1
    ;;
esac`, strings.Join(listed, ","), unclosable, unclosable)
}

// TestCloseDescendantsDrainsBlocksChain is the gt-3xmz regression.
//
// Molecule steps are siblings chained by `blocks` edges. closeDescendantsImpl
// used to enumerate the children once and hand the whole open set to a single
// b.Close, so it only ever closed the steps that happened to be listed after
// their blocker — here, with the chain listed backwards, exactly one of five.
// The recursion above it does not help: it walks parent-child, and the ordering
// constraint runs sideways between siblings.
func TestCloseDescendantsDrainsBlocksChain(t *testing.T) {
	townRoot, closesLog := newDescendantStubTown(t, chainStubBody(5, ""))

	b := beads.New(townRoot)
	closed, err := closeDescendantsImpl(b, "hq-wisp-root", false)
	if err != nil {
		t.Fatalf("closeDescendantsImpl: %v", err)
	}
	if closed != 5 {
		t.Errorf("closed = %d, want 5 (a single pass closes only the chain's head)", closed)
	}
	got := readClosedIDs(t, closesLog)
	want := []string{
		"hq-wisp-step-1", "hq-wisp-step-2", "hq-wisp-step-3",
		"hq-wisp-step-4", "hq-wisp-step-5",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("closed IDs = %v, want %v", got, want)
	}
}

// TestCloseDescendantsReportsStrandedChildren covers the half of gt-3xmz that
// made the strand silent. `bd close a b c` exits 0 when ANY of the IDs settled
// as closed, and the old code took that exit status as proof that all of them
// had — it added len(idsToClose) to the count and returned no error. The caller
// then closed the root over still-open steps and printed a success line naming
// a count of closes that had not happened.
//
// Here step-3 refuses forever, so steps 1 and 2 close and steps 3-5 cannot. The
// count must be the 2 that really closed, and the still-open steps must come
// back as an error rather than silence.
func TestCloseDescendantsReportsStrandedChildren(t *testing.T) {
	townRoot, closesLog := newDescendantStubTown(t, chainStubBody(5, "hq-wisp-step-3"))

	b := beads.New(townRoot)
	closed, err := closeDescendantsImpl(b, "hq-wisp-root", false)
	if err == nil {
		t.Fatal("closeDescendantsImpl returned no error with 3 children left open")
	}
	if closed != 2 {
		t.Errorf("closed = %d, want 2 (only the steps that really closed)", closed)
	}
	for _, id := range []string{"hq-wisp-step-3", "hq-wisp-step-4", "hq-wisp-step-5"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error %q does not name still-open child %s", err, id)
		}
	}
	got := readClosedIDs(t, closesLog)
	if strings.Join(got, ",") != "hq-wisp-step-1,hq-wisp-step-2" {
		t.Errorf("closed IDs = %v, want [hq-wisp-step-1 hq-wisp-step-2]", got)
	}
}

// TestCloseDescendantsStopsWhenAPassClosesNothing guards the loop's exit. A
// child nothing can close must not be retried closeDescendantsPassLimit times:
// the passes stop as soon as one of them fails to shrink the open set.
//
// With step-2 unclosable, the accounting is exact. Pass 1 attempts both steps
// and closes step-1 — progress, so a second pass is warranted (closing step-1
// is precisely the kind of event that unblocks a sibling). Pass 2 attempts only
// the still-open step-2, closes nothing, and ends the loop. Three close
// attempts, not 2 + 63.
func TestCloseDescendantsStopsWhenAPassClosesNothing(t *testing.T) {
	townRoot, closesLog := newDescendantStubTown(t, chainStubBody(2, "hq-wisp-step-2"))

	b := beads.New(townRoot)
	closed, err := closeDescendantsImpl(b, "hq-wisp-root", false)
	if err == nil {
		t.Fatal("closeDescendantsImpl returned no error with a child left open")
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1", closed)
	}
	if !strings.Contains(err.Error(), "hq-wisp-step-2") {
		t.Errorf("error %q does not name the stranded child", err)
	}

	attempts := readClosedIDs(t, closesLog+".attempts")
	want := []string{"hq-wisp-step-1", "hq-wisp-step-2", "hq-wisp-step-2"}
	if strings.Join(attempts, ",") != strings.Join(want, ",") {
		t.Errorf("close attempts = %v, want %v", attempts, want)
	}
}
