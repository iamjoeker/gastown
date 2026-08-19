//go:build linux

package tmpgc

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// liveReferences reports which of the given paths a running process is using.
//
// Three kinds of evidence are gathered, because no one of them covers every
// family of temporary directory this package sweeps:
//
//   - argv. The Go driver passes $WORK paths to every subprocess it spawns —
//     compile, link, vet, asm all receive -o $WORK/b001/... — so a build in
//     progress names its own work directory in the process table even when the
//     driver itself does not.
//   - cwd. A test harness commonly chdirs into the fixture directory it
//     created and names it nowhere else.
//   - open file descriptors. A dolt sql-server started against a fixture data
//     directory holds .dolt/noms/LOCK open for its whole life; that handle may
//     be the only trace it leaves.
//
// The last two are what `lsof +D` reports, read from the source lsof itself
// reads, and without lsof's fatal ambiguity: lsof answers through an EXIT
// STATUS that is non-zero for "nothing is open", for "I could not read that
// directory", for "I am not installed", and — on hosts with docker nsfs mounts
// — for every single invocation including ones that DID list live handles
// (gt-32z). /proc has no status to misread. A path is either named in the
// table or it is not, and a failure to read the table at all is returned as an
// error, which the caller turns into a refusal to remove anything.
//
// argv is matched as a plain substring, which can only over-match: a longer
// directory's path contains a shorter one's as a prefix, so /tmp/go-build12 is
// reported live while /tmp/go-build123 is in use. cwd and fd links are real
// resolved paths, so those are matched as "the candidate itself or something
// beneath it". Over-matching keeps a directory; under-matching deletes live
// data.
//
// Only processes owned by the current user are inspected for cwd and fd
// evidence — /proc denies those links for anyone else's process, exactly as it
// denies them to a non-root lsof. Every directory this package will remove is
// owned by the current user, so a foreign process cannot be holding one it
// created. argv, which /proc publishes for every process, is read for all of
// them regardless.
//
// # Processes even their owner cannot look inside
//
// Reading cwd and the fd links needs PTRACE_MODE_READ, which the kernel denies
// for a process that has marked itself non-dumpable even to the uid that owns
// it. This is not exotic: measured on the host that produced gt-1gdh,
// 14 of 251 processes owned by the user — `systemd --user` and its friends —
// refuse to publish their cwd. Treating that as a failure to gather evidence
// would make the sweep permanently inconclusive on an ordinary desktop, and a
// sweep that can never remove anything is not a safe sweep, it is an inert one:
// TMPDIR fills anyway and the guard reads as working. So those processes are
// SKIPPED for cwd and fd, still read for argv, and COUNTED. The count is
// carried out to the caller and printed, because a report that quietly omitted
// part of its coverage would read as a clean sweep.
//
// Any OTHER failure to read a process owned by the current user is fatal and
// makes the whole answer inconclusive, so the caller removes nothing. A
// process owned by another user cannot be using a 0700 directory owned by us,
// so an unreadable entry belonging to someone else is skipped rather than
// fatal — hidepid hosts would otherwise disable the sweep permanently.
func liveReferences(paths []string) (Evidence, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return Evidence{}, fmt.Errorf("read /proc: %w", err)
	}

	aliases := pathAliases(paths)
	uid := os.Getuid()
	ev := Evidence{Refs: make(map[string]bool, len(paths))}

	for _, entry := range entries {
		if !entry.IsDir() || !isPIDName(entry.Name()) {
			continue
		}
		if err := scanProcess(filepath.Join("/proc", entry.Name()), uid, aliases, &ev); err != nil {
			return Evidence{}, err
		}
	}
	return ev, nil
}

// pathAliases maps every spelling a candidate can be reached by back to the
// caller's spelling of it. /proc resolves symlinks in the links it publishes,
// so a TMPDIR reached through one (/tmp -> /private/tmp) would be spelled
// differently there than here, and the match would silently miss. A miss is
// the direction that deletes a live directory, so both forms are searched.
func pathAliases(paths []string) map[string]string {
	aliases := make(map[string]string, len(paths)*2)
	for _, p := range paths {
		aliases[p] = p
		if resolved, err := filepath.EvalSymlinks(p); err == nil && resolved != p {
			aliases[resolved] = p
		}
	}
	return aliases
}

