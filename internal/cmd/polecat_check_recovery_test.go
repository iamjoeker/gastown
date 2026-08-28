package cmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

type fakeIssueShower struct {
	issue *beads.Issue
	err   error
}

func (f fakeIssueShower) Show(issueID string) (*beads.Issue, error) {
	return f.issue, f.err
}

// fakeCleanupUpdater is a write-and-read-back double. The read-back returns
// whatever the last write stored, so a fake that "succeeds" without storing
// (readBackErr, or storing nothing) reproduces the shape this reconcile has to
// catch: a write that reported success and did not land.
type fakeCleanupUpdater struct {
	err         error
	readBackErr error
	// skipStore makes the write return nil without changing what the read-back
	// sees — the "write reported success and did not land" shape.
	skipStore bool
	stored    string
	id        string
	status    string
	calls     int
	readBacks int
}

func (f *fakeCleanupUpdater) UpdateAgentCleanupStatus(id string, cleanupStatus string) error {
	f.calls++
	f.id = id
	f.status = cleanupStatus
	if f.err != nil {
		return f.err
	}
	if !f.skipStore {
		f.stored = cleanupStatus
	}
	return nil
}

func (f *fakeCleanupUpdater) GetAgentBead(id string) (*beads.Issue, *beads.AgentFields, error) {
	f.readBacks++
	if f.readBackErr != nil {
		return nil, nil, f.readBackErr
	}
	return &beads.Issue{ID: id}, &beads.AgentFields{CleanupStatus: f.stored}, nil
}

type fakeActiveMRRemovalChecker struct {
	activeMR string
	blocker  string
	calls    int
	name     string
}

func (f *fakeActiveMRRemovalChecker) ActiveMRRemovalBlocker(name string) (string, string) {
	f.calls++
	f.name = name
	return f.activeMR, f.blocker
}

type fakeIssueMapShower struct {
	issues map[string]*beads.Issue
	errs   map[string]error
}

