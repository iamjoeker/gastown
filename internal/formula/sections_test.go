package formula

import (
	"strings"
	"testing"
)

const shippedSample = `description = """
## Merge Flow

Prose.

## FORBIDDEN Actions

More prose.
"""
formula = "sample"
version = 1

[[steps]]
id = "queue-scan"
title = "Scan merge queue"
description = """
**Step 1: Look at the queue**

Body.
"""

[[steps]]
id = "ledger-reconcile"
title = "Reconcile the ledger"
needs = ["queue-scan"]
description = """
**Step 4: Reconcile the ledger — merged work whose bead or MR never closed**

Body.
"""
`

// overlaySample is shippedSample with the whole ledger-reconcile step removed
// and one heading dropped from the top-level description — the shape of an
// overlay that never received a shipped addition (gt-yubx).
const overlaySample = `description = """
## Merge Flow

Prose, locally reworded.
"""
formula = "sample"
version = 1

[[steps]]
id = "queue-scan"
title = "Scan the merge queue"
description = """
**Step 1: Look at the queue**

Body, locally reworded.
"""
`

func sectionLabels(sections []Section) []string {
	out := make([]string, 0, len(sections))
	for _, s := range sections {
		out = append(out, s.String())
	}
	return out
}

func TestFormulaSections_ExtractsStepsAndHeadings(t *testing.T) {
	got := formulaSections([]byte(shippedSample))

	var steps, headings int
	for _, s := range got {
		switch s.Kind {
		case SectionStep:
			steps++
		case SectionHeading:
			headings++
		}
	}
	if steps != 2 {
		t.Errorf("steps = %d, want 2: %v", steps, sectionLabels(got))
	}
	if headings != 4 {
		t.Errorf("headings = %d, want 4: %v", headings, sectionLabels(got))
	}

	// A step must carry its title, and a heading inside a step must be
	// attributed to that step — the attribution is what keeps a missing step
	// from being reported once per heading it contains.
	for _, s := range got {
		if s.Kind == SectionStep && s.ID == "ledger-reconcile" && s.Label != "Reconcile the ledger" {
			t.Errorf("step label = %q, want %q", s.Label, "Reconcile the ledger")
		}
		if s.Kind == SectionHeading && strings.HasPrefix(s.Label, "Step 4:") && s.StepID != "ledger-reconcile" {
			t.Errorf("heading %q attributed to step %q, want ledger-reconcile", s.Label, s.StepID)
		}
	}
}

// TestMissingSections_NamesTheAbsentStep is the gt-yubx case in miniature: an
// executing copy that never received a shipped step must be told WHICH step,
// not merely that it is behind.
func TestMissingSections_NamesTheAbsentStep(t *testing.T) {
	missing := missingSections([]byte(shippedSample), []byte(overlaySample))

	var namesStep bool
	for _, s := range missing {
		if s.Kind == SectionStep && s.ID == "ledger-reconcile" {
			namesStep = true
		}
	}
	if !namesStep {
		t.Fatalf("missing sections do not name step ledger-reconcile: %v", sectionLabels(missing))
	}

	// The step's own heading must NOT be listed separately: one absent step is
	// one finding.
	for _, s := range missing {
		if s.Kind == SectionHeading && strings.HasPrefix(s.Label, "Step 4:") {
			t.Errorf("heading of an absent step reported separately: %v", sectionLabels(missing))
		}
	}

	// The heading dropped from the top-level description belongs to no step and
	// must still be reported.
	var namesHeading bool
	for _, s := range missing {
		if s.Kind == SectionHeading && s.Label == "FORBIDDEN Actions" {
			namesHeading = true
		}
	}
	if !namesHeading {
		t.Errorf("top-level heading 'FORBIDDEN Actions' not reported: %v", sectionLabels(missing))
	}
}

