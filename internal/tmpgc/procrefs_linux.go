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

// liveReferences reports which of the given paths are named in the argv of a
// running process.
//
// The Go driver passes $WORK paths to every subprocess it spawns — compile,
// link, vet, asm all receive -o $WORK/b001/... — so a build in progress names
// its own work directory in the process table even when the driver itself does
// not.
//
// Matching is a plain substring test, which can only over-match: a longer
// directory's path contains a shorter one's as a prefix, so /tmp/go-build12 is
// reported live while /tmp/go-build123 is in use. Over-matching keeps a
// directory; under-matching would delete a live build's scratch space.
//
// Any failure to read a process owned by the current user makes the whole
// answer inconclusive, and the caller removes nothing. A process owned by
// another user cannot be using a 0700 directory owned by us, so an unreadable
// entry belonging to someone else is skipped rather than fatal — hidepid hosts
// would otherwise disable the sweep permanently.
func liveReferences(paths []string) (map[string]bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}

	uid := os.Getuid()
	refs := make(map[string]bool, len(paths))
	for _, entry := range entries {
		if !entry.IsDir() || !isPIDName(entry.Name()) {
			continue
		}
		procDir := filepath.Join("/proc", entry.Name())

		data, err := os.ReadFile(filepath.Join(procDir, "cmdline"))
		if err != nil {
			// A process that exited between the listing and the read is
			// holding nothing.
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				continue
			}
			ours, ownerErr := procOwnedByCurrentUser(procDir, uid)
			if ownerErr != nil || ours {
				return nil, fmt.Errorf("read %s/cmdline: %w", procDir, err)
			}
			continue
		}

		// cmdline is NUL-separated; substring matching is unaffected by the
		// separator, and NUL cannot appear inside a path.
		cmdline := string(data)
		for _, p := range paths {
			if !refs[p] && strings.Contains(cmdline, p) {
				refs[p] = true
			}
		}
	}
	return refs, nil
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
