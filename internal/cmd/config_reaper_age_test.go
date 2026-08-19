package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/daemon"
)

// TestReaperAgeKeysRoundTripToDaemonJSON covers the operator half of the chain the
// auto-close bug broke (gt-zjb, gt-7hs): `gt config set` -> daemon.json -> the field
// the daemon's accessor reads.
//
// The original defect was not a wrong number, it was a missing link: stale_issue_age
// and mail_delete_age had a formula var and a documented default but no reader, so the
// daemon ran a package constant while every operator surface described something else.
//
// This test stops at the struct field on purpose. The field is the join, and it is
// checked by the compiler — the daemon-side half (field -> accessor -> the duration
// handed to reaper.AutoClose) is guarded by TestStaleIssueAge and
// TestReaperFormulaVarsAreConfigurable in internal/daemon. That is a different
// situation from the bug, where the two ends were a TOML string and a Go constant
// with nothing between them to break.
func TestReaperAgeKeysRoundTripToDaemonJSON(t *testing.T) {
	tests := []struct {
		key  string
		set  string
		read func(*daemon.WispReaperConfig) string
	}{
		{
			key:  "lifecycle.reaper.stale_issue_age",
			set:  "1440h",
			read: func(c *daemon.WispReaperConfig) string { return c.StaleIssueAgeStr },
		},
		{
			key:  "lifecycle.reaper.mail_delete_age",
			set:  "336h",
			read: func(c *daemon.WispReaperConfig) string { return c.MailDeleteAgeStr },
		},
	}

	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			townRoot := t.TempDir()

			if err := setLifecycleConfig(townRoot, tc.key, tc.set); err != nil {
				t.Fatalf("set %s=%s: %v", tc.key, tc.set, err)
			}

			// Reload from disk rather than reusing the in-memory struct — the
			// question is whether the value survives the file, not whether the
			// setter assigned a field.
			cfg := daemon.LoadPatrolConfig(townRoot)
			if cfg == nil || cfg.Patrols == nil || cfg.Patrols.WispReaper == nil {
				t.Fatalf("set %s wrote a daemon.json with no wisp_reaper patrol", tc.key)
			}
			if got := tc.read(cfg.Patrols.WispReaper); got != tc.set {
				t.Errorf("after `gt config set %s %s`, daemon.json holds %q.\n"+
					"The key is accepted but does not reach the field the daemon reads — that is "+
					"the shape of the original bug, not a value mismatch.", tc.key, tc.set, got)
			}
		})
	}

	// getLifecycleConfig must also know the keys. It prints to stdout, so this
	// asserts only that it does not reject them as unknown — a key you can set
	// but not read back is half a config surface.
	for _, tc := range tests {
		townRoot := t.TempDir()
		if err := getLifecycleConfig(townRoot, tc.key); err != nil {
			t.Errorf("gt config get %s: %v", tc.key, err)
		}
	}
}

// TestReaperAgeKeysRejectGarbage guards the direction that matters most: a value
// that fails to parse must be refused at the CLI, not stored and skipped later.
// A rejected write leaves the documented default acting and says so; an accepted
// write that nothing can parse leaves the operator believing they changed a policy
// that still runs at its old value — which is how the 168h went unnoticed.
func TestReaperAgeKeysRejectGarbage(t *testing.T) {
	for _, key := range []string{"lifecycle.reaper.stale_issue_age", "lifecycle.reaper.mail_delete_age"} {
		townRoot := t.TempDir()
		if err := setLifecycleConfig(townRoot, key, "30 days"); err == nil {
			t.Errorf("set %s = %q was accepted; an unparseable duration must be refused "+
				"at the point of entry", key, "30 days")
		}
	}
}
