package dog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSessionLogSurvivesTheSession pins the property gt-wlco was filed for.
//
// gt-u58w's fix made `gt dog done` report its dispatch-mail cleanup failure to
// stderr. stderr for a dog is a tmux pane that `gt dog done` itself destroys
// three seconds later, so the report was emitted into a surface that no longer
// existed by the time anyone could look — measured: dispatches went 230 -> 559
// across the pack AFTER the fix shipped, with zero error output reaching any
// durable surface. A diagnostic that cannot outlive the process emitting it is
// not a diagnostic.
//
// The test therefore asserts on the FILE, after the writer has returned: the
// message must be readable by a process that shares nothing with the dog.
func TestSessionLogSurvivesTheSession(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "deacon", "dogs", "alpha"), 0755); err != nil {
		t.Fatalf("creating kennel: %v", err)
	}

	const msg = "dispatch-mail cleanup incomplete for alpha (0 archived): actor guard refused archive"
	if err := AppendSessionLog(townRoot, "alpha", msg); err != nil {
		t.Fatalf("AppendSessionLog: %v", err)
	}

	data, err := os.ReadFile(SessionLogPath(townRoot, "alpha"))
	if err != nil {
		t.Fatalf("reading session log: %v. The dog's only durable surface does not exist, "+
			"so the error it reported is unobservable — the gt-wlco defect.", err)
	}
	if !strings.Contains(string(data), msg) {
		t.Errorf("session log = %q, want it to contain %q", data, msg)
	}
}

func TestAppendSessionLogAppendsRatherThanTruncates(t *testing.T) {
	townRoot := t.TempDir()

	for _, msg := range []string{"first", "second", "third"} {
		if err := AppendSessionLog(townRoot, "alpha", msg); err != nil {
			t.Fatalf("AppendSessionLog(%q): %v", msg, err)
		}
	}

	lines, err := ReadSessionLog(townRoot, "alpha", 0)
	if err != nil {
		t.Fatalf("ReadSessionLog: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines %q, want 3 — an overwriting log loses every entry but the last", len(lines), lines)
	}
	for i, want := range []string{"first", "second", "third"} {
		if !strings.HasSuffix(lines[i], want) {
			t.Errorf("line %d = %q, want it to end with %q", i, lines[i], want)
		}
	}
}

// TestAppendSessionLogCreatesKennelDir covers the dog whose kennel directory is
// missing. The log must still be written: the first thing an operator wants
// after a dog fails to set itself up is the record of that failure.
func TestAppendSessionLogCreatesKennelDir(t *testing.T) {
	townRoot := t.TempDir()

	if err := AppendSessionLog(townRoot, "ghost", "session start"); err != nil {
		t.Fatalf("AppendSessionLog: %v", err)
	}
	if _, err := os.Stat(SessionLogPath(townRoot, "ghost")); err != nil {
		t.Fatalf("stat session log: %v", err)
	}
}

func TestFormatSessionLogEntryStampsEveryLine(t *testing.T) {
	now := time.Date(2026, 8, 18, 22, 38, 52, 0, time.UTC)
	stamp := now.Format(time.RFC3339)

	got := formatSessionLogEntry(now, "cleanup failed:\n  actor guard refused\n  mailbox locked\n")

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines %q, want 3", len(lines), lines)
	}
	for i, line := range lines {
		// Every line carries the timestamp so grepping the log never yields an
		// orphan continuation line with no time context.
		if !strings.HasPrefix(line, stamp+" ") {
			t.Errorf("line %d = %q, want prefix %q", i, line, stamp)
		}
	}
	if !strings.HasSuffix(lines[2], "mailbox locked") {
		t.Errorf("last line = %q, want it to end with %q", lines[2], "mailbox locked")
	}
}

