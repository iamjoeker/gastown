package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

func runEscalate(cmd *cobra.Command, args []string) error {
	// Handle --stdin: read reason from stdin (avoids shell quoting issues)
	if escalateStdin {
		if escalateReason != "" {
			return fmt.Errorf("cannot use --stdin with --reason/-r")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		escalateReason = strings.TrimRight(string(data), "\n")
	}

	// Require at least a description when creating an escalation
	if len(args) == 0 {
		return cmd.Help()
	}

	description := strings.Join(args, " ")

	// Validate severity
	severity := strings.ToLower(escalateSeverity)
	if !config.IsValidSeverity(severity) {
		return fmt.Errorf("invalid severity '%s': must be critical, high, medium, or low", escalateSeverity)
	}

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load escalation config
	escalationConfig, err := config.LoadOrCreateEscalationConfig(config.EscalationConfigPath(townRoot))
	if err != nil {
		return fmt.Errorf("loading escalation config: %w", err)
	}

	// Detect agent identity
	agentID := detectSender()
	if agentID == "" {
		agentID = "unknown"
	}

	// Dry run mode
	if escalateDryRun {
		// The same title check the real path makes, made here too. A dry run that
		// previews a create the real command refuses is worse than no dry run: it
		// is a confident rehearsal of something that cannot happen.
		if err := checkEscalationTitleLen(severity, description); err != nil {
			return err
		}
		actions := escalationConfig.GetRouteForSeverity(severity)
		targets := extractMailTargetsFromActions(actions)
		fmt.Printf("Would create escalation:\n")
		fmt.Printf("  Severity: %s\n", severity)
		fmt.Printf("  Description: %s\n", description)
		if escalateReason != "" {
			fmt.Printf("  Reason: %s\n", escalateReason)
		}
		if escalateSource != "" {
			fmt.Printf("  Source: %s\n", escalateSource)
		}
		if escalateFingerprint != "" {
			fmt.Printf("  Fingerprint: %s\n", escalationFingerprintLabel(escalateFingerprint))
		}
		fmt.Printf("  Actions: %s\n", strings.Join(actions, ", "))
		fmt.Printf("  Mail targets: %s\n", strings.Join(targets, ", "))
		// Whether anything outlives the ephemeral record is the question a dry
		// run should answer, and the one nobody could ask before gt-3i4e.
		if slices.Contains(actions, escalationBeadAction) {
			fmt.Printf("  Durable bead: yes\n")
		} else {
			fmt.Printf("  Durable bead: no — only the ephemeral record, which is GC'd unread\n")
		}
		if len(actions) == 0 {
			style.PrintWarning("severity %q routes to nothing: this escalation would be recorded but undelivered, and would exit non-zero", severity)
		}
		return nil
	}

	// Create and route the escalation. The record + routing lives in
	// raiseEscalation so gt done can raise one too (gt-lj2n) without
	// reimplementing the durable-twin rules.
	fingerprintLabel := escalationFingerprintLabel(escalateFingerprint)
	outcome, err := raiseEscalation(escalationRequest{
		TownRoot:    townRoot,
		AgentID:     agentID,
		Description: description,
		Severity:    severity,
		Reason:      escalateReason,
		Source:      escalateSource,
		RelatedBead: escalateRelatedBead,
		Fingerprint: fingerprintLabel,
		Config:      escalationConfig,
	})
	if err != nil {
		return err
	}

	if outcome.Duplicate {
		if escalateJSON {
			result := map[string]interface{}{
				"id":          outcome.RecordID,
				"status":      "duplicate_suppressed",
				"fingerprint": fingerprintLabel,
			}
			out, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(out))
		} else {
			fmt.Printf("%s Duplicate escalation suppressed: %s\n", style.Bold.Render("✓"), outcome.RecordID)
			fmt.Printf("  Fingerprint: %s\n", fingerprintLabel)
		}
		return nil
	}

	// Output
	if escalateJSON {
		hasFailure := false
		for _, status := range outcome.Statuses {
			if status.Error != "" {
				hasFailure = true
				break
			}
		}
		result := map[string]interface{}{
			"id":        outcome.RecordID,
			"severity":  severity,
			"actions":   outcome.Actions,
			"targets":   outcome.Targets,
			"delivery":  outcome.Statuses,
			"status":    escalationDeliveryStatus(outcome.Delivered, hasFailure),
			"delivered": outcome.Delivered,
		}
		if escalateSource != "" {
			result["source"] = escalateSource
		}
		if fingerprintLabel != "" {
			result["fingerprint"] = fingerprintLabel
		}
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
	} else {
		emoji := severityEmoji(severity)
		fmt.Printf("%s Escalation created: %s\n", emoji, outcome.RecordID)
		fmt.Printf("  Severity: %s\n", severity)
		if escalateSource != "" {
			fmt.Printf("  Source: %s\n", escalateSource)
		}
		if fingerprintLabel != "" {
			fmt.Printf("  Fingerprint: %s\n", fingerprintLabel)
		}
		if outcome.DurableBeadID != "" {
			// The record itself is an ephemeral wisp; this is the bead that
			// outlives it and that `gt escalate list` renders.
			fmt.Printf("  Recorded as: %s\n", outcome.DurableBeadID)
		}
		if len(outcome.Targets) > 0 {
			fmt.Printf("  Routed to: %s\n", strings.Join(outcome.Targets, ", "))
		} else {
			// An empty "Routed to:" is how this bug hid for so long — it reads
			// as a rendering glitch rather than as "nobody was told".
			fmt.Printf("  Routed to: no mail targets (%s route: %s)\n", severity, strings.Join(outcome.Actions, ", "))
		}
		for _, status := range outcome.Statuses {
			if status.Error != "" {
				fmt.Printf("  Delivery issue [%s:%s]: %s\n", status.Channel, status.Target, status.Error)
			}
		}
	}

	// A routing no-op must never print a success banner and exit 0.
	if !outcome.Delivered {
		return undeliveredEscalationError(townRoot, outcome.RecordID, severity, outcome.Actions)
	}

	return nil
}

