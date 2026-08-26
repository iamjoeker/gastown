package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
)

// clearNudgesForMessage removes queued nudges announcing a message the reader
// has just dealt with, so the notification does not outlive the message.
//
// Best-effort and deliberately quiet: DrainLive filters spent notifications at
// delivery time regardless, so a failure here costs nothing but a wasted queue
// entry. Clearing eagerly keeps the queue from counting dead entries against
// its depth cap in the meantime.
func clearNudgesForMessage(address, messageID, threadID string) {
	workDir, err := findMailWorkDir()
	if err != nil {
		return
	}
	if err := mail.NewRouter(workDir).ClearMessageNudges(address, messageID, threadID); err != nil {
		fmt.Fprintf(os.Stderr, "gt mail: could not clear queued nudges for %s: %v\n", messageID, err)
	}
}

// getMailbox returns the mailbox for the given address.
func getMailbox(address string) (*mail.Mailbox, error) {
	// All mail uses town beads (two-level architecture)
	workDir, err := findMailWorkDir()
	if err != nil {
		return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Get mailbox
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(address)
	if err != nil {
		return nil, fmt.Errorf("getting mailbox: %w", err)
	}
	return mailbox, nil
}

func runMailInbox(cmd *cobra.Command, args []string) error {
	// Check for mutually exclusive flags
	if mailInboxAll && mailInboxUnread {
		return errors.New("--all and --unread are mutually exclusive")
	}

	// Determine which inbox to check (priority: --identity flag, positional arg, auto-detect)
	address := ""
	if mailInboxIdentity != "" {
		address = mailInboxIdentity
	} else if len(args) > 0 {
		address = args[0]
	} else {
		address = detectSender()
	}

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// Load the inbox once. Count() and ListUnread() both call List(), so using
	// them here doubles the bd/Dolt reads on the hot patrol path.
	messages, counts, err := loadInboxSnapshot(mailbox, mailbox, mailInboxUnread)
	if err != nil {
		return fmt.Errorf("listing messages: %w", err)
	}

	// JSON output
	if mailInboxJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(messages); err != nil {
			return err
		}
		return nil
	}

	// Human-readable output. CC copies are reported apart from the addressed
	// count so the headline number keeps meaning "work addressed to me" (gt-58s).
	fmt.Printf("%s Inbox: %s (%s)\n\n",
		style.Bold.Render("📬"), address, counts.Summary())

	if len(messages) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no messages)"))
		return nil
	}

	for i, msg := range messages {
		readMarker := "●"
		if msg.Read {
			readMarker = "○"
		}
		// A CC copy is someone else's obligation. Rendering it identically to an
		// addressed message has already produced false misroute reports, because
		// the To: line lives in the body and is invisible here (gt-58s).
		ccMarker := ""
		if mailbox.IsCCOnly(msg) {
			ccMarker = " " + style.Dim.Render("(cc)")
		}
		typeMarker := ""
		if msg.Type != "" && msg.Type != mail.TypeNotification {
			typeMarker = fmt.Sprintf(" [%s]", msg.Type)
		}
		priorityMarker := ""
		if msg.Priority == mail.PriorityHigh || msg.Priority == mail.PriorityUrgent {
			priorityMarker = " " + style.Bold.Render("!")
		}
		wispMarker := ""
		if msg.Wisp {
			wispMarker = " " + style.Dim.Render("(wisp)")
		}

		// Show 1-based index for easy reference with 'gt mail read <n>'
		indexStr := style.Dim.Render(fmt.Sprintf("%d.", i+1))
		fmt.Printf("  %s %s %s%s%s%s%s\n", indexStr, readMarker, msg.Subject, typeMarker, priorityMarker, wispMarker, ccMarker)
		if ccMarker != "" {
			// Name the addressee: a cc reader needs to know whose message this is
			// before deciding whether to act on it.
			fmt.Printf("      %s from %s, to %s\n",
				style.Dim.Render(msg.ID),
				msg.From,
				msg.To)
		} else {
			fmt.Printf("      %s from %s\n",
				style.Dim.Render(msg.ID),
				msg.From)
		}
		// Zone label: see the Date line in readMessage. The list is the surface
		// most often read before a bead is opened, so it needs the label too.
		fmt.Printf("      %s\n",
			style.Dim.Render(msg.Timestamp.Local().Format("2006-01-02 15:04 MST")))
	}

	return nil
}

