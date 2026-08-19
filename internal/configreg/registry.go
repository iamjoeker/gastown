package configreg

import (
	"fmt"
	"sort"
	"strings"
)

// Layer names. A layer is one mechanism that can supply a configuration value.
const (
	// LayerDefault is the compiled-in fallback — the value the code uses when
	// no layer supplies one.
	LayerDefault = "default"
	// LayerTownSettings is <town>/settings/config.json.
	LayerTownSettings = "town-settings"
	// LayerDaemonJSON is <town>/mayor/daemon.json.
	LayerDaemonJSON = "daemon-json"
	// LayerRigSettings is <town>/<rig>/settings/config.json.
	LayerRigSettings = "rig-settings"
	// LayerBeadsYAML is a beads namespace's .beads/config.yaml.
	LayerBeadsYAML = "beads-yaml"
	// LayerGitConfig is git config beads.* in the repo owning a beads namespace.
	LayerGitConfig = "git-config"
	// LayerDoltConfig is the config table inside a Dolt database.
	LayerDoltConfig = "dolt-config"
	// LayerFormulaVar is a var declared by a formula in .beads/formulas.
	LayerFormulaVar = "formula-var"
	// LayerEnv is the process environment.
	LayerEnv = "env"
)

// layerRank decides which occurrence of the same key in the same scope is the
// acting one: highest rank wins. Ranks only ever compare occurrences of one key
// within one scope — they are not a global ordering across unrelated namespaces.
//
// The beads ranks are measured, not assumed: `bd config unset routing.mode`
// reported success against config.yaml while the Dolt row kept winning (gt-il30).
var layerRank = map[string]int{
	LayerDefault:      0,
	LayerBeadsYAML:    10,
	LayerFormulaVar:   15,
	LayerTownSettings: 20,
	LayerRigSettings:  20,
	LayerDaemonJSON:   20,
	LayerDoltConfig:   40,
	LayerGitConfig:    45,
	LayerEnv:          90,
}

// Rank returns the precedence rank of a layer. Unknown layers rank lowest.
func Rank(layer string) int { return layerRank[layer] }

// Occurrence is one layer's copy of a key.
type Occurrence struct {
	Layer string `json:"layer"`
	// Path is the concrete location: a file path, "db:hq/config", "git:<repo>".
	Path  string `json:"path"`
	Value string `json:"value"`
}

// Entry is one configuration key within one scope, fully resolved.
type Entry struct {
	Key string `json:"key"`
	// Scope names the object the key configures: "town/settings",
	// "town/daemon", "rig/gastown", "beads/hq", "formula/mol-wisp-gc", "env".
	Scope string `json:"scope"`
	Type  string `json:"type,omitempty"`
	// Default is the compiled-in fallback, empty when the code has none.
	Default string `json:"default"`
	// Value is the acting value: what the code reads today.
	Value string `json:"value"`
	// Source is the layer that supplied Value, or LayerDefault when unset.
	Source string `json:"source"`
	// Path is where Source found it. Empty when Source is LayerDefault.
	Path string `json:"path,omitempty"`
	// Doc is a description when the layer carries one (formula vars do).
	Doc string `json:"doc,omitempty"`
	// Shadowed lists lower-precedence occurrences that are being overridden.
	// A non-empty Shadowed is the failure mode this command exists to expose:
	// unsetting the shadowed copy reports success and changes nothing.
	Shadowed []Occurrence `json:"shadowed,omitempty"`
}

// IsSet reports whether some layer, rather than the compiled-in default,
// supplied the acting value.
func (e Entry) IsSet() bool { return e.Source != LayerDefault }

// LayerStatus records what happened when a layer was read. Every layer is
// reported, including ones that were absent, so an empty listing can never be
// confused with an unreadable source.
type LayerStatus struct {
	Layer string `json:"layer"`
	Path  string `json:"path"`
	// Status is "ok", "absent", or "error".
	Status string `json:"status"`
	// Keys is the number of explicitly set keys this layer supplied.
	Keys  int    `json:"keys"`
	Error string `json:"error,omitempty"`
}

