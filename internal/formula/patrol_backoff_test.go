package formula

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/deacon"
)

// TestPatrolFormulasHaveBackoffLogic verifies that patrol formulas include
// await-signal backoff logic in their loop-or-exit steps.
//
// This is a regression test for a bug where the witness patrol formula's
// await-signal logic was accidentally removed by subsequent commits,
// causing a tight loop when the rig was idle.
//
// See: PR #1052 (original fix), gt-tjm9q (regression report)
// See: gt-0hzeo (refinery stall bug — missing await-signal)
func TestPatrolFormulasHaveBackoffLogic(t *testing.T) {
	// Patrol formulas that must have backoff logic.
	// The loopStepID is the step that contains the await-signal logic;
	// witness/deacon use "loop-or-exit", refinery uses "burn-or-loop".
	type patrolFormula struct {
		name       string
		loopStepID string
		awaitCmd   string // "await-signal" or "await-event"
	}

	patrolFormulas := []patrolFormula{
		{"mol-witness-patrol.formula.toml", "loop-or-exit", "await-signal"},
		{"mol-deacon-patrol.formula.toml", "loop-or-exit", "await-signal"},
		{"mol-refinery-patrol.formula.toml", "burn-or-loop", "await-event"},
	}

	for _, pf := range patrolFormulas {
		t.Run(pf.name, func(t *testing.T) {
			// Read formula content directly from embedded FS
			content, err := formulasFS.ReadFile("formulas/" + pf.name)
			if err != nil {
				t.Fatalf("reading %s: %v", pf.name, err)
			}

			contentStr := string(content)

			// Verify the formula contains the loop/decision step
			doubleQuoted := `id = "` + pf.loopStepID + `"`
			singleQuoted := `id = '` + pf.loopStepID + `'`
			if !strings.Contains(contentStr, doubleQuoted) &&
				!strings.Contains(contentStr, singleQuoted) {
				t.Fatalf("%s: %s step not found", pf.name, pf.loopStepID)
			}

			// Verify the formula contains the required backoff patterns.
			// Witness/deacon use await-signal; refinery uses await-event
			// (file-based event channel system). Both provide backoff logic.
			requiredPatterns := []string{
				pf.awaitCmd,
				"backoff",
				"gt mol step " + pf.awaitCmd,
			}

			for _, pattern := range requiredPatterns {
				if !strings.Contains(contentStr, pattern) {
					t.Errorf("%s missing required pattern %q\n"+
						"The %s step must include %s with backoff logic "+
						"to prevent tight loops when the rig is idle.\n"+
						"See PR #1052 for the original fix.",
						pf.name, pattern, pf.loopStepID, pf.awaitCmd)
				}
			}
		})
	}
}

// TestPatrolFormulasHaveReportCycle verifies that all three patrol formulas
// include `gt patrol report` in their loop step.
//
// The patrol report command atomically closes the current patrol wisp and
// starts a new one, replacing the old squash+new pattern.
//
// Regression test: replaces TestPatrolFormulasHaveSquashCycle (steveyegge/gastown#1371).
func TestPatrolFormulasHaveReportCycle(t *testing.T) {
	type patrolFormula struct {
		name       string
		loopStepID string
	}

	patrolFormulas := []patrolFormula{
		{"mol-witness-patrol.formula.toml", "loop-or-exit"},
		{"mol-deacon-patrol.formula.toml", "loop-or-exit"},
		{"mol-refinery-patrol.formula.toml", "burn-or-loop"},
	}

	for _, pf := range patrolFormulas {
		t.Run(pf.name, func(t *testing.T) {
			content, err := formulasFS.ReadFile("formulas/" + pf.name)
			if err != nil {
				t.Fatalf("reading %s: %v", pf.name, err)
			}

			f, err := Parse(content)
			if err != nil {
				t.Fatalf("parsing %s: %v", pf.name, err)
			}

			var loopDesc string
			for _, step := range f.Steps {
				if step.ID == pf.loopStepID {
					loopDesc = step.Description
					break
				}
			}
			if loopDesc == "" {
				t.Fatalf("%s: %s step not found or has empty description", pf.name, pf.loopStepID)
			}

			// The loop step must use gt patrol report to close current and start next cycle
			if !strings.Contains(loopDesc, "gt patrol report") {
				t.Errorf("%s %s step missing \"gt patrol report\" (close current patrol and start next cycle)\n"+
					"All patrol formulas must use gt patrol report in their loop step.",
					pf.name, pf.loopStepID)
			}
		})
	}
}

