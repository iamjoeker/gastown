package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/polecat"
)

// TestPoolReuseRejectionsNamesEveryRefusal covers the second half of gt-49dp.
//
// `gt sling` used to call FindIdlePolecat and branch on
// `findErr == nil && idlePolecat != nil` with no else. When the gate rejected
// every candidate, control fell through to allocating a fresh worktree emitting
// nothing at all — no stdout, no event, and findErr discarded unexamined — while
// the success path logged TypeSpawn. So the pool's growth was measurable and its
// cause was not: the reuse gate runs eleven predicates and every refusal was
// unrecorded. This asserts the reasons now survive the trip.
func TestPoolReuseRejectionsNamesEveryRefusal(t *testing.T) {
	candidates := []polecat.PoolReuseCandidate{
		{Name: "chrome", State: polecat.StateDone, StateEligible: true, Reason: "mq-not-submitted"},
		{Name: "dag", State: polecat.StateWorking, StateEligible: false, Reason: "state-not-eligible"},
		{Name: "slit", State: polecat.StateIdle, StateEligible: true, Reason: "git-dirty"},
	}

	got := poolReuseRejections(candidates)
	if len(got) != 3 {
		t.Fatalf("rejections = %v, want one per refused candidate", got)
	}
	// The name and the reason together are the whole point: the mayor could
	// measure the cost of the refusals but could not name the condition, because
	// neither half was ever written down.
	for _, want := range []string{"chrome=mq-not-submitted", "dag=state-not-eligible", "slit=git-dirty"} {
		if !strings.Contains(strings.Join(got, "; "), want) {
			t.Fatalf("rejections = %v, want to contain %q", got, want)
		}
	}
	if !strings.Contains(got[0], "state=done") {
		t.Fatalf("rejection %q must carry the state considered", got[0])
	}

	// The accepted candidate is not a refusal and must not be reported as one.
	withReuse := append(candidates, polecat.PoolReuseCandidate{
		Name: "toast", State: polecat.StateIdle, StateEligible: true, Reusable: true, Reason: "reusable",
	})
	if got := poolReuseRejections(withReuse); len(got) != 3 || strings.Contains(strings.Join(got, "; "), "toast") {
		t.Fatalf("rejections = %v, want the reusable polecat omitted", got)
	}

	// A reason-less refusal is still a refusal. Emitting an empty field here
	// would rebuild the silence this path exists to break.
	blank := poolReuseRejections([]polecat.PoolReuseCandidate{{Name: "mute", State: polecat.StateIdle, StateEligible: true}})
	if len(blank) != 1 || !strings.Contains(blank[0], "mute=unknown") {
		t.Fatalf("blank reason = %v, want mute=unknown", blank)
	}

	// Empty pool: nothing was considered, so there is nothing to report. This is
	// the control — if this arm ever produced output, the caller would log a
	// refusal every time a rig had no polecats at all.
	if got := poolReuseRejections(nil); len(got) != 0 {
		t.Fatalf("empty pool = %v, want no rejections", got)
	}
}
