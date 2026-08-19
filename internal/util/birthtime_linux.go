//go:build linux

package util

import (
	"time"

	"golang.org/x/sys/unix"
)

// DirBirthTime returns the filesystem birth (creation) time of path.
//
// Birth time is what discriminates one directory's origin from another's,
// which is why callers use it rather than mtime: mtime moves on every write,
// so a busy directory looks exactly as new as one created seconds ago. Orphan
// database reporting uses it to tell a test fixture from a real database, and
// the scratchpad sweep uses it to tie a session directory to the process that
// created it.
//
// statx(STATX_BTIME) is best-effort — old kernels and some filesystems do not
// record it. When it is unavailable this reports ok=false instead of
// substituting mtime or ctime: a timestamp that means something else is worse
// evidence than no timestamp, because the caller cannot tell which they got.
func DirBirthTime(path string) (time.Time, bool) {
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_STATX_SYNC_AS_STAT, unix.STATX_BTIME, &st); err != nil {
		return time.Time{}, false
	}
	if st.Mask&unix.STATX_BTIME == 0 {
		return time.Time{}, false
	}
	return time.Unix(st.Btime.Sec, int64(st.Btime.Nsec)), true
}
