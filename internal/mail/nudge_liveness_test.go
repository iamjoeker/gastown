package mail

import (
	"testing"

	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/session"
)

// The banner that prompted this: ten Mayor orders replayed at a witness, every
// one of them already read and archived, several of them revisions of each
// other. See gt-loz6.
func TestFilterLiveNudgesDropsSpentMailNotifications(t *testing.T) {
	inbox := []*Message{
		{ID: "gt-live", ThreadID: "thread-live", Read: false},
		{ID: "gt-read", ThreadID: "thread-read", Read: true},
	}

	nudges := []nudge.QueuedNudge{
		{Kind: nudge.KindMail, MessageID: "gt-live", ThreadID: "thread-live", Message: "live"},
		{Kind: nudge.KindMail, MessageID: "gt-read", ThreadID: "thread-read", Message: "already read"},
		{Kind: nudge.KindMail, MessageID: "gt-archived", ThreadID: "thread-archived", Message: "archived"},
		{Kind: nudge.KindEscalation, MessageID: "gt-read", ThreadID: "thread-read", Message: "escalation read"},
		{Message: "plain gt nudge, no kind"},
		{Kind: "hook", Message: "propulsion signal"},
	}

	live := FilterLiveNudges(nudges, inbox)

	var got []string
	for _, n := range live {
		got = append(got, n.Message)
	}
	want := []string{"live", "plain gt nudge, no kind", "propulsion signal"}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("kept[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A thread outlives the individual messages on it. Matching liveness by thread
// keeps a spent notification alive as long as anything newer on the same thread
// is unread — which is how a revoked order stayed in the banner.
func TestFilterLiveNudgesUsesMessageIDNotThread(t *testing.T) {
	inbox := []*Message{
		{ID: "gt-old", ThreadID: "thread-1", Read: true},
		{ID: "gt-new", ThreadID: "thread-1", Read: false},
	}

	nudges := []nudge.QueuedNudge{
		{Kind: nudge.KindMail, MessageID: "gt-old", ThreadID: "thread-1", Message: "superseded order"},
		{Kind: nudge.KindMail, MessageID: "gt-new", ThreadID: "thread-1", Message: "the correction"},
	}

	live := FilterLiveNudges(nudges, inbox)
	if len(live) != 1 {
		t.Fatalf("kept %d nudges, want 1", len(live))
	}
	if live[0].Message != "the correction" {
		t.Errorf("kept %q, want the correction", live[0].Message)
	}
}

// Nudges written before MessageID existed carry only a thread. Thread state is
// as precise as that record allows, and is better than replaying forever.
func TestFilterLiveNudgesFallsBackToThread(t *testing.T) {
	inbox := []*Message{
		{ID: "gt-a", ThreadID: "thread-unread", Read: false},
		{ID: "gt-b", ThreadID: "thread-read", Read: true},
	}

	nudges := []nudge.QueuedNudge{
		{Kind: nudge.KindMail, ThreadID: "thread-unread", Message: "keep"},
		{Kind: nudge.KindMail, ThreadID: "thread-read", Message: "drop"},
		{Kind: nudge.KindMail, ThreadID: "thread-gone", Message: "drop too"},
		{Kind: nudge.KindMail, Message: "uncorrelatable — keep"},
	}

	live := FilterLiveNudges(nudges, inbox)
	if len(live) != 2 {
		t.Fatalf("kept %d nudges, want 2: %+v", len(live), live)
	}
	if live[0].Message != "keep" || live[1].Message != "uncorrelatable — keep" {
		t.Errorf("kept %q and %q", live[0].Message, live[1].Message)
	}
}

// A reply reminder's obligation is to reply, not to read: it survives the read
// and dies with the archive.
func TestFilterLiveNudgesReplyReminderSurvivesRead(t *testing.T) {
	inbox := []*Message{{ID: "gt-read", ThreadID: "thread-1", Read: true}}

	nudges := []nudge.QueuedNudge{
		{Kind: nudge.KindReplyReminder, MessageID: "gt-read", ThreadID: "thread-1", Message: "reply to it"},
		{Kind: nudge.KindReplyReminder, MessageID: "gt-archived", ThreadID: "thread-2", Message: "gone"},
	}

	live := FilterLiveNudges(nudges, inbox)
	if len(live) != 1 || live[0].Message != "reply to it" {
		t.Fatalf("kept %+v, want only the reminder for the open message", live)
	}
}

// Failing open matters more than filtering: a stale notification is noise, a
// dropped one is a lost message. An empty inbox read is indistinguishable from
// a failed one at this layer, so nudges that cannot be correlated stay.
func TestFilterLiveNudgesEmptyInboxKeepsUncorrelatable(t *testing.T) {
	nudges := []nudge.QueuedNudge{
		{Kind: nudge.KindMail, MessageID: "gt-a", Message: "correlatable, absent"},
		{Kind: nudge.KindMail, Message: "uncorrelatable"},
		{Message: "not mail at all"},
	}

	live := FilterLiveNudges(nudges, nil)
	if len(live) != 2 {
		t.Fatalf("kept %d, want 2: %+v", len(live), live)
	}
}

func TestSessionNameToAddress(t *testing.T) {
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })

	tests := []struct {
		session string
		want    string
	}{
		{"hq-mayor", "mayor"},
		{"hq-deacon", "deacon"},
		{"hq-dog-bravo", "deacon/dogs/bravo"},
		{"gt-witness", "gastown/witness"},
		{"gt-refinery", "gastown/refinery"},
		{"gt-crew-max", "gastown/crew/max"},
		{"gt-alpha", "gastown/alpha"},
		{"not a session", ""},
	}
	for _, tt := range tests {
		if got := SessionNameToAddress(tt.session); got != tt.want {
			t.Errorf("SessionNameToAddress(%q) = %q, want %q", tt.session, got, tt.want)
		}
	}
}

// The eager half of the fix: reading or archiving a message clears its queued
// notification, so a dead pointer stops counting against the depth cap and stops
// being re-delivered before any drain gets to filter it.
func TestClearMessageNudges(t *testing.T) {
	townRoot := t.TempDir()
	r := &Router{workDir: t.TempDir(), townRoot: townRoot}
	sessionID := session.CrewSessionName(session.PrefixFor("gastown"), "bob")

	for _, n := range []nudge.QueuedNudge{
		{Sender: "mayor/", Message: "read-it", Kind: nudge.KindMail, MessageID: "gt-a", ThreadID: "thread-1"},
		{Sender: "system", Message: "reply-to-it", Kind: nudge.KindReplyReminder, MessageID: "gt-a", ThreadID: "thread-1"},
		{Sender: "mayor/", Message: "keep-other-message", Kind: nudge.KindMail, MessageID: "gt-b", ThreadID: "thread-1"},
		{Sender: "witness", Message: "keep-plain-nudge"},
	} {
		if err := nudge.Enqueue(townRoot, sessionID, n); err != nil {
			t.Fatalf("Enqueue(%q): %v", n.Message, err)
		}
	}

	if err := r.ClearMessageNudges("gastown/crew/bob", "gt-a", "thread-1"); err != nil {
		t.Fatalf("ClearMessageNudges: %v", err)
	}

	nudges, err := nudge.Drain(townRoot, sessionID)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 2 {
		t.Fatalf("Drain returned %d nudges, want 2: %+v", len(nudges), nudges)
	}
	// Same thread, different message: clearing must not take the sibling with it.
	if nudges[0].Message != "keep-other-message" || nudges[1].Message != "keep-plain-nudge" {
		t.Fatalf("kept %q and %q", nudges[0].Message, nudges[1].Message)
	}
}