// fencedCommands returns the runnable lines inside fenced code blocks of a
// formula step description: non-blank, non-comment lines between ``` markers.
//
// Prose that NAMES a command — including prose forbidding it — is not an
// instruction to run it, so a substring search over the whole description
// cannot distinguish "run this" from "never run this". A suspension notice
// satisfies such a search just as well as the live command it replaced, which
// is how both of this test's predecessors ended up asserting nothing.
func fencedCommands(desc string) []string {
	var cmds []string
	inFence := false
	for _, line := range strings.Split(desc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cmds = append(cmds, trimmed)
	}
	return cmds
}

// TestFencedCommandsSeparatesProseFromCommands pins the distinction that
// TestPatrolFormulasDoNotRunWispGC rests on: a suspension notice naming a
// forbidden command must not read as that command, and a live command must
// still be found.
func TestFencedCommandsSeparatesProseFromCommands(t *testing.T) {
	suspended := "Do not run `bd mol wisp gc --closed --force`.\n" +
		"```bash\n" +
		"# wisp GC SUSPENDED (bd-czf)\n" +
		"```\n"
	if got := fencedCommands(suspended); len(got) != 0 {
		t.Errorf("prose and comments read as commands: %q", got)
	}

	live := "First, clean up CLOSED wisps:\n" +
		"```bash\n" +
		"bd mol wisp gc --closed --force\n" +
		"```\n"
	got := fencedCommands(live)
	if len(got) != 1 || got[0] != "bd mol wisp gc --closed --force" {
		t.Errorf("live command not detected: %q", got)
	}
}

// TestPatrolFormulasDoNotRunWispGC verifies that no patrol formula's inbox-check
// step carries a runnable `bd mol wisp gc` in a fenced command block.
//
// Neither variant is safe. `--age` has no ownership filter and deletes any OPEN
// wisp past the window — other agents' merge requests, and this molecule's own
// open steps, which makes the patrol appear complete early (hq-3pp).
// `--closed --force` purges ALL closed wisps with no age threshold, and a merge
// request CLOSED WITHOUT MERGING is the only record that work was pushed and
// never landed (bd-czf).
//
// This replaces TestPatrolFormulasHaveWispGC (steveyegge/gastown#1712), which
// required the opposite, and the closed-cleanup half of
// TestDeaconPatrolDoesNotRunAgeBasedWispGC.
//
// Regression test for gt-0sq: c0f9e5e2 and d4795e83 suspended wisp GC in the
// refinery and witness embedded defaults but left the deacon's live, so a fresh
// town — which is provisioned from these embedded defaults (ProvisionFormulas)
// — still got a patrol that ran it every cycle.
func TestPatrolFormulasDoNotRunWispGC(t *testing.T) {
	patrolFormulas := []string{
		"mol-witness-patrol.formula.toml",
		"mol-deacon-patrol.formula.toml",
		"mol-refinery-patrol.formula.toml",
	}

	for _, name := range patrolFormulas {
		t.Run(name, func(t *testing.T) {
			content, err := formulasFS.ReadFile("formulas/" + name)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}

			f, err := Parse(content)
			if err != nil {
				t.Fatalf("parsing %s: %v", name, err)
			}

			// Find the inbox-check step (first step in all patrol formulas)
			var inboxDesc string
			for _, step := range f.Steps {
				if step.ID == "inbox-check" {
					inboxDesc = step.Description
					break
				}
			}
			if inboxDesc == "" {
				t.Fatalf("%s: inbox-check step not found or has empty description", name)
			}

			for _, cmd := range fencedCommands(inboxDesc) {
				if strings.Contains(cmd, "bd mol wisp gc") {
					t.Errorf("%s inbox-check step has a runnable wisp GC: %q\n"+
						"Wisp GC is suspended in patrol formulas: --age destroys other\n"+
						"agents' open merge requests and this molecule's own steps (hq-3pp),\n"+
						"and --closed --force destroys closed-but-unmerged MR beads (bd-czf).\n"+
						"Describe the prohibition in prose; leave no command to run.",
						name, cmd)
				}
			}
		})
	}
}

