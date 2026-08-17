package formula

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The deacon patrol's "stale test temp dirs" cleanup is an rm -rf path. It ran
// for months with a guard that failed OPEN: `if ! lsof +D "$dir"` deletes
// whenever lsof exits non-zero, and lsof exits non-zero for "nothing is open",
// "I could not read that directory", "I am not installed", and — on hosts with
// docker nsfs/overlay mounts — for every single invocation, including ones that
// listed live file handles on a running Dolt server.
//
// These tests execute the guard as the formula publishes it, against fixture
// directories only, and assert the REFUSAL branches as hard as the delete
// branch. A guard tested only on its happy path is how the last one shipped.
//
// See: gt-32z
const staleTempDirSection = "**2. Stale test temp dirs**"

// extractStaleTempDirScript pulls the shell block that follows the
// "stale test temp dirs" heading out of the deacon patrol formula.
func extractStaleTempDirScript(t *testing.T) string {
	t.Helper()

	content, err := formulasFS.ReadFile("formulas/mol-deacon-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading deacon patrol formula: %v", err)
	}

	body := string(content)
	sectionIdx := strings.Index(body, staleTempDirSection)
	if sectionIdx < 0 {
		t.Fatalf("deacon patrol formula: %q section not found", staleTempDirSection)
	}

	rest := body[sectionIdx:]
	open := strings.Index(rest, "```bash\n")
	if open < 0 {
		t.Fatal("deacon patrol formula: no bash block after stale test temp dirs section")
	}
	rest = rest[open+len("```bash\n"):]
	closeIdx := strings.Index(rest, "\n```")
	if closeIdx < 0 {
		t.Fatal("deacon patrol formula: unterminated bash block in stale test temp dirs section")
	}
	return rest[:closeIdx]
}

// TestStaleTempDirGuardHasNoExitStatusTest is a cheap regression fence: the
// exit-status form must never come back, and neither must the bare-glob for
// loop whose zsh abort was the only thing keeping the old rm -rf inert.
func TestStaleTempDirGuardHasNoExitStatusTest(t *testing.T) {
	script := extractStaleTempDirScript(t)

	if strings.Contains(script, "! lsof") {
		t.Error("stale temp dir guard tests lsof's exit status; lsof exits non-zero for " +
			"unreadable dirs, missing tool, and (with docker mounts) every invocation. " +
			"Test its STDOUT instead. See gt-32z")
	}
	if strings.Contains(script, `for dir in "$TMPDIR"/`) {
		t.Error("stale temp dir cleanup iterates a bare glob list; an unmatched pattern " +
			"aborts the whole statement under zsh, which silently disables the step. " +
			"Use find. See gt-32z")
	}
	for _, required := range []string{
		"command -v lsof",
		"REFUSING",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("stale temp dir guard is missing %q — it must fail closed", required)
		}
	}
}

// runStaleTempDirGuard executes the formula's cleanup block with TMPDIR pointed
// at a fixture root, and returns its combined output.
func runStaleTempDirGuard(t *testing.T, script, tmpDir string, env ...string) string {
	t.Helper()

	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "TMPDIR="+tmpDir)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("guard script failed: %v\noutput:\n%s", err, out)
	}
	return string(out)
}

func TestStaleTempDirGuardBehavior(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell guard is POSIX-only")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not installed; the guard's own refusal branch covers this host")
	}

	script := extractStaleTempDirScript(t)

	t.Run("deletes an idle fixture dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		idle := filepath.Join(tmpDir, "beads-bd-tests-idle")
		mkFixture(t, idle)
		unrelated := filepath.Join(tmpDir, "some-other-dir")
		mkFixture(t, unrelated)

		out := runStaleTempDirGuard(t, script, tmpDir)

		if _, err := os.Stat(idle); !os.IsNotExist(err) {
			t.Errorf("idle fixture dir should have been cleaned; stat err = %v\noutput:\n%s", err, out)
		}
		if _, err := os.Stat(unrelated); err != nil {
			t.Errorf("non-matching dir must not be touched: %v", err)
		}
		if !strings.Contains(out, "stale_dirs=1") {
			t.Errorf("expected stale_dirs=1, got:\n%s", out)
		}
	})

	t.Run("refuses a dir with live file handles", func(t *testing.T) {
		tmpDir := t.TempDir()
		busy := filepath.Join(tmpDir, "beads-test-dolt-busy")
		mkFixture(t, busy)

		// A real open handle held by this test process, the way a running
		// dolt sql-server holds .dolt/noms/LOCK.
		held, err := os.Create(filepath.Join(busy, "LOCK"))
		if err != nil {
			t.Fatalf("creating held file: %v", err)
		}
		defer held.Close()

		out := runStaleTempDirGuard(t, script, tmpDir)

		if _, err := os.Stat(busy); err != nil {
			t.Fatalf("guard deleted a directory with a live open handle: %v\noutput:\n%s", err, out)
		}
		if !strings.Contains(out, "REFUSING (live file handles)") {
			t.Errorf("expected a live-handle refusal, got:\n%s", out)
		}
		if !strings.Contains(out, "stale_dirs=0") {
			t.Errorf("expected stale_dirs=0, got:\n%s", out)
		}
	})

	t.Run("refuses a dir it cannot fully inspect", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root; permission bits do not block traversal")
		}
		tmpDir := t.TempDir()
		opaque := filepath.Join(tmpDir, "beads-bd-tests-opaque")
		hidden := filepath.Join(opaque, "hidden")
		mkFixture(t, hidden)
		if err := os.Chmod(hidden, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(hidden, 0o700) })

		out := runStaleTempDirGuard(t, script, tmpDir)

		// lsof reports nothing for a subtree it cannot descend into, which is
		// indistinguishable from "nothing is open" unless we check first.
		if _, err := os.Stat(opaque); err != nil {
			t.Fatalf("guard deleted a directory it could not inspect: %v\noutput:\n%s", err, out)
		}
		if !strings.Contains(out, "REFUSING (cannot fully inspect)") {
			t.Errorf("expected an inspection refusal, got:\n%s", out)
		}
	})

	t.Run("refuses everything when lsof is absent", func(t *testing.T) {
		tmpDir := t.TempDir()
		idle := filepath.Join(tmpDir, "beads-bd-tests-idle")
		mkFixture(t, idle)

		emptyPath := t.TempDir()
		out := runStaleTempDirGuard(t, script, tmpDir, "PATH="+emptyPath)

		if _, err := os.Stat(idle); err != nil {
			t.Fatalf("guard deleted a fixture dir with no lsof to prove it idle: %v\noutput:\n%s", err, out)
		}
		if !strings.Contains(out, "REFUSING all stale test dir cleanup: lsof not installed") {
			t.Errorf("expected a missing-lsof refusal, got:\n%s", out)
		}
	})
}

func mkFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating fixture %s: %v", dir, err)
	}
}
