package mail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/telemetry"
)

// timeNow is a function that returns the current time. It can be overridden in tests.
var timeNow = time.Now

// Common errors
var (
	ErrMessageNotFound = errors.New("message not found")
	ErrEmptyInbox      = errors.New("inbox is empty")
)

// Mailbox manages messages for an identity via beads.
// When store is non-nil, beads-mode methods use the in-process beadsdk.Storage
// directly instead of shelling out to the bd CLI.
type Mailbox struct {
	identity string // beads identity (e.g., "gastown/polecats/Toast")

	// address is the GGT address this mailbox was constructed from, kept
	// exactly as the caller wrote it.
	//
	// identity is that address run through AddressToIdentity, which COLLAPSES
	// the container segment ("gastown/polecats/fury" -> "gastown/fury"), and
	// collapsing is lossy: expanding it back would mean guessing between "crew"
	// and "polecats". The send path writes the two ends of a message in
	// different forms — the assignee collapsed, the from: label as passed — so
	// a query keyed only on identity finds the mail this mailbox received and
	// none of the mail it sent. Keeping the original is what lets
	// senderAddressForms ask for both without reconstructing either.
	address string

	workDir  string // directory to run bd commands in
	beadsDir string // explicit .beads directory path (set via BEADS_DIR)
	path     string // for legacy JSONL mode (crew workers)
	legacy   bool   // true = use JSONL files, false = use beads

	// store is an optional in-process beadsdk.Storage. When set, beads-mode
	// methods bypass the bd subprocess and use the store directly.
	// Callers are responsible for closing the store.
	store beadsdk.Storage

	// forceClose adds --force to the underlying `bd close`.
	//
	// bd refuses to close a bead whose assignee does not match the acting
	// identity, and its error tells the operator to "use --force". Until this
	// existed, gt surfaced that advice while offering no way to act on it: the
	// suggested remedy was unreachable from the command that suggested it.
	//
	// An agent archiving its OWN mail no longer needs this: closeInDir acts under
	// the mailbox's canonical identity, which is the identity the delivery path
	// assigned (gt-n3gj). What remains is the case the flag was named for —
	// deliberately closing a record that belongs to another agent, such as
	// clearing mail left behind by an agent that no longer exists.
	// See SetForceClose.
	forceClose bool
}

// SetForceClose makes subsequent close operations pass --force to bd.
//
// Intended for `gt mail archive --force`, where the operator has decided to
// archive their own mail despite an assignee/actor mismatch. It does not
// suppress any other error.
func (m *Mailbox) SetForceClose(force bool) {
	m.forceClose = force
}

// NewMailbox creates a mailbox for the given JSONL path (legacy mode).
// Used by crew workers that have local JSONL inboxes.
func NewMailbox(path string) *Mailbox {
	return &Mailbox{
		path:   filepath.Join(path, "inbox.jsonl"),
		legacy: true,
	}
}

// NewMailboxBeads creates a mailbox backed by beads.
func NewMailboxBeads(identity, workDir string) *Mailbox {
	return &Mailbox{
		identity: identity,
		workDir:  workDir,
		legacy:   false,
	}
}

// NewMailboxFromAddress creates a beads-backed mailbox from a GGT address.
// Follows .beads/redirect for crew workers and polecats using shared beads.
func NewMailboxFromAddress(address, workDir string) *Mailbox {
	beadsDir := beads.ResolveBeadsDir(workDir)
	return &Mailbox{
		identity: AddressToIdentity(address),
		address:  address,
		workDir:  workDir,
		beadsDir: beadsDir,
		legacy:   false,
	}
}

// NewMailboxWithBeadsDir creates a mailbox with an explicit beads directory.
func NewMailboxWithBeadsDir(address, workDir, beadsDir string) *Mailbox {
	return &Mailbox{
		identity: AddressToIdentity(address),
		address:  address,
		workDir:  workDir,
		beadsDir: beadsDir,
		legacy:   false,
	}
}

// Identity returns the beads identity for this mailbox.
func (m *Mailbox) Identity() string {
	return m.identity
}

// Path returns the JSONL path for legacy mailboxes.
func (m *Mailbox) Path() string {
	return m.path
}

// lockLegacy acquires an exclusive flock for legacy mailbox operations.
// Callers must defer Unlock on the returned flock. The lock file is
// separate from the data file to avoid interfering with reads.
func (m *Mailbox) lockLegacy() (*flock.Flock, error) {
	fl := flock.New(m.path + ".lock")
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("acquiring mailbox lock: %w", err)
	}
	return fl, nil
}

// List returns all open messages in the mailbox.
func (m *Mailbox) List() ([]*Message, error) {
	if m.legacy {
		return m.listLegacy()
	}
	return m.listBeads()
}

func (m *Mailbox) listBeads() ([]*Message, error) {
	// Single query to beads - returns both persistent and wisp messages
	// Wisps are stored in same DB with wisp=true flag, not synced to git
	messages, err := m.listFromDir(m.beadsDir)
	if err != nil {
		return nil, err
	}

	// Sort by priority (higher first), then timestamp (newest first).
	sort.Slice(messages, func(i, j int) bool {
		pi, pj := PriorityToBeads(messages[i].Priority), PriorityToBeads(messages[j].Priority)
		if pi != pj {
			return pi < pj // lower beads int = higher priority
		}
		return messages[i].Timestamp.After(messages[j].Timestamp)
	})

	return messages, nil
}

// listFromDir queries messages from a beads directory.
// Returns messages where identity is the assignee OR a CC recipient.
// Includes both open and hooked messages (hooked = auto-assigned handoff mail).
//
// Uses per-identity --assignee queries to push filtering to Dolt, reducing
// memory footprint under concurrent agent load. A separate CC query fetches
// messages where this identity is CC'd.
func (m *Mailbox) listFromDir(beadsDir string) ([]*Message, error) {
	// Use in-process store when available
	if m.store != nil {
		return m.storeListFromDir()
	}

	identities := m.identityVariants()

	if err := beads.EnsureCustomTypes(beadsDir); err != nil {
		return nil, fmt.Errorf("ensuring custom types: %w", err)
	}

	type beadsFetch struct {
		messages []BeadsMessage
		err      error
	}
	type wispFetch struct {
		messages []wispQueryMessage
		err      error
	}

	var assignee beadsFetch
	var cc beadsFetch
	var wisps wispFetch

	// The three fetches are independent. Keep identity variants collapsed inside
	// each fetch so parallelism reduces latency without multiplying wisp SQL work.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		assignee.messages, assignee.err = m.queryIssueMessagesByAssignee(beadsDir, identities)
	}()
	go func() {
		defer wg.Done()
		cc.messages = m.queryIssueMessagesByCC(beadsDir, identities)
	}()
	go func() {
		defer wg.Done()
		wisps.messages, wisps.err = m.queryWispMessages(beadsDir, identities)
	}()
	wg.Wait()

	if assignee.err != nil {
		return nil, assignee.err
	}

	// Deduplicate messages across queries (assignee + CC + wisps may overlap).
	// Assignee results are appended first so a message that is both addressed and
	// CC'd to this identity is treated as addressed, and CC dismissal cannot hide
	// work this identity actually owns (gt-58s).
	seen := make(map[string]bool)
	messages := make([]*Message, 0, len(assignee.messages)+len(cc.messages)+len(wisps.messages))
	messages = appendBeadsMessages(messages, seen, assignee.messages, true)
	messages = appendBeadsMessages(messages, seen, filterClearedCC(cc.messages, identities), false)
	if wisps.err == nil {
		messages = appendWispMessages(messages, seen, wisps.messages, identities)
	}

	return messages, nil
}

