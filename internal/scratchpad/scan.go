package scratchpad

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// sessionIDPattern matches the UUID Claude Code names a session directory
// after. Anything else under a project directory is left strictly alone: this
// package only ever removes directories it can name a session for.
var sessionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// DefaultRoot returns the scratchpad root for the current user,
// $TMPDIR/claude-<uid>.
func DefaultRoot() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("claude-%d", os.Getuid()))
}

// Slugify converts a working directory into the project slug Claude Code uses
// as the scratchpad's first path component: every character outside
// [A-Za-z0-9-] becomes "-", so "/home/u/src/duly_noted" becomes
// "-home-u-src-duly-noted".
//
// The mapping is lossy — distinct directories can share a slug. That only ever
// widens which sessions a live process is assumed to own, which is the safe
// direction.
func Slugify(dir string) string {
	if dir == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(dir))
	for _, r := range dir {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// ScanResult is what Scan found under the scratchpad root.
type ScanResult struct {
	// Sessions are the <project-slug>/<session-id> directories.
	Sessions []Session

	// StrayFiles counts entries directly under the root that are not project
	// directories — files agents wrote outside the convention. They belong to
	// no session, so nothing here can prove them dead and this package never
	// removes them; they are reported so an operator can decide.
	StrayFiles int

	// StrayBytes is their total size.
	StrayBytes int64
}

// Scan walks the scratchpad root and measures every session directory.
func Scan(root string) (*ScanResult, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("reading scratchpad root %s: %w", root, err)
	}

	res := &ScanResult{}
	for _, e := range entries {
		path := filepath.Join(root, e.Name())
		if !e.IsDir() {
			res.StrayFiles++
			if info, err := e.Info(); err == nil {
				res.StrayBytes += info.Size()
			}
			continue
		}
		sessions, err := scanProject(path, e.Name())
		if err != nil {
			return nil, err
		}
		res.Sessions = append(res.Sessions, sessions...)
	}
	return res, nil
}

func scanProject(dir, slug string) ([]Session, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) || os.IsPermission(err) {
			// A project directory that vanished mid-scan (or that we cannot
			// read) contributes nothing; it is not an error for the sweep.
			return nil, nil
		}
		return nil, fmt.Errorf("reading project directory %s: %w", dir, err)
	}

	var sessions []Session
	for _, e := range entries {
		if !e.IsDir() || !sessionIDPattern.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		s := Session{ProjectSlug: slug, ID: e.Name(), Path: path}
		s.Birth, s.BirthKnown = util.DirBirthTime(path)
		s.LastWrite, s.Bytes = measure(path)
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// measure returns the newest modification time anywhere in the subtree and its
// total apparent size. The whole subtree matters: a session that has written
// nothing for hours but whose scratchpad file was touched a minute ago is
// active, and only the deepest mtime shows that.
func measure(root string) (time.Time, int64) {
	var newest time.Time
	var bytes int64

	// The directory itself counts: a session that created its scratchpad and
	// then wrote nothing still has a directory mtime.
	if fi, err := os.Stat(root); err == nil {
		newest = fi.ModTime()
	}

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable or racing entries are skipped rather than aborting
			// the walk: a partial measurement can only make a directory look
			// smaller and quieter than it is, and the liveness rules that
			// decide deletion do not depend on this number.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if !d.IsDir() {
			bytes += info.Size()
		}
		return nil
	})
	return newest, bytes
}

// TranscriptRoots returns the directories holding session transcripts:
// ~/.claude/projects and ~/.claude-accounts/*/projects. Claude Code appends to
// a session's transcript on every turn, which makes its mtime the liveness
// signal that survives `claude --resume` reusing an old session id.
func TranscriptRoots(home string) []string {
	if home == "" {
		return nil
	}
	roots := []string{filepath.Join(home, ".claude", "projects")}
	accounts, err := os.ReadDir(filepath.Join(home, ".claude-accounts"))
	if err != nil {
		return roots
	}
	for _, a := range accounts {
		if a.IsDir() {
			roots = append(roots, filepath.Join(home, ".claude-accounts", a.Name(), "projects"))
		}
	}
	return roots
}

