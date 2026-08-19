package git

import (
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/util"
)

// TestRunMissingWorkDirNamesTheDirectory pins the re-attribution: a git that
// cannot start because its working directory is gone must say so, and must not
// say the binary is missing.
//
// The failure this replaces read "fork/exec /usr/bin/git: no such file or
// directory" on a host where /usr/bin/git exists and runs, which sent readers
// to look for a broken git installation (gt-m7cc).
func TestRunMissingWorkDirNamesTheDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no", "such", "dir")
	g := NewGit(missing)

	_, err := g.run("rev-parse", "--git-dir")
	if err == nil {
		t.Fatal("expected an error running git in a directory that does not exist")
	}

	msg := err.Error()
	if !strings.Contains(msg, missing) {
		t.Errorf("error must name the missing working directory %q, got: %s", missing, msg)
	}
	if strings.Contains(msg, "fork/exec") {
		t.Errorf("error still reports the exec of the binary rather than the directory: %s", msg)
	}

	// The original exec failure stays in the chain, so callers testing for a
	// missing path keep working.
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("errors.Is(err, fs.ErrNotExist) must still hold, got: %v", err)
	}
	var dirErr *ExecDirError
	if !errors.As(err, &dirErr) {
		t.Fatalf("expected an *ExecDirError in the chain, got %T: %v", err, err)
	}
	if dirErr.Dir != missing {
		t.Errorf("ExecDirError.Dir = %q, want %q", dirErr.Dir, missing)
	}
}

// TestRunMissingWorkDirReportsChdirWithoutSysProcAttr is the control for the
// whole finding: with no SysProcAttr, Go attributes the same failure to the
// chdir itself. gt sets SysProcAttr on every git call (SetDetachedProcessGroup),
// which is exactly what costs it that attribution — so if this control ever
// fails, the premise has changed and explainExecFailure should be revisited.
func TestRunMissingWorkDirReportsChdirWithoutSysProcAttr(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the attribution difference is a fork/exec detail; measured on linux only")
	}
	missing := filepath.Join(t.TempDir(), "no", "such", "dir")

	plain := exec.Command("git", "rev-parse", "--git-dir")
	plain.Dir = missing
	plainErr := plain.Run()
	if plainErr == nil {
		t.Fatal("expected the plain exec to fail too")
	}
	if !strings.Contains(plainErr.Error(), missing) {
		t.Skipf("this Go runtime does not attribute the chdir either (%v); the control cannot run", plainErr)
	}

	detached := exec.Command("git", "rev-parse", "--git-dir")
	detached.Dir = missing
	util.SetDetachedProcessGroup(detached)
	detachedErr := detached.Run()
	if detachedErr == nil {
		t.Fatal("expected the detached exec to fail")
	}
	if strings.Contains(detachedErr.Error(), missing) {
		t.Errorf("SysProcAttr no longer costs the chdir attribution (%v) — explainExecFailure may be unnecessary", detachedErr)
	}
}

// TestRunSurfacesRealGitErrors is the other control: a working directory that
// exists must still produce git's own diagnosis, not a directory complaint.
func TestRunSurfacesRealGitErrors(t *testing.T) {
	g := NewGit(t.TempDir())

	_, err := g.run("rev-parse", "--git-dir")
	if err == nil {
		t.Fatal("expected git to reject a directory that is not a repository")
	}
	if strings.Contains(err.Error(), "the missing path is the working directory") {
		t.Errorf("an existing working directory must not be blamed: %s", err)
	}
}
