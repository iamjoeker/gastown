package beads

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// newTownForIdentityTest builds a town root with a town-level .beads directory,
// which is what every role agent's beads actually live in.
func newTownForIdentityTest(t *testing.T) string {
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

func TestCanonicalAgentAddress(t *testing.T) {
	cases := map[string]string{
		"deacon":                   "deacon/",
		"deacon/":                  "deacon/",
		"  deacon  ":               "deacon/",
		"mayor":                    "mayor/",
		"mayor/":                   "mayor/",
		"deacon/boot":              "deacon/boot",
		"deacon/dogs/alpha":        "deacon/dogs/alpha",
		"gastown/witness":          "gastown/witness",
		"gastown/refinery":         "gastown/refinery",
		"gastown/polecats/toast":   "gastown/polecats/toast",
		"duly_noted/refinery":      "duly_noted/refinery",
		"":                         "",
		"gastown/crew/joe":         "gastown/crew/joe",
		"mayorish":                 "mayorish",
		"deaconess/polecats/toast": "deaconess/polecats/toast",
	}
	for in, want := range cases {
		if got := CanonicalAgentAddress(in); got != want {
			t.Errorf("CanonicalAgentAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentAddressFormsCoversBothRoleForms(t *testing.T) {
	// The wisp that stalled the deacon patrol loop was written as "deacon" and
	// queried as "deacon/". Both must be returned, canonical first.
	for _, in := range []string{"deacon", "deacon/"} {
		got := AgentAddressForms(in)
		want := []string{"deacon/", "deacon"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("AgentAddressForms(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"mayor", "mayor/"} {
		got := AgentAddressForms(in)
		want := []string{"mayor/", "mayor"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("AgentAddressForms(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestAgentAddressFormsLeavesRigAgentsSingleForm(t *testing.T) {
	// Control case: rig agents were never affected and must not gain a second
	// query per lookup.
	for _, in := range []string{
		"gastown/witness",
		"gastown/refinery",
		"duly_noted/refinery",
		"gastown/polecats/toast",
		"deacon/boot",
		"deacon/dogs/alpha",
	} {
		got := AgentAddressForms(in)
		if !reflect.DeepEqual(got, []string{in}) {
			t.Errorf("AgentAddressForms(%q) = %v, want single form", in, got)
		}
	}
	if got := AgentAddressForms(""); got != nil {
		t.Errorf("AgentAddressForms(\"\") = %v, want nil", got)
	}
}

func TestResolveBeadsDirWithTownFallbackUsesTownForRoleDirs(t *testing.T) {
	townRoot := newTownForIdentityTest(t)
	townBeads := filepath.Join(townRoot, ".beads")

	// Role directories exist but hold no .beads — this is the shape that made
	// `gt sling <formula> deacon` die with "no beads database found".
	for _, role := range []string{"deacon", "mayor", "daemon"} {
		roleDir := filepath.Join(townRoot, role)
		if err := os.MkdirAll(roleDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", role, err)
		}
		if got := ResolveBeadsDirWithTownFallback(roleDir, townRoot); got != townBeads {
			t.Errorf("ResolveBeadsDirWithTownFallback(%s) = %q, want %q", role, got, townBeads)
		}
		if got := BeadsWorkDirWithTownFallback(roleDir, townRoot); got != townRoot {
			t.Errorf("BeadsWorkDirWithTownFallback(%s) = %q, want %q", role, got, townRoot)
		}
	}
}

func TestResolveBeadsDirWithTownFallbackFindsTownRootWhenNotSupplied(t *testing.T) {
	townRoot := newTownForIdentityTest(t)
	roleDir := filepath.Join(townRoot, "deacon")
	if err := os.MkdirAll(roleDir, 0o755); err != nil {
		t.Fatalf("mkdir deacon: %v", err)
	}

	want := filepath.Join(townRoot, ".beads")
	if got := ResolveBeadsDirWithTownFallback(roleDir, ""); got != want {
		t.Errorf("ResolveBeadsDirWithTownFallback(deacon, \"\") = %q, want %q", got, want)
	}
	if got := BeadsWorkDirWithTownFallback(roleDir, ""); got != townRoot {
		t.Errorf("BeadsWorkDirWithTownFallback(deacon, \"\") = %q, want %q", got, townRoot)
	}
}

func TestResolveBeadsDirWithTownFallbackKeepsPolecatWorktree(t *testing.T) {
	// Known-good control: rig polecat dirs DO have .beads, and slinging to them
	// already worked. Identity resolution must not move them.
	townRoot := newTownForIdentityTest(t)
	polecatDir := filepath.Join(townRoot, "gastown", "polecats", "toast")
	polecatBeads := filepath.Join(polecatDir, ".beads")
	if err := os.MkdirAll(polecatBeads, 0o755); err != nil {
		t.Fatalf("mkdir polecat beads: %v", err)
	}

	if got := ResolveBeadsDirWithTownFallback(polecatDir, townRoot); got != polecatBeads {
		t.Errorf("ResolveBeadsDirWithTownFallback(polecat) = %q, want %q", got, polecatBeads)
	}
	if got := BeadsWorkDirWithTownFallback(polecatDir, townRoot); got != polecatDir {
		t.Errorf("BeadsWorkDirWithTownFallback(polecat) = %q, want %q", got, polecatDir)
	}
}

func TestResolveBeadsDirWithTownFallbackFollowsWorktreeRedirect(t *testing.T) {
	// Rig worktrees reach their database through a redirect. The work dir must
	// stay put (bd's cwd matters for routing) even though the beads dir moves.
	townRoot := newTownForIdentityTest(t)
	rigBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigBeads, 0o755); err != nil {
		t.Fatalf("mkdir rig beads: %v", err)
	}
	refineryDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	if err := os.MkdirAll(filepath.Join(refineryDir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir refinery beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(refineryDir, ".beads", "redirect"), []byte("../../mayor/rig/.beads\n"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	if got := ResolveBeadsDirWithTownFallback(refineryDir, townRoot); got != rigBeads {
		t.Errorf("ResolveBeadsDirWithTownFallback(refinery) = %q, want %q", got, rigBeads)
	}
	if got := BeadsWorkDirWithTownFallback(refineryDir, townRoot); got != refineryDir {
		t.Errorf("BeadsWorkDirWithTownFallback(refinery) = %q, want unchanged %q", got, refineryDir)
	}
}

func TestResolveBeadsDirWithTownFallbackKeepsDiagnosticWithoutTown(t *testing.T) {
	// Outside a town there is nothing to fall back to; callers must keep
	// reporting the same missing-database diagnostic they always have.
	orphan := filepath.Join(t.TempDir(), "deacon")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	want := filepath.Join(orphan, ".beads")
	if got := ResolveBeadsDirWithTownFallback(orphan, ""); got != want {
		t.Errorf("ResolveBeadsDirWithTownFallback(orphan) = %q, want %q", got, want)
	}
	if got := BeadsWorkDirWithTownFallback(orphan, ""); got != orphan {
		t.Errorf("BeadsWorkDirWithTownFallback(orphan) = %q, want %q", got, orphan)
	}
}

func TestBeadsWorkDirWithTownFallbackWhenWorkDirEmpty(t *testing.T) {
	townRoot := newTownForIdentityTest(t)
	if got := BeadsWorkDirWithTownFallback("", townRoot); got != townRoot {
		t.Errorf("BeadsWorkDirWithTownFallback(\"\", townRoot) = %q, want %q", got, townRoot)
	}
	if got := ResolveBeadsDirWithTownFallback("", townRoot); got != filepath.Join(townRoot, ".beads") {
		t.Errorf("ResolveBeadsDirWithTownFallback(\"\", townRoot) = %q, want town beads", got)
	}
}

func TestIsTownScopedAgent(t *testing.T) {
	for _, in := range []string{"mayor", "mayor/", "deacon", "deacon/", "deacon/boot", "deacon/dogs/alpha"} {
		if !IsTownScopedAgent(in) {
			t.Errorf("IsTownScopedAgent(%q) = false, want true", in)
		}
	}
	for _, in := range []string{
		"",
		"gastown/witness",
		"gastown/refinery",
		"gastown/polecats/toast",
		"gastown/crew/joe",
		"deaconess/witness",
	} {
		if IsTownScopedAgent(in) {
			t.Errorf("IsTownScopedAgent(%q) = true, want false", in)
		}
	}
}

func TestAgentBeadsWorkDirSendsRoleAgentsToTown(t *testing.T) {
	// The sling-side defect: resolveTarget hands back the ROLE's own directory,
	// which is non-empty and beads-less, so the old "only when empty" fallback
	// never fired and every bd call died with "no beads database found".
	townRoot := "/town"
	for _, agent := range []string{"deacon/", "deacon", "mayor/", "deacon/boot", "deacon/dogs/alpha"} {
		if got := AgentBeadsWorkDir(agent, "/town/deacon", townRoot); got != townRoot {
			t.Errorf("AgentBeadsWorkDir(%q) = %q, want %q", agent, got, townRoot)
		}
	}
}

func TestAgentBeadsWorkDirKeepsRigAgentWorktree(t *testing.T) {
	// Known-good control: rig polecats, crew, witness and refinery already
	// worked and must keep their own worktree — including a freshly spawned
	// polecat clone whose .beads has not been written yet, which must fail
	// loudly rather than have its wisp silently land in the town database.
	cases := map[string]string{
		"gastown/polecats/toast": "/town/gastown/polecats/toast",
		"gastown/crew/joe":       "/town/gastown/crew/joe",
		"gastown/witness":        "/town/gastown/witness",
		"duly_noted/refinery":    "/town/duly_noted/refinery/rig",
	}
	for agent, workDir := range cases {
		if got := AgentBeadsWorkDir(agent, workDir, "/town"); got != workDir {
			t.Errorf("AgentBeadsWorkDir(%q) = %q, want %q", agent, got, workDir)
		}
	}
}

func TestAgentBeadsWorkDirKeepsEmptyWorkDirFallback(t *testing.T) {
	// Preserved from the original fallback: no work dir at all means town root.
	if got := AgentBeadsWorkDir("gastown/witness", "", "/town"); got != "/town" {
		t.Errorf("AgentBeadsWorkDir(empty work dir) = %q, want /town", got)
	}
	// With no town root either there is nothing to substitute.
	if got := AgentBeadsWorkDir("deacon/", "/town/deacon", ""); got != "/town/deacon" {
		t.Errorf("AgentBeadsWorkDir(no town root) = %q, want the work dir unchanged", got)
	}
}

func TestIsTownLevelSlashRole(t *testing.T) {
	for _, in := range []string{"deacon", "deacon/", "mayor", "mayor/"} {
		if !IsTownLevelSlashRole(in) {
			t.Errorf("IsTownLevelSlashRole(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"", "deacon/boot", "deacon/dogs/alpha", "gastown/witness", "gastown"} {
		if IsTownLevelSlashRole(in) {
			t.Errorf("IsTownLevelSlashRole(%q) = true, want false", in)
		}
	}
}

func TestBareAgentAddress(t *testing.T) {
	cases := map[string]string{
		"deacon/":        "deacon",
		"deacon":         "deacon",
		" mayor/ ":       "mayor",
		"gastown/crew/j": "gastown/crew/j",
		"":               "",
	}
	for in, want := range cases {
		if got := BareAgentAddress(in); got != want {
			t.Errorf("BareAgentAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
