package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

// setupRollbackTown builds a town with one rig, one polecat sandbox on disk, and
// a bd stub on PATH, then chdirs into it. Returns the town root and the path to
// the polecat's worktree.
func setupRollbackTown(t *testing.T, polecatName string) (townRoot, clonePath string) {
	t.Helper()

	townRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("evalsymlinks tempdir: %v", err)
	}

	for _, dir := range []string{
		filepath.Join(townRoot, "mayor", "rig"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(townRoot, "gastown", "mayor", "rig"),
		filepath.Join(townRoot, "gastown", ".repo.git"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigs := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"gastown": {
				GitURL:      "git@github.com:test/gastown.git",
				AddedAt:     time.Now().Truncate(time.Second),
				BeadsConfig: &config.BeadsConfig{Repo: "local", Prefix: "gt-"},
			},
		},
	}
	if err := config.SaveRigsConfig(rigsPath, rigs); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}

	// The sandbox this sling would have reused: polecats/<name>/<rig>/ with a
	// file in it, so its survival (or deletion) is directly observable.
	clonePath = filepath.Join(townRoot, "gastown", "polecats", polecatName, "gastown")
	if err := os.MkdirAll(clonePath, 0755); err != nil {
		t.Fatalf("mkdir clone path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(clonePath, "CLAUDE.md"), []byte("polecat sandbox\n"), 0644); err != nil {
		t.Fatalf("write sandbox marker: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
case "$1" in
  show|list) echo '[]' ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	return townRoot, clonePath
}

// captureCleanupSpawnedPolecat runs cleanupSpawnedPolecat with stdout captured.
func captureCleanupSpawnedPolecat(t *testing.T, spawnInfo *SpawnedPolecatInfo, rigName, convoyID string) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()

	cleanupSpawnedPolecat(spawnInfo, rigName, convoyID)

	_ = w.Close()
	os.Stdout = oldStdout
	return <-done
}

// TestCleanupSpawnedPolecat_ReusedPolecatKeepsSandbox verifies the rollback of a
// sling that REUSED a pool polecat leaves that polecat's worktree on disk.
//
// The removal in the sibling case below is correct for a polecat the sling
// created. It is wrong for one it borrowed: that sandbox pre-dates the failed
// sling, and deleting it churns the persistent pool that gt-2uqy exists to keep —
// one worktree per failed sling, on the same path that already blocked dispatch
// once by filling the per-rig directory cap.
func TestCleanupSpawnedPolecat_ReusedPolecatKeepsSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot, clonePath := setupRollbackTown(t, "toast")

	output := captureCleanupSpawnedPolecat(t, &SpawnedPolecatInfo{
		RigName:     "gastown",
		PolecatName: "toast",
		ClonePath:   clonePath,
		Branch:      "polecat/toast/gt-abc+123",
		Reused:      true,
	}, "gastown", "")

	if _, err := os.Stat(filepath.Join(clonePath, "CLAUDE.md")); err != nil {
		t.Errorf("reused polecat sandbox was destroyed by rollback: %v\noutput:\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(townRoot, "gastown", "polecats", "toast")); err != nil {
		t.Errorf("reused polecat directory was removed by rollback: %v\noutput:\n%s", err, output)
	}
	if strings.Contains(output, "orphaned polecat") {
		t.Errorf("rollback took the removal path for a reused polecat:\n%s", output)
	}
	if !strings.Contains(output, "Released reused polecat") {
		t.Errorf("rollback did not report releasing the polecat back to the pool:\n%s", output)
	}
}

// TestCleanupSpawnedPolecat_SpawnedPolecatIsRemoved is the control: with Reused
// unset — a polecat this sling created — rollback still takes the destructive
// removal path. Without this, the test above would also pass if cleanup had
// simply stopped doing anything at all.
func TestCleanupSpawnedPolecat_SpawnedPolecatIsRemoved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	_, clonePath := setupRollbackTown(t, "crater")

	output := captureCleanupSpawnedPolecat(t, &SpawnedPolecatInfo{
		RigName:     "gastown",
		PolecatName: "crater",
		ClonePath:   clonePath,
		Branch:      "polecat/crater/gt-abc+123",
	}, "gastown", "")

	if !strings.Contains(output, "orphaned polecat") {
		t.Errorf("rollback of a freshly spawned polecat did not attempt removal:\n%s", output)
	}
	if strings.Contains(output, "Released reused polecat") {
		t.Errorf("rollback released a polecat this sling created instead of removing it:\n%s", output)
	}
}
