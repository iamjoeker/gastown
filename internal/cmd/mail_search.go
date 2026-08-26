package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
)

// searchJSON is the --json envelope.
//
// The messages used to be the whole document. They are nested under a scope
// now for the same reason the text output grew a scope line: a bare empty
// array is indistinguishable between "no such message exists" and "the store
// holding it was never read", and a machine reader has even less chance than a
// human of noticing the difference.
type searchJSON struct {
	Scope       []string        `json:"scope"`
	Unavailable []string        `json:"unavailable,omitempty"`
	Messages    []*mail.Message `json:"messages"`
}

// newSearchJSON wraps a search result for --json.
//
// An empty result is an empty ARRAY, never null. This whole change is about
// zeros that cannot be read, and emitting null for "no matches" hands the
// machine reader one more shape to disambiguate for nothing.
func newSearchJSON(result *mail.SearchResult) searchJSON {
	messages := result.Messages
	if messages == nil {
		messages = []*mail.Message{}
	}
	return searchJSON{
		Scope:       scopeNames(result.Scope),
		Unavailable: result.Scope.Unavailable,
		Messages:    messages,
	}
}

// scopeNames lists the stores a search actually read.
func scopeNames(scope mail.SearchScope) []string {
	var names []string
	if scope.Inbox {
		names = append(names, "inbox")
	}
	if scope.Archived {
		names = append(names, "archived")
	}
	if scope.Sent {
		names = append(names, "sent")
	}
	return names
}

// excludedSuffix names the stores that were NOT searched and the flag that
// would have included them.
//
// Naming the exclusion is the load-bearing half. `gt mq list` prints
// scope=status=open and says what it left out for the same reason: a reader
// who is told only what WAS covered still has to know the full set of stores
// to work out what is missing, and the reader who most needs telling is
// exactly the one who does not.
func excludedSuffix(scope mail.SearchScope) string {
	var excluded []string
	if !scope.Archived {
		excluded = append(excluded, "archived (--archive)")
	}
	if !scope.Sent {
		excluded = append(excluded, "sent (--sent)")
	}
	if len(excluded) == 0 {
		return ""
	}
	return " · not searched: " + strings.Join(excluded, ", ")
}

// runMailSearch searches for messages matching a pattern.
func runMailSearch(cmd *cobra.Command, args []string) error {
	query := args[0]

	// Determine which inbox to search
	address := detectSender()

	// Get workspace for mail operations
	workDir, err := findMailWorkDir()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Get mailbox
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(address)
	if err != nil {
		return fmt.Errorf("getting mailbox: %w", err)
	}

	// Build search options
	opts := mail.SearchOptions{
		Query:           query,
		FromFilter:      mailSearchFrom,
		SubjectOnly:     mailSearchSubject,
		BodyOnly:        mailSearchBody,
		IncludeArchived: mailSearchArchive || mailSearchAll,
		IncludeSent:     mailSearchSent || mailSearchAll,
	}

	// Execute search
	result, err := mailbox.Search(opts)
	if err != nil {
		return fmt.Errorf("searching messages: %w", err)
	}
	messages := result.Messages

	// JSON output
	if mailSearchJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(newSearchJSON(result))
	}

	// Human-readable output.
	//
	// The scope goes on the line above the results, not into a --help page,
	// because it is what makes the count interpretable. "0 message(s)" from an
	// inbox-only search and "0 message(s)" from a search that also read the
	// archive are the same string and different findings, and an agent reading
	// the first as the second concludes something was never said when it was
	// only archived (gt-7gvk).
	fmt.Printf("%s Search results for %s: %d message(s)\n",
		style.Bold.Render("🔍"), address, len(messages))
	fmt.Printf("  %s\n", style.Dim.Render("scope="+strings.Join(scopeNames(result.Scope), "+")+
		excludedSuffix(result.Scope)))
	for _, missing := range result.Scope.Unavailable {
		fmt.Printf("  %s %s\n", style.Bold.Render("!"),
			style.Dim.Render("not searched — "+missing))
	}
	fmt.Println()

	if len(messages) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no matches)"))
		return nil
	}

	for _, msg := range messages {
		readMarker := "●"
		if msg.Read {
			readMarker = "○"
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

		fmt.Printf("  %s %s%s%s%s\n", readMarker, msg.Subject, typeMarker, priorityMarker, wispMarker)
		fmt.Printf("    %s from %s\n",
			style.Dim.Render(msg.ID),
			msg.From)
		// Zone label: local here, UTC in bd — see readMessage's Date line.
		fmt.Printf("    %s\n",
			style.Dim.Render(msg.Timestamp.Local().Format("2006-01-02 15:04 MST")))
	}

	return nil
}
