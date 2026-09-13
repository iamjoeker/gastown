package formula

import (
	"strings"
	"testing"
)

// The deacon patrol formula instructed two commands the `gt` CLI does not
// support (gt-bg1w):
//
//   - context-check ran `gt context --usage`. No `gt context` subcommand
//     exists at all, so the step failed and the Deacon silently fell back to
//     self-assessing instead — the patrol still reported the step as done.
//   - dolt-health's remediation branches dispatched dogs with
//     `gt dog dispatch --formula mol-dog-compactor --var db=<db_name>` and
//     `--formula mol-dog-backup`. `gt dog dispatch` has neither a `--formula`
//     nor a `--var` flag; dogs are dispatched by `--plugin <name>`, and the
//     compactor/backup dogs are named `compactor-dog` and `dolt-backup`.
//
// Because Dolt was healthy every time this ran, the broken dispatch lines
// were never exercised — the step passed on the health check, not because the
// remediation works. These are structural regression fences: the command
// strings must not appear again in either step's text.
//
// See: gt-bg1w
func TestDeadCommandsRemovedFromDeaconPatrol(t *testing.T) {
	steps := deaconPatrolSteps(t)

	contextCheck, ok := steps["context-check"]
	if !ok {
		t.Fatal("deacon patrol: step \"context-check\" not found")
	}
	for _, line := range shellLines(contextCheck.Description) {
		if strings.Contains(line, "gt context") {
			t.Errorf("context-check still offers a runnable `gt context` command, which is "+
				"not a real `gt` subcommand: %q. See gt-bg1w.", line)
		}
	}

	doltHealth, ok := steps["dolt-health"]
	if !ok {
		t.Fatal("deacon patrol: step \"dolt-health\" not found")
	}
	for _, line := range shellLines(doltHealth.Description) {
		if strings.Contains(line, "dog dispatch") && strings.Contains(line, "--formula") {
			t.Errorf("dolt-health dispatches a dog with --formula, which `gt dog dispatch` "+
				"does not support (only --plugin): %q. See gt-bg1w.", line)
		}
		if strings.Contains(line, "dog dispatch") && strings.Contains(line, "--var") {
			t.Errorf("dolt-health dispatches a dog with --var, which `gt dog dispatch` "+
				"does not support: %q. See gt-bg1w.", line)
		}
	}
	for _, want := range []string{"--plugin compactor-dog", "--plugin dolt-backup"} {
		if !strings.Contains(doltHealth.Description, want) {
			t.Errorf("dolt-health is missing %q — the corrected dispatch command. See gt-bg1w.", want)
		}
	}
}
