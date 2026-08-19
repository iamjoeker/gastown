//go:build linux

package doltserver

import (
	"time"

	"golang.org/x/sys/unix"
)

// dirBirthTime returns the filesystem birth (creation) time of path.
//
// Birth time is the timestamp that discriminates a test fixture from a real
// database, which is why orphan reporting uses it rather than mtime: mtime
// moves on every write, so a busy production database looks exactly as new as
// a fixture created seconds ago.
//
// statx(STATX_BTIME) is best-effort — old kernels and some filesystems do not
// record it. When it is unavailable this reports ok=false instead of
// substituting mtime or ctime: a timestamp that means something else is worse
// evidence than no timestamp, because the operator cannot tell which they got.
func dirBirthTime(path string) (time.Time, bool) {
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_STATX_SYNC_AS_STAT, unix.STATX_BTIME, &st); err != nil {
		return time.Time{}, false
	}
	if st.Mask&unix.STATX_BTIME == 0 {
		return time.Time{}, false
	}
	return time.Unix(st.Btime.Sec, int64(st.Btime.Nsec)), true
}