type inboxLister interface {
	List() ([]*mail.Message, error)
}

// ccClassifier reports whether a message reached this inbox as a CC copy.
type ccClassifier interface {
	IsCCOnly(msg *mail.Message) bool
}

// inboxCounts separates messages addressed to this inbox from CC copies.
//
// The inbox count is the town's main "do I have unprocessed work" signal, and
// mol-witness-patrol expects it near-empty. A CC copy is someone else's
// obligation, so counting it in the headline number makes that signal read high
// for work the reader cannot act on or clear. See gt-58s.
type inboxCounts struct {
	Addressed       int
	AddressedUnread int
	CC              int
	CCUnread        int
}

// Unread returns every unread message, cc copies included. Reading a cc copy is
// legitimate work; only clearing it was broken.
func (c inboxCounts) Unread() int {
	return c.AddressedUnread + c.CCUnread
}

// Summary renders the counts for the inbox header. CC copies appear only when
// present, so the common case reads exactly as it always has.
func (c inboxCounts) Summary() string {
	summary := fmt.Sprintf("%d messages, %d unread", c.Addressed, c.AddressedUnread)
	if c.CC > 0 {
		summary += fmt.Sprintf(", %d cc (%d unread)", c.CC, c.CCUnread)
	}
	return summary
}

func loadInboxSnapshot(mailbox inboxLister, cc ccClassifier, unreadOnly bool) ([]*mail.Message, inboxCounts, error) {
	allMessages, err := mailbox.List()
	if err != nil {
		return nil, inboxCounts{}, err
	}
	if allMessages == nil {
		allMessages = make([]*mail.Message, 0)
	}

	counts := countInboxMessages(allMessages, cc)
	if unreadOnly {
		return filterUnreadMessages(allMessages), counts, nil
	}
	return allMessages, counts, nil
}

func countInboxMessages(messages []*mail.Message, cc ccClassifier) inboxCounts {
	var counts inboxCounts
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if cc != nil && cc.IsCCOnly(msg) {
			counts.CC++
			if !msg.Read {
				counts.CCUnread++
			}
			continue
		}
		counts.Addressed++
		if !msg.Read {
			counts.AddressedUnread++
		}
	}
	return counts
}

func filterUnreadMessages(messages []*mail.Message) []*mail.Message {
	unreadMessages := make([]*mail.Message, 0)
	for _, msg := range messages {
		if msg != nil && !msg.Read {
			unreadMessages = append(unreadMessages, msg)
		}
	}
	return unreadMessages
}

