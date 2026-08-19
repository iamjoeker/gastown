//go:build windows

package cmd

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/style"
)

// cleanupOrphanedClaude is a Windows stub.
// Orphan cleanup requires Unix-specific signals (SIGTERM/SIGKILL).
func cleanupOrphanedClaude(graceSecs int) {
	fmt.Printf("  %s Orphan cleanup not supported on Windows\n",
		style.Dim.Render("○"))
}

// verifyNoOrphans is a Windows stub. It reports that it did nothing rather than
// returning an error, so shutdown does not fail on a platform where there is
// nothing to verify.
func verifyNoOrphans() error {
	fmt.Printf("  %s Orphan verification not supported on Windows\n",
		style.Dim.Render("○"))
	return nil
}