// TestPatrolFormulasUseDynamicBeadResolution verifies that patrol formulas
// resolve their agent bead ID dynamically at runtime via `gt agents resolve`,
// rather than hardcoding a prefix like `gt-<rig>-refinery`.
//
// Hardcoded IDs break when AgentBeadIDWithPrefix collapses the rig component
// (prefix == rig), producing e.g. "cp-refinery" instead of "gt-cp-refinery".
//
// Regression test for hq-9xs.
func TestPatrolFormulasUseDynamicBeadResolution(t *testing.T) {
	patrolFormulas := []string{
		"mol-witness-patrol.formula.toml",
		"mol-refinery-patrol.formula.toml",
	}
	expectedResolver := map[string]string{
		"mol-witness-patrol.formula.toml":  "YOUR_AGENT_BEAD=$(gt agents resolve --role witness --rig {{rig}})",
		"mol-refinery-patrol.formula.toml": "YOUR_AGENT_BEAD=$(gt agents resolve --role refinery --rig {{rig}})",
	}

	for _, name := range patrolFormulas {
		t.Run(name, func(t *testing.T) {
			content, err := formulasFS.ReadFile("formulas/" + name)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}

			f, err := Parse(content)
			if err != nil {
				t.Fatalf("parsing %s: %v", name, err)
			}

			// Find the loop/exit step
			var loopDesc string
			for _, step := range f.Steps {
				if step.ID == "loop-or-exit" || step.ID == "burn-or-loop" {
					loopDesc = step.Description
					break
				}
			}
			if loopDesc == "" {
				t.Fatalf("%s: loop step not found or has empty description", name)
			}

			// Must use dynamic resolution through the agent resolver. The older
			// bd-list query only sees one table in one DB and misses wisp-backed
			// or town-stranded agent beads.
			if !strings.Contains(loopDesc, expectedResolver[name]) {
				t.Errorf("%s loop step missing dynamic agent bead resolution via gt agents resolve.\n"+
					"Agent bead IDs must be resolved at runtime, not hardcoded.\n"+
					"See hq-9xs.",
					name)
			}
			if !strings.Contains(loopDesc, `--agent-bead "$YOUR_AGENT_BEAD"`) {
				t.Errorf("%s loop step must pass the resolved agent bead to await", name)
			}
			if !strings.Contains(loopDesc, `gt agents state "$YOUR_AGENT_BEAD" --set idle=0`) {
				t.Errorf("%s loop step must reset state on the resolved agent bead", name)
			}
			if strings.Contains(loopDesc, "bd list --label=gt:agent") {
				t.Errorf("%s loop step still uses legacy bd-list agent resolution", name)
			}

			// Must NOT hardcode gt-<rig> prefix pattern
			if strings.Contains(loopDesc, "gt-<rig>") {
				t.Errorf("%s loop step hardcodes gt-<rig> prefix.\n"+
					"This breaks when AgentBeadIDWithPrefix collapses the ID (prefix == rig).\n"+
					"See hq-9xs.",
					name)
			}
			if strings.Contains(loopDesc, "{{prefix}}-{{rig}}-witness") || strings.Contains(loopDesc, "{{prefix}}-{{rig}}-refinery") {
				t.Errorf("%s loop step hardcodes prefix/rig agent bead instead of resolved ID", name)
			}
		})
	}
}