func runMailRead(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("message ID or index required\n\nRun 'gt mail inbox' to list messages and their IDs")
	}
	msgRef := args[0]

	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// Check if the argument is a numeric index (1-based)
	var msgID string
	if idx, err := strconv.Atoi(msgRef); err == nil && idx > 0 {
		// Numeric index: resolve to message ID by listing inbox
		messages, err := mailbox.List()
		if err != nil {
			return fmt.Errorf("listing messages: %w", err)
		}
		if idx > len(messages) {
			return fmt.Errorf("index %d out of range (inbox has %d messages)", idx, len(messages))
		}
		msgID = messages[idx-1].ID
	} else {
		msgID = msgRef
	}

	msg, err := mailbox.Get(msgID)
	if err != nil {
		return fmt.Errorf("getting message: %w", err)
	}

	// Mark as read when viewed. Automated traffic that owes nothing back is
	// closed here; anything that might still be owed keeps its "read" label and
	// stays in the work queue (gt-qffl). Reuse the message already fetched
	// above rather than paying for a second lookup.
	readResult, err := mailbox.MarkReadConsumed(msgID, msg)
	if err != nil {
		// Non-fatal: message was retrieved, just couldn't mark
		style.PrintWarning("could not mark message as read: %v", err)
	} else {
		// "You have new mail" is spent the moment the mail is read (gt-loz6).
		clearNudgesForMessage(address, msgID, msg.ThreadID)
	}

	// JSON output
	if mailReadJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(msg); err != nil {
			return err
		}
		// Ack after output so JSON reflects accurate read-time state.
		if ackErr := mailbox.AcknowledgeDeliveries(address, []*mail.Message{msg}); ackErr != nil {
			fmt.Fprintf(os.Stderr, "gt mail read: delivery ack failed: %v\n", ackErr)
		}
		return nil
	}

	// Human-readable output
	priorityStr := ""
	if msg.Priority == mail.PriorityUrgent {
		priorityStr = " " + style.Bold.Render("[URGENT]")
	} else if msg.Priority == mail.PriorityHigh {
		priorityStr = " " + style.Bold.Render("[HIGH PRIORITY]")
	}

	typeStr := ""
	if msg.Type != "" && msg.Type != mail.TypeNotification {
		typeStr = fmt.Sprintf(" [%s]", msg.Type)
	}

	fmt.Printf("%s %s%s%s\n\n", style.Bold.Render("Subject:"), msg.Subject, typeStr, priorityStr)
	fmt.Printf("From: %s\n", msg.From)
	fmt.Printf("To: %s\n", msg.To)
	if len(msg.CC) > 0 {
		fmt.Printf("CC: %s\n", strings.Join(msg.CC, ", "))
	}
	// Zone label: mail renders local time while bd renders UTC. Correlating the
	// two is routine and the offset was silently carried as an error.
	fmt.Printf("Date: %s\n", msg.Timestamp.Local().Format("2006-01-02 15:04:05 MST"))
	fmt.Printf("ID: %s\n", style.Dim.Render(msg.ID))
	// Correct delivery of a cc copy has already been misreported as a misroute,
	// because nothing distinguished it from a message addressed to the reader.
	if mailbox.IsCCOnly(msg) {
		fmt.Printf("%s\n", style.Dim.Render(
			fmt.Sprintf("(you are cc'd; this message is addressed to %s — delivery is correct, and it is theirs to act on)", msg.To)))
	}

	if msg.ThreadID != "" {
		fmt.Printf("Thread: %s\n", style.Dim.Render(msg.ThreadID))
	}
	if msg.ReplyTo != "" {
		fmt.Printf("Reply-To: %s\n", style.Dim.Render(msg.ReplyTo))
	}

	if msg.Body != "" {
		fmt.Printf("\n%s\n", msg.Body)
	}

	// Say which of the two things happened. "Marked as read" reads identically
	// whether the bead closed or is still sitting in the work queue, and that
	// ambiguity is most of why nobody noticed 520 acknowledged messages had
	// never closed (gt-qffl).
	if readResult == mail.ReadClosed {
		fmt.Printf("\n%s\n", style.Dim.Render("(closed — automated notice, nothing owed back)"))
	}

	// Ack after output (non-fatal).
	if ackErr := mailbox.AcknowledgeDeliveries(address, []*mail.Message{msg}); ackErr != nil {
		fmt.Fprintf(os.Stderr, "gt mail read: delivery ack failed: %v\n", ackErr)
	}

	return nil
}

func runMailPeek(cmd *cobra.Command, args []string) error {
	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return NewSilentExit(1) // Silent exit - can't access mailbox
	}

	// Get unread messages
	messages, err := mailbox.ListUnread()
	if err != nil || len(messages) == 0 {
		return NewSilentExit(1) // Silent exit - no unread
	}

	// Show first unread message
	msg := messages[0]

	// Header with priority indicator
	priorityStr := ""
	if msg.Priority == mail.PriorityUrgent {
		priorityStr = " [URGENT]"
	} else if msg.Priority == mail.PriorityHigh {
		priorityStr = " [!]"
	}

	fmt.Printf("📬 %s%s\n", msg.Subject, priorityStr)
	fmt.Printf("From: %s\n", msg.From)
	fmt.Printf("ID: %s\n\n", msg.ID)

	// Body preview (truncate long bodies)
	if msg.Body != "" {
		body := msg.Body
		// Truncate to ~500 chars for popup display
		if len(body) > 500 {
			body = body[:500] + "\n..."
		}
		fmt.Print(body)
		if !strings.HasSuffix(body, "\n") {
			fmt.Println()
		}
	}

	// Show count if more messages
	if len(messages) > 1 {
		fmt.Printf("\n%s\n", style.Dim.Render(fmt.Sprintf("(+%d more unread)", len(messages)-1)))
	}

	return nil
}

