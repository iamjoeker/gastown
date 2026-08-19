package formula

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// SectionKind distinguishes the two landmarks a formula's text is built from.
type SectionKind string

const (
	// SectionStep is a [[steps]] block, named by its id.
	SectionStep SectionKind = "step"
	// SectionHeading is a markdown heading (## Foo) or a bold heading line
	// (**Step 4: ...**) inside a description string.
	SectionHeading SectionKind = "heading"
)

// Section is one named landmark inside a formula file.
//
// Sections exist for exactly one purpose: to answer "what does the shipped
// default contain that the copy we are executing does not?" for a formula no
// command can auto-repair. A pinned formula is otherwise an unbounded risk —
// you know it is behind, but not by what — and the only way to find out was to
// diff both texts by hand. Twice in one night that hand diff found a landed fix
// sitting inert: a fail-open `rm -rf` guard in mol-deacon-patrol, and the whole
// of Step 4 (ledger reconciliation) in mol-refinery-patrol (gt-yubx).
type Section struct {
	// Kind is SectionStep or SectionHeading.
	Kind SectionKind `json:"kind"`
	// ID is the step id, for SectionStep only.
	ID string `json:"id,omitempty"`
	// Label is the step title or the heading text.
	Label string `json:"label"`
	// StepID names the [[steps]] block a heading was found inside, or "" for
	// headings in the top-level description. Used to suppress the headings of a
	// step that is itself missing — the step line already covers them.
	StepID string `json:"step_id,omitempty"`

	// key is the normalized identity used for present/absent comparison.
	key string
	// altKey is key with any leading ordinal removed, so a section still
	// counts as present when the copy numbers it differently. Dropping two
	// sweeps renumbers every sweep below them, and without this the cascade
	// reported six present sections as missing on the first real run.
	altKey string
}

// String renders a section for operator output.
//
// Labels are quoted by hand rather than with %q: these headings quote things
// themselves ('any "N found" claim'), and backslash-escaping every inner quote
// made the one line this feature exists to print unreadable.
func (s Section) String() string {
	switch s.Kind {
	case SectionStep:
		if s.Label != "" {
			return "step " + s.ID + " \"" + s.Label + "\""
		}
		return "step " + s.ID
	default:
		return "heading \"" + s.Label + "\""
	}
}

// maxSectionLabel bounds a label before it goes into operator output. Heading
// text in these formulas runs to whole sentences.
const maxSectionLabel = 72

// Short renders a section with its label truncated for a one-line summary.
func (s Section) Short() string {
	t := s
	t.Label = truncateLabel(t.Label, maxSectionLabel)
	return t.String()
}

func truncateLabel(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}

// MissingSections returns the sections present in the default shipped in this
// build and absent from the copy that will actually execute, in the order the
// default declares them.
//
// It returns nil when there is nothing to compare (the executing copy IS the
// embedded default, or this build ships no default under that name). An empty,
// non-nil result is meaningful and different: both texts were compared and
// every shipped section is present, so whatever the copy is behind by lives
// inside sections it already has.
func (r *Resolved) MissingSections() []Section {
	if r == nil || r.Tier == TierEmbedded || r.EmbeddedHash == "" {
		return nil
	}
	if r.CurrentHash == r.EmbeddedHash {
		return nil
	}
	embedded, err := GetEmbeddedFormulaContent(r.Filename)
	if err != nil {
		return nil
	}
	return missingSections(embedded, r.Content)
}

