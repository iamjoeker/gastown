package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

// Round-trip regression for gt-gbv4.
//
// The bead is explicit that a narrower test is worthless here: "Assert the full
// patrol ROUND-TRIP, not just hook-as-x / query-as-x-slash: gt patrol report
// closes the old wisp, creates the new one, and gt hook then sees it. The
// round-trip is what actually broke; a narrower test would pass while the loop
// stays dead."
//
// It was dead because the two halves could not be satisfied at once:
//
//	wisp assignee "deacon"  -> gt hook goes false-empty
//	wisp assignee "deacon/" -> gt patrol report finds no active patrol
//
// so the deacon kept the loop alive by hand, flipping the assignee every cycle.
// That workaround is still installed on the host, outside this repo
// (~/gt/deacon/patrol-preflight.sh says "BARE — do NOT slash"), and it is why
// convergence-by-writers cannot be the whole fix: something outside gastown
// converges the other way, every cycle. The invariant these tests hold is
// therefore stronger than "gt writes one form" — it is that the loop closes
// whichever form the wisp carries, so that whoever wins, the engine turns.

// patrolStubStore is a small stateful bd stub: it tracks each wisp's assignee
// and closed-ness, so a query for the wrong address form comes back empty
// exactly as the real store would. A stub that replays a fixed listing cannot
// fail this test, because the whole defect is a query that matches nothing.
const patrolStubStore = `
store="$closes_log.store"
touch "$store"

# rows are "id<TAB>status<TAB>assignee"
put_row() {
  tmp="$store.tmp"
  grep -v "^$1	" "$store" > "$tmp" 2>/dev/null || true
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$tmp"
  mv "$tmp" "$store"
}
row_field() { grep "^$1	" "$store" | head -1 | cut -f"$2"; }

# extract --flag=value or the token after "assignee=" inside a bd query expr
arg_value() {
  for a in $all; do
    case "$a" in "$1"*) echo "${a#$1}"; return ;; esac
  done
}

case "$cmd" in
  list)
    # Durable issues table: patrol wisps never live here.
    echo '[]'
    ;;
  query)
    want_assignee=$(printf '%s' "$all" | sed -n 's/.*assignee="\([^"]*\)".*/\1/p')
    want_status=$(printf '%s' "$all" | sed -n 's/.*status="\([^"]*\)".*/\1/p')
    want_parent=$(printf '%s' "$all" | sed -n 's/.*parent="\([^"]*\)".*/\1/p')
    printf '['
    sep=''
    if [ -n "$want_parent" ]; then
      # Step children of a patrol root. One open step, so the report path has
      # something real to close before it closes the root.
      if [ "$(row_field "$want_parent-step" 2)" != "" ] && [ "$(row_field "$want_parent-step" 2)" != "closed" ]; then
        printf '{"id":"%s-step","title":"loop-or-exit","status":"open","ephemeral":true}' "$want_parent"
        sep=','
      fi
    else
      while IFS='	' read -r rid rstatus rassignee; do
        [ -n "$rid" ] || continue
        case "$rid" in *-step) continue ;; esac
        [ -z "$want_assignee" ] || [ "$rassignee" = "$want_assignee" ] || continue
        [ -z "$want_status" ] || [ "$rstatus" = "$want_status" ] || continue
        printf '%s{"id":"%s","title":"mol-deacon-patrol cycle","status":"%s","assignee":"%s","ephemeral":true}' \
          "$sep" "$rid" "$rstatus" "$rassignee"
        sep=','
      done < "$store"
    fi
    printf ']\n'
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "close $arg" >> "$closes_log"
      put_row "$arg" closed "$(row_field "$arg" 3)"
    done
    ;;
  update)
    uid=""
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      uid="$arg"; break
    done
    [ -n "$uid" ] || exit 0
    ustatus=$(arg_value --status=)
    uassignee=$(arg_value --assignee=)
    [ -n "$ustatus" ] || ustatus=$(row_field "$uid" 2)
    [ -n "$uassignee" ] || uassignee=$(row_field "$uid" 3)
    echo "update $uid status=$ustatus assignee=$uassignee" >> "$closes_log"
    put_row "$uid" "$ustatus" "$uassignee"
    ;;
  mol)
    # bd mol wisp create <proto> --root-only --actor <X> ...
    actor=""
    prev=""
    for a in "$@"; do
      [ "$prev" = "--actor" ] && actor="$a"
      prev="$a"
    done
    echo "spawn actor=$actor" >> "$closes_log"
    put_row hq-wisp-next open ""
    put_row hq-wisp-next-step open ""
    echo "Root issue: hq-wisp-next"
    ;;
esac`

