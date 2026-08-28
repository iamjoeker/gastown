package mail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// searchFakeBD answers the queries Search makes, and answers them DIFFERENTLY
// per scope so a test can tell which query produced a result.
//
// The --all suffix is what separates the archived queries from the inbox ones,
// and it is appended last, so the archived cases must be matched before the
// bare ones.
const searchFakeBD = `#!/bin/sh
if [ "$1" = "list" ]; then
  case "$*" in
    *"--label from:gastown/synth"*)
      printf '%s\n' '[{"id":"sent-open","title":"Sent to mayor","description":"about brahmin","status":"open","priority":2,"assignee":"mayor/","created_at":"2026-06-12T12:00:20Z","labels":["gt:message","from:gastown/synth"]},{"id":"sent-closed","title":"Sent to deacon","description":"also brahmin","status":"closed","priority":2,"assignee":"deacon/","created_at":"2026-06-12T12:00:19Z","labels":["gt:message","from:gastown/synth"]}]'
      exit 0
      ;;
    *"--assignee gastown/synth"*"--all"*)
      printf '%s\n' '[{"id":"inbox-open","title":"Open one","description":"mentions brahmin","status":"open","priority":2,"assignee":"gastown/synth","created_at":"2026-06-12T12:00:05Z","labels":["gt:message","from:mayor/"]},{"id":"archived-closed","title":"Archived one","description":"also mentions brahmin","status":"closed","priority":2,"assignee":"gastown/synth","created_at":"2026-06-12T12:00:04Z","labels":["gt:message","from:mayor/"]}]'
      exit 0
      ;;
    *"--assignee gastown/synth"*)
      printf '%s\n' '[{"id":"inbox-open","title":"Open one","description":"mentions brahmin","status":"open","priority":2,"assignee":"gastown/synth","created_at":"2026-06-12T12:00:05Z","labels":["gt:message","from:mayor/"]}]'
      exit 0
      ;;
    *"--label-any cc:gastown/synth"*)
      printf '%s\n' '[{"id":"archived-cc","title":"Archived cc","description":"brahmin again","status":"closed","priority":2,"assignee":"mayor/","created_at":"2026-06-12T12:00:03Z","labels":["gt:message","cc:gastown/synth","from:mayor/"]},{"id":"cleared-cc","title":"Cleared cc","description":"brahmin dismissed","status":"open","priority":2,"assignee":"mayor/","created_at":"2026-06-12T12:00:06Z","labels":["gt:message","cc:gastown/synth","cc-cleared:gastown/synth","from:mayor/"]},{"id":"live-cc","title":"Live cc","description":"brahmin live","status":"open","priority":2,"assignee":"mayor/","created_at":"2026-06-12T12:00:07Z","labels":["gt:message","cc:gastown/synth","from:mayor/"]}]'
      exit 0
      ;;
  esac
  printf '%s\n' 'No issues found.'
  exit 0
fi
if [ "$1" = "sql" ]; then
  case "$*" in
    *"from_label.label IN"*)
      printf '%s\n' '[{"id":"sent-wisp","title":"Sent wisp","description":"brahmin wisp","status":"open","priority":2,"assignee":"deacon/","created_at":"2026-06-12T12:00:18Z","updated_at":"2026-06-12T12:00:18Z","labels_csv":"gt:message,from:gastown/synth","assignee_match":0,"cc_match":0}]'
      exit 0
      ;;
    *"1 = 1"*)
      printf '%s\n' '[{"id":"archived-wisp","title":"Archived wisp","description":"brahmin wisp","status":"closed","priority":2,"assignee":"gastown/synth","created_at":"2026-06-12T12:00:02Z","updated_at":"2026-06-12T12:00:02Z","labels_csv":"gt:message,from:mayor/","assignee_match":1,"cc_match":0},{"id":"cleared-cc-wisp","title":"Cleared cc wisp","description":"brahmin dismissed wisp","status":"open","priority":2,"assignee":"mayor/","created_at":"2026-06-12T12:00:08Z","updated_at":"2026-06-12T12:00:08Z","labels_csv":"gt:message,cc:gastown/synth,cc-cleared:gastown/synth,from:mayor/","assignee_match":0,"cc_match":1},{"id":"inbox-wisp","title":"Inbox wisp","description":"brahmin inbox wisp","status":"open","priority":2,"assignee":"gastown/synth","created_at":"2026-06-12T12:00:00Z","updated_at":"2026-06-12T12:00:00Z","labels_csv":"gt:message,from:mayor/","assignee_match":1,"cc_match":0}]'
      exit 0
      ;;
    *"IN ('open', 'hooked')"*)
      printf '%s\n' '[{"id":"inbox-wisp","title":"Inbox wisp","description":"brahmin inbox wisp","status":"open","priority":2,"assignee":"gastown/synth","created_at":"2026-06-12T12:00:00Z","updated_at":"2026-06-12T12:00:00Z","labels_csv":"gt:message,from:mayor/","assignee_match":1,"cc_match":0}]'
      exit 0
      ;;
  esac
  printf '%s\n' '[]'
  exit 0
fi
printf 'unexpected bd args: %s\n' "$*" >&2
exit 1
`

