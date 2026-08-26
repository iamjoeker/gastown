package cmd

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
)

// escalationRequest carries everything raiseEscalation needs. `gt escalate`
// fills it from its flags; callers inside other flows (gt done, gt-lj2n) fill it
// directly. It exists so a second caller does not have to reach for the
// command's package-level flag vars, and so nobody reimplements the routing —
// the "durable twin" rules in deliverEscalationBead are the part that decides
// whether an escalation is visible at all or is GC'd unread (gt-3i4e).
type escalationRequest struct {
	TownRoot    string
	AgentID     string
	Description string
	Severity    string
	Reason      string
	Source      string
	RelatedBead string
	Fingerprint string // already normalized via escalationFingerprintLabel
	Config      *config.EscalationConfig
}

// escalationOutcome reports what raiseEscalation actually did. Delivered is the
// load-bearing field: a record that reached nobody is an ephemeral wisp that
// will be garbage-collected, so it must never be reported as a success.
type escalationOutcome struct {
	RecordID      string
	DurableBeadID string
	Actions       []string
	Targets       []string
	Statuses      []deliveryStatus
	Delivered     bool
	Duplicate     bool
}

// raiseEscalation creates the escalation record and runs its configured routing:
// mail to each target, the durable "bead" action, external notifications, and
// the activity feed entry. It does not print a summary and does not turn an
// undelivered escalation into an error — both are the caller's to decide.
func raiseEscalation(req escalationRequest) (*escalationOutcome, error) {
	// The positional description becomes a bead TITLE, and titles are capped.
	// Checked here, before anything is created, because the alternative is worse
	// than it looks: bd rejects the create, gt wraps the rejection together with
	// the whole failing argv, and the operator is shown their own 2437-character
	// message echoed back with the real diagnostic on the far side of it. That
	// wall of text is what got an exit 1 retold as an exit 0 in gt-1t0v — the
	// reader saw their message printed, could not find an error, and walked away
	// believing a HIGH escalation about a wedged deacon existed (gt-khq8).
	if err := checkEscalationTitleLen(req.Severity, req.Description); err != nil {
		return nil, err
	}

	bd := beads.New(beads.ResolveBeadsDir(req.TownRoot))
	if req.Fingerprint != "" {
		matches, err := bd.ListEscalationsByFingerprint(req.Fingerprint)
		if err != nil {
			return nil, fmt.Errorf("checking escalation fingerprint: %w", err)
		}
		if len(matches) > 0 {
			return &escalationOutcome{RecordID: matches[0].ID, Duplicate: true}, nil
		}
	}

	fields := &beads.EscalationFields{
		Severity:    req.Severity,
		Reason:      req.Reason,
		Source:      req.Source,
		EscalatedBy: req.AgentID,
		EscalatedAt: time.Now().Format(time.RFC3339),
		RelatedBead: req.RelatedBead,
		Fingerprint: req.Fingerprint,
	}

	issue, err := bd.CreateEscalationBead(req.Description, fields)
	if err != nil {
		return nil, fmt.Errorf("creating escalation bead: %w", err)
	}

	// Get routing actions for this severity
	actions := req.Config.GetRouteForSeverity(req.Severity)
	targets := extractMailTargetsFromActions(actions)

	// Send mail to each target (actions with "mail:" prefix)
	router := mail.NewRouter(req.TownRoot)
	defer router.WaitPendingNotifications()
	// Statuses start EMPTY. A status is appended only once its channel has been
	// attempted, and its success flags are set only from a real result. Seeding
	// this slice with a hardcoded {Channel: "bead", Created: true} was the whole
	// of the "bead" action's implementation (gt-3i4e): a claim that could never
	// report failure, printed on every escalation at every severity.
	var statuses []deliveryStatus
	subject := escalationSubject(req.Severity, req.Description)
	for _, target := range targets {
		status := deliveryStatus{Target: target, Channel: "mail", Severity: req.Severity, NotificationRoute: "mail+nudge"}
		msg := &mail.Message{
			From:     req.AgentID,
			To:       target,
			Subject:  subject,
			Body:     formatEscalationMailBody(issue.ID, req.Severity, req.Reason, req.AgentID, req.RelatedBead),
			Type:     mail.TypeEscalation,
			ThreadID: issue.ID,
		}

		// Set priority based on severity
		switch req.Severity {
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
			status.Error = err.Error()
			statuses = append(statuses, status)
			style.PrintWarning("failed to send to %s: %v", target, err)
			continue
		}
		status.Persisted = true
		status.RuntimeNotified = true

		mailBeads := beads.New(beads.ResolveBeadsDir(req.TownRoot))
		mailIssue, err := mailBeads.FindLatestIssueByTitleAndAssignee(msg.Subject, mail.AddressToIdentity(target))
		if err != nil {
			status.Warning = fmt.Sprintf("annotation lookup failed: %v", err)
			statuses = append(statuses, status)
			style.PrintWarning("failed to annotate escalation mail for %s: %v", target, err)
			continue
		}
		status.BeadID = mailIssue.ID

		addLabels := []string{
			fmt.Sprintf("severity:%s", req.Severity),
			fmt.Sprintf("escalation:%s", issue.ID),
		}
		if err := mailBeads.Update(mailIssue.ID, beads.UpdateOptions{AddLabels: addLabels}); err != nil {
			status.Warning = fmt.Sprintf("annotation update failed: %v", err)
			style.PrintWarning("failed to annotate escalation mail labels for %s: %v", target, err)
		} else {
			status.Annotated = true
		}
		statuses = append(statuses, status)
	}

	// Execute the "bead" routing action. It runs after the mail loop on purpose:
	// a successfully annotated mail copy already IS the durable delivery bead, so
	// this only creates one when nothing else has (see deliverEscalationBead).
	outcome := &escalationOutcome{RecordID: issue.ID, Actions: actions, Targets: targets}
	beadStatus := deliverEscalationBead(bd, actions, statuses, issue.ID, req.Severity, subject,
		formatEscalationDeliveryBody(issue.ID, req.Description, fields))
	if beadStatus != nil {
		outcome.DurableBeadID = beadStatus.BeadID
		// Prepend: "bead" is listed first in every configured route, and the
		// durable record reads before the notifications about it.
		statuses = append([]deliveryStatus{*beadStatus}, statuses...)
	}

	// Process external notification actions (email:, sms:, slack, log)
	statuses = append(statuses, executeExternalActions(actions, req.Config, issue.ID, req.Severity, req.Description, req.TownRoot)...)

	// Log to activity feed
	payload := events.EscalationPayload(issue.ID, req.AgentID, strings.Join(targets, ","), req.Description)
	payload["severity"] = req.Severity
	payload["actions"] = strings.Join(actions, ",")
	if req.Source != "" {
		payload["source"] = req.Source
	}
	_ = events.LogFeed(events.TypeEscalationSent, req.AgentID, payload)

	outcome.Statuses = statuses
	outcome.Delivered = escalationWasDelivered(statuses)
	return outcome, nil
}

