package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/configreg"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// kvPrefix marks rows in the Dolt config table that belong to the beads memory
// store rather than to configuration. They share the table but are not settings,
// and there can be hundreds, so they stay hidden unless --all is given.
const kvPrefix = "kv."

var (
	configListAll    bool
	configListJSON   bool
	configListScope  string
	configListKey    string
	configListNoDolt bool
)

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List every configuration key, its value, and which layer set it",
	Long: `List the complete Gas Town configuration surface.

For every key: the compiled-in default, the value in force, and the layer that
supplied it. Keys set in more than one layer are marked so you can see which
copy is being shadowed — unsetting a shadowed copy reports success and changes
nothing, which is the failure this command exists to make visible.

Layers read, lowest precedence first:
  default        compiled-in fallbacks
  beads-yaml     <namespace>/.beads/config.yaml
  formula-var    .beads/formulas/*.toml declared vars
  town-settings  <town>/settings/config.json
  rig-settings   <town>/<rig>/settings/config.json
  daemon-json    <town>/mayor/daemon.json
  dolt-config    the config table inside each Dolt database
  git-config     git config beads.* in the repo owning a namespace
  env            exported GT_*, BEADS_*, BD_*, DOLT_* variables

Precedence compares only occurrences of the same key in the same scope; the
layers are not a single global stack.

The key list is derived, not curated: struct-backed layers are reflected over
their json tags and file/table layers are read key-by-key, so a newly added
setting appears here without anyone updating a list.

Every layer is reported with its read status, including absent and failed ones.
If a layer cannot be read the command says which and exits non-zero, so a short
listing is never mistaken for "nothing is set".

Examples:
  gt config list                        # keys set by some layer
  gt config list --all                  # every key, including defaults
  gt config list --json                 # machine-readable, diffable between towns
  gt config list --scope town/daemon    # one scope
  gt config list --key scheduler        # keys containing "scheduler"
  gt config list --no-dolt              # skip the Dolt server`,
	Args: cobra.NoArgs,
	// An unreadable layer is a data problem, not a usage problem — printing the
	// flag list on top of the layer table would bury it.
	SilenceUsage: true,
	RunE:         runConfigList,
}

func runConfigList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	report, err := configreg.Collect(configreg.Options{
		TownRoot: townRoot,
		SkipDolt: configListNoDolt,
	})
	if err != nil {
		return err
	}

	if configListJSON {
		// JSON is the audit format: --scope/--key still narrow it because the
		// operator asked, but defaults and kv rows are always carried so a diff
		// between two towns is complete by construction.
		report.Entries, _, _ = filterEntries(report.Entries, true)
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		shown, hiddenDefaults, hiddenKV := filterEntries(report.Entries, configListAll)
		printConfigList(cmd.OutOrStdout(), report, shown, hiddenDefaults, hiddenKV)
	}

	// A layer that could not be read makes the listing incomplete. Fail loud:
	// an absent key and an unreadable layer must never look the same.
	if err := report.FailureError(); err != nil {
		return err
	}
	return nil
}

// filterEntries applies the --scope/--key filters, and when includeAll is false
// also withholds defaulted keys and beads kv.* rows. The withheld counts are
// returned so the footer can say what was dropped rather than silently truncate.
func filterEntries(entries []configreg.Entry, includeAll bool) (shown []configreg.Entry, hiddenDefaults, hiddenKV int) {
	for _, e := range entries {
		if configListScope != "" && !strings.Contains(e.Scope, configListScope) {
			continue
		}
		if configListKey != "" && !strings.Contains(e.Key, configListKey) {
			continue
		}
		if !includeAll {
			if strings.HasPrefix(e.Key, kvPrefix) {
				hiddenKV++
				continue
			}
			if !e.IsSet() {
				hiddenDefaults++
				continue
			}
		}
		shown = append(shown, e)
	}
	return shown, hiddenDefaults, hiddenKV
}

