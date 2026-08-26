package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

func TestGetNextSeverity(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"low", "medium"},
		{"medium", "high"},
		{"high", "critical"},
		{"critical", "critical"}, // already at max
		{"unknown", "critical"},  // default fallthrough
		{"", "critical"},         // empty defaults to critical
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := getNextSeverity(tt.input)
			if got != tt.want {
				t.Errorf("getNextSeverity(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractMailTargetsFromActions(t *testing.T) {
	tests := []struct {
		name    string
		actions []string
		want    []string
	}{
		{
			name:    "empty actions",
			actions: []string{},
			want:    nil,
		},
		{
			name:    "nil actions",
			actions: nil,
			want:    nil,
		},
		{
			name:    "no mail actions",
			actions: []string{"bead", "log", "email:human"},
			want:    nil,
		},
		{
			name:    "single mail target",
			actions: []string{"bead", "mail:mayor"},
			want:    []string{"mayor"},
		},
		{
			name:    "multiple mail targets",
			actions: []string{"bead", "mail:mayor", "mail:gastown/witness", "email:human"},
			want:    []string{"mayor", "gastown/witness"},
		},
		{
			name:    "mail prefix with empty target ignored",
			actions: []string{"mail:"},
			want:    nil,
		},
		{
			name:    "mixed actions",
			actions: []string{"bead", "mail:mayor", "sms:human", "slack", "mail:deacon", "log"},
			want:    []string{"mayor", "deacon"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMailTargetsFromActions(tt.actions)
			if len(got) != len(tt.want) {
				t.Fatalf("extractMailTargetsFromActions(%v) returned %d targets, want %d: got %v",
					tt.actions, len(got), len(tt.want), got)
			}
			for i, target := range got {
				if target != tt.want[i] {
					t.Errorf("target[%d] = %q, want %q", i, target, tt.want[i])
				}
			}
		})
	}
}

func TestExecuteExternalActionsReportsWarningsAndFailures(t *testing.T) {
	townRoot := t.TempDir()
	statuses := executeExternalActions([]string{"email:human", "log"}, &config.EscalationConfig{}, "hq-esc1", "high", "desc", townRoot)
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
	if statuses[0].Channel != "email" || statuses[0].Warning == "" {
		t.Fatalf("expected email warning status, got %#v", statuses[0])
	}
	if statuses[1].Channel != "log" || !statuses[1].RuntimeNotified {
		t.Fatalf("expected successful log delivery status, got %#v", statuses[1])
	}
}

func TestDeliveryStatusJSONContainsPartialFailure(t *testing.T) {
	statuses := []deliveryStatus{{Channel: "bead", Created: true}, {Channel: "mail", Target: "mayor", Error: "notify failed"}}
	hasFailure := false
	for _, status := range statuses {
		if status.Error != "" {
			hasFailure = true
			break
		}
	}
	result := map[string]interface{}{
		"id":       "hq-esc1",
		"severity": "critical",
		"actions":  []string{"bead", "mail:mayor"},
		"targets":  []string{"mayor"},
		"delivery": statuses,
		"status":   map[bool]string{true: "partial_failure", false: "ok"}[hasFailure],
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(data)
	for _, want := range []string{"\"status\":\"partial_failure\"", "\"delivery\"", "\"channel\":\"mail\"", "\"error\":\"notify failed\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("json output missing %q: %s", want, text)
		}
	}
}

func TestDeliveryStatusJSONContainsSuccessfulMailPathDetails(t *testing.T) {
	statuses := []deliveryStatus{{Channel: "bead", Created: true, Severity: "critical"}, {Channel: "mail", Target: "mayor", Persisted: true, RuntimeNotified: true, Annotated: true, Severity: "critical", NotificationRoute: "mail+nudge"}}
	result := map[string]interface{}{
		"id":       "hq-esc2",
		"severity": "critical",
		"actions":  []string{"bead", "mail:mayor"},
		"targets":  []string{"mayor"},
		"delivery": statuses,
		"status":   "ok",
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(data)
	for _, want := range []string{"\"status\":\"ok\"", "\"runtime_notified\":true", "\"annotated\":true", "\"notification_route\":\"mail+nudge\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("json output missing %q: %s", want, text)
		}
	}
}

func TestEscalationFingerprintLabel(t *testing.T) {
	got := escalationFingerprintLabel(" deacon:await-signal:hq-deacon ")
	trimmed := escalationFingerprintLabel("deacon:await-signal:hq-deacon")
	if got != trimmed {
		t.Fatalf("fingerprint should trim whitespace: %q != %q", got, trimmed)
	}
	if !strings.HasPrefix(got, "escalation-fp:") {
		t.Fatalf("fingerprint label %q missing prefix", got)
	}
	if len(got) != len("escalation-fp:")+12 {
		t.Fatalf("fingerprint label %q has length %d, want %d", got, len(got), len("escalation-fp:")+12)
	}
	if got == escalationFingerprintLabel("deacon:convoy-check:hq-cv-123") {
		t.Fatal("different raw fingerprints should produce different labels")
	}
	if escalationFingerprintLabel(" ") != "" {
		t.Fatal("blank fingerprint should produce empty label")
	}
}

func TestSeverityEmoji(t *testing.T) {
	tests := []struct {
		severity string
		want     string
	}{
		{config.SeverityCritical, "🚨"},
		{config.SeverityHigh, "⚠️"},
		{config.SeverityMedium, "📢"},
		{config.SeverityLow, "ℹ️"},
		{"unknown", "📋"},
		{"", "📋"},
	}

	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			got := severityEmoji(tt.severity)
			if got != tt.want {
				t.Errorf("severityEmoji(%q) = %q, want %q", tt.severity, got, tt.want)
			}
		})
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		timestamp string
		want      string
	}{
		{
			name:      "just now",
			timestamp: now.Add(-10 * time.Second).Format(time.RFC3339),
			want:      "just now",
		},
		{
			name:      "1 minute ago",
			timestamp: now.Add(-1 * time.Minute).Format(time.RFC3339),
			want:      "1 minute ago",
		},
		{
			name:      "multiple minutes ago",
			timestamp: now.Add(-15 * time.Minute).Format(time.RFC3339),
			want:      "15 minutes ago",
		},
		{
			name:      "1 hour ago",
			timestamp: now.Add(-1 * time.Hour).Format(time.RFC3339),
			want:      "1 hour ago",
		},
		{
			name:      "multiple hours ago",
			timestamp: now.Add(-5 * time.Hour).Format(time.RFC3339),
			want:      "5 hours ago",
		},
		{
			name:      "1 day ago",
			timestamp: now.Add(-25 * time.Hour).Format(time.RFC3339),
			want:      "1 day ago",
		},
		{
			name:      "multiple days ago",
			timestamp: now.Add(-72 * time.Hour).Format(time.RFC3339),
			want:      "3 days ago",
		},
		{
			name:      "invalid timestamp returns raw",
			timestamp: "not-a-timestamp",
			want:      "not-a-timestamp",
		},
		{
			name:      "empty timestamp returns raw",
			timestamp: "",
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRelativeTime(tt.timestamp)
			if got != tt.want {
				t.Errorf("formatRelativeTime(%q) = %q, want %q", tt.timestamp, got, tt.want)
			}
		})
	}
}