// escalationTitleMaxRunes mirrors the title length bd enforces on the beads an
// escalation is made of. It is a pre-flight bound, not the authority: bd still
// validates every create, so a cap that moves is caught there rather than let
// through here — this only decides whether the operator gets a sentence or a
// wall of their own argument echoed back.
const escalationTitleMaxRunes = 500

// escalationSubject is the title carried by an escalation's delivered copies.
// The record's own title is the bare description; every copy is prefixed with
// the severity, so this is the longer of the two and the one a length check has
// to measure. Both are produced here so the check and the send cannot disagree.
func escalationSubject(severity, description string) string {
	return fmt.Sprintf("[%s] %s", strings.ToUpper(severity), description)
}

// checkEscalationTitleLen rejects a description too long to survive as a bead
// title, before anything is created.
//
// The failure it replaces did exit non-zero — measured 2026-08-26, exit 1 with a
// positive control — so this is not a fix for a silent success. It is a fix for
// an unreadable one: gt wraps a bd failure together with the whole failing argv,
// which for this case means the operator's entire message is printed back at
// them with the real diagnostic past the end of it. That is indistinguishable
// from a command that echoed the message and succeeded, which is how it was
// reported (gt-1t0v, carried to gt-khq8).
//
// It also stops the half-filed state the old path could reach: a description
// just under the cap files the RECORD and then fails every delivery copy on the
// severity prefix, leaving an ephemeral wisp nobody was told about.
func checkEscalationTitleLen(severity, description string) error {
	subject := escalationSubject(severity, description)
	n := utf8.RuneCountInString(subject)
	if n <= escalationTitleMaxRunes {
		return nil
	}
	return fmt.Errorf("escalation NOT filed: the description is the escalation's TITLE and is capped at %d characters, "+
		"but this one is %d including the %q prefix its delivered copies carry (description alone: %d). "+
		"Nothing was recorded and nobody was told. Keep the description to one line — it starts %q — "+
		"and put the detail in --reason, or --stdin to avoid shell quoting",
		escalationTitleMaxRunes, n, fmt.Sprintf("[%s] ", strings.ToUpper(severity)),
		utf8.RuneCountInString(description), escalationTitlePreview(description))
}

// escalationTitlePreview returns a short, rune-safe, single-line head of an
// over-long description — enough to identify which message was rejected without
// reprinting the thing that made the error unreadable in the first place.
func escalationTitlePreview(description string) string {
	if i := strings.IndexByte(description, '\n'); i >= 0 {
		description = description[:i]
	}
	const previewRunes = 60
	runes := []rune(description)
	if len(runes) <= previewRunes {
		return description
	}
	return string(runes[:previewRunes]) + "…"
}

// undeliveredEscalationError is the error a caller returns when an escalation
// was recorded but routed to nobody. The record is an ephemeral wisp with
// nothing durable referencing it, so it will be garbage-collected unread —
// exactly how the dn-qpk disposition was lost for 16 days (gt-3i4e).
func undeliveredEscalationError(townRoot, recordID, severity string, actions []string) error {
	return fmt.Errorf("escalation %s was recorded but NOT delivered: the %q route (%s) produced no delivery, "+
		"and the record is an ephemeral wisp that will be garbage-collected with no trace. "+
		"Fix the route in %s, or re-file at a higher severity",
		recordID, severity, strings.Join(actions, ", "), config.EscalationConfigPath(townRoot))
}