// printOwnershipRefusalHint explains a beads ownership refusal in terms of the
// command the reader actually ran.
//
// bd's refusal ends "reclaim or use --force to override". That advice is only
// followable from a command that exposes --force: `gt mail archive` does,
// `gt mail delete` does not, and repeating bd's wording there sends the reader
// after a flag that command has never had. Name the command that carries the
// flag instead (gt-gbv4).
func printOwnershipRefusalHint(err error, msgID, address, verb string) {
	if !mail.IsOwnershipRefusal(err) {
		return
	}
	fmt.Printf("  %s %s is addressed to another agent — only its assignee can close it.\n",
		style.Dim.Render("hint"), msgID)
	fmt.Printf("  %s If you expected a cc copy, this message carries no cc label for %s.\n",
		style.Dim.Render("hint"), address)
	if verb != "archive" {
		fmt.Printf("  %s To override anyway, use `%s mail archive %s --force` — `%s mail %s` has no --force.\n",
			style.Dim.Render("hint"), cli.Name(), msgID, cli.Name(), verb)
	}
}

func runMailDelete(cmd *cobra.Command, args []string) error {
	if err := validateMessageIDArgs(args); err != nil {
		return err
	}

	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// Delete all specified messages
	deleted := 0
	var errors []string
	for _, msgID := range args {
		if err := mailbox.Delete(msgID); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", msgID, err))
			printOwnershipRefusalHint(err, msgID, address, "delete")
		} else {
			deleted++
		}
	}

	// Report results
	if len(errors) > 0 {
		fmt.Printf("%s Deleted %d/%d messages\n",
			style.Bold.Render("⚠"), deleted, len(args))
		for _, e := range errors {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to delete %d messages", len(errors))
	}

	if len(args) == 1 {
		fmt.Printf("%s Message deleted\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("%s Deleted %d messages\n", style.Bold.Render("✓"), deleted)
	}
	return nil
}

// validateMessageIDArgs rejects arguments that cannot be a single message ID.
//
// It runs before anything is archived, so a bad batch fails whole rather than
// half-applied.
//
// The multi-ID forms these commands document are one argument per ID, and a
// caller that joins them into ONE argument used to be answered with a
// fabricated reassurance instead of an error. mailbox.Get returns
// ErrMessageNotFound for any identifier it does not recognise — including one
// that could never have been an ID — and archive reads that as "the underlying
// bead was already GC'd", counts it as a success, prints "✓ Message archived"
// in the singular and exits 0. Measured (gt-f0b3): 514 archives submitted as 13
// joined batches reported 13 successes and moved the inbox by 2; the same list
// one ID per invocation moved it by 512.
//
// A not-found is evidence that a bead was GC'd only if the identifier could
// have been a bead ID in the first place. Whitespace is the discriminator that
// costs nothing: no bead ID contains any.
func validateMessageIDArgs(args []string) error {
	for i, arg := range args {
		fields := strings.Fields(arg)
		switch {
		case len(fields) == 0:
			return fmt.Errorf("argument %d is empty: a message ID is required", i+1)
		case len(fields) > 1:
			return fmt.Errorf("%q is not a message ID: it looks like %d IDs passed as a single argument.\n"+
				"Pass them as separate arguments: %s", arg, len(fields), strings.Join(fields, " "))
		case fields[0] != arg:
			return fmt.Errorf("message ID %q has surrounding whitespace; pass it as %q", arg, fields[0])
		}
	}
	return nil
}

func runMailArchive(cmd *cobra.Command, args []string) error {
	if err := validateMessageIDArgs(args); err != nil {
		return err
	}

	// Past argument validation, every remaining failure is operational: the
	// message would not archive, not the command was called wrong. Cobra prints
	// usage below the error, and a flag listing under "Error: ..." reads as a
	// command that never ran (gt-khq8).
	cmd.SilenceUsage = true

	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// --force is forwarded to the underlying `bd close`. bd refuses to close a
	// bead whose assignee does not match the acting identity and advises using
	// --force; before this flag existed that advice was unreachable from here.
	mailbox.SetForceClose(mailArchiveForce)

	if mailArchiveStale {
		if len(args) > 0 {
			return errors.New("--stale cannot be combined with message IDs")
		}
		return runMailArchiveStale(mailbox, address)
	}
	if len(args) == 0 {
		return errors.New("message ID required unless using --stale")
	}
	if mailArchiveDryRun {
		fmt.Printf("%s Would archive %d message(s)\n", style.Dim.Render("(dry-run)"), len(args))
		for _, msgID := range args {
			fmt.Printf("  %s\n", style.Dim.Render(msgID))
		}
		return nil
	}

	// Archive all specified messages.
	//
	// Archive is a mail cleanup operation, not a bead operation. If the
	// underlying bead has been garbage collected (by `bd mol wisp gc` or
	// `bd compact`), the mail entry is effectively already gone — we
	// treat ErrMessageNotFound as success so orphaned inbox references
	// can be cleared without manual surgery. See aa-6hv.
	archived := 0
	gcd := 0
	ccCleared := 0
	var errMsgs []string
	for _, msgID := range args {
		result, err := mailbox.DeleteWithResult(msgID)
		switch {
		case err == nil && result == mail.DeleteCCCleared:
			// The bead stays open and stays the assignee's: only this
			// recipient's cc copy left the inbox (gt-58s).
			ccCleared++
			// The bead stays open for its assignee, but this reader is done with
			// it — so is its notification, which is per-recipient anyway.
			clearNudgesForMessage(address, msgID, "")
			fmt.Printf("  %s %s: cc copy cleared; the message itself remains open for its assignee\n",
				style.Dim.Render("note"), msgID)
		case err == nil:
			archived++
			clearNudgesForMessage(address, msgID, "")
		case errors.Is(err, mail.ErrMessageNotFound):
			gcd++
			// The bead is gone but its notification may not be — that pairing is
			// exactly what leaves a pointer to nothing in the queue (gt-loz6).
			clearNudgesForMessage(address, msgID, "")
			fmt.Printf("  %s %s: underlying bead already gone (GC'd), entry cleared\n",
				style.Dim.Render("note"), msgID)
		default:
			errMsgs = append(errMsgs, fmt.Sprintf("%s: %v", msgID, err))
			printOwnershipRefusalHint(err, msgID, address, "archive")
		}
	}

	// Report results
	if len(errMsgs) > 0 {
		fmt.Printf("%s Archived %d/%d messages\n",
			style.Bold.Render("⚠"), archived+gcd+ccCleared, len(args))
		for _, e := range errMsgs {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to archive %d messages", len(errMsgs))
	}

	// Always a count, and always against the number asked for. The singular
	// "Message archived" carried no number at all, so a batch that had collapsed
	// to one operation was indistinguishable from a batch that worked (gt-f0b3).
	total := archived + gcd + ccCleared
	var detail []string
	if gcd > 0 {
		detail = append(detail, fmt.Sprintf("%d underlying bead%s already gone", gcd, map[bool]string{true: "", false: "s"}[gcd == 1]))
	}
	if ccCleared > 0 {
		detail = append(detail, fmt.Sprintf("%d cc cop%s cleared", ccCleared, map[bool]string{true: "y", false: "ies"}[ccCleared == 1]))
	}
	suffix := ""
	if len(detail) > 0 {
		suffix = " (" + strings.Join(detail, ", ") + ")"
	}
	fmt.Printf("%s Archived %d of %d message%s%s\n",
		style.Bold.Render("✓"), total, len(args), map[bool]string{true: "", false: "s"}[len(args) == 1], suffix)
	return nil
}

type staleMessage struct {
	Message *mail.Message
	Reason  string
}

func runMailArchiveStale(mailbox *mail.Mailbox, address string) error {
	identity, err := session.ParseAddress(address)
	if err != nil {
		return fmt.Errorf("determining session for %s: %w", address, err)
	}

	sessionName := identity.SessionName()
	if sessionName == "" {
		return fmt.Errorf("could not determine session name for %s", address)
	}

	sessionStart, err := session.SessionCreatedAt(sessionName)
	if err != nil {
		return fmt.Errorf("getting session start time for %s: %w", sessionName, err)
	}

	messages, err := mailbox.List()
	if err != nil {
		return fmt.Errorf("listing messages: %w", err)
	}

	staleMessages := staleMessagesForSession(messages, sessionStart)
	if mailArchiveDryRun {
		if len(staleMessages) == 0 {
			fmt.Printf("%s No stale messages found\n", style.Success.Render("✓"))
			return nil
		}
		fmt.Printf("%s Would archive %d stale message(s):\n", style.Dim.Render("(dry-run)"), len(staleMessages))
		for _, stale := range staleMessages {
			fmt.Printf("  %s %s\n", style.Dim.Render(stale.Message.ID), stale.Message.Subject)
		}
		return nil
	}

	if len(staleMessages) == 0 {
		fmt.Printf("%s No stale messages to archive\n", style.Success.Render("✓"))
		return nil
	}

	// GC'd beads (see aa-6hv): if the underlying bead was removed by
	// `bd mol wisp gc` or `bd compact`, the close call returns
	// ErrMessageNotFound. That's a success for archive: the mail entry
	// is already effectively gone.
	archived := 0
	gcd := 0
	var errMsgs []string
	for _, stale := range staleMessages {
		err := mailbox.Delete(stale.Message.ID)
		switch {
		case err == nil:
			archived++
			clearNudgesForMessage(address, stale.Message.ID, stale.Message.ThreadID)
		case errors.Is(err, mail.ErrMessageNotFound):
			gcd++
			clearNudgesForMessage(address, stale.Message.ID, stale.Message.ThreadID)
			fmt.Printf("  %s %s: underlying bead already gone (GC'd), entry cleared\n",
				style.Dim.Render("note"), stale.Message.ID)
		default:
			errMsgs = append(errMsgs, fmt.Sprintf("%s: %v", stale.Message.ID, err))
		}
	}

	if len(errMsgs) > 0 {
		fmt.Printf("%s Archived %d/%d stale messages\n", style.Bold.Render("⚠"), archived+gcd, len(staleMessages))
		for _, e := range errMsgs {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to archive %d stale messages", len(errMsgs))
	}

	total := archived + gcd
	if total == 1 {
		fmt.Printf("%s Stale message archived\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("%s Archived %d stale messages\n", style.Bold.Render("✓"), total)
	}
	return nil
}

func staleMessagesForSession(messages []*mail.Message, sessionStart time.Time) []staleMessage {
	var staleMessages []staleMessage
	for _, msg := range messages {
		stale, reason := session.StaleReasonForTimes(msg.Timestamp, sessionStart)
		if stale {
			staleMessages = append(staleMessages, staleMessage{Message: msg, Reason: reason})
		}
	}
	return staleMessages
}

func runMailMarkRead(cmd *cobra.Command, args []string) error {
	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// --all: mark all unread messages as read
	if mailMarkReadAll {
		if len(args) > 0 {
			return fmt.Errorf("--all cannot be combined with explicit message IDs")
		}
		messages, err := mailbox.ListUnread()
		if err != nil {
			return fmt.Errorf("listing unread messages: %w", err)
		}
		if len(messages) == 0 {
			fmt.Printf("%s No unread messages\n", style.Bold.Render("✓"))
			return nil
		}
		marked := 0
		closed := 0
		for _, msg := range messages {
			// Pass the message we already listed: this loop runs over a whole
			// inbox, and re-fetching each one would turn a bulk mark into N
			// extra bd subprocesses.
			result, err := mailbox.MarkReadConsumed(msg.ID, msg)
			if err != nil {
				style.PrintWarning("could not mark %s as read: %v", msg.ID, err)
				continue
			}
			marked++
			if result == mail.ReadClosed {
				closed++
			}
			clearNudgesForMessage(address, msg.ID, msg.ThreadID)
		}
		printMarkReadSummary(marked, closed)
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("message ID required (or use --all to mark all as read)")
	}
	if err := validateMessageIDArgs(args); err != nil {
		return err
	}

	// Mark all specified messages as read
	marked := 0
	closed := 0
	var errors []string
	for _, msgID := range args {
		result, err := mailbox.MarkReadConsumed(msgID, nil)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", msgID, err))
			continue
		}
		marked++
		if result == mail.ReadClosed {
			closed++
		}
		clearNudgesForMessage(address, msgID, "")
	}

	// Report results
	if len(errors) > 0 {
		fmt.Printf("%s Marked %d/%d messages as read\n",
			style.Bold.Render("⚠"), marked, len(args))
		for _, e := range errors {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to mark %d messages", len(errors))
	}

	if len(args) == 1 && closed == 1 {
		fmt.Printf("%s Message marked as read and closed\n", style.Bold.Render("✓"))
		return nil
	}
	if len(args) == 1 {
		fmt.Printf("%s Message marked as read\n", style.Bold.Render("✓"))
		return nil
	}
	printMarkReadSummary(marked, closed)
	return nil
}

// printMarkReadSummary reports a bulk mark-read, naming how many beads closed.
//
// The count of closed beads is the whole point of the line. Before gt-qffl this
// path printed "Marked N messages as read" over an operation that closed
// nothing, so an inbox could be marked read every day and still grow without
// bound — 520 messages on the hq store were read, acknowledged, and open.
func printMarkReadSummary(marked, closed int) {
	if closed == 0 {
		fmt.Printf("%s Marked %d messages as read\n", style.Bold.Render("✓"), marked)
		return
	}
	fmt.Printf("%s Marked %d messages as read (%d closed, %d still owed a reply or an archive)\n",
		style.Bold.Render("✓"), marked, closed, marked-closed)
}

func runMailMarkUnread(cmd *cobra.Command, args []string) error {
	if err := validateMessageIDArgs(args); err != nil {
		return err
	}

	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// Mark all specified messages as unread
	marked := 0
	var errors []string
	for _, msgID := range args {
		if err := mailbox.MarkUnreadOnly(msgID); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", msgID, err))
		} else {
			marked++
		}
	}

	// Report results
	if len(errors) > 0 {
		fmt.Printf("%s Marked %d/%d messages as unread\n",
			style.Bold.Render("⚠"), marked, len(args))
		for _, e := range errors {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to mark %d messages", len(errors))
	}

	if len(args) == 1 {
		fmt.Printf("%s Message marked as unread\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("%s Marked %d messages as unread\n", style.Bold.Render("✓"), marked)
	}
	return nil
}

func runMailClear(cmd *cobra.Command, args []string) error {
	// Determine which inbox to clear (target arg or auto-detect)
	address := ""
	if len(args) > 0 {
		address = args[0]
	} else {
		address = detectSender()
	}

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// List all messages
	messages, err := mailbox.List()
	if err != nil {
		return fmt.Errorf("listing messages: %w", err)
	}

	if len(messages) == 0 {
		fmt.Printf("%s Inbox %s is already empty\n", style.Dim.Render("○"), address)
		return nil
	}

	// Delete each message
	deleted := 0
	var errors []string
	for _, msg := range messages {
		if err := mailbox.Delete(msg.ID); err != nil {
			// If file is already gone (race condition), ignore it and count as success
			if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
				continue
			}
			errors = append(errors, fmt.Sprintf("%s: %v", msg.ID, err))
		} else {
			deleted++
		}
	}

	// Report results
	if len(errors) > 0 {
		fmt.Printf("%s Cleared %d/%d messages from %s\n",
			style.Bold.Render("⚠"), deleted, len(messages), address)
		for _, e := range errors {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to clear %d messages", len(errors))
	}

	fmt.Printf("%s Cleared %d messages from %s\n",
		style.Bold.Render("✓"), deleted, address)
	return nil
}
