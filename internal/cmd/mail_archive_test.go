package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/mail"
)

func TestStaleMessagesForSession(t *testing.T) {
	sessionStart := time.Date(2026, 1, 24, 2, 0, 0, 0, time.UTC)
	messages := []*mail.Message{
		{ID: "msg-1", Subject: "Older", Timestamp: sessionStart.Add(-2 * time.Minute)},
		{ID: "msg-2", Subject: "Newer", Timestamp: sessionStart.Add(2 * time.Minute)},
		{ID: "msg-3", Subject: "Equal", Timestamp: sessionStart},
	}

	stale := staleMessagesForSession(messages, sessionStart)
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale message, got %d", len(stale))
	}
	if stale[0].Message.ID != "msg-1" {
		t.Fatalf("expected msg-1 stale, got %s", stale[0].Message.ID)
	}
}

// --- Joined message IDs must not be answered with a GC note (gt-f0b3) --------
//
// `gt mail archive` documents a multi-ID form, one argument per ID. A caller
// that joined them into ONE argument got no error: mailbox.Get returns
// ErrMessageNotFound for the joined string, archive reads that as "the
// underlying bead was already GC'd", prints "✓ Message archived" and exits 0.
// Measured: 514 archives submitted as 13 joined batches reported 13 successes
// and moved the inbox by 2; the same IDs one per invocation moved it by 512.

func TestValidateMessageIDArgs_RejectsJoinedIDs(t *testing.T) {
	err := validateMessageIDArgs([]string{"hq-wgjhz hq-9c30h hq-d318a"})
	if err == nil {
		t.Fatal("joined IDs must be rejected: a not-found identifier is only evidence of a GC'd bead if it could have been an ID")
	}
	// The error has to be actionable, or the caller repeats the batch verbatim.
	for _, want := range []string{"3 IDs", "separate arguments", "hq-wgjhz hq-9c30h hq-d318a"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must contain %q, got: %v", want, err)
		}
	}
}

// The reported invocation carried a trailing space ("hq-d318a : underlying" in
// its output). A padded ID fails lookup exactly like a joined one.
func TestValidateMessageIDArgs_RejectsPaddedID(t *testing.T) {
	err := validateMessageIDArgs([]string{"hq-d318a "})
	if err == nil {
		t.Fatal("an ID with surrounding whitespace must be rejected, not looked up and reported GC'd")
	}
	if !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("error must name the whitespace, got: %v", err)
	}
}

func TestValidateMessageIDArgs_RejectsEmpty(t *testing.T) {
	if err := validateMessageIDArgs([]string{"hq-abc123", "   "}); err == nil {
		t.Fatal("an empty argument must be rejected")
	}
}

// The documented form itself must keep working — the guard is worthless if it
// also rejects what the help text tells people to type.
func TestValidateMessageIDArgs_AcceptsTheDocumentedForm(t *testing.T) {
	if err := validateMessageIDArgs([]string{"hq-abc123", "hq-def456", "hq-ghi789"}); err != nil {
		t.Fatalf("separate ID arguments are the documented form: %v", err)
	}
	if err := validateMessageIDArgs(nil); err != nil {
		t.Fatalf("no arguments is --stale/--all territory, not this guard's: %v", err)
	}
}