// TestSessionLogRejectsPathTraversal: the dog name reaches the path unescaped,
// and dispatch names come from mail and config. A traversing name must fail
// rather than append to an arbitrary file.
func TestSessionLogRejectsPathTraversal(t *testing.T) {
	townRoot := t.TempDir()

	for _, name := range []string{"../../etc/passwd", "..", "a/b", ""} {
		if err := AppendSessionLog(townRoot, name, "x"); err == nil {
			t.Errorf("AppendSessionLog(%q) = nil, want an error", name)
		}
		if _, err := ReadSessionLog(townRoot, name, 0); err == nil {
			t.Errorf("ReadSessionLog(%q) = nil error, want an error", name)
		}
	}
}

func TestAppendSessionLogRejectsEmptyTownRoot(t *testing.T) {
	if err := AppendSessionLog("", "alpha", "x"); err == nil {
		t.Error("AppendSessionLog with empty town root = nil, want an error; " +
			"otherwise the log lands at a relative path in whatever directory the dog happened to be in")
	}
}

func TestSessionLogRotatesAtMaxSize(t *testing.T) {
	townRoot := t.TempDir()
	path := SessionLogPath(townRoot, "alpha")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("creating kennel: %v", err)
	}

	// Sparse file at the rotation threshold — no need to actually write 4MB.
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating log: %v", err)
	}
	f.Close()
	if err := os.Truncate(path, sessionLogMaxSize); err != nil {
		t.Fatalf("growing log: %v", err)
	}

	if err := AppendSessionLog(townRoot, "alpha", "after rotation"); err != nil {
		t.Fatalf("AppendSessionLog: %v", err)
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("backup %s.1 missing: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat rotated log: %v", err)
	}
	if info.Size() >= sessionLogMaxSize {
		t.Errorf("log is still %d bytes after rotation; the new entry was appended to the old file", info.Size())
	}
	lines, err := ReadSessionLog(townRoot, "alpha", 0)
	if err != nil {
		t.Fatalf("ReadSessionLog: %v", err)
	}
	if len(lines) != 1 || !strings.HasSuffix(lines[0], "after rotation") {
		t.Errorf("post-rotation log = %q, want the single new entry", lines)
	}
}

func TestReadSessionLogTailsAndToleratesMissingFile(t *testing.T) {
	townRoot := t.TempDir()

	// A dog that has recorded nothing is not an error condition.
	lines, err := ReadSessionLog(townRoot, "alpha", 10)
	if err != nil {
		t.Fatalf("ReadSessionLog on missing file: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("got %q, want no lines", lines)
	}

	for _, msg := range []string{"one", "two", "three", "four"} {
		if err := AppendSessionLog(townRoot, "alpha", msg); err != nil {
			t.Fatalf("AppendSessionLog: %v", err)
		}
	}

	lines, err = ReadSessionLog(townRoot, "alpha", 2)
	if err != nil {
		t.Fatalf("ReadSessionLog: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines %q, want 2", len(lines), lines)
	}
	// Tail order is oldest-first within the tail, like `tail -n`.
	if !strings.HasSuffix(lines[0], "three") || !strings.HasSuffix(lines[1], "four") {
		t.Errorf("tail = %q, want the last two entries in order", lines)
	}
}

// TestManagerLogEventWritesToKennel pins that the Manager-level entry point
// lands in the dog's own kennel directory, next to .dog.json. Command code uses
// this path, so a Manager that wrote somewhere else would leave `gt dog logs`
// reporting an empty log for a dog that had recorded failures.
func TestManagerLogEventWritesToKennel(t *testing.T) {
	townRoot := t.TempDir()
	m := NewManager(townRoot, nil)

	if err := m.LogEvent("alpha", "dog done: work=%q complete", "plugin:rebuild-gt"); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}

	if got, want := m.SessionLogPath("alpha"), SessionLogPath(townRoot, "alpha"); got != want {
		t.Errorf("Manager.SessionLogPath = %q, want %q", got, want)
	}
	data, err := os.ReadFile(m.SessionLogPath("alpha"))
	if err != nil {
		t.Fatalf("reading session log: %v", err)
	}
	if !strings.Contains(string(data), `work="plugin:rebuild-gt" complete`) {
		t.Errorf("session log = %q, want the formatted event", data)
	}
}
