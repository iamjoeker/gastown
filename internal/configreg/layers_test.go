package configreg

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeTown lays down a minimal but realistic town: a rig registry, town
// settings, daemon patrol config, a beads namespace with a config.yaml, and a
// formula. Dolt is skipped — the layers under test are the file-backed ones.
func fakeTown(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("mayor/rigs.json", `{"version":1,"rigs":{"widget":{"git_url":"git@example.com:widget.git"}}}`)
	write("settings/config.json", `{
		"type": "town-settings",
		"version": 1,
		"cli_theme": "dark",
		"scheduler": {"max_polecats": 4}
	}`)
	write("mayor/daemon.json", `{
		"type": "daemon-patrol-config",
		"version": 1,
		"patrols": {"wisp_reaper": {"enabled": false, "interval": "90m"}}
	}`)
	write(".beads/metadata.json", `{"backend":"dolt","dolt_database":"hq"}`)
	write(".beads/config.yaml", "prefix: hq\nrouting:\n    mode: explicit\nbeads.role: maintainer\n")
	write(".beads/formulas/mol-demo.formula.toml", `formula = "mol-demo"
description = "demo"

[[steps]]
id = "one"
prompt = "do the thing"

[vars.stale_age]
description = "How old before a wisp is stale"
default = "168h"
`)
	write("widget/settings/config.json", `{"type":"rig-settings","version":1}`)

	return root
}

func collectFake(t *testing.T, root string) *Report {
	t.Helper()
	rep, err := Collect(Options{TownRoot: root, SkipDolt: true, EnvPrefixes: []string{"GT_CONFIGREG_TEST_"}})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return rep
}

func TestCollectReadsEveryFileLayer(t *testing.T) {
	rep := collectFake(t, fakeTown(t))

	if err := rep.FailureError(); err != nil {
		t.Fatalf("FailureError() = %v, want nil for a healthy town", err)
	}

	// Town settings: a set key reports its layer, an unset one its default.
	theme := findEntry(t, rep, "town/settings", "cli_theme")
	if theme.Value != "dark" || theme.Source != LayerTownSettings {
		t.Errorf("cli_theme = %+v, want dark from town-settings", theme)
	}
	if theme.Default != "auto" {
		t.Errorf("cli_theme default = %q, want the accessor default auto", theme.Default)
	}

	maxPolecats := findEntry(t, rep, "town/settings", "scheduler.max_polecats")
	if maxPolecats.Value != "4" || maxPolecats.Source != LayerTownSettings {
		t.Errorf("scheduler.max_polecats = %+v, want 4 from town-settings", maxPolecats)
	}
	if maxPolecats.Default != "-1" {
		t.Errorf("scheduler.max_polecats default = %q, want -1", maxPolecats.Default)
	}

	// The key whose default silently disabled queue dispatch must be listed
	// even when nobody has ever set it.
	batch := findEntry(t, rep, "town/settings", "scheduler.batch_size")
	if batch.IsSet() {
		t.Errorf("scheduler.batch_size = %+v, want it reported as defaulted", batch)
	}
	if batch.Value != "1" {
		t.Errorf("scheduler.batch_size value = %q, want the default 1", batch.Value)
	}

	// Daemon patrols.
	reaper := findEntry(t, rep, "town/daemon", "patrols.wisp_reaper.interval")
	if reaper.Value != "90m" || reaper.Source != LayerDaemonJSON {
		t.Errorf("wisp_reaper.interval = %+v, want 90m from daemon-json", reaper)
	}
	// An explicit false must not read as "unset" — that is the whole point of
	// checking the raw document rather than the struct's zero value.
	enabled := findEntry(t, rep, "town/daemon", "patrols.wisp_reaper.enabled")
	if enabled.Value != "false" || enabled.Source != LayerDaemonJSON {
		t.Errorf("wisp_reaper.enabled = %+v, want an explicit false from daemon-json", enabled)
	}
	if enabled.Default != "true" {
		t.Errorf("wisp_reaper.enabled default = %q, want true", enabled.Default)
	}

	// Beads namespace config.yaml, including a nested map flattened to dots.
	mode := findEntry(t, rep, "beads/hq", "routing.mode")
	if mode.Value != "explicit" || mode.Source != LayerBeadsYAML {
		t.Errorf("routing.mode = %+v, want explicit from beads-yaml", mode)
	}

	// Formula vars.
	stale := findEntry(t, rep, "formula/mol-demo", "stale_age")
	if stale.Value != "168h" || stale.Source != LayerFormulaVar {
		t.Errorf("stale_age = %+v, want 168h from formula-var", stale)
	}
	if stale.Doc == "" {
		t.Error("formula vars should carry their declared description")
	}
}

func TestCollectFallsBackToNamespaceDirWhenNoDatabase(t *testing.T) {
	// Without metadata.json the namespace is named after its directory rather
	// than dropped from the listing: keys an operator set are still shown.
	root := fakeTown(t)
	if err := os.Remove(filepath.Join(root, ".beads", "metadata.json")); err != nil {
		t.Fatal(err)
	}

	rep := collectFake(t, root)
	scope := "beads/" + filepath.Base(root)
	findEntry(t, rep, scope, "routing.mode")
}

