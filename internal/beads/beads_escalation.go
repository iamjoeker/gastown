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

	// Add severity as a label for easy filtering, and as the bead's priority.
	// The label alone was all the record ever carried, so a `--severity high`
	// escalation landed at bd's default P2 while the help text documents HIGH as
	// P1 (gt-psh) — anything triaging or sorting by priority, which is most
	// things, read every escalation as routine work.
	if fields != nil && fields.Severity != "" {
		args = append(args, fmt.Sprintf("--labels=severity:%s", fields.Severity))
		if priority, ok := escalationPriority(fields.Severity); ok {
			args = append(args, fmt.Sprintf("--priority=%d", priority))
		}
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
// named is the bead the caller asked for, and is nil when that bead is a record
// that has already been reaped. record is the bead holding the structured
// escalation fields, and is nil when the record has been reaped (records are
// ephemeral wisps with a TTL, so a long-lived copy can outlive its record).
// recordID is returned even in that case, since it still identifies the copies
// belonging to this escalation.
func (b *Beads) resolveEscalation(id string) (named, record *Issue, recordID string, err error) {
	named, err = b.forIssueID(id).Show(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// The reaped-record case, and the one every reader hits: the ID
			// printed in the escalation mail body and in the delivery bead's
			// "To close: gt escalate close <id>" is the RECORD's, and the record
			// is an ephemeral wisp that ages out from under it. Failing here left
			// the documented ack and close commands returning "not found" while
			// the durable copies stayed live in the Mayor's queue forever — the
			// copies are still reachable by the record's own link label, so
			// resolve against them rather than giving up (gt-psh).
			if copies, listErr := b.openEscalationCopies(id); listErr == nil && len(copies) > 0 {
				return nil, nil, id, nil
			}
		}
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
	open, _, err := b.ListEscalationsWithStranded()
	return open, err
}

// ListEscalationsWithStranded returns the open escalations, and separately the
// open delivered copies this list hides because their escalation record is
// closed.
//
// The hidden set is not a detail. It is the whole difference between what
// `gt escalate list` prints and what every bead-counting surface counts, and
// losing it without trace is how the two came to disagree in silence: measured
// on hq 2026-08-23, the list printed 3 while `bd list --label=gt:escalation
// --status=open` returned 4, with no output of any kind about the fourth
// (gt-f0b3). The missing bead was a HIGH the Mayor had been told was live.
//
// The hiding itself is right — a resolved escalation must not sit in the queue
// as an open HIGH (gt-4xl) — but its evidence is weaker than it looks. A record
// is an ephemeral wisp that anything can close: `bd close` run by hand, as
// happened to hq-wisp-aor1wa, closes it without touching the copy the same way
// a pre-gt-4xl `gt escalate close` did. So "record closed" means "probably
// resolved, and nobody reconciled the halves", never "resolved". Returning the
// set lets the caller say so and name the reconcile.
func (b *Beads) ListEscalationsWithStranded() (open, stranded []*Issue, err error) {
	issues, err := b.listOpenEscalationIssues()
	if err != nil {
		return nil, nil, err
	}

	open, stranded = b.partitionResolvedEscalations(filterEscalationRecords(issues))
	return open, stranded, nil
}

// listOpenEscalationIssues runs the open-escalation query, including the pinned
// escalations `bd list --status=open` leaves out.
//
// `bd list` has no "include pinned" flag. It has `--pinned` (pinned ONLY) and
// `--no-pinned` (exclude them), and its DEFAULT is exactly `--no-pinned`:
// measured on the hq store 2026-08-26, `bd list --status=open --limit 0`
// returned 686 issues, `--no-pinned` returned the same 686, `--pinned` returned
// 3, and `SELECT pinned, COUNT(*) ... WHERE status='open' GROUP BY pinned`
// returned 686/3. So a pinned open issue is not in the default result set at
// all, and nothing says so.
//
// All three pinned issues in that measurement were escalations, and `gt escalate
// list` printed "No escalations found" while they sat open — the P0 in gt-qee3.
// Pinning is what an operator does to an escalation to keep it in view; with a
// single default query that act deletes it from the one surface whose whole job
// is to show what is live. `--all` was unaffected because it bypasses this
// query, which is what made the renderer look healthy while the filter was not.
//
// The two result sets are unioned rather than switched between, so this stays
// correct if bd's default ever changes to include pinned issues.
func (b *Beads) listOpenEscalationIssues(extraFilters ...string) ([]*Issue, error) {
	base := append([]string{"list", "--label=gt:escalation", "--status=open"}, extraFilters...)

	var all []*Issue
	seen := make(map[string]bool)
	for _, pinnedFilter := range []string{"--no-pinned", "--pinned"} {
		args := append(append([]string{}, base...), pinnedFilter, "--json")
		out, err := b.run(args...)
		if err != nil {
			return nil, err
		}

		var issues []*Issue
		if err := json.Unmarshal(out, &issues); err != nil {
			return nil, fmt.Errorf("parsing bd list output: %w", err)
		}
		for _, issue := range issues {
			if issue == nil || seen[issue.ID] {
				continue
			}
			seen[issue.ID] = true
			all = append(all, issue)
		}
	}
	return all, nil
}

// ListEscalationsByFingerprint returns open escalation beads matching a stable fingerprint label.
//
// This is the duplicate-suppression probe, so a pinned escalation missing from
// it does not merely go unseen: it re-fires as a fresh escalation on every
// raise. See listOpenEscalationIssues for why the pinned half has to be asked
// for separately.
func (b *Beads) ListEscalationsByFingerprint(fingerprintLabel string) ([]*Issue, error) {
	if fingerprintLabel == "" {
		return nil, nil
	}
	issues, err := b.listOpenEscalationIssues("--label=" + fingerprintLabel)
	if err != nil {
		return nil, err
	}

	kept, _ := b.partitionResolvedEscalations(filterEscalationRecords(issues))
	return kept, nil
}

// ListEscalationsBySeverity returns open escalation beads filtered by severity.
func (b *Beads) ListEscalationsBySeverity(severity string) ([]*Issue, error) {
	issues, err := b.listOpenEscalationIssues("--label=severity:" + severity)
	if err != nil {
		return nil, err
	}

	kept, _ := b.partitionResolvedEscalations(filterEscalationRecords(issues))
	return kept, nil
}

// partitionResolvedEscalations splits delivered escalation copies into the ones
// the queue should show and the ones whose escalation record has already been
// closed.
//
// A copy is a durable issue and its record is an ephemeral wisp, so closing the
// record has never propagated to the copy. Without this split every escalation
// closed by the documented `gt escalate close <record-id>` stayed in the queue
// as an open HIGH forever, and `gt escalate stale` would re-escalate resolved
// escalations up to critical (gt-4xl). New closes now reconcile both halves;
// this covers the copies stranded before that, with no migration.
//
// The resolved half is RETURNED rather than discarded. These beads are still
// open, so they still count everywhere beads are counted, and dropping them on
// the floor made the queue and those counts disagree with nothing to explain it
// (gt-f0b3).
//
// Fails OPEN: a record that cannot be read — reaped, or Dolt unreachable — keeps
// its copy listed. Hiding a live escalation is far worse than showing a resolved
// one, and a missing record is not evidence of resolution.
func (b *Beads) partitionResolvedEscalations(issues []*Issue) (kept, resolvedCopies []*Issue) {
	closedRecord := make(map[string]bool)

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
			resolvedCopies = append(resolvedCopies, issue)
			continue
		}
		kept = append(kept, issue)
	}
	return kept, resolvedCopies
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
	seen := make(map[string]bool)

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

		if !createdAt.Before(cutoff) {
			continue
		}

		// One entry per escalation, not one per bead. A record and every one of
		// its delivered copies carry "gt:escalation", so an un-deduped list
		// re-escalated the same escalation once per bead in a single pass —
		// low could reach high in one run, and each bump re-mails the targets.
		recordID := EscalationRecordID(issue)
		if seen[recordID] {
			continue
		}
		seen[recordID] = true
		stale = append(stale, issue)
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
//
// Either half's ID may be passed. The bump is written to the RECORD, which is
// where the structured severity and reescalation history live — resolving first
// matters because `gt escalate stale` feeds this whatever bead its list turned
// up, and running the bump against a delivered copy would rewrite that copy's
// mail body as a structured record and re-derive severity from it.
func (b *Beads) ReescalateEscalation(id, reescalatedBy string, maxReescalations int) (*ReescalationResult, error) {
	_, record, recordID, err := b.resolveEscalation(id)
	if err != nil {
		return nil, err
	}

	result := &ReescalationResult{ID: recordID}

	// The record holds the severity, the reescalation count and the history. A
	// reaped one leaves nothing to bump, and the copies' severity must not be
	// re-derived from a mail body, so report the skip rather than guessing.
	if record == nil {
		result.Skipped = true
		result.SkipReason = "escalation record has been reaped; only its delivered copies remain"
		return result, nil
	}

	// Whichever half was named, the record is the bead this bump writes to.
	issue := record
	fields := ParseEscalationFields(issue.Description)
	result.Title = issue.Title
	result.OldSeverity = fields.Severity

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

	// Priority moves with severity. Bumping only the label left a re-escalated
	// escalation sorting and filtering at the priority it was filed with, so the
	// mechanism meant to make an ignored escalation harder to ignore changed
	// nothing on the surfaces that order by priority (gt-psh).
	priority, hasPriority := escalationPriority(newSeverity)

	// Update the bead with new description and severity label
	recordUpdate := UpdateOptions{
		Description:  &description,
		AddLabels:    []string{"reescalated", "severity:" + newSeverity},
		RemoveLabels: []string{"severity:" + result.OldSeverity},
	}
	if hasPriority {
		recordUpdate.Priority = &priority
	}
	if err := b.forIssueID(issue.ID).Update(issue.ID, recordUpdate); err != nil {
		return nil, fmt.Errorf("updating escalation: %w", err)
	}

	// Propagate to the delivered copies. They are what `gt escalate list` and
	// the Mayor's queue render, so a bump that wrote only the record showed the
	// original severity everywhere anyone actually looks — the same record/copy
	// asymmetry gt-4xl fixed for close. Labels and priority only: a copy's
	// description is the mail body, not the structured escalation record.
	copies, err := b.openEscalationCopies(recordID)
	if err != nil {
		return result, fmt.Errorf("escalation record %s re-escalated to %s, but its delivered copies could not be listed and still show %s: %w",
			recordID, newSeverity, result.OldSeverity, err)
	}

	var failures []string
	for _, copied := range copies {
		if copied.ID == issue.ID {
			continue
		}
		copyUpdate := UpdateOptions{
			AddLabels:    []string{"reescalated", "severity:" + newSeverity},
			RemoveLabels: []string{"severity:" + result.OldSeverity},
		}
		if hasPriority {
			copyUpdate.Priority = &priority
		}
		if err := b.forIssueID(copied.ID).Update(copied.ID, copyUpdate); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", copied.ID, err))
		}
	}
	if len(failures) > 0 {
		return result, fmt.Errorf("escalation record %s re-escalated to %s, but %s and still show %s: %s",
			recordID, newSeverity, pluralCopies(len(failures), "could not be updated"), result.OldSeverity, strings.Join(failures, "; "))
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
