package templates

import (
	"strings"
	"testing"
)

// A witness patrol convoy sat at 1/10 steps for 19+ hours: the daemon's
// patrol-wake nudge (injected straight into the pane, not mail) was
// acknowledged in prose ("Patrol continues") without the witness ever
// running the next step's command, and the turn ended at an empty prompt
// again. This repeated at least twice. Once explicitly instructed to
// actually run its steps, the witness then abandoned the stalled convoy
// and minted a brand new one from scratch instead of resuming it — a
// human had to manually force-close the orphaned original. See gt-hsad,
// same shape as gt-sbog's refinery fix.
//
// TestRenderRole_Witness_PatrolWakeIsNotSmallTalk pins the witness role
// template to explicitly telling the agent that a wake nudge requires
// running a real command, not a chat reply, and that resuming means
// continuing the existing open patrol wisp rather than starting a fresh
// one via `gt patrol report`.
func TestRenderRole_Witness_PatrolWakeIsNotSmallTalk(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	data := RoleData{
		Role:     "witness",
		TownRoot: "/test/town",
		TownName: "town",
		WorkDir:  "/test/town",
		RigName:  "testrig",
	}

	output, err := tmpl.RenderRole("witness", data)
	if err != nil {
		t.Fatalf("RenderRole() error = %v", err)
	}

	if !strings.Contains(output, "gt-hsad") {
		t.Error("witness role template does not cite gt-hsad, so a future " +
			"editor has no signal this text is load-bearing against a known " +
			"regression")
	}
	if !strings.Contains(strings.ToLower(output), "wake signal") {
		t.Error("witness role template no longer frames a patrol-wake nudge " +
			"as a wake signal requiring action, not a chat reply")
	}
	if !strings.Contains(output, "gt patrol report") {
		t.Error("witness role template does not warn against treating a " +
			"wake as a reason to run `gt patrol report`, which mints a new " +
			"patrol cycle instead of resuming the stalled one")
	}
	if !strings.Contains(output, "RESUME it") {
		t.Error("witness role template does not instruct the agent to " +
			"resume the existing open patrol wisp rather than spawning a " +
			"second one")
	}
}
