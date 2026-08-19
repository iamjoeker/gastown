package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// `gt dolt migrate`'s stale-manifest remedy is instruction text an operator
// executes by hand, and step 2 of it (`dolt fsck --repair`) writes to a
// database directory. These tests pin the same two properties gt-xvwu pinned
// for the cleanup remedy (gt-4ruo): the stop step must stop whatever restarts
// the server, and the write must be gated on a check that it is actually down.

func TestStaleManifestRemedyUnderSupervisor(t *testing.T) {
	sup := &doltserver.Supervisor{
		Kind:     "systemd",
		Unit:     "gt-dolt.service",
		UserUnit: true,
		Restart:  "always",
	}
	out := staleManifestRemedy("/home/op/gt/.dolt-data", []string{"gastown"}, sup)

	if !strings.Contains(out, "systemctl --user stop gt-dolt.service") {
		t.Errorf("remedy must stop the unit, got:\n%s", out)
	}
	if !strings.Contains(out, "systemctl --user start gt-dolt.service") {
		t.Errorf("remedy must start the unit, got:\n%s", out)
	}
	// "`gt dolt stop` would only signal the process" is allowed — it is the
	// explanation. What must not appear is `gt dolt stop` as the step to run.
	if strings.Contains(out, "Stop the server:   gt dolt stop") {
		t.Errorf("remedy must not tell a supervised operator to `gt dolt stop`, got:\n%s", out)
	}
	if !strings.Contains(out, "Restart=always") {
		t.Errorf("remedy must name the restart policy that makes `gt dolt stop` ineffective, got:\n%s", out)
	}

	stopIdx := strings.Index(out, "systemctl --user stop")
	verifyIdx := strings.Index(out, "gt dolt status")
	repairIdx := strings.Index(out, "dolt fsck --repair")
	startIdx := strings.Index(out, "systemctl --user start")
	if !(stopIdx < verifyIdx && verifyIdx < repairIdx && repairIdx < startIdx) {
		t.Errorf("steps must run stop -> verify -> repair -> start, got:\n%s", out)
	}
}

func TestStaleManifestRemedyWithoutSupervisor(t *testing.T) {
	out := staleManifestRemedy("/home/op/gt/.dolt-data", []string{"gastown"}, nil)

	if !strings.Contains(out, "gt dolt stop") || !strings.Contains(out, "gt dolt start") {
		t.Errorf("unsupervised remedy should use gt's own stop/start, got:\n%s", out)
	}
	if strings.Contains(out, "systemctl") {
		t.Errorf("no supervisor detected, so no systemctl advice:\n%s", out)
	}
	// Detection returning nil means "unknown", not "definitely unsupervised",
	// so the verification step has to survive here too.
	if !strings.Contains(out, "gt dolt status") {
		t.Errorf("remedy must keep the verification step even with no supervisor, got:\n%s", out)
	}
}

func TestStaleManifestRemedyStatesTheWriteRisk(t *testing.T) {
	for _, sup := range []*doltserver.Supervisor{nil, {Kind: "systemd", Unit: "gt-dolt.service", Restart: "always"}} {
		out := staleManifestRemedy("/home/op/gt/.dolt-data", []string{"gastown"}, sup)

		// The whole reason the ordering matters: `fsck --repair` is not a
		// read-only inspection. An operator who thinks it is has no reason to
		// care whether the server came back.
		if !strings.Contains(out, "writes to the database directory") {
			t.Errorf("remedy must say that fsck --repair writes, got:\n%s", out)
		}
		if !strings.Contains(out, "corrupt") {
			t.Errorf("remedy must state the corruption risk of repairing under a live server, got:\n%s", out)
		}
	}
}

// The old text told the operator to `cd "$GT_ROOT"/.dolt-data/<db>` — two
// placeholders to fill in by hand, immediately before a command that writes to
// whatever directory they land in.
func TestStaleManifestRemedyNamesEachMissingDatabase(t *testing.T) {
	out := staleManifestRemedy("/home/op/gt/.dolt-data", []string{"gastown", "beads"}, nil)

	for _, want := range []string{
		"cd /home/op/gt/.dolt-data/gastown && dolt fsck --repair",
		"cd /home/op/gt/.dolt-data/beads && dolt fsck --repair",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("remedy must spell out %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<db>") || strings.Contains(out, "$GT_ROOT") {
		t.Errorf("remedy must not leave paths for the operator to compose, got:\n%s", out)
	}
}