// newSearchScopeMailbox stands up a beads-backed mailbox for gastown/synth
// whose bd is searchFakeBD, and seeds archive.jsonl with entries for three
// different recipients.
func newSearchScopeMailbox(t *testing.T) *Mailbox {
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
	fakeBD := filepath.Join(binDir, "bd")
	if err := os.WriteFile(fakeBD, []byte(searchFakeBD), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// archive.jsonl belongs to the beads DIRECTORY, not to any one identity, so
	// it holds mail for whoever shares the directory. Two of these three are
	// this mailbox's; the third is a stranger's and must never surface.
	stamp := time.Date(2026, 6, 12, 12, 0, 1, 0, time.UTC)
	fileArchived := []*Message{
		{ID: "file-direct", From: "mayor/", To: "gastown/synth", Subject: "Filed direct", Body: "brahmin in the file", Timestamp: stamp},
		{ID: "file-cc", From: "mayor/", To: "deacon/", CC: []string{"gastown/synth"}, Subject: "Filed cc", Body: "brahmin cc'd", Timestamp: stamp},
		{ID: "file-stranger", From: "mayor/", To: "deacon/dogs/alpha", Subject: "Not yours", Body: "brahmin for somebody else", Timestamp: stamp},
	}
	var buf strings.Builder
	for _, msg := range fileArchived {
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal archived message: %v", err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "archive.jsonl"), []byte(buf.String()), 0644); err != nil {
		t.Fatalf("write archive.jsonl: %v", err)
	}

	return NewMailboxWithBeadsDir("gastown/synth", t.TempDir(), beadsDir)
}

func searchIDs(t *testing.T, m *Mailbox, opts SearchOptions) (*SearchResult, map[string]bool) {
	t.Helper()
	result, err := m.Search(opts)
	if err != nil {
		t.Fatalf("Search(%+v): %v", opts, err)
	}
	ids := make(map[string]bool, len(result.Messages))
	for _, msg := range result.Messages {
		ids[msg.ID] = true
	}
	return result, ids
}

func assertIDs(t *testing.T, ids map[string]bool, want, notWant []string) {
	t.Helper()
	for _, id := range want {
		if !ids[id] {
			t.Errorf("missing %s; got %v", id, sortedKeys(ids))
		}
	}
	for _, id := range notWant {
		if ids[id] {
			t.Errorf("unexpected %s; got %v", id, sortedKeys(ids))
		}
	}
}

func sortedKeys(ids map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	return out
}

// TestSearchDefaultScopeIsInboxOnly pins the default down to the inbox.
//
// This is a regression guard with a measured cause. Search used to fold
// archive.jsonl in unconditionally and unfiltered, which handed a polecat whose
// inbox held one message 56832 results — 0 of them addressed to it, the rest
// four deacon dogs' mail. The default must not read that file at all (gt-7gvk).
func TestSearchDefaultScopeIsInboxOnly(t *testing.T) {
	m := newSearchScopeMailbox(t)

	result, ids := searchIDs(t, m, SearchOptions{Query: "brahmin"})

	assertIDs(t, ids,
		[]string{"inbox-open", "live-cc", "inbox-wisp"},
		[]string{"archived-closed", "archived-cc", "cleared-cc", "cleared-cc-wisp", "archived-wisp", "sent-open", "sent-wisp", "file-direct", "file-cc", "file-stranger"})

	if !result.Scope.Inbox || result.Scope.Archived || result.Scope.Sent {
		t.Errorf("default scope = %+v, want inbox only", result.Scope)
	}
	if len(result.Scope.Unavailable) != 0 {
		t.Errorf("Unavailable = %v, want none", result.Scope.Unavailable)
	}
}

// TestSearchIncludeArchivedReachesClosedBeads is the reported case.
//
// `gt mail archive` closes the message bead where it stands, so archived mail
// is a CLOSED bead — precisely what the inbox query filters out. Archiving a
// message naming "brahmin" and then searching "brahmin" returned 0 one minute
// later, which is what made a 0 from this command carry no information.
func TestSearchIncludeArchivedReachesClosedBeads(t *testing.T) {
	m := newSearchScopeMailbox(t)

	result, ids := searchIDs(t, m, SearchOptions{Query: "brahmin", IncludeArchived: true})

	assertIDs(t, ids,
		[]string{"inbox-open", "inbox-wisp", "archived-closed", "archived-cc", "archived-wisp"},
		[]string{"sent-open", "sent-wisp"})

	if !result.Scope.Archived || result.Scope.Sent {
		t.Errorf("scope = %+v, want archived without sent", result.Scope)
	}
}

// TestSearchArchivedFindsDismissedCCCopies covers the case a closed-status test
// would miss entirely.
//
// A CC'd message is not its CC recipient's to close — the bead belongs to its
// assignee — so archiving a CC copy adds cc-cleared:<me> and leaves the bead
// OPEN. The inbox hides it because it is cleared; a status test would hide it
// because it is open. Between them the message would be in no store at all,
// which is the disappearance this whole change exists to fix, reproduced one
// level down.
func TestSearchArchivedFindsDismissedCCCopies(t *testing.T) {
	m := newSearchScopeMailbox(t)

	_, inboxIDs := searchIDs(t, m, SearchOptions{Query: "brahmin"})
	assertIDs(t, inboxIDs, []string{"live-cc"}, []string{"cleared-cc", "cleared-cc-wisp"})

	_, archiveIDs := searchIDs(t, m, SearchOptions{Query: "brahmin", IncludeArchived: true})
	assertIDs(t, archiveIDs, []string{"cleared-cc", "cleared-cc-wisp", "live-cc"}, nil)
}

// TestSearchArchivedFiltersTheSharedFileByRecipient covers the other half of
// the same defect: archive.jsonl is per beads directory, so an agent that
// shares one with the deacon's dogs reads their mail out of it unless the
// entries are filtered by recipient.
//
// The direct and CC entries must both survive — dropping CC would trade one
// blindness for another — and the comparison has to go through
// AgentAddressKey, because the mailbox identity keeps its container segment
// while a delivered message records the canonical address.
func TestSearchArchivedFiltersTheSharedFileByRecipient(t *testing.T) {
	m := newSearchScopeMailbox(t)

	_, ids := searchIDs(t, m, SearchOptions{Query: "brahmin", IncludeArchived: true})

	assertIDs(t, ids, []string{"file-direct", "file-cc"}, []string{"file-stranger"})
}

// TestSearchArchivedMatchesNestedIdentityAgainstCanonicalAddress guards the
// comparison rule directly: a polecat's mailbox identity is
// "gastown/polecats/fury" while its delivered mail records "gastown/fury".
// Comparing those as written discards every entry.
func TestSearchArchivedMatchesNestedIdentityAgainstCanonicalAddress(t *testing.T) {
	m := newSearchScopeMailbox(t)
	m.identity = "gastown/polecats/synth"

	if !m.addressesThisMailbox(&Message{To: "gastown/synth"}) {
		t.Error("nested identity did not match its own canonical address")
	}
	if !m.addressesThisMailbox(&Message{To: "mayor/", CC: []string{"gastown/synth"}}) {
		t.Error("nested identity did not match its own canonical address in CC")
	}
	if m.addressesThisMailbox(&Message{To: "gastown/other"}) {
		t.Error("matched a message addressed to somebody else")
	}
}

// TestSearchIncludeSentSelectsByFromLabel covers the surface that did not
// exist at all: sent mail is addressed to somebody else, so no query over this
// mailbox's recipients reaches it, and without it a detector cannot ask whether
// it already reported something.
func TestSearchIncludeSentSelectsByFromLabel(t *testing.T) {
	m := newSearchScopeMailbox(t)

	result, ids := searchIDs(t, m, SearchOptions{Query: "brahmin", IncludeSent: true})

	assertIDs(t, ids,
		[]string{"inbox-open", "sent-open", "sent-wisp"},
		[]string{"archived-closed", "archived-cc", "cleared-cc", "archived-wisp", "file-direct"})

	// Sent mail is worth finding after its recipient closes it; the sender does
	// not lose their own outbox when the other end reads it.
	if !ids["sent-closed"] {
		t.Errorf("closed sent message missing; got %v", sortedKeys(ids))
	}
	if !result.Scope.Sent || result.Scope.Archived {
		t.Errorf("scope = %+v, want sent without archived", result.Scope)
	}
}

// TestSentQueriesTheNestedAddressFormTheSendPathWrites guards a blind zero
// found by running the reported control end to end, which reading the code did
// not surface.
//
// The send path writes the two ends of one message in DIFFERENT forms. A
// message a polecat sent itself came back assigned "gastown/fury" — the
// collapsed form AddressToIdentity produces, and the mailbox's identity — and
// labelled "from:gastown/polecats/fury". Measured against the live store:
// --label from:gastown/polecats/fury returned 1 and --label from:gastown/fury
// returned 0, for the same message. A sent query keyed on the identity alone
// therefore finds nothing, forever, for every polecat and crew worker.
func TestSentQueriesTheNestedAddressFormTheSendPathWrites(t *testing.T) {
	m := NewMailboxWithBeadsDir("gastown/polecats/fury", t.TempDir(), t.TempDir())

	if m.identity != "gastown/fury" {
		t.Fatalf("identity = %q, want the collapsed form — the premise of this test", m.identity)
	}

	forms := m.senderAddressForms()
	has := func(want string) bool {
		for _, form := range forms {
			if form == want {
				return true
			}
		}
		return false
	}

	if !has("gastown/polecats/fury") {
		t.Errorf("senderAddressForms = %v, missing the nested form the from: label carries", forms)
	}
	if !has("gastown/fury") {
		t.Errorf("senderAddressForms = %v, missing the collapsed form", forms)
	}

	// The forms must come from what the caller supplied, not from expanding the
	// identity: an expansion has to guess between "crew" and "polecats", and a
	// wrong guess pulls in a different agent's sent mail.
	for _, form := range forms {
		if strings.Contains(form, "/crew/") {
			t.Errorf("senderAddressForms = %v, invented a crew form for a polecat address", forms)
		}
	}
}

// TestSearchAllScopesDeduplicate: a message can legitimately be in more than
// one store — sent mail CC'd back to the sender is both sent and received —
// and must be reported once.
func TestSearchAllScopesDeduplicate(t *testing.T) {
	m := newSearchScopeMailbox(t)

	result, _ := searchIDs(t, m, SearchOptions{Query: "brahmin", IncludeArchived: true, IncludeSent: true})

	seen := make(map[string]int)
	for _, msg := range result.Messages {
		seen[msg.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s appears %d times, want 1", id, n)
		}
	}
	if !result.Scope.Inbox || !result.Scope.Archived || !result.Scope.Sent {
		t.Errorf("scope = %+v, want all three", result.Scope)
	}
}

// TestSearchReportsUnreadableStoreInsteadOfSwallowingIt is the point of the
// scope type. A search that quietly drops one of its stores is a search whose
// zero is a lie, so an unreadable store is NAMED — and the matches from the
// stores that did answer are still delivered rather than lost to an error
// return.
func TestSearchReportsUnreadableStoreInsteadOfSwallowingIt(t *testing.T) {
	beadsDir := t.TempDir()
	path := filepath.Join(beadsDir, "inbox.jsonl")
	msg := &Message{ID: "legacy-1", From: "mayor/", To: "crew", Subject: "Hello", Body: "brahmin", Timestamp: time.Now()}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatalf("write inbox: %v", err)
	}

	m := NewMailbox(beadsDir)
	result, err := m.Search(SearchOptions{Query: "brahmin", IncludeSent: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(result.Messages) != 1 || result.Messages[0].ID != "legacy-1" {
		t.Errorf("messages = %+v, want the inbox match despite the unreadable store", result.Messages)
	}
	if result.Scope.Sent {
		t.Error("Scope.Sent is true for a store that could not be read")
	}
	if len(result.Scope.Unavailable) != 1 || !strings.HasPrefix(result.Scope.Unavailable[0], "sent: ") {
		t.Errorf("Unavailable = %v, want one entry naming the sent store", result.Scope.Unavailable)
	}
}
