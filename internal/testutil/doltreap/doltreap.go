// Package doltreap registers test cleanup that stops the Dolt servers a test
// provably owns.
//
// It is a separate package from internal/testutil, its only caller's natural
// home, because it imports internal/doltserver and doltserver's own
// integration-tagged tests import testutil. Held together in testutil the two
// form an import cycle that is invisible to the default build and only
// surfaces under a build tag — which is how the whole integration suite for
// internal/doltserver stayed unbuildable without any gate going red (gt-pp8o).
// Keeping the doltserver edge in a leaf package no test package imports back
// means testutil can stay importable from anywhere, doltserver included.
package doltreap

import (
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// OnCleanup registers test cleanup for Dolt servers whose metadata and process
// args prove they belong to townRoot. It never kills by broad name or port, so
// production Dolt is protected when tests run inside a real workspace.
func OnCleanup(t testing.TB, townRoot string) {
	t.Helper()
	t.Cleanup(func() {
		stopped, err := doltserver.ReapOwnedTestServers(townRoot)
		if err != nil {
			t.Logf("owned Dolt cleanup skipped: %v", err)
			return
		}
		if stopped > 0 {
			t.Logf("stopped %d owned Dolt sql-server process(es)", stopped)
		}
	})
}
