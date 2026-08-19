package formula

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Tier names the resolution tier that actually supplied a formula's content.
// See ResolveFormulaContent for the precedence rules.
type Tier string

const (
	// TierRig is <townRoot>/<rig>/.beads/formulas/<name>.formula.toml
	TierRig Tier = "rig"
	// TierTown is <townRoot>/.beads/formulas/<name>.formula.toml
	TierTown Tier = "town"
	// TierEmbedded is the copy compiled into the gt binary.
	TierEmbedded Tier = "embedded"
)

// DriftKind classifies the executing copy of a formula against the default
// shipped in this binary.
//
// The distinction that matters is not "does the disk copy differ" — a
// deliberate local customization differs forever and that is fine — but "is a
// NEWER shipped default being shadowed". Only the latter means a fix that looks
// landed in git is not running. DriftCustomized is the benign case and is
// deliberately excluded from ShadowsNewerDefault.
type DriftKind string

const (
	// DriftNone means the executing copy is the embedded default (or the
	// formula is embedded-only, or nothing is shipped under that name).
	DriftNone DriftKind = ""
	// DriftOutdated means the disk copy is an unmodified older install and a
	// newer default has shipped since. UpdateFormulas will replace it.
	DriftOutdated DriftKind = "outdated"
	// DriftUntracked means the disk copy differs from the shipped default and
	// no install hash was recorded for it, so gt cannot tell a stale copy from
	// a deliberate edit.
	DriftUntracked DriftKind = "untracked"
	// DriftPinned means the disk copy was edited locally AND the shipped
	// default has moved since that edit. UpdateFormulas skips modified files by
	// design, so the town is pinned to the local edit permanently: no rebuild,
	// reinstall, or `gt doctor --fix` will ever deliver the newer default.
	DriftPinned DriftKind = "pinned"
	// DriftCustomized means the disk copy was edited locally but the shipped
	// default has not moved since. Nothing is being missed.
	DriftCustomized DriftKind = "customized"
)

// Resolved describes not just a formula's content but where that content came
// from and how it compares to the default shipped in this binary.
//
// ResolveFormulaContent answers "what will run"; Resolved answers the question
// that has been rediscovered the hard way several times — "is what will run the
// thing we just shipped?" (gt-0wm7).
type Resolved struct {
	// Name is the formula name without the .formula.toml suffix.
	Name string
	// Filename is Name plus the .formula.toml suffix.
	Filename string
	// Tier is the tier the content came from.
	Tier Tier
	// Path is the file the content was read from; empty for TierEmbedded.
	Path string
	// Content is the formula body that will actually be parsed and executed.
	Content []byte

	// Drift classifies Content against the embedded default.
	Drift DriftKind
	// CurrentHash is the sha256 of Content.
	CurrentHash string
	// InstalledHash is the hash recorded for this file in the .installed.json
	// beside it, or "" when there is no record.
	InstalledHash string
	// EmbeddedHash is the sha256 of the embedded default, or "" when this
	// binary ships no formula under this name.
	EmbeddedHash string
}

// ShadowsNewerDefault reports whether the executing copy is hiding a newer
// shipped default — the condition under which a merged fix is not live.
func (r *Resolved) ShadowsNewerDefault() bool {
	if r == nil {
		return false
	}
	switch r.Drift {
	case DriftOutdated, DriftUntracked, DriftPinned:
		return true
	default:
		return false
	}
}

// AutoFixable reports whether `gt doctor --fix` / `gt upgrade` can repair the
// drift on its own. DriftPinned is deliberately not auto-fixable: overwriting
// would discard the local edit.
func (r *Resolved) AutoFixable() bool {
	if r == nil {
		return false
	}
	// UpdateFormulas only manages the town tier.
	if r.Tier != TierTown {
		return false
	}
	return r.Drift == DriftOutdated || r.Drift == DriftUntracked
}

// driftNoticeSectionLimit bounds how many missing sections the prime-time
// notice names before falling back to a count. The notice is printed into
// every agent's prime output, so it stays short; `gt formula drift <name>`
// prints them all.
const driftNoticeSectionLimit = 2

