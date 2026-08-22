package cmd

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/cli"
)

// noStepsError explains why a molecule root turned up no step children.
//
// gt-ba4h: since gt-pzx, a formula that does not declare `pour = true` — every
// formula in the corpus but beads-release — is instantiated root-only, so its
// steps are never materialized as child wisps and no parent-child edge is ever
// written for them. That is the intended behavior; `gt prime` renders the
// checklist inline from attached_formula and never queries step children.
//
// The tools that DO walk the step tree kept a message written before that was
// possible: "no steps found for X (not a molecule root?)". For a root-only
// molecule every word after the comma is false — X is a molecule root, and the
// reason there are no steps has nothing to do with what X is. That message is
// the diagnostic dead end this bead was filed from. Reading it on a live,
// correctly-formed wisp, the town concluded parent-child edge creation had
// silently broken in two stores and opened a suspected data-loss incident;
// the edges were intact and the roots were fine, and four hours of the wrong
// investigation followed a sentence that said so.
//
// The pre-gt-pzx reading is still the right one for anything that is not a
// molecule root, so that branch keeps the original wording.
func noStepsError(rootID string, root *beads.Issue) error {
	if !isMoleculeRoot(root) {
		return fmt.Errorf("no steps found for %s (not a molecule root?)", rootID)
	}
	return fmt.Errorf("%s is a root-only molecule%s: its steps were never materialized as child wisps, "+
		"so there is no step tree to walk. This is normal — run `%s prime` to read the checklist inline",
		rootID, formulaSuffix(root), cli.Name())
}

// isMoleculeRoot reports whether an issue is a molecule root, the only kind of
// issue for which an empty child listing means "root-only" rather than "wrong
// ID". Molecule roots carry issue_type=molecule whether they were poured or
// spawned root-only, so this does not depend on which spawn path made them.
func isMoleculeRoot(issue *beads.Issue) bool {
	return issue != nil && strings.EqualFold(strings.TrimSpace(issue.Type), "molecule")
}

// formulaSuffix names the formula a molecule root was instantiated from, when
// the title carries it. Both spawners title the root with the formula name, but
// a hand-built molecule need not, so an unrecognizable title yields no suffix
// rather than a guess.
func formulaSuffix(root *beads.Issue) string {
	title := strings.TrimSpace(root.Title)
	if title == "" || !strings.HasPrefix(title, "mol-") || strings.ContainsAny(title, " \t") {
		return ""
	}
	return " (" + title + ")"
}