// escalationBeadAction is the routing action that records an escalation as a
// durable bead.
const escalationBeadAction = "bead"

// escalationBeadCreator is the slice of *beads.Beads deliverEscalationBead needs,
// so the routing decision can be tested without a bd subprocess.
type escalationBeadCreator interface {
	CreateEscalationDeliveryBead(title, body, recordID, severity string) (*beads.Issue, error)
}

// deliverEscalationBead executes the "bead" routing action and returns its
// delivery status, or nil when the route does not contain the action.
//
// Nothing used to dispatch on "bead" at all: it was configured on all four
// routes, reported as created on every escalation, and implemented nowhere
// (gt-3i4e). The durable artifact it names existed only as a side effect of the
// "mail:" actions, so severity "low" — whose default route is exactly ["bead"] —
// produced no durable bead, showed up on no surface, and was silently destroyed
// when its wisp aged out. Severity was the revealer, not the cause.
//
// When the mail path already produced a linked durable copy, the action is
// satisfied by it: creating a second bead would double every critical, high and
// medium escalation in `gt escalate list` and in the Mayor's queue. mailStatuses
// must therefore be the statuses collected from the mail loop.
func deliverEscalationBead(bd escalationBeadCreator, actions []string, mailStatuses []deliveryStatus, recordID, severity, title, body string) *deliveryStatus {
	if !slices.Contains(actions, escalationBeadAction) {
		return nil
	}

	status := &deliveryStatus{Channel: escalationBeadAction, Severity: severity}
	for _, mailStatus := range mailStatuses {
		// Annotated, not merely persisted: an un-annotated copy is missing the
		// "escalation:<record-id>" label, so nothing can find it from the record.
		if mailStatus.Annotated && mailStatus.BeadID != "" {
			status.Created = true
			status.BeadID = mailStatus.BeadID
			status.Detail = "durable copy created by mail delivery to " + mailStatus.Target
			return status
		}
	}

	delivered, err := bd.CreateEscalationDeliveryBead(title, body, recordID, severity)
	if err != nil {
		status.Error = err.Error()
		style.PrintWarning("failed to create durable escalation bead: %v", err)
		return status
	}
	status.Created = true
	status.BeadID = delivered.ID
	return status
}

// formatEscalationDeliveryBody is the description of the durable delivery bead.
//
// It carries the record's own structured fields rather than the mail body, so
// severity, reason, source and provenance survive the record wisp being
// garbage-collected — that loss is the damage gt-3i4e is about. The trailing
// block names the record, since ack and close are documented against its ID.
func formatEscalationDeliveryBody(recordID, description string, fields *beads.EscalationFields) string {
	return strings.Join([]string{
		beads.FormatEscalationDescription(description, fields),
		"",
		"---",
		fmt.Sprintf("Escalation record: %s (ephemeral — this bead is the durable copy)", recordID),
		"To acknowledge: gt escalate ack " + recordID,
		"To close: gt escalate close " + recordID + " --reason \"resolution\"",
	}, "\n")
}

// escalationWasDelivered reports whether any channel actually delivered
// something. Warnings do not count (a skipped email is not a delivery) and
// neither does the escalation record itself, which is created regardless of
// routing and is ephemeral.
func escalationWasDelivered(statuses []deliveryStatus) bool {
	for _, status := range statuses {
		if status.Error != "" {
			continue
		}
		if status.Created || status.Persisted || status.RuntimeNotified {
			return true
		}
	}
	return false
}

