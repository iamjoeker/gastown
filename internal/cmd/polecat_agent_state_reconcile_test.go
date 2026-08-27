package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

// fakeAgentStateUpdater is a write-and-read-back double, the same shape the
// cleanup reconcile's double has: the read-back returns whatever the last write
// stored, so a fake that "succeeds" without storing reproduces the case this
// reconcile has to catch — a write that reported success and did not land.
type fakeAgentStateUpdater struct {
	err         error
	readBackErr error
	skipStore   bool
	stored      string
	id          string
	wrote       string
	calls       int
	readBacks   int
}

func (f *fakeAgentStateUpdater) UpdateAgentDescriptionFields(id string, updates beads.AgentFieldUpdates) error {
	f.calls++
	f.id = id
	if updates.AgentState != nil {
		f.wrote = *updates.AgentState
	}
	if f.err != nil {
		return f.err
	}
	if !f.skipStore && updates.AgentState != nil {
		f.stored = *updates.AgentState
	}
	return nil
}

func (f *fakeAgentStateUpdater) GetAgentBead(id string) (*beads.Issue, *beads.AgentFields, error) {
	f.readBacks++
	if f.readBackErr != nil {
		return nil, nil, f.readBackErr
	}
	return &beads.Issue{ID: id}, &beads.AgentFields{AgentState: f.stored}, nil
}

// staleWorkingStatus is the RecoveryStatus a stranded polecat actually arrives
// with: no session, a missing cleanup_status, and the pre-repair NEEDS_RECOVERY
// verdict that a missing cleanup_status produces.
func staleWorkingStatus() *RecoveryStatus {
	return &RecoveryStatus{
		Rig:             "gastown",
		Polecat:         "chrome",
		SessionPresence: polecat.SessionAbsent,
		CleanupStatus:   "",
		Verdict:         "NEEDS_RECOVERY",
		NeedsRecovery:   true,
		Branch:          "polecat/nitro",
		Blockers:        []string{"cleanup_status=<missing>"},
	}
}

// staleWorkingInput is safeReconcileInput plus the one fact that makes the
// agent bead's claim of work in progress false: `tmux has-session` ran and found
// no session.
func staleWorkingInput() polecat.WorkstateInput {
	in := safeReconcileInput("")
	in.SessionPresence = polecat.SessionAbsent
	return in
}

func workingAgentFields() *beads.AgentFields {
	return &beads.AgentFields{AgentState: string(beads.AgentStateWorking)}
}

// TestReconcileAgentStateIfStale is the measured case: agent_state=working on a
// polecat whose session `tmux has-session` proved gone, with nothing else at
// risk. It is the state beads/capable, gastown/chrome and gastown/deathclaw were
// all in, and the state no verb anywhere could write (gt-xj5d).
func TestReconcileAgentStateIfStale(t *testing.T) {
	// The write path records a town-log audit line via workspace.FindFromCwd.
	// Run from a directory that is not a town so the test cannot append to a
	// live town's log.
	t.Chdir(t.TempDir())

	status := staleWorkingStatus()
	fields := workingAgentFields()
	updater := &fakeAgentStateUpdater{}

	reconcileAgentStateIfStale(status, updater, "gt-gastown-polecat-chrome",
		&polecat.Polecat{State: polecat.StateIdle}, fields, staleWorkingInput())

	if updater.calls != 1 {
		t.Fatalf("UpdateAgentDescriptionFields calls = %d, want 1", updater.calls)
	}
	if updater.id != "gt-gastown-polecat-chrome" || updater.wrote != string(beads.AgentStateIdle) {
		t.Fatalf("update = (%q, %q), want an idle write for the agent bead", updater.id, updater.wrote)
	}
	if updater.readBacks != 1 {
		t.Fatalf("GetAgentBead calls = %d, want 1 — the write must confirm itself", updater.readBacks)
	}
	// The bead this fix came from asks specifically that the STORED agent_state
	// be confirmed changed, not the command's rendered output believed.
	if updater.stored != string(beads.AgentStateIdle) {
		t.Fatalf("stored agent_state = %q, want idle", updater.stored)
	}
	// And the in-memory copy has to track it, because the cleanup reconcile
	// downstream reads this very field as its gate.
	if fields.AgentState != string(beads.AgentStateIdle) {
		t.Fatalf("fields.AgentState = %q, want idle", fields.AgentState)
	}
	if status.AgentState != string(beads.AgentStateIdle) || !status.Reconciled {
		t.Fatalf("status = (%q, reconciled=%v), want idle true", status.AgentState, status.Reconciled)
	}
	assertReconcileOutcome(t, status.Reconcile, "agent_state", reconcileActionWritten)
	if err := reconcileExitError(status.Reconcile); err != nil {
		t.Fatalf("reconcileExitError() = %v, want nil after a successful write", err)
	}
}

