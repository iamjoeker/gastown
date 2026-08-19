// Package beads provides escalation bead management.
package beads

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EscalationFields holds structured fields for escalation beads.
// These are stored as "key: value" lines in the description.
type EscalationFields struct {
	Severity          string // critical, high, medium, low
	Reason            string // Why this was escalated
	Source            string // Source identifier (e.g., plugin:rebuild-gt, patrol:deacon)
	EscalatedBy       string // Agent address that escalated (e.g., "gastown/Toast")
	EscalatedAt       string // ISO 8601 timestamp
	AckedBy           string // Agent that acknowledged (empty if not acked)
	AckedAt           string // When acknowledged (empty if not acked)
	ClosedBy          string // Agent that closed (empty if not closed)
	ClosedReason      string // Resolution reason (empty if not closed)
	RelatedBead       string // Optional: related bead ID (task, bug, etc.)
	OriginalSeverity  string // Original severity before any re-escalation
	ReescalationCount int    // Number of times this has been re-escalated
	LastReescalatedAt string // When last re-escalated (empty if never)
	LastReescalatedBy string // Who last re-escalated (empty if never)
	Fingerprint       string // Stable duplicate-suppression label
}

// FormatEscalationDescription creates a description string from escalation fields.
func FormatEscalationDescription(title string, fields *EscalationFields) string {
	if fields == nil {
		return title
	}

	var lines []string
	lines = append(lines, title)
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("severity: %s", fields.Severity))
	lines = append(lines, fmt.Sprintf("reason: %s", fields.Reason))
	if fields.Source != "" {
		lines = append(lines, fmt.Sprintf("source: %s", fields.Source))
	} else {
		lines = append(lines, "source: null")
	}
	lines = append(lines, fmt.Sprintf("escalated_by: %s", fields.EscalatedBy))
	lines = append(lines, fmt.Sprintf("escalated_at: %s", fields.EscalatedAt))

	if fields.AckedBy != "" {
		lines = append(lines, fmt.Sprintf("acked_by: %s", fields.AckedBy))
	} else {
		lines = append(lines, "acked_by: null")
	}

	if fields.AckedAt != "" {
		lines = append(lines, fmt.Sprintf("acked_at: %s", fields.AckedAt))
	} else {
		lines = append(lines, "acked_at: null")
	}

	if fields.ClosedBy != "" {
		lines = append(lines, fmt.Sprintf("closed_by: %s", fields.ClosedBy))
	} else {
		lines = append(lines, "closed_by: null")
	}

	if fields.ClosedReason != "" {
		lines = append(lines, fmt.Sprintf("closed_reason: %s", fields.ClosedReason))
	} else {
		lines = append(lines, "closed_reason: null")
	}

	if fields.RelatedBead != "" {
		lines = append(lines, fmt.Sprintf("related_bead: %s", fields.RelatedBead))
	} else {
		lines = append(lines, "related_bead: null")
	}

	// Reescalation fields
	if fields.OriginalSeverity != "" {
		lines = append(lines, fmt.Sprintf("original_severity: %s", fields.OriginalSeverity))
	} else {
		lines = append(lines, "original_severity: null")
	}
	lines = append(lines, fmt.Sprintf("reescalation_count: %d", fields.ReescalationCount))
	if fields.LastReescalatedAt != "" {
		lines = append(lines, fmt.Sprintf("last_reescalated_at: %s", fields.LastReescalatedAt))
	} else {
		lines = append(lines, "last_reescalated_at: null")
	}
	if fields.LastReescalatedBy != "" {
		lines = append(lines, fmt.Sprintf("last_reescalated_by: %s", fields.LastReescalatedBy))
	} else {
		lines = append(lines, "last_reescalated_by: null")
	}
	if fields.Fingerprint != "" {
		lines = append(lines, fmt.Sprintf("fingerprint: %s", fields.Fingerprint))
	} else {
		lines = append(lines, "fingerprint: null")
	}

	return strings.Join(lines, "\n")
}

