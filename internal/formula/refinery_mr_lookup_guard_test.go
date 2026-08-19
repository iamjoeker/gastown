package formula

import (
	"strings"
	"testing"
)

// The refinery patrol's orphaned-MR sweep spent months asking a question its
// instrument could not answer. `gt mq submit` writes MR beads as EPHEMERAL
// beads — rows in the `wisps` table (GH#2446) — while `bd list` reads the
// `issues` table. The merge-request type filter therefore returns "No issues
// found" unconditionally, which is indistinguishable from a clean queue.
//
// The step now calls `gt mq list <rig> --status open --verify`, which reads
// wisps. These tests fence that in.
//
// The second fence matters more than the first. The prohibition originally
// justified itself with `beads.role=contributor` routing. That premise has
// since gone false — measured 2026-08-19 from gastown's refinery/rig, where
// `git config beads.role` prints `maintainer`:
//
//	bd list, merge-request type filter -> "No issues found"
//	gt mq list gastown --status all    -> 20 MR beads, 9 open/ready
//	control: bd list --status=open     -> rig issues (225 in store)
//
// The control proves routing was already correct and the query still saw
// nothing. So an agent who checks the role, finds `maintainer`, and concludes
// the warning is stale would re-disarm the gate. The rationale must name the
// storage table, not the role.
//
// See: gt-ybz
const refineryPatrolFormula = "formulas/mol-refinery-patrol.formula.toml"

// mrLookupLiterals are the shapes of the blind query. None may appear in the
// formula — not as an instruction, and not quoted inside a warning, because an
// audit grepping the corpus for them must not get a false hit either.
var mrLookupLiterals = []string{
	"--type=merge-request",
	"--type merge-request",
	"-t merge-request",
}

func readRefineryPatrol(t *testing.T) string {
	t.Helper()

	content, err := formulasFS.ReadFile(refineryPatrolFormula)
	if err != nil {
		t.Fatalf("reading refinery patrol formula: %v", err)
	}
	return string(content)
}

// blockquoteAfter returns the run of consecutive ">"-prefixed lines that
// follows marker, which is how the formula publishes its warnings.
func blockquoteAfter(t *testing.T, body, marker string) string {
	t.Helper()

	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("refinery patrol formula: marker %q not found", marker)
	}

	var quoted []string
	started := false
	for _, line := range strings.Split(body[idx:], "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ">") {
			started = true
			quoted = append(quoted, trimmed)
			continue
		}
		if started {
			break
		}
	}
	if len(quoted) == 0 {
		t.Fatalf("refinery patrol formula: no blockquote after %q", marker)
	}
	return strings.Join(quoted, "\n")
}

// TestRefineryPatrolHasNoMergeRequestTypeFilter is the regression fence: the
// blind form must never come back.
func TestRefineryPatrolHasNoMergeRequestTypeFilter(t *testing.T) {
	body := readRefineryPatrol(t)

	for _, literal := range mrLookupLiterals {
		if strings.Contains(body, literal) {
			t.Errorf("refinery patrol formula contains %q; MR beads are wisps and "+
				"bd list reads the issues table, so that form returns "+
				"\"No issues found\" at every beads.role. Use `gt mq list`. See gt-ybz",
				literal)
		}
	}
}

// TestRefineryOrphanSweepUsesMergeQueueReader pins the orphan sweep to the
// reader that can actually see wisps, and to --verify, which is the only part
// that answers the question the step asks ("is the branch gone?").
func TestRefineryOrphanSweepUsesMergeQueueReader(t *testing.T) {
	body := readRefineryPatrol(t)

	const marker = "**Step 3: Check for orphaned MR beads**"
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("refinery patrol formula: %q section not found", marker)
	}

	rest := body[idx:]
	open := strings.Index(rest, "```bash\n")
	if open < 0 {
		t.Fatal("refinery patrol formula: no bash block after the orphaned-MR heading")
	}
	rest = rest[open+len("```bash\n"):]
	closeIdx := strings.Index(rest, "\n```")
	if closeIdx < 0 {
		t.Fatal("refinery patrol formula: unterminated bash block in the orphaned-MR step")
	}
	script := rest[:closeIdx]

	if !strings.Contains(script, "gt mq list") {
		t.Errorf("orphaned-MR sweep does not use `gt mq list`; got:\n%s", script)
	}
	if !strings.Contains(script, "--verify") {
		t.Errorf("orphaned-MR sweep omits --verify, so it never checks whether the "+
			"branch still exists — the whole point of the step; got:\n%s", script)
	}
}

// TestMRLookupProhibitionSurvivesRoleFlip is the fence that gt-ybz is really
// about. A prohibition justified only by `beads.role=contributor` invites an
// agent to check the role, find `maintainer`, and switch back.
func TestMRLookupProhibitionSurvivesRoleFlip(t *testing.T) {
	body := readRefineryPatrol(t)

	for _, tc := range []struct {
		name   string
		marker string
	}{
		{"merge-push step 2", "**Do NOT look up MR beads with a `bd list` merge-request type filter.**"},
		{"orphan sweep step 3", "**Do NOT substitute the `bd list` merge-request type filter here.**"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warning := blockquoteAfter(t, body, tc.marker)

			// The durable cause: which table each reader touches.
			if !strings.Contains(warning, "wisps") {
				t.Error("warning does not name the `wisps` table. The blind query is " +
					"blind because MR beads are wisps and bd list reads issues — " +
					"state that, or the next reader will blame routing. See gt-ybz")
			}
			if !strings.Contains(warning, "issues") {
				t.Error("warning does not name the `issues` table that bd list actually reads")
			}

			// And it must say so explicitly, so checking the role cannot be
			// mistaken for a way out.
			if !strings.Contains(warning, "maintainer") {
				t.Error("warning does not state that the failure persists at " +
					"beads.role=maintainer; without that, an agent who fixes routing " +
					"will re-enable the blind query. See gt-ybz")
			}

			if strings.Contains(warning, "only") && !strings.Contains(warning, "wisps") {
				t.Error("warning appears to narrow the cause to routing alone")
			}
		})
	}
}

// TestOrphanSweepDemandsAPositiveControl checks that the step tells the
// refinery how to read a zero. "No orphans" and "could not see any MR beads"
// produce the same empty output, and only a control distinguishes them.
func TestOrphanSweepDemandsAPositiveControl(t *testing.T) {
	body := readRefineryPatrol(t)

	const marker = "**Step 3: Check for orphaned MR beads**"
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("refinery patrol formula: %q section not found", marker)
	}
	// Bound the search to this step so a stray "NOT MEASURED" elsewhere in the
	// formula cannot satisfy it.
	section := body[idx:]
	if end := strings.Index(section, "\n[[steps]]"); end >= 0 {
		section = section[:end]
	}

	for _, required := range []string{
		"--status all",
		"NOT MEASURED",
	} {
		if !strings.Contains(section, required) {
			t.Errorf("orphaned-MR step is missing %q: a zero from the sweep is only "+
				"reportable as \"no orphans\" if a positive control showed the reader "+
				"can see this rig's MR beads at all. See gt-ybz", required)
		}
	}
}
