package mail

import "testing"

// TestConsumedByReading pins the three layers of gt-qffl's predicate: the type
// must be STAMPED, the stamped type must say a read consumes it, and for
// "notification" — the value the old default put on everything — the subject
// must independently show the message is automated traffic.
func TestConsumedByReading(t *testing.T) {
	tests := []struct {
		name    string
		msg     *Message
		want    bool
		because string
	}{
		{
			name:    "automated notification",
			msg:     &Message{RawType: "notification", Subject: "Convoy complete: Dog timers over-emit"},
			want:    true,
			because: "225 of these on the hq store, 35 still open; nothing can be replied to",
		},
		{
			name:    "scheduler capacity notice",
			msg:     &Message{RawType: "notification", Subject: "SCHEDULER_OPEN: gastown/chrome completed (exit=completed)"},
			want:    true,
			because: "superseded by the next completion",
		},
		{
			name:    "merge receipt",
			msg:     &Message{RawType: "notification", Subject: "MERGED nux"},
			want:    true,
			because: "the refinery reporting, not instructing",
		},
		{
			name:    "agent-written notification",
			msg:     &Message{RawType: "notification", Subject: "Why was gt-wisp-0an54 rejected?"},
			want:    false,
			because: "gt-ac1l: a real question typed 'notification' by the old default",
		},
		{
			name:    "query keeps its subject from saving it",
			msg:     &Message{RawType: "query", Subject: "Convoy complete: something"},
			want:    false,
			because: "the type layer is not overridden by a machine-looking subject",
		},
		{
			name: "task",
			msg:  &Message{RawType: "task", Subject: "MERGE_READY chrome"},
			want: false,
		},
		{
			name: "scavenge",
			msg:  &Message{RawType: "scavenge", Subject: "MERGED nux"},
			want: false,
		},
		{
			name:    "escalation",
			msg:     &Message{RawType: "escalation", Subject: "MERGED nux"},
			want:    false,
			because: "escalations have their own ack surface; auto-closing loses it",
		},
		{
			name:    "reply needs no subject corroboration",
			msg:     &Message{RawType: "reply", Subject: "Re: your question about the reaper"},
			want:    true,
			because: "'reply' is set structurally by --reply-to, never typed by a sender",
		},
		{
			name:    "handoff needs no subject corroboration",
			msg:     &Message{RawType: "handoff", Subject: "Polecat work handoff"},
			want:    true,
			because: "'handoff' was unrepresentable before gt-do5c, so no backlog carries it",
		},
		{
			name:    "untyped with an automated subject",
			msg:     &Message{RawType: "", Subject: "Convoy complete: something"},
			want:    false,
			because: "an unstamped type is what a writer that forgot produces; fail closed",
		},
		{
			name:    "untyped POLECAT_DIED",
			msg:     &Message{RawType: "", Subject: "POLECAT_DIED: 1 polecat(s) died with active work in gastown"},
			want:    false,
			because: "measured: every POLECAT_DIED on hq is stamped msg-type: with an empty value, and its body carries resling instructions",
		},
		{
			name:    "unrecognised type",
			msg:     &Message{RawType: "urgent-thing-a-future-build-invents", Subject: "MERGED nux"},
			want:    false,
			because: "a value this build cannot interpret is never safe to auto-close",
		},
		{
			name: "nil message",
			msg:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConsumedByReading(tt.msg); got != tt.want {
				t.Errorf("ConsumedByReading() = %v, want %v (%s)", got, tt.want, tt.because)
			}
		})
	}
}