// TestReconcileAgentStateUnblocksTheCleanupRepair is the point of the whole
// change, held end to end. The two repairs are a chain — the cleanup repair is
// gated on agent_state=idle — so a run over a polecat carrying BOTH stale fields
// must come out with both rewritten. Testing the agent_state write alone would
// pass over a chain that still deadlocks one step later.
func TestReconcileAgentStateUnblocksTheCleanupRepair(t *testing.T) {
	t.Chdir(t.TempDir())

	status := staleWorkingStatus()
	fields := &beads.AgentFields{AgentState: string(beads.AgentStateWorking)}
	input := staleWorkingInput()
	p := &polecat.Polecat{State: polecat.StateIdle}

	reconcileAgentStateIfStale(status, &fakeAgentStateUpdater{}, "gt-gastown-polecat-chrome", p, fields, input)
	reconcileCleanupStatusIfSafe(status, &fakeCleanupUpdater{}, "gt-gastown-polecat-chrome", p, fields, input)

	assertReconcileOutcome(t, status.Reconcile, "agent_state", reconcileActionWritten)
	assertReconcileOutcome(t, status.Reconcile, "cleanup_status", reconcileActionWritten)
	if status.Verdict != "SAFE_TO_NUKE" || status.NeedsRecovery {
		t.Fatalf("verdict after both repairs = %q needs=%v, want SAFE_TO_NUKE false", status.Verdict, status.NeedsRecovery)
	}
	if err := reconcileExitError(status.Reconcile); err != nil {
		t.Fatalf("reconcileExitError() = %v, want nil when both repairs landed", err)
	}
}

// TestReconcileAgentStateIfStale_FailsClosed holds the write to the same bar the
// other two reconciles are held to: a repair that did not land must say so, must
// turn the verdict red, and must carry a non-zero exit.
func TestReconcileAgentStateIfStale_FailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		updater agentStateUpdater
		want    string
	}{
		{name: "write errors", updater: &fakeAgentStateUpdater{err: errors.New("bd update failed")}, want: "bd update failed"},
		{name: "read back errors", updater: &fakeAgentStateUpdater{readBackErr: errors.New("dolt unreachable")}, want: "could not be re-read"},
		// The shape the read-back exists for: bd returns nil and the row is
		// unchanged. Without it this reports a repair that never happened.
		{name: "write reports success and does not land", updater: &fakeAgentStateUpdater{skipStore: true}, want: "still reads <unset>"},
		{name: "no updater", updater: nil, want: "updater unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			status := staleWorkingStatus()
			reconcileAgentStateIfStale(status, tt.updater, "gt-gastown-polecat-chrome",
				&polecat.Polecat{State: polecat.StateIdle}, workingAgentFields(), staleWorkingInput())

			if status.Verdict != "NEEDS_RECOVERY" || !status.NeedsRecovery {
				t.Fatalf("failed update verdict = %q needs=%v, want NEEDS_RECOVERY true", status.Verdict, status.NeedsRecovery)
			}
			if !containsSubstring(status.Blockers, "agent_state_reconcile_failed") {
				t.Fatalf("blockers = %v, want agent_state_reconcile_failed", status.Blockers)
			}
			out := assertReconcileOutcome(t, status.Reconcile, "agent_state", reconcileActionFailed)
			if !strings.Contains(out.Detail, tt.want) {
				t.Fatalf("detail = %q, want it to contain %q", out.Detail, tt.want)
			}
			if err := reconcileExitError(status.Reconcile); err == nil {
				t.Fatal("reconcileExitError() = nil after a failed write, want an error")
			}
		})
	}
}