// ParseEscalationFields extracts escalation fields from an issue's description.
//
// Two description shapes reach here. Escalation RECORDS carry the structured
// block FormatEscalationDescription writes ("escalated_by: ..."). Delivered
// COPIES are usually the mail bead, whose body is formatEscalationMailBody and
// spells the same field "From: ..." — and the copies are what `gt escalate list`
// renders, so every row of the live queue printed "From:" with nothing after it
// (measured on hq 2026-08-18: 10 of 10). "from" is therefore accepted as a
// fallback, never an override: an explicit escalated_by always wins, whichever
// order the two appear in.
func ParseEscalationFields(description string) *EscalationFields {
	fields := &EscalationFields{}
	var mailFrom string
	escalatedByExplicit := false

	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}

		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])
		if value == "null" || value == "" {
			value = ""
		}

		switch strings.ToLower(key) {
		case "severity":
			fields.Severity = value
		case "reason":
			fields.Reason = value
		case "source":
			fields.Source = value
		case "escalated_by":
			fields.EscalatedBy = value
			escalatedByExplicit = true
		case "from":
			mailFrom = value
		case "escalated_at":
			fields.EscalatedAt = value
		case "acked_by":
			fields.AckedBy = value
		case "acked_at":
			fields.AckedAt = value
		case "closed_by":
			fields.ClosedBy = value
		case "closed_reason":
			fields.ClosedReason = value
		case "related_bead":
			fields.RelatedBead = value
		case "original_severity":
			fields.OriginalSeverity = value
		case "reescalation_count":
			if n, err := strconv.Atoi(value); err == nil {
				fields.ReescalationCount = n
			}
		case "last_reescalated_at":
			fields.LastReescalatedAt = value
		case "last_reescalated_by":
			fields.LastReescalatedBy = value
		case "fingerprint":
			fields.Fingerprint = value
		}
	}

	if !escalatedByExplicit && mailFrom != "" {
		fields.EscalatedBy = mailFrom
	}

	return fields
}

// CreateEscalationBead creates an escalation bead for tracking escalations.
// The created_by field is populated from BD_ACTOR env var for provenance tracking.
func (b *Beads) CreateEscalationBead(title string, fields *EscalationFields) (*Issue, error) {
	// Guard against flag-like titles (gt-e0kx5: --help garbage beads)
	if IsFlagLikeTitle(title) {
		return nil, fmt.Errorf("refusing to create escalation bead: %w (got %q)", ErrFlagTitle, title)
	}

	description := FormatEscalationDescription(title, fields)

	// Pass description via stdin (--body-file=-) instead of --description=...
	// to avoid embedding newlines in a flag value. bd 1.0.3+ rejects newline-
	// containing flag values, which broke `gt escalate` for any escalation
	// with structured YAML metadata in the description.
	args := []string{"create", "--json",
		"--title=" + title,
		"--body-file=-",
		"--type=task",
		"--ephemeral",
		"--wisp-type=escalation",
		"--labels=gt:escalation",
	}

	// Add severity as a label for easy filtering
	if fields != nil && fields.Severity != "" {
		args = append(args, fmt.Sprintf("--labels=severity:%s", fields.Severity))
	}
	// ...and as the bead's PRIORITY, which is the field every generic reader
	// renders and every generic filter keys on (gt-nhp). Without it bd's default
	// applies and the record reads P2 whatever was filed: hq-wisp-yro9 went in as
	// -s HIGH about a live nuke hazard and showed up in `bd show` as an ordinary
	// P2. Measured on hq 2026-08-18, ALL 12 most recent escalation records sat at
	// priority 2 regardless of their severity label.
	//
	// It is not cosmetic. AutoClose exempts P0/P1 from staleness closure, so a
	// severity that does not reach the priority column also forfeits the
	// protection that column buys. The delivery copy has carried this since
	// gt-3i4e; the record was left behind.
	if fields != nil {
		if priority, ok := escalationPriority(fields.Severity); ok {
			args = append(args, fmt.Sprintf("--priority=%d", priority))
		}
	}
	if fields != nil && fields.Fingerprint != "" {
		args = append(args, "--labels="+fields.Fingerprint)
	}

	// Default actor from BD_ACTOR env var for provenance tracking
	// Uses getActor() to respect isolated mode (tests)
	if actor := b.getActor(); actor != "" {
		args = append(args, "--actor="+actor)
	}

	out, err := b.runWithStdin([]byte(description), args...)
	if err != nil {
		return nil, err
	}

	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("parsing bd create output: %w", err)
	}

	return &issue, nil
}

