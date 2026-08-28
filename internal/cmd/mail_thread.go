package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
)

func runMailThread(cmd *cobra.Command, args []string) error {
	threadID := args[0]

	// All mail uses town beads (two-level architecture)
	workDir, err := findMailWorkDir()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine which inbox
	address := detectSender()

	// Get mailbox and thread messages
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(address)
	if err != nil {
		return fmt.Errorf("getting mailbox: %w", err)
	}

	messages, err := mailbox.ListByThread(threadID)
	if err != nil {
		return fmt.Errorf("getting thread: %w", err)
	}

	// JSON output
	if mailThreadJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(messages)
	}

	// Human-readable output
	fmt.Printf("%s Thread: %s (%d messages)\n\n",
		style.Bold.Render("🧵"), threadID, len(messages))

	if len(messages) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no messages in thread)"))
		return nil
	}

	for i, msg := range messages {
		typeMarker := ""
		if msg.Type != "" && msg.Type != mail.TypeNotification {
			typeMarker = fmt.Sprintf(" [%s]", msg.Type)
		}
		priorityMarker := ""
		if msg.Priority == mail.PriorityHigh || msg.Priority == mail.PriorityUrgent {
			priorityMarker = " " + style.Bold.Render("!")
		}

		if i > 0 {
			fmt.Printf("  %s\n", style.Dim.Render("│"))
		}
		fmt.Printf("  %s %s%s%s\n", style.Bold.Render("●"), msg.Subject, typeMarker, priorityMarker)
		fmt.Printf("    %s from %s to %s\n",
			style.Dim.Render(msg.ID),
			msg.From, msg.To)
		// Zone label: local here, UTC in bd — see readMessage's Date line. A
		// thread is read for ordering, which is exactly what a hidden offset breaks.
		fmt.Printf("    %s\n",
			style.Dim.Render(msg.Timestamp.Local().Format("2006-01-02 15:04 MST")))

		if msg.Body != "" {
			fmt.Printf("    %s\n", msg.Body)
		}
	}

	return nil
}

func runMailReply(cmd *cobra.Command, args []string) error {
	// A reply that fails at runtime must not answer with a flag listing. Cobra
	// prints usage BELOW the error, so the last line an operator (or a `| tail`)
	// sees is a flag description, and "the reply did not go" reads as "the
	// command never ran" — which is how this command's failures were retold as
	// exit 0 (gt-khq8). Set here rather than on the command so genuine flag
	// misuse, which cobra validates before RunE, still gets usage.
	cmd.SilenceUsage = true

	msgID := args[0]

	// Get message body from positional arg or flag (positional takes precedence)
	messageBody := mailReplyMessage
	if len(args) > 1 {
		messageBody = args[1]
	}

	// Validate message is provided
	if messageBody == "" {
		return fmt.Errorf("message body required: provide as second argument or use -m flag")
	}

	// All mail uses town beads (two-level architecture)
	workDir, err := findMailWorkDir()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine current address
	from := detectSender()

	// Get the original message
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(from)
	if err != nil {
		return fmt.Errorf("getting mailbox: %w", err)
	}

	original, err := mailbox.Get(msgID)
	if err != nil {
		return fmt.Errorf("getting message: %w", err)
	}

	// Build reply subject
	subject := mailReplySubject
	if subject == "" {
		if strings.HasPrefix(original.Subject, "Re: ") {
			subject = original.Subject
		} else {
			subject = "Re: " + original.Subject
		}
	}

	// Create reply message
	reply := &mail.Message{
		From:     from,
		To:       original.From, // Reply to sender
		CC:       mailReplyCC,
		Subject:  subject,
		Body:     messageBody,
		Type:     mail.TypeReply,
		Priority: mail.PriorityNormal,
		ReplyTo:  msgID,
		ThreadID: original.ThreadID,
	}

	// If original has no thread ID, create one
	if reply.ThreadID == "" {
		reply.ThreadID = generateThreadID()
	}

	// Send the reply (defer drains async notification goroutines before CLI exits)
	defer router.WaitPendingNotifications()
	if err := router.Send(reply); err != nil {
		return fmt.Errorf("sending reply: %w", err)
	}
	if err := router.ClearReplyReminders(from, reply.ThreadID); err != nil {
		style.PrintWarning("could not clear satisfied reply reminders: %v", err)
	}

	// Log mail event to activity feed. Without this, a reply is silently
	// worse than a fresh send at waking a parked agent: send emits this
	// event and reply did not (gt-bq4m, mirrors hq-9uslp).
	_ = events.LogFeed(events.TypeMail, from, events.MailPayload(reply.To, subject))

	fmt.Printf("%s Reply sent to %s\n", style.Bold.Render("✓"), original.From)
	fmt.Printf("  Subject: %s\n", subject)
	if len(reply.CC) > 0 {
		fmt.Printf("  CC: %s\n", strings.Join(reply.CC, ", "))
	}
	if original.ThreadID != "" {
		fmt.Printf("  Thread: %s\n", style.Dim.Render(original.ThreadID))
	}

	return nil
}
