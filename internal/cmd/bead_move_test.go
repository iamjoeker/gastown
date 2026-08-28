package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/workspace"
)

// newBeadMoveTestTown builds a town root whose routes.jsonl maps hq- to the town
// itself and gt- to a rig, and leaves zz- unrouted.
func newBeadMoveTestTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()

	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}
	writeFile(t, filepath.Join(townRoot, "mayor", "town.json"), `{"name":"bead-move-test-town"}`)
	writeFile(t, filepath.Join(townRoot, ".beads", "routes.jsonl"),
		"{\"prefix\":\"hq-\",\"path\":\".\"}\n"+
			"{\"prefix\":\"gt-\",\"path\":\"gastown/mayor/rig\"}\n")

	// The fixture only means anything if it is the town workspace.Find lands on.
	// A TMPDIR nested inside a real town would make Find return that outer town
	// and quietly test the wrong routes (workspace.Find returns the OUTERMOST
	// match), so fail loudly rather than pass against the wrong tree.
	if found, err := workspace.Find(townRoot); err != nil || found != townRoot {
		t.Fatalf("fixture town %s does not resolve as the town root (got %q, err %v); "+
			"is TMPDIR inside a Gas Town workspace?", townRoot, found, err)
	}
	return townRoot
}

func TestResolveBeadMoveTargetRoutesByPrefix(t *testing.T) {
	townRoot := newBeadMoveTestTown(t)

	tests := []struct {
		prefix string
		want   string
	}{
		{prefix: "hq-", want: townRoot},
		{prefix: "gt-", want: filepath.Join(townRoot, "gastown", "mayor", "rig")},
	}
	for _, tc := range tests {
		target, err := resolveBeadMoveTargetIn(townRoot, tc.prefix)
		if err != nil {
			t.Fatalf("resolveBeadMoveTargetIn(%q) returned error: %v", tc.prefix, err)
		}
		if target.workDir != tc.want {
			t.Errorf("resolveBeadMoveTargetIn(%q).workDir = %q, want %q", tc.prefix, target.workDir, tc.want)
		}
		if target.townRoot != townRoot {
			t.Errorf("resolveBeadMoveTargetIn(%q).townRoot = %q, want %q", tc.prefix, target.townRoot, townRoot)
		}
	}
}

// An unrouted prefix must be refused, not absorbed. bd create files into
// whatever database its working directory resolves to, so returning any
// directory at all for a prefix with no route would file the copy in a silently
// wrong store — the failure gt-ecff calls worse than the loud one.
func TestResolveBeadMoveTargetRefusesUnroutedPrefix(t *testing.T) {
	townRoot := newBeadMoveTestTown(t)

	target, err := resolveBeadMoveTargetIn(townRoot, "zz-")
	if err == nil {
		t.Fatalf("resolveBeadMoveTargetIn(%q) succeeded with workDir %q, want an error", "zz-", target.workDir)
	}
	if target.workDir != "" {
		t.Errorf("failed resolution returned workDir %q, want empty", target.workDir)
	}
	if !strings.Contains(err.Error(), "zz-") {
		t.Errorf("error %q does not name the offending prefix", err)
	}

	// Control: the same call for a routed prefix succeeds, so the error above is
	// the missing route and not a broken fixture.
	if _, err := resolveBeadMoveTargetIn(townRoot, "gt-"); err != nil {
		t.Fatalf("control: resolveBeadMoveTargetIn(%q) returned error: %v", "gt-", err)
	}
}

func TestResolveBeadMoveTargetRequiresAPrefix(t *testing.T) {
	townRoot := newBeadMoveTestTown(t)

	for _, prefix := range []string{"", "-"} {
		if _, err := resolveBeadMoveTargetIn(townRoot, prefix); err == nil {
			t.Errorf("resolveBeadMoveTargetIn(%q) succeeded, want an error", prefix)
		}
	}
}

// The dry run must not certify a move the real path cannot make. Before gt-ecff
// it printed its plan and exited 0 for a target that could never be reached.
func TestBeadMoveDryRunFailsWhenTargetIsUnreachable(t *testing.T) {
	townRoot := newBeadMoveTestTown(t)
	t.Chdir(townRoot)

	t.Cleanup(func() { beadMoveDryRun = false })
	beadMoveDryRun = true

	err := runBeadMove(beadMoveCmd, []string{"hq-source", "zz-"})
	if err == nil {
		t.Fatal("dry run reported success for a prefix with no route")
	}
	// The fixture has no beads database, so almost anything downstream also
	// errors. Insist the failure is the unreachable target and that it came
	// before the source lookup — otherwise a target-resolution fallback would
	// leave this test green on an error it never meant to catch.
	if !strings.Contains(err.Error(), "zz-") {
		t.Fatalf("dry run failed for some reason other than the unroutable target: %v", err)
	}
	if strings.Contains(err.Error(), "getting bead") {
		t.Fatalf("dry run reached the source lookup before resolving the target: %v", err)
	}
}

// Every flag the move passes to bd create must be one bd create accepts. The
// original defect was a single argv flag — --prefix, which belongs to bd init —
// that made every move fail while --dry-run still reported success.
func TestBeadMoveCreateArgsAreAcceptedByBd(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skipf("bd not on PATH: %v", err)
	}

	source := moveBeadInfo{
		ID:          "hq-source",
		Title:       "move argv probe",
		Type:        "bug",
		Priority:    1,
		Description: "probe description",
		Labels:      []string{"one", "two"},
		Assignee:    "gastown/polecats/dust",
	}
	args := beadMoveCreateArgs(source)

	// Run in an empty directory with --dry-run: no database is created and no
	// row is written, but bd still parses argv before it looks for a store.
	runBd := func(extra ...string) string {
		t.Helper()
		cmd := exec.Command("bd", append(append([]string{}, args...), extra...)...) //nolint:gosec // fixed argv
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "BEADS_DIR=")
		out, _ := cmd.CombinedOutput()
		return string(out)
	}

	// Control first: the probe must be able to see a bad flag at all.
	if got := runBd("--dry-run", "--prefix", "gt-"); !strings.Contains(got, "unknown flag") {
		t.Fatalf("control failed: bd did not reject an invented flag; output:\n%s", got)
	}

	if got := runBd("--dry-run"); strings.Contains(got, "unknown flag") {
		t.Errorf("bd create rejected a flag the move passes; output:\n%s", got)
	}
}