// TestConsumedByReadingIgnoresLenientType is the mutation check for the reason
// Message.RawType exists at all.
//
// Message.Type is the LENIENT reading: ToMessage runs the label through
// ParseMessageType, which rewrites an absent or unrecognised value to
// TypeNotification. So SafeToCloseOnRead's promise to fail closed on an unset
// type was unreachable through Type — by the time the predicate ran, "" had
// already become "notification". If ConsumedByReading is ever "simplified" to
// read msg.Type, this test fails and both of these close.
func TestConsumedByReadingIgnoresLenientType(t *testing.T) {
	untyped := &Message{Type: TypeNotification, RawType: "", Subject: "MERGED nux"}
	if ConsumedByReading(untyped) {
		t.Error("ConsumedByReading() = true for an unstamped type; it read Message.Type, which ParseMessageType had already rewritten to notification")
	}

	unknown := &Message{Type: TypeNotification, RawType: "bogus", Subject: "MERGED nux"}
	if ConsumedByReading(unknown) {
		t.Error("ConsumedByReading() = true for an unrecognised stamped type")
	}

	// Control: the same subject and a real stamp DOES close, so the two
	// failures above are attributable to the stamp and not to the subject
	// check rejecting "MERGED nux" for some unrelated reason.
	stamped := &Message{Type: TypeNotification, RawType: "notification", Subject: "MERGED nux"}
	if !ConsumedByReading(stamped) {
		t.Error("ConsumedByReading() = false for a properly stamped automated notification; the control does not fire, so this test cannot discriminate")
	}
}

// TestHasConsumedSubjectIsCaseSensitive guards the gt-rhxb lesson. A
// case-insensitive prefix match over this same protocol vocabulary silently
// swallowed ordinary prose: "Merged crater's fix by hand" was stored ephemeral
// because it matched "MERGED". Here the cost of that mistake is higher — the
// message would be closed rather than merely filed elsewhere.
func TestHasConsumedSubjectIsCaseSensitive(t *testing.T) {
	if HasConsumedSubject("Merged crater's fix by hand, here is what I found") {
		t.Error("HasConsumedSubject() matched prose that merely starts with the word Merged")
	}
	if HasConsumedSubject("convoy complete: lowercase prose about a convoy") {
		t.Error("HasConsumedSubject() matched a lowercase paraphrase of a protocol subject")
	}
	if !HasConsumedSubject("MERGED nux") {
		t.Error("HasConsumedSubject() = false for the real protocol subject; the checks above prove nothing if this one cannot match")
	}
}

// TestConsumedSubjectsExcludeActionableProtocolMail pins the distinction
// between this list and drainableSubjects (internal/cmd/mail_drain.go).
//
// Both look like "protocol subject prefixes", and merging them is the obvious
// simplification. It is wrong: drain is an explicit command behind a 30-minute
// age gate, so it may archive things that needed action but no longer do.
// Close-on-read has no age gate and no opt-in, so a subject only belongs there
// if reading it can never leave anything to do.
func TestConsumedSubjectsExcludeActionableProtocolMail(t *testing.T) {
	actionable := map[string]string{
		"POLECAT_DIED: 1 polecat(s) died with active work in gastown": "body ends in resling instructions the mayor has to run",
		"BRANCH SWEEP gastown: 4 branches to check":                   "a short list for a human to triage",
		"PATROL gastown: survey-workers HELD":                         "gt-ac1l: hq-1szee was a patrol report awaiting a ruling",
		"CRASHED_POLECAT gastown/nux":                                 "drainable once stale, not consumed on read",
		"SWARM_START gastown":                                         "drainable once stale, not consumed on read",
		"HELP: stuck on the rebase":                                   "the whole point is that someone answers",
	}
	for subject, why := range actionable {
		if HasConsumedSubject(subject) {
			t.Errorf("HasConsumedSubject(%q) = true, want false: %s", subject, why)
		}
	}

	// MERGE_READY and MERGE_FAILED are excluded by the TYPE layer rather than
	// this one (protocol.NewMergeReadyMessage sets TypeTask). Assert that,
	// so a future reader who adds them to the subject list finds out here that
	// the type is what was doing the work.
	for _, subject := range []string{"MERGE_READY chrome", "MERGE_FAILED chrome"} {
		if ConsumedByReading(&Message{RawType: string(TypeTask), Subject: subject}) {
			t.Errorf("ConsumedByReading(%q as task) = true, want false", subject)
		}
	}
}

