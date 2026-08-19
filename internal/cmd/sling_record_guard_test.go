package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// recordGuardTown builds a town with a bd stub whose `show` returns one open
// bead carrying the given labels JSON fragment (e.g. `,"labels":["gt:ledger"]`).
func recordGuardTown(t *testing.T, labelsJSON string) string {
	t.Helper()

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Refinery merge ledger, session 2026-08-17","status":"open","assignee":"","description":"Durable copy of this session'"'"'s merge outcomes."` + labelsJSON + `}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return townRoot
}

// A durable archival record must not be dispatched: doing so burns a polecat
// session per attempt and recurs forever, because a ledger is never "done" in
// the implementer sense (gt-f8td).
func TestExecuteSling_RecordBeadRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := recordGuardTown(t, `,"labels":["gt:ledger"]`)

	result, err := executeSling(SlingParams{
		BeadID:   "gt-8uc",
		RigName:  "testrig",
		TownRoot: townRoot,
	})
	if err == nil {
		t.Fatal("expected error when slinging a record bead, got nil")
	}
	if result.ErrMsg != "record bead" {
		t.Errorf("expected ErrMsg=%q, got %q", "record bead", result.ErrMsg)
	}
	if !strings.Contains(err.Error(), "gt:ledger") {
		t.Errorf("error should name the offending label: %v", err)
	}
}

// Control: the identical bead WITHOUT a record label must get past the record
// guard. Without this, a guard that rejected everything would still pass the
// test above.
func TestExecuteSling_UnlabelledBeadPassesRecordGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := recordGuardTown(t, "")

	result, err := executeSling(SlingParams{
		BeadID:   "gt-8uc",
		RigName:  "testrig",
		TownRoot: townRoot,
	})
	// Dispatch still fails further down (the stub has no rig), which is fine —
	// it just must not fail on the record guard.
	if err != nil && strings.Contains(err.Error(), "record bead") {
		t.Errorf("unlabelled bead was refused by the record guard: %v", err)
	}
	if result != nil && result.ErrMsg == "record bead" {
		t.Errorf("unlabelled bead got ErrMsg=%q", result.ErrMsg)
	}
}

// --force is the documented override, so an operator who disagrees is not stuck.
func TestExecuteSling_RecordBeadForceOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := recordGuardTown(t, `,"labels":["gt:record"]`)

	result, err := executeSling(SlingParams{
		BeadID:   "gt-8uc",
		RigName:  "testrig",
		TownRoot: townRoot,
		Force:    true,
	})
	if err != nil && strings.Contains(err.Error(), "record bead") {
		t.Errorf("--force did not override the record guard: %v", err)
	}
	if result != nil && result.ErrMsg == "record bead" {
		t.Errorf("--force did not override the record guard: ErrMsg=%q", result.ErrMsg)
	}
}