// escalationDeliveryStatus renders the JSON "status" field. "ok" used to be
// unconditional for want of any failing channel — the seeded bead status could
// not fail, so every escalation ever emitted reported ok (gt-3i4e).
func escalationDeliveryStatus(delivered, hasFailure bool) string {
	switch {
	case !delivered:
		return "undelivered"
	case hasFailure:
		return "partial_failure"
	default:
		return "ok"
	}
}

func escalationFingerprintLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("escalation-fp:%x", sum[:6])
}

type deliveryStatus struct {
	Target            string `json:"target,omitempty"`
	Channel           string `json:"channel"`
	BeadID            string `json:"bead_id,omitempty"` // durable bead this delivery produced
	Detail            string `json:"detail,omitempty"`  // how the channel was satisfied
	Created           bool   `json:"created,omitempty"`
	Persisted         bool   `json:"persisted,omitempty"`
	RuntimeNotified   bool   `json:"runtime_notified,omitempty"`
	Annotated         bool   `json:"annotated,omitempty"`
	Severity          string `json:"severity,omitempty"`
	Error             string `json:"error,omitempty"`
	Warning           string `json:"warning,omitempty"`
	NotificationRoute string `json:"notification_route,omitempty"`
}

func runEscalateList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	bd := beads.New(beads.ResolveBeadsDir(townRoot))

	var issues []*beads.Issue
	// Open escalation beads this list hides because their record is closed. They
	// are still open beads, so they still count wherever beads are counted, and
	// saying nothing about them is what made this list disagree with those counts
	// in silence (gt-f0b3).
	var stranded []*beads.Issue
	if escalateListAll {
		// List all (open and closed)
		out, err := bd.Run("list", "--label=gt:escalation", "--status=all", "--json")
		if err != nil {
			return fmt.Errorf("listing escalations: %w", err)
		}
		if err := json.Unmarshal(out, &issues); err != nil {
			return fmt.Errorf("parsing escalations: %w", err)
		}
	} else {
		issues, stranded, err = bd.ListEscalationsWithStranded()
		if err != nil {
			return fmt.Errorf("listing escalations: %w", err)
		}
	}

	// Cross-check each entry against live Dolt to filter out phantom escalations.
	// When a rig's Dolt server dies and is restarted fresh, the label-based list
	// query may still return stale IDs (e.g. from a cached or cross-rig query)
	// that no longer exist in the live database. We skip any entries that cannot
	// be fetched individually, since they cannot be acked or closed anyway.
	var live []*beads.Issue
	var phantomCount int
	for _, issue := range issues {
		if _, err := bd.Show(issue.ID); err != nil {
			if errors.Is(err, beads.ErrNotFound) {
				phantomCount++
				fmt.Fprintf(os.Stderr, "warning: skipping unresolvable escalation %s (not found in live Dolt)\n", issue.ID)
				continue
			}
			// For other errors (e.g. Dolt temporarily unreachable), include
			// the entry so the user can see it — just warn.
			fmt.Fprintf(os.Stderr, "warning: could not verify escalation %s: %v\n", issue.ID, err)
		}
		live = append(live, issue)
	}
	issues = live

	if escalateListJSON {
		// A nil slice marshals to `null`, and `null` is not a measured zero — it
		// is what a parser sees when the query died, when the filter dropped
		// everything, and when there genuinely are no open escalations, with no
		// way to tell the three apart. Errors already return early above, so the
		// only honest empty answer here is an empty array (gt-qee3).
		if issues == nil {
			issues = []*beads.Issue{}
		}
		out, _ := json.MarshalIndent(issues, "", "  ")
		fmt.Println(string(out))
		// The JSON shape stays a plain array of the open escalations, so the
		// hidden set goes to stderr rather than changing what parsers see —
		// the same channel the phantom warning above already uses.
		printStrandedEscalations(os.Stderr, stranded)
		return nil
	}

	if len(issues) == 0 {
		if phantomCount > 0 {
			fmt.Printf("No escalations found (%d phantom entr%s skipped — bead IDs no longer exist in live Dolt)\n",
				phantomCount, map[bool]string{true: "y", false: "ies"}[phantomCount == 1])
		} else {
			fmt.Println("No escalations found")
		}
		// "No escalations found" is the most consequential line this command
		// prints, and it must not be the whole story while open escalation beads
		// exist.
		printStrandedEscalations(os.Stdout, stranded)
		return nil
	}

	fmt.Printf("Escalations (%d):\n\n", len(issues))
	for _, issue := range issues {
		fields := beads.ParseEscalationFields(issue.Description)
		emoji := severityEmoji(fields.Severity)

		// The rendered status is a PROJECTION, not issues.status. Every row this
		// list prints is status='open' by construction — the query filters on it
		// — so an "[acked]" row disagrees with the table by design, and anyone
		// reconciling the two finds a mismatch that is not a data problem
		// (gt-qee3). Ack state lives in the bare "acked" label, written by
		// `gt escalate ack`. Note the near-miss: mail delivery writes
		// "delivery:acked" + "delivery-acked-by:<agent>" when a recipient's inbox
		// receives the copy, and that is NOT an acknowledgement of the
		// escalation — it must not be read here, or every delivered escalation
		// would render as handled the moment it was sent.
		status := issue.Status
		if beads.HasLabel(issue, "acked") {
			status = "acked"
		}

		fmt.Printf("  %s %s [%s] %s\n", emoji, issue.ID, status, issue.Title)
		fmt.Printf("     Severity: %s | From: %s | %s\n",
			fields.Severity, fields.EscalatedBy, formatRelativeTime(issue.CreatedAt))
		if fields.AckedBy != "" {
			fmt.Printf("     Acked by: %s\n", fields.AckedBy)
		}
		fmt.Println()
	}

	printStrandedEscalations(os.Stdout, stranded)

	return nil
}

