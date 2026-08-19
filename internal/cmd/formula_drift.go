package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/formula"
	"github.com/steveyegge/gastown/internal/style"
)

var (
	formulaDriftJSON           bool
	formulaDriftRig            string
	formulaDriftEmbedded       bool
	formulaDriftAcceptEmbedded bool
	formulaDriftMarkReconciled bool
)

var formulaDriftCmd = &cobra.Command{
	Use:   "drift [name]",
	Short: "Show formulas whose executing copy shadows a newer shipped default",
	Long: `Show formulas whose executing copy is NOT the default shipped in this gt build.

'gt prime' resolves formula steps rig > town > embedded, so a disk copy shadows
the embedded one. That is intended for customization, but it also means a fix
merged into the embedded corpus never reaches a town that has a disk copy — and
nothing used to say so, which is how a P1 fix once sat inert for two days
looking done (gt-0wm7).

Drift kinds:
  outdated   Unmodified older install; a newer default has shipped.
             'gt doctor --fix' will update it.
  untracked  No install hash recorded, so gt cannot tell a stale copy from a
             deliberate edit.
  pinned     The file was edited locally AND the shipped default moved
             afterwards. gt skips modified files by design, so this one is
             pinned forever: no rebuild, reinstall, or 'gt doctor --fix'
             delivers the newer default. It needs a human merge.

A local edit whose shipped default has NOT moved since is not drift and is not
listed — nothing is being missed there.

What a pinned copy is missing:

Being "behind" is not a finding — it is an unknown. So drift also names the
sections (steps, and headings inside step descriptions) that the shipped
default has and the executing copy does not. A missing '**Step 4: Reconcile
the ledger**' is a step that is not running; that line is what a hand diff was
being run to find (gt-yubx). A renamed heading also reads as missing, so treat
the list as where to look, not as proof.

Reconciling a pinned formula:

  # 1. See what the shipped default says
  gt formula drift mol-witness-patrol --embedded > /tmp/shipped.toml
  diff -u <disk copy> /tmp/shipped.toml

  # 2. Merge the parts you want into the disk copy by hand, then declare it
  #    reconciled so it stops reporting as pinned:
  gt formula drift mol-witness-patrol --mark-reconciled

  # ...or give up the local edits entirely:
  gt formula drift mol-witness-patrol --accept-embedded

--mark-reconciled records the current embedded hash as the file's install
baseline without touching its contents. Without it, a formula a human has
already merged by hand keeps reporting as pinned forever, because the recorded
hash still names the pre-fix default.

Examples:
  gt formula drift                                 # list drifted formulas
  gt formula drift --json                          # machine-readable
  gt formula drift mol-witness-patrol              # one formula, with detail
  gt formula drift mol-witness-patrol --embedded   # print the shipped default`,
	Args: cobra.MaximumNArgs(1),
	RunE: runFormulaDrift,
}

func init() {
	formulaDriftCmd.Flags().BoolVar(&formulaDriftJSON, "json", false, "Output as JSON")
	formulaDriftCmd.Flags().StringVar(&formulaDriftRig, "rig", "", "Rig whose tier-1 formulas to consider (default: inferred from cwd)")
	formulaDriftCmd.Flags().BoolVar(&formulaDriftEmbedded, "embedded", false, "Print the embedded default for <name> to stdout (for diffing)")
	formulaDriftCmd.Flags().BoolVar(&formulaDriftAcceptEmbedded, "accept-embedded", false, "Overwrite the disk copy of <name> with the embedded default (DISCARDS local edits)")
	formulaDriftCmd.Flags().BoolVar(&formulaDriftMarkReconciled, "mark-reconciled", false, "Keep the disk copy of <name> but record the current embedded hash as its baseline")

	formulaCmd.AddCommand(formulaDriftCmd)
}

// driftListSectionLimit bounds how many missing sections the fleet list names
// per formula. One line per formula keeps the list scannable; the detail view
// prints them all.
const driftListSectionLimit = 2

// formulaDriftJSONEntry is the machine-readable shape of one drifted formula.
type formulaDriftJSONEntry struct {
	Name          string `json:"name"`
	Tier          string `json:"tier"`
	Path          string `json:"path"`
	Drift         string `json:"drift"`
	AutoFixable   bool   `json:"auto_fixable"`
	CurrentHash   string `json:"current_hash"`
	InstalledHash string `json:"installed_hash"`
	EmbeddedHash  string `json:"embedded_hash"`
	// MissingSections is null when the two texts were not comparable (the
	// executing copy IS the embedded default, or this build ships no default
	// under that name) and [] when they were compared and nothing is absent.
	MissingSections []formula.Section `json:"missing_sections"`
}

func runFormulaDrift(cmd *cobra.Command, args []string) error {
	townRoot, rigName, err := resolveOverlayContext(formulaDriftRig)
	if err != nil {
		return err
	}

	needsName := formulaDriftEmbedded || formulaDriftAcceptEmbedded || formulaDriftMarkReconciled
	if needsName && len(args) == 0 {
		return fmt.Errorf("--embedded, --accept-embedded and --mark-reconciled need a formula name")
	}
	if formulaDriftAcceptEmbedded && formulaDriftMarkReconciled {
		return fmt.Errorf("--accept-embedded and --mark-reconciled are mutually exclusive")
	}

	if len(args) == 1 {
		return formulaDriftOne(args[0], townRoot, rigName)
	}
	return formulaDriftAll(townRoot, rigName)
}