// TestAgentStateReconcilePlanRefusesWithAReason is the anti-silence test, and
// the guard the bead asks for by name: the verb must not widen into overriding
// the gates it sits behind. A verb that cleared both would be worse than one
// that cleared neither.
func TestAgentStateReconcilePlanRefusesWithAReason(t *testing.T) {
	tests := []struct {
		name       string
		status     *RecoveryStatus
		p          *polecat.Polecat
		fields     *beads.AgentFields
		input      polecat.WorkstateInput
		wantAction string
		wantDetail string
	}{
		{
			name:       "already idle is not a refusal",
			status:     staleWorkingStatus(),
			p:          &polecat.Polecat{State: polecat.StateIdle},
			fields:     &beads.AgentFields{AgentState: string(beads.AgentStateIdle)},
			input:      staleWorkingInput(),
			wantAction: reconcileActionNoChange,
			wantDetail: "already idle",
		},
		{
			// The line this reconcile must not cross. A pause is somebody's
			// decision; rewriting it here would discard it silently, which is
			// the failure clear-state exists to avoid rather than cause
			// (gt-fbgq).
			name:   "a deliberate pause routes to clear-state",
			status: staleWorkingStatus(),
			p:      &polecat.Polecat{State: polecat.StateIdle},
			fields: &beads.AgentFields{AgentState: string(beads.AgentStateStuck)},
			input:  staleWorkingInput(),
			// no_change, not refused: `refused` carries the non-zero exit, and a
			// state that was never this flag's business must not spend it.
			wantAction: reconcileActionNoChange,
			wantDetail: "gt polecat clear-state gastown/chrome",
		},
		{
			name:       "done is a resting state, not a stale claim",
			status:     staleWorkingStatus(),
			p:          &polecat.Polecat{State: polecat.StateIdle},
			fields:     &beads.AgentFields{AgentState: string(beads.AgentStateDone)},
			input:      staleWorkingInput(),
			wantAction: reconcileActionNoChange,
			wantDetail: "does not claim work in progress",
		},
		{
			// THE discriminator. An unmeasured session is not evidence the agent
			// is gone, and clearing the state on it would clear the state of a
			// polecat working right now (gt-9f67).
			name:   "an unknown session is not evidence of a dead one",
			status: staleWorkingStatus(),
			p:      &polecat.Polecat{State: polecat.StateIdle},
			fields: workingAgentFields(),
			input: func() polecat.WorkstateInput {
				in := staleWorkingInput()
				in.SessionPresence = polecat.SessionPresenceUnknown
				return in
			}(),
			wantAction: reconcileActionRefused,
			wantDetail: "MEASURED absent session",
		},
		{
			name:   "a present session refuses",
			status: staleWorkingStatus(),
			p:      &polecat.Polecat{State: polecat.StateIdle},
			fields: workingAgentFields(),
			input: func() polecat.WorkstateInput {
				in := staleWorkingInput()
				in.SessionPresence = polecat.SessionPresent
				return in
			}(),
			wantAction: reconcileActionRefused,
			wantDetail: "session_presence=present",
		},
		{
			name:       "a non-idle polecat refuses",
			status:     staleWorkingStatus(),
			p:          &polecat.Polecat{State: polecat.StateStalled},
			fields:     workingAgentFields(),
			input:      staleWorkingInput(),
			wantAction: reconcileActionRefused,
			wantDetail: "polecat_state=stalled",
		},
		{
			// Gate 3 of the chain, and the one the bead is explicit about: this
			// verb must still refuse a polecat with genuine uncommitted work.
			// All three stranded polecats had some — 4, 4 and 1 files.
			name:   "uncommitted work still refuses",
			status: staleWorkingStatus(),
			p:      &polecat.Polecat{State: polecat.StateIdle},
			fields: workingAgentFields(),
			input: func() polecat.WorkstateInput {
				in := staleWorkingInput()
				in.GitDirty = true
				return in
			}(),
			wantAction: reconcileActionRefused,
			wantDetail: "git_state=has_uncommitted",
		},
		{
			name:   "work still on the hook refuses",
			status: staleWorkingStatus(),
			p:      &polecat.Polecat{State: polecat.StateIdle},
			fields: workingAgentFields(),
			input: func() polecat.WorkstateInput {
				in := staleWorkingInput()
				in.HookBead = "gt-ibtb"
				return in
			}(),
			wantAction: reconcileActionRefused,
			wantDetail: "gt-ibtb",
		},
		{
			name:   "unsubmitted work refuses",
			status: staleWorkingStatus(),
			p:      &polecat.Polecat{State: polecat.StateIdle},
			fields: workingAgentFields(),
			input: func() polecat.WorkstateInput {
				in := staleWorkingInput()
				in.MRSubmitted = false
				return in
			}(),
			wantAction: reconcileActionRefused,
			wantDetail: "mq_status=not_submitted",
		},
		{
			name:   "a branch with no merge-queue check refuses",
			status: staleWorkingStatus(),
			p:      &polecat.Polecat{State: polecat.StateIdle},
			fields: workingAgentFields(),
			input: func() polecat.WorkstateInput {
				in := staleWorkingInput()
				in.MQCheckRequired = false
				return in
			}(),
			wantAction: reconcileActionRefused,
			wantDetail: "no merge-queue check was run",
		},
		{
			name: "an earlier reconcile that did not land is not compounded",
			status: func() *RecoveryStatus {
				s := staleWorkingStatus()
				s.Reconcile = []ReconcileOutcome{{Field: "push_failed", Action: reconcileActionRefused, Detail: "d"}}
				return s
			}(),
			p:          &polecat.Polecat{State: polecat.StateIdle},
			fields:     workingAgentFields(),
			input:      staleWorkingInput(),
			wantAction: reconcileActionRefused,
			wantDetail: "not compounding it",
		},
		{
			name:       "no facts at all",
			status:     nil,
			p:          nil,
			fields:     nil,
			input:      polecat.WorkstateInput{},
			wantAction: reconcileActionRefused,
			wantDetail: "never read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, out, ok := agentStateReconcilePlan(tt.status, tt.p, tt.fields, tt.input)
			if ok {
				t.Fatal("agentStateReconcilePlan() allowed an unsafe rewrite")
			}
			if out.Action != tt.wantAction {
				t.Fatalf("action = %q, want %q (detail: %s)", out.Action, tt.wantAction, out.Detail)
			}
			// A refusal with an empty detail is the silent no-op wearing a
			// struct — the defect gt-hm0v was filed about, one field over.
			if strings.TrimSpace(out.Detail) == "" {
				t.Fatal("refusal carried no detail — this is the silent no-op (gt-hm0v)")
			}
			if !strings.Contains(out.Detail, tt.wantDetail) {
				t.Fatalf("detail = %q, want it to contain %q", out.Detail, tt.wantDetail)
			}
			if out.Field != "agent_state" {
				t.Fatalf("field = %q, want agent_state", out.Field)
			}
			if out.Previous == "" {
				t.Fatal("previous was blank — a missing agent_state must render as <unset>, not as nothing")
			}
		})
	}
}