// ScanTranscripts maps session id to the newest transcript modification time
// across every root. A session id missing from the map has no transcript
// anywhere.
func ScanTranscripts(roots []string) map[string]time.Time {
	out := make(map[string]time.Time)
	for _, root := range roots {
		projects, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, p := range projects {
			if !p.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(root, p.Name()))
			if err != nil {
				continue
			}
			for _, f := range files {
				name := strings.TrimSuffix(f.Name(), ".jsonl")
				if name == f.Name() || !sessionIDPattern.MatchString(name) {
					continue
				}
				info, err := f.Info()
				if err != nil {
					continue
				}
				if cur, ok := out[name]; !ok || info.ModTime().After(cur) {
					out[name] = info.ModTime()
				}
			}
		}
	}
	return out
}

// Survey is the whole read-only pipeline: what is on disk, what is running,
// and the verdict for each scratchpad. Nothing here deletes anything.
type Survey struct {
	Root      string
	Scan      *ScanResult
	Processes []Process
	Decisions []Decision
}

// Take runs the survey. home is the directory holding the transcript roots,
// normally the user's home directory.
//
// A failure to read the process table is fatal rather than an empty process
// list: with nothing known to be running, every scratchpad would classify as
// dead. Absence of evidence is not evidence of death.
func Take(root, home string, p Policy, now time.Time) (*Survey, error) {
	scan, err := Scan(root)
	if err != nil {
		return nil, err
	}
	procs, err := LiveProcesses(now)
	if err != nil {
		return nil, fmt.Errorf("cannot enumerate live agent processes, refusing to treat any scratchpad as dead: %w", err)
	}
	transcripts := ScanTranscripts(TranscriptRoots(home))
	return &Survey{
		Root:      root,
		Scan:      scan,
		Processes: procs,
		Decisions: Classify(scan.Sessions, procs, transcripts, p, now),
	}, nil
}

// Dead reports how many scratchpads passed every death check, and their size.
func (s *Survey) Dead() (count int, bytes int64) {
	for _, d := range s.Decisions {
		if d.Verdict == VerdictSweep {
			count++
			bytes += d.Session.Bytes
		}
	}
	return count, bytes
}

// Removal records what happened to one selected scratchpad.
type Removal struct {
	Session Session
	Removed bool
	// Reason explains a skip: the directory changed between classification and
	// deletion, or the delete itself failed.
	Reason string
}

// Execute deletes the selected scratchpads, re-checking each one immediately
// before removing it.
//
// The re-check closes the window between classification and deletion: a session
// that came back to life in those seconds — or a scratchpad that turns out to
// have been written since it was measured — is skipped rather than removed.
func Execute(selected []Decision, p Policy, now time.Time) []Removal {
	out := make([]Removal, 0, len(selected))
	for _, d := range selected {
		s := d.Session
		lastWrite, _ := measure(s.Path)
		if lastWrite.After(s.LastWrite) {
			out = append(out, Removal{Session: s, Reason: fmt.Sprintf("written since it was classified (%s ago) — skipped", short(now.Sub(lastWrite)))})
			continue
		}
		if idle := now.Sub(lastWrite); idle < p.Idle {
			out = append(out, Removal{Session: s, Reason: fmt.Sprintf("written %s ago at delete time — skipped", short(idle))})
			continue
		}
		birth, ok := util.DirBirthTime(s.Path)
		if !ok || !birth.Equal(s.Birth) {
			out = append(out, Removal{Session: s, Reason: "birth time changed since it was classified — skipped"})
			continue
		}
		if err := os.RemoveAll(s.Path); err != nil {
			out = append(out, Removal{Session: s, Reason: fmt.Sprintf("remove failed: %v", err)})
			continue
		}
		out = append(out, Removal{Session: s, Removed: true})
	}
	return out
}

// isUnder reports whether path is dir or lives inside it.
func isUnder(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}
