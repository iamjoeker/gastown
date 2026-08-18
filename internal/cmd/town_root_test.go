package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runTownRoot executes townRootCmd with stdout captured, the way a shell
// script consumes it: `TOWN_ROOT=$(gt town root)`.
func runTownRoot(t *testing.T) (stdout string, err error) {
	t.Helper()

	var buf bytes.Buffer
	townRootCmd.SetOut(&buf)
	t.Cleanup(func() { townRootCmd.SetOut(nil) })

	err = townRootCmd.RunE(townRootCmd, nil)
	return buf.String(), err
}

// newTownFixture creates a workspace root (mayor/town.json) and chdirs into
// dir, returning the town root as the process sees it after chdir. Comparing
// against os.Getwd rather than the t.TempDir string keeps the test honest on
// platforms where TMPDIR is a symlink — workspace.Find deliberately does not
// resolve symlinks.
func newTownFixture(t *testing.T) string {
	t.Helper()

	townRoot := t.TempDir()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{"name":"test-town"}`), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(townRoot)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return cwd
}

func TestTownRootPrintsWorkspaceRoot(t *testing.T) {
	townRoot := newTownFixture(t)

	stdout, err := runTownRoot(t)
	if err != nil {
		t.Fatalf("gt town root in a workspace: unexpected error: %v", err)
	}
	if got := strings.TrimSuffix(stdout, "\n"); got != townRoot {
		t.Errorf("stdout = %q, want %q", got, townRoot)
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Errorf("stdout = %q, want a trailing newline for $(...) consumers", stdout)
	}
}

func TestTownRootFindsRootFromSubdirectory(t *testing.T) {
	townRoot := newTownFixture(t)

	sub := filepath.Join(townRoot, "gastown", "polecats", "enclave")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	stdout, err := runTownRoot(t)
	if err != nil {
		t.Fatalf("gt town root from a subdirectory: unexpected error: %v", err)
	}
	if got := strings.TrimSuffix(stdout, "\n"); got != townRoot {
		t.Errorf("stdout = %q, want %q", got, townRoot)
	}
}

func TestTownRootUsesEnvFallbackOutsideWorkspace(t *testing.T) {
	townRoot := newTownFixture(t)

	outside := t.TempDir()
	t.Chdir(outside)
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", "")

	stdout, err := runTownRoot(t)
	if err != nil {
		t.Fatalf("gt town root with GT_TOWN_ROOT set: unexpected error: %v", err)
	}
	if got := strings.TrimSuffix(stdout, "\n"); got != townRoot {
		t.Errorf("stdout = %q, want %q", got, townRoot)
	}
}

// TestTownRootFailsOutsideWorkspace is the core of gt-cr2: the failure mode
// must be an error with nothing on stdout. Anything printed on stdout gets
// captured into a path variable by the plugins that call this command.
func TestTownRootFailsOutsideWorkspace(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GT_TOWN_ROOT", "")
	t.Setenv("GT_ROOT", "")

	stdout, err := runTownRoot(t)
	if err == nil {
		t.Fatal("gt town root outside a workspace: want an error, got nil")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty — scripts capture stdout as a path", stdout)
	}
}

// TestTownRejectsUnknownSubcommand locks in the other half of gt-cr2: before
// the fix, `gt town root` was an unknown subcommand and Cobra printed `gt
// town`'s help to stdout and exited 0, so the command substitution succeeded
// with help text as its value.
func TestTownRejectsUnknownSubcommand(t *testing.T) {
	if townCmd.RunE == nil {
		t.Fatal("townCmd has no RunE: an unknown subcommand would print help to stdout and exit 0")
	}

	tests := []struct {
		name string
		args []string
	}{
		{name: "no subcommand", args: nil},
		{name: "unknown subcommand", args: []string{"definitely-not-a-subcommand"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := townCmd.RunE(townCmd, tt.args); err == nil {
				t.Errorf("townCmd.RunE(%q) = nil, want an error", tt.args)
			}
		})
	}
}

// TestTownRootIsRegistered guards against the subcommand being dropped while
// the plugins still shell out to it.
func TestTownRootIsRegistered(t *testing.T) {
	for _, sub := range townCmd.Commands() {
		if sub.Name() == "root" {
			return
		}
	}
	t.Error("gt town has no `root` subcommand; plugins/*/run.sh call `gt town root`")
}