// newPatrolAddressTown builds a town whose bd is the stateful stub above, plus
// a gt stub for the one `gt formula list` autoSpawnPatrol shells out to.
func newPatrolAddressTown(t *testing.T) (townRoot, closesLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot, closesLog = newDescendantStubTown(t, patrolStubStore)

	binDir := filepath.Join(townRoot, "bin")
	gtStub := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"formula\" ] && [ \"$2\" = \"list\" ]; then\n  echo '%s   deacon patrol'\nfi\nexit 0\n", constants.MolDeaconPatrol)
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtStub), 0o755); err != nil {
		t.Fatalf("write gt stub: %v", err)
	}
	return townRoot, closesLog
}

// seedPatrolWisp puts a hooked patrol root into the stub store under a specific
// assignee form.
func seedPatrolWisp(t *testing.T, closesLog, id, assignee string) {
	t.Helper()
	row := fmt.Sprintf("%s\thooked\t%s\n%s-step\topen\t\n", id, assignee, id)
	if err := os.WriteFile(closesLog+".store", []byte(row), 0o644); err != nil {
		t.Fatalf("seed store: %v", err)
	}
}

func storeAssignee(t *testing.T, closesLog, id string) (status, assignee string) {
	t.Helper()
	data, err := os.ReadFile(closesLog + ".store")
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) >= 3 && fields[0] == id {
			return fields[1], fields[2]
		}
	}
	return "", ""
}

func deaconPatrolCfg(townRoot string) PatrolConfig {
	return PatrolConfig{
		RoleName:      "deacon",
		PatrolMolName: constants.MolDeaconPatrol,
		BeadsDir:      townRoot,
		Assignee:      beads.CanonicalAgentAddress("deacon"),
		Beads:         beads.New(townRoot),
	}
}