// TestToMessagePreservesRawTypeAndStatus checks the two fields close-on-read
// depends on actually survive the conversion the read path performs. Both were
// being dropped: Status was never copied at all, and the msg-type label was
// only reachable after ParseMessageType had rewritten it.
func TestToMessagePreservesRawTypeAndStatus(t *testing.T) {
	t.Run("stamped type and status", func(t *testing.T) {
		bm := &BeadsMessage{
			ID:       "hq-abc",
			Title:    "Convoy complete: something",
			Assignee: "mayor",
			Status:   "open",
			Labels:   []string{"gt:message", "from:gastown/witness", "msg-type:notification"},
		}
		msg := bm.ToMessage()
		if msg.RawType != "notification" {
			t.Errorf("RawType = %q, want %q", msg.RawType, "notification")
		}
		if msg.Status != "open" {
			t.Errorf("Status = %q, want %q", msg.Status, "open")
		}
		if !ConsumedByReading(msg) {
			t.Error("ConsumedByReading() = false for an automated notification read back through ToMessage")
		}
	})

	t.Run("no msg-type label at all", func(t *testing.T) {
		bm := &BeadsMessage{
			ID:       "hq-def",
			Title:    "Convoy complete: something",
			Assignee: "mayor",
			Status:   "open",
			Labels:   []string{"gt:message", "from:gastown/witness"},
		}
		msg := bm.ToMessage()
		if msg.RawType != "" {
			t.Errorf("RawType = %q, want \"\" for a bead with no msg-type label", msg.RawType)
		}
		// Type stays lenient on purpose — old beads must still load and display.
		if msg.Type != TypeNotification {
			t.Errorf("Type = %q, want %q: the lenient read path must not change", msg.Type, TypeNotification)
		}
		if ConsumedByReading(msg) {
			t.Error("ConsumedByReading() = true for a bead with no msg-type label")
		}
	})

	t.Run("hooked status survives", func(t *testing.T) {
		bm := &BeadsMessage{
			ID:       "hq-ghi",
			Title:    "Polecat work handoff",
			Assignee: "gastown/polecats/brahmin",
			Status:   "hooked",
			Labels:   []string{"gt:message", "msg-type:handoff"},
		}
		if got := bm.ToMessage().Status; got != "hooked" {
			t.Errorf("Status = %q, want %q; without it the mailbox cannot tell hooked mail from open mail", got, "hooked")
		}
	})
}

// TestConsumesOnRead covers the conditions that belong to the READER rather
// than to the message. ConsumedByReading judges the message alone; these are
// the three things that make closing it this reader's business.
func TestConsumesOnRead(t *testing.T) {
	const me = "gastown/witness"
	consumable := func(mutate func(*Message)) *Message {
		msg := &Message{
			To:      me,
			Status:  "open",
			RawType: "notification",
			Subject: "Convoy complete: something",
		}
		if mutate != nil {
			mutate(msg)
		}
		return msg
	}

	tests := []struct {
		name    string
		msg     *Message
		want    bool
		because string
	}{
		{
			name: "addressed, open, consumable",
			msg:  consumable(nil),
			want: true,
		},
		{
			name:    "cc copy",
			msg:     consumable(func(m *Message) { m.To = "mayor/"; m.CC = []string{me} }),
			want:    false,
			because: "a CC copy is a second view of ONE bead; closing it clears the addressee's obligation for them",
		},
		{
			name:    "hooked bead",
			msg:     consumable(func(m *Message) { m.Status = "hooked" }),
			want:    false,
			because: "the inbox lists hooked mail, and gt hook reads the successor's context out of that bead",
		},
		{
			name:    "unknown status",
			msg:     consumable(func(m *Message) { m.Status = "" }),
			want:    false,
			because: "a status this code cannot classify is not an invitation to close",
		},
		{
			name: "nil",
			msg:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMailboxWithBeadsDir(me, t.TempDir(), t.TempDir())
			if got := m.consumesOnRead(tt.msg); got != tt.want {
				t.Errorf("consumesOnRead() = %v, want %v (%s)", got, tt.want, tt.because)
			}
		})
	}
}