func TestFormatEscalationMailBody(t *testing.T) {
	tests := []struct {
		name     string
		beadID   string
		severity string
		reason   string
		from     string
		related  string
		wantIn   []string
		notIn    []string
	}{
		{
			name:     "basic escalation",
			beadID:   "hq-abc123",
			severity: "high",
			reason:   "Build failing",
			from:     "gastown/witness",
			related:  "",
			wantIn: []string{
				"Escalation ID: hq-abc123",
				"Severity: high",
				"From: gastown/witness",
				"Reason:",
				"Build failing",
				"gt escalate ack hq-abc123",
				"gt escalate close hq-abc123",
			},
			notIn: []string{"Related:"},
		},
		{
			name:     "with related bead",
			beadID:   "hq-xyz789",
			severity: "critical",
			reason:   "Agent stuck",
			from:     "gastown/deacon",
			related:  "gt-stuck42",
			wantIn: []string{
				"Escalation ID: hq-xyz789",
				"Severity: critical",
				"Related: gt-stuck42",
			},
		},
		{
			name:     "no reason",
			beadID:   "hq-nnn",
			severity: "low",
			reason:   "",
			from:     "system",
			related:  "",
			wantIn: []string{
				"Escalation ID: hq-nnn",
				"Severity: low",
				"From: system",
			},
			notIn: []string{"Reason:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatEscalationMailBody(tt.beadID, tt.severity, tt.reason, tt.from, tt.related)
			for _, s := range tt.wantIn {
				if !strings.Contains(got, s) {
					t.Errorf("missing %q in output:\n%s", s, got)
				}
			}
			for _, s := range tt.notIn {
				if strings.Contains(got, s) {
					t.Errorf("unexpected %q in output:\n%s", s, got)
				}
			}
		})
	}
}