func TestCollectReportsAbsentLayersWithoutFailing(t *testing.T) {
	root := t.TempDir() // nothing in it at all
	rep := collectFake(t, root)

	if err := rep.FailureError(); err != nil {
		t.Fatalf("FailureError() = %v, want nil: absent is not failed", err)
	}
	for _, layer := range []string{LayerTownSettings, LayerDaemonJSON} {
		if s := findLayer(t, rep, layer, root); s.Status != StatusAbsent {
			t.Errorf("%s status = %q, want absent", layer, s.Status)
		}
	}
	// An empty town still enumerates its keys at their defaults, so the reader
	// can see what exists to configure.
	if len(rep.Entries) == 0 {
		t.Fatal("an empty town should still list every known key at its default")
	}
	findEntry(t, rep, "town/settings", "scheduler.max_polecats")
}

func TestCollectFailsLoudlyOnUnparseableLayer(t *testing.T) {
	root := fakeTown(t)
	if err := os.WriteFile(filepath.Join(root, "mayor", "daemon.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := collectFake(t, root)

	s := findLayer(t, rep, LayerDaemonJSON, "daemon.json")
	if s.Status != StatusError {
		t.Errorf("daemon-json status = %q, want error", s.Status)
	}
	err := rep.FailureError()
	if err == nil {
		t.Fatal("FailureError() = nil for a town with an unparseable layer")
	}
	// The whole point: the caller must be able to tell "nothing is set" from
	// "I could not read this".
	if len(rep.Entries) == 0 {
		t.Error("a broken layer should not empty the rest of the listing")
	}
}

func TestCollectFailsOnUnparseableBeadsYAML(t *testing.T) {
	root := fakeTown(t)
	if err := os.WriteFile(filepath.Join(root, ".beads", "config.yaml"), []byte("prefix: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := collectFake(t, root)
	if s := findLayer(t, rep, LayerBeadsYAML, "config.yaml"); s.Status != StatusError {
		t.Errorf("beads-yaml status = %q, want error", s.Status)
	}
	if rep.FailureError() == nil {
		t.Error("FailureError() = nil despite an unparseable config.yaml")
	}
}

func TestCollectInventoriesEnvironment(t *testing.T) {
	t.Setenv("GT_CONFIGREG_TEST_MODE", "loud")
	rep := collectFake(t, fakeTown(t))

	e := findEntry(t, rep, "env", "GT_CONFIGREG_TEST_MODE")
	if e.Value != "loud" || e.Source != LayerEnv {
		t.Errorf("env entry = %+v, want loud from env", e)
	}
}

func TestCollectSkipDoltReportsAbsentNotSilent(t *testing.T) {
	rep := collectFake(t, fakeTown(t))
	s := findLayer(t, rep, LayerDoltConfig, "hq")
	if s.Status != StatusAbsent {
		t.Errorf("dolt-config status = %q, want absent when skipped", s.Status)
	}
	if s.Error == "" {
		t.Error("a skipped layer should say it was skipped, not look like it was read")
	}
	if rep.FailureError() != nil {
		t.Error("skipping a layer on purpose is not a failure")
	}
}

func TestCollectRequiresTownRoot(t *testing.T) {
	if _, err := Collect(Options{}); err == nil {
		t.Error("Collect with no TownRoot should error rather than list nothing")
	}
}

func TestFlattenDottedYAMLKeys(t *testing.T) {
	got := flatten("", map[string]any{
		"prefix":  "hq",
		"routing": map[string]any{"mode": "explicit", "contributor": "/tmp/x"},
		"types":   map[string]any{"custom": "agent,role"},
		"list":    []any{"a", "b"},
	})
	want := map[string]string{
		"prefix":              "hq",
		"routing.mode":        "explicit",
		"routing.contributor": "/tmp/x",
		"types.custom":        "agent,role",
		"list":                `["a","b"]`,
	}
	if len(got) != len(want) {
		t.Fatalf("flatten produced %d pairs, want %d: %+v", len(got), len(want), got)
	}
	for _, kv := range got {
		if want[kv.key] != kv.value {
			t.Errorf("%s = %q, want %q", kv.key, kv.value, want[kv.key])
		}
	}
}

func TestLookupPathDistinguishesAbsentFromZero(t *testing.T) {
	doc := map[string]any{"patrols": map[string]any{"wisp_reaper": map[string]any{"enabled": false}}}

	if v, ok := lookupPath(doc, "patrols.wisp_reaper.enabled"); !ok || v != false {
		t.Errorf("explicit false = (%v, %v), want (false, true)", v, ok)
	}
	if _, ok := lookupPath(doc, "patrols.wisp_reaper.interval"); ok {
		t.Error("an absent key reported as present")
	}
	if _, ok := lookupPath(doc, "patrols.doctor_dog.enabled"); ok {
		t.Error("a key under an absent section reported as present")
	}
	if _, ok := lookupPath(nil, "anything"); ok {
		t.Error("a nil document reported a key as present")
	}
}