// scanProcess gathers every kind of evidence one process can offer.
func scanProcess(procDir string, uid int, aliases map[string]string, ev *Evidence) error {
	// cmdline is world-readable, so argv evidence is collected for every
	// process on the host, not just ours.
	data, err := os.ReadFile(filepath.Join(procDir, "cmdline"))
	if err != nil {
		if vanished(err) {
			// A process that exited between the listing and the read is
			// holding nothing.
			return nil
		}
		ours, ownerErr := procOwnedByCurrentUser(procDir, uid)
		if ownerErr != nil || ours {
			return fmt.Errorf("read %s/cmdline: %w", procDir, err)
		}
		return nil
	}
	// cmdline is NUL-separated; substring matching is unaffected by the
	// separator, and NUL cannot appear inside a path.
	cmdline := string(data)
	for alias, orig := range aliases {
		if !ev.Refs[orig] && strings.Contains(cmdline, alias) {
			ev.Refs[orig] = true
		}
	}

	// cwd and fd links are readable only for our own processes. Asking about
	// anyone else's is not an error to report, it is a question /proc does not
	// answer for us.
	ours, err := procOwnedByCurrentUser(procDir, uid)
	if err != nil {
		return fmt.Errorf("owner of %s: %w", procDir, err)
	}
	if !ours {
		return nil
	}

	opaque := false
	if target, err := os.Readlink(filepath.Join(procDir, "cwd")); err == nil {
		markPathRef(target, aliases, ev)
	} else if denied(err) {
		opaque = true
	} else if !vanished(err) {
		// Ours, present, readable in principle, and still failing: we cannot
		// say what this process is holding, and not knowing is never
		// permission to delete.
		return fmt.Errorf("readlink %s/cwd: %w", procDir, err)
	}

	fdDir := filepath.Join(procDir, "fd")
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		switch {
		case vanished(err):
			// Nothing left to hold anything.
		case denied(err):
			opaque = true
		default:
			return fmt.Errorf("read %s: %w", fdDir, err)
		}
	}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
		if err != nil {
			switch {
			case vanished(err):
				// A descriptor closed between the listing and the readlink is
				// holding nothing.
			case denied(err):
				opaque = true
			default:
				return fmt.Errorf("readlink %s/%s: %w", fdDir, fd.Name(), err)
			}
			continue
		}
		markPathRef(target, aliases, ev)
	}
	if opaque {
		ev.OpaqueProcesses++
	}
	return nil
}

// markPathRef records a candidate as live when target is the candidate itself
// or anything beneath it.
//
// A deleted file's link reads as "<path> (deleted)". The prefix test still
// matches it, which is correct: a process still holding an unlinked file
// inside the tree is still using the tree. Descriptors that are not files at
// all — "socket:[12345]", "pipe:[678]", "anon_inode:..." — cannot carry a
// leading "/tmp/..." and simply never match.
func markPathRef(target string, aliases map[string]string, ev *Evidence) {
	for alias, orig := range aliases {
		if ev.Refs[orig] {
			continue
		}
		if target == alias || strings.HasPrefix(target, alias+string(filepath.Separator)) {
			ev.Refs[orig] = true
		}
	}
}

// vanished reports whether an error means the process or descriptor went away
// mid-scan, as opposed to a failure to look.
func vanished(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

// denied reports whether the kernel refused to publish a link. For a process
// we own this means it has marked itself non-dumpable; see liveReferences.
func denied(err error) bool {
	return errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

// procOwnedByCurrentUser reports whether a /proc/<pid> directory belongs to the
// current uid.
func procOwnedByCurrentUser(procDir string, uid int) (bool, error) {
	fi, err := os.Stat(procDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("no stat information for %s", procDir)
	}
	return int(st.Uid) == uid, nil
}

// isPIDName reports whether a /proc entry name is a process id.
func isPIDName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
