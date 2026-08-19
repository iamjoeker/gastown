package formula

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The deacon patrol's "stale PID/lock files" cleanup is sub-section 2's defect
// pattern one blast radius smaller (gt-hkv, discovered while fixing gt-32z):
//
// `for pidfile in /tmp/A-*.pid /tmp/B-*.pid; do ... if ! kill -0 "$PID"`.
//
// Two globs in one `for` list: zsh aborts the whole statement when the first
// pattern matches nothing, so the loop ran zero iterations. And `! kill -0
// "$PID"` reads "dead", but kill -0 also fails with EPERM when the process
// EXISTS and is owned by another user — the same exit status as ESRCH.
//
// Repairing the glob alone would have armed the second defect. These tests
// execute the guard as the formula publishes it, against fixture PID files, and
// assert the KEEP branches as hard as the delete branch.
//
// See: gt-hkv, gt-32z
const stalePIDFileSection = "**3. Stale PID/lock files**"

// extractStalePIDFileScript pulls the shell block that follows the "stale
// PID/lock files" heading out of the deacon patrol formula.
func extractStalePIDFileScript(t *testing.T) string {
	t.Helper()

	content, err := formulasFS.ReadFile("formulas/mol-deacon-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading deacon patrol formula: %v", err)
	}

	body := string(content)
	sectionIdx := strings.Index(body, stalePIDFileSection)
	if sectionIdx < 0 {
		t.Fatalf("deacon patrol formula: %q section not found", stalePIDFileSection)
	}

	rest := body[sectionIdx:]
	open := strings.Index(rest, "```bash\n")
	if open < 0 {
		t.Fatal("deacon patrol formula: no bash block after stale PID/lock files section")
	}
	rest = rest[open+len("```bash\n"):]
	closeIdx := strings.Index(rest, "\n```")
	if closeIdx < 0 {
		t.Fatal("deacon patrol formula: unterminated bash block in stale PID/lock files section")
	}
	return rest[:closeIdx]
}

// TestStalePIDFileGuardHasNoBareGlobOrExitStatusTest is a cheap regression
// fence: neither the bare-glob `for` list nor the exit-status-only kill probe
// may come back, and the fail-closed branches must still be present.
func TestStalePIDFileGuardHasNoBareGlobOrExitStatusTest(t *testing.T) {
	script := extractStalePIDFileScript(t)

	if strings.Contains(script, "for pidfile in") {
		t.Error("stale PID file cleanup iterates a bare glob list; an unmatched pattern " +
			"aborts the whole statement under zsh, which silently disables the step. " +
			"Use find. See gt-hkv")
	}
	if strings.Contains(script, "! kill -0") {
		t.Error("stale PID file guard tests kill -0's exit status alone; kill -0 also " +
			"fails with EPERM for a LIVE process owned by another user, so this deletes " +
			"a running server's PID file as stale. See gt-hkv")
	}
	for _, required := range []string{
		"find ",
		"No such process",
		"REFUSING",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("stale PID file guard is missing %q — it must fail closed", required)
		}
	}
}

// runStalePIDFileGuard executes the formula's cleanup block with TMPDIR pointed
// at a fixture root, using the named shell.
func runStalePIDFileGuard(t *testing.T, shell, script, tmpDir string) string {
	t.Helper()

	cmd := exec.Command(shell, "-c", script)
	cmd.Env = append(os.Environ(), "TMPDIR="+tmpDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("guard script failed under %s: %v\noutput:\n%s", shell, err, out)
	}
	return string(out)
}

// writePIDFile drops a fixture PID file and returns its path.
func writePIDFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing fixture PID file %s: %v", path, err)
	}
	return path
}

// deadPID returns the PID of a process that has run and been reaped, i.e. the
// contents of a genuinely stale PID file.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("bash", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting throwaway process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reaping throwaway process: %v", err)
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); err == nil {
			t.Skipf("PID %d was recycled before the assertion; rerun", pid)
		}
	}
	return pid
}

