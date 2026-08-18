package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/dog"
)

func readDogLog(t *testing.T, townRoot, name string) string {
	t.Helper()
	data, err := os.ReadFile(dog.SessionLogPath(townRoot, name))
	if err != nil {
		return ""
	}
	return string(data)
}

// TestDogWarnReachesDurableSurface is the wiring test for gt-wlco.
//
// The dog layer's error reporting used bare fmt.Fprintf(os.Stderr, ...). For a
// dog that is a write-only surface: `gt dog done` kills the tmux session three
// seconds after printing, so the pane holding the message is destroyed before
// any operator or patrol can read it. gt-u58w's cleanup-failure report went
// there and was never seen once while the leak it reported grew 230 -> 559.
//
// dogWarn must therefore write to the log file as well as stderr. Asserting on
// stderr would prove nothing — stderr is the surface that does not survive.
func TestDogWarnReachesDurableSurface(t *testing.T) {
	townRoot := t.TempDir()
	mgr := dog.NewManager(townRoot, nil)

	dogWarn(mgr, "alpha", "dispatch-mail cleanup incomplete for %s (%d archived)", "alpha", 0)

	got := readDogLog(t, townRoot, "alpha")
	if got == "" {
		t.Fatal("dogWarn wrote nothing durable. The warning exists only on a stderr stream " +
			"attached to a tmux pane that gt dog done destroys — that is the gt-wlco defect.")
	}
	if !strings.Contains(got, "dispatch-mail cleanup incomplete for alpha (0 archived)") {
		t.Errorf("session log = %q, want the formatted warning", got)
	}
	if !strings.Contains(got, "WARN") {
		t.Errorf("session log = %q, want warnings marked so they can be grepped apart from outcomes", got)
	}
}

// TestDogRecordLogsSuccessOutcomes: a log that holds only failures cannot
// distinguish a dog that succeeded from one that never ran. The bead's own
// diagnosis — "we can see what was sent and never what happened" — is about
// missing outcomes, not only missing errors.
func TestDogRecordLogsSuccessOutcomes(t *testing.T) {
	townRoot := t.TempDir()
	mgr := dog.NewManager(townRoot, nil)

	dogRecord(mgr, "alpha", "dog done: work=%q complete, dog returned to kennel (idle)", "plugin:rebuild-gt")

	got := readDogLog(t, townRoot, "alpha")
	if !strings.Contains(got, `dog done: work="plugin:rebuild-gt" complete`) {
		t.Errorf("session log = %q, want the completion record", got)
	}
	if strings.Contains(got, "WARN") {
		t.Errorf("session log = %q, want successes unmarked so WARN stays a useful filter", got)
	}
}

func TestReportClosedPluginMailsSkipsZero(t *testing.T) {
	townRoot := t.TempDir()
	mgr := dog.NewManager(townRoot, nil)

	// An empty inbox is the common, healthy case; recording it every time would
	// bury the entries that matter.
	reportClosedPluginMails(mgr, "alpha", 0)
	if got := readDogLog(t, townRoot, "alpha"); got != "" {
		t.Errorf("session log = %q, want nothing recorded for a zero-mail cleanup", got)
	}

	reportClosedPluginMails(mgr, "alpha", 3)
	if got := readDogLog(t, townRoot, "alpha"); !strings.Contains(got, "archived 3 mail(s)") {
		t.Errorf("session log = %q, want the archived count recorded", got)
	}
}

// TestDogHelpersToleratteNilManager: `gt dog done` runs in odd places (a dog
// worktree outside a resolvable town, a test harness). Logging must never be
// the thing that panics the command that was reporting a failure.
func TestDogHelpersTolerateNilManager(t *testing.T) {
	dogWarn(nil, "alpha", "no manager available")
	dogRecord(nil, "alpha", "no manager available")
	reportClosedPluginMails(nil, "alpha", 2)
}

func TestDetectDogNameFromCwd(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "deacon", "dogs", "alpha", "gastown")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatalf("creating worktree: %v", err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if err := os.Chdir(worktree); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	got, err := detectDogNameFromCwd()
	if err != nil {
		t.Fatalf("detectDogNameFromCwd: %v", err)
	}
	if got != "alpha" {
		t.Errorf("detectDogNameFromCwd = %q, want %q", got, "alpha")
	}

	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	if _, err := detectDogNameFromCwd(); err == nil {
		t.Error("detectDogNameFromCwd outside a kennel = nil error, want an error")
	}
}
