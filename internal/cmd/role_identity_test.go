package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

// Regression tests for gt-mm0: role-agent identity must decide BOTH the beads
// database and the assignee address form, in one place.
//
// Defect 1 — the beads dir was derived from cwd or from the sling target, which
// for a role target is a directory with no .beads at all.
// Defect 2 — role assignees were written bare ("deacon") and queried with a
// trailing slash ("deacon/"), so hooked work went invisible and the agent idled.

// ===== Defect 1: beads dir comes from identity, not cwd/target =====

func newTownForRoleIdentityTest(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	return townRoot
}

// TestSlingToRoleTargetResolvesTownBeadsDir covers the Mayor's repro:
//
//	gt sling mol-deacon-patrol deacon
//	  Error: checking existing hooked formulas for deacon/: ...
//	         no beads database found
//
// resolveTarget hands back <town>/deacon as the target work dir; every bd call
// in runSlingFormula routes through it.
func TestSlingToRoleTargetResolvesTownBeadsDir(t *testing.T) {
	// daemon is a directory, not an addressable agent; it is covered by the
	// cwd-rooted fallback (see TestCompactResolvesWorkDirFromIdentity and
	// beads.TestResolveBeadsDirWithTownFallbackUsesTownForRoleDirs).
	townRoot := newTownForRoleIdentityTest(t)
	for _, role := range []string{"deacon", "mayor"} {
		roleDir := filepath.Join(townRoot, role)
		if err := os.MkdirAll(roleDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", role, err)
		}
		addr := beads.CanonicalAgentAddress(role)
		if got := beads.AgentBeadsWorkDir(addr, roleDir, townRoot); got != townRoot {
			t.Errorf("formula work dir for %s target = %q, want town root %q", role, got, townRoot)
		}
	}
}

// TestSlingToPolecatTargetKeepsWorktree is the known-good control from the
// bead: slinging to rig polecats already worked and must keep working.
func TestSlingToPolecatTargetKeepsWorktree(t *testing.T) {
	townRoot := newTownForRoleIdentityTest(t)
	polecatDir := filepath.Join(townRoot, "duly_noted", "polecats", "obsidian")
	if err := os.MkdirAll(filepath.Join(polecatDir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir polecat beads: %v", err)
	}
	if got := beads.AgentBeadsWorkDir("duly_noted/polecats/obsidian", polecatDir, townRoot); got != polecatDir {
		t.Errorf("formula work dir for polecat target = %q, want %q", got, polecatDir)
	}
}

// TestSlingFormulaResolvesWorkDirFromIdentity pins the wiring: the old
// "only when empty" fallback never fired for role targets, because a role
// target's work dir is non-empty and merely beads-less.
func TestSlingFormulaResolvesWorkDirFromIdentity(t *testing.T) {
	body := runSlingFormulaSourceForTest(t)
	if !strings.Contains(body, "formulaWorkDir = beads.AgentBeadsWorkDir(targetAgent, formulaWorkDir, townRoot)") {
		t.Fatal("runSlingFormula must resolve its bd work dir from identity via beads.AgentBeadsWorkDir")
	}
	if strings.Contains(body, `if formulaWorkDir == "" {`) {
		t.Fatal("the empty-only work dir fallback is what left role targets pointed at a beads-less directory")
	}
}

// TestCompactResolvesWorkDirFromIdentity covers `gt compact` run from deacon/,
// which failed with no workaround other than cd'ing to the town root.
func TestCompactResolvesWorkDirFromIdentity(t *testing.T) {
	for _, file := range []string{"compact.go", "compact_report.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		source := string(data)
		if strings.Contains(source, "beads.New(workDir)") {
			t.Errorf("%s still derives its beads database from cwd alone", file)
		}
		if !strings.Contains(source, "beads.BeadsWorkDirWithTownFallback(workDir") {
			t.Errorf("%s must resolve its beads database from identity", file)
		}
	}
}

// TestAgentStateResolvesBeadsDirRegardlessOfCwd guards the await-event path:
// the state database follows the agent bead, not the cwd. Without it the idle
// counter never increments and exponential backoff stays pinned at its base
// interval — continuous token burn on an idle rig.
func TestAgentStateResolvesBeadsDirRegardlessOfCwd(t *testing.T) {
	data, err := os.ReadFile("molecule_await_event.go")
	if err != nil {
		t.Fatalf("read molecule_await_event.go: %v", err)
	}
	if !strings.Contains(string(data), "resolveAgentStateBeadsDir(awaitEventAgentBead)") {
		t.Fatal("await-event must resolve the agent state database from the agent bead, not from cwd")
	}
}

// ===== Defect 2: assignee address forms =====

type recordingLister struct {
	queried []string
	byForm  map[string][]*beads.Issue
	err     error
}

func (r *recordingLister) list(opts beads.ListOptions) ([]*beads.Issue, error) {
	r.queried = append(r.queried, opts.Assignee)
	if r.err != nil {
		return nil, r.err
	}
	return r.byForm[opts.Assignee], nil
}

// TestHookFindsWispWrittenWithBareRoleAssignee is the ACTUAL patrol-loop
// staller: deacon verified hq-wisp-8vo was hooked under assignee "deacon" while
// gt hook queried "deacon/" and reported "Nothing on hook".
func TestHookFindsWispWrittenWithBareRoleAssignee(t *testing.T) {
	stalled := &beads.Issue{ID: "hq-wisp-8vo", Status: beads.StatusHooked, UpdatedAt: "2026-08-01T00:00:00Z"}
	lister := &recordingLister{byForm: map[string][]*beads.Issue{"deacon": {stalled}}}

	got, err := listAcrossAssigneeForms(lister.list, beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: "deacon/",
		Priority: -1,
	})
	if err != nil {
		t.Fatalf("listAcrossAssigneeForms: %v", err)
	}
	if len(got) != 1 || got[0].ID != "hq-wisp-8vo" {
		t.Fatalf("got %v, want the wisp hooked under the bare form", got)
	}
	if len(lister.queried) != 2 {
		t.Fatalf("queried %v, want both address forms", lister.queried)
	}
}