func printConfigList(w io.Writer, report *configreg.Report, shown []configreg.Entry, hiddenDefaults, hiddenKV int) {
	fmt.Fprintf(w, "%s\n", style.Bold.Render("Gas Town configuration"))
	fmt.Fprintf(w, "%s\n\n", style.Dim.Render(report.TownRoot))

	printLayerStatus(w, report)

	byScope := map[string][]configreg.Entry{}
	var scopes []string
	for _, e := range shown {
		if _, ok := byScope[e.Scope]; !ok {
			scopes = append(scopes, e.Scope)
		}
		byScope[e.Scope] = append(byScope[e.Scope], e)
	}
	sort.Strings(scopes)

	shadowed := 0
	for _, scope := range scopes {
		fmt.Fprintf(w, "\n%s\n", style.Bold.Render(scope))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n",
			style.Dim.Render("KEY"), style.Dim.Render("VALUE"),
			style.Dim.Render("DEFAULT"), style.Dim.Render("SOURCE"))
		for _, e := range byScope[scope] {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n",
				e.Key, orDash(e.Value), orDash(e.Default), sourceLabel(e))
			for _, s := range e.Shadowed {
				shadowed++
				fmt.Fprintf(tw, "  %s\t%s\t\t%s\n",
					style.Dim.Render("  └ shadowed"),
					style.Dim.Render(clip(s.Value, 40)),
					style.Dim.Render(s.Layer+" @ "+s.Path))
			}
		}
		_ = tw.Flush()
	}

	if len(shown) == 0 {
		fmt.Fprintf(w, "\n%s\n", style.Dim.Render("No keys matched. See the layer table above for what was read."))
	}

	fmt.Fprintln(w)
	if shadowed > 0 {
		fmt.Fprintf(w, "%s\n", style.Bold.Render(fmt.Sprintf(
			"%d key(s) are set in more than one layer — the shadowed copies have no effect.", shadowed)))
	}
	if hiddenDefaults > 0 {
		fmt.Fprintf(w, "%s\n", style.Dim.Render(fmt.Sprintf(
			"%d key(s) at their compiled-in default are hidden — gt config list --all", hiddenDefaults)))
	}
	if hiddenKV > 0 {
		fmt.Fprintf(w, "%s\n", style.Dim.Render(fmt.Sprintf(
			"%d beads memory row(s) (kv.*) in the config table are hidden — gt config list --all", hiddenKV)))
	}
}

// printLayerStatus prints how every layer was read. This table is what makes an
// empty listing interpretable: "absent" and "error" are different answers.
func printLayerStatus(w io.Writer, report *configreg.Report) {
	fmt.Fprintf(w, "%s\n", style.Bold.Render("Layers"))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, l := range report.Layers {
		detail := ""
		switch l.Status {
		case configreg.StatusOK:
			detail = fmt.Sprintf("%d key(s)", l.Keys)
		case configreg.StatusError:
			detail = l.Error
		default:
			if l.Error != "" {
				detail = l.Error
			}
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n",
			layerStatusMark(l.Status), l.Layer, l.Path, style.Dim.Render(detail))
	}
	_ = tw.Flush()
}

func layerStatusMark(status string) string {
	switch status {
	case configreg.StatusOK:
		return style.Success.Render("ok")
	case configreg.StatusError:
		return style.Error.Render("FAIL")
	default:
		return style.Dim.Render("--")
	}
}

func sourceLabel(e configreg.Entry) string {
	if !e.IsSet() {
		return style.Dim.Render(e.Source)
	}
	return e.Source
}

func orDash(s string) string {
	if s == "" {
		return style.Dim.Render("—")
	}
	return clip(s, 48)
}

// clip shortens a value for the table, collapsing newlines. Config values can
// be long (a Dolt row holds prose), and the JSON output carries them in full.
func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func init() {
	configListCmd.Flags().BoolVar(&configListAll, "all", false, "Include keys sitting at their compiled-in default and beads kv.* rows")
	configListCmd.Flags().BoolVar(&configListJSON, "json", false, "Output the full report as JSON (always includes every key and layer)")
	configListCmd.Flags().StringVar(&configListScope, "scope", "", "Only scopes containing this substring (town/settings, beads/hq, ...)")
	configListCmd.Flags().StringVar(&configListKey, "key", "", "Only keys containing this substring")
	configListCmd.Flags().BoolVar(&configListNoDolt, "no-dolt", false, "Do not contact the Dolt server; its layer is reported absent")

	configCmd.AddCommand(configListCmd)
}