// CreateEscalationDeliveryBead creates the durable delivery bead for an
// escalation's "bead" routing action.
//
// The escalation RECORD created by CreateEscalationBead is an ephemeral wisp:
// unversioned, unbacked, dolt_ignore'd and age-GC'd with no restore path. The
// half of an escalation that survives is the delivered copy — and until gt-3i4e
// that copy only ever existed as a side effect of a "mail:" action. A route with
// no mail target (the default "low" route is just ["bead"]) therefore produced
// nothing durable, appeared on no surface anyone reads, and was destroyed when
// the wisp aged out, while the command still reported the bead as created.
//
// The bead written here is the same artifact the mail path produces: labelled
// "gt:escalation", "severity:<sev>" and "escalation:<record-id>", which is what
// ListEscalations, openEscalationCopies, AckEscalation and CloseEscalation all
// key off.
func (b *Beads) CreateEscalationDeliveryBead(title, body, recordID, severity string) (*Issue, error) {
	// Guard against flag-like titles (gt-e0kx5: --help garbage beads)
	if IsFlagLikeTitle(title) {
		return nil, fmt.Errorf("refusing to create escalation delivery bead: %w (got %q)", ErrFlagTitle, title)
	}
	if recordID == "" {
		return nil, errors.New("refusing to create escalation delivery bead: no escalation record ID to link it to")
	}

	// Description goes over stdin for the same reason CreateEscalationBead does:
	// bd 1.0.3+ rejects newlines inside a --description flag value (dc-1bxe).
	args := []string{"create", "--json",
		"--title=" + title,
		"--body-file=-",
		"--type=task",
		"--labels=gt:escalation",
		"--labels=" + EscalationLinkLabelPrefix + recordID,
	}
	if severity != "" {
		args = append(args, "--labels=severity:"+severity)
	}
	if priority, ok := escalationPriority(severity); ok {
		args = append(args, fmt.Sprintf("--priority=%d", priority))
	}
	if actor := b.getActor(); actor != "" {
		args = append(args, "--actor="+actor)
	}

	out, err := b.runWithStdin([]byte(body), args...)
	if err != nil {
		return nil, err
	}

	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("parsing bd create output: %w", err)
	}
	return &issue, nil
}

// escalationPriority maps an escalation severity onto the bead priority the
// `gt escalate` help text documents (critical P0 … low P3). Returns false for an
// unrecognised severity so the bead keeps bd's default rather than silently
// landing at P0.
func escalationPriority(severity string) (int, bool) {
	switch severity {
	case "critical":
		return 0, true
	case "high":
		return 1, true
	case "medium":
		return 2, true
	case "low":
		return 3, true
	}
	return 0, false
}

// EscalationLinkLabelPrefix marks a delivered escalation copy with the ID of the
// escalation record it belongs to.
//
// Every escalation exists as TWO kinds of bead. `gt escalate` creates one
// ephemeral RECORD (a wisp, carrying the structured severity/reason/closed_by
// fields — this is the ID printed in the escalation mail body and the one the
// documented close command takes) and then delivers that escalation as mail,
// producing one durable COPY per target in the issues table. The copies are the
// beads `gt escalate list` and the Mayor's queue actually render, and they carry
// "escalation:<record-id>" plus "thread:<record-id>".
//
// Nothing used to reconcile the two, so closing the record left every copy open
// forever and the queue carried resolved escalations as live HIGHs (gt-4xl).
const EscalationLinkLabelPrefix = "escalation:"

