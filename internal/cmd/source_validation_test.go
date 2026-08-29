package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
)

func TestRoutedIssueBeadsUsesTownRoutesForCustomPrefix(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)

	_, gotCurrent, gotRouted := routedIssueBeads(workDir, "bd-source")
	if gotCurrent != currentBeadsDir {
		t.Fatalf("current beads dir = %q, want %q", gotCurrent, currentBeadsDir)
	}
	if gotRouted != ownerBeadsDir {
		t.Fatalf("routed beads dir = %q, want %q", gotRouted, ownerBeadsDir)
	}
}

func TestSourceRouteContextNamesCurrentAndRoutedDB(t *testing.T) {
	context := sourceRouteContext("/town/gastown/.beads", "/town/beads/.beads")
	for _, want := range []string{"current_db=/town/gastown/.beads", "routed_db=/town/beads/.beads"} {
		if !strings.Contains(context, want) {
			t.Fatalf("source route context %q missing %q", context, want)
		}
	}
}

func TestResolveSubmitSourceIssueIgnoresCurrentRigMirror(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	installSubmitSourceBDStub(t, currentBeadsDir, ownerBeadsDir, false)

	source, err := resolveSubmitSourceIssue(workDir, "bd-source")
	if err != nil {
		t.Fatalf("resolveSubmitSourceIssue: %v", err)
	}
	if source.Issue.Title != "owner source" {
		t.Fatalf("source title = %q, want routed owner source (current-rig mirror must be ignored)", source.Issue.Title)
	}
	if source.CurrentBeadsDir != currentBeadsDir || source.RoutedBeadsDir != ownerBeadsDir {
		t.Fatalf("route = current %q routed %q, want current %q routed %q", source.CurrentBeadsDir, source.RoutedBeadsDir, currentBeadsDir, ownerBeadsDir)
	}
}

func TestResolveSubmitSourceIssueFailureNamesRoutingContext(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	installSubmitSourceBDStub(t, currentBeadsDir, ownerBeadsDir, true)

	_, err := resolveSubmitSourceIssue(workDir, "bd-source")
	if err == nil {
		t.Fatal("resolveSubmitSourceIssue succeeded, want routed owner lookup failure")
	}
	errText := err.Error()
	for _, want := range []string{"source_issue bd-source could not be resolved", "current_db=" + currentBeadsDir, "routed_db=" + ownerBeadsDir} {
		if !strings.Contains(errText, want) {
			t.Fatalf("error %q missing %q", errText, want)
		}
	}
}

// gt-zu5n: gt:keep is the sanctioned reaper exemption, and applying it also made
// the bead an invalid gt done source — so hardening a bead converted the polecat
// working it into a zombie. Resolution must not judge concreteness; only the
// caller that is about to create an MR may.
func TestLookupSubmitSourceIssueTakesProtectedSourceResolveRejectsIt(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	installSubmitSourceBDStubWithOwnerJSON(t, currentBeadsDir, ownerBeadsDir, false,
		`[{"id":"bd-source","title":"owner source","status":"open","priority":1,"issue_type":"task","labels":["gt:keep"]}]`)

	source, err := lookupSubmitSourceIssue(workDir, "bd-source")
	if err != nil {
		t.Fatalf("lookupSubmitSourceIssue on gt:keep source: %v; want the bead resolved, not rejected", err)
	}
	if source.Issue.Title != "owner source" {
		t.Fatalf("source title = %q, want routed owner source", source.Issue.Title)
	}

	// The same bead is still refused where an MR really would be created.
	if _, err := resolveSubmitSourceIssue(workDir, "bd-source"); err == nil ||
		!strings.Contains(err.Error(), "protected-label:gt:keep") {
		t.Fatalf("resolveSubmitSourceIssue on gt:keep source = %v, want protected-label rejection", err)
	}
}

func TestValidateMergeRequestSourceUsesPreResolvedSource(t *testing.T) {
	mr := &beads.Issue{ID: "gt-mr", Description: "source_issue: bd-source\n"}
	if err := validateMergeRequestSource(mr, "bd-source", nil); err == nil || !strings.Contains(err.Error(), "pre-resolved") {
		t.Fatalf("validateMergeRequestSource without source = %v, want pre-resolved error", err)
	}
	if err := validateMergeRequestSource(mr, "bd-source", &beads.Issue{ID: "bd-source", Type: "task"}); err != nil {
		t.Fatalf("validateMergeRequestSource with routed source: %v", err)
	}
}

