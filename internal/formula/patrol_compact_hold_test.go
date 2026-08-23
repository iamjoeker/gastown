package formula

import (
	"strings"
	"testing"
)

// The deacon patrol instructed `gt compact report --dry-run` every cycle. On
// 2026-08-22 that step destroyed 454 wisps: the report command exec'd the
// compaction FIRST and consulted its own --dry-run flag 38 lines later, so the
// flag suppressed the audit bead and the digest mail and nothing else. The run
// was killed mid-pass, and nothing records a wisp deletion, so the casualties
// could not be enumerated at all. Wisps are dolt-ignored — no AS OF, no backup.
//
// Both defects are fixed in internal/cmd (gt-hv3p). The hold on the two patrol
// steps outlives the fix on purpose: it lifts on VERIFICATION against a live
// rig, not on a merged patch, because the thing that failed here was believing
// the command's own account of what it did.
//
// These tests are structural rather than semantic. A destructive step cannot be
// caught by running it — it succeeds and prints a tidy summary. What catches
// the regression is the absence of the command from the step text.
//
// See: gt-hv3p, hq-6y9dp, hq-ap0pq
var compactHeldSteps = []string{"wisp-compact", "compact-report"}

func deaconPatrolSteps(t *testing.T) map[string]Step {
	t.Helper()

	content, err := formulasFS.ReadFile("formulas/mol-deacon-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading deacon patrol formula: %v", err)
	}
	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing deacon patrol formula: %v", err)
	}

	steps := make(map[string]Step, len(f.Steps))
	for _, s := range f.Steps {
		steps[s.ID] = s
	}
	return steps
}

// TestCompactStepsCarryNoRunnableCompactCommand is the hold itself. A held step
// with the command still in a fenced block is not held: an agent working
// through a checklist runs the blocks.
func TestCompactStepsCarryNoRunnableCompactCommand(t *testing.T) {
	steps := deaconPatrolSteps(t)

	for _, id := range compactHeldSteps {
		step, ok := steps[id]
		if !ok {
			t.Fatalf("deacon patrol: step %q not found", id)
		}
		for _, line := range shellLines(step.Description) {
			if strings.Contains(line, "gt compact") {
				t.Errorf("step %q offers a runnable compaction command while the step is "+
					"on hold: %q\nEvery flag form of `gt compact report` deletes, including "+
					"--dry-run and --json (gt-hv3p).", id, line)
			}
		}
	}
}

// TestCompactStepsStateTheHoldAndItsRestoreCondition keeps the hold from
// decaying into an unexplained absence. A step that merely stops saying what to
// do gets "restored" by the next person who notices the digest went quiet.
func TestCompactStepsStateTheHoldAndItsRestoreCondition(t *testing.T) {
	steps := deaconPatrolSteps(t)

	for _, id := range compactHeldSteps {
		step := steps[id]
		if !strings.Contains(step.Description, "gt-hv3p") {
			t.Errorf("step %q does not cite the bead that holds it (gt-hv3p)", id)
		}
		if !strings.Contains(strings.ToUpper(step.Description), "ON HOLD") {
			t.Errorf("step %q does not say it is on hold", id)
		}
	}

	// The restore condition lives on wisp-compact, and compact-report points at
	// it. Without a stated condition a hold is indistinguishable from neglect.
	restore := steps["wisp-compact"].Description
	for _, want := range []string{"Restore condition", "gt reaper archive"} {
		if !strings.Contains(restore, want) {
			t.Errorf("wisp-compact hold does not state %q — the condition for lifting it "+
				"has to be checkable by whoever finds the step", want)
		}
	}
}

// TestCompactStepDropsTheFalseAsOfClaim pins the belief that made a bulk-delete
// path read as routine hygiene. The step used to describe deletion as safe
// because "Dolt AS OF preserves history"; the wisps tables are dolt-ignored, so
// there is no history to read AS OF and no backup to restore from.
func TestCompactStepDropsTheFalseAsOfClaim(t *testing.T) {
	steps := deaconPatrolSteps(t)

	for _, id := range compactHeldSteps {
		desc := steps[id].Description
		if strings.Contains(desc, "AS OF preserves history") {
			t.Errorf("step %q still claims Dolt AS OF preserves deleted wisps. It does "+
				"not: the wisp tables are dolt-ignored (hq-del4).", id)
		}
	}
}
