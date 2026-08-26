package mail

import (
	"errors"
	"strings"
	"testing"
)

// The gt-khq8 regression: `gt mail archive <id>` printed "✓ Archived 1 of 1
// message" and exited 0 for a message that was still in the inbox unread a
// cycle later with its wisp status=open.
//
// Success was read off the RETURN of the close and never off the mailbox, so
// every way a close can be acknowledged without taking effect — a write routed
// to a different store than the read, an ownership guard answered leniently, a
// wisp row an issues-table update never touched — was indistinguishable from
// success on the only surface the caller consults.
//
// The fake below is a bd that says "✓ Closed" and changes nothing. That is the
// whole defect, expressed in five lines.
func TestCloseThatDoesNotTakeIsNotReportedAsSuccess(t *testing.T) {
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
if [ "$1" = "show" ]; then
  printf '%s\n' '[{"id":"hq-wisp-noop","title":"Still here","description":"","status":"open","priority":2,"assignee":"gastown/witness","created_at":"2026-08-24T01:31:00Z","labels":["gt:message","from:mayor/"]}]'
  exit 0
fi
if [ "$1" = "close" ]; then
  printf '%s\n' "Closed hq-wisp-noop"
  exit 0
fi
if [ "$1" = "label" ]; then
  exit 0
fi
printf 'unexpected bd args: %s\n' "$*" >&2
exit 1
`
	m, _, logPath := fakeBDMailbox(t, "gastown/witness", script)

	_, err := m.DeleteWithResult("hq-wisp-noop")
	if err == nil {
		t.Fatal("a close that changed nothing was reported as success — this is the gt-khq8 defect")
	}
	if !errors.Is(err, ErrCloseNotApplied) {
		t.Fatalf("error is not ErrCloseNotApplied: %v", err)
	}
	// The message has to name the case, not merely fail: the operator's next
	// move depends on knowing the bead is still open rather than missing.
	for _, want := range []string{"hq-wisp-noop", "open"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q: %v", want, err)
		}
	}

	// The confirmation must be a SECOND observation, not the close's own word
	// for it — so bd is asked again after the close.
	log := readBDLog(t, logPath)
	closeAt := strings.Index(log, "close hq-wisp-noop")
	if closeAt < 0 {
		t.Fatalf("close never ran; bd log:\n%s", log)
	}
	if !strings.Contains(log[closeAt:], "show hq-wisp-noop") {
		t.Fatalf("no re-read after the close; bd log:\n%s", log)
	}
}

// The positive control for the test above: the same shape, with a bd whose
// close actually applies, must still succeed. Without this the check above
// passes for a mailbox that refuses everything.
func TestCloseThatTakesIsStillReportedAsSuccess(t *testing.T) {
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
if [ "$1" = "show" ]; then
  if [ -f "$BD_LOG.closed" ]; then st=closed; else st=open; fi
  printf '[{"id":"hq-wisp-real","title":"Goes away","description":"","status":"%s","priority":2,"assignee":"gastown/witness","created_at":"2026-08-24T01:31:00Z","labels":["gt:message","from:mayor/"]}]\n' "$st"
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
	m, _, _ := fakeBDMailbox(t, "gastown/witness", script)

	result, err := m.DeleteWithResult("hq-wisp-real")
	if err != nil {
		t.Fatalf("close that took was reported as a failure: %v", err)
	}
	if result != DeleteClosed {
		t.Fatalf("result = %v, want DeleteClosed", result)
	}
}

// The check fails OPEN. A bead that has vanished between the close and the
// re-read is the outcome the caller wanted, and a store that cannot answer at
// all is unproven rather than failed — turning either into an archive error
// would trade one wrong answer for another.
func TestCloseVerificationFailsOpenWhenTheReReadCannotAnswer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after string
	}{
		{"bead gone", `printf 'issue not found\n' >&2; exit 1`},
		{"store unreadable", `printf 'dial tcp 127.0.0.1:3307: connection refused\n' >&2; exit 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
if [ "$1" = "show" ]; then
  if [ -f "$BD_LOG.closed" ]; then
    ` + tc.after + `
  fi
  printf '%s\n' '[{"id":"hq-wisp-gone","title":"Vanishes","description":"","status":"open","priority":2,"assignee":"gastown/witness","created_at":"2026-08-24T01:31:00Z","labels":["gt:message","from:mayor/"]}]'
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
			m, _, _ := fakeBDMailbox(t, "gastown/witness", script)

			if _, err := m.DeleteWithResult("hq-wisp-gone"); err != nil {
				t.Fatalf("unreadable re-read turned a successful close into a failure: %v", err)
			}
		})
	}
}

// MarkRead closes the same bead through the same choke point, so it inherits
// the confirmation. A mark-read that leaves the message unread is the same
// silent no-op wearing a different command name.
func TestMarkReadThatDoesNotTakeIsNotReportedAsSuccess(t *testing.T) {
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
if [ "$1" = "show" ]; then
  printf '%s\n' '[{"id":"hq-wisp-unread","title":"Unread","description":"","status":"open","priority":2,"assignee":"gastown/witness","created_at":"2026-08-24T01:31:00Z","labels":["gt:message","from:mayor/"]}]'
  exit 0
fi
if [ "$1" = "close" ] || [ "$1" = "label" ]; then
  exit 0
fi
printf 'unexpected bd args: %s\n' "$*" >&2
exit 1
`
	m, _, _ := fakeBDMailbox(t, "gastown/witness", script)

	if err := m.MarkRead("hq-wisp-unread"); !errors.Is(err, ErrCloseNotApplied) {
		t.Fatalf("MarkRead err = %v, want ErrCloseNotApplied", err)
	}
}

// The inbox lists "open" and "hooked" and nothing else, so those are the two
// statuses a close must have moved away from. Every other status is treated as
// gone, so a status this code has not met cannot manufacture a failure.
func TestIsClosedStatus(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{"open", false},
		{"OPEN", false},
		{" open ", false},
		{"hooked", false},
		{"closed", true},
		{"Closed", true},
		{"deferred", true},
		{"blocked", true},
		{"something-new", true},
	} {
		if got := isClosedStatus(tc.status); got != tc.want {
			t.Errorf("isClosedStatus(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}