func TestClosedSourceIssueRefusal(t *testing.T) {
	tests := []struct {
		name       string
		issue      *beads.Issue
		wantRefuse bool
	}{
		{"open", &beads.Issue{ID: "bd-6jp", Status: "open"}, false},
		{"in_progress", &beads.Issue{ID: "bd-6jp", Status: "in_progress"}, false},
		{"hooked", &beads.Issue{ID: "bd-6jp", Status: "hooked"}, false},
		{"blocked", &beads.Issue{ID: "bd-6jp", Status: "blocked"}, false},
		{"deferred", &beads.Issue{ID: "bd-6jp", Status: "deferred"}, false},
		{"closed", &beads.Issue{ID: "bd-6jp", Status: "closed"}, true},
		{"closed mixed case", &beads.Issue{ID: "bd-6jp", Status: " CLOSED "}, true},
		{"tombstone", &beads.Issue{ID: "bd-6jp", Status: "tombstone"}, true},
		{"nil issue defers to concreteness check", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refusal := closedSourceIssueRefusal("bd-6jp", tt.issue)
			if (refusal != "") != tt.wantRefuse {
				t.Fatalf("closedSourceIssueRefusal = %q, want refusal=%v", refusal, tt.wantRefuse)
			}
			if tt.wantRefuse && !strings.Contains(refusal, "bd-6jp") {
				t.Fatalf("refusal %q does not name the source issue", refusal)
			}
		})
	}
}

func TestNoOpMRRefusal(t *testing.T) {
	tests := []struct {
		name       string
		landedErr  error
		wantRefuse bool
	}{
		{"already landed (proof found)", nil, true},
		{"not landed (real work)", git.ErrCommitNotLanded, false},
		{"unprovable fails open", git.ErrMergeProofUnprovable, false},
		{"other error fails open", errors.New("offline"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refusal := noOpMRRefusal("main", "abc123", tt.landedErr)
			if (refusal != "") != tt.wantRefuse {
				t.Fatalf("noOpMRRefusal = %q, want refusal=%v", refusal, tt.wantRefuse)
			}
			if tt.wantRefuse && !strings.Contains(refusal, "main") {
				t.Fatalf("refusal %q does not name the target", refusal)
			}
		})
	}
}

