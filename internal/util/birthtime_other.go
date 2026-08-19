//go:build !linux && !darwin

package util

import "time"

// DirBirthTime reports that no birth time is available on this platform.
// Callers must treat the zero time as "unknown" and omit the evidence rather
// than falling back to mtime, which answers a different question.
func DirBirthTime(path string) (time.Time, bool) {
	return time.Time{}, false
}