// DriftNotice renders a short operator-facing warning, or "" when the executing
// copy is not shadowing a newer default. It is deliberately terse: it is
// printed into every agent's prime output.
func (r *Resolved) DriftNotice() string {
	if !r.ShadowsNewerDefault() {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "⚠️  FORMULA DRIFT: %s is running from the %s copy\n", r.Name, r.Tier)
	fmt.Fprintf(&b, "    %s\n", r.Path)
	b.WriteString("    which is NOT the default shipped in this gt build. Steps below may be missing\n")
	b.WriteString("    fixes that look landed in git.\n")

	// Name what is absent, not merely that something is. "Behind" is an
	// unbounded risk an operator has to hand-diff to size; "missing step
	// ledger-reconcile" is a finding (gt-yubx).
	if missing := r.MissingSections(); len(missing) > 0 {
		fmt.Fprintf(&b, "    %s\n", SummarizeMissingSections(missing, driftNoticeSectionLimit))
		fmt.Fprintf(&b, "    Full list: gt formula drift %s\n", r.Name)
	}

	switch r.Drift {
	case DriftPinned:
		b.WriteString("    Cause: the file was edited locally AND the shipped default moved afterwards.\n")
		b.WriteString("    gt keeps skipping it — 'gt doctor --fix' cannot repair this one.\n")
		b.WriteString(r.reconcileSteps())
	case DriftOutdated:
		b.WriteString("    Cause: an unmodified older install; a newer default has shipped since.\n")
		b.WriteString("    Fix: gt doctor --fix (or gt upgrade)\n")
	case DriftUntracked:
		b.WriteString("    Cause: no install hash was recorded for this file, so gt cannot tell a stale\n")
		b.WriteString("    copy from a deliberate edit.\n")
		if r.AutoFixable() {
			b.WriteString("    Fix: gt doctor --fix (or gt upgrade)\n")
		} else {
			fmt.Fprintf(&b, "    Nothing updates a %s-tier copy automatically.\n", r.Tier)
			b.WriteString(r.reconcileSteps())
		}
	}
	return b.String()
}

// reconcileSteps renders the hand-merge recipe for a drift kind that no command
// can repair on its own.
//
// It names the flags that actually change something. The bare pointer this
// replaced — "Reconcile: gt formula drift <name>" — was the command that
// PRINTED the notice, so an operator who followed it got the same warning back
// and had nowhere to go (gt-nrzk). Every line below is runnable as written.
func (r *Resolved) reconcileSteps() string {
	shipped := filepath.Join(os.TempDir(), r.Name+".shipped.toml")
	disk := r.Path
	if disk == "" {
		disk = "<disk copy>"
	}

	var b strings.Builder
	b.WriteString("    Reconcile — the merge is by hand; these three commands are the whole recipe:\n")
	fmt.Fprintf(&b, "      1. gt formula drift %s --embedded > %s\n", r.Name, shipped)
	fmt.Fprintf(&b, "      2. diff -u %s %s\n", disk, shipped)
	b.WriteString("         ...and copy the parts you want into the disk copy.\n")
	fmt.Fprintf(&b, "      3. gt formula drift %s --mark-reconciled     # clears this warning\n", r.Name)
	fmt.Fprintf(&b, "    Or discard the local edits entirely: gt formula drift %s --accept-embedded\n", r.Name)
	return b.String()
}

// ResolveFormula resolves a formula the same way ResolveFormulaContent does and
// additionally reports which tier won and how that copy compares to the
// embedded default.
//
// Either townRoot or rigName may be empty; those tiers are skipped. An error is
// returned only when no tier supplies content at all.
func ResolveFormula(name, townRoot, rigName string) (*Resolved, error) {
	bare := strings.TrimSuffix(name, ".formula.toml")
	filename := bare + ".formula.toml"

	r := &Resolved{Name: bare, Filename: filename}

	// The embedded default is the yardstick, whichever tier wins. A missing
	// embedded default is not an error: purely local formulas are legitimate.
	if embedded, err := GetEmbeddedFormulaContent(filename); err == nil {
		r.EmbeddedHash = computeHash(embedded)
	}

	candidates := make([]struct {
		tier Tier
		path string
	}, 0, 2)
	if townRoot != "" && rigName != "" {
		candidates = append(candidates, struct {
			tier Tier
			path string
		}{TierRig, filepath.Join(townRoot, rigName, ".beads", "formulas", filename)})
	}
	if townRoot != "" {
		candidates = append(candidates, struct {
			tier Tier
			path string
		}{TierTown, filepath.Join(townRoot, ".beads", "formulas", filename)})
	}

	for _, c := range candidates {
		content, err := os.ReadFile(c.path) //nolint:gosec // G304: path built from town layout
		if err != nil {
			continue
		}
		r.Tier = c.tier
		r.Path = c.path
		r.Content = content
		r.CurrentHash = computeHash(content)
		r.Drift = classifyDrift(filepath.Dir(c.path), filename, r.CurrentHash, r.EmbeddedHash, &r.InstalledHash)
		return r, nil
	}

	// Tier 3: embedded.
	content, err := GetEmbeddedFormulaContent(filename)
	if err != nil {
		return nil, err
	}
	r.Tier = TierEmbedded
	r.Content = content
	r.CurrentHash = r.EmbeddedHash
	r.Drift = DriftNone
	return r, nil
}

