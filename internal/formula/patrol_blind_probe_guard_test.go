package formula

import (
	"strings"
	"testing"
)

// Three detection steps in the witness patrol shipped for months printing a
// confident all-clear that carried no evidence either way, because the handle
// each one filtered on had zero rows anywhere in the schema:
//
//	process-cleanups        bd list --label cleanup --status=open
//	check-swarm-completion  bd list --label swarm --status=open
//	check-timer-gates       bd gate check --type=timer --escalate
//
// Two independent defects, either alone sufficient: `bd list` reads the ISSUES
// table and cannot see WISPS at all, and the handles were unwritten. A witness
// reporting "no cleanup wisps, no swarms, no expired gates" every cycle was
// reporting the shape of the schema, not the state of the rig.
//
// These tests fence the repaired steps. They are deliberately structural rather
// than semantic: a blind probe is not detectable by running it — it succeeds,
// returns zero, and looks exactly like good news. The only thing that catches
// the regression is the absence of a control next to the count.
//
// See: gt-kr6, hq-lehg
var blindProbeSteps = []string{
	"process-cleanups",
	"check-swarm-completion",
	"check-timer-gates",
}

// witnessPatrolSteps parses the embedded witness patrol formula and returns its
// steps keyed by ID.
func witnessPatrolSteps(t *testing.T) map[string]Step {
	t.Helper()

	content, err := formulasFS.ReadFile("formulas/mol-witness-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading witness patrol formula: %v", err)
	}
	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing witness patrol formula: %v", err)
	}

	steps := make(map[string]Step, len(f.Steps))
	for _, s := range f.Steps {
		steps[s.ID] = s
	}
	for _, id := range blindProbeSteps {
		if _, ok := steps[id]; !ok {
			t.Fatalf("witness patrol formula: step %q not found", id)
		}
	}
	return steps
}

// shellLines returns the executable lines of every ```bash block in a step
// description — comments and blank lines stripped. Prose and comments may
// quote a retired command to explain why it was retired; only what the witness
// would actually run is fenced.
func shellLines(description string) []string {
	var out []string
	rest := description
	for {
		open := strings.Index(rest, "```bash\n")
		if open < 0 {
			return out
		}
		rest = rest[open+len("```bash\n"):]
		end := strings.Index(rest, "\n```")
		if end < 0 {
			return out
		}
		for _, line := range strings.Split(rest[:end], "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			out = append(out, trimmed)
		}
		rest = rest[end+len("\n```"):]
	}
}

// TestBlindProbeStepsDoNotUseBdList fences the first of the two defects: these
// steps look for WISPS, and `bd list` reads the issues table, so a bd list probe
// here returns zero no matter what the rig is doing.
func TestBlindProbeStepsDoNotUseBdList(t *testing.T) {
	steps := witnessPatrolSteps(t)

	for _, id := range blindProbeSteps {
		t.Run(id, func(t *testing.T) {
			for _, line := range shellLines(steps[id].Description) {
				if strings.Contains(line, "bd list") {
					t.Errorf("step %q runs %q; `bd list` reads the ISSUES table and "+
						"cannot see wisps, so this probe can never return a row. "+
						"Query the wisps table directly. See gt-kr6", id, line)
				}
			}
		})
	}
}

// TestBlindProbeStepsCarryAControl fences the second defect: a bare count is
// uninterpretable. Every one of these steps must print a control alongside it
// and must tell the witness to say NOT MEASURED rather than reporting a zero
// against a populated control as good news.
func TestBlindProbeStepsCarryAControl(t *testing.T) {
	steps := witnessPatrolSteps(t)

	for _, id := range blindProbeSteps {
		t.Run(id, func(t *testing.T) {
			desc := steps[id].Description

			// Collapse whitespace first: the phrase is prose and gets wrapped,
			// and a fence that fails on a line break rather than on a missing
			// instruction is reporting its own formatting, not the defect.
			if !strings.Contains(strings.Join(strings.Fields(desc), " "), "NOT MEASURED") {
				t.Errorf("step %q never tells the witness to report NOT MEASURED; a zero "+
					"from an unpopulated handle would be reported as an all-clear. See gt-kr6", id)
			}

			var controls int
			for _, line := range shellLines(desc) {
				controls += strings.Count(line, "control_")
			}
			if controls == 0 {
				t.Errorf("step %q counts without a control; a zero here is indistinguishable "+
					"from a probe that is not reaching the store. See gt-kr6", id)
			}
		})
	}
}

