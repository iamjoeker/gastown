package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

// fakePushFailedUpdater models the agent bead as a single mutable flag so the
// read-back is answered by the write rather than by the fake's own optimism.
// readErr and stuck exist to reproduce the two ways a write can report success
// without the row having changed — the failure mode this reconcile is guarding
// against, since a false "cleared" would strand the polecat with a diagnostic
// claiming otherwise.
type fakePushFailedUpdater struct {
	pushFailed bool
	writeErr   error
	readErr    error
	stuck      bool // write "succeeds" but the row keeps push_failed=true
	writes     int
	id         string
	fieldsNil  bool
}

func (f *fakePushFailedUpdater) UpdateAgentDescriptionFields(id string, updates beads.AgentFieldUpdates) error {
	f.writes++
	f.id = id
	if f.writeErr != nil {
		return f.writeErr
	}
	if updates.PushFailed != nil && !f.stuck {
		f.pushFailed = *updates.PushFailed
	}
	return nil
}

func (f *fakePushFailedUpdater) GetAgentBead(id string) (*beads.Issue, *beads.AgentFields, error) {
	if f.readErr != nil {
		return nil, nil, f.readErr
	}
	if f.fieldsNil {
		return nil, nil, nil
	}
	return nil, &beads.AgentFields{PushFailed: f.pushFailed}, nil
}

func refutedInput() polecat.WorkstateInput {
	return polecat.WorkstateInput{PushFailed: true, PushFailedRefuted: true}
}

func TestReconcilePushFailedIfRefuted_ClearsAndConfirms(t *testing.T) {
	status := &RecoveryStatus{Verdict: "SAFE_TO_NUKE", Branch: "polecat/brahmin/gt-bel1"}
	updater := &fakePushFailedUpdater{pushFailed: true}
	fields := &beads.AgentFields{PushFailed: true}

	reconcilePushFailedIfRefuted(status, updater, "gt-gastown-polecat-brahmin", refutedInput(), fields)

	// The caller's copy must track the store, or the command goes on printing
	// "the bead still records push_failed=true" about a bead that does not.
	if fields.PushFailed {
		t.Fatal("caller's AgentFields still reads push_failed=true after a confirmed clear")
	}
	if pushFailedReconcileCandidate(status, refutedInput(), fields) {
		t.Fatal("still a reconcile candidate after the flag was cleared")
	}
	if updater.writes != 1 || updater.id != "gt-gastown-polecat-brahmin" {
		t.Fatalf("writes = %d id = %q, want 1 write to the agent bead", updater.writes, updater.id)
	}
	if updater.pushFailed {
		t.Fatal("agent bead still reads push_failed=true after reconcile")
	}
	if !status.Reconciled {
		t.Fatal("status.Reconciled = false, want true")
	}
	if status.Verdict != "SAFE_TO_NUKE" || status.NeedsRecovery {
		t.Fatalf("verdict = %q needs=%v, want the verdict left alone", status.Verdict, status.NeedsRecovery)
	}
	if !hasDiagnostic(status, "reconciled_push_failed=false") {
		t.Fatalf("diagnostics = %v, want reconciled_push_failed=false", status.Diagnostics)
	}
}

// The whole point of the reconcile is that the field stops being true. A write
// that returns nil while the row is unchanged must not be reported as a clear.
func TestReconcilePushFailedIfRefuted_UnconfirmedWriteFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		updater *fakePushFailedUpdater
		want    string
	}{
		{"write error", &fakePushFailedUpdater{pushFailed: true, writeErr: errors.New("bd update failed")}, "bd update failed"},
		{"read-back error", &fakePushFailedUpdater{pushFailed: true, readErr: errors.New("dolt timeout")}, "could not be re-read"},
		{"row unchanged", &fakePushFailedUpdater{pushFailed: true, stuck: true}, "still reads push_failed=true"},
		{"unparsable fields", &fakePushFailedUpdater{pushFailed: true, fieldsNil: true}, "still reads push_failed=true"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := &RecoveryStatus{Verdict: "SAFE_TO_NUKE"}
			reconcilePushFailedIfRefuted(status, tc.updater, "gt-gastown-polecat-brahmin", refutedInput(),
				&beads.AgentFields{PushFailed: true})

			if status.Verdict != "NEEDS_RECOVERY" || !status.NeedsRecovery {
				t.Fatalf("verdict = %q needs=%v, want NEEDS_RECOVERY true", status.Verdict, status.NeedsRecovery)
			}
			if status.Reconciled {
				t.Fatal("status.Reconciled = true after an unconfirmed write")
			}
			if len(status.Blockers) == 0 || !strings.Contains(status.Blockers[0], "push_failed_reconcile_failed") {
				t.Fatalf("blockers = %v, want push_failed_reconcile_failed", status.Blockers)
			}
			if !strings.Contains(status.Blockers[0], tc.want) {
				t.Fatalf("blocker = %q, want it to name %q", status.Blockers[0], tc.want)
			}
		})
	}
}