// printStrandedEscalations reports the open escalation beads this list hid.
//
// An escalation is two beads: an ephemeral record wisp and a durable delivered
// copy. When the record is closed and the copy is not, the copy is almost
// certainly resolved residue and showing it as a live HIGH is the bug gt-4xl
// fixed — but "almost certainly" is the whole point. Anything can close the
// record without touching the copy: `bd close` run by hand, or any
// `gt escalate close` from before gt-4xl. So the difference is reported rather
// than swallowed, with the ID and the command that reconciles it. The count is
// exactly the gap between this list and `bd list --label=gt:escalation
// --status=open`, which is what nothing explained before (gt-f0b3).
func printStrandedEscalations(w io.Writer, stranded []*beads.Issue) {
	if len(stranded) == 0 {
		return
	}

	noun := "escalation beads are"
	if len(stranded) == 1 {
		noun = "escalation bead is"
	}
	fmt.Fprintf(w, "%d open %s hidden from this list: the escalation record is closed but the delivered copy was never closed with it,\n", len(stranded), noun)
	fmt.Fprintf(w, "so this list reads %d lower than any count of open gt:escalation beads. A closed record is not proof the escalation was handled.\n", len(stranded))
	for _, issue := range stranded {
		fmt.Fprintf(w, "  %s [%s] %s\n", issue.ID, issue.Status, issue.Title)
		fmt.Fprintf(w, "     record %s is closed | reconcile: gt escalate close %s --reason \"...\"\n",
			beads.EscalationRecordID(issue), issue.ID)
	}
	fmt.Fprintf(w, "  Full history, including these: gt escalate list --all\n")
}

func runEscalateAck(cmd *cobra.Command, args []string) error {
	escalationID := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Detect who is acknowledging
	ackedBy := detectSender()
	if ackedBy == "" {
		ackedBy = "unknown"
	}

	bd := beads.New(beads.ResolveBeadsDir(townRoot))
	if err := bd.AckEscalation(escalationID, ackedBy); err != nil {
		return fmt.Errorf("acknowledging escalation: %w", err)
	}

	// Log to activity feed
	_ = events.LogFeed(events.TypeEscalationAcked, ackedBy, map[string]interface{}{
		"escalation_id": escalationID,
		"acked_by":      ackedBy,
	})

	fmt.Printf("%s Escalation acknowledged: %s\n", style.Bold.Render("✓"), escalationID)
	return nil
}

func runEscalateClose(cmd *cobra.Command, args []string) error {
	escalationID := args[0]

	// Cobra only honours SilenceUsage from the executed command and the ROOT, so
	// the flag on `escalate` never reached its subcommands: a close that failed
	// printed the error and then dumped the usage block over it. That is how
	// three failed closes read as quiet successes — the operator's `| tail -1`
	// showed the last line of the usage block, not the error (gt-u3mo).
	// Setting it here rather than on the command keeps usage on arg/flag misuse,
	// which cobra validates before RunE.
	cmd.SilenceUsage = true

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Detect who is closing
	closedBy := detectSender()
	if closedBy == "" {
		closedBy = "unknown"
	}

	bd := beads.New(beads.ResolveBeadsDir(townRoot))
	result, err := bd.CloseEscalation(escalationID, closedBy, escalateCloseReason)
	if err != nil {
		if result != nil {
			// Partial close: the error already spells out which half landed and
			// which beads are still live in the queue.
			return err
		}
		return fmt.Errorf("closing escalation: %w", err)
	}

	// Log to activity feed
	_ = events.LogFeed(events.TypeEscalationClosed, closedBy, map[string]interface{}{
		"escalation_id": result.RecordID,
		"requested_id":  result.RequestedID,
		"closed_by":     closedBy,
		"reason":        escalateCloseReason,
		"record_closed": result.RecordClosed,
		"copies_closed": strings.Join(result.CopyIDs, ","),
	})

	printEscalateCloseReport(os.Stdout, result, escalateCloseReason)
	return nil
}

