package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/events"
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

// TestPoolReuseEventTypeFollowsTheSettledOutcome covers gt-ibtb.
//
// The emit used to run BEFORE the check that distinguishes success from
// refusal, so TypePoolReuseRefused fired whenever any candidate was passed
// over — which, because the gate short-circuits on the first reusable polecat,
// is the normal shape of a SUCCESS. All 21 events on record were successes and
// a genuine total refusal had never been emitted.
func TestPoolReuseEventTypeFollowsTheSettledOutcome(t *testing.T) {
	// Reuse succeeded: the candidates before it were passed over, not refused.
	reused := events.PoolReuseOutcome{ReusedPolecat: "chrome", GateAccepted: true}
	if got := poolReuseEventType(reused); got != events.TypePoolReuseSkipped {
		t.Fatalf("type = %q on a successful reuse, want %q", got, events.TypePoolReuseSkipped)
	}

	// Nothing reused: this is the case the old name always claimed and never
	// once described.
	if got := poolReuseEventType(events.PoolReuseOutcome{}); got != events.TypePoolReuseRefused {
		t.Fatalf("type = %q with no reuse, want %q", got, events.TypePoolReuseRefused)
	}

	// The gate cleared a candidate and reuse then failed on it. Accepting is
	// not reusing, and the type must follow the reuse — this is the shape that
	// would reintroduce the bug one step further along the path.
	gateOnly := events.PoolReuseOutcome{GateAccepted: true}
	if got := poolReuseEventType(gateOnly); got != events.TypePoolReuseRefused {
		t.Fatalf("type = %q when the gate accepted but reuse failed, want %q", got, events.TypePoolReuseRefused)
	}
}

// TestPoolReuseGateOutcomeMarksThePrefix pins the other half: the candidate
// list stops at the polecat the gate accepted, so it is a prefix whenever one
// was accepted — whether or not reuse then succeeded.
func TestPoolReuseGateOutcomeMarksThePrefix(t *testing.T) {
	candidates := []polecat.PoolReuseCandidate{
		{Name: "brahmin", State: polecat.StateDone, StateEligible: true, Reason: "push-failed"},
		{Name: "chrome", State: polecat.StateIdle, StateEligible: true, Reusable: true, Reason: "reusable"},
	}

	accepted := poolReuseGateOutcome("gastown", &polecat.Polecat{Name: "chrome"}, candidates, nil)
	if !accepted.GateAccepted {
		t.Fatal("GateAccepted = false when the gate short-circuited on chrome")
	}
	if accepted.Considered != 2 {
		t.Fatalf("Considered = %d, want 2", accepted.Considered)
	}
	// The accepted candidate is not a rejection.
	if len(accepted.Rejections) != 1 || !strings.Contains(accepted.Rejections[0], "brahmin") {
		t.Fatalf("Rejections = %v, want just brahmin", accepted.Rejections)
	}
	// The gate's verdict alone must never claim a reuse: the attempt has not
	// run yet at this point.
	if accepted.ReusedPolecat != "" {
		t.Fatalf("ReusedPolecat = %q before the reuse was attempted", accepted.ReusedPolecat)
	}

	exhausted := poolReuseGateOutcome("gastown", nil, candidates[:1], nil)
	if exhausted.GateAccepted {
		t.Fatal("GateAccepted = true when nothing was accepted")
	}

	// The lookup error the caller used to discard unexamined (gt-49dp).
	failed := poolReuseGateOutcome("gastown", nil, nil, errors.New("listing polecats: boom"))
	if !strings.Contains(failed.LookupError, "boom") {
		t.Fatalf("LookupError = %q, want the lookup failure", failed.LookupError)
	}
}

// TestAuditPoolReuseSummaryIsNotTheBareTypeName is the `gt audit` half of the
// same guarantee the feed curator carries. Both surfaces fell through to their
// default arm and printed the type name, and the type name was the lie.
func TestAuditPoolReuseSummaryIsNotTheBareTypeName(t *testing.T) {
	for _, tc := range []struct {
		name      string
		eventType string
		payload   map[string]interface{}
		want      string
	}{
		{
			name:      "reuse",
			eventType: events.TypePoolReuseSkipped,
			payload: events.PoolReuseOutcomePayload(events.PoolReuseOutcome{
				Rig: "gastown", Considered: 2,
				Rejections:    []string{"brahmin=push-failed state=done"},
				ReusedPolecat: "chrome", GateAccepted: true,
			}),
			want: "chrome",
		},
		{
			name:      "refusal",
			eventType: events.TypePoolReuseRefused,
			payload: events.PoolReuseOutcomePayload(events.PoolReuseOutcome{
				Rig: "gastown", Considered: 1,
				Rejections: []string{"brahmin=git-dirty state=idle"},
			}),
			want: "REFUSED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := formatFeedSummary(events.Event{Type: tc.eventType, Actor: "gt", Payload: tc.payload})
			if got == tc.eventType {
				t.Fatalf("summary fell through to the default arm: %q", got)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("summary %q must contain %q", got, tc.want)
			}
		})
	}
}
