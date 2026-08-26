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

type fakeCleanupUpdater struct {
	err    error
	id     string
	status string
	calls  int
}

func (f *fakeCleanupUpdater) UpdateAgentCleanupStatus(id string, cleanupStatus string) error {
	f.calls++
	f.id = id
	f.status = cleanupStatus
	return f.err
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

func TestReconcileCleanupStatusIfSafe(t *testing.T) {
	for _, previous := range []polecat.CleanupStatus{polecat.CleanupUnpushed, polecat.CleanupStash, polecat.CleanupUncommitted} {
		t.Run(string(previous), func(t *testing.T) {
			status := &RecoveryStatus{
				CleanupStatus: previous,
				Verdict:       "SAFE_TO_NUKE",
				Branch:        "polecat/nitro",
				MQStatus:      "submitted",
			}
			updater := &fakeCleanupUpdater{}
			reconcileCleanupStatusIfSafe(status, updater, "gt-gastown-polecat-nitro", &polecat.Polecat{State: polecat.StateIdle}, &beads.AgentFields{
				AgentState:    string(beads.AgentStateIdle),
				CleanupStatus: string(previous),
			})

			if updater.calls != 1 {
				t.Fatalf("UpdateAgentCleanupStatus calls = %d, want 1", updater.calls)
			}
			if updater.id != "gt-gastown-polecat-nitro" || updater.status != string(polecat.CleanupClean) {
				t.Fatalf("update = (%q, %q), want clean update for agent", updater.id, updater.status)
			}
			if status.CleanupStatus != polecat.CleanupClean || !status.Reconciled {
				t.Fatalf("status after reconcile = (%q, reconciled=%v), want clean true", status.CleanupStatus, status.Reconciled)
			}
		})
	}
}

func TestReconcileCleanupStatusIfSafe_FailsClosed(t *testing.T) {
	status := &RecoveryStatus{
		CleanupStatus: polecat.CleanupUnpushed,
		Verdict:       "SAFE_TO_NUKE",
		Branch:        "polecat/nitro",
		MQStatus:      "submitted",
	}
	reconcileCleanupStatusIfSafe(status, &fakeCleanupUpdater{err: errors.New("bd update failed")}, "gt-gastown-polecat-nitro", &polecat.Polecat{State: polecat.StateIdle}, &beads.AgentFields{
		AgentState:    string(beads.AgentStateIdle),
		CleanupStatus: string(polecat.CleanupUnpushed),
	})

	if status.Verdict != "NEEDS_RECOVERY" || !status.NeedsRecovery {
		t.Fatalf("failed update verdict = %q needs=%v, want NEEDS_RECOVERY true", status.Verdict, status.NeedsRecovery)
	}
	if len(status.Blockers) == 0 || !strings.Contains(status.Blockers[0], "cleanup_reconcile_failed") {
		t.Fatalf("blockers = %v, want cleanup_reconcile_failed", status.Blockers)
	}
}

func TestCleanupStatusReconcileCandidateRequiresStrictPredicates(t *testing.T) {
	baseStatus := &RecoveryStatus{Verdict: "SAFE_TO_NUKE", Branch: "polecat/nitro", MQStatus: "submitted"}
	basePolecat := &polecat.Polecat{State: polecat.StateIdle}
	baseFields := &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupUnpushed)}

	tests := []struct {
		name   string
		status *RecoveryStatus
		p      *polecat.Polecat
		fields *beads.AgentFields
	}{
		{name: "stale clean is not rewritten", status: baseStatus, p: basePolecat, fields: &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)}},
		{name: "working polecat blocks", status: baseStatus, p: &polecat.Polecat{State: polecat.StateWorking}, fields: baseFields},
		{name: "working agent bead blocks", status: baseStatus, p: basePolecat, fields: &beads.AgentFields{AgentState: string(beads.AgentStateWorking), CleanupStatus: string(polecat.CleanupUnpushed)}},
		{name: "needs recovery blocks", status: &RecoveryStatus{Verdict: "NEEDS_RECOVERY", NeedsRecovery: true, Branch: "polecat/nitro", MQStatus: "submitted"}, p: basePolecat, fields: baseFields},
		{name: "unknown mq blocks", status: &RecoveryStatus{Verdict: "SAFE_TO_NUKE", Branch: "polecat/nitro", MQStatus: "unknown"}, p: basePolecat, fields: baseFields},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := cleanupStatusReconcileCandidate(tt.status, tt.p, tt.fields); ok {
				t.Fatal("cleanupStatusReconcileCandidate() allowed unsafe reconciliation")
			}
		})
	}
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