// missingSections diffs two formula texts by section.
func missingSections(shipped, executing []byte) []Section {
	have := make(map[string]struct{})
	for _, s := range formulaSections(executing) {
		have[s.key] = struct{}{}
		if s.altKey != "" {
			have[s.altKey] = struct{}{}
		}
	}
	present := func(s Section) bool {
		if _, ok := have[s.key]; ok {
			return true
		}
		if s.altKey == "" {
			return false
		}
		_, ok := have[s.altKey]
		return ok
	}

	missingSteps := make(map[string]struct{})
	out := []Section{}
	for _, s := range formulaSections(shipped) {
		if present(s) {
			continue
		}
		if s.Kind == SectionStep {
			missingSteps[s.ID] = struct{}{}
			out = append(out, s)
			continue
		}
		// A heading inside a step the copy does not have at all is not a
		// separate finding — reporting both turns one missing step into a
		// dozen lines of noise.
		if s.StepID != "" {
			if _, gone := missingSteps[s.StepID]; gone {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// formulaSections extracts the ordered, deduplicated landmarks of a formula
// file.
//
// This is a line scan over the raw text, not a TOML parse, deliberately: the
// interesting content — headings like "**Step 4: Reconcile the ledger**" — is
// markdown living inside TOML description strings, so a parse buys nothing and
// costs the ability to read a file that does not parse. Overlays are hand-
// edited; a hand-edited file that no longer parses is exactly when an operator
// most needs to know what it is missing.
func formulaSections(content []byte) []Section {
	var (
		sections    []Section
		seen        = make(map[string]struct{})
		table       string // last TOML table header seen
		stepID      string // id of the [[steps]] block currently open
		pendingID   bool   // inside [[steps]], still looking for its id
		pendingName string // title seen before the id it belongs to
		inFence     bool   // inside a ``` block in a description
	)

	// setStepTitle attaches a title to an already-recorded step entry.
	setStepTitle := func(id, title string) {
		for i := range sections {
			if sections[i].Kind == SectionStep && sections[i].ID == id && sections[i].Label == "" {
				sections[i].Label = title
				return
			}
		}
	}

	add := func(s Section) {
		if _, dup := seen[s.key]; dup {
			return
		}
		seen[s.key] = struct{}{}
		sections = append(sections, s)
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// These descriptions are mostly bash, and bash comments start with the
		// same character as a markdown heading. Scanning inside a fence turned
		// one drifted formula's report into 30 "missing sections", two thirds
		// of them fragments of wrapped shell comments — a list that long is not
		// a pointer, it is the hand diff again. TOML table detection stays live
		// inside fences: an unbalanced fence must never cost us a missing STEP,
		// the strongest signal here.
		//
		// What this gives up is naming a guard that lives ONLY as comments
		// inside a block — the deacon's fail-open `lsof` guard is one. It is
		// not lost: that guard sits under the '2. Stale test temp dirs'
		// heading, which is itself reported absent, so the enclosing section
		// carries the signal. A heading whose body changed while the heading
		// stayed is the one case this scan cannot see, and the output says so.
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}

		if header, ok := tomlTableHeader(trimmed); ok {
			table = header
			stepID, pendingName = "", ""
			pendingID = header == "steps"
			continue
		}

		if table == "steps" {
			if pendingID {
				if id, ok := tomlStringValue(trimmed, "id"); ok {
					stepID, pendingID = id, false
					add(Section{Kind: SectionStep, ID: id, Label: pendingName, key: "step:" + strings.ToLower(id)})
					pendingName = ""
					continue
				}
			}
			// A step's title is not its identity — renaming a title is a
			// wording change, not a missing section — so it decorates the
			// step's entry rather than becoming one.
			if title, ok := tomlStringValue(trimmed, "title"); ok {
				if stepID == "" {
					pendingName = title
				} else {
					setStepTitle(stepID, title)
				}
				continue
			}
		}

		if inFence {
			continue
		}

		if text, ok := headingText(trimmed); ok {
			norm := normalizeHeading(text)
			s := Section{
				Kind:   SectionHeading,
				Label:  text,
				StepID: stepID,
				key:    "heading:" + norm,
			}
			if bare := stripLeadingOrdinal(norm); bare != norm && bare != "" {
				s.altKey = "heading:" + bare
			}
			add(s)
		}
	}
	return sections
}

// tomlTableHeader returns the table name for a [table] or [[array]] line.
func tomlTableHeader(line string) (string, bool) {
	if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		return "", false
	}
	name := strings.Trim(line, "[]")
	if name == "" || strings.ContainsAny(name, " \t\"'") {
		return "", false
	}
	return name, true
}

// tomlStringValue returns the value of a `key = "value"` line.
func tomlStringValue(line, key string) (string, bool) {
	rest, ok := strings.CutPrefix(line, key)
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	rest, ok = strings.CutPrefix(rest, "=")
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if len(rest) < 2 || !strings.HasPrefix(rest, `"`) || !strings.HasSuffix(rest, `"`) {
		return "", false
	}
	return rest[1 : len(rest)-1], true
}

// maxHeadingLen bounds what counts as a heading line. Some of these formulas
// write a whole sentence in bold; a paragraph is not a heading.
const maxHeadingLen = 200

// headingText recognizes both heading forms these formulas use: a markdown
// ATX heading, and a line that is entirely bold — which is how every "Step N:"
// sub-heading inside a step description is written.
func headingText(line string) (string, bool) {
	if len(line) > maxHeadingLen {
		return "", false
	}
	if h, ok := strings.CutPrefix(line, "#"); ok {
		h = strings.TrimLeft(h, "#")
		if !strings.HasPrefix(h, " ") && !strings.HasPrefix(h, "\t") {
			return "", false // "#!/bin/sh", "#123"
		}
		h = strings.TrimSpace(h)
		if h == "" {
			return "", false
		}
		return h, true
	}
	if strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**") && len(line) > 4 {
		inner := strings.TrimSuffix(strings.TrimPrefix(line, "**"), "**")
		// One bold run for the whole line, not two bold words in a sentence.
		if strings.Contains(inner, "**") || strings.TrimSpace(inner) == "" {
			return "", false
		}
		return strings.TrimSpace(inner), true
	}
	return "", false
}

// normalizeHeading reduces a heading to the identity used for comparison:
// case, surrounding emphasis and trailing punctuation are wording, not
// content.
func normalizeHeading(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '*', '`', '#', '_':
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(s, " :.-—")
}

// stripLeadingOrdinal removes a "4. ", "0.5 " or "7) " prefix from a
// normalized heading, returning s unchanged when there is none.
//
// The number is position, not identity. It is also the least stable part of a
// heading: an overlay missing two sweeps renumbers every sweep after them, and
// matching on the number turns one real omission into a page of phantom ones.
func stripLeadingOrdinal(s string) string {
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	if i == 0 || i == len(s) {
		return s
	}
	if s[i] == ')' || s[i] == ':' {
		i++
	}
	if i >= len(s) || (s[i] != ' ' && s[i] != '\t') {
		return s
	}
	rest := strings.TrimSpace(s[i:])
	if rest == "" {
		return s
	}
	return rest
}

// SummarizeMissingSections renders the one-line form used where space is tight
// — prime output, the drift list and doctor. It names up to limit sections,
// steps first, and counts the rest. It returns "" when nothing is missing.
//
// The line the mayor asked this feature for is the difference between a
// warning that says "you are behind" and one that says "you are missing the
// step that prevents re-dispatch" (gt-yubx).
func SummarizeMissingSections(missing []Section, limit int) string {
	if len(missing) == 0 {
		return ""
	}
	if limit <= 0 || limit > len(missing) {
		limit = len(missing)
	}
	// Steps first when the list is truncated. An absent [[steps]] block is a
	// step that will not run at all; an absent heading may be a paragraph of
	// hardening inside a step that does. Both are reported in full elsewhere,
	// but only one of them can lead a one-line summary.
	ordered := make([]Section, 0, len(missing))
	for _, s := range missing {
		if s.Kind == SectionStep {
			ordered = append(ordered, s)
		}
	}
	for _, s := range missing {
		if s.Kind != SectionStep {
			ordered = append(ordered, s)
		}
	}

	parts := make([]string, 0, limit)
	for _, s := range ordered[:limit] {
		parts = append(parts, s.Short())
	}
	line := fmt.Sprintf("Missing %d shipped section(s): %s", len(missing), strings.Join(parts, "; "))
	if rest := len(missing) - limit; rest > 0 {
		line += fmt.Sprintf("; +%d more", rest)
	}
	return line
}