// extra appends bd flags to the query. Archived mail reuses this function with
// --all rather than writing its own, so that the inbox and archive halves
// cannot drift apart in how they select a recipient.
func (m *Mailbox) queryIssueMessagesByAssignee(beadsDir string, identities []string, extra ...string) ([]BeadsMessage, error) {
	var messages []BeadsMessage
	for _, id := range identities {
		args := []string{"list",
			"--label", "gt:message",
			"--assignee", id,
			"--json",
			"--limit", "0",
		}
		args = append(args, extra...)

		ctx, cancel := bdReadCtx()
		stdout, err := runBdCommand(ctx, args, m.workDir, beadsDir)
		cancel()
		if err != nil {
			return nil, err
		}
		msgs, err := parseBeadsListOutput(stdout)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msgs...)
	}
	return messages, nil
}

// queryIssueMessagesByCC fetches messages where any of this agent's address
// forms appears as a CC recipient.
//
// The address forms are ORed inside a single bd call rather than run as one
// call per form. Every mail poll spawns a bd subprocess per query, and bd
// subprocesses are the unit of load on Dolt.
//
// The saving is narrow and worth stating exactly: beads.AgentAddressForms
// returns two forms only for the town-level singleton roles — "deacon/" and
// "deacon", "mayor/" and "mayor" — and one form for everyone else. So this
// removes one bd subprocess per poll from the mayor's and the deacon's
// mailboxes, which are the two busiest in the town, and changes nothing for a
// polecat. It also makes the CC query shape independent of how many forms an
// address grows later.
//
// The direct-recipient half cannot be folded in the same way: it selects on
// --assignee, which takes a single value and is ANDed with labels, so it
// cannot be ORed against a cc: label. Merging the two halves into one call
// would require the recipient to be carried as a to:<agent> LABEL as well as
// an assignee, and no message carries one today (verified against the live
// store: 0 of 2576 gt:message beads have any to: label, while 188 carry cc:
// and all 2576 carry from:, so the query is not blind). Adding the label at
// send time would only help mail sent afterwards; every existing message would
// silently drop out of a merged query.
// extra appends bd flags to the query; see queryIssueMessagesByAssignee.
func (m *Mailbox) queryIssueMessagesByCC(beadsDir string, identities []string, extra ...string) []BeadsMessage {
	var messages []BeadsMessage
	for _, batch := range ccLabelBatches(identities) {
		args := []string{"list",
			"--label", "gt:message",
			"--label-any", strings.Join(batch, ","),
			"--json",
			"--limit", "0",
		}
		args = append(args, extra...)

		ctx, cancel := bdReadCtx()
		stdout, err := runBdCommand(ctx, args, m.workDir, beadsDir)
		cancel()
		if err != nil {
			continue
		}
		msgs, err := parseBeadsListOutput(stdout)
		if err != nil {
			continue
		}
		messages = append(messages, msgs...)
	}
	return messages
}

// ccLabelBatches turns identities into the cc: label sets to query.
//
// Normally that is one batch holding every form, answered by a single bd call.
// bd's --label-any takes a comma-separated list, so an identity containing a
// comma would be split into two labels that match nothing; such an identity
// falls back to one batch per form, which is what this code did for every
// identity before.
func ccLabelBatches(identities []string) [][]string {
	labels := make([]string, 0, len(identities))
	for _, id := range identities {
		if strings.Contains(id, ",") {
			batches := make([][]string, 0, len(identities))
			for _, one := range identities {
				batches = append(batches, []string{"cc:" + one})
			}
			return batches
		}
		labels = append(labels, "cc:"+id)
	}
	if len(labels) == 0 {
		return nil
	}
	return [][]string{labels}
}

