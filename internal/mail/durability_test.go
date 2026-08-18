package mail

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// bdCreateRecorder stands up a minimal town, stubs bd so `bd create` appends its
// argv to a log file, and returns a router plus a reader for that log.
//
// The stub is deliberately dumb: it records and succeeds. What the tests assert
// is the argv the router builds, because `--ephemeral` is the single bit that
// decides whether a message lands in the age-GC-reclaimable wisps table or in
// the durable issues table.
func bdCreateRecorder(t *testing.T) (*Router, func(t *testing.T) []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses a bash bd stub")
	}

	tmpDir := t.TempDir()
	townRoot := filepath.Join(tmpDir, "town")
	senderDir := filepath.Join(townRoot, "barnaby", "crew", "tom")
	recipientDir := filepath.Join(townRoot, "barnaby", "crew", "troy")
	mayorDir := filepath.Join(townRoot, "mayor")
	townBeadsDir := filepath.Join(townRoot, ".beads")

	for _, dir := range []string{senderDir, recipientDir, mayorDir, townBeadsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("write beads.db: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	// Sentinel so beads.EnsureCustomTypes skips its bd config calls.
	if err := os.WriteFile(filepath.Join(townBeadsDir, ".gt-types-configured"), []byte(beads.TypeConfigSentinelValue()+"\n"), 0644); err != nil {
		t.Fatalf("write types sentinel: %v", err)
	}

	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath := filepath.Join(tmpDir, "bd-create.log")
	script := `#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "config" || "${1:-}" == "init" ]]; then
  exit 0
fi

if [[ "${1:-}" == "list" ]]; then
  echo "[]"
  exit 0
fi

if [[ "${1:-}" == "mol" && "${2:-}" == "wisp" && "${3:-}" == "list" ]]; then
  echo "[]"
  exit 0
fi

if [[ "${1:-}" == "create" ]]; then
  printf '%s\n' "$*" >> "` + logPath + `"
  echo "hq-testmail-1"
  exit 0
fi

echo "unsupported bd args: $*" >&2
exit 1
`
	bdStub := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdStub, []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	readLog := func(t *testing.T) []string {
		t.Helper()
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read bd create log: %v", err)
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}

	return NewRouter(senderDir), readLog
}

// TestSendStoresOrdinaryMailDurably is the acceptance test for gt-jbn.
//
// Ordinary mail must NOT be created with --ephemeral. An ephemeral bead lives in
// the wisps table, where `bd mol wisp gc --age` reclaims it — and unread mail
// ages fastest, because nothing ever touches its updated_at. That made the
// channel agents are told to use for anything that must survive session death
// the channel most likely to lose it, silently, in both directions.
//
// The lifecycle case is the negative control: protocol chatter is still stored
// ephemerally, so a router that had simply stopped emitting --ephemeral at all
// fails here.
func TestSendStoresOrdinaryMailDurably(t *testing.T) {
	tests := []struct {
		name          string
		msg           *Message
		wantEphemeral bool
	}{
		{
			name:          "ordinary mail is durable",
			msg:           &Message{Subject: "HELP: auth bug", Body: "Stuck on token refresh"},
			wantEphemeral: false,
		},
		{
			name:          "handoff is durable",
			msg:           &Message{Subject: "HANDOFF: context notes", Body: "Where I left off"},
			wantEphemeral: false,
		},
		{
			name:          "explicit --wisp is honoured",
			msg:           &Message{Subject: "Routine ping", Body: "still here", Wisp: true},
			wantEphemeral: true,
		},
		{
			name:          "lifecycle protocol traffic stays ephemeral",
			msg:           &Message{Subject: "MERGE_READY nux", Body: "branch pushed"},
			wantEphemeral: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, readLog := bdCreateRecorder(t)

			msg := *tt.msg
			msg.From = "barnaby/crew/tom"
			msg.To = "barnaby/troy"
			msg.SuppressNotify = true

			if err := r.Send(&msg); err != nil {
				t.Fatalf("Send: %v", err)
			}

			lines := readLog(t)
			if len(lines) != 1 {
				t.Fatalf("bd create invocations = %d, want 1: %v", len(lines), lines)
			}
			gotEphemeral := strings.Contains(lines[0], "--ephemeral")
			if gotEphemeral != tt.wantEphemeral {
				t.Errorf("bd create --ephemeral = %v, want %v\nargv: %s",
					gotEphemeral, tt.wantEphemeral, lines[0])
			}
		})
	}
}
