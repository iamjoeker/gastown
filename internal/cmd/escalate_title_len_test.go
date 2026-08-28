package cmd

import (
	"strings"
	"testing"
)

// The positional description of `gt escalate` becomes a bead TITLE, and titles
// are capped. Before gt-khq8 the cap was discovered only by bd rejecting the
// create, and gt reported that rejection by printing the whole failing argv —
// so a 2437-character escalation was echoed back in full with the real
// diagnostic past the end of it, and read as a command that had succeeded.

func TestCheckEscalationTitleLenAcceptsAnOrdinaryDescription(t *testing.T) {
	if err := checkEscalationTitleLen("high", "Deacon wedged on await-signal"); err != nil {
		t.Fatalf("ordinary description rejected: %v", err)
	}
}

func TestCheckEscalationTitleLenRejectsAnOverLongDescription(t *testing.T) {
	description := strings.Repeat("A", 2437)

	err := checkEscalationTitleLen("high", description)
	if err == nil {
		t.Fatal("2437-character description accepted; nothing would be filed and the command would say so only via bd")
	}

	msg := err.Error()
	// The operator has to learn three things, and the old failure taught none of
	// them legibly: that nothing was filed, what the cap is, and where the detail
	// belongs instead.
	for _, want := range []string{"NOT filed", "Nothing was recorded", "500", "--reason"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error is missing %q: %s", want, msg)
		}
	}

	// ...and it must not reproduce the thing that made the old failure
	// unreadable. A 60-rune preview identifies the message; 2437 characters of
	// it buries the diagnostic exactly as before.
	if strings.Contains(msg, description) {
		t.Error("error echoes the entire over-long description back at the operator")
	}
	if len(msg) > 600 {
		t.Errorf("error is %d bytes; it has to stay readable in a terminal", len(msg))
	}
}

// The record's title is the bare description, but every delivered copy carries
// "[SEVERITY] " in front of it. A description just under the cap therefore used
// to file the record and then fail every copy — an ephemeral wisp nobody was
// told about, which is GC'd unread.
func TestCheckEscalationTitleLenCountsTheSeverityPrefix(t *testing.T) {
	// 498 runes: under the cap alone, over it once "[CRITICAL] " is prepended.
	description := strings.Repeat("B", 498)

	if err := checkEscalationTitleLen("critical", description); err == nil {
		t.Fatal("description that only fits without the severity prefix was accepted")
	}

	// The same length is fine at a severity whose prefix is short enough.
	if err := checkEscalationTitleLen("low", strings.Repeat("B", 480)); err != nil {
		t.Fatalf("480-rune description at severity low rejected: %v", err)
	}
}

// Length is counted in characters, not bytes, because that is the unit the cap
// is expressed in and the unit the error quotes back.
func TestCheckEscalationTitleLenCountsRunesNotBytes(t *testing.T) {
	// 480 three-byte runes is 1440 bytes and 480 characters.
	if err := checkEscalationTitleLen("low", strings.Repeat("あ", 480)); err != nil {
		t.Fatalf("480-character multibyte description rejected: %v", err)
	}
}

func TestEscalationTitlePreviewIsShortSingleLineAndRuneSafe(t *testing.T) {
	preview := escalationTitlePreview(strings.Repeat("あ", 200))
	if strings.Contains(preview, "�") {
		t.Errorf("preview split a multibyte rune: %q", preview)
	}
	if got := len([]rune(preview)); got != 61 { // 60 runes + the ellipsis
		t.Errorf("preview is %d runes, want 61", got)
	}

	if got := escalationTitlePreview("first line\nsecond line"); got != "first line" {
		t.Errorf("preview did not stop at the newline: %q", got)
	}
}

// The subject a check measures has to be the subject the send builds, or the
// two drift and the guard stops guarding the thing it was written for.
func TestEscalationSubjectMatchesWhatTheCheckMeasures(t *testing.T) {
	if got, want := escalationSubject("high", "Build failing"), "[HIGH] Build failing"; got != want {
		t.Errorf("escalationSubject() = %q, want %q", got, want)
	}
}