// EscalationRecordID returns the ID of the escalation record a bead belongs to:
// the "escalation:<id>" target for a delivered copy, or the bead's own ID when
// it is the record itself.
//
// Note the trailing colon in the prefix — it keeps the fingerprint label
// ("escalation-fp:...") from being mistaken for a link.
func EscalationRecordID(issue *Issue) string {
	if issue == nil {
		return ""
	}
	for _, label := range issue.Labels {
		if id, ok := strings.CutPrefix(label, EscalationLinkLabelPrefix); ok && id != "" && id != issue.ID {
			return id
		}
	}
	return issue.ID
}

// resolveEscalation looks up the bead named by id and the escalation record it
// belongs to.
//
// named is always the bead the caller asked for. record is the bead holding the
// structured escalation fields, and is nil when the record has already been
// reaped (records are ephemeral wisps with a TTL, so a long-lived copy can
// outlive its record). recordID is returned even in that case, since it still
// identifies the copies belonging to this escalation.
func (b *Beads) resolveEscalation(id string) (named, record *Issue, recordID string, err error) {
	named, err = b.forIssueID(id).Show(id)
	if err != nil {
		return nil, nil, "", err
	}

	// Verify it's an escalation
	if !HasLabel(named, "gt:escalation") {
		return nil, nil, "", fmt.Errorf("issue %s is not an escalation bead (missing gt:escalation label)", id)
	}

	recordID = EscalationRecordID(named)
	if recordID == named.ID {
		return named, named, recordID, nil
	}

	// The caller named a delivered copy — which is what `gt escalate list`
	// prints, so it is the ID a reader is most likely to have in hand.
	record, err = b.forIssueID(recordID).Show(recordID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return named, nil, recordID, nil
		}
		return nil, nil, "", err
	}
	return named, record, recordID, nil
}

// openEscalationCopies returns the open delivered copies of an escalation record.
func (b *Beads) openEscalationCopies(recordID string) ([]*Issue, error) {
	if recordID == "" {
		return nil, nil
	}
	out, err := b.run("list", "--label="+EscalationLinkLabelPrefix+recordID, "--status=open", "--json")
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}
	return issues, nil
}

// AckEscalation acknowledges an escalation bead.
// Sets acked_by and acked_at fields on the escalation record, and adds the
// "acked" label to the record and to every open delivered copy — the copies are
// what `gt escalate list` renders, so an ack that only touched the record showed
// up nowhere.
func (b *Beads) AckEscalation(id, ackedBy string) error {
	_, record, recordID, err := b.resolveEscalation(id)
	if err != nil {
		return err
	}

	if record != nil {
		// Parse existing fields
		fields := ParseEscalationFields(record.Description)
		fields.AckedBy = ackedBy
		fields.AckedAt = time.Now().Format(time.RFC3339)

		// Format new description
		description := FormatEscalationDescription(record.Title, fields)

		if err := b.forIssueID(record.ID).Update(record.ID, UpdateOptions{
			Description: &description,
			AddLabels:   []string{"acked"},
		}); err != nil {
			return err
		}
	}

	copies, err := b.openEscalationCopies(recordID)
	if err != nil {
		return fmt.Errorf("finding delivered copies of escalation %s: %w", recordID, err)
	}

	var failures []string
	for _, copied := range copies {
		if record != nil && copied.ID == record.ID {
			continue
		}
		// Only the label: a copy's description is the mail body, not the
		// structured escalation record, and must not be overwritten.
		if err := b.forIssueID(copied.ID).Update(copied.ID, UpdateOptions{AddLabels: []string{"acked"}}); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", copied.ID, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("escalation record %s acknowledged, but %s: %s",
			recordID, pluralCopies(len(failures), "could not be marked acked"), strings.Join(failures, "; "))
	}
	return nil
}

