package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/configreg"
)

// setterKeyTargets maps every key `gt config set` accepts to the registry key
// `gt config list` reports it under. The setter uses friendly aliases and writes
// to two different files, so the mapping is not always the identity.
//
// Adding a key to runConfigSet without adding it here fails this test: that is
// the point. A config surface you cannot enumerate is the bug (gt-il30), and the
// only way the enumeration stays complete is if a missing key is a build break.
var setterKeyTargets = map[string][]string{
	"convoy.notify_on_complete":   {"convoy.notify_on_complete"},
	"cli_theme":                   {"cli_theme"},
	"default_agent":               {"default_agent"},
	"scheduler.max_polecats":      {"scheduler.max_polecats"},
	"scheduler.batch_size":        {"scheduler.batch_size"},
	"scheduler.spawn_delay":       {"scheduler.spawn_delay"},
	"polecat.target_clean_policy": {"polecat.target_clean_policy"},
	"maintenance.window":          {"patrols.scheduled_maintenance.window"},
	"maintenance.interval":        {"patrols.scheduled_maintenance.interval"},
	"maintenance.threshold":       {"patrols.scheduled_maintenance.threshold"},
	// dolt.port writes daemon.json's env map, which the listing carries whole
	// and the env layer reports again as GT_DOLT_PORT when exported.
	"dolt.port":                     {"env"},
	"lifecycle.reaper.enabled":      {"patrols.wisp_reaper.enabled"},
	"lifecycle.reaper.interval":     {"patrols.wisp_reaper.interval"},
	"lifecycle.reaper.delete_age":   {"patrols.wisp_reaper.delete_age"},
	"lifecycle.compactor.enabled":   {"patrols.compactor_dog.enabled"},
	"lifecycle.compactor.interval":  {"patrols.compactor_dog.interval"},
	"lifecycle.compactor.threshold": {"patrols.compactor_dog.threshold"},
	"lifecycle.doctor.enabled":      {"patrols.doctor_dog.enabled"},
	"lifecycle.doctor.interval":     {"patrols.doctor_dog.interval"},
	"lifecycle.backup.enabled":      {"patrols.jsonl_git_backup.enabled", "patrols.dolt_backup.enabled"},
	"lifecycle.backup.interval":     {"patrols.jsonl_git_backup.interval", "patrols.dolt_backup.interval"},
}

