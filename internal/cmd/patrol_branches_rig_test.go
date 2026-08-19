package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTown builds a town root whose registry names the given rigs, and creates
// only the directories asked for. A rig that is registered but has no
// repository is the interesting case, so the two are separate arguments.
func fakeTown(t *testing.T, registered []string, dirs []string) string {
	t.Helper()
	townRoot := t.TempDir()

	entries := make([]string, 0, len(registered))
	for _, name := range registered {
		entries = append(entries, `"`+name+`": {"git_url": "git@example.com:x/`+name+`.git"}`)
	}
	registry := `{"version": 1, "rigs": {` + strings.Join(entries, ", ") + `}}`
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(townRoot, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return townRoot
}

// chdir moves into dir for the duration of the test. inferRigFromCwd reads the
// process working directory, so there is no way to exercise it without this.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func TestResolvePatrolBranchesRigRejectsNonRigDirectory(t *testing.T) {
	t.Setenv("GT_RIG", "")
	// The reported shape: cwd is the town's own mayor/, which is a directory
	// under the town root but is not a rig. Inference returns "mayor" happily.
	townRoot := fakeTown(t, []string{"beads", "gastown"}, []string{"beads", "gastown"})
	chdir(t, filepath.Join(townRoot, "mayor"))

	// Fixture control: the bad inference this guards against really does fire
	// here, so a pass below is the guard working and not the setup missing.
	if inferred, inferErr := inferRigFromCwd(townRoot); inferErr != nil || inferred != "mayor" {
		t.Fatalf("fixture does not reproduce the bug: inferRigFromCwd = %q, %v", inferred, inferErr)
	}

	name, _, err := resolvePatrolBranchesRig(townRoot, "")
	if err == nil {
		t.Fatalf("expected a refusal, got rig %q", name)
	}
	if !strings.Contains(err.Error(), `"mayor" is not a registered rig`) {
		t.Errorf("error must name what was inferred and why it was rejected, got: %v", err)
	}
	if !strings.Contains(err.Error(), "beads, gastown") {
		t.Errorf("error must list the rigs that could be passed instead, got: %v", err)
	}
}

func TestResolvePatrolBranchesRigAcceptsRealRig(t *testing.T) {
	t.Setenv("GT_RIG", "")
	townRoot := fakeTown(t, []string{"gastown"}, []string{"gastown/mayor/rig"})
	chdir(t, filepath.Join(townRoot, "gastown", "mayor", "rig"))

	name, source, err := resolvePatrolBranchesRig(townRoot, "")
	if err != nil {
		t.Fatalf("inference from inside a rig must succeed: %v", err)
	}
	if name != "gastown" {
		t.Errorf("name = %q, want gastown", name)
	}
	if !strings.Contains(source, "inferred") {
		t.Errorf("source = %q, want it to say the name was inferred", source)
	}
}

// A single-rig town has only one answer, so a cwd that yields nothing is not a
// reason to fail. Two rigs is ambiguous and must still fail.
func TestResolvePatrolBranchesRigDefaultsToTheOnlyRig(t *testing.T) {
	t.Setenv("GT_RIG", "")
	townRoot := fakeTown(t, []string{"gastown"}, []string{"gastown"})
	chdir(t, filepath.Join(townRoot, "mayor"))

	name, source, err := resolvePatrolBranchesRig(townRoot, "")
	if err != nil {
		t.Fatalf("a one-rig town has an unambiguous answer: %v", err)
	}
	if name != "gastown" {
		t.Errorf("name = %q, want gastown", name)
	}
	if !strings.Contains(source, "only registered rig") {
		t.Errorf("source = %q must say the name was defaulted, not inferred", source)
	}
}

// An explicit name is answered as asked, even when it is wrong: substituting a
// different rig would answer a question nobody put. Being wrong is caught a
// step later, by ensureRigRepoUsable.
func TestResolvePatrolBranchesRigHonoursExplicitName(t *testing.T) {
	t.Setenv("GT_RIG", "beads")
	townRoot := fakeTown(t, []string{"gastown"}, []string{"gastown"})
	chdir(t, filepath.Join(townRoot, "gastown"))

	name, source, err := resolvePatrolBranchesRig(townRoot, "mayor")
	if err != nil || name != "mayor" {
		t.Fatalf("--rig must win: name=%q err=%v", name, err)
	}
	if !strings.Contains(source, "--rig") {
		t.Errorf("source = %q, want it to credit --rig", source)
	}

	name, source, err = resolvePatrolBranchesRig(townRoot, "")
	if err != nil || name != "beads" {
		t.Fatalf("GT_RIG must win over inference: name=%q err=%v", name, err)
	}
	if !strings.Contains(source, "GT_RIG") {
		t.Errorf("source = %q, want it to credit GT_RIG", source)
	}
}

// An unreadable registry must not veto a guess it could not check. There is no
// way to distinguish "no rigs" from "could not read", so the check is skipped
// and the repository check downstream is what catches a bad name.
func TestResolvePatrolBranchesRigToleratesNoRegistry(t *testing.T) {
	t.Setenv("GT_RIG", "")
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, filepath.Join(townRoot, "gastown"))

	name, _, err := resolvePatrolBranchesRig(townRoot, "")
	if err != nil {
		t.Fatalf("a missing registry must not block inference: %v", err)
	}
	if name != "gastown" {
		t.Errorf("name = %q, want gastown", name)
	}
}

func TestEnsureRigRepoUsableNamesBothCandidates(t *testing.T) {
	townRoot := fakeTown(t, []string{"mayor"}, []string{"mayor"})
	rigPath := filepath.Join(townRoot, "mayor")

	err := ensureRigRepoUsable("mayor", rigPath, "from --rig", getRepoGitForRig(rigPath))
	if err == nil {
		t.Fatal("a rig directory with no repository must be refused before git runs")
	}
	bare, worktree := rigRepoCandidates(rigPath)
	for _, want := range []string{bare, worktree, "from --rig", "--rig <name>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q, got: %v", want, err)
		}
	}
}

func TestEnsureRigRepoUsableAcceptsBothLayouts(t *testing.T) {
	townRoot := t.TempDir()

	worktreeRig := filepath.Join(townRoot, "worktree")
	if err := os.MkdirAll(filepath.Join(worktreeRig, "mayor", "rig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureRigRepoUsable("worktree", worktreeRig, "from --rig", getRepoGitForRig(worktreeRig)); err != nil {
		t.Errorf("a mayor worktree must be accepted: %v", err)
	}

	// A bare mirror gives git --git-dir and no working directory at all, so
	// there is nothing to chdir into and nothing to check.
	bareRig := filepath.Join(townRoot, "bare")
	if err := os.MkdirAll(filepath.Join(bareRig, ".repo.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureRigRepoUsable("bare", bareRig, "from --rig", getRepoGitForRig(bareRig)); err != nil {
		t.Errorf("a bare mirror must be accepted: %v", err)
	}
}
