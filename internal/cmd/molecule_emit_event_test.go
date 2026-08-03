package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/channelevents"
)

// testEmitRig names the rig used by the emit tests below. "test-channel" is not
// a town-scoped channel, so it is rig-scoped and needs one.
const testEmitRig = "testrig"

// allowTestEmit opts a single test in to real event emission.
//
// channelevents.emit refuses to write under `go test` unless this is set. That
// backstop exists because of this very package: a test here once reached a live
// emit and woke every refinery in the town. Set it only in a test that emits
// exclusively to a t.TempDir() town root, as the ones below do.
func allowTestEmit(t *testing.T) {
	t.Helper()
	t.Setenv(channelevents.AllowTestEmitEnv, "1")
}

func TestEmitEvent(t *testing.T) {
	allowTestEmit(t)

	t.Run("basic event creation", func(t *testing.T) {
		townRoot := t.TempDir()

		path, err := channelevents.EmitToRig(townRoot, testEmitRig, "test-channel", "MERGE_READY", []string{"polecat=nux", "branch=feat/test"})
		if err != nil {
			t.Fatalf("EmitEvent failed: %v", err)
		}
		if !strings.HasSuffix(path, ".event") {
			t.Errorf("expected .event suffix, got %q", path)
		}

		// Read and verify content
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read event file: %v", err)
		}

		var event map[string]interface{}
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("failed to parse event JSON: %v", err)
		}
		if event["type"] != "MERGE_READY" {
			t.Errorf("type = %v, want MERGE_READY", event["type"])
		}
		if event["channel"] != "test-channel" {
			t.Errorf("channel = %v, want test-channel", event["channel"])
		}
		if event["timestamp"] == nil {
			t.Error("expected timestamp to be set")
		}

		payload, ok := event["payload"].(map[string]interface{})
		if !ok {
			t.Fatalf("payload is not a map: %T", event["payload"])
		}
		if payload["polecat"] != "nux" {
			t.Errorf("payload.polecat = %v, want nux", payload["polecat"])
		}
		if payload["branch"] != "feat/test" {
			t.Errorf("payload.branch = %v, want feat/test", payload["branch"])
		}
	})

	t.Run("empty payload", func(t *testing.T) {
		townRoot := t.TempDir()
		path, err := channelevents.EmitToRig(townRoot, testEmitRig, "test-channel", "PATROL_WAKE", nil)
		if err != nil {
			t.Fatalf("EmitEvent failed: %v", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read event file: %v", err)
		}

		var event map[string]interface{}
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("failed to parse event JSON: %v", err)
		}
		if event["type"] != "PATROL_WAKE" {
			t.Errorf("type = %v, want PATROL_WAKE", event["type"])
		}

		payload, ok := event["payload"].(map[string]interface{})
		if !ok {
			t.Fatalf("payload is not a map: %T", event["payload"])
		}
		if len(payload) != 0 {
			t.Errorf("expected empty payload, got %v", payload)
		}
	})

	t.Run("multiple events unique paths", func(t *testing.T) {
		townRoot := t.TempDir()
		paths := make(map[string]bool)
		for i := 0; i < 5; i++ {
			path, err := channelevents.EmitToRig(townRoot, testEmitRig, "test-channel", "TEST", nil)
			if err != nil {
				t.Fatalf("EmitEvent failed on iteration %d: %v", i, err)
			}
			if paths[path] {
				t.Errorf("duplicate path on iteration %d: %s", i, path)
			}
			paths[path] = true
		}
	})

	t.Run("malformed payload pair ignored", func(t *testing.T) {
		townRoot := t.TempDir()
		path, err := channelevents.EmitToRig(townRoot, testEmitRig, "test-channel", "TEST", []string{"valid=yes", "no-equals-sign"})
		if err != nil {
			t.Fatalf("EmitEvent failed: %v", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read event file: %v", err)
		}

		var event map[string]interface{}
		json.Unmarshal(data, &event)
		payload := event["payload"].(map[string]interface{})
		if payload["valid"] != "yes" {
			t.Errorf("expected payload.valid=yes, got %v", payload["valid"])
		}
		// "no-equals-sign" has no = so strings.Cut returns found=false, skipped
		if _, exists := payload["no-equals-sign"]; exists {
			t.Error("malformed pair should not be in payload")
		}
	})
}

func TestEmitEventChannelValidation(t *testing.T) {
	allowTestEmit(t)
	townRoot := t.TempDir()

	// Channel names become path segments, so they are validated before use.
	// EmitToRig is the rig-scoped entry point; EmitToTown would reject all of
	// these on scope alone and never reach the name check.

	// Valid channel name should succeed
	_, err := channelevents.EmitToRig(townRoot, testEmitRig, "valid-channel", "TEST", nil)
	if err != nil {
		t.Errorf("valid channel name rejected: %v", err)
	}

	// Path traversal should be rejected
	_, err = channelevents.EmitToRig(townRoot, testEmitRig, "../etc", "TEST", nil)
	if err == nil {
		t.Error("expected error for path traversal channel name, got nil")
	}

	// Slash in channel should be rejected
	_, err = channelevents.EmitToRig(townRoot, testEmitRig, "foo/bar", "TEST", nil)
	if err == nil {
		t.Error("expected error for channel with slash, got nil")
	}

	// Empty channel should be rejected
	_, err = channelevents.EmitToRig(townRoot, testEmitRig, "", "TEST", nil)
	if err == nil {
		t.Error("expected error for empty channel name, got nil")
	}

	// A rig name is a path segment too, and gets the same treatment.
	_, err = channelevents.EmitToRig(townRoot, "../etc", "valid-channel", "TEST", nil)
	if err == nil {
		t.Error("expected error for path traversal rig name, got nil")
	}

	// A rig-scoped channel with no rig must fail rather than silently falling
	// back to the flat path — that fallback is the collision being prevented.
	_, err = channelevents.EmitToRig(townRoot, "", "valid-channel", "TEST", nil)
	if err == nil {
		t.Error("expected error for rig-scoped channel with empty rig, got nil")
	}
}

func TestEmitEventPIDInFilename(t *testing.T) {
	allowTestEmit(t)
	townRoot := t.TempDir()
	path, err := channelevents.EmitToRig(townRoot, testEmitRig, "test-channel", "TEST", nil)
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	// Filename should contain PID for uniqueness: <nanoseconds>-<seq>-<pid>.event
	base := filepath.Base(path)
	if !strings.Contains(base, "-") {
		t.Errorf("filename %q should contain separator '-'", base)
	}
	parts := strings.Split(strings.TrimSuffix(base, ".event"), "-")
	if len(parts) != 3 {
		t.Errorf("filename %q should be <nanos>-<seq>-<pid>.event, got %d parts", base, len(parts))
	}
}

func TestEmitEventResult(t *testing.T) {
	result := EmitEventResult{
		Path:    "/home/gt/events/refinery/12345.event",
		Channel: "refinery",
		Type:    "MERGE_READY",
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded EmitEventResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if decoded.Path != result.Path {
		t.Errorf("path = %q, want %q", decoded.Path, result.Path)
	}
	if decoded.Channel != result.Channel {
		t.Errorf("channel = %q, want %q", decoded.Channel, result.Channel)
	}
	if decoded.Type != result.Type {
		t.Errorf("type = %q, want %q", decoded.Type, result.Type)
	}
}
