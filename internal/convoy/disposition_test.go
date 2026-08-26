package convoy

import (
	"testing"
)

// aliveSession makes exactly the named sessions look live.
func aliveSession(names ...string) func(string) bool {
	live := map[string]bool{}
	for _, n := range names {
		live[n] = true
	}
	return func(n string) bool { return live[n] }
}

// queuedFor makes exactly the named beads look like they have an open MR.
func queuedFor(ids ...string) func(string) bool {
	q := map[string]bool{}
	for _, id := range ids {
		q[id] = true
	}
	return func(id string) bool { return q[id] }
}

// TestClassify_LiveSessionIsWorkingRegardlessOfSilence is acceptance 1 of
// gt-skzk.1 at the classifier: a bead whose polecat session is up is WORKING,
// and no amount of elapsed silence can change that — Classify takes no clock at
// all, so the only way to fail this test is to reintroduce one.
func TestClassify_LiveSessionIsWorkingRegardlessOfSilence(t *testing.T) {
	tracked := TrackedIssue{ID: "gt-1", Status: "in_progress", Assignee: "gastown/polecats/brahmin"}

	sessionName, _ := AssigneeSessionName(tracked.Assignee)
	if sessionName == "" {
		t.Fatalf("AssigneeSessionName(%q) returned no session name", tracked.Assignee)
	}

	got := Classify(tracked, Env{SessionAlive: aliveSession(sessionName)})
	if got != DispoWorking {
		t.Fatalf("Classify() = %q, want %q", got, DispoWorking)
	}

	_, evidence := ClassifyAll("", []TrackedIssue{tracked}, Env{SessionAlive: aliveSession(sessionName)})
	if ws := WorkStatus(1, evidence); ws != WorkStatusWorking {
		t.Fatalf("WorkStatus() = %q, want %q", ws, WorkStatusWorking)
	}
}

// TestClassify_QueuedMRIsNotAbandoned is acceptance 3: a polecat that pushed,
// submitted and exited leaves a dead session behind, and that is the ordinary
// end of a SUCCESSFUL run.
func TestClassify_QueuedMRIsNotAbandoned(t *testing.T) {
	tracked := TrackedIssue{ID: "gt-2", Status: "hooked", Assignee: "gastown/polecats/brahmin"}

	env := Env{
		SessionAlive: aliveSession(), // session is gone
		QueuedMR:     queuedFor("gt-2"),
	}
	if got := Classify(tracked, env); got != DispoInQueue {
		t.Fatalf("Classify() with an open MR = %q, want %q", got, DispoInQueue)
	}

	_, evidence := ClassifyAll("", []TrackedIssue{tracked}, env)
	if ws := WorkStatus(1, evidence); ws != WorkStatusInQueue {
		t.Fatalf("WorkStatus() = %q, want %q", ws, WorkStatusInQueue)
	}

	// Control: without the merge-queue lookup the same bead reads as ready, so
	// the test above is measuring the MR consultation and not something else.
	if got := Classify(tracked, Env{SessionAlive: aliveSession()}); got != DispoReady {
		t.Fatalf("Classify() with no MR lookup = %q, want %q", got, DispoReady)
	}
}

// strandedReasonV1 is the verdict `gt convoy stranded` computed before the
// dashboard shared its classifier: an INDEPENDENT spelling of the old rules,
// kept here on purpose. Two surfaces agreeing because one calls the other is
// not evidence that the shared verdict preserved the old behaviour; this
// function is the differential control that makes the next test say something.
func strandedReasonV1(trackedCount int, evidence map[string]int) string {
	switch {
	case trackedCount == 0:
		return ReasonEmpty
	case evidence[DispoReady] > 0:
		return ReasonFeedable
	case evidence[DispoBlocked]+evidence[DispoUnknown]+evidence[DispoNotSlingable] > 0:
		return ReasonNeedsReview
	case evidence[DispoClosed] == trackedCount:
		return ReasonComplete
	default:
		return ReasonWaiting
	}
}