// printEscalateCloseReport renders what a close actually did.
//
// Every line here is derived from a bead this close WROTE TO, never from an ID
// it merely resolved. The old report was the latter: it printed
// "✓ Escalation closed: <result.RecordID>" unconditionally, so a close that
// wrote to nothing still produced a checkmark, and the ID on it was not even
// the one the operator typed (gt-w0z8).
func printEscalateCloseReport(w io.Writer, result *beads.EscalationCloseResult, reason string) {
	// A close that closed nothing must not print a checkmark. The whole stranded
	// population has a closed record by definition, so a success line derived
	// from "we resolved an ID" was true of every no-op this command could
	// produce — the operator had no signal to distinguish them.
	if !result.Changed() {
		fmt.Fprintf(w, "Nothing to close: %s is already closed.\n", result.RequestedID)
		if result.RecordID != result.RequestedID {
			fmt.Fprintf(w, "  Escalation record %s is closed too, and no open delivered copy is linked to it.\n", result.RecordID)
		}
		return
	}

	// The ID on the success line is the one the operator PASSED. Printing the
	// resolved record ID instead is how a close that touched nothing they named
	// read as a success: the tell was there — ✓ Escalation closed: hq-wisp-51nirc
	// after typing hq-9mxa7 — but only to someone who noticed the ID had changed.
	fmt.Fprintf(w, "%s Escalation closed: %s\n", style.Bold.Render("✓"), result.RequestedID)
	fmt.Fprintf(w, "  Reason: %s\n", reason)
	if result.RecordID != result.RequestedID {
		if result.RecordClosed {
			fmt.Fprintf(w, "  Escalation record: %s (closed)\n", result.RecordID)
		} else {
			fmt.Fprintf(w, "  Escalation record: %s (was already closed)\n", result.RecordID)
		}
	}
	// The delivered copies are what the queue renders, so say plainly that they
	// went with it — a close that only touched the record used to report success
	// while leaving the escalation live in the Mayor's queue (gt-4xl).
	if len(result.CopyIDs) > 0 {
		fmt.Fprintf(w, "  Cleared from queue: %s\n", strings.Join(result.CopyIDs, ", "))
	}
}

