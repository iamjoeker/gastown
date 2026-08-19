package web

// A panel that says "unavailable" without saying WHY has replaced one useless
// value with another. gt-egq9 gave runCmd a stderr capture for exactly this
// reason and left runBdCmd — its bd twin — untouched, so every bd-backed panel
// still renders the literal text "exit status 1" (gt-1jrl).
//
// These tests assert on the REASON carried by the error, not on its existence:
// an assertion that a failed query errors passes just as well against an error
// that says nothing, which is the state being fixed here. Each is paired with a
// control that a successful query stays silent.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- runBdCmd ---------------------------------------------------------------

func TestRunBdCmd_CarriesBdsOwnWords(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	f := fakeBdFetcher(t, `#!/bin/sh
echo "dial tcp 127.0.0.1:3307: connect: connection refused" >&2
exit 1
`)

	_, err := f.runBdCmd(f.townRoot, "list", "--json")
	if err == nil {
		t.Fatal("a bd that exits non-zero with no stdout must return an error")
	}
	// "connection refused" and "unknown label" send an operator to different
	// places; "exit status 1" sends them nowhere.
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error must carry bd's stderr, got: %v", err)
	}
}

func TestRunBdCmd_ReasonIsOneLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	// bd is happy to print a stack trace or a multi-line hint. The reason is
	// rendered inline in a panel notice, so only the first line is carried.
	f := fakeBdFetcher(t, `#!/bin/sh
echo "connection refused" >&2
echo "hint: is the dolt server running?" >&2
echo "  see gt dolt status" >&2
exit 1
`)

	_, err := f.runBdCmd(f.townRoot, "list", "--json")
	if err == nil {
		t.Fatal("a bd that exits non-zero with no stdout must return an error")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("panel notices are one line; error was multi-line: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("the first stderr line is the one to keep, got: %v", err)
	}
}

func TestRunBdCmd_SuccessfulQueryHasNoReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	// Control. bd writes progress and deprecation chatter to stderr on happy
	// paths; folding stderr into an error must not invent one.
	f := fakeBdFetcher(t, `#!/bin/sh
echo "note: using flat output" >&2
echo "[]"
`)

	stdout, err := f.runBdCmd(f.townRoot, "list", "--json")
	if err != nil {
		t.Fatalf("a successful bd must not error because it wrote to stderr: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "[]" {
		t.Errorf("stdout = %q, want %q", got, "[]")
	}
}

func TestFetchQueues_PanelNoticeNamesTheCause(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	// The end-to-end shape: what the operator reads on the Queues panel is this
	// error's text. TestFetchQueues_BdFailureIsAnErrorNotAnEmptyList already
	// asserts an error arrives; this asserts it is worth reading.
	f := fakeBdFetcher(t, `#!/bin/sh
echo "dial tcp 127.0.0.1:3307: connect: connection refused" >&2
exit 1
`)

	if _, err := f.FetchQueues(); err == nil {
		t.Fatal("a failed bd query must return an error")
	} else if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("the queue panel would say %q, which names no cause", err.Error())
	}
}

// --- runCmd -----------------------------------------------------------------

func TestRunCmd_ReasonIsOneLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	script := filepath.Join(t.TempDir(), "noisy")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
echo "no server running on /tmp/tmux-1000/gtsock" >&2
echo "second line of noise" >&2
exit 1
`), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	_, err := runCmd(30*time.Second, script)
	if err == nil {
		t.Fatal("a command that exits non-zero must return an error")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("panel notices are one line; error was multi-line: %q", err.Error())
	}
	// The classification tmuxServerAbsent performs reads this same string, so
	// trimming to one line must not trim away the fact it branches on.
	if !tmuxServerAbsent(err) {
		t.Errorf("the no-server fact must survive the trim, got: %v", err)
	}
}

// --- Dogs -------------------------------------------------------------------

// FetchDogs already distinguishes a missing kennel from an unreadable one. What
// these pin down is that the distinction reaches the reader: the handler used to
// log the error and hand the template an empty slice, so an unreadable kennel
// rendered "No dogs in kennel" (gt-1jrl).

func TestFetchDogs_UnreadableKennelIsAnError(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}

	// A file where the kennel directory should be: present, and unreadable as a
	// directory.
	kennelPath := filepath.Join(f.townRoot, "deacon", "dogs")
	if err := os.MkdirAll(filepath.Dir(kennelPath), 0o755); err != nil {
		t.Fatalf("create deacon dir: %v", err)
	}
	if err := os.WriteFile(kennelPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("create unreadable kennel: %v", err)
	}

	rows, err := f.FetchDogs()
	if err == nil {
		t.Fatal("an unreadable kennel must return an error, not an empty kennel")
	}
	if !strings.Contains(err.Error(), "reading kennel") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the kennel could not be read", rows)
	}
}

func TestFetchDogs_MissingKennelIsNotAnError(t *testing.T) {
	// Control, and the reason this is not a one-line fix: a town with no kennel
	// has never had a dog. The filesystem ANSWERED, so zero really is zero, and
	// a caveat here would appear on every young town and stop meaning anything.
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}

	rows, err := f.FetchDogs()
	if err != nil {
		t.Fatalf("a town with no kennel is an empty kennel, not a failure: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

func TestFetchDogs_EmptyKennelIsNotAnError(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}

	if err := os.MkdirAll(filepath.Join(f.townRoot, "deacon", "dogs"), 0o755); err != nil {
		t.Fatalf("create kennel: %v", err)
	}

	rows, err := f.FetchDogs()
	if err != nil {
		t.Fatalf("a kennel that exists and holds nothing must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

// --- Activity ---------------------------------------------------------------

func TestFetchActivity_EmptyFileIsNotConfusedWithAMissingOne(t *testing.T) {
	// The last of the nine swallows: a second `return nil, nil` guarded by
	// len(lines) == 0, which strings.Split cannot produce. It was unreachable,
	// so removing it changes no behaviour — this records the behaviour it was
	// meant to guard, so the removal stays honest.
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}

	if err := os.WriteFile(filepath.Join(f.townRoot, ".events.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("write empty event log: %v", err)
	}

	rows, err := f.FetchActivity()
	if err != nil {
		t.Fatalf("an empty event log is a quiet town, not a failure: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}