func collectTestTown(t *testing.T) *configreg.Report {
	t.Helper()
	root := t.TempDir()
	rep, err := configreg.Collect(configreg.Options{
		TownRoot:    root,
		SkipDolt:    true,
		EnvPrefixes: []string{"GT_CONFIG_LIST_TEST_"},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return rep
}

func TestConfigListCoversEverySetterKey(t *testing.T) {
	rep := collectTestTown(t)

	present := map[string]bool{}
	for _, e := range rep.Entries {
		present[e.Key] = true
	}

	for setterKey, targets := range setterKeyTargets {
		for _, target := range targets {
			if !present[target] {
				t.Errorf("gt config set %s writes %q, but gt config list never lists it", setterKey, target)
			}
		}
	}
}

func TestConfigListListsUnsetKeysInAnEmptyTown(t *testing.T) {
	// The motivating failure: scheduler.max_polecats defaults to -1, which
	// means direct dispatch, and nothing in the town says so.
	rep := collectTestTown(t)

	var found *configreg.Entry
	for i := range rep.Entries {
		if rep.Entries[i].Key == "scheduler.max_polecats" {
			found = &rep.Entries[i]
			break
		}
	}
	if found == nil {
		t.Fatal("scheduler.max_polecats missing from an unconfigured town")
	}
	if found.Value != "-1" || found.Default != "-1" {
		t.Errorf("scheduler.max_polecats = %+v, want -1 by default", *found)
	}
	if found.Source != configreg.LayerDefault {
		t.Errorf("source = %q, want %q", found.Source, configreg.LayerDefault)
	}
}

func withListFlags(t *testing.T, all bool, scope, key string) {
	t.Helper()
	prevAll, prevScope, prevKey := configListAll, configListScope, configListKey
	configListAll, configListScope, configListKey = all, scope, key
	t.Cleanup(func() {
		configListAll, configListScope, configListKey = prevAll, prevScope, prevKey
	})
}

func TestFilterEntriesHidesDefaultsButCountsThem(t *testing.T) {
	entries := []configreg.Entry{
		{Key: "a", Scope: "town/settings", Source: configreg.LayerDefault},
		{Key: "b", Scope: "town/settings", Source: configreg.LayerTownSettings},
		{Key: "kv.memory.note", Scope: "beads/hq", Source: configreg.LayerDoltConfig},
	}

	withListFlags(t, false, "", "")
	shown, hiddenDefaults, hiddenKV := filterEntries(entries, false)
	if len(shown) != 1 || shown[0].Key != "b" {
		t.Errorf("shown = %+v, want only the key some layer set", shown)
	}
	if hiddenDefaults != 1 || hiddenKV != 1 {
		t.Errorf("hidden counts = %d defaults / %d kv, want 1 / 1", hiddenDefaults, hiddenKV)
	}

	shown, hiddenDefaults, hiddenKV = filterEntries(entries, true)
	if len(shown) != 3 || hiddenDefaults != 0 || hiddenKV != 0 {
		t.Errorf("includeAll: shown=%d hiddenDefaults=%d hiddenKV=%d, want 3/0/0",
			len(shown), hiddenDefaults, hiddenKV)
	}
}

func TestFilterEntriesNarrowsByScopeAndKey(t *testing.T) {
	entries := []configreg.Entry{
		{Key: "scheduler.batch_size", Scope: "town/settings", Source: configreg.LayerTownSettings},
		{Key: "patrols.wisp_reaper.enabled", Scope: "town/daemon", Source: configreg.LayerDaemonJSON},
	}

	withListFlags(t, true, "town/daemon", "")
	if shown, _, _ := filterEntries(entries, true); len(shown) != 1 || shown[0].Scope != "town/daemon" {
		t.Errorf("scope filter = %+v, want only town/daemon", shown)
	}

	withListFlags(t, true, "", "scheduler")
	if shown, _, _ := filterEntries(entries, true); len(shown) != 1 || shown[0].Key != "scheduler.batch_size" {
		t.Errorf("key filter = %+v, want only the scheduler key", shown)
	}
}

func TestPrintConfigListShowsLayerStatusAndShadowing(t *testing.T) {
	rep := &configreg.Report{
		TownRoot: "/t",
		Layers: []configreg.LayerStatus{
			{Layer: configreg.LayerTownSettings, Path: "/t/settings/config.json", Status: configreg.StatusAbsent},
			{Layer: configreg.LayerDoltConfig, Path: "dolt:hq", Status: configreg.StatusError, Error: "connection refused"},
		},
		Entries: []configreg.Entry{{
			Key: "routing.mode", Scope: "beads/hq", Default: "", Value: "contributor",
			Source: configreg.LayerDoltConfig, Path: "dolt:hq",
			Shadowed: []configreg.Occurrence{{
				Layer: configreg.LayerBeadsYAML, Path: "/t/.beads/config.yaml", Value: "explicit",
			}},
		}},
	}

	var buf strings.Builder
	printConfigList(&buf, rep, rep.Entries, 7, 3)
	out := buf.String()

	for _, want := range []string{
		"routing.mode", "contributor", "shadowed", "explicit",
		"/t/.beads/config.yaml", "connection refused",
		"7 key(s) at their compiled-in default are hidden",
		"3 beads memory row(s)",
		"set in more than one layer",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintConfigListEmptyResultStillExplainsItself(t *testing.T) {
	// An empty listing must never be mistaken for "nothing is configured":
	// the layer table is printed either way.
	rep := &configreg.Report{
		TownRoot: "/t",
		Layers: []configreg.LayerStatus{
			{Layer: configreg.LayerDoltConfig, Path: "dolt:hq", Status: configreg.StatusError, Error: "connection refused"},
		},
	}

	var buf strings.Builder
	printConfigList(&buf, rep, nil, 0, 0)
	out := buf.String()

	if !strings.Contains(out, "No keys matched") {
		t.Errorf("empty listing does not say so:\n%s", out)
	}
	if !strings.Contains(out, "connection refused") {
		t.Errorf("empty listing does not name the unreadable layer:\n%s", out)
	}
}

func TestConfigListSurfacesShadowedKeysFromRealLayers(t *testing.T) {
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// git config and config.yaml both carry beads.role; git config wins.
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
		[]byte("beads.role: contributor\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"),
		[]byte(`{"dolt_database":"hq"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init")
	runGit(t, root, "config", "beads.role", "maintainer")

	rep, err := configreg.Collect(configreg.Options{TownRoot: root, SkipDolt: true})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	for _, e := range rep.Entries {
		if e.Key != "beads.role" {
			continue
		}
		if e.Value != "maintainer" || e.Source != configreg.LayerGitConfig {
			t.Errorf("beads.role = %+v, want maintainer from git-config", e)
		}
		if len(e.Shadowed) != 1 || e.Shadowed[0].Value != "contributor" {
			t.Errorf("beads.role shadowed = %+v, want the config.yaml copy", e.Shadowed)
		}
		return
	}
	t.Fatal("beads.role not listed")
}