func TestFormatReescalationMailBody(t *testing.T) {
	result := &beads.ReescalationResult{
		ID:              "hq-esc123",
		Title:           "Build blocked",
		OldSeverity:     "medium",
		NewSeverity:     "high",
		ReescalationNum: 2,
	}

	got := formatReescalationMailBody(result, "gastown/patrol")

	wantIn := []string{
		"Escalation ID: hq-esc123",
		"Severity bumped: medium → high",
		"Reescalation #2",
		"Reescalated by: gastown/patrol",
		"stale threshold",
		"gt escalate ack hq-esc123",
		"gt escalate close hq-esc123",
	}

	for _, s := range wantIn {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in output:\n%s", s, got)
		}
	}
}

func TestDetectSenderFallback(t *testing.T) {
	// Save original env vars
	origActor := os.Getenv("BD_ACTOR")
	origRole := os.Getenv("GT_ROLE")
	defer func() {
		os.Setenv("BD_ACTOR", origActor)
		os.Setenv("GT_ROLE", origRole)
	}()

	tests := []struct {
		name  string
		actor string
		role  string
		want  string
	}{
		{
			name:  "BD_ACTOR takes priority",
			actor: "gastown/polecats/alpha",
			role:  "gastown/witness",
			want:  "gastown/polecats/alpha",
		},
		{
			name:  "GT_ROLE used when BD_ACTOR empty",
			actor: "",
			role:  "gastown/witness",
			want:  "gastown/witness",
		},
		{
			name:  "empty when both unset",
			actor: "",
			role:  "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("BD_ACTOR", tt.actor)
			os.Setenv("GT_ROLE", tt.role)

			got := detectSenderFallback()
			if got != tt.want {
				t.Errorf("detectSenderFallback() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecuteExternalActions(t *testing.T) {
	// executeExternalActions prints warnings/info but doesn't return errors.
	// We test that it doesn't panic with various configurations.

	tests := []struct {
		name    string
		actions []string
		cfg     *config.EscalationConfig
	}{
		{
			name:    "no external actions",
			actions: []string{"bead", "mail:mayor"},
			cfg:     &config.EscalationConfig{},
		},
		{
			name:    "email action without contact",
			actions: []string{"email:human"},
			cfg:     &config.EscalationConfig{},
		},
		{
			name:    "email action with contact",
			actions: []string{"email:human"},
			cfg: &config.EscalationConfig{
				Contacts: config.EscalationContacts{
					HumanEmail: "test@example.com",
				},
			},
		},
		{
			name:    "sms action without contact",
			actions: []string{"sms:human"},
			cfg:     &config.EscalationConfig{},
		},
		{
			name:    "sms action with contact",
			actions: []string{"sms:human"},
			cfg: &config.EscalationConfig{
				Contacts: config.EscalationContacts{
					HumanSMS: "+15551234567",
				},
			},
		},
		{
			name:    "slack action without webhook",
			actions: []string{"slack"},
			cfg:     &config.EscalationConfig{},
		},
		{
			name:    "slack action with webhook",
			actions: []string{"slack"},
			cfg: &config.EscalationConfig{
				Contacts: config.EscalationContacts{
					SlackWebhook: "https://hooks.slack.com/test",
				},
			},
		},
		{
			name:    "log action",
			actions: []string{"log"},
			cfg:     &config.EscalationConfig{},
		},
		{
			name:    "all external actions combined",
			actions: []string{"email:human", "sms:human", "slack", "log"},
			cfg: &config.EscalationConfig{
				Contacts: config.EscalationContacts{
					HumanEmail:   "test@example.com",
					HumanSMS:     "+15551234567",
					SlackWebhook: "https://hooks.slack.com/test",
				},
			},
		},
		{
			name:    "empty actions",
			actions: []string{},
			cfg:     &config.EscalationConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			// Should not panic
			executeExternalActions(tt.actions, tt.cfg, "hq-test", "high", "Test escalation", tmpDir)
		})
	}
}

func TestWriteEscalationLog(t *testing.T) {
	tmpDir := t.TempDir()
	err := writeEscalationLog(tmpDir, "hq-abc", "critical", "Test failure")
	if err != nil {
		t.Fatalf("writeEscalationLog returned error: %v", err)
	}

	logPath := tmpDir + "/logs/escalations.log"
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "[CRITICAL]") {
		t.Errorf("log entry missing severity, got: %s", content)
	}
	if !strings.Contains(content, "hq-abc") {
		t.Errorf("log entry missing bead ID, got: %s", content)
	}
	if !strings.Contains(content, "Test failure") {
		t.Errorf("log entry missing description, got: %s", content)
	}
}

func TestRunEscalateValidation(t *testing.T) {
	// Save and restore package-level flags
	origSeverity := escalateSeverity
	origReason := escalateReason
	origStdin := escalateStdin
	origDryRun := escalateDryRun
	defer func() {
		escalateSeverity = origSeverity
		escalateReason = origReason
		escalateStdin = origStdin
		escalateDryRun = origDryRun
	}()

	t.Run("stdin and reason conflict", func(t *testing.T) {
		escalateStdin = true
		escalateReason = "some reason"
		escalateSeverity = "medium"

		err := runEscalate(escalateCmd, []string{"test"})
		if err == nil {
			t.Fatal("expected error when --stdin and --reason are both set")
		}
		if !strings.Contains(err.Error(), "cannot use --stdin with --reason/-r") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("no args shows help", func(t *testing.T) {
		escalateStdin = false
		escalateReason = ""
		escalateSeverity = "medium"

		// No args should return nil (shows help)
		err := runEscalate(escalateCmd, []string{})
		if err != nil {
			t.Errorf("expected nil error for no args (help case), got: %v", err)
		}
	})

	t.Run("invalid severity", func(t *testing.T) {
		escalateStdin = false
		escalateReason = ""
		escalateSeverity = "emergency"

		err := runEscalate(escalateCmd, []string{"test escalation"})
		if err == nil {
			t.Fatal("expected error for invalid severity")
		}
		if !strings.Contains(err.Error(), "invalid severity") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestFormatEscalationMailBodyNeutralSubjectStillCarriesStructuredBody(t *testing.T) {
	body := formatEscalationMailBody("hq-abc123", "high", "Database drift", "deacon/", "gt-xyz")
	for _, want := range []string{
		"Escalation ID: hq-abc123",
		"Severity: high",
		"From: deacon/",
		"Related: gt-xyz",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %s", want, body)
		}
	}
}

// --- The "bead" routing action (gt-3i4e) -------------------------------------
//
// "bead" was configured on all four routes, reported as created on every
// escalation, and dispatched nowhere. The durable artifact it names existed only
// as a side effect of the "mail:" actions, so the default "low" route (["bead"])
// delivered nothing at all and still exited 0 with created:true.

// fakeBeadCreator records what deliverEscalationBead asked bd to create.
type fakeBeadCreator struct {
	calls int
	title string
	body  string
	rec   string
	sev   string
	err   error
}

func (f *fakeBeadCreator) CreateEscalationDeliveryBead(title, body, recordID, severity string) (*beads.Issue, error) {
	f.calls++
	f.title, f.body, f.rec, f.sev = title, body, recordID, severity
	if f.err != nil {
		return nil, f.err
	}
	return &beads.Issue{ID: "hq-copy1"}, nil
}

func TestDeliverEscalationBeadCreatesDurableBeadForBeadOnlyRoute(t *testing.T) {
	creator := &fakeBeadCreator{}
	// The default "low" route: the only one with no mail: action.
	status := deliverEscalationBead(creator, []string{"bead"}, nil, "hq-rec1", "low", "[LOW] disk filling", "body")

	if status == nil {
		t.Fatal("bead action present but no delivery status returned")
	}
	if creator.calls != 1 {
		t.Fatalf("expected exactly 1 bead created, got %d", creator.calls)
	}
	if !status.Created || status.BeadID != "hq-copy1" || status.Error != "" {
		t.Fatalf("expected a created durable bead, got %#v", status)
	}
	if creator.rec != "hq-rec1" || creator.sev != "low" {
		t.Fatalf("delivery bead not linked to the record: %#v", creator)
	}
	if !escalationWasDelivered([]deliveryStatus{*status}) {
		t.Fatal("a created durable bead must count as a delivery")
	}
}

func TestDeliverEscalationBeadSkippedWhenRouteOmitsIt(t *testing.T) {
	creator := &fakeBeadCreator{}
	if status := deliverEscalationBead(creator, []string{"mail:mayor"}, nil, "hq-rec1", "medium", "t", "b"); status != nil {
		t.Fatalf("route has no bead action, expected no bead status, got %#v", status)
	}
	if creator.calls != 0 {
		t.Fatalf("expected no bead created, got %d", creator.calls)
	}
}

// A route carrying both "bead" and "mail:" must not produce two beads — the
// annotated mail copy IS the durable artifact, and duplicating it would double
// every critical/high/medium escalation in `gt escalate list`.
func TestDeliverEscalationBeadReusesAnnotatedMailCopy(t *testing.T) {
	creator := &fakeBeadCreator{}
	mailStatuses := []deliveryStatus{
		{Channel: "mail", Target: "mayor", Persisted: true, Annotated: true, BeadID: "hq-mail1"},
	}
	status := deliverEscalationBead(creator, []string{"bead", "mail:mayor"}, mailStatuses, "hq-rec1", "critical", "t", "b")

	if creator.calls != 0 {
		t.Fatalf("expected the mail copy to satisfy the bead action, but %d bead(s) were created", creator.calls)
	}
	if status == nil || !status.Created || status.BeadID != "hq-mail1" {
		t.Fatalf("expected the bead status to point at the mail copy, got %#v", status)
	}
	if status.Detail == "" {
		t.Error("a reused copy must say so, or the JSON claims a bead this run did not write")
	}
}

// An un-annotated mail copy is missing the "escalation:<record-id>" label, so
// nothing can find it from the record. It cannot stand in for the bead action.
func TestDeliverEscalationBeadDoesNotReuseUnannotatedMailCopy(t *testing.T) {
	creator := &fakeBeadCreator{}
	mailStatuses := []deliveryStatus{
		{Channel: "mail", Target: "mayor", Persisted: true, BeadID: "hq-mail1", Warning: "annotation update failed"},
	}
	status := deliverEscalationBead(creator, []string{"bead", "mail:mayor"}, mailStatuses, "hq-rec1", "high", "t", "b")

	if creator.calls != 1 {
		t.Fatalf("expected a linked bead to be created, got %d", creator.calls)
	}
	if status == nil || status.BeadID != "hq-copy1" {
		t.Fatalf("expected the newly created bead, got %#v", status)
	}
}

// The status must come from the creation result. Seeding it as successful is the
// original defect: a hardcoded Created:true can never report failure.
func TestDeliverEscalationBeadReportsCreationFailure(t *testing.T) {
	creator := &fakeBeadCreator{err: os.ErrPermission}
	status := deliverEscalationBead(creator, []string{"bead"}, nil, "hq-rec1", "low", "t", "b")

	if status == nil {
		t.Fatal("expected a failing bead status, got nil")
	}
	if status.Created {
		t.Fatal("bead creation failed but the status still claims created:true")
	}
	if status.Error == "" {
		t.Fatal("failed bead creation must record the error")
	}
	if escalationWasDelivered([]deliveryStatus{*status}) {
		t.Fatal("a failed bead creation must not count as a delivery")
	}
}

// The durable bead is what remains after the record wisp is GC'd, so it has to
// carry the record's structured fields — not just the mail prose.
func TestFormatEscalationDeliveryBodySurvivesTheRecord(t *testing.T) {
	fields := &beads.EscalationFields{
		Severity:    "low",
		Reason:      "orphan branch disposition needs a decision",
		Source:      "patrol:refinery",
		EscalatedBy: "duly_noted/refinery",
		EscalatedAt: "2026-08-03T12:00:00Z",
		RelatedBead: "dn-qpk",
	}
	body := formatEscalationDeliveryBody("hq-rec1", "orphan branch dn-qpk", fields)

	// The trailing ack/close block must not corrupt the structured block.
	parsed := beads.ParseEscalationFields(body)
	if parsed.Severity != "low" || parsed.EscalatedBy != "duly_noted/refinery" || parsed.Source != "patrol:refinery" {
		t.Fatalf("structured fields did not round-trip through the delivery body: %#v", parsed)
	}
	if parsed.Reason != fields.Reason || parsed.RelatedBead != "dn-qpk" {
		t.Fatalf("reason/related lost in the delivery body: %#v", parsed)
	}
	if !strings.Contains(body, "gt escalate close hq-rec1") {
		t.Errorf("delivery body should name the record ID ack/close take, got:\n%s", body)
	}
}

func TestEscalationWasDelivered(t *testing.T) {
	tests := []struct {
		name     string
		statuses []deliveryStatus
		want     bool
	}{
		{"no route at all", nil, false},
		{"bead created", []deliveryStatus{{Channel: "bead", Created: true}}, true},
		{"mail persisted", []deliveryStatus{{Channel: "mail", Target: "mayor", Persisted: true}}, true},
		{"log written", []deliveryStatus{{Channel: "log", RuntimeNotified: true}}, true},
		{"every channel failed", []deliveryStatus{
			{Channel: "bead", Error: "dolt down"},
			{Channel: "mail", Target: "mayor", Error: "send failed"},
		}, false},
		// A warning is a skipped channel, not a delivery: an email: action with
		// no contact configured tells nobody anything.
		{"only skipped channels", []deliveryStatus{{Channel: "email", Warning: "contacts.human_email not configured"}}, false},
		{"one success among failures", []deliveryStatus{
			{Channel: "bead", Created: true},
			{Channel: "sms", Error: "webhook 500"},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escalationWasDelivered(tt.statuses); got != tt.want {
				t.Fatalf("escalationWasDelivered(%#v) = %v, want %v", tt.statuses, got, tt.want)
			}
		})
	}
}

func TestEscalationDeliveryStatusReportsUndelivered(t *testing.T) {
	tests := []struct {
		delivered  bool
		hasFailure bool
		want       string
	}{
		{true, false, "ok"},
		{true, true, "partial_failure"},
		{false, false, "undelivered"}, // the case that used to report "ok"
		{false, true, "undelivered"},
	}
	for _, tt := range tests {
		if got := escalationDeliveryStatus(tt.delivered, tt.hasFailure); got != tt.want {
			t.Errorf("escalationDeliveryStatus(%v, %v) = %q, want %q", tt.delivered, tt.hasFailure, got, tt.want)
		}
	}
}

// The whole point of the exit-code gate: the shipped default routes must all
// deliver something. "low" is ["bead"] and used to deliver nothing.
func TestDefaultRoutesAllDeliver(t *testing.T) {
	cfg := config.NewEscalationConfig()
	for _, severity := range config.ValidSeverities() {
		actions := cfg.GetRouteForSeverity(severity)
		creator := &fakeBeadCreator{}
		var statuses []deliveryStatus
		for _, target := range extractMailTargetsFromActions(actions) {
			statuses = append(statuses, deliveryStatus{Channel: "mail", Target: target, Persisted: true, Annotated: true, BeadID: "hq-mail-" + target})
		}
		if status := deliverEscalationBead(creator, actions, statuses, "hq-rec1", severity, "t", "b"); status != nil {
			statuses = append([]deliveryStatus{*status}, statuses...)
		}
		if !escalationWasDelivered(statuses) {
			t.Errorf("severity %q route %v delivers nothing — it would exit non-zero", severity, actions)
		}
	}
}

// gt escalate --help pointed at ~/gt/settings/escalation.json, which is not a
// town root on any host: it cost duly_noted/witness a patrol cycle looking for a
// config file that does not exist.
// escalateCloseCmdDeclaredSilenceUsage captures the declared value before any
// test can mutate the shared command: package-level vars initialize after
// escalateCloseCmd itself and before any test body, so this is the value in the
// source, not one an earlier test left behind.
var escalateCloseCmdDeclaredSilenceUsage = escalateCloseCmd.SilenceUsage

// TestEscalateCloseUsageOnlyForMalformedInvocations covers gt-u3mo, the same
// defect gt-5h2 fixed for `gt nudge`. Cobra honours SilenceUsage only from the
// executed command and the ROOT, so the flag declared on `escalate` never
// reached `escalate close`: a close that failed printed its error and then
// dumped ~8 lines of usage over it. That is how three failed closes read as
// quiet successes — the operator's `| tail -1` showed the last line of the
// usage block rather than the error.
//
// The malformed case is the control: suppressing usage everywhere would be just
// as wrong, and it fails independently if the fix is moved onto the command
// literal.
func TestEscalateCloseUsageOnlyForMalformedInvocations(t *testing.T) {
	if escalateCloseCmdDeclaredSilenceUsage {
		t.Fatal("escalateCloseCmd declares SilenceUsage: true, which also suppresses usage for flag-parse and arg-count errors; set cmd.SilenceUsage inside runEscalateClose instead (gt-u3mo)")
	}

	newCloseTestCmd := func(out *bytes.Buffer) *cobra.Command {
		c := &cobra.Command{
			Use:          escalateCloseCmd.Use,
			Args:         escalateCloseCmd.Args,
			RunE:         runEscalateClose,
			SilenceUsage: escalateCloseCmdDeclaredSilenceUsage,
		}
		c.SetOut(out)
		c.SetErr(out)
		return c
	}

	t.Run("runtime error prints no usage", func(t *testing.T) {
		// Both env vars as well as the cwd: GT_TOWN_ROOT and GT_ROOT outrank the
		// working directory in workspace resolution, so chdir alone would still
		// resolve the live town and go on to touch its beads.
		outside := t.TempDir()
		t.Setenv("GT_TOWN_ROOT", outside)
		t.Setenv("GT_ROOT", outside)
		t.Chdir(outside)

		var out bytes.Buffer
		c := newCloseTestCmd(&out)
		c.SetArgs([]string{"hq-wisp-nosuch"})

		if err := c.Execute(); err == nil {
			t.Fatal("Execute() outside a workspace returned nil, want an error")
		}
		got := out.String()
		if !strings.Contains(got, "not in a Gas Town workspace") {
			t.Errorf("output does not carry the diagnosis; the error itself must keep printing:\n%s", got)
		}
		if strings.Contains(got, "Usage:") {
			t.Errorf("runtime failure printed cobra's usage block over its error:\n%s", got)
		}
	})

	t.Run("malformed invocation still prints usage", func(t *testing.T) {
		var out bytes.Buffer
		c := newCloseTestCmd(&out)
		c.SetArgs([]string{}) // violates Args: ExactArgs(1), so RunE never runs

		if err := c.Execute(); err == nil {
			t.Fatal("Execute() with no arguments returned nil, want an arg-count error")
		}
		if got := out.String(); !strings.Contains(got, "Usage:") {
			t.Errorf("malformed invocation lost its usage block:\n%s", got)
		}
	})
}

func TestEscalateHelpDoesNotPointAtTildeGt(t *testing.T) {
	if strings.Contains(escalateCmd.Long, "~/gt/settings") {
		t.Error("escalate help documents ~/gt/settings, which is not where the config lives")
	}
	if !strings.Contains(escalateCmd.Long, "$GT_ROOT/settings/escalation.json") {
		t.Error("escalate help should name $GT_ROOT/settings/escalation.json")
	}
}

func TestGetNextSeverityMatchesConfig(t *testing.T) {
	// Verify getNextSeverity in escalate_impl.go matches config.NextSeverity
	// to catch if they ever diverge.
	severities := []string{"low", "medium", "high", "critical"}
	for _, s := range severities {
		cmdResult := getNextSeverity(s)
		configResult := config.NextSeverity(s)
		if cmdResult != configResult {
			t.Errorf("getNextSeverity(%q) = %q but config.NextSeverity(%q) = %q — they diverge!",
				s, cmdResult, s, configResult)
		}
	}
}

// --- The hidden half of the escalation queue (gt-f0b3) -----------------------
//
// `gt escalate list` hides a delivered copy whose escalation record is closed.
// That is right (gt-4xl) and it was silent, which is not: measured on hq
// 2026-08-23 the list printed 3 while `bd list --label=gt:escalation
// --status=open` returned 4, and nothing anywhere accounted for the fourth.

func TestPrintStrandedEscalations_NamesTheHiddenBeadsAndTheReconcile(t *testing.T) {
	stranded := []*beads.Issue{{
		ID:     "hq-budjm",
		Status: "open",
		Title:  "[HIGH] Scheduler dispatch dead town-wide 1h19m",
		Labels: []string{"gt:escalation", "escalation:hq-wisp-aor1wa"},
	}}

	var buf strings.Builder
	printStrandedEscalations(&buf, stranded)
	got := buf.String()

	for _, want := range []string{
		"hq-budjm",                   // the bead the count disagreement is made of
		"hq-wisp-aor1wa",             // the record whose closure hid it
		"gt escalate close hq-budjm", // the command that reconciles the halves
		"1 lower",                    // the size of the gap, stated
		"not proof",                  // a closed record is not evidence of handling
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report must contain %q, got:\n%s", want, got)
		}
	}
}

// Nothing to report must print nothing at all: an unconditional footer would
// train readers to skip the one case that matters.
func TestPrintStrandedEscalations_SilentWhenNothingHidden(t *testing.T) {
	var buf strings.Builder
	printStrandedEscalations(&buf, nil)
	if buf.String() != "" {
		t.Errorf("no hidden beads must print nothing, got: %q", buf.String())
	}
}