func parseBeadsListOutput(stdout []byte) ([]BeadsMessage, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("No issues found.")) {
		return nil, nil
	}
	if !isJSON(trimmed) {
		return nil, nil
	}

	var msgs []BeadsMessage
	if err := json.Unmarshal(trimmed, &msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

func appendBeadsMessages(messages []*Message, seen map[string]bool, msgs []BeadsMessage, includeHooked bool) []*Message {
	for i := range msgs {
		bm := &msgs[i]
		if seen[bm.ID] {
			continue
		}
		if bm.Status == "open" || (includeHooked && bm.Status == "hooked") {
			seen[bm.ID] = true
			messages = append(messages, bm.ToMessage())
		}
	}
	return messages
}

func appendWispMessages(messages []*Message, seen map[string]bool, wisps []wispQueryMessage, identities []string) []*Message {
	for i := range wisps {
		wisp := &wisps[i]
		bm := &wisp.message
		if seen[bm.ID] {
			continue
		}
		include := wisp.assigneeMatch && (bm.Status == "open" || bm.Status == "hooked")
		include = include || (wisp.ccMatch && !ccClearedFor(bm, identities) && bm.Status == "open")
		if include {
			seen[bm.ID] = true
			messages = append(messages, bm.ToMessage())
		}
	}
	return messages
}

type wispQueryMessage struct {
	message       BeadsMessage
	assigneeMatch bool
	ccMatch       bool
}

// queryWispMessages queries ephemeral messages once across all identity variants.
// Protocol/lifecycle messages are stored as wisps by shouldBeWisp(), but bd list
// only queries the issues table.
func (m *Mailbox) queryWispMessages(beadsDir string, identities []string) ([]wispQueryMessage, error) {
	return m.queryWispMessagesWithStatus(beadsDir, identities, "w.status IN ('open', 'hooked')")
}

// queryWispMessagesWithStatus selects wisp messages addressed to these
// identities, restricted by the given SQL predicate over w.status.
//
// The predicate is a parameter because the inbox and the archive want opposite
// halves of the same population: the inbox wants open and hooked, archived mail
// wants everything else. Building the second query separately is how the two
// would come to disagree about what "addressed to me" means.
func (m *Mailbox) queryWispMessagesWithStatus(beadsDir string, identities []string, statusPredicate string) ([]wispQueryMessage, error) {
	if len(identities) == 0 {
		return nil, nil
	}

	ccLabels := make([]string, 0, len(identities))
	for _, id := range identities {
		ccLabels = append(ccLabels, "cc:"+id)
	}
	identityList := sqlStringList(identities)
	ccLabelList := sqlStringList(ccLabels)

	query := fmt.Sprintf(
		"SELECT w.id, w.title, w.description, w.status, w.priority, w.assignee, w.created_at, w.updated_at, "+
			"GROUP_CONCAT(DISTINCT al.label) as labels_csv, "+
			"MAX(CASE WHEN w.assignee IN (%s) THEN 1 ELSE 0 END) as assignee_match, "+
			"MAX(CASE WHEN cc.label IS NOT NULL THEN 1 ELSE 0 END) as cc_match "+
			"FROM wisps w "+
			"JOIN wisp_labels msg_label ON w.id = msg_label.issue_id AND msg_label.label = 'gt:message' "+
			"JOIN wisp_labels al ON w.id = al.issue_id "+
			"LEFT JOIN wisp_labels cc ON w.id = cc.issue_id AND cc.label IN (%s) "+
			"WHERE %s AND (w.assignee IN (%s) OR cc.label IS NOT NULL) "+
			"GROUP BY w.id, w.title, w.description, w.status, w.priority, w.assignee, w.created_at, w.updated_at",
		identityList, ccLabelList, statusPredicate, identityList)
	return m.runWispSQL(beadsDir, query)
}

// queryWispMessagesFrom selects wisp messages SENT by these identities.
//
// Sent mail is addressed to somebody else, so the recipient predicate the other
// wisp queries use cannot reach it; it is selected by its from: label instead.
// No status restriction: sent mail is worth finding whether or not the
// recipient has since closed it.
func (m *Mailbox) queryWispMessagesFrom(beadsDir string, identities []string) ([]wispQueryMessage, error) {
	if len(identities) == 0 {
		return nil, nil
	}

	fromLabels := make([]string, 0, len(identities))
	for _, id := range identities {
		fromLabels = append(fromLabels, "from:"+id)
	}

	query := fmt.Sprintf(
		"SELECT w.id, w.title, w.description, w.status, w.priority, w.assignee, w.created_at, w.updated_at, "+
			"GROUP_CONCAT(DISTINCT al.label) as labels_csv, "+
			"0 as assignee_match, 0 as cc_match "+
			"FROM wisps w "+
			"JOIN wisp_labels msg_label ON w.id = msg_label.issue_id AND msg_label.label = 'gt:message' "+
			"JOIN wisp_labels from_label ON w.id = from_label.issue_id AND from_label.label IN (%s) "+
			"JOIN wisp_labels al ON w.id = al.issue_id "+
			"GROUP BY w.id, w.title, w.description, w.status, w.priority, w.assignee, w.created_at, w.updated_at",
		sqlStringList(fromLabels))
	return m.runWispSQL(beadsDir, query)
}

// wispSQLRow represents a row from the wisps SQL query with aggregated labels.
type wispSQLRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Priority    int    `json:"priority"`
	Assignee    string `json:"assignee"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	LabelsCSV   string `json:"labels_csv"`
	AssigneeHit int    `json:"assignee_match"`
	CCHit       int    `json:"cc_match"`
}

// runWispSQL executes a bd sql --json query and converts results to wisp query messages.
func (m *Mailbox) runWispSQL(beadsDir, query string) ([]wispQueryMessage, error) {
	args := []string{"sql", "--json", query}
	ctx, cancel := bdReadCtx()
	stdout, err := runBdCommand(ctx, args, m.workDir, beadsDir)
	cancel()
	if err != nil {
		return nil, err // Wisps table may not exist yet.
	}
	if !isJSON(stdout) {
		return nil, nil
	}

	var rows []wispSQLRow
	if err := json.Unmarshal(stdout, &rows); err != nil {
		return nil, err
	}

	msgs := make([]wispQueryMessage, 0, len(rows))
	for _, row := range rows {
		bm := BeadsMessage{
			ID:          row.ID,
			Title:       row.Title,
			Description: row.Description,
			Status:      row.Status,
			Priority:    row.Priority,
			Assignee:    row.Assignee,
			Wisp:        true,
		}
		if t, ok := parseWispTimestamp(row.CreatedAt); ok {
			bm.CreatedAt = t
		}
		if row.LabelsCSV != "" {
			bm.Labels = strings.Split(row.LabelsCSV, ",")
		}
		msgs = append(msgs, wispQueryMessage{
			message:       bm,
			assigneeMatch: row.AssigneeHit != 0,
			ccMatch:       row.CCHit != 0,
		})
	}
	return msgs, nil
}

func parseWispTimestamp(value string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02 15:04:05 -0700 MST",
		// Dolt/MySQL DATETIME values are emitted without a zone; Gas Town runs
		// managed Dolt servers in UTC, so treat bare timestamps as UTC.
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func sqlStringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+escapeSQLString(value)+"'")
	}
	return strings.Join(quoted, ",")
}

// escapeSQLString escapes backslashes and single quotes for Dolt/MySQL SQL string literals.
func escapeSQLString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "'", "''")
}

// identityVariants returns all identity formats to query.
// For town-level agents (mayor/, deacon/), also includes the variant without
// trailing slash for backwards compatibility with legacy messages.
func (m *Mailbox) identityVariants() []string {
	// Town-level agents may have legacy messages without the trailing slash.
	// beads.AgentAddressForms is the single place that knows which addresses
	// have more than one form; keeping a private copy here is how gt hook and
	// gt mail came to disagree about "deacon" vs "deacon/" in the first place.
	variants := beads.AgentAddressForms(m.identity)
	if len(variants) == 0 {
		return []string{m.identity}
	}
	return variants
}

func (m *Mailbox) listLegacy() ([]*Message, error) {
	file, err := os.Open(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return make([]*Message, 0), nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }() // non-fatal: OS will close on exit

	messages := make([]*Message, 0)
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, fmt.Errorf("corrupt mailbox %s line %d: %w", m.path, lineNum, err)
		}
		messages = append(messages, &msg)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Sort by priority (higher first), then timestamp (newest first).
	sort.Slice(messages, func(i, j int) bool {
		pi, pj := PriorityToBeads(messages[i].Priority), PriorityToBeads(messages[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return messages[i].Timestamp.After(messages[j].Timestamp)
	})

	return messages, nil
}

// ListUnread returns unread (open) messages.
// Filters out messages marked as read (via "read" label in beads mode).
func (m *Mailbox) ListUnread() ([]*Message, error) {
	all, err := m.List()
	if err != nil {
		return nil, err
	}
	unread := make([]*Message, 0)
	for _, msg := range all {
		if !msg.Read {
			unread = append(unread, msg)
		}
	}
	return unread, nil
}

// Get returns a message by ID.
func (m *Mailbox) Get(id string) (*Message, error) {
	if m.legacy {
		return m.getLegacy(id)
	}
	return m.getBeads(id)
}

func (m *Mailbox) getBeads(id string) (*Message, error) {
	// Resolve correct beadsDir based on bead ID prefix (GH#2423)
	primary := beads.ResolveBeadsDirForID(m.beadsDir, id)
	msg, err := m.getFromDir(id, primary)
	if errors.Is(err, ErrMessageNotFound) && primary != m.beadsDir {
		// Cross-rig bead IDs (e.g. ne-*) may live in the home DB when created
		// via the mail router (which always uses town beads). Fall back to
		// m.beadsDir before giving up. See ne-bgr.
		return m.getFromDir(id, m.beadsDir)
	}
	return msg, err
}

// getFromDir retrieves a message from a beads directory.
func (m *Mailbox) getFromDir(id, beadsDir string) (*Message, error) {
	if m.store != nil {
		return m.storeGetFromDir(id)
	}

	args := []string{"show", id, "--json"}

	ctx, cancel := bdReadCtx()
	defer cancel()
	stdout, err := runBdCommand(ctx, args, m.workDir, beadsDir)
	if err != nil {
		if bdErr, ok := err.(*bdError); ok && (bdErr.ContainsError("not found") || bdErr.ContainsError("no issue found") || bdErr.ContainsError("no issue found")) {
			return nil, ErrMessageNotFound
		}
		return nil, err
	}

	// bd show --json returns an array
	if !isJSON(stdout) {
		return nil, ErrMessageNotFound
	}
	var bms []BeadsMessage
	if err := json.Unmarshal(stdout, &bms); err != nil {
		return nil, err
	}
	if len(bms) == 0 {
		return nil, ErrMessageNotFound
	}

	// Wisp status comes from beads issue.wisp field via ToMessage()
	return bms[0].ToMessage(), nil
}

func (m *Mailbox) getLegacy(id string) (*Message, error) {
	messages, err := m.List()
	if err != nil {
		return nil, err
	}
	for _, msg := range messages {
		if msg.ID == id {
			return msg, nil
		}
	}
	return nil, ErrMessageNotFound
}

// MarkRead marks a message as read.
func (m *Mailbox) MarkRead(id string) error {
	if m.legacy {
		return m.markReadLegacy(id)
	}
	return m.markReadBeads(id)
}

func (m *Mailbox) markReadBeads(id string) error {
	if err := m.acknowledgeDeliveryForPrimary(id, nil); err != nil {
		return err
	}
	return m.closeMessage(id)
}

// closeMessage closes a message bead, resolving the beads directory from the
// bead ID prefix, and confirms the message actually left the mailbox.
func (m *Mailbox) closeMessage(id string) error {
	// Resolve correct beadsDir based on bead ID prefix (GH#2423)
	primary := beads.ResolveBeadsDirForID(m.beadsDir, id)
	err := m.closeInDir(id, primary)
	if errors.Is(err, ErrMessageNotFound) && primary != m.beadsDir {
		// Cross-rig bead IDs (e.g. ne-*) may live in the home DB when created
		// via the mail router (which always uses town beads). Fall back to
		// m.beadsDir before giving up. See ne-bgr.
		err = m.closeInDir(id, m.beadsDir)
	}
	if err != nil {
		return err
	}
	return m.confirmClosed(id)
}

// ErrCloseNotApplied reports a close that returned success without taking
// effect: the bead is still readable in this mailbox with an unclosed status.
var ErrCloseNotApplied = errors.New("close reported success but the message is still open")

// confirmClosed re-reads a message after its close returned nil and reports a
// close that did not take.
//
// This is a SECOND observation of the effect, made with a different query than
// the one that claimed it, and that is the whole point. `gt mail archive`
// printed "✓ Archived 1 of 1 message" and exited 0 for a message that was still
// in the inbox unread a cycle later with its wisp status=open — because success
// was read off the return of the close and never off the mailbox (gt-1t0v #4,
// carried to gt-khq8). Every path that could make that close a no-op — a write
// routed to a different store than the read, an ownership guard answered
// leniently, a wisp row untouched by an issues-table update — is invisible from
// where the caller stands, and they all end here.
//
// Fails OPEN on anything that is not a clear "still open": a re-read that errors
// leaves the close reported as it was. The check exists to catch a silent no-op,
// and turning an unreadable store into a spurious archive failure would trade
// one wrong answer for another.
func (m *Mailbox) confirmClosed(id string) error {
	msg, err := m.Get(id)
	if err != nil {
		// Gone is the outcome we wanted; anything else is unproven, not failed.
		return nil
	}
	if msg == nil || msg.Status == "" {
		// No status to judge — older read paths leave it empty (see Message.Status).
		return nil
	}
	if isClosedStatus(msg.Status) {
		return nil
	}
	return fmt.Errorf("%w: %s is still %q in %s", ErrCloseNotApplied, id, msg.Status, m.identity)
}

// isClosedStatus reports whether a bead status means the message has left the
// inbox. The inbox lists "open" and "hooked" (see queryWispMessages and
// listFromDir), so those two are the states a close must have moved away from;
// anything else is treated as gone rather than as a failure, so a status this
// code has not seen before cannot manufacture an archive error.
func isClosedStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "open", "hooked":
		return false
	}
	return true
}

// closeInDir closes a message in a specific beads directory.
func (m *Mailbox) closeInDir(id, beadsDir string) error {
	if m.store != nil {
		return m.storeCloseInDir(id)
	}

	args := []string{"close", id}
	// Act under this mailbox's canonical identity. bd's ownership guard compares
	// the acting identity against the bead's assignee, and mail beads are always
	// assigned the canonical form (AddressToIdentity, e.g. "gastown/toast"). The
	// ambient BD_ACTOR is the agent's role path ("gastown/polecats/toast"), which
	// is the right actor for work beads but never matches a mail bead — so
	// without this every polecat and crew agent was refused when archiving its
	// own mail, and had to reach for --force. See gt-n3gj.
	if actor := actorForIdentity(m.identity); actor != "" {
		args = append(args, "--actor="+actor)
	}
	// Pass session ID for work attribution if available
	if sessionID := runtime.SessionIDFromEnv(); sessionID != "" {
		args = append(args, "--session="+sessionID)
	}
	if m.forceClose {
		args = append(args, "--force")
	}

	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err := runBdCommand(ctx, args, m.workDir, beadsDir)
	telemetry.RecordMailMessage(context.Background(), "read", telemetry.MailMessageInfo{
		ID: id,
		To: m.identity,
	}, err)
	if err != nil {
		if isBdNotFound(err) {
			return ErrMessageNotFound
		}
		return err
	}

	return nil
}

// actorForIdentity returns the bd actor a mailbox should act under, or "" when
// the mailbox has no agent identity to act as.
//
// The identity is re-normalized because not every Mailbox constructor runs it
// through AddressToIdentity, and normalization is idempotent. Queue, channel and
// announce mailboxes are shared destinations rather than agents: their beads are
// assigned "queue:name" and friends, which is not an actor, so they keep the
// ambient BD_ACTOR and the real agent stays in the audit trail.
func actorForIdentity(identity string) string {
	if identity == "" {
		return ""
	}
	for _, prefix := range []string{"queue:", "channel:", "announce:", "list:", "group:"} {
		if strings.HasPrefix(identity, prefix) {
			return ""
		}
	}
	return AddressToIdentity(identity)
}

func (m *Mailbox) markReadLegacy(id string) error {
	fl, err := m.lockLegacy()
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	messages, err := m.List()
	if err != nil {
		return err
	}

	found := false
	for _, msg := range messages {
		if msg.ID == id {
			msg.Read = true
			found = true
		}
	}

	if !found {
		return ErrMessageNotFound
	}

	return m.rewriteLegacy(messages)
}

// MarkReadOnly marks a message as read WITHOUT archiving/closing it.
// For beads mode, this adds a "read" label to the message.
// For legacy mode, this sets the Read field to true.
// The message remains in the inbox but is displayed as read.
func (m *Mailbox) MarkReadOnly(id string) error {
	if m.legacy {
		return m.markReadLegacy(id)
	}
	return m.markReadOnlyBeads(id)
}

func (m *Mailbox) markReadOnlyBeads(id string) error {
	if err := m.acknowledgeDeliveryForPrimary(id, nil); err != nil {
		return err
	}

	// Add "read" label to mark as read without closing
	return m.addLabel(id, "read")
}

// ReadResult reports which path a read took.
type ReadResult int

const (
	// ReadKeptOpen means the message was marked read and left in the work
	// queue: something is still owed, so only the sender's answer or an
	// explicit `gt mail archive` closes it.
	ReadKeptOpen ReadResult = iota
	// ReadClosed means reading consumed the message and the bead was closed.
	ReadClosed
)

// MarkReadConsumed marks a message read, closing it when reading consumes it
// and leaving it open otherwise. Pass an already-fetched msg to reuse it; nil
// fetches one.
//
// This is the read path the CLI should call. MarkRead (always closes) and
// MarkReadOnly (never closes) are the two halves it chooses between, and
// neither is right on its own: MarkRead run over an inbox closes unanswered
// questions, and MarkReadOnly is why 520 acknowledged messages were still
// sitting open in the work queue (gt-qffl).
//
// Closing needs three things to be true, beyond ConsumedByReading's judgement
// of the message itself. All three fail closed, which here means falling back
// to the pre-existing mark-read-only behaviour — never an error, never a
// message lost:
//
//   - Beads mode. Legacy mailboxes have no bead to close.
//   - This mailbox is the addressee, not a CC. A CC copy is a second view of
//     ONE bead, so closing it would clear the addressee's obligation on their
//     behalf; a CC'd reader clears its own copy with DismissCC. Same rule
//     DeleteWithResult follows.
//   - The bead is open. The inbox deliberately lists hooked mail (handoff
//     context is auto-hooked on arrival), and handoff is a type reading
//     consumes — so without this check, `gt mail mark-read --all` would close
//     the very bead `gt hook` reads the successor's context out of.
func (m *Mailbox) MarkReadConsumed(id string, msg *Message) (ReadResult, error) {
	if m.legacy {
		return ReadKeptOpen, m.markReadLegacy(id)
	}

	if msg == nil {
		fetched, err := m.Get(id)
		if err != nil {
			// A message that cannot be read cannot be judged. Marking it read
			// is what the caller asked for and is not destructive, so let the
			// mark-read-only path report its own outcome rather than failing
			// the read over a classification we could not make.
			return ReadKeptOpen, m.markReadOnlyBeads(id)
		}
		msg = fetched
	}

	if !m.consumesOnRead(msg) {
		return ReadKeptOpen, m.markReadOnlyBeads(id)
	}

	// markReadBeads acks delivery and then closes, in that order: the ack is
	// what records that this identity received it, and it must land before the
	// bead stops being open.
	if err := m.markReadBeads(id); err != nil {
		return ReadKeptOpen, err
	}
	return ReadClosed, nil
}

// consumesOnRead reports whether THIS mailbox reading msg consumes it. See
// MarkReadConsumed for why each condition is here.
func (m *Mailbox) consumesOnRead(msg *Message) bool {
	if msg == nil {
		return false
	}
	if m.IsCCOnly(msg) {
		return false
	}
	if msg.Status != "open" {
		return false
	}
	return ConsumedByReading(msg)
}

// addLabel adds a label to a message bead, resolving the beads directory from
// the bead ID prefix and falling back to the home DB for cross-rig IDs.
//
// Labels are metadata on the mail record, not a change of ownership: adding one
// does not close the bead, reassign it, or otherwise touch the assignee's
// obligation. That is what lets a CC'd recipient clear its own copy.
func (m *Mailbox) addLabel(id, label string) error {
	if m.store != nil {
		return m.storeAddLabel(id, label)
	}

	args := []string{"label", "add", id, label}
	primary := beads.ResolveBeadsDirForID(m.beadsDir, id)

	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err := runBdCommand(ctx, args, m.workDir, primary)
	if err != nil {
		if isBdNotFound(err) {
			if primary != m.beadsDir {
				// Cross-rig bead IDs (e.g. ne-*) may live in the home DB. See ne-bgr.
				ctx2, cancel2 := bdWriteCtx()
				defer cancel2()
				_, err2 := runBdCommand(ctx2, args, m.workDir, m.beadsDir)
				if err2 != nil {
					if isBdNotFound(err2) {
						return ErrMessageNotFound
					}
					return err2
				}
				return nil
			}
			return ErrMessageNotFound
		}
		return err
	}

	return nil
}

// acknowledgeDeliveryForPrimary acks delivery for a message addressed to this
// mailbox. Pass an already-fetched msg to reuse it; nil fetches one.
func (m *Mailbox) acknowledgeDeliveryForPrimary(id string, msg *Message) error {
	if m.legacy {
		return nil
	}
	if m.store != nil {
		return m.storeAcknowledgeDeliveryForPrimary(id)
	}

	if msg == nil {
		fetched, err := m.Get(id)
		if err != nil {
			return err
		}
		msg = fetched
	}
	if msg == nil || msg.DeliveryState == "" || AddressToIdentity(msg.To) != m.identity {
		return nil
	}
	return AcknowledgeDeliveryBead(m.workDir, m.beadsDir, id, m.identity)
}

func isBdNotFound(err error) bool {
	bdErr, ok := err.(*bdError)
	return ok && (bdErr.ContainsError("not found") || bdErr.ContainsError("no issue found"))
}

// MarkUnreadOnly marks a message as unread (removes "read" label).
// For beads mode, this removes the "read" label from the message.
// For legacy mode, this sets the Read field to false.
func (m *Mailbox) MarkUnreadOnly(id string) error {
	if m.legacy {
		return m.markUnreadLegacy(id)
	}
	return m.markUnreadOnlyBeads(id)
}

func (m *Mailbox) markUnreadOnlyBeads(id string) error {
	if m.store != nil {
		return m.storeMarkUnreadOnly(id)
	}

	// Remove "read" label to mark as unread
	args := []string{"label", "remove", id, "read"}
	primary := beads.ResolveBeadsDirForID(m.beadsDir, id)

	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err := runBdCommand(ctx, args, m.workDir, primary)
	if err != nil {
		if isBdNotFound(err) {
			if primary != m.beadsDir {
				// Cross-rig bead IDs (e.g. ne-*) may live in the home DB. See ne-bgr.
				ctx2, cancel2 := bdWriteCtx()
				defer cancel2()
				_, err2 := runBdCommand(ctx2, args, m.workDir, m.beadsDir)
				if err2 != nil {
					if isBdNotFound(err2) {
						return ErrMessageNotFound
					}
					if bdErr2, ok := err2.(*bdError); ok && bdErr2.ContainsError("does not have label") {
						return nil
					}
					return err2
				}
				return nil
			}
			return ErrMessageNotFound
		}
		// Ignore error if label doesn't exist
		if bdErr, ok := err.(*bdError); ok && bdErr.ContainsError("does not have label") {
			return nil
		}
		return err
	}

	return nil
}

// MarkUnread marks a message as unread (reopens in beads).
func (m *Mailbox) MarkUnread(id string) error {
	if m.legacy {
		return m.markUnreadLegacy(id)
	}
	return m.markUnreadBeads(id)
}

func (m *Mailbox) markUnreadBeads(id string) error {
	if m.store != nil {
		return m.storeMarkUnread(id)
	}

	args := []string{"reopen", id}
	primary := beads.ResolveBeadsDirForID(m.beadsDir, id)

	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err := runBdCommand(ctx, args, m.workDir, primary)
	if err != nil {
		if isBdNotFound(err) {
			if primary != m.beadsDir {
				// Cross-rig bead IDs (e.g. ne-*) may live in the home DB. See ne-bgr.
				ctx2, cancel2 := bdWriteCtx()
				defer cancel2()
				_, err2 := runBdCommand(ctx2, args, m.workDir, m.beadsDir)
				if err2 != nil {
					if isBdNotFound(err2) {
						return ErrMessageNotFound
					}
					return err2
				}
				return nil
			}
			return ErrMessageNotFound
		}
		return err
	}

	return nil
}

func (m *Mailbox) markUnreadLegacy(id string) error {
	fl, err := m.lockLegacy()
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	messages, err := m.List()
	if err != nil {
		return err
	}

	found := false
	for _, msg := range messages {
		if msg.ID == id {
			msg.Read = false
			found = true
		}
	}

	if !found {
		return ErrMessageNotFound
	}

	return m.rewriteLegacy(messages)
}

// Delete removes a message from this inbox.
//
// For a message addressed to this mailbox, that means closing the bead. For a CC
// copy it means dismissing this recipient's copy only: the bead belongs to the
// To: recipient, the ownership guard correctly refuses to let anyone else close
// it, and without this branch a CC'd recipient had no legitimate way to clear
// its inbox at all. See gt-58s.
func (m *Mailbox) Delete(id string) error {
	_, err := m.DeleteWithResult(id)
	return err
}

// DeleteResult describes how a message left the inbox, so callers can report
// accurately without a second lookup.
type DeleteResult int

const (
	// DeleteClosed means the message bead was closed (it was addressed here).
	DeleteClosed DeleteResult = iota
	// DeleteCCCleared means only this recipient's CC copy was dismissed; the
	// bead remains open and still belongs to its assignee.
	DeleteCCCleared
)

// DeleteWithResult removes a message from this inbox and reports which path it
// took. See Delete for the CC-copy rationale.
func (m *Mailbox) DeleteWithResult(id string) (DeleteResult, error) {
	if m.legacy {
		return DeleteClosed, m.deleteLegacy(id)
	}

	// One lookup serves both decisions: whether this is a CC copy to dismiss, and
	// the delivery ack that precedes a close. Its failure is the same failure the
	// close path would report (the ack fetches the same message), including the
	// ErrMessageNotFound that callers treat as "already gone".
	msg, err := m.Get(id)
	if err != nil {
		return DeleteClosed, err
	}
	// --force is an explicit decision to close the record itself, so it is not
	// diverted to a CC dismissal.
	if !m.forceClose && m.IsCCOnly(msg) {
		return DeleteCCCleared, m.DismissCC(id)
	}
	if ackErr := m.acknowledgeDeliveryForPrimary(id, msg); ackErr != nil {
		return DeleteClosed, ackErr
	}
	return DeleteClosed, m.closeMessage(id)
}

func (m *Mailbox) deleteLegacy(id string) error {
	fl, err := m.lockLegacy()
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	messages, err := m.List()
	if err != nil {
		return err
	}

	var filtered []*Message
	found := false
	for _, msg := range messages {
		if msg.ID == id {
			found = true
		} else {
			filtered = append(filtered, msg)
		}
	}

	if !found {
		return ErrMessageNotFound
	}

	return m.rewriteLegacy(filtered)
}

// Archive moves a message to the archive file and removes it from inbox.
//
// Archive is a mail cleanup operation, not a bead operation. If the
// underlying bead has been garbage collected (by `bd mol wisp gc` or
// `bd compact`), there is nothing to append to the archive and nothing
// to close — we still return nil so the caller's inbox reference is
// considered cleared. See aa-6hv.
func (m *Mailbox) Archive(id string) error {
	if m.legacy {
		return m.archiveLegacy(id)
	}
	// Beads mode: append to archive then close
	msg, err := m.Get(id)
	if err != nil {
		if errors.Is(err, ErrMessageNotFound) {
			// Underlying bead has been GC'd; nothing to archive or close.
			return nil
		}
		return err
	}
	if err := m.appendToArchive(msg); err != nil {
		return err
	}
	if err := m.Delete(id); err != nil {
		if errors.Is(err, ErrMessageNotFound) {
			// Bead was GC'd between Get and Delete; metadata is archived,
			// and there is nothing left to close.
			return nil
		}
		return err
	}
	return nil
}

// archiveLegacy moves a message to the archive file atomically.
// A single flock covers the entire read-archive-rewrite cycle so that
// a crash between appendToArchive and the inbox rewrite cannot lose the
// message (worst case: duplicate in both archive and inbox).
func (m *Mailbox) archiveLegacy(id string) error {
	fl, err := m.lockLegacy()
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	// Read inbox
	messages, err := m.listLegacy()
	if err != nil {
		return err
	}

	// Find and extract target
	var target *Message
	var remaining []*Message
	for _, msg := range messages {
		if msg.ID == id {
			target = msg
		} else {
			remaining = append(remaining, msg)
		}
	}
	if target == nil {
		return ErrMessageNotFound
	}

	// Append to archive first (safe failure mode: duplicate, not loss)
	if err := m.appendToArchive(target); err != nil {
		return err
	}

	// Rewrite inbox without the target
	return m.rewriteLegacy(remaining)
}

// ArchivePath returns the path to the archive file.
func (m *Mailbox) ArchivePath() string {
	if m.legacy {
		return m.path + ".archive"
	}
	// For beads, use archive.jsonl in the same directory as beads
	return filepath.Join(m.beadsDir, "archive.jsonl")
}

func (m *Mailbox) appendToArchive(msg *Message) error {
	archivePath := m.ArchivePath()

	// Ensure directory exists
	dir := filepath.Dir(archivePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Open for append
	file, err := os.OpenFile(archivePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec // G302: archive is non-sensitive operational data
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	_, err = file.WriteString(string(data) + "\n")
	return err
}

// ListArchived returns all messages in the archive file.
func (m *Mailbox) ListArchived() ([]*Message, error) {
	archivePath := m.ArchivePath()

	file, err := os.Open(archivePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var messages []*Message
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, fmt.Errorf("corrupt archive %s line %d: %w", archivePath, lineNum, err)
		}
		messages = append(messages, &msg)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return messages, nil
}

// ListArchivedMail returns mail addressed to this identity that has left its
// inbox.
//
// Archived mail lives in two places, and neither of them is the inbox:
//
//   - The message bead, CLOSED. `gt mail archive` closes the bead where it
//     stands (runMailArchive → Mailbox.Delete); it does not move it anywhere.
//     A closed bead is precisely what the inbox query filters out, so this is
//     where an agent's own archived mail is and why an inbox-scoped search
//     cannot see it.
//   - archive.jsonl, appended to by the dog dispatch path (Mailbox.Archive).
//     That file is per-BEADS-DIRECTORY, not per-identity, so its entries are
//     filtered here by recipient. Reading it unfiltered handed a polecat whose
//     inbox held one message 56832 search results, none of the 56832 addressed
//     to it (gt-7gvk).
//
// A store that cannot be read is skipped rather than fatal: partial archived
// mail is worth more than none, and the caller reports what it got.
func (m *Mailbox) ListArchivedMail() ([]*Message, error) {
	if m.legacy {
		// The legacy archive file belongs to one mailbox, so it is already
		// recipient-scoped and needs no filtering.
		archived, err := m.ListArchived()
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return archived, nil
	}

	identities := m.identityVariants()
	seen := make(map[string]bool)
	var messages []*Message

	// Closed message beads: the archive proper.
	//
	// The query asks for --all and the status test happens here rather than
	// asking bd for --status closed, because status and pinning are
	// independent in beads: on the live store --status closed returned 1864
	// message beads and --status closed --pinned another 53 that the first
	// query did not include. --all cannot miss a pinned row.
	//
	// isClosedStatus is the archive's half of the inbox's own predicate — it
	// answers "has this left the inbox", which is the question here, and
	// sharing it is what keeps the two halves from drifting into disagreeing
	// about which statuses the inbox shows.
	byAssignee, err := m.queryIssueMessagesByAssignee(m.beadsDir, identities, "--all")
	if err != nil {
		return nil, err
	}
	messages = appendMessagesMatching(messages, seen, byAssignee, isClosedStatus)

	// CC copies are archived DIFFERENTLY, and testing them for a closed status
	// would hide every one of them.
	//
	// A CC'd message is not this recipient's to close — the bead belongs to its
	// assignee — so archiving a CC copy adds cc-cleared:<me> and leaves the bead
	// OPEN. That is rejected by both of the inbox's filters and by the status
	// test above, which would put archived CC mail in no store at all: exactly
	// the disappearance this change exists to fix, reproduced one level down.
	byCC := m.queryIssueMessagesByCC(m.beadsDir, identities, "--all")
	for i := range byCC {
		bm := &byCC[i]
		if seen[bm.ID] {
			continue
		}
		if !ccClearedFor(bm, identities) && !isClosedStatus(bm.Status) {
			continue
		}
		seen[bm.ID] = true
		messages = append(messages, bm.ToMessage())
	}

	// Wisp messages. No status restriction in SQL, for the CC reason above: an
	// archived CC copy is open, so the decision has to happen here. Anything
	// still in the inbox was collected before this and is in seen.
	//
	// The wisps table may not exist, which runWispSQL reports as an error; a
	// mailbox with no wisps still has an archive.
	if wisps, err := m.queryWispMessagesWithStatus(m.beadsDir, identities, "1 = 1"); err == nil {
		for i := range wisps {
			w := &wisps[i]
			bm := &w.message
			if seen[bm.ID] {
				continue
			}
			archivedCC := w.ccMatch && ccClearedFor(bm, identities)
			if !isClosedStatus(bm.Status) && !archivedCC {
				continue
			}
			seen[bm.ID] = true
			messages = append(messages, bm.ToMessage())
		}
	}

	// The flat archive file, filtered to this identity.
	fileArchived, err := m.ListArchived()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, msg := range fileArchived {
		if msg == nil || seen[msg.ID] || !m.addressesThisMailbox(msg) {
			continue
		}
		seen[msg.ID] = true
		messages = append(messages, msg)
	}

	return messages, nil
}

// ListSent returns mail this identity sent.
//
// Sent mail is addressed to somebody else, so no query over this mailbox's
// recipients reaches it. It is selected by the from:<sender> label that the
// send path stamps on every message instead. Status is not restricted: mail
// stays worth finding after its recipient closes it, and whether it has been
// closed is not the sender's business.
func (m *Mailbox) ListSent() ([]*Message, error) {
	if m.legacy {
		// Legacy JSONL mailboxes hold delivered mail only; nothing records
		// what this mailbox sent, so say so rather than answering an
		// unqualified zero.
		return nil, errors.New("legacy JSONL mailboxes keep no record of sent mail")
	}

	senders := m.senderAddressForms()
	seen := make(map[string]bool)
	var messages []*Message

	for _, id := range senders {
		sent, err := m.queryIssueMessagesByLabels(m.beadsDir, []string{"gt:message", "from:" + id}, "--all")
		if err != nil {
			return nil, err
		}
		messages = appendMessagesMatching(messages, seen, sent, nil)
	}

	if wisps, err := m.queryWispMessagesFrom(m.beadsDir, senders); err == nil {
		for i := range wisps {
			bm := &wisps[i].message
			if seen[bm.ID] {
				continue
			}
			seen[bm.ID] = true
			messages = append(messages, bm.ToMessage())
		}
	}

	return messages, nil
}

// senderAddressForms returns the address forms this mailbox's OUTGOING mail may
// be labelled with, canonical-identity form first.
//
// It is deliberately wider than identityVariants, and the reason is measured
// rather than defensive: the send path writes the two ends of one message in
// DIFFERENT forms. A message this polecat sent to itself came back assigned
// "gastown/fury" — the collapsed form AddressToIdentity produces — and labelled
// "from:gastown/polecats/fury", the nested form the caller passed. So a
// recipient query keyed on the mailbox identity matches that message while a
// from: query keyed on the same string returns 0. That is a fresh blind zero of
// exactly the kind this command was fixed to stop producing, and it was found
// by running the reported control end to end rather than by reading the code.
//
// The forms come from what the caller actually supplied, never from
// reconstructing them out of the identity: collapsing is lossy, and expanding
// "gastown/fury" back would mean guessing between "crew" and "polecats" — a
// guess that would silently pull in a different agent's sent mail whenever both
// exist under one name.
func (m *Mailbox) senderAddressForms() []string {
	var forms []string
	seen := make(map[string]bool)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		forms = append(forms, value)
	}

	for _, variant := range m.identityVariants() {
		add(variant)
	}
	if m.address != "" {
		add(m.address)
		add(AddressToIdentity(m.address))
		for _, variant := range beads.AgentAddressForms(m.address) {
			add(variant)
		}
	}
	return forms
}

// addressesThisMailbox reports whether msg was delivered to this identity,
// either directly or as a CC.
//
// Comparison goes through beads.AgentAddressKey because the two sides are
// written by different conventions: a mailbox identity keeps its container
// segment ("gastown/polecats/fury") while a delivered message records the
// canonical address ("gastown/fury"). Comparing the strings as written
// discards every message.
func (m *Mailbox) addressesThisMailbox(msg *Message) bool {
	key := beads.AgentAddressKey(m.identity)
	if key == "" {
		return false
	}
	if beads.AgentAddressKey(msg.To) == key {
		return true
	}
	for _, cc := range msg.CC {
		if beads.AgentAddressKey(cc) == key {
			return true
		}
	}
	return false
}

// appendMessagesMatching appends the beads messages whose status satisfies
// keep, skipping IDs already seen. A nil keep accepts every status.
func appendMessagesMatching(messages []*Message, seen map[string]bool, msgs []BeadsMessage, keep func(status string) bool) []*Message {
	for i := range msgs {
		bm := &msgs[i]
		if seen[bm.ID] {
			continue
		}
		if keep != nil && !keep(bm.Status) {
			continue
		}
		seen[bm.ID] = true
		messages = append(messages, bm.ToMessage())
	}
	return messages
}

// queryIssueMessagesByLabels lists message beads carrying ALL of the given
// labels. Used for the from: selection that sent mail needs.
func (m *Mailbox) queryIssueMessagesByLabels(beadsDir string, labels []string, extra ...string) ([]BeadsMessage, error) {
	args := []string{"list", "--json", "--limit", "0"}
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	args = append(args, extra...)

	ctx, cancel := bdReadCtx()
	stdout, err := runBdCommand(ctx, args, m.workDir, beadsDir)
	cancel()
	if err != nil {
		return nil, err
	}
	return parseBeadsListOutput(stdout)
}

// PurgeArchive removes messages from the archive, optionally filtering by age.
// If olderThanDays is 0, removes all archived messages.
func (m *Mailbox) PurgeArchive(olderThanDays int) (int, error) {
	if m.legacy {
		fl, err := m.lockLegacy()
		if err != nil {
			return 0, err
		}
		defer func() { _ = fl.Unlock() }()
	}

	messages, err := m.ListArchived()
	if err != nil {
		return 0, err
	}

	if len(messages) == 0 {
		return 0, nil
	}

	// If no age filter, remove all
	if olderThanDays <= 0 {
		if err := os.Remove(m.ArchivePath()); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		return len(messages), nil
	}

	// Filter by age
	cutoff := timeNow().AddDate(0, 0, -olderThanDays)
	var keep []*Message
	purged := 0

	for _, msg := range messages {
		if msg.Timestamp.Before(cutoff) {
			purged++
		} else {
			keep = append(keep, msg)
		}
	}

	// Rewrite archive with remaining messages
	if len(keep) == 0 {
		if err := os.Remove(m.ArchivePath()); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
	} else {
		if err := m.rewriteArchive(keep); err != nil {
			return 0, err
		}
	}

	return purged, nil
}

func (m *Mailbox) rewriteArchive(messages []*Message) error {
	archivePath := m.ArchivePath()
	tmpPath := archivePath + ".tmp"

	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	for _, msg := range messages {
		data, err := json.Marshal(msg)
		if err != nil {
			_ = file.Close()
			_ = os.Remove(tmpPath)
			return err
		}
		if _, err := file.WriteString(string(data) + "\n"); err != nil {
			_ = file.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("writing archive: %w", err)
		}
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, archivePath)
}

// SearchOptions specifies search parameters.
type SearchOptions struct {
	Query       string // Literal text to search for (case-insensitive)
	FromFilter  string // Optional: only match messages from this sender
	SubjectOnly bool   // Only search subject
	BodyOnly    bool   // Only search body

	// IncludeArchived widens the search to mail this identity received and has
	// since archived.
	//
	// It has to be asked for separately because archiving does not move a
	// message anywhere the inbox query can see: `gt mail archive` CLOSES the
	// message bead in place, and a closed bead is exactly what the inbox query
	// filters out. Without this the search is inbox-scoped, so it answers 0 for
	// every term the moment the inbox is empty — including terms in mail read
	// minutes earlier (gt-7gvk).
	IncludeArchived bool

	// IncludeSent widens the search to mail this identity sent.
	//
	// Sent mail is addressed to somebody else, so no query over this mailbox's
	// recipients can reach it; it is selected by its from: label instead.
	// Without it there is no sent-mail surface at all, and a detector that
	// reports across cycles cannot ask whether it already reported something
	// (gt-7gvk).
	IncludeSent bool
}

// SearchScope records which stores a search actually read.
//
// It exists so that a zero result is interpretable. Mail is spread across
// stores that are separate in beads — the inbox is open message beads,
// archived mail is closed ones, sent mail is selected by a from: label, and a
// flat archive.jsonl holds what the dog dispatch path filed — and an empty
// result says nothing at all until the caller knows which of those were
// consulted and which of them answered.
type SearchScope struct {
	Inbox    bool
	Archived bool
	Sent     bool

	// Unavailable names stores that were asked for but could not be read, each
	// with its reason.
	//
	// These are reported rather than returned as an error so that matches from
	// the stores that DID answer still reach the caller. A search that quietly
	// drops one of its stores is a search whose zero is a lie, which is the
	// defect this type was added to close.
	Unavailable []string
}

// SearchResult is the outcome of a search: the matches, and the scope they
// were drawn from.
type SearchResult struct {
	Messages []*Message
	Scope    SearchScope
}

// Search finds messages matching the given criteria.
//
// The inbox is always searched. Archived and sent mail are searched only when
// asked for; see SearchOptions. The returned scope names what was read, so a
// caller can report what a zero covers.
//
// Query and FromFilter are treated as literal strings (not regex) to prevent ReDoS.
func (m *Mailbox) Search(opts SearchOptions) (*SearchResult, error) {
	// Use QuoteMeta to escape special regex chars - prevents ReDoS attacks
	// and provides intuitive literal string matching for users
	re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(opts.Query))
	if err != nil {
		return nil, fmt.Errorf("invalid search pattern: %w", err)
	}

	var fromRe *regexp.Regexp
	if opts.FromFilter != "" {
		fromRe, err = regexp.Compile("(?i)" + regexp.QuoteMeta(opts.FromFilter))
		if err != nil {
			return nil, fmt.Errorf("invalid from pattern: %w", err)
		}
	}

	result := &SearchResult{Scope: SearchScope{Inbox: true}}

	// Get inbox messages
	inbox, err := m.List()
	if err != nil {
		return nil, err
	}

	// A message can legitimately turn up in more than one store — sent mail
	// that was CC'd back to the sender is both sent and received, and a
	// message archived by the dog path is in both archive.jsonl and the closed
	// beads. Collapse by ID so it is reported once.
	seen := make(map[string]bool, len(inbox))
	var candidates []*Message
	collect := func(msgs []*Message) {
		for _, msg := range msgs {
			if msg == nil || seen[msg.ID] {
				continue
			}
			seen[msg.ID] = true
			candidates = append(candidates, msg)
		}
	}
	collect(inbox)

	if opts.IncludeArchived {
		archived, err := m.ListArchivedMail()
		if err != nil {
			result.Scope.Unavailable = append(result.Scope.Unavailable, fmt.Sprintf("archived: %v", err))
		} else {
			result.Scope.Archived = true
			collect(archived)
		}
	}

	if opts.IncludeSent {
		sent, err := m.ListSent()
		if err != nil {
			result.Scope.Unavailable = append(result.Scope.Unavailable, fmt.Sprintf("sent: %v", err))
		} else {
			result.Scope.Sent = true
			collect(sent)
		}
	}

	var matches []*Message
	for _, msg := range candidates {
		// Apply from filter
		if fromRe != nil && !fromRe.MatchString(msg.From) {
			continue
		}

		// Search in specified fields
		matched := false
		if opts.SubjectOnly {
			matched = re.MatchString(msg.Subject)
		} else if opts.BodyOnly {
			matched = re.MatchString(msg.Body)
		} else {
			// Search in both subject and body
			matched = re.MatchString(msg.Subject) || re.MatchString(msg.Body)
		}

		if matched {
			matches = append(matches, msg)
		}
	}

	// Sort by priority (higher first), then timestamp (newest first).
	sort.Slice(matches, func(i, j int) bool {
		pi, pj := PriorityToBeads(matches[i].Priority), PriorityToBeads(matches[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return matches[i].Timestamp.After(matches[j].Timestamp)
	})

	result.Messages = matches
	return result, nil
}

// Count returns the total and unread message counts.
func (m *Mailbox) Count() (total, unread int, err error) {
	messages, err := m.List()
	if err != nil {
		return 0, 0, err
	}

	total = len(messages)
	// Count messages that are NOT marked as read (including via "read" label)
	for _, msg := range messages {
		if !msg.Read {
			unread++
		}
	}

	return total, unread, nil
}

// AcknowledgeDeliveries marks delivery receipt for unread messages where this
// mailbox is the primary recipient. This is phase-2 of two-phase delivery
// tracking (phase-1 is written at send time as delivery:pending).
// Acks are run concurrently (bounded to 8) to avoid N+1 sequential subprocess
// spawns on the hot path.
func (m *Mailbox) AcknowledgeDeliveries(recipientAddress string, messages []*Message) error {
	if m.legacy || len(messages) == 0 {
		return nil
	}

	recipientIdentity := AddressToIdentity(recipientAddress)

	// Collect messages that need acking.
	var toAck []*Message
	for _, msg := range messages {
		if msg == nil || msg.ID == "" {
			continue
		}
		if AddressToIdentity(msg.To) != recipientIdentity {
			continue
		}
		if msg.DeliveryState == "" {
			continue
		}
		toAck = append(toAck, msg)
	}
	if len(toAck) == 0 {
		return nil
	}

	// Run acks concurrently with bounded parallelism.
	const maxConcurrentAckOps = 8
	sem := make(chan struct{}, maxConcurrentAckOps)
	var mu sync.Mutex
	var errs []string
	var wg sync.WaitGroup

	for _, msg := range toAck {
		wg.Add(1)
		sem <- struct{}{} // acquire
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }() // release
			if err := AcknowledgeDeliveryBead(m.workDir, m.beadsDir, id, recipientIdentity); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", id, err))
				mu.Unlock()
			}
		}(msg.ID)
	}
	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("acknowledging deliveries failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Append adds a message to the mailbox (legacy mode only).
// For beads mode, use Router.Send() instead.
func (m *Mailbox) Append(msg *Message) error {
	if !m.legacy {
		return errors.New("use Router.Send() to send messages via beads")
	}
	return m.appendLegacy(msg)
}

func (m *Mailbox) appendLegacy(msg *Message) error {
	// Ensure directory exists before acquiring lock (lock file is in same dir)
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	fl, err := m.lockLegacy()
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	// Open for append
	file, err := os.OpenFile(m.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }() // non-fatal: OS will close on exit

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	_, err = file.WriteString(string(data) + "\n")
	return err
}

// rewriteLegacy rewrites the mailbox with the given messages.
func (m *Mailbox) rewriteLegacy(messages []*Message) error {
	// Sort by timestamp (oldest first for JSONL)
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})

	// Write to temp file
	tmpPath := m.path + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	for _, msg := range messages {
		data, err := json.Marshal(msg)
		if err != nil {
			_ = file.Close()       // best-effort cleanup
			_ = os.Remove(tmpPath) // best-effort cleanup
			return err
		}
		if _, err := file.WriteString(string(data) + "\n"); err != nil {
			_ = file.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("writing mailbox: %w", err)
		}
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath) // best-effort cleanup
		return err
	}

	// Atomic rename
	return os.Rename(tmpPath, m.path)
}

// ListByThread returns all messages in a given thread.
func (m *Mailbox) ListByThread(threadID string) ([]*Message, error) {
	if m.legacy {
		return m.listByThreadLegacy(threadID)
	}
	return m.listByThreadBeads(threadID)
}

func (m *Mailbox) listByThreadBeads(threadID string) ([]*Message, error) {
	args := []string{"message", "thread", threadID, "--json"}

	ctx, cancel := bdReadCtx()
	defer cancel()
	stdout, err := runBdCommand(ctx, args, m.workDir, m.beadsDir, "BD_IDENTITY="+m.identity)
	if err != nil {
		return nil, err
	}

	if !isJSON(stdout) {
		return nil, nil
	}
	var beadsMsgs []BeadsMessage
	if err := json.Unmarshal(stdout, &beadsMsgs); err != nil {
		return nil, err
	}

	var messages []*Message
	for _, bm := range beadsMsgs {
		messages = append(messages, bm.ToMessage())
	}

	// Sort by timestamp (oldest first for thread view)
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})

	return messages, nil
}

func (m *Mailbox) listByThreadLegacy(threadID string) ([]*Message, error) {
	messages, err := m.List()
	if err != nil {
		return nil, err
	}

	var thread []*Message
	for _, msg := range messages {
		if msg.ThreadID == threadID {
			thread = append(thread, msg)
		}
	}

	// Sort by timestamp (oldest first for thread view)
	sort.Slice(thread, func(i, j int) bool {
		return thread[i].Timestamp.Before(thread[j].Timestamp)
	})

	return thread, nil
}

// isJSON returns true if the byte slice looks like JSON (starts with [ or {).
// bd list --json may return plain text like "No issues found." instead of JSON
// when there are no results.
func isJSON(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '[', '{':
			return true
		default:
			return false
		}
	}
	return false
}