// TestPatrolLoopClosesFromEitherAddressForm is the round trip, run once per
// address form the live wisp may be carrying when the cycle ends.
//
// Each pass asserts the whole chain, not one link: the report finds the root it
// was NOT told the form of, closes its step and then the root, spawns the
// successor, hooks it under the canonical form, and the reader `gt hook` and
// `gt prime` share then sees the successor.
func TestPatrolLoopClosesFromEitherAddressForm(t *testing.T) {
	for _, stored := range []string{"deacon", "deacon/"} {
		t.Run("stored="+stored, func(t *testing.T) {
			townRoot, closesLog := newPatrolAddressTown(t)
			seedPatrolWisp(t, closesLog, "hq-wisp-root", stored)
			cfg := deaconPatrolCfg(townRoot)

			if err := reportPatrolCycle(cfg, "all clear", "heartbeat:OK", autoSpawnPatrol); err != nil {
				t.Fatalf("reportPatrolCycle with stored assignee %q: %v", stored, err)
			}

			// 1. The old cycle is finished, step first then root.
			if status, _ := storeAssignee(t, closesLog, "hq-wisp-root"); status != "closed" {
				t.Errorf("old patrol root status = %q, want closed — the cycle cannot advance over a root it could not find", status)
			}
			if status, _ := storeAssignee(t, closesLog, "hq-wisp-root-step"); status != "closed" {
				t.Errorf("orphaned step wisp status = %q, want closed", status)
			}

			// 2. The successor exists and is hooked under the canonical form.
			status, assignee := storeAssignee(t, closesLog, "hq-wisp-next")
			if status != beads.StatusHooked {
				t.Fatalf("successor status = %q, want %q — no next cycle was hooked", status, beads.StatusHooked)
			}
			if assignee != beads.CanonicalAgentAddress("deacon") {
				t.Errorf("successor assignee = %q, want canonical %q", assignee, beads.CanonicalAgentAddress("deacon"))
			}

			// 2b. It was also BORN under that form. PatrolConfig carries the
			//     identity twice — RoleName "deacon" and Assignee "deacon/" —
			//     and spawning under the display name made every wisp start
			//     bare, with the acting identity disagreeing with the assignee
			//     written moments later on the same wisp.
			if !hasLine(readClosedIDs(t, closesLog), "spawn actor="+beads.CanonicalAgentAddress("deacon")) {
				t.Errorf("spawn actor lines = %v, want the canonical %q — the wisp is born under the other convention",
					spawnActorLines(t, closesLog), beads.CanonicalAgentAddress("deacon"))
			}

			// 3. The reader behind gt hook / gt prime sees it. This is the link
			//    that used to break: a successor written in one form and queried
			//    in the other reads as an idle town.
			found, err := listAssignedActiveWork(beads.New(townRoot), "deacon")
			if err != nil {
				t.Fatalf("listAssignedActiveWork: %v", err)
			}
			if len(found) != 1 || found[0].ID != "hq-wisp-next" {
				t.Errorf("gt hook sees %v, want [hq-wisp-next] — the loop is hooked but invisible", ids(found))
			}
		})
	}
}

// TestHookSeesPatrolWispWhicheverFormEachSideChose is the "no stable resting
// state" control, as a matrix rather than a single direction.
//
// The bead's two failure directions are one cell each of this table. Asserting
// only the cell that was reported would leave the opposite cell free to regress
// into the same stall with the roles swapped, which is exactly how the loop got
// into a state where neither form worked.
func TestHookSeesPatrolWispWhicheverFormEachSideChose(t *testing.T) {
	for _, stored := range []string{"deacon", "deacon/"} {
		for _, queried := range []string{"deacon", "deacon/"} {
			t.Run("stored="+stored+",queried="+queried, func(t *testing.T) {
				townRoot, closesLog := newPatrolAddressTown(t)
				seedPatrolWisp(t, closesLog, "hq-wisp-root", stored)

				found, err := listAssignedActiveWork(beads.New(townRoot), queried)
				if err != nil {
					t.Fatalf("listAssignedActiveWork: %v", err)
				}
				if len(found) != 1 || found[0].ID != "hq-wisp-root" {
					t.Fatalf("stored %q, queried %q: got %v, want [hq-wisp-root]",
						stored, queried, ids(found))
				}
			})
		}
	}
}

// TestPatrolStubDiscriminatesOnAddressForm validates the control.
//
// Every assertion above rests on the stub actually filtering by assignee. If it
// returned its rows regardless, all four matrix cells would pass against code
// that matches nothing — the failure mode this whole bead is about. So: ask for
// an agent that is not there, and require an empty answer.
func TestPatrolStubDiscriminatesOnAddressForm(t *testing.T) {
	townRoot, closesLog := newPatrolAddressTown(t)
	seedPatrolWisp(t, closesLog, "hq-wisp-root", "deacon/")

	found, err := listAssignedActiveWork(beads.New(townRoot), "gastown/witness")
	if err != nil {
		t.Fatalf("listAssignedActiveWork: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("stub returned %v for an unrelated assignee — it does not filter, so the round-trip assertions prove nothing", ids(found))
	}
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func spawnActorLines(t *testing.T, closesLog string) []string {
	t.Helper()
	var out []string
	for _, l := range readClosedIDs(t, closesLog) {
		if strings.HasPrefix(l, "spawn actor=") {
			out = append(out, l)
		}
	}
	return out
}

func ids(issues []*beads.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.ID)
	}
	return out
}
