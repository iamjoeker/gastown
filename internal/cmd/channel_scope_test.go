package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/channelevents"
)

// newFakeTown creates a directory that workspace.Find recognizes as a town
// root, so code under test resolves events into it instead of the real town.
func newFakeTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("creating fake town: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("creating fake town marker: %v", err)
	}
	return townRoot
}

// countEvents returns how many .event files exist anywhere under the town's
// events tree.
func countEvents(t *testing.T, townRoot string) int {
	t.Helper()
	var n int
	root := filepath.Join(townRoot, "events")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Missing tree simply means no events.
		}
		if !d.IsDir() && strings.HasSuffix(path, ".event") {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return n
}

// TestNudgeUnderTestHookEmitsNoEvent is the regression test for the defect that
// made `go test ./internal/cmd/...` wake every refinery in the town.
//
// The nudge guard used to check `os.Getenv(...) != ""`, so a test that blanked
// GT_TEST_NUDGE_LOG fell straight through to a live channelevents emit. The
// guard now keys on presence, so a blank value still means "test mode".
//
// channelevents' own backstop would also stop the emit, so this test opts in to
// real emission first — that isolates the nudge guard, and the test would still
// catch a regression here if the backstop were ever removed.
func TestNudgeUnderTestHookEmitsNoEvent(t *testing.T) {
	setupSlingTestRegistry(t)
	t.Setenv(channelevents.AllowTestEmitEnv, "1")

	townRoot := newFakeTown(t)
	t.Chdir(townRoot)

	for _, value := range []string{"", filepath.Join(t.TempDir(), "nudge.log")} {
		t.Setenv("GT_TEST_NUDGE_LOG", value)

		nudgeRefinery("gastown", "test message")
		nudgeWitness("gastown", "test message")

		if n := countEvents(t, townRoot); n != 0 {
			t.Fatalf("GT_TEST_NUDGE_LOG=%q: %d real event(s) emitted from a unit test; want 0", value, n)
		}
	}
}

// TestTestNudgeHookPresenceNotValue pins the distinction that the original bug
// turned on: an empty value is still test mode.
func TestTestNudgeHookPresenceNotValue(t *testing.T) {
	t.Setenv("GT_TEST_NUDGE_LOG", "")
	if path, inTest := testNudgeHook(); !inTest || path != "" {
		t.Errorf("blank value: got (%q, %v), want (\"\", true)", path, inTest)
	}

	t.Setenv("GT_TEST_NUDGE_LOG", "/tmp/x.log")
	if path, inTest := testNudgeHook(); !inTest || path != "/tmp/x.log" {
		t.Errorf("set value: got (%q, %v), want (\"/tmp/x.log\", true)", path, inTest)
	}

	if err := os.Unsetenv("GT_TEST_NUDGE_LOG"); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if path, inTest := testNudgeHook(); inTest {
		t.Errorf("unset: got (%q, %v), want inTest=false", path, inTest)
	}
}

func TestResolveChannelRig(t *testing.T) {
	townRoot := newFakeTown(t)

	t.Run("explicit flag wins over environment", func(t *testing.T) {
		t.Setenv("GT_RIG", "from-env")
		if got := resolveChannelRig("from-flag", townRoot); got != "from-flag" {
			t.Errorf("got %q, want from-flag", got)
		}
	})

	t.Run("environment wins over cwd", func(t *testing.T) {
		t.Setenv("GT_RIG", "from-env")
		t.Chdir(t.TempDir())
		if got := resolveChannelRig("", townRoot); got != "from-env" {
			t.Errorf("got %q, want from-env", got)
		}
	})

	t.Run("falls back to cwd", func(t *testing.T) {
		t.Setenv("GT_RIG", "")
		rigDir := filepath.Join(townRoot, "gastown", "polecats", "fury")
		if err := os.MkdirAll(rigDir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		t.Chdir(rigDir)
		if got := resolveChannelRig("", townRoot); got != "gastown" {
			t.Errorf("got %q, want gastown", got)
		}
	})

	t.Run("unresolvable rig is empty, not a guess", func(t *testing.T) {
		t.Setenv("GT_RIG", "")
		t.Chdir(townRoot)
		// ChannelDir turns this into an actionable error for rig-scoped
		// channels; guessing a rig here would recreate the collision.
		if got := resolveChannelRig("", townRoot); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestRegisterChannelConsumer(t *testing.T) {
	t.Parallel()

	t.Run("first consumer has no conflict", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if other := registerChannelConsumer(dir, "gt-refinery"); other != "" {
			t.Errorf("got conflict %q, want none", other)
		}
	})

	t.Run("re-registering the same consumer is not a conflict", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		registerChannelConsumer(dir, "gt-refinery")
		if other := registerChannelConsumer(dir, "gt-refinery"); other != "" {
			t.Errorf("got conflict %q on refresh, want none", other)
		}
	})

	t.Run("second live consumer conflicts", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		registerChannelConsumer(dir, "gt-refinery")
		other := registerChannelConsumer(dir, "bd-refinery")
		if other != "gt-refinery" {
			t.Errorf("got %q, want gt-refinery", other)
		}
	})

	t.Run("departed consumer is pruned, not reported", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		registerChannelConsumer(dir, "gt-refinery")

		// Age the registration past the TTL: a consumer that stopped
		// refreshing is gone, and must not block its successor forever.
		stale := filepath.Join(dir, consumersDirName, "gt-refinery.consumer")
		old := time.Now().Add(-2 * consumerTTL)
		if err := os.Chtimes(stale, old, old); err != nil {
			t.Fatalf("aging registration: %v", err)
		}

		if other := registerChannelConsumer(dir, "bd-refinery"); other != "" {
			t.Errorf("got conflict %q from a departed consumer, want none", other)
		}
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Errorf("stale registration should have been pruned: %v", err)
		}
	})

	t.Run("empty consumer id is ignored", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if other := registerChannelConsumer(dir, ""); other != "" {
			t.Errorf("got %q, want empty", other)
		}
		if _, err := os.Stat(filepath.Join(dir, consumersDirName)); !os.IsNotExist(err) {
			t.Error("no consumers dir should be created for an empty id")
		}
	})

	t.Run("registrations are invisible to event readers", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		registerChannelConsumer(dir, "gt-refinery")

		// Bookkeeping lives in a subdirectory and carries a non-.event
		// suffix, so an await-event scan cannot mistake it for an event.
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() != consumersDirName {
				t.Errorf("unexpected entry %q in channel dir", entry.Name())
			}
		}
	})
}