// TestHookAddressFormsDoNotDoubleCount: one bead visible under both forms is
// still one bead.
func TestHookAddressFormsDoNotDoubleCount(t *testing.T) {
	dupe := &beads.Issue{ID: "hq-wisp-dup", Status: beads.StatusHooked}
	lister := &recordingLister{byForm: map[string][]*beads.Issue{
		"deacon/": {dupe},
		"deacon":  {dupe},
	}}

	got, err := listAcrossAssigneeForms(lister.list, beads.ListOptions{Assignee: "deacon"})
	if err != nil {
		t.Fatalf("listAcrossAssigneeForms: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d beads, want 1 after dedup: %v", len(got), got)
	}
}

// TestRigAgentsKeepSingleAssigneeQuery is the no-regression control: rig agents
// hold "duly_noted/witness" / "duly_noted/refinery" and were never affected.
func TestRigAgentsKeepSingleAssigneeQuery(t *testing.T) {
	for _, agent := range []string{
		"duly_noted/witness",
		"duly_noted/refinery",
		"gastown/polecats/toast",
		"gastown/crew/joe",
		"deacon/dogs/alpha",
	} {
		lister := &recordingLister{byForm: map[string][]*beads.Issue{}}
		if _, err := listAcrossAssigneeForms(lister.list, beads.ListOptions{Assignee: agent}); err != nil {
			t.Fatalf("listAcrossAssigneeForms(%s): %v", agent, err)
		}
		if len(lister.queried) != 1 || lister.queried[0] != agent {
			t.Errorf("queried %v for %s, want exactly one unchanged query", lister.queried, agent)
		}
	}
}

