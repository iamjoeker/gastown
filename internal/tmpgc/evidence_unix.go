//go:build !windows

package tmpgc

import (
	"fmt"
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether the file is owned by the uid running this
// process. Go work directories are created 0700 by their owner, so a directory
// owned by anyone else is not ours to reclaim.
func ownedByCurrentUser(fi os.FileInfo) (bool, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("no stat information for %s", fi.Name())
	}
	return int(st.Uid) == os.Getuid(), nil
}
