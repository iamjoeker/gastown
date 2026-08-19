package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// The remedy is instruction text an operator executes by hand. These tests pin
// the two properties that made the old text dangerous (gt-xvwu): the stop step
// must stop whatever restarts the server, and the deletion must be gated on a
// verification that the server is actually down.

func TestFilesystemCleanupRemedyUnderSupervisor(t *testing.T) {
	sup := &doltserver.Supervisor{
		Kind:     "systemd",
		Unit:     "gt-dolt.service",
		UserUnit: true,
		Restart:  "always",
	}
	out := filesystemCleanupRemedy("/home/op/gt", sup)

	if !strings.Contains(out, "systemctl --user stop gt-dolt.service") {
		t.Errorf("remedy must stop the unit, got:\n%s", out)
	}
	if !strings.Contains(out, "systemctl --user start gt-dolt.service") {
		t.Errorf("remedy must start the unit, got:\n%s", out)
	}
	if strings.Contains(out, "    gt dolt stop\n") {
		t.Errorf("remedy must not tell a supervised operator to `gt dolt stop`, got:\n%s", out)
	}
	if !strings.Contains(out, "Restart=always") {
		t.Errorf("remedy must name the restart policy that makes `gt dolt stop` ineffective, got:\n%s", out)
	}

	stopIdx := strings.Index(out, "systemctl --user stop")
	verifyIdx := strings.Index(out, "gt dolt status")
	rmIdx := strings.Index(out, "rm -rf")
	startIdx := strings.Index(out, "systemctl --user start")
	if !(stopIdx < verifyIdx && verifyIdx < rmIdx && rmIdx < startIdx) {
		t.Errorf("steps must run stop -> verify -> rm -> start, got:\n%s", out)
	}
}

func TestFilesystemCleanupRemedyWithoutSupervisor(t *testing.T) {
	out := filesystemCleanupRemedy("/home/op/gt", nil)

	if !strings.Contains(out, "gt dolt stop") || !strings.Contains(out, "gt dolt start") {
		t.Errorf("unsupervised remedy should use gt's own stop/start, got:\n%s", out)
	}
	if strings.Contains(out, "systemctl") {
		t.Errorf("no supervisor detected, so no systemctl advice: \n%s", out)
	}
	// Detection returning nil means "unknown", not "definitely unsupervised",
	// so the verification step has to survive here too.
	if !strings.Contains(out, "gt dolt status") {
		t.Errorf("remedy must keep the verification step even with no supervisor, got:\n%s", out)
	}
}

func TestFilesystemCleanupRemedyDoesNotClaimBlanketSafety(t *testing.T) {
	for _, sup := range []*doltserver.Supervisor{nil, {Kind: "systemd", Unit: "gt-dolt.service", Restart: "always"}} {
		out := filesystemCleanupRemedy("/home/op/gt", sup)

		// The old line — "This is safe — orphan databases have no production
		// data" — read as blanket authorisation to run the rm at any time.
		if strings.Contains(out, "This is safe") {
			t.Errorf("remedy must not claim the deletion is unconditionally safe, got:\n%s", out)
		}
		if !strings.Contains(out, "corrupt") {
			t.Errorf("remedy must state the corruption risk of deleting under a live server, got:\n%s", out)
		}
		if !strings.Contains(out, "/home/op/gt/.dolt-data") {
			t.Errorf("remedy must target the town's own data directory, got:\n%s", out)
		}
		if !strings.Contains(out, orphanCleanupGlobs) {
			t.Errorf("remedy must list the orphan globs, got:\n%s", out)
		}
	}
}

// The globs are the whole basis of the "no production data" claim, so a change
// that widened them to match hq or a rig database would invalidate the text.
func TestOrphanCleanupGlobsExcludeProductionDatabases(t *testing.T) {
	for _, db := range []string{"hq", "gastown", "beads", "wisps"} {
		for _, glob := range strings.Fields(orphanCleanupGlobs) {
			prefix := strings.TrimSuffix(glob, "*")
			if glob != prefix && strings.HasPrefix(db, prefix) {
				t.Errorf("glob %q matches production database %q", glob, db)
			}
		}
	}
}