// TestDeaconPatrolHasHeartbeatSteps verifies the deacon patrol formula
// includes heartbeat refresh steps to prevent the daemon from killing a
// healthy Deacon mid-cycle.
//
// Without heartbeat refreshes, a patrol cycle that exceeds 20 minutes
// (HeartbeatVeryStaleThreshold = 20m) causes the daemon to consider the Deacon
// stuck and kill it, even though the Deacon is actively executing steps.
func TestDeaconPatrolHasHeartbeatSteps(t *testing.T) {
	content, err := formulasFS.ReadFile("formulas/mol-deacon-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading deacon patrol formula: %v", err)
	}

	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing deacon patrol formula: %v", err)
	}

	// The first step must be the heartbeat step (no dependencies)
	if len(f.Steps) == 0 {
		t.Fatal("deacon patrol formula has no steps")
	}
	if f.Steps[0].ID != "heartbeat" {
		t.Errorf("first step should be \"heartbeat\", got %q", f.Steps[0].ID)
	}
	if !strings.Contains(f.Steps[0].Description, "gt deacon heartbeat") {
		t.Error("heartbeat step must contain \"gt deacon heartbeat\" command")
	}

	// inbox-check must depend on heartbeat
	for _, step := range f.Steps {
		if step.ID == "inbox-check" {
			hasHeartbeatDep := false
			for _, dep := range step.Needs {
				if dep == "heartbeat" {
					hasHeartbeatDep = true
					break
				}
			}
			if !hasHeartbeatDep {
				t.Error("inbox-check step must depend on \"heartbeat\" step")
			}
			break
		}
	}

	// There should be a mid-cycle heartbeat step
	foundMid := false
	foundPreAwait := false
	foundMandatoryHandoff := false
	for _, step := range f.Steps {
		if step.ID == "heartbeat-mid" {
			foundMid = true
			if !strings.Contains(step.Description, "gt deacon heartbeat") {
				t.Error("heartbeat-mid step must contain \"gt deacon heartbeat\" command")
			}
		}
		if step.ID == "loop-or-exit" && strings.Contains(step.Description, "pre-await checkpoint") {
			foundPreAwait = true
			if !strings.Contains(step.Description, "gt deacon heartbeat") {
				t.Error("loop-or-exit step must refresh heartbeat before await-signal")
			}
			if strings.Contains(step.Description, "gt handoff -s") && strings.Contains(step.Description, "mandatory") {
				foundMandatoryHandoff = true
			}
			heartbeatPos := strings.Index(step.Description, "gt deacon heartbeat \"pre-await checkpoint\"")
			awaitPos := strings.Index(step.Description, "gt mol step await-signal")
			if heartbeatPos == -1 || awaitPos == -1 {
				t.Error("loop-or-exit step must contain both pre-await heartbeat and await-signal commands")
			} else if heartbeatPos > awaitPos {
				t.Error("pre-await heartbeat must appear before await-signal to close the stale-heartbeat window")
			}
		}
	}
	if !foundMid {
		t.Error("deacon patrol formula must have a \"heartbeat-mid\" step for mid-cycle refresh")
	}
	if !foundPreAwait {
		t.Error("deacon patrol formula must refresh heartbeat again before await-signal")
	}
	if !foundMandatoryHandoff {
		t.Error("deacon patrol formula must require gt handoff after patrol report")
	}
}

// TestDeaconStaleThresholdCoversTheDesignedPark asserts the cross-artifact
// invariant that gt-cbd is about: the heartbeat staleness thresholds the daemon
// and `gt deacon status` judge against must cover the longest park the deacon
// patrol formula is allowed to take.
//
// The two halves live in different files and nothing connected them. The formula
// parks in await-signal for up to --backoff-max, and the heartbeat only stamps at
// fixed points in the cycle, so heartbeat age ramps from zero to the cycle length
// and resets: it reports position in the loop, not liveness. A threshold shorter
// than the park therefore fires on a Deacon doing exactly what it was told to do.
// Measured against a 5m stale threshold, 29 of 30 consecutive patrol cycles read
// stale, 50.7% of wall-clock, and the daemon nudged a working agent at 6m and 7m.
//
// This test fails from either side — lowering a threshold, or raising the
// formula's backoff — which is what the original defect needed and did not have.
func TestDeaconStaleThresholdCoversTheDesignedPark(t *testing.T) {
	content, err := formulasFS.ReadFile("formulas/mol-deacon-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading deacon patrol formula: %v", err)
	}

	m := regexp.MustCompile(`--backoff-max\s+(\S+)`).FindSubmatch(content)
	if m == nil {
		t.Fatal("deacon patrol formula declares no --backoff-max; the park is unbounded and no threshold can cover it")
	}
	backoffMax, err := time.ParseDuration(string(m[1]))
	if err != nil {
		t.Fatalf("parsing --backoff-max %q: %v", m[1], err)
	}

	if deacon.HeartbeatStaleThreshold < backoffMax {
		t.Errorf("HeartbeatStaleThreshold = %s but the patrol parks for up to %s — "+
			"a healthy Deacon sleeping its full backoff reads stale (gt-cbd)",
			deacon.HeartbeatStaleThreshold, backoffMax)
	}
	if deacon.HeartbeatVeryStaleThreshold <= backoffMax {
		t.Errorf("HeartbeatVeryStaleThreshold = %s but the patrol parks for up to %s — "+
			"the daemon would kill and restart a Deacon that is merely asleep",
			deacon.HeartbeatVeryStaleThreshold, backoffMax)
	}
	if deacon.HeartbeatVeryStaleThreshold <= deacon.HeartbeatStaleThreshold {
		t.Errorf("HeartbeatVeryStaleThreshold (%s) must stay above HeartbeatStaleThreshold (%s), "+
			"or the nudge tier is unreachable and every stale heartbeat goes straight to a restart",
			deacon.HeartbeatVeryStaleThreshold, deacon.HeartbeatStaleThreshold)
	}
}