// EscalationCloseResult reports every bead a CloseEscalation touched.
type EscalationCloseResult struct {
	RecordID string   // the escalation record (an ephemeral wisp, normally)
	CopyIDs  []string // delivered mail copies closed alongside it
}

// CloseEscalation closes an escalation with a resolution reason.
//
// It closes BOTH halves of the escalation: the record (setting closed_by and
// closed_reason) and every open delivered copy. Either ID may be passed — the
// record ID printed in the escalation mail, or the copy ID printed by
// `gt escalate list` — and the escalation is resolved the same way.
//
// Closing an escalation whose record is already closed is not an error: it
// reconciles the copies that an earlier record-only close left stranded.
func (b *Beads) CloseEscalation(id, closedBy, reason string) (*EscalationCloseResult, error) {
	_, record, recordID, err := b.resolveEscalation(id)
	if err != nil {
		return nil, err
	}

	result := &EscalationCloseResult{RecordID: recordID}
	if record != nil {
		if err := b.closeEscalationRecord(record, closedBy, reason); err != nil {
			return nil, fmt.Errorf("closing escalation record %s: %w", record.ID, err)
		}
	}

	copies, err := b.openEscalationCopies(recordID)
	if err != nil {
		return result, fmt.Errorf("escalation record %s closed, but its delivered copies could not be listed and may stay in the queue: %w", recordID, err)
	}

	var failures []string
	for _, copied := range copies {
		if record != nil && copied.ID == record.ID {
			continue
		}
		if err := b.closeEscalationCopy(copied, reason); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", copied.ID, err))
			continue
		}
		result.CopyIDs = append(result.CopyIDs, copied.ID)
	}
	if len(failures) > 0 {
		return result, fmt.Errorf("escalation record %s closed, but %s and will stay in the queue: %s",
			recordID, pluralCopies(len(failures), "could not be closed"), strings.Join(failures, "; "))
	}

	return result, nil
}

// closeEscalationRecord writes the resolution onto the escalation record and
// closes it. A record that is already closed is left alone so the caller can
// still reconcile its copies.
func (b *Beads) closeEscalationRecord(issue *Issue, closedBy, reason string) error {
	if strings.EqualFold(issue.Status, "closed") {
		return nil
	}

	// Parse existing fields
	fields := ParseEscalationFields(issue.Description)
	fields.ClosedBy = closedBy
	fields.ClosedReason = reason

	// Format new description
	description := FormatEscalationDescription(issue.Title, fields)

	target := b.forIssueID(issue.ID)

	// Update description first
	if err := target.Update(issue.ID, UpdateOptions{
		Description: &description,
		AddLabels:   []string{"resolved"},
	}); err != nil {
		return err
	}

	// Close the issue
	_, err := target.run("close", issue.ID, "--reason="+reason)
	return err
}

// closeEscalationCopy closes one delivered escalation mail bead.
//
// The copy's description is the mail body, not the structured escalation
// record, so it is labelled but never rewritten. --force is required because
// the copy is assigned to its recipient (mayor/, typically) and bd refuses to
// close another agent's bead; the copy is a delivery artifact of the escalation
// being resolved, not independent work of the recipient's.
func (b *Beads) closeEscalationCopy(issue *Issue, reason string) error {
	target := b.forIssueID(issue.ID)
	if err := target.Update(issue.ID, UpdateOptions{AddLabels: []string{"resolved"}}); err != nil {
		return err
	}
	_, err := target.run("close", issue.ID, "--force", "--reason="+reason)
	return err
}

// pluralCopies renders "1 delivered copy <verb>" / "N delivered copies <verb>".
func pluralCopies(n int, verb string) string {
	if n == 1 {
		return fmt.Sprintf("1 delivered copy %s", verb)
	}
	return fmt.Sprintf("%d delivered copies %s", n, verb)
}