// TestStrandedAndDashboardNeverDisagree is acceptance 2 of gt-skzk.1.
//
// The dashboard reads WorkStatus and `gt convoy stranded` reads Reason. If the
// two can differ about a convoy, one of them is wrong and the operator cannot
// tell which — so this walks a table of convoy shapes and asserts three things
// at once: the fine verdict maps onto the coarse one, the coarse one still
// matches the rules the CLI shipped with, and STUCK appears on the dashboard
// exactly when the CLI calls a convoy unexplained.
func TestStrandedAndDashboardNeverDisagree(t *testing.T) {
	tests := []struct {
		name           string
		evidence       map[string]int
		wantWorkStatus string
	}{
		{"empty convoy", map[string]int{}, WorkStatusEmpty},
		{"all closed", map[string]int{DispoClosed: 3}, WorkStatusComplete},
		{"one polecat working", map[string]int{DispoWorking: 1}, WorkStatusWorking},
		{"working beside closed", map[string]int{DispoWorking: 1, DispoClosed: 4}, WorkStatusWorking},
		{"queued MR, no worker", map[string]int{DispoInQueue: 1}, WorkStatusInQueue},
		{"queued beside closed", map[string]int{DispoInQueue: 1, DispoClosed: 2}, WorkStatusInQueue},
		{"ready work", map[string]int{DispoReady: 2}, WorkStatusReady},
		{"ready beside working", map[string]int{DispoReady: 1, DispoWorking: 1}, WorkStatusReady},
		{"blocked", map[string]int{DispoBlocked: 1}, WorkStatusStuck},
		{"unreadable store", map[string]int{DispoUnknown: 1}, WorkStatusStuck},
		{"town-level bead", map[string]int{DispoNotSlingable: 1}, WorkStatusStuck},
		{"blocked beside working", map[string]int{DispoBlocked: 1, DispoWorking: 1}, WorkStatusStuck},
		{"deferred only", map[string]int{DispoDeferred: 1}, WorkStatusWaiting},
		{"scheduled only", map[string]int{DispoScheduled: 2}, WorkStatusWaiting},
		{"deferred beside closed", map[string]int{DispoDeferred: 1, DispoClosed: 1}, WorkStatusWaiting},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := 0
			for _, n := range tt.evidence {
				total += n
			}

			gotWorkStatus := WorkStatus(total, tt.evidence)
			if gotWorkStatus != tt.wantWorkStatus {
				t.Fatalf("WorkStatus(%d, %v) = %q, want %q", total, tt.evidence, gotWorkStatus, tt.wantWorkStatus)
			}

			gotReason := Reason(gotWorkStatus)
			if wantReason := strandedReasonV1(total, tt.evidence); gotReason != wantReason {
				t.Fatalf("dashboard says %q -> stranded reason %q, but `gt convoy stranded` says %q",
					gotWorkStatus, gotReason, wantReason)
			}

			// The operator-facing half: an alarm on one surface must be an alarm
			// on the other, in both directions.
			if (gotWorkStatus == WorkStatusStuck) != (gotReason == ReasonNeedsReview) {
				t.Fatalf("stuck/needs-review disagree: work status %q, reason %q", gotWorkStatus, gotReason)
			}
			if gotReason == ReasonWaiting && gotWorkStatus == WorkStatusStuck {
				t.Fatalf("convoy is waiting per the CLI but painted stuck on the dashboard")
			}
		})
	}
}

// TestReasonIsTotalOverWorkStatus guards the mapping's completeness: a new work
// status with no reason would silently fall into "waiting", which is the
// reassuring direction and therefore the dangerous one.
func TestReasonIsTotalOverWorkStatus(t *testing.T) {
	want := map[string]string{
		WorkStatusEmpty:    ReasonEmpty,
		WorkStatusReady:    ReasonFeedable,
		WorkStatusStuck:    ReasonNeedsReview,
		WorkStatusComplete: ReasonComplete,
		WorkStatusWorking:  ReasonWaiting,
		WorkStatusInQueue:  ReasonWaiting,
		WorkStatusWaiting:  ReasonWaiting,
	}
	for ws, wantReason := range want {
		if got := Reason(ws); got != wantReason {
			t.Errorf("Reason(%q) = %q, want %q", ws, got, wantReason)
		}
	}
}

func TestFormatEvidence(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]int
		want string
	}{
		{"working and closed hides closed", map[string]int{DispoWorking: 1, DispoClosed: 3}, "1 working"},
		{"all closed keeps closed", map[string]int{DispoClosed: 2}, "2 closed"},
		{"stable order", map[string]int{DispoDeferred: 1, DispoReady: 2}, "2 ready, 1 deferred"},
		{"empty", map[string]int{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatEvidence(tt.in); got != tt.want {
				t.Fatalf("FormatEvidence(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
