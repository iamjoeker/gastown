package mail

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestIsCCOnly(t *testing.T) {
	tests := []struct {
		name     string
		identity string
		msg      *Message
		want     bool
	}{
		{
			name:     "addressed to me",
			identity: "gastown/witness",
			msg:      &Message{To: "gastown/witness"},
			want:     false,
		},
		{
			name:     "cc to me, addressed elsewhere",
			identity: "gastown/witness",
			msg:      &Message{To: "beads/refinery", CC: []string{"gastown/witness", "deacon/"}},
			want:     true,
		},
		{
			// Being both is being addressed: the assignee owns the record and
			// clears it by closing it, so a dismissal must not apply.
			name:     "addressed and cc'd",
			identity: "gastown/witness",
			msg:      &Message{To: "gastown/witness", CC: []string{"gastown/witness"}},
			want:     false,
		},
		{
			name:     "cc to someone else",
			identity: "gastown/witness",
			msg:      &Message{To: "beads/refinery", CC: []string{"deacon/"}},
			want:     false,
		},
		{
			name:     "no cc at all",
			identity: "gastown/witness",
			msg:      &Message{To: "beads/refinery"},
			want:     false,
		},
		{
			// mayor/ and mayor are two spellings of one principal; a cc must
			// resolve through the same variants the inbox query uses.
			name:     "legacy identity variant",
			identity: "mayor/",
			msg:      &Message{To: "deacon/", CC: []string{"mayor"}},
			want:     true,
		},
		{
			name:     "nil message",
			identity: "gastown/witness",
			msg:      nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMailboxWithBeadsDir(tt.identity, t.TempDir(), t.TempDir())
			if got := m.IsCCOnly(tt.msg); got != tt.want {
				t.Fatalf("IsCCOnly = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCCClearedLabel(t *testing.T) {
	if got := CCClearedLabel("gastown/witness"); got != "cc-cleared:gastown/witness" {
		t.Fatalf("CCClearedLabel = %q", got)
	}
}

func TestFilterClearedCCDropsOnlyDismissedCopies(t *testing.T) {
	msgs := []BeadsMessage{
		{ID: "kept", Labels: []string{"gt:message", "cc:gastown/witness"}},
		{ID: "cleared", Labels: []string{"gt:message", "cc:gastown/witness", "cc-cleared:gastown/witness"}},
		{ID: "cleared-by-other", Labels: []string{"gt:message", "cc:gastown/witness", "cc-cleared:deacon/"}},
	}

	kept := filterClearedCC(msgs, []string{"gastown/witness"})

	var ids []string
	for _, bm := range kept {
		ids = append(ids, bm.ID)
	}
	want := []string{"kept", "cleared-by-other"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("kept = %v, want %v", ids, want)
	}
}

func TestFilterClearedCCMatchesIdentityVariants(t *testing.T) {
	msgs := []BeadsMessage{
		{ID: "legacy-cleared", Labels: []string{"gt:message", "cc:mayor", "cc-cleared:mayor"}},
	}
	if kept := filterClearedCC(msgs, []string{"mayor/", "mayor"}); len(kept) != 0 {
		t.Fatalf("kept %d messages, want 0 (legacy variant dismissal must count)", len(kept))
	}
}

func TestIsOwnershipRefusal(t *testing.T) {
	// The exact refusal bd emits (bd 1.2.2, cmd close): matching is textual
	// because the message crosses the subprocess boundary untyped.
	refusal := errors.New(`cannot close hq-wisp-abc: assignee is "beads/refinery", actor is "beads/witness"; reclaim or use --force to override`)
	if !IsOwnershipRefusal(refusal) {
		t.Fatal("ownership refusal not recognized")
	}
	if IsOwnershipRefusal(nil) {
		t.Fatal("nil should not be an ownership refusal")
	}
	if IsOwnershipRefusal(ErrMessageNotFound) {
		t.Fatal("not-found should not be an ownership refusal")
	}
}

// fakeBDMailbox installs a fake bd on PATH that logs its arguments and serves
// canned inbox/show responses, then returns a mailbox wired to it.
func fakeBDMailbox(t *testing.T, identity, script string) (*Mailbox, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake bd is POSIX-only")
	}

	beadsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(beadsDir, ".gt-types-configured"),
		[]byte(beads.TypeConfigSentinelValue()+"\n"), 0644); err != nil {
		t.Fatalf("write types sentinel: %v", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd.log")
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)

	return NewMailboxWithBeadsDir(identity, t.TempDir(), beadsDir), beadsDir, logPath
}

func readBDLog(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	return string(data)
}

// TestListFromDirHidesDismissedCCCopies is the count-drift regression: once a
// CC'd recipient clears its copy, the copy must stay out of the inbox, or the
// count climbs forever with no way down (gt-58s).
func TestListFromDirHidesDismissedCCCopies(t *testing.T) {
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
if [ "$1" = "list" ]; then
  case "$*" in
    *"--assignee gastown/witness"*)
      printf '%s\n' '[{"id":"issue-mine","title":"Mine","description":"","status":"open","priority":2,"assignee":"gastown/witness","created_at":"2026-08-18T01:00:00Z","labels":["gt:message","from:mayor/","cc-cleared:gastown/witness"]}]'
      exit 0
      ;;
    *"--label-any cc:gastown/witness"*)
      printf '%s\n' '[{"id":"issue-cc-live","title":"CC live","description":"","status":"open","priority":2,"assignee":"beads/refinery","created_at":"2026-08-18T00:59:00Z","labels":["gt:message","from:mayor/","cc:gastown/witness"]},{"id":"issue-cc-cleared","title":"CC cleared","description":"","status":"open","priority":2,"assignee":"beads/refinery","created_at":"2026-08-18T00:58:00Z","labels":["gt:message","from:mayor/","cc:gastown/witness","cc-cleared:gastown/witness"]}]'
      exit 0
      ;;
  esac
  printf '%s\n' 'No issues found.'
  exit 0
fi
if [ "$1" = "sql" ]; then
  printf '%s\n' '[{"id":"wisp-cc-live","title":"Wisp CC live","description":"","status":"open","priority":2,"assignee":"beads/refinery","created_at":"2026-08-18T00:57:00Z","updated_at":"2026-08-18T00:57:00Z","labels_csv":"gt:message,cc:gastown/witness,from:mayor/","assignee_match":0,"cc_match":1},{"id":"wisp-cc-cleared","title":"Wisp CC cleared","description":"","status":"open","priority":2,"assignee":"beads/refinery","created_at":"2026-08-18T00:56:00Z","updated_at":"2026-08-18T00:56:00Z","labels_csv":"gt:message,cc:gastown/witness,cc-cleared:gastown/witness,from:mayor/","assignee_match":0,"cc_match":1}]'
  exit 0
fi
printf 'unexpected bd args: %s\n' "$*" >&2
exit 1
`
	m, beadsDir, _ := fakeBDMailbox(t, "gastown/witness", script)

	msgs, err := m.listFromDir(beadsDir)
	if err != nil {
		t.Fatalf("listFromDir: %v", err)
	}

	present := make(map[string]bool, len(msgs))
	for _, msg := range msgs {
		present[msg.ID] = true
	}
	for _, id := range []string{"issue-cc-live", "wisp-cc-live"} {
		if !present[id] {
			t.Fatalf("live cc copy %s missing from inbox: %v", id, present)
		}
	}
	for _, id := range []string{"issue-cc-cleared", "wisp-cc-cleared"} {
		if present[id] {
			t.Fatalf("dismissed cc copy %s still in inbox", id)
		}
	}
	// A dismissal label must never hide work this identity actually owns.
	if !present["issue-mine"] {
		t.Fatal("addressed message hidden by a stale cc-cleared label")
	}
}

// TestDeleteCCCopyDismissesWithoutClosing is the core of gt-58s: clearing a CC
// copy must not attempt to close a bead assigned to someone else, because the
// ownership guard refuses that and the CC'd party is then stuck forever.
func TestDeleteCCCopyDismissesWithoutClosing(t *testing.T) {
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
if [ "$1" = "show" ]; then
  printf '%s\n' '[{"id":"hq-wisp-cc","title":"Clearance","description":"","status":"open","priority":2,"assignee":"beads/refinery","created_at":"2026-08-18T01:31:00Z","labels":["gt:message","from:mayor/","cc:gastown/witness"]}]'
  exit 0
fi
if [ "$1" = "label" ]; then
  exit 0
fi
if [ "$1" = "close" ]; then
  printf 'cannot close hq-wisp-cc: assignee is "beads/refinery", actor is "gastown/witness"; reclaim or use --force to override\n' >&2
  exit 1
fi
printf 'unexpected bd args: %s\n' "$*" >&2
exit 1
`
	m, _, logPath := fakeBDMailbox(t, "gastown/witness", script)

	result, err := m.DeleteWithResult("hq-wisp-cc")
	if err != nil {
		t.Fatalf("DeleteWithResult on cc copy: %v", err)
	}
	if result != DeleteCCCleared {
		t.Fatalf("result = %v, want DeleteCCCleared", result)
	}

	log := readBDLog(t, logPath)
	if !strings.Contains(log, "label add hq-wisp-cc cc-cleared:gastown/witness") {
		t.Fatalf("dismissal label not added; bd log:\n%s", log)
	}
	if strings.Contains(log, "close ") {
		t.Fatalf("cc dismissal must not close the assignee's bead; bd log:\n%s", log)
	}
}

// TestDeleteAddressedMessageStillCloses guards the other direction: the CC
// branch must not divert messages this mailbox actually owns.
func TestDeleteAddressedMessageStillCloses(t *testing.T) {
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
if [ "$1" = "show" ]; then
  printf '%s\n' '[{"id":"hq-wisp-mine","title":"Mine","description":"","status":"open","priority":2,"assignee":"gastown/witness","created_at":"2026-08-18T01:31:00Z","labels":["gt:message","from:mayor/"]}]'
  exit 0
fi
if [ "$1" = "close" ]; then
  exit 0
fi
if [ "$1" = "label" ]; then
  exit 0
fi
printf 'unexpected bd args: %s\n' "$*" >&2
exit 1
`
	m, _, logPath := fakeBDMailbox(t, "gastown/witness", script)

	result, err := m.DeleteWithResult("hq-wisp-mine")
	if err != nil {
		t.Fatalf("DeleteWithResult on addressed message: %v", err)
	}
	if result != DeleteClosed {
		t.Fatalf("result = %v, want DeleteClosed", result)
	}

	log := readBDLog(t, logPath)
	if !strings.Contains(log, "close hq-wisp-mine") {
		t.Fatalf("addressed message was not closed; bd log:\n%s", log)
	}
	if strings.Contains(log, "cc-cleared") {
		t.Fatalf("addressed message must not be dismissed as a cc; bd log:\n%s", log)
	}
}

// TestDeleteCCCopyWithForceStillCloses keeps --force meaning what it says: an
// operator who explicitly asks to close the record is not diverted to a
// dismissal.
func TestDeleteCCCopyWithForceStillCloses(t *testing.T) {
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
if [ "$1" = "show" ]; then
  printf '%s\n' '[{"id":"hq-wisp-cc","title":"Clearance","description":"","status":"open","priority":2,"assignee":"beads/refinery","created_at":"2026-08-18T01:31:00Z","labels":["gt:message","from:mayor/","cc:gastown/witness"]}]'
  exit 0
fi
if [ "$1" = "close" ] || [ "$1" = "label" ]; then
  exit 0
fi
printf 'unexpected bd args: %s\n' "$*" >&2
exit 1
`
	m, _, logPath := fakeBDMailbox(t, "gastown/witness", script)
	m.SetForceClose(true)

	result, err := m.DeleteWithResult("hq-wisp-cc")
	if err != nil {
		t.Fatalf("DeleteWithResult with --force: %v", err)
	}
	if result != DeleteClosed {
		t.Fatalf("result = %v, want DeleteClosed", result)
	}
	log := readBDLog(t, logPath)
	if !strings.Contains(log, "close hq-wisp-cc") || !strings.Contains(log, "--force") {
		t.Fatalf("--force should reach bd close; bd log:\n%s", log)
	}
}

// TestDeleteGCdBeadStillReportsNotFound guards the aa-6hv contract: archive
// treats a GC'd bead as already cleared, which depends on ErrMessageNotFound
// surviving the CC lookup.
func TestDeleteGCdBeadStillReportsNotFound(t *testing.T) {
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
if [ "$1" = "show" ]; then
  printf 'issue not found: hq-wisp-gone\n' >&2
  exit 1
fi
printf 'unexpected bd args: %s\n' "$*" >&2
exit 1
`
	m, _, _ := fakeBDMailbox(t, "gastown/witness", script)

	if _, err := m.DeleteWithResult("hq-wisp-gone"); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("err = %v, want ErrMessageNotFound", err)
	}
}