// TestReconcileAgentStateNeverSilent holds the property over every road at once:
// the reconcile may write, decline, or fail, but it may never come back with
// nothing to say. Exit 0 over an unwritten field is the whole defect family.
func TestReconcileAgentStateNeverSilent(t *testing.T) {
	roads := []struct {
		name    string
		p       *polecat.Polecat
		fields  *beads.AgentFields
		input   polecat.WorkstateInput
		updater agentStateUpdater
	}{
		{name: "writes", p: &polecat.Polecat{State: polecat.StateIdle}, fields: workingAgentFields(), input: staleWorkingInput(), updater: &fakeAgentStateUpdater{}},
		{name: "already idle", p: &polecat.Polecat{State: polecat.StateIdle}, fields: &beads.AgentFields{AgentState: string(beads.AgentStateIdle)}, input: staleWorkingInput(), updater: &fakeAgentStateUpdater{}},
		{name: "refuses", p: &polecat.Polecat{State: polecat.StateWorking}, fields: workingAgentFields(), input: staleWorkingInput(), updater: &fakeAgentStateUpdater{}},
		{name: "fails", p: &polecat.Polecat{State: polecat.StateIdle}, fields: workingAgentFields(), input: staleWorkingInput(), updater: &fakeAgentStateUpdater{err: errors.New("boom")}},
		{name: "no updater", p: &polecat.Polecat{State: polecat.StateIdle}, fields: workingAgentFields(), input: staleWorkingInput(), updater: nil},
	}

	for _, road := range roads {
		t.Run(road.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			status := staleWorkingStatus()
			reconcileAgentStateIfStale(status, road.updater, "gt-gastown-polecat-chrome", road.p, road.fields, road.input)
			if len(status.Reconcile) != 1 {
				t.Fatalf("outcomes = %d, want exactly 1 — every road must report (gt-hm0v)", len(status.Reconcile))
			}
			out := status.Reconcile[0]
			if out.Action == "" || strings.TrimSpace(out.Detail) == "" {
				t.Fatalf("outcome = %+v, want a named action and a detail on every road", out)
			}
			if out.Field != "agent_state" {
				t.Fatalf("field = %q, want agent_state", out.Field)
			}
		})
	}
}

