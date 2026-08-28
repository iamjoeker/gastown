package mail

import (
	"strings"
	"testing"
)

// TestCloseActsUnderCanonicalIdentity is the gt-n3gj regression.
//
// Delivery assigns every mail bead the canonical identity ("gastown/toast"),
// but a polecat's ambient BD_ACTOR is its role path ("gastown/polecats/toast").
// bd's ownership guard compares the two, so before this fix a polecat could not
// archive its own mail at all — the message stayed in the inbox forever unless
// the agent reached for --force, which is spelled the same as closing someone
// else's mail. The fake bd below enforces the same guard bd does.
func TestCloseActsUnderCanonicalIdentity(t *testing.T) {
	// A successful close is APPLIED here, not merely acknowledged: closeMessage
	// re-reads the bead to confirm the close took, so a fake whose show always
	// answers "open" is modelling the gt-khq8 defect rather than bd.
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
if [ "$1" = "show" ]; then
  if [ -f "$BD_LOG.closed" ]; then st=closed; else st=open; fi
  printf '[{"id":"hq-msg-mine","title":"Mine","description":"","status":"%s","priority":2,"assignee":"gastown/toast","created_at":"2026-08-19T01:31:00Z","labels":["gt:message","from:gastown/witness"]}]\n' "$st"
  exit 0
fi
if [ "$1" = "label" ]; then
  exit 0
fi
if [ "$1" = "close" ]; then
  case "$*" in
    *--actor=gastown/toast*) : > "$BD_LOG.closed"; exit 0 ;;
  esac
  printf 'cannot close hq-msg-mine: assignee is "gastown/toast", actor is "gastown/polecats/toast"; reclaim or use --force to override\n' >&2
  exit 1
fi
printf 'unexpected bd args: %s\n' "$*" >&2
exit 1
`
	// The address a polecat detects for itself is its role path; the mailbox
	// canonicalizes it, and the close must act under that canonical form.
	m, _, logPath := fakeBDMailbox(t, "gastown/polecats/toast", script)

	result, err := m.DeleteWithResult("hq-msg-mine")
	if err != nil {
		t.Fatalf("polecat archiving its own mail: %v", err)
	}
	if result != DeleteClosed {
		t.Fatalf("result = %v, want DeleteClosed", result)
	}

	log := readBDLog(t, logPath)
	if !strings.Contains(log, "--actor=gastown/toast") {
		t.Fatalf("close did not carry the canonical actor; bd log:\n%s", log)
	}
	if strings.Contains(log, "--force") {
		t.Fatalf("archiving own mail must not need --force; bd log:\n%s", log)
	}
}

// TestCloseQueueMailboxKeepsAmbientActor guards the other direction: a queue is
// a shared destination, not an agent. Its beads are assigned "queue:<name>",
// which is not an actor, so the acting agent must stay in the audit trail.
func TestCloseQueueMailboxKeepsAmbientActor(t *testing.T) {
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
if [ "$1" = "show" ]; then
  if [ -f "$BD_LOG.closed" ]; then st=closed; else st=open; fi
  printf '[{"id":"hq-msg-queued","title":"Queued","description":"","status":"%s","priority":2,"assignee":"queue:builds","created_at":"2026-08-19T01:31:00Z","labels":["gt:message","from:gastown/witness"]}]\n' "$st"
  exit 0
fi
if [ "$1" = "close" ]; then
  : > "$BD_LOG.closed"
  exit 0
fi
if [ "$1" = "label" ]; then
  exit 0
fi
printf 'unexpected bd args: %s\n' "$*" >&2
exit 1
`
	m, _, logPath := fakeBDMailbox(t, "queue:builds", script)

	if _, err := m.DeleteWithResult("hq-msg-queued"); err != nil {
		t.Fatalf("DeleteWithResult on queue message: %v", err)
	}

	log := readBDLog(t, logPath)
	if !strings.Contains(log, "close hq-msg-queued") {
		t.Fatalf("queue message was not closed; bd log:\n%s", log)
	}
	if strings.Contains(log, "--actor") {
		t.Fatalf("queue mailbox must not override the acting agent; bd log:\n%s", log)
	}
}

func TestActorForIdentity(t *testing.T) {
	tests := []struct {
		name     string
		identity string
		want     string
	}{
		{"polecat role path", "gastown/polecats/toast", "gastown/toast"},
		{"crew role path", "gastown/crew/max", "gastown/max"},
		{"already canonical", "gastown/toast", "gastown/toast"},
		{"rig singleton", "gastown/witness", "gastown/witness"},
		{"town singleton", "mayor", "mayor/"},
		{"overseer", "overseer", "overseer"},
		{"empty", "", ""},
		{"queue", "queue:builds", ""},
		{"channel", "channel:general", ""},
		{"announce", "announce:releases", ""},
		{"list", "list:everyone", ""},
		{"group", "group:leads", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := actorForIdentity(tt.identity); got != tt.want {
				t.Errorf("actorForIdentity(%q) = %q, want %q", tt.identity, got, tt.want)
			}
		})
	}
}
