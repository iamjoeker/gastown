package events

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/testguard"
)

// liveTown returns a real, writable town root the guard must treat as live: a
// temporary directory with a workspace marker, with TMPDIR then moved elsewhere
// so the directory falls outside the disposable boundary.
//
// Writable is the point. If the guard regresses, the event really is appended and
// the assertion sees the file; an unwritable path would make a regression look
// like a refusal.
func liveTown(t *testing.T) string {
	t.Helper()
	live := t.TempDir()
	if err := os.MkdirAll(filepath.Join(live, "mayor"), 0755); err != nil {
		t.Fatalf("creating town marker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(live, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("writing town marker: %v", err)
	}
	elsewhere := t.TempDir()
	t.Setenv("TMPDIR", elsewhere)
	if got := os.TempDir(); got != elsewhere {
		t.Skipf("os.TempDir() = %q, not the TMPDIR just set; cannot stage a live town on this platform", got)
	}
	t.Chdir(live)
	return live
}

// TestWrite_RefusesLiveTownFromTestBinary covers the feed pollution found while
// fixing gt-vmj: 81 events in the live feed from "testrig/refinery", a rig that
// exists only in a test fixture.
func TestWrite_RefusesLiveTownFromTestBinary(t *testing.T) {
	t.Setenv(testguard.AllowEnv, "")
	if err := os.Unsetenv(testguard.AllowEnv); err != nil {
		t.Fatalf("unset %s: %v", testguard.AllowEnv, err)
	}
	live := liveTown(t)

	err := LogFeed("nudge", "testrig/refinery", map[string]interface{}{"to": "mayor/"})
	if !errors.Is(err, testguard.ErrRefused) {
		t.Errorf("LogFeed() into a live town = %v, want ErrRefused", err)
	}

	if _, statErr := os.Stat(filepath.Join(live, EventsFile)); !os.IsNotExist(statErr) {
		data, _ := os.ReadFile(filepath.Join(live, EventsFile))
		t.Errorf("refused LogFeed() wrote %s:\n%s", EventsFile, data)
	}
}

// TestWrite_AllowsDisposableTown is what keeps the guard free: tests that build a
// town with t.TempDir() declare nothing and keep working.
func TestWrite_AllowsDisposableTown(t *testing.T) {
	t.Setenv(testguard.AllowEnv, "")
	if err := os.Unsetenv(testguard.AllowEnv); err != nil {
		t.Fatalf("unset %s: %v", testguard.AllowEnv, err)
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("creating town marker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("writing town marker: %v", err)
	}
	t.Chdir(townRoot)

	if err := LogFeed("nudge", "gastown/nudge-test-fixture", map[string]interface{}{"to": "mayor/"}); err != nil {
		t.Fatalf("LogFeed() into a temporary town = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(townRoot, EventsFile)); err != nil {
		t.Fatalf("LogFeed() into a temporary town wrote nothing: %v", err)
	}
}
