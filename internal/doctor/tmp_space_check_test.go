package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/tmpgc"
)

// setTmpDir points os.TempDir() at a private directory for one test. It stays
// under the real TMPDIR: relocating temp files under $HOME makes bd fixtures
// inherit an ancestor beads workspace, which manufactures failures elsewhere in
// the suite.
func setTmpDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	if os.TempDir() != dir {
		t.Skipf("os.TempDir() does not follow TMPDIR on %s", runtime.GOOS)
	}
	return dir
}

// mkOrphanWorkDir writes a directory that looks exactly like a Go work
// directory abandoned by a killed build.
func mkOrphanWorkDir(t *testing.T, root, name string, size int, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "b001"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(dir, "b001", "_pkg_.a")
	if err := os.WriteFile(file, make([]byte, size), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	stamp := time.Now().Add(-age)
	for _, p := range []string{file, filepath.Join(dir, "b001"), dir} {
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	return dir
}

func TestTmpSpaceCheckIdentity(t *testing.T) {
	c := NewTmpSpaceCheck()
	if c.Name() != "tmp-space" {
		t.Errorf("Name() = %q, want %q", c.Name(), "tmp-space")
	}
	if c.Category() != CategoryInfrastructure {
		t.Errorf("Category() = %q, want %q", c.Category(), CategoryInfrastructure)
	}
	if !c.CanFix() {
		t.Error("CanFix() = false; the check exists so that --fix can reclaim the space")
	}
}

// TestTmpSpaceCheckReportsTmpDirNotTownRoot is the point of the check: it must
// measure TMPDIR, which is the filesystem the town root check cannot see.
func TestTmpSpaceCheckReportsTmpDirNotTownRoot(t *testing.T) {
	dir := setTmpDir(t)

	res := NewTmpSpaceCheck().Run(&CheckContext{TownRoot: t.TempDir()})
	if !strings.Contains(res.Message, dir) {
		t.Errorf("message %q does not name TMPDIR %q", res.Message, dir)
	}
}

func TestTmpSpaceCheckFixReclaimsOrphan(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("live-process evidence requires procfs; the sweep refuses without it")
	}
	dir := setTmpDir(t)
	orphan := mkOrphanWorkDir(t, dir, "go-build2674671939", 4096, 6*time.Hour)
	keep := mkOrphanWorkDir(t, dir, "claude-1000", 4096, 6*time.Hour)

	if err := NewTmpSpaceCheck().Fix(&CheckContext{TownRoot: t.TempDir()}); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan %s survived Fix (err=%v)", orphan, err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("Fix removed a directory that is not a Go work directory: %v", err)
	}
}

// TestTmpSpaceCheckFixReportsWhenNothingToReclaim keeps --fix honest: a fix
// that returns nil having done nothing tells the operator the problem is
// handled when the filesystem is still full.
func TestTmpSpaceCheckFixReportsWhenNothingToReclaim(t *testing.T) {
	setTmpDir(t)

	err := NewTmpSpaceCheck().Fix(&CheckContext{TownRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Fix returned nil having reclaimed nothing")
	}
	if !strings.Contains(err.Error(), "no orphaned Go work directories") &&
		!strings.Contains(err.Error(), "liveness evidence unavailable") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCountReclaimable(t *testing.T) {
	res := &tmpgc.Result{Candidates: []tmpgc.Candidate{
		{Status: tmpgc.StatusReclaimable},
		{Status: tmpgc.StatusLive},
		{Status: tmpgc.StatusReclaimable},
		{Status: tmpgc.StatusYoung},
		{Status: tmpgc.StatusRefused},
	}}
	if got := countReclaimable(res); got != 2 {
		t.Errorf("countReclaimable = %d, want 2", got)
	}
}