func runEscalateStale(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load escalation config for threshold and max reescalations
	escalationConfig, err := config.LoadOrCreateEscalationConfig(config.EscalationConfigPath(townRoot))
	if err != nil {
		return fmt.Errorf("loading escalation config: %w", err)
	}

	threshold := escalationConfig.GetStaleThreshold()
	maxReescalations := escalationConfig.GetMaxReescalations()

	bd := beads.New(beads.ResolveBeadsDir(townRoot))
	stale, err := bd.ListStaleEscalations(threshold)
	if err != nil {
		return fmt.Errorf("listing stale escalations: %w", err)
	}

	if len(stale) == 0 {
		if !escalateStaleJSON {
			fmt.Printf("No stale escalations (threshold: %s)\n", threshold)
		} else {
			fmt.Println("[]")
		}
		return nil
	}

	// Detect who is reescalating
	reescalatedBy := detectSender()
	if reescalatedBy == "" {
		reescalatedBy = "system"
	}

	// Dry run mode - just show what would happen
	if escalateDryRun {
		fmt.Printf("Would re-escalate %d stale escalations (threshold: %s):\n\n", len(stale), threshold)
		for _, issue := range stale {
			fields := beads.ParseEscalationFields(issue.Description)
			newSeverity := getNextSeverity(fields.Severity)
			willSkip := maxReescalations > 0 && fields.ReescalationCount >= maxReescalations
			if fields.Severity == "critical" {
				willSkip = true
			}

			emoji := severityEmoji(fields.Severity)
			if willSkip {
				fmt.Printf("  %s %s [SKIP] %s\n", emoji, issue.ID, issue.Title)
				if fields.Severity == "critical" {
					fmt.Printf("     Already at critical severity\n")
				} else {
					fmt.Printf("     Already at max reescalations (%d)\n", maxReescalations)
				}
			} else {
				fmt.Printf("  %s %s %s\n", emoji, issue.ID, issue.Title)
				fmt.Printf("     %s → %s (reescalation %d/%d)\n",
					fields.Severity, newSeverity, fields.ReescalationCount+1, maxReescalations)
			}
			fmt.Println()
		}
		return nil
	}

	// Perform re-escalation
	var results []*beads.ReescalationResult
	router := mail.NewRouter(townRoot)
	defer router.WaitPendingNotifications()

	for _, issue := range stale {
		result, err := bd.ReescalateEscalation(issue.ID, reescalatedBy, maxReescalations)
		if err != nil {
			style.PrintWarning("failed to reescalate %s: %v", issue.ID, err)
			// A non-nil result alongside the error is a PARTIAL bump: the record
			// carries the new severity and the error names the delivered copies
			// left behind. Dropping it here would suppress the re-routing mail
			// for a severity that has already been raised.
			if result == nil {
				continue
			}
		}
		results = append(results, result)

		// If not skipped, re-route to new severity targets
		if !result.Skipped {
			actions := escalationConfig.GetRouteForSeverity(result.NewSeverity)
			targets := extractMailTargetsFromActions(actions)

			// Send mail to each target about the reescalation
			for _, target := range targets {
				msg := &mail.Message{
					From:    reescalatedBy,
					To:      target,
					Subject: fmt.Sprintf("[%s→%s] Re-escalated: %s", strings.ToUpper(result.OldSeverity), strings.ToUpper(result.NewSeverity), result.Title),
					Body:    formatReescalationMailBody(result, reescalatedBy),
					Type:    mail.TypeTask,
				}

				// Set priority based on new severity
				switch result.NewSeverity {
				case config.SeverityCritical:
					msg.Priority = mail.PriorityUrgent
				case config.SeverityHigh:
					msg.Priority = mail.PriorityHigh
				case config.SeverityMedium:
					msg.Priority = mail.PriorityNormal
				default:
					msg.Priority = mail.PriorityLow
				}

				if err := router.Send(msg); err != nil {
					style.PrintWarning("failed to send reescalation to %s: %v", target, err)
				}
			}

			// Log to activity feed
			_ = events.LogFeed(events.TypeEscalationSent, reescalatedBy, map[string]interface{}{
				"escalation_id":    result.ID,
				"reescalated":      true,
				"old_severity":     result.OldSeverity,
				"new_severity":     result.NewSeverity,
				"reescalation_num": result.ReescalationNum,
				"targets":          strings.Join(targets, ","),
			})
		}
	}

	// Output results
	if escalateStaleJSON {
		out, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	reescalated := 0
	skipped := 0
	for _, r := range results {
		if r.Skipped {
			skipped++
		} else {
			reescalated++
		}
	}

	if reescalated == 0 && skipped > 0 {
		fmt.Printf("No escalations re-escalated (%d at max level)\n", skipped)
		return nil
	}

	fmt.Printf("🔄 Re-escalated %d stale escalations:\n\n", reescalated)
	for _, result := range results {
		if result.Skipped {
			continue
		}
		emoji := severityEmoji(result.NewSeverity)
		fmt.Printf("  %s %s: %s → %s (reescalation %d)\n",
			emoji, result.ID, result.OldSeverity, result.NewSeverity, result.ReescalationNum)
	}

	if skipped > 0 {
		fmt.Printf("\n  (%d skipped - at max level)\n", skipped)
	}

	return nil
}

func getNextSeverity(severity string) string {
	switch severity {
	case "low":
		return "medium"
	case "medium":
		return "high"
	case "high":
		return "critical"
	default:
		return "critical"
	}
}

func formatReescalationMailBody(result *beads.ReescalationResult, reescalatedBy string) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("Escalation ID: %s", result.ID))
	lines = append(lines, fmt.Sprintf("Severity bumped: %s → %s", result.OldSeverity, result.NewSeverity))
	lines = append(lines, fmt.Sprintf("Reescalation #%d", result.ReescalationNum))
	lines = append(lines, fmt.Sprintf("Reescalated by: %s", reescalatedBy))
	lines = append(lines, "")
	lines = append(lines, "This escalation was not acknowledged within the stale threshold and has been automatically re-escalated to a higher severity.")
	lines = append(lines, "")
	lines = append(lines, "---")
	lines = append(lines, "To acknowledge: gt escalate ack "+result.ID)
	lines = append(lines, "To close: gt escalate close "+result.ID+" --reason \"resolution\"")
	return strings.Join(lines, "\n")
}

