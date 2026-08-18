package polecat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
)

// TestSetupSharedBeadsPinsMaintainerRole covers gt-k3h / gt-2ta.
//
// bd routes a repo's beads by beads.role. Under "contributor" it silently
// redirects create and list into the caller's personal planning store — still
// exiting 0 with a real bead ID — so work a polecat filed with a bare
// `bd create` became invisible to the Witness, Mayor and Refinery and could
// never be updated or closed. A rig's beads ARE the project's beads, so
// provisioning a sandbox must leave the role as maintainer.
func TestSetupSharedBeadsPinsMaintainerRole(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")
	clonePath := filepath.Join(rigPath, "polecats", "thunder")

	// Town-level beads gives SetupRedirect a target to point the sandbox at.
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads", "dolt"), 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	if err := os.MkdirAll(clonePath, 0o755); err != nil {
		t.Fatalf("mkdir clone: %v", err)
	}
	runGit(t, clonePath, "init")

	m := &Manager{rig: &rig.Rig{Name: "testrig", Path: rigPath}}
	if err := m.setupSharedBeads(clonePath); err != nil {
		t.Fatalf("setupSharedBeads: %v", err)
	}

	if got := gitConfig(t, clonePath, "beads.role"); got != "maintainer" {
		t.Fatalf("beads.role = %q, want maintainer — contributor strands every bead the sandbox files", got)
	}
}

// TestSetupSharedBeadsRoleIsRepoWide records why the value above matters beyond
// the sandbox being provisioned: polecat sandboxes are worktrees that share one
// config file, so writing the role from a per-sandbox setup step rewrites it for
// the rig root and every sibling sandbox as well.
func TestSetupSharedBeadsRoleIsRepoWide(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads", "dolt"), 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	runGit(t, rigPath, "init")
	runGit(t, rigPath, "config", "user.email", "test@example.com")
	runGit(t, rigPath, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(rigPath, "README.md"), []byte("rig\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, rigPath, "add", "README.md")
	runGit(t, rigPath, "commit", "-m", "init")

	clonePath := filepath.Join(rigPath, "polecats", "thunder")
	runGit(t, rigPath, "worktree", "add", "-b", "polecat/thunder", clonePath)

	m := &Manager{rig: &rig.Rig{Name: "testrig", Path: rigPath}}
	if err := m.setupSharedBeads(clonePath); err != nil {
		t.Fatalf("setupSharedBeads: %v", err)
	}

	// The rig root is a different working tree, yet it reads the same value.
	if got := gitConfig(t, rigPath, "beads.role"); got != "maintainer" {
		t.Fatalf("rig root beads.role = %q, want maintainer", got)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitConfig(t *testing.T, dir, key string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "config", "--get", key).Output()
	if err != nil {
		t.Fatalf("git config --get %s in %s: %v", key, dir, err)
	}
	return strings.TrimSpace(string(out))
}
