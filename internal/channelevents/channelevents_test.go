package channelevents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain opts this package in to real event emission.
//
// emit refuses to write under `go test` unless this variable is set, so that a
// test which reaches an emit by accident cannot wake live agents in the town it
// happens to be running inside. Every emit below targets a t.TempDir(), so the
// opt-in is safe here and nowhere else — do not copy it into another package's
// TestMain without the same guarantee.
func TestMain(m *testing.M) {
	// Point this package at a dead Dolt port before anything else runs, so a
	// test that reaches for Dolt without arranging a server of its own cannot
	// land on the production one. See testenv.GuardProductionDolt.
	testenv.GuardProductionDolt()

	if err := os.Setenv(AllowTestEmitEnv, "1"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestEmitToTown(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	path, err := EmitToTown(townRoot, "mayor", "SLOT_OPEN", []string{
		"source=witness",
		"rig=dashboard",
	})
	if err != nil {
		t.Fatalf("EmitToTown failed: %v", err)
	}

	if !strings.HasSuffix(path, ".event") {
		t.Errorf("expected .event suffix, got %q", path)
	}

	// Town-scoped channels get no rig segment.
	wantDir := filepath.Join(townRoot, "events", "mayor")
	if got := filepath.Dir(path); got != wantDir {
		t.Errorf("event dir = %q, want %q", got, wantDir)
	}

	event := readEvent(t, path)
	if event["type"] != "SLOT_OPEN" {
		t.Errorf("type = %v, want SLOT_OPEN", event["type"])
	}
	if event["channel"] != "mayor" {
		t.Errorf("channel = %v, want mayor", event["channel"])
	}
	if _, ok := event["rig"]; ok {
		t.Errorf("town-scoped event should carry no rig field, got %v", event["rig"])
	}

	payload, ok := event["payload"].(map[string]interface{})
	if !ok {
		t.Fatal("payload is not a map")
	}
	if payload["source"] != "witness" {
		t.Errorf("payload.source = %v, want witness", payload["source"])
	}
	if payload["rig"] != "dashboard" {
		t.Errorf("payload.rig = %v, want dashboard", payload["rig"])
	}
}

func TestEmitToRig(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	path, err := EmitToRig(townRoot, "gastown", "refinery", "MQ_SUBMIT", []string{"source=sling"})
	if err != nil {
		t.Fatalf("EmitToRig failed: %v", err)
	}

	// The rig segment is what separates one rig's refinery from another's.
	wantDir := filepath.Join(townRoot, "events", "gastown", "refinery")
	if got := filepath.Dir(path); got != wantDir {
		t.Errorf("event dir = %q, want %q", got, wantDir)
	}

	event := readEvent(t, path)
	if event["rig"] != "gastown" {
		t.Errorf("rig = %v, want gastown", event["rig"])
	}
	if event["channel"] != "refinery" {
		t.Errorf("channel = %v, want refinery", event["channel"])
	}
}

// TestCrossRigIsolation is the regression test for the defect this scoping
// exists to prevent: two rigs' agents watching the same logical channel must
// not share a directory. If they did, whichever consumer read first would
// delete the event (await-event --cleanup) and the other rig's refinery would
// never learn its MR was submitted.
func TestCrossRigIsolation(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	rigs := []string{"gastown", "beads", "duly_noted"}
	paths := make(map[string]string, len(rigs))

	for _, rigName := range rigs {
		path, err := EmitToRig(townRoot, rigName, "refinery", "MQ_SUBMIT", []string{"rig=" + rigName})
		if err != nil {
			t.Fatalf("EmitToRig(%s) failed: %v", rigName, err)
		}
		paths[rigName] = path
	}

	// Each rig's consumer sees exactly its own event, and nobody else's.
	for _, rigName := range rigs {
		dir, err := ChannelDir(townRoot, rigName, "refinery")
		if err != nil {
			t.Fatalf("ChannelDir(%s): %v", rigName, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		var events []string
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".event") {
				events = append(events, entry.Name())
			}
		}
		if len(events) != 1 {
			t.Fatalf("rig %s: got %d events in its channel, want exactly 1 (cross-rig leak)", rigName, len(events))
		}

		event := readEvent(t, filepath.Join(dir, events[0]))
		if event["rig"] != rigName {
			t.Errorf("rig %s: channel contains an event owned by %v", rigName, event["rig"])
		}
	}

	// A consumer draining its own channel with --cleanup must not destroy
	// another rig's pending event.
	if err := os.Remove(paths["gastown"]); err != nil {
		t.Fatalf("simulating --cleanup: %v", err)
	}
	for _, rigName := range []string{"beads", "duly_noted"} {
		if _, err := os.Stat(paths[rigName]); err != nil {
			t.Errorf("rig %s lost its event when gastown drained its own channel: %v", rigName, err)
		}
	}
}

// TestScopeMismatchRejected verifies the two emit entry points refuse channels
// belonging to the other scope, rather than silently writing to the wrong path.
func TestScopeMismatchRejected(t *testing.T) {
	t.Parallel()

	if _, err := EmitToTown(t.TempDir(), "refinery", "TEST", nil); err == nil {
		t.Error("EmitToTown on a rig-scoped channel should error")
	}
	if _, err := EmitToRig(t.TempDir(), "gastown", "mayor", "TEST", nil); err == nil {
		t.Error("EmitToRig on a town-scoped channel should error")
	}
}

func TestChannelDir(t *testing.T) {
	t.Parallel()
	townRoot := "/town"

	tests := []struct {
		name    string
		rig     string
		channel string
		want    string
		wantErr bool
	}{
		{"rig-scoped channel", "gastown", "refinery", "/town/events/gastown/refinery", false},
		{"witness is rig-scoped too", "beads", "witness", "/town/events/beads/witness", false},
		{"mayor is town-scoped", "gastown", "mayor", "/town/events/mayor", false},
		{"deacon is town-scoped", "", "deacon", "/town/events/deacon", false},
		{"unknown channels default to rig-scoped", "gastown", "custom", "/town/events/gastown/custom", false},
		// An empty rig must be an error, never a silent fall back to the flat
		// path — that fallback is the collision this layout prevents.
		{"rig-scoped channel with no rig", "", "refinery", "", true},
		{"invalid channel", "gastown", "../escape", "", true},
		{"invalid rig", "../escape", "refinery", "", true},
		{"rig with slash", "a/b", "refinery", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ChannelDir(townRoot, tt.rig, tt.channel)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != filepath.FromSlash(tt.want) {
				t.Errorf("ChannelDir = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsTownScoped(t *testing.T) {
	t.Parallel()
	// Rig-scoped is the safe default: an over-scoped channel merely loses a
	// wake-up, while an under-scoped one lets another rig destroy events.
	for _, channel := range []string{"mayor", "deacon"} {
		if !IsTownScoped(channel) {
			t.Errorf("%q should be town-scoped", channel)
		}
	}
	for _, channel := range []string{"refinery", "witness", "anything-else"} {
		if IsTownScoped(channel) {
			t.Errorf("%q should be rig-scoped", channel)
		}
	}
}

func TestEmitToRig_UniqueFilenames(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	seen := make(map[string]bool)

	for i := 0; i < 10; i++ {
		path, err := EmitToRig(townRoot, "gastown", "test", "EVENT", nil)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if seen[path] {
			t.Errorf("duplicate filename: %s", path)
		}
		seen[path] = true
	}
}

func TestValidChannelName(t *testing.T) {
	t.Parallel()
	valid := []string{"refinery", "witness", "my-channel", "test_chan", "abc123"}
	for _, name := range valid {
		if !ValidChannelName.MatchString(name) {
			t.Errorf("%q should be valid", name)
		}
	}

	invalid := []string{"../escape", "has space", "has/slash", "", "has.dot"}
	for _, name := range invalid {
		if ValidChannelName.MatchString(name) {
			t.Errorf("%q should be invalid", name)
		}
	}
}

func TestEmitCreatesDirectory(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	channelDir := filepath.Join(townRoot, "events", "gastown", "newchannel")

	if _, err := os.Stat(channelDir); !os.IsNotExist(err) {
		t.Fatal("channel dir should not exist yet")
	}

	if _, err := EmitToRig(townRoot, "gastown", "newchannel", "TEST", nil); err != nil {
		t.Fatalf("EmitToRig failed: %v", err)
	}

	if _, err := os.Stat(channelDir); err != nil {
		t.Errorf("channel dir should exist after emit: %v", err)
	}
}

// TestEmitRefusedUnderTestWithoutOptIn covers the backstop that keeps unit
// tests from emitting real town events. Without the opt-in this package's
// TestMain sets, emit must write nothing — a test binary usually runs inside
// the live town, where a stray emit wakes every refinery into a full patrol.
//
// Deliberately not parallel: it mutates the environment, and Go runs sequential
// tests only when no parallel test is running.
func TestEmitRefusedUnderTestWithoutOptIn(t *testing.T) {
	t.Setenv(AllowTestEmitEnv, "")

	townRoot := t.TempDir()
	path, err := EmitToRig(townRoot, "gastown", "refinery", "MQ_SUBMIT", nil)
	if err != nil {
		t.Fatalf("refused emit should report no error, got: %v", err)
	}
	if path != "" {
		t.Errorf("refused emit should return an empty path, got %q", path)
	}

	// Nothing at all should have been written, not even the directory.
	if _, err := os.Stat(filepath.Join(townRoot, "events")); !os.IsNotExist(err) {
		t.Errorf("refused emit created an events tree: %v", err)
	}
}

func readEvent(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading event file: %v", err)
	}
	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("unmarshaling event: %v", err)
	}
	return event
}
