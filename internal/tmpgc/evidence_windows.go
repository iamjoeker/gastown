//go:build windows

package tmpgc

import (
	"errors"
	"os"
)

// ownedByCurrentUser has no cheap portable answer on Windows. Refusing here
// means the sweep reports candidates and removes nothing, which is the correct
// failure direction for an rm -rf path.
func ownedByCurrentUser(os.FileInfo) (bool, error) {
	return false, errors.New("ownership check unsupported on windows")
}