// TestLabelProbesFilterOnOpenStatus fences the inverse failure. The 'cleanup'
// label acquired a live writer (internal/witness/handlers.go:561), at which
// point an unfiltered label count stopped returning a structural zero and
// started returning a structural NONZERO — 15 rows, every one of them closed —
// sending the step off to "process" wisps that were resolved days ago. The
// repaired probes count OPEN wisps and keep the any-status count as the control
// that separates a measured zero from a blind one.
func TestLabelProbesFilterOnOpenStatus(t *testing.T) {
	steps := witnessPatrolSteps(t)

	for _, id := range []string{"process-cleanups", "check-swarm-completion"} {
		t.Run(id, func(t *testing.T) {
			// The filter must be a join onto wisps. Looking for status='open'
			// anywhere on the line is not enough: the pre-fix probe carried
			// `count(*) from hq.wisps where status='open'` as a CONTROL on the
			// same line while still counting label rows at every status, so a
			// substring check would have passed the exact query it must reject.
			var counted, filtered bool
			for _, line := range shellLines(steps[id].Description) {
				if !strings.Contains(line, "wisp_labels") {
					continue
				}
				counted = true
				if strings.Contains(line, "join hq.wisps") && strings.Contains(line, "status='open'") {
					filtered = true
				}
			}
			if !counted {
				t.Fatalf("step %q no longer queries wisp_labels; if the probe moved, move "+
					"this fence with it. See gt-kr6", id)
			}
			if !filtered {
				t.Errorf("step %q counts label rows at every status; once the label has a "+
					"writer this reports closed wisps as work to do, forever. Join hq.wisps "+
					"and filter status='open'. See gt-kr6", id)
			}
		})
	}
}

// TestBlindProbeFencesCanFail exercises the fences against the text they exist
// to reject. A structural fence that has only ever been run against a passing
// corpus proves nothing — that is how the probes it guards shipped in the first
// place — so the retired forms are kept here as fixtures and each rule is shown
// firing on them.
func TestBlindProbeFencesCanFail(t *testing.T) {
	// The original blind probe: bd list, no control, no status filter.
	original := "```bash\nbd list --label cleanup --status=open\n```\nNo cleanup wisps found."

	// The first repair: a real wisps query with controls, but counting label
	// rows at every status. Reads as clean until the label gains a writer, then
	// reads as permanently busy.
	firstRepair := "```bash\n# probe was `bd list --label cleanup --status=open`\n" +
		`bd sql "select (select count(*) from hq.wisp_labels where label='cleanup') as cleanup_labelled, ` +
		`(select count(*) from hq.wisp_labels) as control_total_label_rows, ` +
		`(select count(*) from hq.wisps where status='open') as control_open_wisps"` +
		"\n```\nReport NOT MEASURED if the controls are populated."

	t.Run("bd list is rejected in a shell block", func(t *testing.T) {
		var found bool
		for _, line := range shellLines(original) {
			if strings.Contains(line, "bd list") {
				found = true
			}
		}
		if !found {
			t.Error("the bd list fence does not fire on the original probe; it cannot fail")
		}
	})

	t.Run("bd list is tolerated in a comment", func(t *testing.T) {
		for _, line := range shellLines(firstRepair) {
			if strings.Contains(line, "bd list") {
				t.Errorf("the bd list fence fires on a commented-out mention (%q); it would "+
					"forbid the step from explaining why the command was retired", line)
			}
		}
	})

	t.Run("a missing control is caught", func(t *testing.T) {
		var controls int
		for _, line := range shellLines(original) {
			controls += strings.Count(line, "control_")
		}
		if controls != 0 {
			t.Error("the control fence does not fire on the uncontrolled probe; it cannot fail")
		}
	})

	t.Run("an unfiltered label count is caught", func(t *testing.T) {
		var counted, filtered bool
		for _, line := range shellLines(firstRepair) {
			if !strings.Contains(line, "wisp_labels") {
				continue
			}
			counted = true
			if strings.Contains(line, "join hq.wisps") && strings.Contains(line, "status='open'") {
				filtered = true
			}
		}
		if !counted {
			t.Fatal("fixture no longer queries wisp_labels")
		}
		if filtered {
			t.Error("the status fence passes the unfiltered probe — it matched the status='open' " +
				"that belongs to the CONTROL subquery, which is the exact false negative this " +
				"fixture exists to catch")
		}
	})

	t.Run("a bare is-not-null await_type test is caught", func(t *testing.T) {
		bare := "```bash\n" + `bd sql "select count(*) from hq.wisps where await_type is not null"` + "\n```"
		for _, line := range shellLines(bare) {
			if !strings.Contains(line, "await_type") {
				continue
			}
			if strings.Contains(line, "await_type<>''") || strings.Contains(line, "await_type != ''") {
				t.Error("the await_type fence does not fire on a bare is-not-null test; it cannot fail")
			}
		}
	})
}

// TestAwaitTypeEmptinessTestIsNotNullCheck fences a trap that has already
// misled two agents into believing this bead was refuted: await_type is the
// empty STRING on every row, never NULL, so `where await_type is not null`
// matches every row and reads as "fully populated". due_at on the same rows IS
// genuinely NULL, so the two fields need different emptiness predicates and a
// single uniform check is wrong for one of them whichever form is picked.
func TestAwaitTypeEmptinessTestIsNotNullCheck(t *testing.T) {
	steps := witnessPatrolSteps(t)

	desc := steps["check-timer-gates"].Description
	var probed bool
	for _, line := range shellLines(desc) {
		if !strings.Contains(line, "await_type") {
			continue
		}
		probed = true
		if !strings.Contains(line, "await_type<>''") && !strings.Contains(line, "await_type != ''") {
			t.Errorf("check-timer-gates probes await_type with %q but never tests it against "+
				"the empty string; `is not null` alone matches every row, because the column "+
				"is '' and not NULL. See gt-kr6", line)
		}
	}
	if !probed {
		t.Error("check-timer-gates no longer measures await_type; the step's whole claim " +
			"rests on that column being unpopulated, so it must show the reader. See gt-kr6")
	}
}
