//go:build darwin

package util

import (
	"syscall"
	"time"
)

// DirBirthTime returns the filesystem birth (creation) time of path.
// See the linux implementation for why birth time and not mtime.
func DirBirthTime(path string) (time.Time, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return time.Time{}, false
	}
	return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec), true
}