// TestMissingSections_WordingIsNotAMissingSection guards the other direction.
// overlaySample rewords a step title and body text; neither is a missing
// section, and reporting them would bury the one finding that matters.
func TestMissingSections_WordingIsNotAMissingSection(t *testing.T) {
	missing := missingSections([]byte(shippedSample), []byte(overlaySample))
	for _, s := range missing {
		if s.Kind == SectionStep && s.ID == "queue-scan" {
			t.Errorf("retitled step reported as missing: %v", sectionLabels(missing))
		}
		if s.Kind == SectionHeading && s.Label == "Step 1: Look at the queue" {
			t.Errorf("unchanged heading reported as missing: %v", sectionLabels(missing))
		}
	}
}

// TestMissingSections_EmptyIsNotNil distinguishes "compared, nothing absent"
// from "not comparable". The empty result is a real answer — it bounds a pinned
// formula's risk to wording inside sections it already has — and callers print
// the two cases differently.
func TestMissingSections_EmptyIsNotNil(t *testing.T) {
	reworded := strings.ReplaceAll(shippedSample, "Body.", "Body, reworded locally.")
	missing := missingSections([]byte(shippedSample), []byte(reworded))
	if missing == nil {
		t.Fatal("missingSections returned nil for two comparable texts")
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", sectionLabels(missing))
	}
}

// TestMissingSections_RealCorpus runs the diff against a formula gt actually
// ships, with the Step 4 heading the mayor found absent from the executing
// overlay removed. A synthetic fixture cannot catch a scanner that chokes on
// the real file's 1200 lines of embedded bash, tables and ASCII diagrams.
func TestMissingSections_RealCorpus(t *testing.T) {
	const step4 = "**Step 4: Reconcile the ledger — merged work whose bead or MR never closed**"

	shipped, err := GetEmbeddedFormulaContent("mol-refinery-patrol.formula.toml")
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent: %v", err)
	}
	if !strings.Contains(string(shipped), step4) {
		t.Skipf("shipped mol-refinery-patrol no longer contains the gt-yubx step 4 heading")
	}

	overlay := strings.Replace(string(shipped), step4+"\n", "", 1)
	missing := missingSections(shipped, []byte(overlay))

	var found bool
	for _, s := range missing {
		if s.Kind == SectionHeading && strings.Contains(s.Label, "Reconcile the ledger") {
			found = true
		}
	}
	if !found {
		t.Fatalf("removed step 4 heading not reported; missing = %v", sectionLabels(missing))
	}
	// Removing one line must produce one finding. A scanner that mis-parses the
	// file would report dozens and be useless as a pointer.
	if len(missing) != 1 {
		t.Errorf("missing = %d sections, want 1: %v", len(missing), sectionLabels(missing))
	}

	summary := SummarizeMissingSections(missing, 2)
	if !strings.Contains(summary, "Reconcile the ledger") {
		t.Errorf("summary does not name the section: %q", summary)
	}
}

// TestMissingSections_RealCorpusIdentical is the control for the test above: if
// the same scan reported sections missing from a file compared with itself, the
// finding there would be an artifact of the scanner, not of the removed line.
func TestMissingSections_RealCorpusIdentical(t *testing.T) {
	for _, name := range []string{
		"mol-refinery-patrol.formula.toml",
		"mol-deacon-patrol.formula.toml",
		"mol-witness-patrol.formula.toml",
		"mol-polecat-work.formula.toml",
	} {
		content, err := GetEmbeddedFormulaContent(name)
		if err != nil {
			t.Fatalf("GetEmbeddedFormulaContent(%s): %v", name, err)
		}
		if missing := missingSections(content, content); len(missing) != 0 {
			t.Errorf("%s compared with itself reports %d missing: %v", name, len(missing), sectionLabels(missing))
		}
		if sections := formulaSections(content); len(sections) == 0 {
			t.Errorf("%s: no sections extracted at all", name)
		}
	}
}