func TestStalePIDFileGuardBehavior(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell guard is POSIX-only")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	script := extractStalePIDFileScript(t)

	t.Run("removes a PID file whose process is dead", func(t *testing.T) {
		tmpDir := t.TempDir()
		stale := writePIDFile(t, tmpDir, "dolt-test-server-abc.pid", strconv.Itoa(deadPID(t))+"\n")
		unrelated := writePIDFile(t, tmpDir, "some-other.pid", "1\n")

		out := runStalePIDFileGuard(t, "bash", script, tmpDir)

		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Errorf("stale PID file should have been removed; stat err = %v\noutput:\n%s", err, out)
		}
		if _, err := os.Stat(unrelated); err != nil {
			t.Errorf("non-matching file must not be touched: %v", err)
		}
		if !strings.Contains(out, "stale_pidfiles=1") {
			t.Errorf("expected stale_pidfiles=1, got:\n%s", out)
		}
	})

	t.Run("keeps a PID file whose process is alive and ours", func(t *testing.T) {
		tmpDir := t.TempDir()
		live := writePIDFile(t, tmpDir, "beads-test-dolt-live.pid", strconv.Itoa(os.Getpid())+"\n")

		out := runStalePIDFileGuard(t, "bash", script, tmpDir)

		if _, err := os.Stat(live); err != nil {
			t.Fatalf("guard removed the PID file of a live process: %v\noutput:\n%s", err, out)
		}
		if !strings.Contains(out, "stale_pidfiles=0") {
			t.Errorf("expected stale_pidfiles=0, got:\n%s", out)
		}
	})

	// The defect this bead exists for: kill -0 against a live process owned by
	// another user fails with EPERM, which the old guard read as death.
	t.Run("keeps a PID file whose process is alive but foreign", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root; kill -0 never returns EPERM")
		}
		tmpDir := t.TempDir()
		foreign := writePIDFile(t, tmpDir, "dolt-test-server-foreign.pid", "1\n")

		out := runStalePIDFileGuard(t, "bash", script, tmpDir)

		if _, err := os.Stat(foreign); err != nil {
			t.Fatalf("guard removed the PID file of a live process owned by another user: %v\noutput:\n%s", err, out)
		}
		if !strings.Contains(out, "alive, owned by another user") {
			t.Errorf("expected a foreign-alive keep, got:\n%s", out)
		}
		if !strings.Contains(out, "stale_pidfiles=0") {
			t.Errorf("expected stale_pidfiles=0, got:\n%s", out)
		}
	})

	t.Run("refuses an unreadable or non-numeric PID file", func(t *testing.T) {
		for _, tc := range []struct{ name, contents string }{
			{"beads-test-dolt-empty.pid", ""},
			{"beads-test-dolt-garbage.pid", "not-a-pid\n"},
		} {
			tmpDir := t.TempDir()
			path := writePIDFile(t, tmpDir, tc.name, tc.contents)

			out := runStalePIDFileGuard(t, "bash", script, tmpDir)

			if _, err := os.Stat(path); err != nil {
				t.Errorf("guard removed %s on a PID it could not parse: %v\noutput:\n%s", tc.name, err, out)
			}
			if !strings.Contains(out, "REFUSING (PID file unreadable or non-numeric)") {
				t.Errorf("expected a parse refusal for %s, got:\n%s", tc.name, out)
			}
		}
	})
}

// TestStalePIDFileGuardRunsUnderZsh is the inertness regression. The old form
// listed two globs in one `for`; when the first matched nothing zsh aborted the
// statement and the loop ran zero iterations, so the step did nothing at all.
func TestStalePIDFileGuardRunsUnderZsh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell guard is POSIX-only")
	}
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}

	script := extractStalePIDFileScript(t)
	tmpDir := t.TempDir()
	// Only the SECOND pattern matches. Under the old glob form this produced
	// zero iterations under zsh.
	stale := writePIDFile(t, tmpDir, "beads-test-dolt-only.pid", strconv.Itoa(deadPID(t))+"\n")

	out := runStalePIDFileGuard(t, "zsh", script, tmpDir)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("guard is inert under zsh when the first pattern matches nothing; "+
			"stat err = %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "stale_pidfiles=1") {
		t.Errorf("expected stale_pidfiles=1 under zsh, got:\n%s", out)
	}
}