// GetEscalationBead retrieves an escalation bead by ID.
// Returns nil if not found.
func (b *Beads) GetEscalationBead(id string) (*Issue, *EscalationFields, error) {
	issue, err := b.forIssueID(id).Show(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	if !HasLabel(issue, "gt:escalation") {
		return nil, nil, fmt.Errorf("issue %s is not an escalation bead (missing gt:escalation label)", id)
	}

	fields := ParseEscalationFields(issue.Description)
	return issue, fields, nil
}

// ListEscalations returns all open escalation beads.
func (b *Beads) ListEscalations() ([]*Issue, error) {
	out, err := b.run("list", "--label=gt:escalation", "--status=open", "--json")
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	return b.dropResolvedEscalations(filterEscalationRecords(issues)), nil
}

// ListEscalationsByFingerprint returns open escalation beads matching a stable fingerprint label.
func (b *Beads) ListEscalationsByFingerprint(fingerprintLabel string) ([]*Issue, error) {
	if fingerprintLabel == "" {
		return nil, nil
	}
	out, err := b.run("list",
		"--label=gt:escalation",
		"--label="+fingerprintLabel,
		"--status=open",
		"--json",
	)
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	return b.dropResolvedEscalations(filterEscalationRecords(issues)), nil
}

// ListEscalationsBySeverity returns open escalation beads filtered by severity.
func (b *Beads) ListEscalationsBySeverity(severity string) ([]*Issue, error) {
	out, err := b.run("list",
		"--label=gt:escalation",
		"--label=severity:"+severity,
		"--status=open",
		"--json",
	)
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	return b.dropResolvedEscalations(filterEscalationRecords(issues)), nil
}

// dropResolvedEscalations removes delivered escalation copies whose escalation
// record has already been closed.
//
// A copy is a durable issue and its record is an ephemeral wisp, so closing the
// record has never propagated to the copy. Without this filter every escalation
// closed by the documented `gt escalate close <record-id>` stayed in the queue
// as an open HIGH forever, and `gt escalate stale` would re-escalate resolved
// escalations up to critical (gt-4xl). New closes now reconcile both halves;
// this covers the copies stranded before that, with no migration.
//
// Fails OPEN: a record that cannot be read — reaped, or Dolt unreachable — keeps
// its copy listed. Hiding a live escalation is far worse than showing a resolved
// one, and a missing record is not evidence of resolution.
func (b *Beads) dropResolvedEscalations(issues []*Issue) []*Issue {
	closedRecord := make(map[string]bool)

	kept := issues[:0]
	for _, issue := range issues {
		recordID := EscalationRecordID(issue)
		if recordID == "" || recordID == issue.ID {
			kept = append(kept, issue)
			continue
		}

		resolved, checked := closedRecord[recordID]
		if !checked {
			record, err := b.forIssueID(recordID).Show(recordID)
			resolved = err == nil && record != nil && strings.EqualFold(record.Status, "closed")
			closedRecord[recordID] = resolved
		}
		if resolved {
			continue
		}
		kept = append(kept, issue)
	}
	return kept
}

// filterEscalationRecords drops mail-only beads that are not themselves escalations.
//
// It used to drop anything carrying "gt:message", on the assumption that a root
// escalation and its mail copy could be told apart by that label. They cannot:
// `gt escalate` delivers the escalation AS mail, so the root bead carries
// "gt:message" too. Measured on the hq store (hq-q0lc): 74 of 74 escalation beads
// carry "gt:message" and ZERO have the label-free "root" shape the old unit test
// constructed. The old filter therefore discarded every escalation, always, and
// `gt escalate list` could never return a non-empty result — while
// `gt escalate list --all` looked fine because it bypasses this helper.
//
// Requiring the ABSENCE of "gt:escalation" keeps the original intent (a pure mail
// message is not an escalation record) without depending on a label that does not
// discriminate. Callers here already query --label=gt:escalation, so in practice
// nothing is dropped; the guard remains meaningful for any caller passing a
// broader set.
func filterEscalationRecords(issues []*Issue) []*Issue {
	filtered := issues[:0]
	for _, issue := range issues {
		if HasLabel(issue, "gt:message") && !HasLabel(issue, "gt:escalation") {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}

// ListStaleEscalations returns escalations older than the given threshold.
// threshold is a duration string like "1h" or "30m".
func (b *Beads) ListStaleEscalations(threshold time.Duration) ([]*Issue, error) {
	// Get all open escalations
	escalations, err := b.ListEscalations()
	if err != nil {
		return nil, err
	}

	cutoff := time.Now().Add(-threshold)
	var stale []*Issue

	for _, issue := range escalations {
		// Skip acknowledged escalations
		if HasLabel(issue, "acked") {
			continue
		}

		// Check if older than threshold
		createdAt, err := time.Parse(time.RFC3339, issue.CreatedAt)
		if err != nil {
			continue // Skip if can't parse
		}

		if createdAt.Before(cutoff) {
			stale = append(stale, issue)
		}
	}

	return stale, nil
}

// ReescalationResult holds the result of a reescalation operation.
type ReescalationResult struct {
	ID              string
	Title           string
	OldSeverity     string
	NewSeverity     string
	ReescalationNum int
	Skipped         bool
	SkipReason      string
}

// ReescalateEscalation bumps the severity of an escalation and updates tracking fields.
// Returns the new severity if successful, or an error.
// reescalatedBy should be the identity of the agent/process doing the reescalation.
// maxReescalations limits how many times an escalation can be bumped (0 = unlimited).
func (b *Beads) ReescalateEscalation(id, reescalatedBy string, maxReescalations int) (*ReescalationResult, error) {
	// Get the escalation
	issue, fields, err := b.GetEscalationBead(id)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, fmt.Errorf("escalation not found: %s", id)
	}

	result := &ReescalationResult{
		ID:          id,
		Title:       issue.Title,
		OldSeverity: fields.Severity,
	}

	// Check if already at max reescalations
	if maxReescalations > 0 && fields.ReescalationCount >= maxReescalations {
		result.Skipped = true
		result.SkipReason = fmt.Sprintf("already at max reescalations (%d)", maxReescalations)
		return result, nil
	}

	// Check if already at critical (can't bump further)
	if fields.Severity == "critical" {
		result.Skipped = true
		result.SkipReason = "already at critical severity"
		result.NewSeverity = "critical"
		return result, nil
	}

	// Save original severity on first reescalation
	if fields.OriginalSeverity == "" {
		fields.OriginalSeverity = fields.Severity
	}

	// Bump severity
	newSeverity := bumpSeverity(fields.Severity)
	fields.Severity = newSeverity
	fields.ReescalationCount++
	fields.LastReescalatedAt = time.Now().Format(time.RFC3339)
	fields.LastReescalatedBy = reescalatedBy

	result.NewSeverity = newSeverity
	result.ReescalationNum = fields.ReescalationCount

	// Format new description
	description := FormatEscalationDescription(issue.Title, fields)

	// Update the bead with new description, severity label and priority.
	//
	// The priority moves with the severity for the same reason it is set at
	// creation (gt-nhp): a re-escalation exists to make an ignored escalation
	// louder, and leaving the priority where it was means the one surface most
	// readers sort by never registers the bump. It also restores the P0/P1
	// staleness exemption that a low-severity escalation does not have — which
	// matters most here, since being ignored is what triggered the bump.
	opts := UpdateOptions{
		Description:  &description,
		AddLabels:    []string{"reescalated", "severity:" + newSeverity},
		RemoveLabels: []string{"severity:" + result.OldSeverity},
	}
	if priority, ok := escalationPriority(newSeverity); ok {
		opts.Priority = &priority
	}
	if err := b.forIssueID(id).Update(id, opts); err != nil {
		return nil, fmt.Errorf("updating escalation: %w", err)
	}

	return result, nil
}

// bumpSeverity returns the next higher severity level.
// low -> medium -> high -> critical
func bumpSeverity(severity string) string {
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