// TestFormulaSections_IgnoresFencedCode is a regression test for the first
// real run of this scan: these descriptions are mostly bash, bash comments
// start with the same character as a markdown heading, and one pinned formula
// reported 30 "missing sections" — two thirds of them fragments of wrapped
// shell comments. A list that long is not a pointer, it is the hand diff again.
func TestFormulaSections_IgnoresFencedCode(t *testing.T) {
	const withCode = `description = """
## Real Heading

` + "```bash" + `
# 2. Stale test temp dirs
# No lsof means no evidence. Missing evidence is not permission to delete.
**not a heading either**
` + "```" + `

**Real Bold Heading**
"""
formula = "sample"
version = 1
`
	got := formulaSections([]byte(withCode))
	want := map[string]bool{"Real Heading": true, "Real Bold Heading": true}
	for _, s := range got {
		if !want[s.Label] {
			t.Errorf("extracted %q from inside a code fence", s.Label)
		}
		delete(want, s.Label)
	}
	for label := range want {
		t.Errorf("heading %q outside the fence was not extracted", label)
	}
}

// TestMissingSections_RenumberedHeadingIsPresent is the other regression from
// that first real run: dropping two sweeps renumbers every sweep below them, so
// matching on the number turned one real omission into six phantom ones. The
// number is position, not identity.
func TestMissingSections_RenumberedHeadingIsPresent(t *testing.T) {
	shipped := `description = """
**2. Stale test temp dirs**

**6. Dead dog worktrees**
"""
formula = "sample"
version = 1
`
	executing := `description = """
**4. Dead dog worktrees**
"""
formula = "sample"
version = 1
`
	missing := missingSections([]byte(shipped), []byte(executing))
	if len(missing) != 1 {
		t.Fatalf("missing = %v, want only the absent sweep", sectionLabels(missing))
	}
	if !strings.Contains(missing[0].Label, "Stale test temp dirs") {
		t.Errorf("missing[0] = %q, want the stale-temp-dirs sweep", missing[0].Label)
	}
}