func TestListAcrossAssigneeFormsPropagatesError(t *testing.T) {
	want := errors.New("bd exploded")
	lister := &recordingLister{err: want}
	if _, err := listAcrossAssigneeForms(lister.list, beads.ListOptions{Assignee: "deacon/"}); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestListAcrossAssigneeFormsHonoursLimit(t *testing.T) {
	lister := &recordingLister{byForm: map[string][]*beads.Issue{
		"deacon/": {{ID: "a", UpdatedAt: "2026-08-03T00:00:00Z"}},
		"deacon":  {{ID: "b", UpdatedAt: "2026-08-02T00:00:00Z"}, {ID: "c", UpdatedAt: "2026-08-01T00:00:00Z"}},
	}}
	got, err := listAcrossAssigneeForms(lister.list, beads.ListOptions{Assignee: "deacon", Limit: 2})
	if err != nil {
		t.Fatalf("listAcrossAssigneeForms: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d beads, want 2 (limit applied after merge)", len(got))
	}
}

// TestPatrolWritesCanonicalRoleAssignee closes the write half of the migration:
// gt patrol report / gt patrol new / gt prime wrote the next patrol wisp as
// assignee "deacon" while every reader queried "deacon/".
func TestPatrolWritesCanonicalRoleAssignee(t *testing.T) {
	for _, file := range []string{"patrol_report.go", "patrol_new.go", "prime_molecule.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		source := string(data)
		if strings.Contains(source, `Assignee:      "deacon",`) || strings.Contains(source, `Assignee: "deacon",`) {
			t.Errorf("%s still writes the bare role assignee; use beads.CanonicalAgentAddress", file)
		}
		if !strings.Contains(source, `beads.CanonicalAgentAddress("deacon")`) {
			t.Errorf("%s must write the canonical role assignee", file)
		}
	}
}

// TestPatrolRigNameIgnoresTownLevelRole: "deacon/" splits into a first segment
// that is a role, not a rig. Reading it as a rig would render the patrol wisp
// against a rig named "deacon".
func TestPatrolRigNameIgnoresTownLevelRole(t *testing.T) {
	for _, assignee := range []string{"deacon", "deacon/", "mayor/"} {
		if got := patrolRigName(PatrolConfig{Assignee: assignee}); got != "" {
			t.Errorf("patrolRigName(%q) = %q, want empty", assignee, got)
		}
	}
	for assignee, want := range map[string]string{
		"gastown/witness":     "gastown",
		"duly_noted/refinery": "duly_noted",
	} {
		if got := patrolRigName(PatrolConfig{Assignee: assignee}); got != want {
			t.Errorf("patrolRigName(%q) = %q, want %q", assignee, got, want)
		}
	}
}

// TestRoleAddressNormalizationHasOneOwner: three independent TrimSuffix sites
// were the shape that let some paths strip the slash and others not.
func TestRoleAddressNormalizationHasOneOwner(t *testing.T) {
	for _, file := range []string{"role.go", "status.go", "sling_helpers.go", "sling_target.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if strings.Contains(string(data), `strings.TrimSuffix(s, "/")`) ||
			strings.Contains(string(data), `strings.TrimSuffix(agentID, "/")`) ||
			strings.Contains(string(data), `strings.TrimSuffix(agent.Address, "/")`) {
			t.Errorf("%s still normalizes role addresses ad hoc; use beads.BareAgentAddress", file)
		}
	}
}

// TestRoleConstantsMatchCanonicalAddresses guards the assumption that the
// town-level slash roles are exactly mayor and deacon.
func TestRoleConstantsMatchCanonicalAddresses(t *testing.T) {
	if beads.CanonicalAgentAddress(constants.RoleMayor) != "mayor/" {
		t.Errorf("mayor is not canonicalized to a slash address")
	}
	if beads.CanonicalAgentAddress(constants.RoleDeacon) != "deacon/" {
		t.Errorf("deacon is not canonicalized to a slash address")
	}
	if !isTownLevelRole(beads.CanonicalAgentAddress(constants.RoleDeacon)) {
		t.Errorf("isTownLevelRole rejects the canonical deacon address")
	}
}