// Status values for LayerStatus.
const (
	StatusOK     = "ok"
	StatusAbsent = "absent"
	StatusError  = "error"
)

// Report is the full configuration inventory.
type Report struct {
	TownRoot string        `json:"town_root"`
	Entries  []Entry       `json:"entries"`
	Layers   []LayerStatus `json:"layers"`
}

// Failures returns the layers that could not be read. A caller that prints a
// listing must treat a non-empty result as an error: a key missing from an
// unreadable layer is not the same as a key that is unset.
func (r *Report) Failures() []LayerStatus {
	var out []LayerStatus
	for _, l := range r.Layers {
		if l.Status == StatusError {
			out = append(out, l)
		}
	}
	return out
}

// FailureError summarizes unreadable layers, or returns nil when all were read.
func (r *Report) FailureError() error {
	failed := r.Failures()
	if len(failed) == 0 {
		return nil
	}
	parts := make([]string, 0, len(failed))
	for _, l := range failed {
		parts = append(parts, fmt.Sprintf("%s (%s): %s", l.Layer, l.Path, l.Error))
	}
	return fmt.Errorf("could not read %d config layer(s), listing is incomplete: %s",
		len(failed), strings.Join(parts, "; "))
}

// Scopes returns the distinct scopes present, in listing order.
func (r *Report) Scopes() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range r.Entries {
		if !seen[e.Scope] {
			seen[e.Scope] = true
			out = append(out, e.Scope)
		}
	}
	sort.Strings(out)
	return out
}

// builder accumulates keys from every collector and resolves precedence once
// all layers have reported.
type builder struct {
	entries map[string]*Entry
	order   []string
	occs    map[string][]Occurrence
	layers  []LayerStatus
}

func newBuilder() *builder {
	return &builder{
		entries: map[string]*Entry{},
		occs:    map[string][]Occurrence{},
	}
}

func entryID(scope, key string) string { return scope + "\x00" + key }

// declare registers a key that exists in a scope, with its compiled-in default.
// Calling it twice for the same key keeps the first non-empty default.
func (b *builder) declare(scope, key, typ, def, doc string) {
	id := entryID(scope, key)
	e, ok := b.entries[id]
	if !ok {
		b.entries[id] = &Entry{
			Key: key, Scope: scope, Type: typ,
			Default: def, Value: def, Source: LayerDefault, Doc: doc,
		}
		b.order = append(b.order, id)
		return
	}
	if e.Default == "" && def != "" {
		e.Default = def
	}
	if e.Type == "" {
		e.Type = typ
	}
	if e.Doc == "" {
		e.Doc = doc
	}
}

// observe records that a layer supplies a value for a key. Keys observed in a
// layer but never declared are still listed — a config file may legitimately
// hold keys no Go struct models (the Dolt config table does).
func (b *builder) observe(scope, key, layer, path, value string) {
	b.declare(scope, key, "", "", "")
	id := entryID(scope, key)
	b.occs[id] = append(b.occs[id], Occurrence{Layer: layer, Path: path, Value: value})
}

func (b *builder) layer(s LayerStatus) { b.layers = append(b.layers, s) }

// resolve picks the acting occurrence for each key and records the rest as
// shadowed, highest precedence first.
func (b *builder) resolve(townRoot string) *Report {
	rep := &Report{TownRoot: townRoot, Layers: b.layers}
	for _, id := range b.order {
		e := b.entries[id]
		occs := b.occs[id]
		if len(occs) > 0 {
			sort.SliceStable(occs, func(i, j int) bool {
				return Rank(occs[i].Layer) > Rank(occs[j].Layer)
			})
			acting := occs[0]
			e.Source = acting.Layer
			e.Path = acting.Path
			e.Value = acting.Value
			if len(occs) > 1 {
				e.Shadowed = occs[1:]
			}
		}
		rep.Entries = append(rep.Entries, *e)
	}
	sort.SliceStable(rep.Entries, func(i, j int) bool {
		if rep.Entries[i].Scope != rep.Entries[j].Scope {
			return rep.Entries[i].Scope < rep.Entries[j].Scope
		}
		return rep.Entries[i].Key < rep.Entries[j].Key
	})
	return rep
}
