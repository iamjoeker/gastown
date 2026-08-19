package doltserver

import (
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// dirBirthTime returns the filesystem birth (creation) time of path, or
// ok=false where the platform or filesystem does not record one.
//
// Birth time is the timestamp that discriminates a test fixture from a real
// database, which is why orphan reporting uses it rather than mtime: mtime
// moves on every write, so a busy production database looks exactly as new as
// a fixture created seconds ago.
func dirBirthTime(path string) (time.Time, bool) {
	return util.DirBirthTime(path)
}