func TestReconcilePushFailedIfRefuted_NilUpdaterFailsClosed(t *testing.T) {
	status := &RecoveryStatus{Verdict: "SAFE_TO_NUKE"}
	reconcilePushFailedIfRefuted(status, nil, "gt-gastown-polecat-brahmin", refutedInput(),
		&beads.AgentFields{PushFailed: true})

	if status.Verdict != "NEEDS_RECOVERY" || len(status.Blockers) == 0 {
		t.Fatalf("verdict = %q blockers = %v, want NEEDS_RECOVERY with a blocker", status.Verdict, status.Blockers)
	}
}

// Refuted only by measurement, never by silence. Each of these leaves the flag
// alone, and the "unrefuted" case is the one that matters: an unmeasured or
// failed git check must not be able to erase evidence of a real push failure.
func TestPushFailedReconcileCandidateRequiresStrictPredicates(t *testing.T) {
	tests := []struct {
		name   string
		status *RecoveryStatus
		input  polecat.WorkstateInput
		fields *beads.AgentFields
		want   bool
	}{
		{
			name:   "measured refutation on a clear polecat",
			status: &RecoveryStatus{Verdict: "SAFE_TO_NUKE"},
			input:  refutedInput(),
			fields: &beads.AgentFields{PushFailed: true},
			want:   true,
		},
		{
			name:   "flag not set",
			status: &RecoveryStatus{Verdict: "SAFE_TO_NUKE"},
			input:  polecat.WorkstateInput{PushFailedRefuted: true},
			fields: &beads.AgentFields{},
		},
		{
			name:   "unrefuted flag survives",
			status: &RecoveryStatus{Verdict: "SAFE_TO_NUKE"},
			input:  polecat.WorkstateInput{PushFailed: true},
			fields: &beads.AgentFields{PushFailed: true},
		},
		{
			name:   "another blocker still stands",
			status: &RecoveryStatus{Verdict: "NEEDS_RECOVERY"},
			input:  refutedInput(),
			fields: &beads.AgentFields{PushFailed: true},
		},
		{
			name:   "work outside the merge queue",
			status: &RecoveryStatus{Verdict: "NEEDS_MQ_SUBMIT"},
			input:  refutedInput(),
			fields: &beads.AgentFields{PushFailed: true},
		},
		{
			name:   "no facts gathered",
			status: &RecoveryStatus{Verdict: "UNVERIFIED"},
			input:  refutedInput(),
			fields: &beads.AgentFields{PushFailed: true},
		},
		{
			name:   "no agent fields",
			status: &RecoveryStatus{Verdict: "SAFE_TO_NUKE"},
			input:  refutedInput(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pushFailedReconcileCandidate(tt.status, tt.input, tt.fields); got != tt.want {
				t.Fatalf("pushFailedReconcileCandidate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// End to end over DecideWorkstate: the reconcile writes false, and a LATER
// bead-only surface — one that runs no git and so can never refute anything —
// stops reporting idle-recovery-needed. That second surface is the one the
// operator reads, and it is why ignoring the flag at read time was not enough
// to get gastown/brahmin back into the pool (gt-uapr).
func TestReconciledPushFailedClearsTheBeadOnlySurface(t *testing.T) {
	beadOnly := func(pushFailed bool) polecat.WorkstateDisposition {
		return polecat.DecideWorkstate(polecat.WorkstateInput{
			State:         polecat.StateIdle,
			CleanupStatus: polecat.CleanupClean,
			Branch:        "polecat/brahmin/gt-bel1",
			PushFailed:    pushFailed,
			// A surface with no git cannot set this, which is the point.
			PushFailedRefuted:  false,
			ReuseFactsMeasured: false,
		})
	}

	if got := beadOnly(true).ReuseStatus; got != "idle-recovery-needed" {
		t.Fatalf("before reconcile reuse_status = %q, want idle-recovery-needed", got)
	}

	status := &RecoveryStatus{Verdict: "SAFE_TO_NUKE"}
	updater := &fakePushFailedUpdater{pushFailed: true}
	reconcilePushFailedIfRefuted(status, updater, "gt-gastown-polecat-brahmin", refutedInput(),
		&beads.AgentFields{PushFailed: true})
	if updater.pushFailed {
		t.Fatal("reconcile did not clear the flag")
	}

	if got := beadOnly(updater.pushFailed).ReuseStatus; got == "idle-recovery-needed" {
		t.Fatalf("after reconcile reuse_status = %q, want it off the recovery road", got)
	}
}

func hasDiagnostic(status *RecoveryStatus, want string) bool {
	for _, d := range status.Diagnostics {
		if strings.Contains(d, want) {
			return true
		}
	}
	return false
}
