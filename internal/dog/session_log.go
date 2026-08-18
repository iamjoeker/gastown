package dog

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The dog session log exists because a dog's stdout/stderr goes nowhere durable.
//
// A dog runs inside a tmux session that `gt dog done` terminates a few seconds
// after the work finishes. Anything written to the pane — a warning, a panic, a
// plugin's output — dies with the pane. That is not a cosmetic gap: it defeats
// error reporting for the whole dog layer by construction. gt-u58w made
// `gt dog done` report its dispatch-mail cleanup failure to stderr; the report
// landed in a pane that was destroyed seconds later, so dispatch mail kept
// leaking (230 -> 559 across the pack) with zero output reaching any surface an
// operator could read. A fix that reports into a dying session is not a fix.
//
// So every dog-side diagnostic is appended here instead, at
// <townRoot>/deacon/dogs/<name>/session.log, next to the dog's .dog.json and
// mirroring the daemon.log convention. The file outlives the session, the dog,
// and the operator's attention span.
//
// Writes are best-effort and never block dog state transitions: a dog stuck
// non-idle is worse than a dog with an unwritten log line. Callers surface the
// returned error rather than swallowing it — that is the same rule gt-u58w was
// filed for.

const (
	// SessionLogName is the per-dog durable log file, inside the dog's kennel
	// directory (<townRoot>/deacon/dogs/<name>/).
	SessionLogName = "session.log"

	// sessionLogMaxSize is the size at which the log is rotated to
	// session.log.1. One backup is kept. Dogs write a handful of lines per
	// session, so this bounds the kennel at a few MB per dog even if a plugin
	// goes haywire.
	sessionLogMaxSize int64 = 4 * 1024 * 1024
)

// SessionLogPath returns the durable session log path for a dog.
func SessionLogPath(townRoot, dogName string) string {
	return filepath.Join(townRoot, "deacon", "dogs", dogName, SessionLogName)
}

// SessionLogPath returns the durable session log path for a dog.
func (m *Manager) SessionLogPath(name string) string {
	return filepath.Join(m.dogDir(name), SessionLogName)
}

// LogEvent appends a timestamped entry to the dog's durable session log.
//
// It is best-effort: a failure to log must not abort the caller's work. Callers
// should report the returned error rather than discard it, so a log that has
// stopped recording does not itself become invisible.
func (m *Manager) LogEvent(name, format string, args ...any) error {
	return AppendSessionLog(m.townRoot, name, fmt.Sprintf(format, args...))
}

// AppendSessionLog appends msg to a dog's session log, one timestamped line per
// line of msg so multi-line errors stay greppable.
func AppendSessionLog(townRoot, dogName, msg string) error {
	if townRoot == "" {
		return fmt.Errorf("dog session log: town root is empty")
	}
	if err := validateDogName(dogName); err != nil {
		return fmt.Errorf("dog session log: %w", err)
	}

	path := SessionLogPath(townRoot, dogName)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("dog session log: creating kennel dir for %s: %w", dogName, err)
	}

	rotateSessionLog(path)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("dog session log: opening %s: %w", path, err)
	}
	defer f.Close()

	// One write call: O_APPEND makes a single small write atomic, so concurrent
	// writers (the done goroutine and the deacon's health-check) interleave by
	// record rather than mid-line.
	if _, err := f.WriteString(formatSessionLogEntry(time.Now(), msg)); err != nil {
		return fmt.Errorf("dog session log: writing %s: %w", path, err)
	}
	return nil
}

// formatSessionLogEntry renders msg as timestamped log lines. Every line of a
// multi-line message carries the timestamp, so `grep` on the log never returns
// an orphan continuation line with no time context.
func formatSessionLogEntry(now time.Time, msg string) string {
	stamp := now.Format(time.RFC3339)
	msg = strings.TrimRight(msg, "\n")
	if msg == "" {
		return stamp + "\n"
	}

	var b strings.Builder
	for _, line := range strings.Split(msg, "\n") {
		b.WriteString(stamp)
		b.WriteByte(' ')
		b.WriteString(strings.TrimRight(line, "\r"))
		b.WriteByte('\n')
	}
	return b.String()
}

// rotateSessionLog moves an oversized log to session.log.1, keeping one backup.
// Best-effort: if rotation fails the log simply keeps growing, which is better
// than dropping the entry we were about to write.
func rotateSessionLog(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < sessionLogMaxSize {
		return
	}
	_ = os.Rename(path, path+".1")
}

// ReadSessionLog returns the last n lines of a dog's session log, oldest first.
// n <= 0 returns the whole file. A missing log is not an error: it means the dog
// has not recorded anything yet.
func ReadSessionLog(townRoot, dogName string, n int) ([]string, error) {
	if err := validateDogName(dogName); err != nil {
		return nil, fmt.Errorf("dog session log: %w", err)
	}

	path := SessionLogPath(townRoot, dogName)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("dog session log: opening %s: %w", path, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	// Plugin output can produce long lines; the default 64KB cap would abort the
	// scan with an error and hide the rest of the log.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if n > 0 && len(lines) > n {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return lines, fmt.Errorf("dog session log: reading %s: %w", path, err)
	}
	return lines, nil
}