// classifyDrift compares a disk copy against the embedded default, consulting
// the .installed.json that sits beside it. installedOut receives the recorded
// hash (or "") so callers can report it.
func classifyDrift(dir, filename, currentHash, embeddedHash string, installedOut *string) DriftKind {
	if embeddedHash == "" {
		// Nothing shipped under this name — a purely local formula.
		return DriftNone
	}
	if currentHash == embeddedHash {
		return DriftNone
	}

	installedHash := ""
	if record, err := loadInstalledRecord(dir); err == nil {
		installedHash = record.Formulas[filename]
	}
	if installedOut != nil {
		*installedOut = installedHash
	}

	switch {
	case installedHash == "":
		return DriftUntracked
	case installedHash == currentHash:
		// Untouched since install, and the default has moved.
		return DriftOutdated
	case installedHash == embeddedHash:
		// Locally edited, but nothing newer has shipped since.
		return DriftCustomized
	default:
		return DriftPinned
	}
}

// ExecutingDrift resolves every formula this binary ships plus every formula
// present on disk, and returns the ones whose executing copy shadows a newer
// shipped default. Results are sorted by name.
//
// This is the fleet-wide version of the question ResolveFormula answers for one
// formula: which of the steps agents are running are not the steps we shipped?
func ExecutingDrift(townRoot, rigName string) ([]*Resolved, error) {
	names, err := candidateFormulaNames(townRoot, rigName)
	if err != nil {
		return nil, err
	}

	var drifted []*Resolved
	for _, name := range names {
		r, err := ResolveFormula(name, townRoot, rigName)
		if err != nil {
			continue
		}
		if r.ShadowsNewerDefault() {
			drifted = append(drifted, r)
		}
	}
	sort.Slice(drifted, func(i, j int) bool { return drifted[i].Name < drifted[j].Name })
	return drifted, nil
}

// candidateFormulaNames returns the union of embedded formula names and the
// names present in the rig and town formula directories.
func candidateFormulaNames(townRoot, rigName string) ([]string, error) {
	seen := make(map[string]struct{})

	embedded, err := getEmbeddedFormulas()
	if err != nil {
		return nil, err
	}
	for filename := range embedded {
		seen[strings.TrimSuffix(filename, ".formula.toml")] = struct{}{}
	}

	dirs := make([]string, 0, 2)
	if townRoot != "" && rigName != "" {
		dirs = append(dirs, filepath.Join(townRoot, rigName, ".beads", "formulas"))
	}
	if townRoot != "" {
		dirs = append(dirs, filepath.Join(townRoot, ".beads", "formulas"))
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !hasFormulaSuffix(entry.Name()) {
				continue
			}
			seen[strings.TrimSuffix(entry.Name(), ".formula.toml")] = struct{}{}
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// AcceptEmbedded overwrites the resolved disk copy with the embedded default
// and records its hash, clearing the drift. Local edits in that file are lost.
func AcceptEmbedded(r *Resolved) error {
	if r == nil || r.Tier == TierEmbedded {
		return fmt.Errorf("no disk copy to replace")
	}
	content, err := GetEmbeddedFormulaContent(r.Filename)
	if err != nil {
		return err
	}
	if err := os.WriteFile(r.Path, content, 0644); err != nil { //nolint:gosec // G306: formulas are world-readable by design
		return fmt.Errorf("writing %s: %w", r.Path, err)
	}
	return recordInstalledHash(filepath.Dir(r.Path), r.Filename, computeHash(content))
}

// MarkReconciled keeps the disk copy as-is but records the CURRENT embedded
// hash as its install baseline, declaring "this local edit already carries the
// shipped default as of this build".
//
// Without it a reconciled file reads as pinned forever: the recorded hash still
// names the pre-fix default, so DriftPinned never clears even after a human has
// merged the fix in by hand (gt-0wm7, remedy step 2).
func MarkReconciled(r *Resolved) error {
	if r == nil || r.Tier == TierEmbedded {
		return fmt.Errorf("no disk copy to mark")
	}
	if r.EmbeddedHash == "" {
		return fmt.Errorf("this gt build ships no default for %s", r.Name)
	}
	return recordInstalledHash(filepath.Dir(r.Path), r.Filename, r.EmbeddedHash)
}

// recordInstalledHash updates one entry in the .installed.json in dir.
func recordInstalledHash(dir, filename, hash string) error {
	record, err := loadInstalledRecord(dir)
	if err != nil {
		return err
	}
	record.Formulas[filename] = hash
	return saveInstalledRecord(dir, record)
}