func formulaDriftOne(name, townRoot, rigName string) error {
	resolved, err := formula.ResolveFormula(name, townRoot, rigName)
	if err != nil {
		return err
	}

	if formulaDriftEmbedded {
		content, err := formula.GetEmbeddedFormulaContent(resolved.Filename)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(content)
		return err
	}

	if formulaDriftAcceptEmbedded {
		if err := formula.AcceptEmbedded(resolved); err != nil {
			return err
		}
		fmt.Printf("%s %s replaced with the embedded default (%s)\n", style.SuccessPrefix, resolved.Name, resolved.Path)
		return nil
	}

	if formulaDriftMarkReconciled {
		if err := formula.MarkReconciled(resolved); err != nil {
			return err
		}
		fmt.Printf("%s %s marked reconciled against this build's default; contents untouched\n", style.SuccessPrefix, resolved.Name)
		fmt.Printf("  %s\n", style.Dim.Render(resolved.Path))
		return nil
	}

	if formulaDriftJSON {
		return formulaDriftPrintJSON([]*formula.Resolved{resolved})
	}

	fmt.Printf("%s\n", style.Bold.Render(resolved.Name))
	fmt.Printf("  executing tier: %s\n", resolved.Tier)
	if resolved.Path != "" {
		fmt.Printf("  path:           %s\n", resolved.Path)
	}
	if !resolved.ShadowsNewerDefault() {
		reason := "matches the default shipped in this build"
		prefix := style.SuccessPrefix
		switch resolved.Drift {
		case formula.DriftCustomized:
			reason = "locally customized, but no newer default has shipped since — nothing is being missed"
			// A hand merge declared complete with --mark-reconciled lands
			// here, and an INCOMPLETE one lands here too: the hash baseline
			// says "reconciled" because a human said so, not because the
			// sections arrived. Compare the texts before repeating the claim.
			if len(resolved.MissingSections()) > 0 {
				reason = "locally customized, and no newer default has shipped since — but sections of the current default are absent:"
				prefix = style.WarningPrefix
			}
		case formula.DriftNone:
			if resolved.EmbeddedHash == "" {
				reason = "this build ships no default under that name (purely local formula)"
			}
		}
		fmt.Printf("  %s %s\n", prefix, reason)
		formulaDriftPrintMissing(resolved)
		return nil
	}

	fmt.Printf("  drift:          %s\n", resolved.Drift)
	fmt.Println()
	fmt.Print(resolved.DriftNotice())
	formulaDriftPrintMissing(resolved)
	return nil
}

// formulaDriftPrintMissing lists, in full, the sections the shipped default has
// and the executing copy does not.
//
// The empty case is printed too, and is not filler: "compared both texts, every
// shipped section is present" bounds a pinned formula's risk to wording inside
// sections it already has. Silence there would be indistinguishable from not
// having looked.
func formulaDriftPrintMissing(r *formula.Resolved) {
	missing := r.MissingSections()
	if missing == nil {
		return
	}
	fmt.Println()
	if len(missing) == 0 {
		fmt.Printf("%s Every section of the shipped default is present in this copy;\n", style.SuccessPrefix)
		fmt.Printf("  %s\n", style.Dim.Render("whatever it is behind by is wording inside sections it already has."))
		return
	}
	fmt.Printf("%s Shipped sections absent from this copy (%d):\n", style.WarningPrefix, len(missing))
	for _, s := range missing {
		fmt.Printf("    %s\n", s)
	}
	fmt.Printf("  %s\n", style.Dim.Render("A renamed heading reads as missing — these are where to look, not proof."))
}

func formulaDriftAll(townRoot, rigName string) error {
	drifted, err := formula.ExecutingDrift(townRoot, rigName)
	if err != nil {
		return err
	}

	if formulaDriftJSON {
		return formulaDriftPrintJSON(drifted)
	}

	if len(drifted) == 0 {
		fmt.Printf("%s No formula drift: every executing copy is the default shipped in this build\n", style.SuccessPrefix)
		fmt.Printf("  %s\n", style.Dim.Render("(deliberate local edits with no newer shipped default are not drift)"))
		return nil
	}

	fmt.Printf("%s %d formula(s) executing a copy that is not this build's default:\n\n",
		style.WarningPrefix, len(drifted))
	for _, r := range drifted {
		fix := "gt doctor --fix"
		if !r.AutoFixable() {
			fix = "needs a hand merge — gt formula drift " + r.Name + "   (prints the recipe)"
		}
		fmt.Printf("  %-38s %-9s %s\n", r.Name, r.Drift, r.Tier)
		fmt.Printf("  %s\n", style.Dim.Render(r.Path))
		if line := formula.SummarizeMissingSections(r.MissingSections(), driftListSectionLimit); line != "" {
			fmt.Printf("  %s\n", line)
		}
		fmt.Printf("  %s\n\n", style.Dim.Render("→ "+fix))
	}
	fmt.Printf("%s\n", style.Dim.Render("Detail for one: gt formula drift <name>"))
	return nil
}

func formulaDriftPrintJSON(list []*formula.Resolved) error {
	entries := make([]formulaDriftJSONEntry, 0, len(list))
	for _, r := range list {
		entries = append(entries, formulaDriftJSONEntry{
			Name:            r.Name,
			Tier:            string(r.Tier),
			Path:            r.Path,
			Drift:           string(r.Drift),
			AutoFixable:     r.AutoFixable(),
			CurrentHash:     r.CurrentHash,
			InstalledHash:   r.InstalledHash,
			EmbeddedHash:    r.EmbeddedHash,
			MissingSections: r.MissingSections(),
		})
	}
	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}
