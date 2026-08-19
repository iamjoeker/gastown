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
		switch resolved.Drift {
		case formula.DriftCustomized:
			reason = "locally customized, but no newer default has shipped since — nothing is being missed"
		case formula.DriftNone:
			if resolved.EmbeddedHash == "" {
				reason = "this build ships no default under that name (purely local formula)"
			}
		}
		fmt.Printf("  %s %s\n", style.SuccessPrefix, reason)
		return nil
	}

	fmt.Printf("  drift:          %s\n", resolved.Drift)
	fmt.Println()
	fmt.Print(resolved.DriftNotice())
	return nil
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
			fix = "needs a human merge — gt formula drift " + r.Name
		}
		fmt.Printf("  %-38s %-9s %s\n", r.Name, r.Drift, r.Tier)
		fmt.Printf("  %s\n", style.Dim.Render(r.Path))
		fmt.Printf("  %s\n\n", style.Dim.Render("→ "+fix))
	}
	fmt.Printf("%s\n", style.Dim.Render("Detail for one: gt formula drift <name>"))
	return nil
}

func formulaDriftPrintJSON(list []*formula.Resolved) error {
	entries := make([]formulaDriftJSONEntry, 0, len(list))
	for _, r := range list {
		entries = append(entries, formulaDriftJSONEntry{
			Name:          r.Name,
			Tier:          string(r.Tier),
			Path:          r.Path,
			Drift:         string(r.Drift),
			AutoFixable:   r.AutoFixable(),
			CurrentHash:   r.CurrentHash,
			InstalledHash: r.InstalledHash,
			EmbeddedHash:  r.EmbeddedHash,
		})
	}
	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}
