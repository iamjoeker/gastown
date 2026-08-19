package cmd

import (
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/formula"
	"github.com/steveyegge/gastown/internal/style"
)

// stampMoleculeWispType writes wisp_type onto a freshly spawned molecule wisp
// and, when includeChildren is set, onto its step wisps too (gt-fqd5).
//
// Why an UPDATE and not a create flag: bd's only wisp_type write path is
// `bd create --wisp-type`. The molecule spawners — `bd mol wisp <formula>`,
// `bd mol wisp create <proto>`, `bd mol bond` — accept no such flag, and
// `bd update` has none either, so a molecule wisp has no route to the column
// except this one. Molecule wisps and their steps were the overwhelming
// majority of the untyped rows gt-fqd5 measured.
//
// The value comes from the formula's [vars.wisp_type], resolved through the
// same rig > town > embedded precedence the spawn itself used. A formula that
// declares nothing leaves its wisps unclassified, which is deliberate: bd's
// vocabulary is a closed set of seven TTL buckets, and work molecules like
// mol-polecat-work genuinely have no member in it. Guessing one would assign a
// TTL to work whose retention nobody has reasoned about.
//
// Every failure is non-fatal and reported. An unclassified wisp is the
// pre-gt-fqd5 status quo, so it is not worth failing a sling or a patrol cycle
// over — but it is worth saying out loud, because silence at exactly this point
// is how the column stayed empty in the first place.
func stampMoleculeWispType(formulaName, townRoot, rigName, rootID string, includeChildren bool, configure func(*bdCmd) *bdCmd) {
	if rootID == "" {
		return
	}

	wispType := formula.DeclaredWispType(formulaName, townRoot, rigName)
	if wispType == "" {
		return
	}

	ids := []string{rootID}
	if includeChildren {
		ids = append(ids, moleculeChildWispIDs(rootID, configure)...)
	}

	stmt, err := beads.WispTypeUpdateSQL(wispType, ids)
	if err != nil {
		style.PrintWarning("formula %s declares an unusable wisp_type: %v", formulaName, err)
		return
	}
	if err := configure(BdCmd("sql", stmt)).Run(); err != nil {
		style.PrintWarning("could not set wisp_type=%s on %s (%d wisps): %v", wispType, rootID, len(ids), err)
	}
}

// moleculeChildWispIDs lists the step wisps under a molecule root. Returns nil
// on any failure — the root still gets stamped, and a partially classified
// molecule beats an unclassified one.
func moleculeChildWispIDs(rootID string, configure func(*bdCmd) *bdCmd) []string {
	out, err := configure(BdCmd("show", rootID, "--children", "--json")).Output()
	if err != nil {
		style.PrintWarning("could not list steps of %s for wisp_type: %v", rootID, err)
		return nil
	}

	// bd wraps the children in an object keyed by the PARENT ID, alongside a
	// schema_version key — {"hq-wisp-abc": [...], "schema_version": 1} — not a
	// bare array and not a "children" key. Decoding either of those guesses
	// succeeds and yields nothing, so the caller would report zero steps for a
	// seven-step molecule and stamp only the root. beads.ParseChildrenJSON is
	// the one implementation that knows the real envelope.
	children, err := beads.ParseChildrenJSON(string(out))
	if err != nil {
		style.PrintWarning("could not parse steps of %s for wisp_type: %v", rootID, err)
		return nil
	}

	ids := make([]string, 0, len(children))
	for _, c := range children {
		if c.ID != "" {
			ids = append(ids, c.ID)
		}
	}
	return ids
}
