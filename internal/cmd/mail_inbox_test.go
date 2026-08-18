package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/mail"
)

type fakeInboxLister struct {
	calls    int
	messages []*mail.Message
	err      error
}

func (f *fakeInboxLister) List() ([]*mail.Message, error) {
	f.calls++
	return f.messages, f.err
}

func TestLoadInboxSnapshotListsOnceAndCounts(t *testing.T) {
	box := &fakeInboxLister{
		messages: []*mail.Message{
			{ID: "msg-1", Read: false},
			{ID: "msg-2", Read: true},
			{ID: "msg-3", Read: false},
		},
	}

	messages, counts, err := loadInboxSnapshot(box, nil, false)
	if err != nil {
		t.Fatalf("loadInboxSnapshot returned error: %v", err)
	}
	if box.calls != 1 {
		t.Fatalf("List calls = %d, want 1", box.calls)
	}
	if counts.Addressed != 3 || counts.AddressedUnread != 2 {
		t.Fatalf("counts = (%d addressed, %d unread), want (3, 2)", counts.Addressed, counts.AddressedUnread)
	}
	if len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(messages))
	}
}

func TestLoadInboxSnapshotUnreadOnlyFiltersAfterSingleList(t *testing.T) {
	box := &fakeInboxLister{
		messages: []*mail.Message{
			{ID: "msg-1", Read: false},
			{ID: "msg-2", Read: true},
			{ID: "msg-3", Read: false},
		},
	}

	messages, counts, err := loadInboxSnapshot(box, nil, true)
	if err != nil {
		t.Fatalf("loadInboxSnapshot returned error: %v", err)
	}
	if box.calls != 1 {
		t.Fatalf("List calls = %d, want 1", box.calls)
	}
	if counts.Addressed != 3 || counts.AddressedUnread != 2 {
		t.Fatalf("counts = (%d addressed, %d unread), want (3, 2)", counts.Addressed, counts.AddressedUnread)
	}
	if len(messages) != 2 {
		t.Fatalf("filtered messages len = %d, want 2", len(messages))
	}
	if messages[0].ID != "msg-1" || messages[1].ID != "msg-3" {
		t.Fatalf("filtered messages = [%s %s], want [msg-1 msg-3]", messages[0].ID, messages[1].ID)
	}
}

// fakeCCClassifier treats any message whose To is not the given address as a cc
// copy, mirroring mail.Mailbox.IsCCOnly without needing a beads backend.
type fakeCCClassifier struct{ address string }

func (f fakeCCClassifier) IsCCOnly(msg *mail.Message) bool {
	if msg == nil {
		return false
	}
	if msg.To == f.address {
		return false
	}
	for _, cc := range msg.CC {
		if cc == f.address {
			return true
		}
	}
	return false
}

// TestCountInboxMessagesSeparatesCCCopies covers the count that lied: cc copies
// are someone else's obligation, so they must not inflate the headline
// "unprocessed work" number (gt-58s).
func TestCountInboxMessagesSeparatesCCCopies(t *testing.T) {
	messages := []*mail.Message{
		{ID: "m1", To: "gastown/witness", Read: false},
		{ID: "m2", To: "gastown/witness", Read: true},
		{ID: "m3", To: "beads/refinery", CC: []string{"gastown/witness"}, Read: true},
		{ID: "m4", To: "beads/refinery", CC: []string{"gastown/witness"}, Read: false},
		nil,
	}

	counts := countInboxMessages(messages, fakeCCClassifier{address: "gastown/witness"})

	if counts.Addressed != 2 || counts.AddressedUnread != 1 {
		t.Fatalf("addressed = (%d, %d unread), want (2, 1)", counts.Addressed, counts.AddressedUnread)
	}
	if counts.CC != 2 || counts.CCUnread != 1 {
		t.Fatalf("cc = (%d, %d unread), want (2, 1)", counts.CC, counts.CCUnread)
	}
	// Reading a cc copy is legitimate work; only clearing it was broken.
	if counts.Unread() != 2 {
		t.Fatalf("Unread() = %d, want 2", counts.Unread())
	}
}

func TestInboxCountsSummary(t *testing.T) {
	// Without cc copies the header must read exactly as it always has.
	plain := inboxCounts{Addressed: 3, AddressedUnread: 1}
	if got := plain.Summary(); got != "3 messages, 1 unread" {
		t.Fatalf("Summary = %q", got)
	}
	withCC := inboxCounts{Addressed: 5, AddressedUnread: 2, CC: 4, CCUnread: 1}
	if got := withCC.Summary(); got != "5 messages, 2 unread, 4 cc (1 unread)" {
		t.Fatalf("Summary = %q", got)
	}
}

func TestLoadInboxSnapshotCountsCCSeparately(t *testing.T) {
	box := &fakeInboxLister{
		messages: []*mail.Message{
			{ID: "m1", To: "gastown/witness", Read: false},
			{ID: "m2", To: "beads/refinery", CC: []string{"gastown/witness"}, Read: false},
		},
	}

	messages, counts, err := loadInboxSnapshot(box, fakeCCClassifier{address: "gastown/witness"}, false)
	if err != nil {
		t.Fatalf("loadInboxSnapshot returned error: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2 (cc copies stay visible)", len(messages))
	}
	if counts.Addressed != 1 || counts.CC != 1 {
		t.Fatalf("counts = (%d addressed, %d cc), want (1, 1)", counts.Addressed, counts.CC)
	}
}

func TestInjectMessageLineMarksCCCopies(t *testing.T) {
	cc := fakeCCClassifier{address: "gastown/refinery"}

	ccLine := injectMessageLine(&mail.Message{
		ID: "hq-wisp-re1fri", From: "deacon/", Subject: "Clearance",
		To: "beads/refinery", CC: []string{"gastown/refinery"},
	}, cc)
	// The misroute report that prompted this: correct delivery of a cc copy read
	// as a message addressed to the reader.
	if !strings.Contains(ccLine, "cc — addressed to beads/refinery") {
		t.Fatalf("cc line = %q", ccLine)
	}

	own := injectMessageLine(&mail.Message{
		ID: "hq-wisp-mine", From: "mayor/", Subject: "Yours", To: "gastown/refinery",
	}, cc)
	if strings.Contains(own, "cc") {
		t.Fatalf("addressed line should carry no cc marker, got %q", own)
	}
	// A nil classifier is the pre-existing rendering, unchanged.
	if got := injectMessageLine(&mail.Message{ID: "x", From: "mayor/", Subject: "S", To: "who"}, nil); got != "- x from mayor/: S\n" {
		t.Fatalf("nil classifier line = %q", got)
	}
}

func TestLoadInboxSnapshotPropagatesListError(t *testing.T) {
	wantErr := errors.New("list failed")
	box := &fakeInboxLister{err: wantErr}

	_, _, err := loadInboxSnapshot(box, nil, false)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if box.calls != 1 {
		t.Fatalf("List calls = %d, want 1", box.calls)
	}
}
