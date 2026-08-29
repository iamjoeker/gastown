package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/plugin"
)

// TestExecutePluginScript_Success verifies that a run.sh exiting 0 is
// reported as ResultSuccess with no error.
func TestExecutePluginScript_Success(t *testing.T) {
	dir := t.TempDir()
	writeRunScript(t, dir, "#!/bin/sh\nexit 0\n")

	p := &plugin.Plugin{Name: "ok-plugin", Path: dir, HasRunScript: true}

	result, err := executePluginScript(p)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != plugin.ResultSuccess {
		t.Errorf("expected ResultSuccess, got %q", result)
	}
}

// TestExecutePluginScript_Failure verifies that a run.sh exiting non-zero
// is reported as ResultFailure — this is the bug fixed by gt-6eis: a
// receipt must reflect what actually happened, not an assumed success.
func TestExecutePluginScript_Failure(t *testing.T) {
	dir := t.TempDir()
	writeRunScript(t, dir, "#!/bin/sh\nexit 1\n")

	p := &plugin.Plugin{Name: "bad-plugin", Path: dir, HasRunScript: true}

	result, err := executePluginScript(p)
	if err == nil {
		t.Fatal("expected an error from a failing run.sh")
	}
	if result != plugin.ResultFailure {
		t.Errorf("expected ResultFailure, got %q", result)
	}
}

// TestExecutePluginScript_Timeout verifies that a plugin's execution.timeout
// is honored rather than letting a hung script block gt plugin run forever.
func TestExecutePluginScript_Timeout(t *testing.T) {
	dir := t.TempDir()
	writeRunScript(t, dir, "#!/bin/sh\nsleep 5\nexit 0\n")

	p := &plugin.Plugin{
		Name:         "slow-plugin",
		Path:         dir,
		HasRunScript: true,
		Execution:    &plugin.Execution{Timeout: "50ms"},
	}

	result, err := executePluginScript(p)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if result != plugin.ResultFailure {
		t.Errorf("expected ResultFailure on timeout, got %q", result)
	}
}

func writeRunScript(t *testing.T, dir, contents string) {
	t.Helper()
	path := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil { //nolint:gosec // G306: test fixture, needs to be executable
		t.Fatalf("writing run.sh: %v", err)
	}
}