func TestValidateNonEmptyMRSourceExplainsRecovery(t *testing.T) {
	if err := validateNonEmptyMRSource("main", "abc123", git.ErrCommitNotLanded); err != nil {
		t.Fatalf("validateNonEmptyMRSource with real work = %v, want nil", err)
	}
	err := validateNonEmptyMRSource("main", "abc123", nil)
	if err == nil {
		t.Fatal("validateNonEmptyMRSource for already-landed commit = nil, want refusal")
	}
	for _, want := range []string{"already on main", "no-op", "--allow-noop"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestValidateOpenSourceIssueForMRExplainsRecovery(t *testing.T) {
	if err := validateOpenSourceIssueForMR("bd-6jp", &beads.Issue{ID: "bd-6jp", Status: "open"}); err != nil {
		t.Fatalf("validateOpenSourceIssueForMR on open issue = %v, want nil", err)
	}
	err := validateOpenSourceIssueForMR("bd-6jp", &beads.Issue{ID: "bd-6jp", Status: "closed"})
	if err == nil {
		t.Fatal("validateOpenSourceIssueForMR on closed issue = nil, want refusal")
	}
	for _, want := range []string{"bd-6jp is closed", "bd update bd-6jp --status=open", "--allow-closed-issue"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestRunMqSubmitRefusesClosedSourceIssue(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	branch := setupRoutedSubmitGitRepo(t, workDir, true)
	logPath := installSubmitSourceBDRecorderWithStatus(t, currentBeadsDir, ownerBeadsDir, "closed")
	resetMqSubmitFlagsForTest(t)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_RIG", "")
	t.Chdir(workDir)

	mqSubmitBranch = branch
	mqSubmitIssue = "bd-source"
	mqSubmitNoCleanup = true
	err := runMqSubmit(nil, nil)
	if err == nil {
		t.Fatal("runMqSubmit against closed source issue succeeded, want refusal")
	}
	if !strings.Contains(err.Error(), "bd-source is closed") {
		t.Fatalf("error %q does not explain the closed source issue", err.Error())
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogNotContains(t, log, currentBeadsDir, "create --json")
}

func TestRunMqSubmitAllowClosedIssueOverride(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	branch := setupRoutedSubmitGitRepo(t, workDir, true)
	logPath := installSubmitSourceBDRecorderWithStatus(t, currentBeadsDir, ownerBeadsDir, "closed")
	resetMqSubmitFlagsForTest(t)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_RIG", "")
	t.Chdir(workDir)

	mqSubmitBranch = branch
	mqSubmitIssue = "bd-source"
	mqSubmitNoCleanup = true
	mqSubmitAllowClosedIssue = true
	if err := runMqSubmit(nil, nil); err != nil {
		t.Fatalf("runMqSubmit with --allow-closed-issue: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogContains(t, log, currentBeadsDir, "create --json")
}

func TestRunMqSubmitRefusesNoOpMR(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	branch := setupRoutedSubmitGitRepoNoOp(t, workDir)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetMqSubmitFlagsForTest(t)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_RIG", "")
	t.Chdir(workDir)

	mqSubmitBranch = branch
	mqSubmitIssue = "bd-source"
	mqSubmitNoCleanup = true
	err := runMqSubmit(nil, nil)
	if err == nil {
		t.Fatal("runMqSubmit for a branch with no new work succeeded, want refusal")
	}
	if !strings.Contains(err.Error(), "there is no new work to merge") {
		t.Fatalf("error %q does not explain the no-op merge", err.Error())
	}
	if !strings.Contains(err.Error(), "--allow-noop") {
		t.Fatalf("error %q missing operator override hint", err.Error())
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogNotContains(t, log, currentBeadsDir, "create --json")
}

func TestRunMqSubmitAllowNoOpOverride(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	branch := setupRoutedSubmitGitRepoNoOp(t, workDir)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetMqSubmitFlagsForTest(t)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_RIG", "")
	t.Chdir(workDir)

	mqSubmitBranch = branch
	mqSubmitIssue = "bd-source"
	mqSubmitNoCleanup = true
	mqSubmitAllowNoOp = true
	if err := runMqSubmit(nil, nil); err != nil {
		t.Fatalf("runMqSubmit with --allow-noop: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogContains(t, log, currentBeadsDir, "create --json")
}

func TestMqSubmitPathUsesRoutedSourceAndCurrentRigQueueBeads(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)

	currentBD := beads.New(workDir)
	source, err := resolveSubmitSourceIssue(workDir, "bd-source")
	if err != nil {
		t.Fatalf("resolveSubmitSourceIssue: %v", err)
	}

	if _, err := currentBD.Create(beads.CreateOptions{
		Title:       "Merge: bd-source",
		Labels:      []string{"gt:merge-request"},
		Priority:    source.Issue.Priority,
		Description: "branch: polecat/refuge/bd-source\ntarget: main\nsource_issue: bd-source\nrig: gastown",
		Ephemeral:   true,
		Rig:         "gastown",
	}); err != nil {
		t.Fatalf("current-rig MR create: %v", err)
	}
	if err := source.BD.AddComment("bd-source", "MR created: gt-mr"); err != nil {
		t.Fatalf("source back-link comment: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogContains(t, log, ownerBeadsDir, "show bd-source --json")
	assertBDLogContains(t, log, currentBeadsDir, "create --json")
	assertBDLogContains(t, log, ownerBeadsDir, "comments add bd-source")
	assertBDLogNotContains(t, log, currentBeadsDir, "show bd-source --json")
}

func TestDoneNoMRClosePathUsesRoutedSourceBeads(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)

	source, err := resolveSubmitSourceIssue(workDir, "bd-source")
	if err != nil {
		t.Fatalf("resolveSubmitSourceIssue: %v", err)
	}
	if skipReason, fatal := doneSourceCloseSkipReason(source.BD, "bd-source", source.Issue); skipReason != "" || fatal {
		t.Fatalf("doneSourceCloseSkipReason = %q, %v; want close allowed", skipReason, fatal)
	}
	if err := source.BD.ForceCloseWithReason("done", "bd-source"); err != nil {
		t.Fatalf("routed source close: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogContains(t, log, ownerBeadsDir, "show bd-source --json")
	assertBDLogContains(t, log, ownerBeadsDir, "close bd-source")
	assertBDLogNotContains(t, log, currentBeadsDir, "close bd-source")
}

func TestRunMqSubmitWithRoutedIssueIgnoresCurrentRigMirror(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	branch := setupRoutedSubmitGitRepo(t, workDir, true)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetMqSubmitFlagsForTest(t)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_RIG", "")
	t.Chdir(workDir)

	mqSubmitBranch = branch
	mqSubmitIssue = "bd-source"
	mqSubmitNoCleanup = true
	if err := runMqSubmit(nil, nil); err != nil {
		t.Fatalf("runMqSubmit: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogContains(t, log, ownerBeadsDir, "show bd-source --json")
	assertBDLogContains(t, log, currentBeadsDir, "create --json")
	assertBDLogContains(t, log, ownerBeadsDir, "comments add bd-source")
	assertBDLogNotContains(t, log, currentBeadsDir, "show bd-source --json")
}

func TestRunDoneWithRoutedIssueIgnoresCurrentRigMirror(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupRoutedSubmitGitRepo(t, workDir, false)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetDoneFlagsForTest(t)
	townRoot := routedSourceTestTownRoot(workDir)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)
	t.Setenv("GT_ROLE", "gastown/polecats/refuge")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "refuge")
	t.Setenv("BD_ACTOR", "gastown/polecats/refuge")
	t.Chdir(workDir)

	doneIssue = "bd-source"
	doneCleanupStatus = "unpushed"
	doneSkipVerify = true
	updateAgentStateOnDoneFn = func(cwd, townRoot, exitType, issueID, mrID string) error { return nil }
	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogContains(t, log, ownerBeadsDir, "show bd-source --json")
	assertBDLogContains(t, log, currentBeadsDir, "create --json")
	assertBDLogContains(t, log, ownerBeadsDir, "comments add bd-source")
	assertBDLogContains(t, log, currentBeadsDir, "show gt-mr --json")
	assertBDLogNotContains(t, log, currentBeadsDir, "show bd-source --json")
}

// gt-7qm: a polecat that respawns onto an already-closed bead must complete
// without producing a merge request. Creating one restarts the submit/reject
// loop that the closed bead is the terminal state of.
func TestRunDoneCreatesNoMRForClosedSourceIssue(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupRoutedSubmitGitRepo(t, workDir, false)
	logPath := installSubmitSourceBDRecorderWithStatus(t, currentBeadsDir, ownerBeadsDir, "closed")
	resetDoneFlagsForTest(t)
	townRoot := routedSourceTestTownRoot(workDir)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)
	t.Setenv("GT_ROLE", "gastown/polecats/refuge")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "refuge")
	t.Setenv("BD_ACTOR", "gastown/polecats/refuge")
	t.Chdir(workDir)

	doneIssue = "bd-source"
	doneCleanupStatus = "unpushed"
	doneSkipVerify = true
	updateAgentStateOnDoneFn = func(cwd, townRoot, exitType, issueID, mrID string) error { return nil }
	// gt-7k3q: the refusal is correct and it is still a refusal. Every step
	// below it runs — the stranding report, the witness mail, the retirement —
	// and then gt done exits non-zero, because a caller that checks only the
	// exit status must not read "no merge request was created, and the branch
	// is not in the target" as a clean completion.
	err := runDone(nil, nil)
	if err == nil {
		t.Fatal("runDone returned nil after refusing the MR; a refused submit must exit non-zero")
	}
	if !strings.Contains(err.Error(), "merge request creation was refused") {
		t.Errorf("runDone error should name the refusal, got: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogContains(t, log, ownerBeadsDir, "show bd-source --json")
	// The gt-7qm invariant is that no MERGE REQUEST is produced — that is what
	// restarts the submit/reject loop. It was originally pinned as "no bead is
	// created at all", which gt-rbul had to loosen: the refusal now files a
	// stranded-work report when the branch it refused on behalf of is not in the
	// target. Narrow the negative to the MR rather than dropping it.
	assertBDLogNotContains(t, log, currentBeadsDir, "create --json --title=Merge: bd-source")
	assertBDLogNotContains(t, log, currentBeadsDir, "--labels=gt:merge-request")
	// gt-rbul: and the stranding must be reported, not merely refused. This
	// branch is 1 commit ahead of an unpushed origin/main, so it is stranded.
	assertBDLogContains(t, log, currentBeadsDir, "create --json --title=Stranded by gt done: bd-source is closed, branch feature/routed-submit left unmerged")
	assertBDLogContains(t, log, currentBeadsDir, "update gt-mr --assignee=gastown/witness")
}

func setupRoutedSourceTestTown(t *testing.T) (workDir, currentBeadsDir, ownerBeadsDir string) {
	t.Helper()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write town sentinel: %v", err)
	}

	workDir = filepath.Join(townRoot, "gastown", "polecats", "refuge", "gastown")
	currentBeadsDir = filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	ownerBeadsDir = filepath.Join(townRoot, "beads", "mayor", "rig", ".beads")
	townBeadsDir := filepath.Join(townRoot, ".beads")
	for _, dir := range []string{filepath.Join(workDir, ".beads"), currentBeadsDir, ownerBeadsDir, townBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(workDir, ".beads", "redirect"), []byte("../../../mayor/rig/.beads\n"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	return workDir, currentBeadsDir, ownerBeadsDir
}

func routedSourceTestTownRoot(workDir string) string {
	return filepath.Clean(filepath.Join(workDir, "..", "..", "..", ".."))
}

func setupRoutedSubmitCommandTown(t *testing.T, workDir string) {
	t.Helper()
	townRoot := routedSourceTestTownRoot(workDir)
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	if err := config.SaveRigsConfig(rigsPath, &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs: map[string]config.RigEntry{
			"gastown": {GitURL: "file://test-gastown"},
		},
	}); err != nil {
		t.Fatalf("save rigs config: %v", err)
	}
}

func setupRoutedSubmitGitRepo(t *testing.T, workDir string, pushBranch bool) string {
	t.Helper()
	remote := t.TempDir()
	runGitForMQSubmitTest(t, remote, "init", "--bare")
	runGitForMQSubmitTest(t, workDir, "init")
	runGitForMQSubmitTest(t, workDir, "config", "user.email", "test@example.com")
	runGitForMQSubmitTest(t, workDir, "config", "user.name", "Test User")
	runGitForMQSubmitTest(t, workDir, "remote", "add", "origin", remote)
	writeMQSubmitTestFile(t, workDir, ".gitignore", ".beads/\n.runtime/\n")
	writeMQSubmitTestFile(t, workDir, "file.txt", "main\n")
	runGitForMQSubmitTest(t, workDir, "add", ".gitignore", "file.txt")
	runGitForMQSubmitTest(t, workDir, "commit", "-m", "main")
	runGitForMQSubmitTest(t, workDir, "branch", "-M", "main")
	runGitForMQSubmitTest(t, workDir, "push", "-u", "origin", "main")
	branch := "feature/routed-submit"
	runGitForMQSubmitTest(t, workDir, "checkout", "-b", branch)
	writeMQSubmitTestFile(t, workDir, "file.txt", "feature\n")
	runGitForMQSubmitTest(t, workDir, "commit", "-am", "feature")
	if pushBranch {
		runGitForMQSubmitTest(t, workDir, "push", "origin", branch)
	}
	return branch
}

// setupRoutedSubmitGitRepoNoOp mirrors setupRoutedSubmitGitRepo but the
// returned branch carries no work beyond main: it is branched and pushed
// with zero new commits, so its tip is exactly origin/main's tip. Exercises
// the gt-2fgq no-op-MR gate.
func setupRoutedSubmitGitRepoNoOp(t *testing.T, workDir string) string {
	t.Helper()
	remote := t.TempDir()
	runGitForMQSubmitTest(t, remote, "init", "--bare")
	runGitForMQSubmitTest(t, workDir, "init")
	runGitForMQSubmitTest(t, workDir, "config", "user.email", "test@example.com")
	runGitForMQSubmitTest(t, workDir, "config", "user.name", "Test User")
	runGitForMQSubmitTest(t, workDir, "remote", "add", "origin", remote)
	writeMQSubmitTestFile(t, workDir, ".gitignore", ".beads/\n.runtime/\n")
	writeMQSubmitTestFile(t, workDir, "file.txt", "main\n")
	runGitForMQSubmitTest(t, workDir, "add", ".gitignore", "file.txt")
	runGitForMQSubmitTest(t, workDir, "commit", "-m", "main")
	runGitForMQSubmitTest(t, workDir, "branch", "-M", "main")
	runGitForMQSubmitTest(t, workDir, "push", "-u", "origin", "main")
	branch := "feature/no-new-work"
	runGitForMQSubmitTest(t, workDir, "checkout", "-b", branch)
	runGitForMQSubmitTest(t, workDir, "push", "origin", branch)
	return branch
}

func installSubmitSourceBDStub(t *testing.T, currentBeadsDir, ownerBeadsDir string, ownerMissing bool) {
	t.Helper()
	installSubmitSourceBDStubWithOwnerJSON(t, currentBeadsDir, ownerBeadsDir, ownerMissing,
		`[{"id":"bd-source","title":"owner source","status":"open","priority":1,"issue_type":"task"}]`)
}

func installSubmitSourceBDStubWithOwnerJSON(t *testing.T, currentBeadsDir, ownerBeadsDir string, ownerMissing bool, ownerJSON string) {
	t.Helper()
	binDir := t.TempDir()
	// Single-quoted so the JSON's double quotes survive /bin/sh verbatim.
	ownerCase := fmt.Sprintf(`
if [ "$BEADS_DIR" = %q ]; then
  echo '%s'
  exit 0
fi`, ownerBeadsDir, ownerJSON)
	if ownerMissing {
		ownerCase = fmt.Sprintf(`
if [ "$BEADS_DIR" = %q ]; then
  echo "Issue not found in owner" >&2
  exit 1
fi`, ownerBeadsDir)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  shift
fi
if [ "$1" = "version" ]; then
  echo "bd stub"
  exit 0
fi
if [ "$1" = "show" ] && [ "$2" = "bd-source" ]; then
  if [ "$BEADS_DIR" = %q ]; then
    echo '[{"id":"bd-source","title":"current mirror","status":"open","priority":1,"issue_type":"task"}]'
    exit 0
  fi
%s
  echo "Issue not found in $BEADS_DIR" >&2
  exit 1
fi
echo "unexpected bd command: $*" >&2
exit 1
`, currentBeadsDir, ownerCase)
	path := filepath.Join(binDir, "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	beads.ResetBdAllowStaleCacheForTest()
}

func installSubmitSourceBDRecorder(t *testing.T, currentBeadsDir, ownerBeadsDir string) string {
	t.Helper()
	return installSubmitSourceBDRecorderWithStatus(t, currentBeadsDir, ownerBeadsDir, "open")
}

func installSubmitSourceBDRecorderWithStatus(t *testing.T, currentBeadsDir, ownerBeadsDir, sourceStatus string) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd.log")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  shift
fi
if [ "$1" = "version" ]; then
  echo "bd stub"
  exit 0
fi
printf '%%s\t%%s\n' "$BEADS_DIR" "$*" >> %q
if [ "$1" = "show" ] && [ "$2" = "bd-source" ]; then
  if [ "$BEADS_DIR" = %q ]; then
    echo '[{"id":"bd-source","title":"current mirror","status":"%s","priority":1,"issue_type":"task","description":"convoy_id: hq-cv-test\\nmerge_strategy: mr"}]'
    exit 0
  fi
  if [ "$BEADS_DIR" = %q ]; then
    echo '[{"id":"bd-source","title":"owner source","status":"%s","priority":1,"issue_type":"task","description":"convoy_id: hq-cv-test\\nmerge_strategy: mr"}]'
    exit 0
  fi
  echo "Issue not found in $BEADS_DIR" >&2
  exit 1
fi
if [ "$1" = "show" ] && [ "$2" = "gt-mr" ]; then
  echo '[{"id":"gt-mr","title":"Merge: bd-source","status":"open","priority":1,"issue_type":"task","labels":["gt:merge-request"],"description":"branch: feature/routed-submit\\ntarget: main\\nsource_issue: bd-source\\nrig: gastown"}]'
  exit 0
fi
if [ "$1" = "list" ]; then
  echo '[]'
  exit 0
fi
if [ "$1" = "sql" ]; then
  echo '[]'
  exit 0
fi
if [ "$1" = "create" ]; then
  echo '{"id":"gt-mr","title":"Merge: bd-source","status":"open","priority":1,"issue_type":"task","labels":["gt:merge-request"]}'
  exit 0
fi
if [ "$1" = "comments" ] && [ "$2" = "add" ]; then
  exit 0
fi
if [ "$1" = "close" ]; then
  exit 0
fi
echo "unexpected bd command: $*" >&2
exit 1
`, logPath, currentBeadsDir, sourceStatus, ownerBeadsDir, sourceStatus)
	path := filepath.Join(binDir, "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write bd recorder: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)
	return logPath
}

func readSubmitSourceBDLog(t *testing.T, logPath string) string {
	t.Helper()
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd recorder log: %v", err)
	}
	return string(log)
}

func assertBDLogContains(t *testing.T, log, beadsDir, args string) {
	t.Helper()
	needle := beadsDir + "\t" + args
	if !strings.Contains(log, needle) {
		t.Fatalf("bd log missing %q:\n%s", needle, log)
	}
}

func assertBDLogNotContains(t *testing.T, log, beadsDir, args string) {
	t.Helper()
	needle := beadsDir + "\t" + args
	if strings.Contains(log, needle) {
		t.Fatalf("bd log unexpectedly contains %q:\n%s", needle, log)
	}
}

func resetMqSubmitFlagsForTest(t *testing.T) {
	t.Helper()
	oldBranch, oldIssue, oldEpic := mqSubmitBranch, mqSubmitIssue, mqSubmitEpic
	oldPriority := mqSubmitPriority
	oldNoCleanup, oldSkipDeps, oldResubmit := mqSubmitNoCleanup, mqSubmitSkipDeps, mqSubmitResubmit
	oldAllowClosed := mqSubmitAllowClosedIssue
	oldAllowNoOp := mqSubmitAllowNoOp
	mqSubmitBranch, mqSubmitIssue, mqSubmitEpic = "", "", ""
	mqSubmitPriority = -1
	mqSubmitNoCleanup, mqSubmitSkipDeps, mqSubmitResubmit = false, false, false
	mqSubmitAllowClosedIssue = false
	mqSubmitAllowNoOp = false
	t.Cleanup(func() {
		mqSubmitBranch, mqSubmitIssue, mqSubmitEpic = oldBranch, oldIssue, oldEpic
		mqSubmitPriority = oldPriority
		mqSubmitNoCleanup, mqSubmitSkipDeps, mqSubmitResubmit = oldNoCleanup, oldSkipDeps, oldResubmit
		mqSubmitAllowClosedIssue = oldAllowClosed
		mqSubmitAllowNoOp = oldAllowNoOp
	})
}

func resetDoneFlagsForTest(t *testing.T) {
	t.Helper()
	oldIssue, oldStatus, oldCleanupStatus, oldTarget := doneIssue, doneStatus, doneCleanupStatus, doneTarget
	oldPriority := donePriority
	oldResume, oldPreVerified, oldSkipVerify := doneResume, donePreVerified, doneSkipVerify
	oldUpdateAgentStateOnDoneFn := updateAgentStateOnDoneFn
	doneIssue = ""
	donePriority = -1
	doneStatus = ExitCompleted
	doneCleanupStatus = ""
	doneResume = false
	donePreVerified = false
	doneTarget = ""
	doneSkipVerify = false
	t.Cleanup(func() {
		doneIssue, doneStatus, doneCleanupStatus, doneTarget = oldIssue, oldStatus, oldCleanupStatus, oldTarget
		donePriority = oldPriority
		doneResume, donePreVerified, doneSkipVerify = oldResume, oldPreVerified, oldSkipVerify
		updateAgentStateOnDoneFn = oldUpdateAgentStateOnDoneFn
	})
}