func TestStripLeadingOrdinal(t *testing.T) {
	cases := map[string]string{
		"4. dead dog worktrees":   "dead dog worktrees",
		"0.5 verify branch":       "verify branch",
		"7) report":               "report",
		"step 4: reconcile":       "step 4: reconcile",
		"2026-08-19 was the date": "2026-08-19 was the date",
		"nothing to strip":        "nothing to strip",
		"4.":                      "4.",
		"4. ":                     "4. ",
		"":                        "",
	}
	for in, want := range cases {
		if got := stripLeadingOrdinal(in); got != want {
			t.Errorf("stripLeadingOrdinal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSummarizeMissingSections(t *testing.T) {
	missing := []Section{
		{Kind: SectionStep, ID: "ledger-reconcile", Label: "Reconcile the ledger"},
		{Kind: SectionHeading, Label: "FORBIDDEN Actions"},
		{Kind: SectionHeading, Label: "Third thing"},
	}

	if got := SummarizeMissingSections(nil, 2); got != "" {
		t.Errorf("empty summary = %q, want \"\"", got)
	}

	// A step buried behind two headings must still lead a two-item summary:
	// an absent step will not run at all.
	buried := []Section{
		{Kind: SectionHeading, Label: "FORBIDDEN Actions"},
		{Kind: SectionHeading, Label: "Third thing"},
		{Kind: SectionStep, ID: "ledger-reconcile", Label: "Reconcile the ledger"},
	}
	if got := SummarizeMissingSections(buried, 1); !strings.Contains(got, "ledger-reconcile") {
		t.Errorf("summary led with a heading over a missing step: %q", got)
	}

	got := SummarizeMissingSections(missing, 2)
	for _, want := range []string{"Missing 3 shipped section(s)", "ledger-reconcile", "FORBIDDEN Actions", "+1 more"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "Third thing") {
		t.Errorf("summary exceeded its limit: %q", got)
	}

	long := []Section{{Kind: SectionHeading, Label: strings.Repeat("x", 300)}}
	if line := SummarizeMissingSections(long, 1); len([]rune(line)) > 120 {
		t.Errorf("long label not truncated: %d runes", len([]rune(line)))
	}
}

func TestHeadingText(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"## Merge Flow", "Merge Flow"},
		{"# Title", "Title"},
		{"**Step 4: Reconcile the ledger**", "Step 4: Reconcile the ledger"},
		{"#!/bin/bash", ""},
		{"#123 is a number", ""},
		{"###", ""},
		{"Plain prose.", ""},
		{"A **bold** word and **another** in a sentence.", ""},
		{"**", ""},
		{"****", ""},
		{strings.Repeat("**very long**", 40), ""},
	}
	for _, c := range cases {
		got, ok := headingText(c.line)
		if c.want == "" {
			if ok {
				t.Errorf("headingText(%q) = %q, want not-a-heading", c.line, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("headingText(%q) = %q, %v; want %q, true", c.line, got, ok, c.want)
		}
	}
}

// TestResolvedMissingSections_NilWhenNotComparable checks the Resolved-level
// guards: there is nothing to diff when the executing copy IS the embedded
// default, or when this build ships no default under that name.
func TestResolvedMissingSections_NilWhenNotComparable(t *testing.T) {
	embedded, err := ResolveFormula(knownFormula, t.TempDir(), "")
	if err != nil {
		t.Fatalf("ResolveFormula: %v", err)
	}
	if got := embedded.MissingSections(); got != nil {
		t.Errorf("embedded tier MissingSections = %v, want nil", sectionLabels(got))
	}

	townRoot := t.TempDir()
	writeTownFormula(t, townRoot, "purely-local.formula.toml", shippedSample)
	local, err := ResolveFormula("purely-local", townRoot, "")
	if err != nil {
		t.Fatalf("ResolveFormula(purely-local): %v", err)
	}
	if got := local.MissingSections(); got != nil {
		t.Errorf("local-only MissingSections = %v, want nil", sectionLabels(got))
	}
}

// TestDriftNotice_NamesMissingSections is the prime-time payoff: the warning an
// agent sees must say what is absent, not only that something is. "You are
// behind" sent operators to a hand diff; twice in one night that diff found a
// landed fix sitting inert (gt-yubx).
func TestDriftNotice_NamesMissingSections(t *testing.T) {
	townRoot := t.TempDir()
	shipped, err := GetEmbeddedFormulaContent(knownFormulaFile())
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent: %v", err)
	}
	sections := formulaSections(shipped)
	var victim Section
	for _, s := range sections {
		if s.Kind == SectionStep {
			victim = s
			break
		}
	}
	if victim.ID == "" {
		t.Fatalf("%s ships no steps to remove", knownFormula)
	}

	overlay := removeStepBlock(t, string(shipped), victim.ID)
	writeTownFormula(t, townRoot, knownFormulaFile(), overlay)
	// Pinned: edited locally AND recorded against a third, older hash.
	writeInstalledRecord(t, townRoot, map[string]string{
		knownFormulaFile(): strings.Repeat("0", 64),
	})

	r, err := ResolveFormula(knownFormula, townRoot, "")
	if err != nil {
		t.Fatalf("ResolveFormula: %v", err)
	}
	if r.Drift != DriftPinned {
		t.Fatalf("drift = %q, want pinned", r.Drift)
	}
	notice := r.DriftNotice()
	if !strings.Contains(notice, victim.ID) {
		t.Errorf("DriftNotice does not name the removed step %q:\n%s", victim.ID, notice)
	}
	if !strings.Contains(notice, "gt formula drift "+knownFormula) {
		t.Errorf("DriftNotice does not point at the full list:\n%s", notice)
	}
}

// removeStepBlock deletes the [[steps]] block with the given id from a formula
// text, leaving the rest byte-identical.
func removeStepBlock(t *testing.T, content, id string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start, end := -1, len(lines)
	for i, line := range lines {
		if strings.TrimSpace(line) != "[[steps]]" {
			continue
		}
		if start >= 0 {
			end = i
			break
		}
		if i+1 < len(lines) {
			if got, ok := tomlStringValue(strings.TrimSpace(lines[i+1]), "id"); ok && got == id {
				start = i
			}
		}
	}
	if start < 0 {
		t.Fatalf("step %q not found", id)
	}
	return strings.Join(append(append([]string{}, lines[:start]...), lines[end:]...), "\n")
}