func (f fakeIssueMapShower) Show(issueID string) (*beads.Issue, error) {
	if err := f.errs[issueID]; err != nil {
		return nil, err
	}
	issue, ok := f.issues[issueID]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

func TestCheckNukeActiveMRSafety(t *testing.T) {
	checker := &fakeActiveMRRemovalChecker{activeMR: "gt-mr", blocker: "active_mr=gt-mr status=in_progress"}
	err := checkNukeActiveMRSafety(checker, "toast", "gastown", false)
	if err == nil {
		t.Fatal("checkNukeActiveMRSafety() error = nil, want pending MR blocker")
	}
	if checker.calls != 1 || checker.name != "toast" {
		t.Fatalf("checker calls = %d name = %q, want one call for toast", checker.calls, checker.name)
	}
	for _, want := range []string{"gastown/toast", "gt-mr", "status=in_progress", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}

	checker.calls = 0
	if err := checkNukeActiveMRSafety(checker, "toast", "gastown", true); err != nil {
		t.Fatalf("forced checkNukeActiveMRSafety() error = %v, want nil", err)
	}
	if checker.calls != 0 {
		t.Fatalf("forced check called blocker %d times, want 0", checker.calls)
	}

	lookupErrorChecker := &fakeActiveMRRemovalChecker{activeMR: "<unknown>", blocker: "agent_lookup_error: bd exploded"}
	err = checkNukeActiveMRSafety(lookupErrorChecker, "toast", "gastown", false)
	if err == nil || !strings.Contains(err.Error(), "agent_lookup_error") {
		t.Fatalf("lookup-error check = %v, want fail-closed agent_lookup_error", err)
	}
}

func TestIsMQNotRequiredSource(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
		err   error
		want  bool
	}{
		{
			name:  "no merge source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{NoMerge: true})},
			want:  true,
		},
		{
			name:  "review only source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{ReviewOnly: true})},
			want:  true,
		},
		{
			name:  "local merge strategy source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{MergeStrategy: "local"})},
			want:  true,
		},
		{
			name:  "normal merge queue source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{MergeStrategy: "mr"})},
			want:  false,
		},
		{
			name: "missing source is conservative",
			err:  beads.ErrNotFound,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMQNotRequiredSource(fakeIssueShower{issue: tt.issue, err: tt.err}, "gt-test")
			if got != tt.want {
				t.Errorf("isMQNotRequiredSource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanupStatusBlocker(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{status: "clean", want: ""},
		{status: "has_unpushed", want: "cleanup_status=has_unpushed"},
		{status: "unknown", want: "cleanup_status=unknown"},
		{status: "", want: "cleanup_status=<missing>"},
		{status: "weird", want: "cleanup_status=weird"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := cleanupStatusBlocker(polecat.CleanupStatus(tt.status))
			if got != tt.want {
				t.Errorf("cleanupStatusBlocker(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestCleanupStatusBlockerForRecovery_PartialSpawnWithoutHook(t *testing.T) {
	tests := []struct {
		name         string
		status       polecat.CleanupStatus
		partialSpawn bool
		want         string
	}{
		{name: "missing cleanup is safe for partial spawn", partialSpawn: true, want: ""},
		{name: "unknown cleanup is safe for partial spawn", status: polecat.CleanupUnknown, partialSpawn: true, want: ""},
		{name: "dirty cleanup still blocks partial spawn", status: polecat.CleanupUnpushed, partialSpawn: true, want: "cleanup_status=has_unpushed"},
		{name: "missing cleanup still blocks ordinary polecat", partialSpawn: false, want: "cleanup_status=<missing>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanupStatusBlockerForRecovery(tt.status, tt.partialSpawn)
			if got != tt.want {
				t.Errorf("cleanupStatusBlockerForRecovery() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStaleCleanupStatusCanBeIgnoredForRecovery(t *testing.T) {
	tests := []struct {
		name         string
		status       polecat.CleanupStatus
		workTerminal bool
		hookSafe     bool
		activeMRSafe bool
		gitSafe      bool
		wantCanSkip  bool
	}{
		{
			name:         "closed source with clean git ignores stale unpushed cleanup",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
			wantCanSkip:  true,
		},
		{
			name:         "open source still blocks",
			status:       polecat.CleanupUnpushed,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
		},
		{
			name:         "hooked work still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			activeMRSafe: true,
			gitSafe:      true,
		},
		{
			name:         "active MR still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			gitSafe:      true,
		},
		{
			name:         "dirty git still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
		},
		{
			name:         "git error still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
		},
		{
			name:         "closed source with clean git ignores stale stash cleanup",
			status:       polecat.CleanupStash,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
			wantCanSkip:  true,
		},
		{
			name:         "closed source with clean git ignores stale uncommitted cleanup",
			status:       polecat.CleanupUncommitted,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
			wantCanSkip:  true,
		},
		{
			name:         "unknown cleanup still blocks",
			status:       polecat.CleanupUnknown,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
		},
		{
			name:         "terminal hook can satisfy work terminal predicate",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitSafe:      true,
			wantCanSkip:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := polecat.CanIgnoreStaleCleanupStatus(tt.status, tt.workTerminal, tt.hookSafe, tt.activeMRSafe, tt.gitSafe)
			if got != tt.wantCanSkip {
				t.Fatalf("CanIgnoreStaleCleanupStatus() = %v, want %v", got, tt.wantCanSkip)
			}
		})
	}
}

// safeReconcileInput is the WorkstateInput of a polecat whose every predicate
// OTHER than cleanup_status has been measured safe: idle, no hook, clean tree,
// and a branch the merge-queue check found already submitted.
//
// It is the state gt-hm0v is about. The reconcile is asked its question here
// and nowhere else that matters — cleanup_status is the last thing standing,
// which is exactly when the flag has to act and exactly when it did not.
func safeReconcileInput(previous polecat.CleanupStatus) polecat.WorkstateInput {
	return polecat.WorkstateInput{
		State:              polecat.StateIdle,
		CleanupStatus:      previous,
		Branch:             "polecat/nitro",
		ReuseFactsMeasured: true,
		MQCheckRequired:    true,
		HasSubmittableWork: true,
		MRSubmitted:        true,
	}
}

func safeReconcileStatus(previous polecat.CleanupStatus) *RecoveryStatus {
	// The verdict a polecat in this state actually arrives with: the stale
	// cleanup_status is a blocker, so the pre-repair verdict is NEEDS_RECOVERY
	// and the merge-queue tail was never reached, leaving MQStatus empty.
	return &RecoveryStatus{
		CleanupStatus: previous,
		Verdict:       "NEEDS_RECOVERY",
		NeedsRecovery: true,
		Branch:        "polecat/nitro",
		Blockers:      []string{cleanupStatusBlocker(previous)},
	}
}

func idleAgentFields(previous polecat.CleanupStatus) *beads.AgentFields {
	return &beads.AgentFields{
		AgentState:    string(beads.AgentStateIdle),
		CleanupStatus: string(previous),
	}
}

// TestReconcileCleanupStatusIfSafe covers the missing value alongside the dirty
// ones. "" is listed FIRST because it is the one the flag skipped by name: every
// polecat this bug stranded carried cleanup_status=<missing>, and the one flag
// that exists to clear that blocker excluded it before any predicate ran
// (gt-hm0v).
func TestReconcileCleanupStatusIfSafe(t *testing.T) {
	for _, previous := range []polecat.CleanupStatus{"", polecat.CleanupUnknown, polecat.CleanupUnpushed, polecat.CleanupStash, polecat.CleanupUncommitted} {
		t.Run(cleanupStatusLabel(previous), func(t *testing.T) {
			status := safeReconcileStatus(previous)
			fields := idleAgentFields(previous)
			updater := &fakeCleanupUpdater{}
			reconcileCleanupStatusIfSafe(status, updater, "gt-gastown-polecat-nitro", &polecat.Polecat{State: polecat.StateIdle}, fields, safeReconcileInput(previous))

			if updater.calls != 1 {
				t.Fatalf("UpdateAgentCleanupStatus calls = %d, want 1", updater.calls)
			}
			if updater.id != "gt-gastown-polecat-nitro" || updater.status != string(polecat.CleanupClean) {
				t.Fatalf("update = (%q, %q), want clean update for agent", updater.id, updater.status)
			}
			if updater.readBacks != 1 {
				t.Fatalf("GetAgentBead calls = %d, want 1 — the write must confirm itself", updater.readBacks)
			}
			if status.CleanupStatus != polecat.CleanupClean || !status.Reconciled {
				t.Fatalf("status after reconcile = (%q, reconciled=%v), want clean true", status.CleanupStatus, status.Reconciled)
			}
			// The verdict has to move with the field. A caller that reads
			// NEEDS_RECOVERY off a run that just repaired the only blocker is
			// back where it started, and the witness's slot-open check reads
			// exactly that field.
			if status.Verdict != "SAFE_TO_NUKE" || status.NeedsRecovery {
				t.Fatalf("verdict after reconcile = %q needs=%v, want SAFE_TO_NUKE false", status.Verdict, status.NeedsRecovery)
			}
			if len(status.Blockers) != 0 {
				t.Fatalf("blockers after reconcile = %v, want none", status.Blockers)
			}
			// The in-memory fields must track the store they just changed, or
			// anything re-reading them describes a polecat that no longer exists.
			if fields.CleanupStatus != string(polecat.CleanupClean) {
				t.Fatalf("fields.CleanupStatus = %q, want clean", fields.CleanupStatus)
			}
			assertReconcileOutcome(t, status.Reconcile, "cleanup_status", reconcileActionWritten)
			if err := reconcileExitError(status.Reconcile); err != nil {
				t.Fatalf("reconcileExitError() = %v, want nil after a successful write", err)
			}
		})
	}
}

func TestReconcileCleanupStatusIfSafe_FailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		updater *fakeCleanupUpdater
		want    string
	}{
		{name: "write errors", updater: &fakeCleanupUpdater{err: errors.New("bd update failed")}, want: "bd update failed"},
		{name: "read back errors", updater: &fakeCleanupUpdater{readBackErr: errors.New("dolt unreachable")}, want: "could not be re-read"},
		// The shape the read-back exists for: bd returns nil and the row is
		// unchanged. Without the read-back this reports a repair that did not
		// happen — the same silent-success family as the bug itself.
		{name: "write reports success and does not land", updater: &fakeCleanupUpdater{skipStore: true}, want: "still reads <missing>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := safeReconcileStatus("")
			reconcileCleanupStatusIfSafe(status, tt.updater, "gt-gastown-polecat-nitro", &polecat.Polecat{State: polecat.StateIdle}, idleAgentFields(""), safeReconcileInput(""))

			if status.Verdict != "NEEDS_RECOVERY" || !status.NeedsRecovery {
				t.Fatalf("failed update verdict = %q needs=%v, want NEEDS_RECOVERY true", status.Verdict, status.NeedsRecovery)
			}
			if !containsSubstring(status.Blockers, "cleanup_reconcile_failed") {
				t.Fatalf("blockers = %v, want cleanup_reconcile_failed", status.Blockers)
			}
			out := assertReconcileOutcome(t, status.Reconcile, "cleanup_status", reconcileActionFailed)
			if !strings.Contains(out.Detail, tt.want) {
				t.Fatalf("detail = %q, want it to contain %q", out.Detail, tt.want)
			}
			// A failed repair must reach the caller as a non-zero exit. Exit 0
			// over an unwritten field is the defect (gt-hm0v).
			if err := reconcileExitError(status.Reconcile); err == nil {
				t.Fatal("reconcileExitError() = nil after a failed write, want an error")
			}
		})
	}
}

// TestCleanupStatusReconcilePlanRefusesWithAReason is the anti-silence test.
// Every refusal must name the predicate that stopped it: the old candidate
// function returned a bare false on each of these roads, and the command then
// exited 0 having said nothing about the action it was asked to perform.
func TestCleanupStatusReconcilePlanRefusesWithAReason(t *testing.T) {
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
			name:       "already clean is not a refusal",
			status:     safeReconcileStatus(polecat.CleanupClean),
			p:          &polecat.Polecat{State: polecat.StateIdle},
			fields:     idleAgentFields(polecat.CleanupClean),
			input:      safeReconcileInput(polecat.CleanupClean),
			wantAction: reconcileActionNoChange,
			wantDetail: "already clean",
		},
		{
			name:       "working polecat blocks",
			status:     safeReconcileStatus(polecat.CleanupUnpushed),
			p:          &polecat.Polecat{State: polecat.StateWorking},
			fields:     idleAgentFields(polecat.CleanupUnpushed),
			input:      safeReconcileInput(polecat.CleanupUnpushed),
			wantAction: reconcileActionRefused,
			wantDetail: "polecat_state=working",
		},
		{
			name:       "working agent bead blocks",
			status:     safeReconcileStatus(polecat.CleanupUnpushed),
			p:          &polecat.Polecat{State: polecat.StateIdle},
			fields:     &beads.AgentFields{AgentState: string(beads.AgentStateWorking), CleanupStatus: string(polecat.CleanupUnpushed)},
			input:      safeReconcileInput(polecat.CleanupUnpushed),
			wantAction: reconcileActionRefused,
			wantDetail: "agent_state=working",
		},
		{
			// The distinction the fix turns on: a blocker that is NOT
			// cleanup_status survives the repair, so the repair is refused —
			// and the refusal names the survivor rather than going quiet.
			name:   "a blocker other than cleanup_status still refuses",
			status: safeReconcileStatus(polecat.CleanupUnpushed),
			p:      &polecat.Polecat{State: polecat.StateIdle},
			fields: idleAgentFields(polecat.CleanupUnpushed),
			input: func() polecat.WorkstateInput {
				in := safeReconcileInput(polecat.CleanupUnpushed)
				in.GitDirty = true
				return in
			}(),
			wantAction: reconcileActionRefused,
			wantDetail: "git_state=has_uncommitted",
		},
		{
			name:   "unknown mq blocks",
			status: safeReconcileStatus(polecat.CleanupUnpushed),
			p:      &polecat.Polecat{State: polecat.StateIdle},
			fields: idleAgentFields(polecat.CleanupUnpushed),
			input: func() polecat.WorkstateInput {
				in := safeReconcileInput(polecat.CleanupUnpushed)
				in.MQLookupFailed = true
				return in
			}(),
			wantAction: reconcileActionRefused,
			wantDetail: "mq_status=unknown",
		},
		{
			// A branch nobody ran the queue check for. The zeros of a surface
			// too cheap to look are the zeros of a branch with nothing to
			// submit, and only the caller knows which it was (gt-49dp).
			name:   "a branch with no merge-queue check blocks",
			status: safeReconcileStatus(polecat.CleanupUnpushed),
			p:      &polecat.Polecat{State: polecat.StateIdle},
			fields: idleAgentFields(polecat.CleanupUnpushed),
			input: func() polecat.WorkstateInput {
				in := safeReconcileInput(polecat.CleanupUnpushed)
				in.MQCheckRequired = false
				return in
			}(),
			wantAction: reconcileActionRefused,
			wantDetail: "no merge-queue check was run",
		},
		{
			name:       "no facts at all",
			status:     nil,
			p:          nil,
			fields:     nil,
			input:      polecat.WorkstateInput{},
			wantAction: reconcileActionRefused,
			wantDetail: "no agent bead",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, out, ok := cleanupStatusReconcilePlan(tt.status, tt.p, tt.fields, tt.input)
			if ok {
				t.Fatal("cleanupStatusReconcilePlan() allowed unsafe reconciliation")
			}
			if out.Action != tt.wantAction {
				t.Fatalf("action = %q, want %q", out.Action, tt.wantAction)
			}
			// The whole point. A refusal with an empty detail is the silent
			// no-op wearing a struct.
			if strings.TrimSpace(out.Detail) == "" {
				t.Fatal("refusal carried no detail — this is the silent no-op (gt-hm0v)")
			}
			if !strings.Contains(out.Detail, tt.wantDetail) {
				t.Fatalf("detail = %q, want it to contain %q", out.Detail, tt.wantDetail)
			}
			if out.Field != "cleanup_status" {
				t.Fatalf("field = %q, want cleanup_status", out.Field)
			}
			if out.Previous == "" {
				t.Fatal("previous was blank — a missing cleanup_status must render as <missing>, not as nothing")
			}
		})
	}
}

// TestReconcileCleanupStatusNeverSilent is the property the bead asks for, held
// over every road at once: the flag may write, decline, or fail, but it may
// never come back with nothing to say.
func TestReconcileCleanupStatusNeverSilent(t *testing.T) {
	roads := []struct {
		name    string
		status  *RecoveryStatus
		p       *polecat.Polecat
		fields  *beads.AgentFields
		input   polecat.WorkstateInput
		updater cleanupStatusUpdater
	}{
		{name: "writes", status: safeReconcileStatus(""), p: &polecat.Polecat{State: polecat.StateIdle}, fields: idleAgentFields(""), input: safeReconcileInput(""), updater: &fakeCleanupUpdater{}},
		{name: "already clean", status: safeReconcileStatus(polecat.CleanupClean), p: &polecat.Polecat{State: polecat.StateIdle}, fields: idleAgentFields(polecat.CleanupClean), input: safeReconcileInput(polecat.CleanupClean), updater: &fakeCleanupUpdater{}},
		{name: "refuses", status: safeReconcileStatus(""), p: &polecat.Polecat{State: polecat.StateWorking}, fields: idleAgentFields(""), input: safeReconcileInput(""), updater: &fakeCleanupUpdater{}},
		{name: "fails", status: safeReconcileStatus(""), p: &polecat.Polecat{State: polecat.StateIdle}, fields: idleAgentFields(""), input: safeReconcileInput(""), updater: &fakeCleanupUpdater{err: errors.New("boom")}},
		{name: "no updater", status: safeReconcileStatus(""), p: &polecat.Polecat{State: polecat.StateIdle}, fields: idleAgentFields(""), input: safeReconcileInput(""), updater: nil},
	}

	for _, road := range roads {
		t.Run(road.name, func(t *testing.T) {
			reconcileCleanupStatusIfSafe(road.status, road.updater, "gt-gastown-polecat-nitro", road.p, road.fields, road.input)
			if len(road.status.Reconcile) != 1 {
				t.Fatalf("outcomes = %d, want exactly 1 — every road must report (gt-hm0v)", len(road.status.Reconcile))
			}
			out := road.status.Reconcile[0]
			if out.Action == "" || strings.TrimSpace(out.Detail) == "" {
				t.Fatalf("outcome = %+v, want a named action and a detail on every road", out)
			}

			// And the human surface must render it. An operator reading an
			// ordinary refusal report with no mention of the repair concludes
			// "the safe path did not apply here" — which is the reasoning that
			// reaches for nuke.
			var buf bytes.Buffer
			printReconcileOutcomes(&buf, true, road.status.Reconcile)
			if !strings.Contains(buf.String(), "Reconcile:") || !strings.Contains(buf.String(), out.Detail) {
				t.Fatalf("printed output = %q, want a Reconcile section naming the detail", buf.String())
			}
		})
	}
}

// TestPrintReconcileOutcomesSilentWithoutTheFlag keeps the report scoped to
// runs that asked for it: a plain `check-recovery` must look exactly as it did.
func TestPrintReconcileOutcomesSilentWithoutTheFlag(t *testing.T) {
	var buf bytes.Buffer
	printReconcileOutcomes(&buf, false, []ReconcileOutcome{{Field: "cleanup_status", Action: reconcileActionWritten, Detail: "d"}})
	if buf.Len() != 0 {
		t.Fatalf("printed %q without --reconcile-cleanup, want nothing", buf.String())
	}
}

// TestPushFailedReconcilePlanNamesItsRefusals holds the second reconcile to the
// same bar. It shares the defect and it shares the fix.
func TestPushFailedReconcilePlanNamesItsRefusals(t *testing.T) {
	tests := []struct {
		name       string
		status     *RecoveryStatus
		input      polecat.WorkstateInput
		fields     *beads.AgentFields
		wantAction string
		wantDetail string
	}{
		{
			name:       "not set",
			status:     &RecoveryStatus{Verdict: "SAFE_TO_NUKE"},
			fields:     &beads.AgentFields{},
			wantAction: reconcileActionNoChange,
			wantDetail: "not set",
		},
		{
			name:       "set but unrefuted",
			status:     &RecoveryStatus{Verdict: "SAFE_TO_NUKE"},
			fields:     &beads.AgentFields{PushFailed: true},
			wantAction: reconcileActionRefused,
			wantDetail: "did not refute it",
		},
		{
			name:       "refuted but the verdict blocks",
			status:     &RecoveryStatus{Verdict: "NEEDS_RECOVERY", Blockers: []string{"has work on hook (gt-abc)"}},
			input:      polecat.WorkstateInput{PushFailedRefuted: true},
			fields:     &beads.AgentFields{PushFailed: true},
			wantAction: reconcileActionRefused,
			wantDetail: "has work on hook (gt-abc)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, ok := pushFailedReconcilePlan(tt.status, tt.input, tt.fields)
			if ok {
				t.Fatal("pushFailedReconcilePlan() allowed the write")
			}
			if out.Action != tt.wantAction {
				t.Fatalf("action = %q, want %q", out.Action, tt.wantAction)
			}
			if !strings.Contains(out.Detail, tt.wantDetail) {
				t.Fatalf("detail = %q, want it to contain %q", out.Detail, tt.wantDetail)
			}
			// pushFailedReconcileCandidate must keep answering exactly as the
			// plan does, or the SAFE_TO_NUKE arm's repair hint drifts from the
			// repair itself.
			if pushFailedReconcileCandidate(tt.status, tt.input, tt.fields) {
				t.Fatal("pushFailedReconcileCandidate() disagreed with pushFailedReconcilePlan()")
			}
		})
	}
}

// TestReconcileExitErrorSeparatesRefusedFromDone is the caller-facing half: a
// caller cannot tell "refused" from "done" by reading prose, so the exit status
// has to carry it. Exit 0 over an unwritten field is the bug.
func TestReconcileExitErrorSeparatesRefusedFromDone(t *testing.T) {
	done := []ReconcileOutcome{
		{Field: "push_failed", Action: reconcileActionNoChange, Detail: "not set"},
		{Field: "cleanup_status", Action: reconcileActionWritten, Detail: "rewritten"},
	}
	if err := reconcileExitError(done); err != nil {
		t.Fatalf("reconcileExitError(done) = %v, want nil", err)
	}

	refused := append(append([]ReconcileOutcome{}, done...),
		ReconcileOutcome{Field: "cleanup_status", Action: reconcileActionRefused, Detail: "git_state=has_uncommitted"})
	err := reconcileExitError(refused)
	if err == nil {
		t.Fatal("reconcileExitError(refused) = nil, want an error")
	}
	// The error has to name the predicate, not merely fail. "It declined" with
	// no reason is as unactionable as silence.
	if !strings.Contains(err.Error(), "git_state=has_uncommitted") {
		t.Fatalf("error = %q, want it to name the predicate", err)
	}
}

func assertReconcileOutcome(t *testing.T, outcomes []ReconcileOutcome, field, action string) ReconcileOutcome {
	t.Helper()
	for _, o := range outcomes {
		if o.Field == field {
			if o.Action != action {
				t.Fatalf("%s action = %q, want %q (detail: %s)", field, o.Action, action, o.Detail)
			}
			return o
		}
	}
	t.Fatalf("no outcome reported for %s; got %+v", field, outcomes)
	return ReconcileOutcome{}
}

func TestAssessHookBead(t *testing.T) {
	const assignee = "gastown/polecats/synth"

	tests := []struct {
		name           string
		hookBead       string
		bd             issueShower
		wantSafe       bool
		wantTerminal   bool
		wantUnverified bool
		wantBlocker    string
		wantDiagnostic string
	}{
		{name: "empty hook", wantSafe: true},
		{
			name:           "terminal hook",
			hookBead:       "gt-work",
			bd:             fakeIssueShower{issue: &beads.Issue{Status: "closed"}},
			wantSafe:       true,
			wantTerminal:   true,
			wantDiagnostic: "hook=released",
		},
		{
			name:        "hooked bead held by this polecat blocks",
			hookBead:    "gt-work",
			bd:          fakeIssueShower{issue: &beads.Issue{Status: "hooked", Assignee: assignee}},
			wantBlocker: "hook_bead=gt-work status=hooked",
		},
		{
			name:        "in_progress bead held by this polecat blocks",
			hookBead:    "gt-work",
			bd:          fakeIssueShower{issue: &beads.Issue{Status: "in_progress", Assignee: assignee}},
			wantBlocker: "hook_bead=gt-work status=in_progress",
		},
		{
			// The normalized address form mail writes must still count as held,
			// or a real hook silently stops blocking.
			name:        "normalized assignee form still counts as held",
			hookBead:    "gt-work",
			bd:          fakeIssueShower{issue: &beads.Issue{Status: "hooked", Assignee: "gastown/synth"}},
			wantBlocker: "hook_bead=gt-work status=hooked",
		},
		{
			// The reopen-to-unstrand shape, and what gt unsling writes: the
			// issue store says nobody holds it, so the slot is a stale copy.
			name:           "open bead with no assignee is a stale association",
			hookBead:       "gt-2uqy",
			bd:             fakeIssueShower{issue: &beads.Issue{Status: "open"}},
			wantSafe:       true,
			wantDiagnostic: "store_status=open store_assignee=<none> hook=stale",
		},
		{
			name:           "bead held by another polecat is not our hook",
			hookBead:       "gt-work",
			bd:             fakeIssueShower{issue: &beads.Issue{Status: "hooked", Assignee: "gastown/polecats/nitro"}},
			wantSafe:       true,
			wantDiagnostic: "store_assignee=gastown/polecats/nitro hook=stale",
		},
		{
			// Ambiguous, not released: the status says somebody holds it while
			// the assignee names nobody. Ambiguity keeps blocking.
			name:        "hooked bead with no assignee still blocks",
			hookBead:    "gt-work",
			bd:          fakeIssueShower{issue: &beads.Issue{Status: "hooked"}},
			wantBlocker: "hook_bead=gt-work status=hooked",
		},
		{
			name:           "lookup error blocks",
			hookBead:       "gt-work",
			bd:             fakeIssueShower{err: errors.New("bd exploded")},
			wantUnverified: true,
			wantBlocker:    "lookup_error",
		},
		{
			name:           "missing bead blocks",
			hookBead:       "gt-work",
			bd:             fakeIssueShower{},
			wantUnverified: true,
			wantBlocker:    "hook_bead=gt-work status=missing",
		},
		{
			name:           "no store to read blocks",
			hookBead:       "gt-work",
			wantUnverified: true,
			wantBlocker:    "hook_bead=gt-work status=unverified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := assessHookBead(tt.bd, tt.hookBead, assignee)
			if got.Safe != tt.wantSafe || got.Terminal != tt.wantTerminal || got.Unverified != tt.wantUnverified {
				t.Fatalf("assessHookBead() safe/terminal/unverified = (%v, %v, %v), want (%v, %v, %v)",
					got.Safe, got.Terminal, got.Unverified, tt.wantSafe, tt.wantTerminal, tt.wantUnverified)
			}
			if tt.wantBlocker == "" && got.Blocker != "" {
				t.Fatalf("blocker = %q, want none", got.Blocker)
			}
			if tt.wantBlocker != "" && !strings.Contains(got.Blocker, tt.wantBlocker) {
				t.Fatalf("blocker = %q, want contains %q", got.Blocker, tt.wantBlocker)
			}
			if tt.wantDiagnostic != "" && !strings.Contains(got.Diagnostic, tt.wantDiagnostic) {
				t.Fatalf("diagnostic = %q, want contains %q", got.Diagnostic, tt.wantDiagnostic)
			}
			// Whichever way it lands, a hook slot that was read against the
			// store names the surfaces it read (gt-dh3d).
			if tt.hookBead != "" && !tt.wantUnverified && got.Diagnostic == "" {
				t.Fatalf("diagnostic = %q, want the surfaces named", got.Diagnostic)
			}
		})
	}
}

// The stale-association case must stay distinguishable from a genuine hook all
// the way through to the verdict: a slot the issue store contradicts produces
// no blocker at all, while the same slot backed by an assignment does.
func TestAssessHookBeadDrivesWorkstateBlocker(t *testing.T) {
	const (
		assignee = "gastown/polecats/synth"
		hookBead = "gt-2uqy"
	)

	for _, tt := range []struct {
		name        string
		issue       *beads.Issue
		wantBlocked bool
	}{
		{name: "reopened and unassigned", issue: &beads.Issue{Status: "open"}},
		{name: "genuinely hooked", issue: &beads.Issue{Status: "hooked", Assignee: assignee}, wantBlocked: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hook := assessHookBead(fakeIssueShower{issue: tt.issue}, hookBead, assignee)
			input := polecat.WorkstateInput{
				State:         polecat.StateIdle,
				CleanupStatus: polecat.CleanupClean,
			}
			if hook.Blocker != "" {
				input.HookBead = hookBead
			}
			got := polecat.DecideWorkstate(input)
			blocked := false
			for _, blocker := range got.Blockers {
				if strings.Contains(blocker, "has work on hook") {
					blocked = true
				}
			}
			if blocked != tt.wantBlocked {
				t.Fatalf("verdict %s blockers = %v, want hook blocker present = %v", got.Verdict, got.Blockers, tt.wantBlocked)
			}
		})
	}
}

func TestPartialSpawnWithoutDurableHook(t *testing.T) {
	assignee := "gastown/polecats/nitro"
	tests := []struct {
		name         string
		fields       *beads.AgentFields
		currentIssue string
		issue        *beads.Issue
		wantPartial  bool
	}{
		{
			name:        "spawning legacy hook points to open unassigned bead",
			fields:      &beads.AgentFields{AgentState: "spawning", HookBead: "gt-work"},
			issue:       &beads.Issue{ID: "gt-work", Status: "open"},
			wantPartial: true,
		},
		{
			name:   "durably hooked bead is not partial",
			fields: &beads.AgentFields{AgentState: "spawning", HookBead: "gt-work"},
			issue:  &beads.Issue{ID: "gt-work", Status: beads.StatusHooked, Assignee: assignee},
		},
		{
			name:         "current issue already found is not partial",
			fields:       &beads.AgentFields{AgentState: "spawning", HookBead: "gt-work"},
			currentIssue: "gt-work",
			issue:        &beads.Issue{ID: "gt-work", Status: "open"},
		},
		{
			name:   "working state is not partial spawn",
			fields: &beads.AgentFields{AgentState: "working", HookBead: "gt-work"},
			issue:  &beads.Issue{ID: "gt-work", Status: "open"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, diagnostic := partialSpawnWithoutDurableHook(fakeIssueShower{issue: tt.issue}, tt.fields, assignee, tt.currentIssue)
			if got != tt.wantPartial {
				t.Fatalf("partialSpawnWithoutDurableHook() = %v, want %v", got, tt.wantPartial)
			}
			if got && !strings.Contains(diagnostic, "partial_spawn_without_durable_hook") {
				t.Fatalf("diagnostic missing partial spawn marker: %q", diagnostic)
			}
		})
	}
}

func TestRecoveryGitStateBlocker(t *testing.T) {
	tests := []struct {
		name  string
		state *GitState
		err   error
		want  string
	}{
		{
			name:  "clean has no blocker",
			state: &GitState{Clean: true},
		},
		{
			name:  "uncommitted work is classified",
			state: &GitState{UncommittedFiles: []string{"a.go", "b.go"}},
			want:  "git_state=has_uncommitted uncommitted_files=2",
		},
		{
			name:  "stash is classified",
			state: &GitState{StashCount: 1},
			want:  "git_state=has_stash stash_count=1",
		},
		{
			name:  "unpushed commits are classified",
			state: &GitState{UnpushedCommits: 3},
			want:  "git_state=has_unpushed unpushed_commits=3",
		},
		{
			name: "git error is classified",
			err:  errors.New("git failed"),
			want: "git_state=unknown path=/tmp/polecat: git failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recoveryGitStateBlocker("/tmp/polecat", tt.state, tt.err)
			if got != tt.want {
				t.Errorf("recoveryGitStateBlocker() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRecoveryActionsForBlockers(t *testing.T) {
	actions := recoveryActionsForBlockers([]string{"git_state=has_stash stash_count=1"}, "gastown", "synth")
	if len(actions) != 1 || !strings.Contains(actions[0], "preserve branch-owned stash") {
		t.Fatalf("actions = %v, want branch stash preservation action", actions)
	}
	if actions := recoveryActionsForBlockers([]string{"cleanup_status=has_stash"}, "gastown", "synth"); len(actions) != 0 {
		t.Fatalf("stale cleanup-only blocker actions = %v, want none", actions)
	}

	// A hook blocker must carry a command that can actually run against a dead
	// agent, which is the only kind this verdict is ever about (gt-dh3d).
	actions = recoveryActionsForBlockers([]string{"has work on hook (gt-2uqy)"}, "gastown", "synth")
	if len(actions) != 1 || !strings.Contains(actions[0], "gt unsling gt-2uqy gastown/synth") {
		t.Fatalf("actions = %v, want an unsling command naming the bead and agent", actions)
	}
}

func TestStaleCleanWithRealUnpushedStillBlocks(t *testing.T) {
	status := RecoveryStatus{CleanupStatus: polecat.CleanupClean}
	if blocker := recoveryGitStateBlocker("/tmp/polecat", &GitState{UnpushedCommits: 1}, nil); blocker != "" {
		status.Blockers = append(status.Blockers, blocker)
	}
	if len(status.Blockers) != 1 || !strings.Contains(status.Blockers[0], "git_state=has_unpushed") {
		t.Fatalf("blockers = %v, want git_state=has_unpushed", status.Blockers)
	}
}

func TestActiveMRBlocker(t *testing.T) {
	tests := []struct {
		name       string
		mrID       string
		sourceHint string
		bd         issueShower
		want       string
	}{
		{name: "empty", want: ""},
		{name: "closed terminal source", mrID: "mr-1", sourceHint: "gt-closed", bd: fakeIssueMapShower{issues: map[string]*beads.Issue{"mr-1": &beads.Issue{ID: "mr-1", Status: "closed"}, "gt-closed": &beads.Issue{ID: "gt-closed", Status: "closed"}}}, want: ""},
		{name: "closed unknown source", mrID: "mr-1", bd: fakeIssueMapShower{issues: map[string]*beads.Issue{"mr-1": &beads.Issue{ID: "mr-1", Status: "closed"}}}, want: "active_mr=mr-1 status=closed source_issue=<missing>"},
		{name: "open", mrID: "mr-1", bd: fakeIssueShower{issue: &beads.Issue{ID: "mr-1", Status: "open"}}, want: "active_mr=mr-1 status=open"},
		{name: "missing terminal source", mrID: "mr-1", sourceHint: "gt-closed", bd: fakeIssueMapShower{issues: map[string]*beads.Issue{"gt-closed": &beads.Issue{ID: "gt-closed", Status: "closed"}}}, want: ""},
		{name: "missing unknown source", mrID: "mr-1", bd: fakeIssueMapShower{}, want: "active_mr=mr-1 status=missing source_issue=<missing>"},
		{name: "nil issue unknown source", mrID: "mr-1", bd: fakeIssueShower{issue: nil}, want: "active_mr=mr-1 status=missing source_issue=<missing>"},
		{name: "nil reader", mrID: "mr-1", bd: nil, want: "active_mr=mr-1 status=unverified"},
		{name: "lookup error", mrID: "mr-1", bd: fakeIssueShower{err: errors.New("bd exploded")}, want: "active_mr=mr-1 status=lookup_error: bd exploded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := activeMRBlocker(tt.bd, tt.mrID, tt.sourceHint, false, false)
			if got != tt.want {
				t.Errorf("activeMRBlocker() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatSafetyCheckBlockers(t *testing.T) {
	blocked := []*SafetyCheckResult{
		{Polecat: "gastown/fury", Reasons: []string{"cleanup_status=unknown", "active_mr=hq-wisp-1 status=open"}},
		{Polecat: "gastown/rust", Reasons: []string{"has work on hook (gt-abc)"}},
	}

	got := formatSafetyCheckBlockers(blocked)
	want := "gastown/fury: cleanup_status=unknown; active_mr=hq-wisp-1 status=open | gastown/rust: has work on hook (gt-abc)"
	if got != want {
		t.Errorf("formatSafetyCheckBlockers() = %q, want %q", got, want)
	}
}

func TestDisplaySafetyCheckBlockedToIncludesPredicates(t *testing.T) {
	var buf bytes.Buffer
	displaySafetyCheckBlockedTo(&buf, []*SafetyCheckResult{{
		Polecat: "gastown/fury",
		Reasons: []string{"cleanup_status=unknown", "active_mr=hq-wisp-1 status=open"},
	}})
	out := buf.String()
	for _, want := range []string{
		"Cannot nuke",
		"gastown/fury",
		"cleanup_status=unknown",
		"active_mr=hq-wisp-1 status=open",
		"Force nuke (LOSES WORK)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("displaySafetyCheckBlockedTo() missing %q in %q", want, out)
		}
	}
}

func TestDryRunNukeSummary(t *testing.T) {
	tests := []struct {
		name    string
		total   int
		blocked int
		want    string
	}{
		{name: "safe", total: 2, want: "Would nuke 2 polecat(s)."},
		{name: "blocked", total: 2, blocked: 1, want: "Would refuse to nuke 1 of 2 polecat(s) without --force."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dryRunNukeSummary(tt.total, tt.blocked); got != tt.want {
				t.Errorf("dryRunNukeSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasSubmittableWorkForRecoveryUsesUpstream(t *testing.T) {
	repo := setupRecoveryGitRepo(t)

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("branch with no commits ahead of its upstream should not require MQ submission")
	}

	writeRecoveryFile(t, filepath.Join(repo, "change.txt"), "change")
	runGit(t, repo, "add", "change.txt")
	runGit(t, repo, "commit", "-m", "change")

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{}, nil); !got {
		t.Fatal("branch with commits ahead of its upstream should require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryIgnoresSelfUpstream(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/test")
	writeRecoveryFile(t, filepath.Join(repo, "feature.txt"), "feature")
	runGit(t, repo, "add", "feature.txt")
	runGit(t, repo, "commit", "-m", "feature")
	runGit(t, repo, "push", "-u", "origin", "polecat/test")

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{UnpushedCommits: 1}, nil); !got {
		t.Fatal("self-upstream feature branch should fall back and preserve MQ requirement")
	}
}

func TestHasSubmittableWorkForRecoveryIgnoresPatchEquivalentBranch(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/equivalent")
	writeRecoveryFile(t, filepath.Join(repo, "equiv.txt"), "equiv")
	runGit(t, repo, "add", "equiv.txt")
	runGit(t, repo, "commit", "-m", "equiv")
	runGit(t, repo, "switch", "integration/test")
	writeRecoveryFile(t, filepath.Join(repo, "other.txt"), "other")
	runGit(t, repo, "add", "other.txt")
	runGit(t, repo, "commit", "-m", "other")
	runGit(t, repo, "cherry-pick", "polecat/equivalent")
	runGit(t, repo, "push", "origin", "integration/test")
	runGit(t, repo, "switch", "polecat/equivalent")
	runGit(t, repo, "branch", "--set-upstream-to=origin/integration/test")

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("patch-equivalent branch should not require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryUsesExplicitTargetAncestor(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/contained")
	writeRecoveryFile(t, filepath.Join(repo, "contained.txt"), "contained")
	runGit(t, repo, "add", "contained.txt")
	runGit(t, repo, "commit", "-m", "contained")
	runGit(t, repo, "switch", "integration/test")
	runGit(t, repo, "merge", "--ff-only", "polecat/contained")
	runGit(t, repo, "push", "origin", "integration/test")
	runGit(t, repo, "switch", "polecat/contained")

	if got := hasSubmittableWorkForRecovery(repo, []string{"integration/test"}, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("branch whose HEAD is contained by explicit target should not require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryUsesExplicitTargetCherry(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/cherry")
	writeRecoveryFile(t, filepath.Join(repo, "cherry.txt"), "cherry")
	runGit(t, repo, "add", "cherry.txt")
	runGit(t, repo, "commit", "-m", "cherry")
	runGit(t, repo, "switch", "integration/test")
	writeRecoveryFile(t, filepath.Join(repo, "target.txt"), "target")
	runGit(t, repo, "add", "target.txt")
	runGit(t, repo, "commit", "-m", "advance target")
	runGit(t, repo, "cherry-pick", "polecat/cherry")
	runGit(t, repo, "push", "origin", "integration/test")
	runGit(t, repo, "switch", "polecat/cherry")

	if got := hasSubmittableWorkForRecovery(repo, []string{"integration/test"}, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("patch-equivalent branch on advanced explicit target should not require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryUsesExplicitTargetSquashNoop(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	if err := exec.Command("git", "-C", repo, "merge-tree", "--write-tree", "HEAD", "HEAD").Run(); err != nil {
		t.Skipf("git merge-tree --write-tree unsupported: %v", err)
	}
	runGit(t, repo, "switch", "-c", "polecat/squash")
	writeRecoveryFile(t, filepath.Join(repo, "squash.txt"), "one\n")
	runGit(t, repo, "add", "squash.txt")
	runGit(t, repo, "commit", "-m", "checkpoint one")
	writeRecoveryFile(t, filepath.Join(repo, "squash.txt"), "one\ntwo\n")
	runGit(t, repo, "add", "squash.txt")
	runGit(t, repo, "commit", "-m", "checkpoint two")

	runGit(t, repo, "switch", "integration/test")
	runGit(t, repo, "merge", "--squash", "polecat/squash")
	runGit(t, repo, "commit", "-m", "squash polecat work")
	writeRecoveryFile(t, filepath.Join(repo, "target.txt"), "target advanced\n")
	runGit(t, repo, "add", "target.txt")
	runGit(t, repo, "commit", "-m", "advance target")
	runGit(t, repo, "push", "origin", "integration/test")
	runGit(t, repo, "switch", "polecat/squash")

	if got := hasSubmittableWorkForRecovery(repo, []string{"integration/test"}, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("squash-preserved branch on advanced explicit target should not require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryKeepsExplicitTargetUniquePatch(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/unique")
	writeRecoveryFile(t, filepath.Join(repo, "unique.txt"), "unique")
	runGit(t, repo, "add", "unique.txt")
	runGit(t, repo, "commit", "-m", "unique")

	if got := hasSubmittableWorkForRecovery(repo, []string{"integration/test"}, &GitState{}, nil); !got {
		t.Fatal("unique patch absent from explicit target should require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryFallback(t *testing.T) {
	if got := hasSubmittableWorkForRecovery("/does/not/exist", nil, &GitState{UnpushedCommits: 0}, nil); got {
		t.Fatal("clean fallback git state should not require MQ submission")
	}
	if got := hasSubmittableWorkForRecovery("/does/not/exist", nil, &GitState{UnpushedCommits: 1}, nil); !got {
		t.Fatal("unpushed fallback git state should require MQ submission")
	}
	if got := hasSubmittableWorkForRecovery("/does/not/exist", nil, nil, errors.New("git failed")); !got {
		t.Fatal("git-state error fallback should remain conservative")
	}
}

func setupRecoveryGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	repo := filepath.Join(root, "repo")
	runCmd(t, root, "git", "init", "--bare", remote)
	runCmd(t, root, "git", "init", repo)
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	writeRecoveryFile(t, filepath.Join(repo, "README.md"), "base")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "base")
	runGit(t, repo, "branch", "-M", "main")
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "-u", "origin", "main")
	runGit(t, repo, "switch", "-c", "integration/test")
	runGit(t, repo, "push", "-u", "origin", "integration/test")
	return repo
}

func writeRecoveryFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	runCmd(t, dir, "git", args...)
}

func runCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// TestPolecatWorkingEvidenceNamesItsSource is the gt-mkpm regression on the
// remedy surface.
//
// check-recovery is what agents are told to reach for when `gt polecat list` is
// untrustworthy, and its WORKING arm printed "The agent's pane shows it
// mid-turn" on BOTH roads to that verdict — including the one that reads the
// agent bead and never looks at a pane. Measured wrong twice in one evening
// (gastown/crater, parked with no interrupt line and "Churned for 13m 51s";
// gastown/brahmin, parked, and a re-run a minute later gave NEEDS_RECOVERY and
// named push_failed) and right once (gastown/foundation, genuinely mid-turn).
// The same sentence, three times, with nothing separating the cases — and its
// prescription is leave-alone, which on the brahmin instance argued for NOT
// acting on an unhealthy polecat.
func TestPolecatWorkingEvidenceNamesItsSource(t *testing.T) {
	setupPolecatTestRegistry(t)

	var measured bytes.Buffer
	printPolecatWorkingEvidence(&measured, polecat.WorkstateReasonSessionBusy, "gastown", "foundation")
	if !strings.Contains(measured.String(), "pane was read") {
		t.Fatalf("session-busy must say the pane was read:\n%s", measured.String())
	}

	var beadDerived bytes.Buffer
	printPolecatWorkingEvidence(&beadDerived, polecat.WorkstateReasonNotIdle, "gastown", "crater")
	got := beadDerived.String()
	// The retracted claim must be gone from this road. Matched
	// case-insensitively and on the CONTENT phrase, not on a heading: the
	// sentence is prose and could legitimately be reworded, but any wording that
	// still asserts what the pane shows is the defect.
	if strings.Contains(strings.ToLower(got), "pane shows it mid-turn") {
		t.Fatalf("bead-derived WORKING must not assert what the pane shows:\n%s", got)
	}
	if !strings.Contains(got, "AGENT BEAD") || !strings.Contains(got, "NOT measured busy") {
		t.Fatalf("bead-derived WORKING must name what it did read:\n%s", got)
	}
	// And it must hand over the discriminator that disagreed correctly all five
	// times this bead recorded, rather than leaving the reader to know it.
	if !strings.Contains(got, "esc to interrupt") {
		t.Fatalf("bead-derived WORKING must give the pane check:\n%s", got)
	}
	// The control: the two roads must actually differ. If they ever converge
	// again, the assertions above can both pass on identical text.
	if got == measured.String() {
		t.Fatal("both roads to WORKING print the same prose again — that is the whole defect")
	}
}

// TestPolecatListMeasurementFooter pins the acceptance criterion of gt-mkpm: a
// reader of `gt polecat list` alone can tell a measured blocker from an
// unmeasured one, without consulting source and without running a second
// command.
func TestPolecatListMeasurementFooter(t *testing.T) {
	var out bytes.Buffer
	printPolecatListMeasurementFooter(&out, []PolecatListItem{
		{Rig: "gastown", Name: "ghoul", ReuseStatus: polecat.ReuseStatusMQUnchecked},
		{Rig: "gastown", Name: "synth", ReuseStatus: polecat.ReuseStatusRecoveryNeeded},
		{Rig: "gastown", Name: "crater", ReuseStatus: polecat.ReuseStatusUnverified},
	})
	got := out.String()
	if !strings.Contains(got, "2 of 3") {
		t.Fatalf("footer must count only the unmeasured rows:\n%s", got)
	}
	if !strings.Contains(got, "gt polecat check-recovery gastown/ghoul") {
		t.Fatalf("footer must name the surface that measures, on a real row:\n%s", got)
	}

	// The control, and it is the one that keeps the footer meaningful: a listing
	// where every verdict was earned prints nothing. A footer that always fires
	// is a footer readers stop seeing.
	var quiet bytes.Buffer
	printPolecatListMeasurementFooter(&quiet, []PolecatListItem{
		{Rig: "gastown", Name: "synth", ReuseStatus: polecat.ReuseStatusRecoveryNeeded},
		{Rig: "gastown", Name: "dag", ReuseStatus: polecat.ReuseStatusPreserved},
	})
	if quiet.String() != "" {
		t.Fatalf("a fully measured listing must print no footer:\n%s", quiet.String())
	}
}
