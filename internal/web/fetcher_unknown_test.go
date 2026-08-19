package web

// Every test here comes in a pair: a failing source must return an error, and a
// working source that finds nothing must NOT. Asserting only the first half
// would pass against a fetcher that always errored; asserting only on the
// returned slice passes against the (nil, nil) bug these replace, because an
// unreadable panel and an empty one returned the identical value (gt-1jrl).

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stubTmux replaces the command runner with one that returns the given output
// and error, and restores it when the test ends.
func stubTmux(t *testing.T, out string, err error) {
	t.Helper()
	original := fetcherRunCmd
	t.Cleanup(func() { fetcherRunCmd = original })
	fetcherRunCmd = func(_ time.Duration, _ string, _ ...string) (*bytes.Buffer, error) {
		if err != nil {
			return nil, err
		}
		return bytes.NewBufferString(out), nil
	}
}

func TestFetchQueues_BdFailureIsAnErrorNotAnEmptyList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	f := fakeBdFetcher(t, `#!/bin/sh
echo "dial tcp 127.0.0.1:3307: connect: connection refused" >&2
exit 1
`)

	rows, err := f.FetchQueues()
	if err == nil {
		t.Fatal("a failed bd query must return an error, not an empty queue list")
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the query failed", rows)
	}
	if !strings.Contains(err.Error(), "listing queues") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	// bd's own words survive into the error. The notice is rendered on the
	// panel, and "connection refused" and "exit status 1" send an operator to
	// very different places.
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error should carry bd's stderr, got: %v", err)
	}
}

func TestFetchQueues_NoQueuesIsNotAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	f := fakeBdFetcher(t, `#!/bin/sh
echo "[]"
`)

	rows, err := f.FetchQueues()
	if err != nil {
		t.Fatalf("a successful empty query must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

func TestFetchWorkers_BrokenTmuxIsAnErrorNotAnEmptyTown(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: townWithRigsConfig(t), tmuxCmdTimeout: time.Second}
	stubTmux(t, "", errors.New("tmux: exit status 1: lost server"))

	rows, err := f.FetchWorkers()
	if err == nil {
		t.Fatal("a tmux failure must return an error, not a town with nobody working")
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the query failed", rows)
	}
}

func TestFetchWorkers_NoTmuxServerIsARealZero(t *testing.T) {
	// The control: tmux answered, and its answer was "there is no server". That
	// really is zero workers, and must not raise a false alarm every time the
	// town is shut down.
	f := &LiveConvoyFetcher{townRoot: townWithRigsConfig(t), tmuxCmdTimeout: time.Second}
	stubTmux(t, "", errors.New("tmux: exit status 1: no server running on /tmp/tmux-1000/gt"))

	rows, err := f.FetchWorkers()
	if err != nil {
		t.Fatalf("no tmux server is a definite zero, not an error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

func TestFetchSessions_BrokenTmuxIsAnErrorNotAnEmptyList(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir(), tmuxCmdTimeout: time.Second}
	stubTmux(t, "", errors.New("exec: \"tmux\": executable file not found in $PATH"))

	rows, err := f.FetchSessions()
	if err == nil {
		t.Fatal("a tmux failure must return an error, not an empty session list")
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the query failed", rows)
	}
}

func TestFetchSessions_NoTmuxServerIsARealZero(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir(), tmuxCmdTimeout: time.Second}
	stubTmux(t, "", errors.New("error connecting to /tmp/tmux-1000/gt (No such file or directory)"))

	rows, err := f.FetchSessions()
	if err != nil {
		t.Fatalf("no tmux server is a definite zero, not an error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

func TestFetchActivity_UnreadableEventLogIsAnError(t *testing.T) {
	townRoot := t.TempDir()
	// A directory where the event log should be: readable path, unreadable
	// file. Independent of the test user's privileges, unlike chmod 0.
	if err := os.Mkdir(filepath.Join(townRoot, ".events.jsonl"), 0o755); err != nil {
		t.Fatalf("create unreadable event log: %v", err)
	}

	f := &LiveConvoyFetcher{townRoot: townRoot}
	rows, err := f.FetchActivity()
	if err == nil {
		t.Fatal("an unreadable event log must return an error, not an empty timeline")
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the log could not be read", rows)
	}
}

func TestFetchActivity_MissingEventLogIsARealZero(t *testing.T) {
	// The control: no log has ever been written, so nothing has happened. That
	// is a fact, not a blind spot.
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}

	rows, err := f.FetchActivity()
	if err != nil {
		t.Fatalf("a town with no event log yet must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

func TestFetchDogs_UnreadableKennelIsAnError(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(townRoot, "deacon"), 0o755); err != nil {
		t.Fatalf("create deacon dir: %v", err)
	}
	// A file where the kennel directory should be: present, but not listable.
	if err := os.WriteFile(filepath.Join(townRoot, "deacon", "dogs"), []byte("x"), 0o644); err != nil {
		t.Fatalf("create unreadable kennel: %v", err)
	}

	f := &LiveConvoyFetcher{townRoot: townRoot}
	rows, err := f.FetchDogs()
	if err == nil {
		t.Fatal("an unreadable kennel must return an error, not an empty dog list")
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the kennel could not be read", rows)
	}
}

func TestFetchDogs_MissingKennelIsARealZero(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}

	rows, err := f.FetchDogs()
	if err != nil {
		t.Fatalf("a town with no kennel yet must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

// townWithRigsConfig builds a town root with an empty rigs.json, which
// FetchWorkers loads before it ever reaches tmux.
func townWithRigsConfig(t *testing.T) string {
	t.Helper()

	townRoot := t.TempDir()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("create mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(`{"rigs":{}}`), 0o644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}
	return townRoot
}