// TestReconcileAgentStateKeepsHealthyRunsAtExitZero is the regression this
// reconcile nearly introduced, and it was caught by running the built binary
// against the live town rather than by any unit test here.
//
// `refused` is what turns --reconcile-cleanup's exit non-zero. Almost every
// polecat in a town rests at agent_state=done or at a deliberate pause, and the
// witness runs this flag on every slot that opens — so reporting `refused` for a
// state that was never this flag's business made a healthy run exit 1. A
// non-zero that fires on the ordinary case cannot be read as a signal on the
// exceptional one, which is the same defect as the silent exit 0 that gt-hm0v
// removed, pointed the other way.
func TestReconcileAgentStateKeepsHealthyRunsAtExitZero(t *testing.T) {
	for _, state := range []beads.AgentState{
		beads.AgentStateDone,
		beads.AgentStateIdle,
		beads.AgentStateNuked,
		beads.AgentStateStuck,
		beads.AgentStateAwaitingGate,
		beads.AgentStateEscalated,
	} {
		t.Run(string(state), func(t *testing.T) {
			t.Chdir(t.TempDir())
			status := staleWorkingStatus()
			status.CleanupStatus = polecat.CleanupClean
			status.Verdict = "SAFE_TO_NUKE"
			status.NeedsRecovery = false
			status.Blockers = nil
			fields := &beads.AgentFields{
				AgentState:    string(state),
				CleanupStatus: string(polecat.CleanupClean),
			}
			input := staleWorkingInput()
			input.CleanupStatus = polecat.CleanupClean
			p := &polecat.Polecat{State: polecat.StateIdle}

			reconcileAgentStateIfStale(status, &fakeAgentStateUpdater{}, "gt-gastown-polecat-nitro", p, fields, input)
			reconcileCleanupStatusIfSafe(status, &fakeCleanupUpdater{}, "gt-gastown-polecat-nitro", p, fields, input)

			if err := reconcileExitError(status.Reconcile); err != nil {
				t.Fatalf("reconcileExitError() = %v, want nil — a healthy polecat must not spend the non-zero exit", err)
			}
			// And it must still SAY something about the field, on every road.
			out := assertReconcileOutcome(t, status.Reconcile, "agent_state", reconcileActionNoChange)
			if strings.TrimSpace(out.Detail) == "" {
				t.Fatal("no_change carried no detail — silence is the other half of the defect")
			}
		})
	}
}

// TestNotPausedMessageNamesTheVerbThatApplies covers the sentence three stranded
// polecats got at exit 0. It is true and it reads as "nothing is wrong here",
// which is how a reader concludes no verb exists and reaches for nuke.
func TestNotPausedMessageNamesTheVerbThatApplies(t *testing.T) {
	working := notPausedMessage("gastown", "chrome", string(beads.AgentStateWorking))
	if !strings.Contains(working, "agent_state=working is not a paused state") {
		t.Fatalf("message = %q, want it to keep saying what it did not do", working)
	}
	if !strings.Contains(working, "gt polecat check-recovery gastown/chrome --reconcile-cleanup") {
		t.Fatalf("message = %q, want it to name the command that repairs a stale working", working)
	}

	// A state that is neither a pause nor a claim of activity has no second
	// verb to name, and inventing one would send the reader somewhere that
	// refuses. The message stays exactly as it was.
	done := notPausedMessage("gastown", "chrome", string(beads.AgentStateDone))
	if strings.Contains(done, "check-recovery") {
		t.Fatalf("message = %q, want no repair pointer for a resting state", done)
	}
	if !strings.Contains(done, "agent_state=done is not a paused state") {
		t.Fatalf("message = %q, want the original sentence", done)
	}

	// An empty agent_state must not render as a blank after the equals sign.
	unset := notPausedMessage("gastown", "chrome", "")
	if !strings.Contains(unset, "<empty>") {
		t.Fatalf("message = %q, want an empty agent_state to render visibly", unset)
	}
}