func runEscalateShow(cmd *cobra.Command, args []string) error {
	escalationID := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	bd := beads.New(beads.ResolveBeadsDir(townRoot))
	issue, fields, err := bd.GetEscalationBead(escalationID)
	if err != nil {
		return fmt.Errorf("getting escalation: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("escalation not found: %s", escalationID)
	}

	if escalateJSON {
		data := map[string]interface{}{
			"id":           issue.ID,
			"title":        issue.Title,
			"status":       issue.Status,
			"created_at":   issue.CreatedAt,
			"severity":     fields.Severity,
			"reason":       fields.Reason,
			"escalatedBy":  fields.EscalatedBy,
			"escalatedAt":  fields.EscalatedAt,
			"ackedBy":      fields.AckedBy,
			"ackedAt":      fields.AckedAt,
			"closedBy":     fields.ClosedBy,
			"closedReason": fields.ClosedReason,
			"relatedBead":  fields.RelatedBead,
		}
		out, _ := json.MarshalIndent(data, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	emoji := severityEmoji(fields.Severity)
	fmt.Printf("%s Escalation: %s\n", emoji, issue.ID)
	fmt.Printf("  Title: %s\n", issue.Title)
	fmt.Printf("  Status: %s\n", issue.Status)
	fmt.Printf("  Severity: %s\n", fields.Severity)
	fmt.Printf("  Created: %s\n", formatRelativeTime(issue.CreatedAt))
	fmt.Printf("  Escalated by: %s\n", fields.EscalatedBy)
	if fields.Reason != "" {
		fmt.Printf("  Reason: %s\n", fields.Reason)
	}
	if fields.AckedBy != "" {
		fmt.Printf("  Acknowledged by: %s at %s\n", fields.AckedBy, fields.AckedAt)
	}
	if fields.ClosedBy != "" {
		fmt.Printf("  Closed by: %s\n", fields.ClosedBy)
		fmt.Printf("  Resolution: %s\n", fields.ClosedReason)
	}
	if fields.RelatedBead != "" {
		fmt.Printf("  Related: %s\n", fields.RelatedBead)
	}

	return nil
}

// Helper functions

// extractMailTargetsFromActions extracts mail targets from action strings.
// Action format: "mail:target" returns "target"
// E.g., ["bead", "mail:mayor", "email:human"] returns ["mayor"]
func extractMailTargetsFromActions(actions []string) []string {
	var targets []string
	for _, action := range actions {
		if strings.HasPrefix(action, "mail:") {
			target := strings.TrimPrefix(action, "mail:")
			if target != "" {
				targets = append(targets, target)
			}
		}
	}
	return targets
}

// executeExternalActions processes external notification actions (email:, sms:, slack, log).
func executeExternalActions(actions []string, cfg *config.EscalationConfig, beadID, severity, description, townRoot string) []deliveryStatus {
	statuses := []deliveryStatus{}
	for _, action := range actions {
		switch {
		case strings.HasPrefix(action, "email:"):
			status := deliveryStatus{Channel: "email", Target: strings.TrimPrefix(action, "email:"), Severity: severity}
			if cfg.Contacts.HumanEmail == "" {
				status.Warning = "contacts.human_email not configured"
				style.PrintWarning("email action '%s' skipped: contacts.human_email not configured in settings/escalation.json", action)
			} else if cfg.Contacts.SMTPHost == "" {
				status.Warning = "contacts.smtp_host not configured"
				style.PrintWarning("email action '%s' skipped: contacts.smtp_host not configured in settings/escalation.json", action)
			} else {
				if err := sendEscalationEmail(cfg, beadID, severity, description); err != nil {
					status.Error = err.Error()
					style.PrintWarning("email send failed: %v", err)
				} else {
					status.RuntimeNotified = true
					fmt.Printf("  📧 Email sent to %s\n", cfg.Contacts.HumanEmail)
				}
			}
			statuses = append(statuses, status)

		case strings.HasPrefix(action, "sms:"):
			status := deliveryStatus{Channel: "sms", Target: strings.TrimPrefix(action, "sms:"), Severity: severity}
			if cfg.Contacts.HumanSMS == "" {
				status.Warning = "contacts.human_sms not configured"
				style.PrintWarning("sms action '%s' skipped: contacts.human_sms not configured in settings/escalation.json", action)
			} else if cfg.Contacts.SMSWebhook == "" {
				status.Warning = "contacts.sms_webhook not configured"
				style.PrintWarning("sms action '%s' skipped: contacts.sms_webhook not configured in settings/escalation.json", action)
			} else {
				if err := sendEscalationSMS(cfg, beadID, severity, description); err != nil {
					status.Error = err.Error()
					style.PrintWarning("sms send failed: %v", err)
				} else {
					status.RuntimeNotified = true
					fmt.Printf("  📱 SMS sent to %s\n", cfg.Contacts.HumanSMS)
				}
			}
			statuses = append(statuses, status)

		case action == "slack":
			status := deliveryStatus{Channel: "slack", Target: "slack", Severity: severity}
			if cfg.Contacts.SlackWebhook == "" {
				status.Warning = "contacts.slack_webhook not configured"
				style.PrintWarning("slack action skipped: contacts.slack_webhook not configured in settings/escalation.json")
			} else {
				if err := sendEscalationSlack(cfg, beadID, severity, description); err != nil {
					status.Error = err.Error()
					style.PrintWarning("slack post failed: %v", err)
				} else {
					status.RuntimeNotified = true
					fmt.Printf("  💬 Posted to Slack\n")
				}
			}
			statuses = append(statuses, status)

		case action == "log":
			status := deliveryStatus{Channel: "log", Target: "log", Severity: severity}
			if err := writeEscalationLog(townRoot, beadID, severity, description); err != nil {
				status.Error = err.Error()
				style.PrintWarning("log write failed: %v", err)
			} else {
				status.RuntimeNotified = true
				fmt.Printf("  📝 Logged to escalation log\n")
			}
			statuses = append(statuses, status)
		}
	}
	return statuses
}

// sendEscalationEmail sends an escalation notification via SMTP.
func sendEscalationEmail(cfg *config.EscalationConfig, beadID, severity, description string) error {
	host := cfg.Contacts.SMTPHost
	port := cfg.Contacts.SMTPPort
	if port == "" {
		port = "587"
	}
	from := cfg.Contacts.SMTPFrom
	if from == "" {
		from = "gastown@localhost"
	}
	to := cfg.Contacts.HumanEmail
	subject := fmt.Sprintf("[Gas Town %s] %s", strings.ToUpper(severity), description)

	body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n"+
		"Gas Town Escalation\r\n"+
		"====================\r\n"+
		"Bead: %s\r\n"+
		"Severity: %s\r\n"+
		"Description: %s\r\n\r\n"+
		"Acknowledge: gt escalate ack %s\r\n",
		from, to, subject, beadID, strings.ToUpper(severity), description, beadID)

	addr := fmt.Sprintf("%s:%s", host, port)

	var auth smtp.Auth
	if cfg.Contacts.SMTPUser != "" {
		auth = smtp.PlainAuth("", cfg.Contacts.SMTPUser, cfg.Contacts.SMTPPass, host)
	}

	return smtp.SendMail(addr, auth, from, []string{to}, []byte(body))
}

// sendEscalationSlack posts an escalation notification to a Slack webhook.
func sendEscalationSlack(cfg *config.EscalationConfig, beadID, severity, description string) error {
	severityEmoji := map[string]string{
		"critical": "🔴",
		"high":     "🟠",
		"medium":   "🟡",
	}
	emoji := severityEmoji[severity]
	if emoji == "" {
		emoji = "⚪"
	}

	payload := map[string]string{
		"text": fmt.Sprintf("%s *[%s] Escalation %s*\n%s\n_Acknowledge: `gt escalate ack %s`_",
			emoji, strings.ToUpper(severity), beadID, description, beadID),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling slack payload: %w", err)
	}

	resp, err := http.Post(cfg.Contacts.SlackWebhook, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("posting to slack: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slack webhook returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// sendEscalationSMS posts an escalation notification via SMS webhook (e.g. Twilio).
func sendEscalationSMS(cfg *config.EscalationConfig, beadID, severity, description string) error {
	payload := map[string]string{
		"to":   cfg.Contacts.HumanSMS,
		"body": fmt.Sprintf("[Gas Town %s] %s (bead: %s)", strings.ToUpper(severity), description, beadID),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling sms payload: %w", err)
	}

	resp, err := http.Post(cfg.Contacts.SMSWebhook, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("posting to sms webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sms webhook returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// writeEscalationLog appends an escalation entry to the log file.
func writeEscalationLog(townRoot, beadID, severity, description string) error {
	logDir := fmt.Sprintf("%s/logs", townRoot)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("creating log directory: %w", err)
	}
	logPath := fmt.Sprintf("%s/escalations.log", logDir)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	defer f.Close()

	entry := fmt.Sprintf("%s [%s] %s: %s\n", time.Now().Format(time.RFC3339), strings.ToUpper(severity), beadID, description)
	_, err = f.WriteString(entry)
	return err
}

func formatEscalationMailBody(beadID, severity, reason, from, related string) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("Escalation ID: %s", beadID))
	lines = append(lines, fmt.Sprintf("Severity: %s", severity))
	lines = append(lines, fmt.Sprintf("From: %s", from))
	if reason != "" {
		lines = append(lines, "")
		lines = append(lines, "Reason:")
		lines = append(lines, reason)
	}
	if related != "" {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("Related: %s", related))
	}
	lines = append(lines, "")
	lines = append(lines, "---")
	lines = append(lines, "To acknowledge: gt escalate ack "+beadID)
	lines = append(lines, "To close: gt escalate close "+beadID+" --reason \"resolution\"")
	return strings.Join(lines, "\n")
}

func severityEmoji(severity string) string {
	switch severity {
	case config.SeverityCritical:
		return "🚨"
	case config.SeverityHigh:
		return "⚠️"
	case config.SeverityMedium:
		return "📢"
	case config.SeverityLow:
		return "ℹ️"
	default:
		return "📋"
	}
}

func formatRelativeTime(timestamp string) string {
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return timestamp
	}

	duration := time.Since(t)
	if duration < time.Minute {
		return "just now"
	}
	if duration < time.Hour {
		mins := int(duration.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	}
	if duration < 24*time.Hour {
		hours := int(duration.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	}
	days := int(duration.Hours() / 24)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}

// detectSender is defined in mail_send.go - we reuse it here
// If it's not accessible, we fall back to environment variables
func detectSenderFallback() string {
	// Try BD_ACTOR first (most common in agent context)
	if actor := os.Getenv("BD_ACTOR"); actor != "" {
		return actor
	}
	// Try GT_ROLE
	if role := os.Getenv("GT_ROLE"); role != "" {
		return role
	}
	return ""
}
