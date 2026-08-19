package configreg

import (
	"strings"
	"testing"
)

func findEntry(t *testing.T, rep *Report, scope, key string) Entry {
	t.Helper()
	for _, e := range rep.Entries {
		if e.Scope == scope && e.Key == key {
			return e
		}
	}
	t.Fatalf("entry %s/%s not found in report", scope, key)
	return Entry{}
}

func findLayer(t *testing.T, rep *Report, layer, pathContains string) LayerStatus {
	t.Helper()
	for _, l := range rep.Layers {
		if l.Layer == layer && strings.Contains(l.Path, pathContains) {
			return l
		}
	}
	t.Fatalf("layer %s (path containing %q) not reported; layers: %+v", layer, pathContains, rep.Layers)
	return LayerStatus{}
}

func TestResolvePicksHighestRankLayer(t *testing.T) {
	b := newBuilder()
	b.declare("beads/hq", "routing.mode", "string", "", "")
	b.observe("beads/hq", "routing.mode", LayerBeadsYAML, "/t/.beads/config.yaml", "explicit")
	b.observe("beads/hq", "routing.mode", LayerDoltConfig, "dolt:hq", "contributor")

	rep := b.resolve("/t")
	e := findEntry(t, rep, "beads/hq", "routing.mode")

	// Measured behavior: the Dolt row wins and the file merely shadows it, which
	// is why `bd config unset` reported success and changed nothing (gt-il30).
	if e.Source != LayerDoltConfig || e.Value != "contributor" {
		t.Errorf("acting = %s/%s, want dolt-config/contributor", e.Source, e.Value)
	}
	if len(e.Shadowed) != 1 || e.Shadowed[0].Layer != LayerBeadsYAML {
		t.Errorf("shadowed = %+v, want the config.yaml occurrence", e.Shadowed)
	}
}

func TestResolveOrdersShadowedByPrecedence(t *testing.T) {
	b := newBuilder()
	b.observe("beads/hq", "beads.role", LayerBeadsYAML, "yaml", "contributor")
	b.observe("beads/hq", "beads.role", LayerDoltConfig, "dolt", "maintainer")
	b.observe("beads/hq", "beads.role", LayerGitConfig, "git", "maintainer")

	e := findEntry(t, b.resolve("/t"), "beads/hq", "beads.role")
	if e.Source != LayerGitConfig {
		t.Fatalf("source = %s, want git-config (highest rank)", e.Source)
	}
	want := []string{LayerDoltConfig, LayerBeadsYAML}
	if len(e.Shadowed) != len(want) {
		t.Fatalf("shadowed = %+v, want %d entries", e.Shadowed, len(want))
	}
	for i, layer := range want {
		if e.Shadowed[i].Layer != layer {
			t.Errorf("shadowed[%d] = %s, want %s", i, e.Shadowed[i].Layer, layer)
		}
	}
}

func TestResolveKeepsUnsetKeysAtTheirDefault(t *testing.T) {
	b := newBuilder()
	b.declare("town/settings", "scheduler.max_polecats", "int", "-1", "")

	e := findEntry(t, b.resolve("/t"), "town/settings", "scheduler.max_polecats")
	if e.Source != LayerDefault {
		t.Errorf("source = %s, want default", e.Source)
	}
	if e.Value != "-1" || e.Default != "-1" {
		t.Errorf("value/default = %s/%s, want -1/-1", e.Value, e.Default)
	}
	if e.IsSet() {
		t.Error("IsSet() = true for a key no layer supplies")
	}
	if e.Path != "" {
		t.Errorf("path = %q, want empty for a defaulted key", e.Path)
	}
}

func TestObserveListsKeysNoStructModels(t *testing.T) {
	// The Dolt config table holds keys gt has no struct for. Dropping them
	// would rebuild the curated subset this command exists to replace.
	b := newBuilder()
	b.observe("beads/hq", "some.future.key", LayerDoltConfig, "dolt:hq", "on")

	e := findEntry(t, b.resolve("/t"), "beads/hq", "some.future.key")
	if e.Value != "on" || e.Source != LayerDoltConfig {
		t.Errorf("entry = %+v, want the observed dolt value", e)
	}
}

func TestFailureErrorNamesTheUnreadableLayer(t *testing.T) {
	rep := &Report{Layers: []LayerStatus{
		{Layer: LayerBeadsYAML, Path: "/t/.beads/config.yaml", Status: StatusOK, Keys: 3},
		{Layer: LayerDoltConfig, Path: "dolt:hq", Status: StatusError, Error: "connection refused"},
	}}

	err := rep.FailureError()
	if err == nil {
		t.Fatal("FailureError() = nil for a report with a failed layer")
	}
	for _, want := range []string{"dolt-config", "dolt:hq", "connection refused", "incomplete"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if len(rep.Failures()) != 1 {
		t.Errorf("Failures() = %d, want 1", len(rep.Failures()))
	}
}

func TestFailureErrorNilWhenEveryLayerRead(t *testing.T) {
	// Absent is not failed: a town with no settings file is a valid answer.
	rep := &Report{Layers: []LayerStatus{
		{Layer: LayerTownSettings, Path: "/t/settings/config.json", Status: StatusAbsent},
		{Layer: LayerEnv, Path: "process environment", Status: StatusOK},
	}}
	if err := rep.FailureError(); err != nil {
		t.Errorf("FailureError() = %v, want nil", err)
	}
}

func TestDeclareKeepsFirstNonEmptyDefault(t *testing.T) {
	b := newBuilder()
	b.declare("town/settings", "cli_theme", "string", "", "")
	b.declare("town/settings", "cli_theme", "string", "auto", "the doc")

	e := findEntry(t, b.resolve("/t"), "town/settings", "cli_theme")
	if e.Default != "auto" {
		t.Errorf("default = %q, want auto", e.Default)
	}
	if e.Doc != "the doc" {
		t.Errorf("doc = %q, want it filled in on the second declare", e.Doc)
	}
}

func TestRankOrdersLayersAsDocumented(t *testing.T) {
	ordered := []string{
		LayerDefault, LayerBeadsYAML, LayerFormulaVar,
		LayerTownSettings, LayerDoltConfig, LayerGitConfig, LayerEnv,
	}
	for i := 1; i < len(ordered); i++ {
		if Rank(ordered[i-1]) >= Rank(ordered[i]) {
			t.Errorf("Rank(%s)=%d should be below Rank(%s)=%d",
				ordered[i-1], Rank(ordered[i-1]), ordered[i], Rank(ordered[i]))
		}
	}
	if Rank("not-a-layer") != 0 {
		t.Error("an unknown layer should rank lowest, never win")
	}
}
