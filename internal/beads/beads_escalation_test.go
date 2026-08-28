package beads

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestFormatEscalationDescription(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		fields *EscalationFields
		want   []string
		notIn  []string
	}{
		{
			name:   "nil fields returns title only",
			title:  "Test Escalation",
			fields: nil,
			want:   []string{"Test Escalation"},
			notIn:  []string{"severity:"},
		},
		{
			name:  "basic escalation",
			title: "Build failure",
			fields: &EscalationFields{
				Severity:    "high",
				Reason:      "Build failed 3 times",
				Source:      "patrol:deacon",
				EscalatedBy: "gastown/deacon",
				EscalatedAt: "2024-01-15T10:00:00Z",
			},
			want: []string{
				"Build failure",
				"severity: high",
				"reason: Build failed 3 times",
				"source: patrol:deacon",
				"escalated_by: gastown/deacon",
				"escalated_at: 2024-01-15T10:00:00Z",
			},
		},
		{
			name:  "acknowledged escalation",
			title: "Agent stuck",
			fields: &EscalationFields{
				Severity:    "medium",
				Reason:      "Agent not responding",
				EscalatedBy: "gastown/witness",
				EscalatedAt: "2024-01-15T10:00:00Z",
				AckedBy:     "gastown/crew/joe",
				AckedAt:     "2024-01-15T10:05:00Z",
			},
			want: []string{
				"severity: medium",
				"acked_by: gastown/crew/joe",
				"acked_at: 2024-01-15T10:05:00Z",
			},
		},
		{
			name:  "closed escalation",
			title: "Disk full",
			fields: &EscalationFields{
				Severity:     "critical",
				Reason:       "Disk >95%",
				EscalatedBy:  "gastown/deacon",
				EscalatedAt:  "2024-01-15T10:00:00Z",
				ClosedBy:     "human",
				ClosedReason: "Cleaned up temp files",
			},
			want: []string{
				"closed_by: human",
				"closed_reason: Cleaned up temp files",
			},
		},
		{
			name:  "null fields formatted explicitly",
			title: "New escalation",
			fields: &EscalationFields{
				Severity:    "low",
				Reason:      "Minor issue",
				EscalatedBy: "test",
				EscalatedAt: "2024-01-01T00:00:00Z",
			},
			want: []string{
				"acked_by: null",
				"acked_at: null",
				"closed_by: null",
				"closed_reason: null",
				"related_bead: null",
				"original_severity: null",
			},
		},
		{
			name:  "reescalation fields",
			title: "Bumped escalation",
			fields: &EscalationFields{
				Severity:          "high",
				Reason:            "Stale for 2h",
				EscalatedBy:       "patrol",
				EscalatedAt:       "2024-01-15T08:00:00Z",
				OriginalSeverity:  "low",
				ReescalationCount: 2,
				LastReescalatedAt: "2024-01-15T10:00:00Z",
				LastReescalatedBy: "deacon",
			},
			want: []string{
				"original_severity: low",
				"reescalation_count: 2",
				"last_reescalated_at: 2024-01-15T10:00:00Z",
				"last_reescalated_by: deacon",
			},
		},
		{
			name:  "fingerprint field",
			title: "Repeated alert",
			fields: &EscalationFields{
				Severity:    "medium",
				Reason:      "control-plane timeout",
				EscalatedBy: "deacon",
				EscalatedAt: "2024-01-15T10:00:00Z",
				Fingerprint: "escalation-fp:abc123def456",
			},
			want: []string{
				"fingerprint: escalation-fp:abc123def456",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatEscalationDescription(tt.title, tt.fields)
			for _, line := range tt.want {
				if !strings.Contains(got, line) {
					t.Errorf("missing line %q in output:\n%s", line, got)
				}
			}
			for _, line := range tt.notIn {
				if strings.Contains(got, line) {
					t.Errorf("unexpected %q in output:\n%s", line, got)
				}
			}
		})
	}
}

func TestParseEscalationFields(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want *EscalationFields
	}{
		{
			name: "empty description",
			desc: "",
			want: &EscalationFields{},
		},
		{
			name: "full escalation",
			desc: `Escalation: Build failure

severity: high
reason: Build failed 3 times
source: patrol:deacon
escalated_by: gastown/deacon
escalated_at: 2024-01-15T10:00:00Z
acked_by: gastown/crew/joe
acked_at: 2024-01-15T10:05:00Z
closed_by: null
closed_reason: null
related_bead: gt-abc123
original_severity: medium
reescalation_count: 1
last_reescalated_at: 2024-01-15T09:30:00Z
last_reescalated_by: deacon
fingerprint: escalation-fp:abc123def456`,
			want: &EscalationFields{
				Severity:          "high",
				Reason:            "Build failed 3 times",
				Source:            "patrol:deacon",
				EscalatedBy:       "gastown/deacon",
				EscalatedAt:       "2024-01-15T10:00:00Z",
				AckedBy:           "gastown/crew/joe",
				AckedAt:           "2024-01-15T10:05:00Z",
				ClosedBy:          "",
				ClosedReason:      "",
				RelatedBead:       "gt-abc123",
				OriginalSeverity:  "medium",
				ReescalationCount: 1,
				LastReescalatedAt: "2024-01-15T09:30:00Z",
				LastReescalatedBy: "deacon",
				Fingerprint:       "escalation-fp:abc123def456",
			},
		},
		{
			name: "null values become empty strings",
			desc: "severity: critical\nsource: null\nacked_by: null",
			want: &EscalationFields{
				Severity: "critical",
				Source:   "",
				AckedBy:  "",
			},
		},
		{
			name: "invalid reescalation_count ignored",
			desc: "severity: low\nreescalation_count: not-a-number",
			want: &EscalationFields{
				Severity:          "low",
				ReescalationCount: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseEscalationFields(tt.desc)
			if got.Severity != tt.want.Severity {
				t.Errorf("Severity = %q, want %q", got.Severity, tt.want.Severity)
			}
			if got.Reason != tt.want.Reason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.want.Reason)
			}
			if got.Source != tt.want.Source {
				t.Errorf("Source = %q, want %q", got.Source, tt.want.Source)
			}
			if got.EscalatedBy != tt.want.EscalatedBy {
				t.Errorf("EscalatedBy = %q, want %q", got.EscalatedBy, tt.want.EscalatedBy)
			}
			if got.EscalatedAt != tt.want.EscalatedAt {
				t.Errorf("EscalatedAt = %q, want %q", got.EscalatedAt, tt.want.EscalatedAt)
			}
			if got.AckedBy != tt.want.AckedBy {
				t.Errorf("AckedBy = %q, want %q", got.AckedBy, tt.want.AckedBy)
			}
			if got.AckedAt != tt.want.AckedAt {
				t.Errorf("AckedAt = %q, want %q", got.AckedAt, tt.want.AckedAt)
			}
			if got.ClosedBy != tt.want.ClosedBy {
				t.Errorf("ClosedBy = %q, want %q", got.ClosedBy, tt.want.ClosedBy)
			}
			if got.ClosedReason != tt.want.ClosedReason {
				t.Errorf("ClosedReason = %q, want %q", got.ClosedReason, tt.want.ClosedReason)
			}
			if got.RelatedBead != tt.want.RelatedBead {
				t.Errorf("RelatedBead = %q, want %q", got.RelatedBead, tt.want.RelatedBead)
			}
			if got.OriginalSeverity != tt.want.OriginalSeverity {
				t.Errorf("OriginalSeverity = %q, want %q", got.OriginalSeverity, tt.want.OriginalSeverity)
			}
			if got.ReescalationCount != tt.want.ReescalationCount {
				t.Errorf("ReescalationCount = %d, want %d", got.ReescalationCount, tt.want.ReescalationCount)
			}
			if got.LastReescalatedAt != tt.want.LastReescalatedAt {
				t.Errorf("LastReescalatedAt = %q, want %q", got.LastReescalatedAt, tt.want.LastReescalatedAt)
			}
			if got.LastReescalatedBy != tt.want.LastReescalatedBy {
				t.Errorf("LastReescalatedBy = %q, want %q", got.LastReescalatedBy, tt.want.LastReescalatedBy)
			}
			if got.Fingerprint != tt.want.Fingerprint {
				t.Errorf("Fingerprint = %q, want %q", got.Fingerprint, tt.want.Fingerprint)
			}
		})
	}
}

func TestEscalationFieldsRoundTrip(t *testing.T) {
	original := &EscalationFields{
		Severity:          "high",
		Reason:            "Agent stuck for 1h",
		Source:            "patrol:witness",
		EscalatedBy:       "gastown/witness",
		EscalatedAt:       "2024-06-15T12:00:00Z",
		AckedBy:           "gastown/crew/joe",
		AckedAt:           "2024-06-15T12:05:00Z",
		RelatedBead:       "gt-stuck123",
		OriginalSeverity:  "medium",
		ReescalationCount: 1,
		LastReescalatedAt: "2024-06-15T11:30:00Z",
		LastReescalatedBy: "deacon",
		Fingerprint:       "escalation-fp:feedface1234",
	}

	formatted := FormatEscalationDescription("Escalation: Agent stuck", original)
	parsed := ParseEscalationFields(formatted)

	if parsed.Severity != original.Severity {
		t.Errorf("Severity: got %q, want %q", parsed.Severity, original.Severity)
	}
	if parsed.Reason != original.Reason {
		t.Errorf("Reason: got %q, want %q", parsed.Reason, original.Reason)
	}
	if parsed.Source != original.Source {
		t.Errorf("Source: got %q, want %q", parsed.Source, original.Source)
	}
	if parsed.EscalatedBy != original.EscalatedBy {
		t.Errorf("EscalatedBy: got %q, want %q", parsed.EscalatedBy, original.EscalatedBy)
	}
	if parsed.EscalatedAt != original.EscalatedAt {
		t.Errorf("EscalatedAt: got %q, want %q", parsed.EscalatedAt, original.EscalatedAt)
	}
	if parsed.AckedBy != original.AckedBy {
		t.Errorf("AckedBy: got %q, want %q", parsed.AckedBy, original.AckedBy)
	}
	if parsed.AckedAt != original.AckedAt {
		t.Errorf("AckedAt: got %q, want %q", parsed.AckedAt, original.AckedAt)
	}
	if parsed.RelatedBead != original.RelatedBead {
		t.Errorf("RelatedBead: got %q, want %q", parsed.RelatedBead, original.RelatedBead)
	}
	if parsed.OriginalSeverity != original.OriginalSeverity {
		t.Errorf("OriginalSeverity: got %q, want %q", parsed.OriginalSeverity, original.OriginalSeverity)
	}
	if parsed.ReescalationCount != original.ReescalationCount {
		t.Errorf("ReescalationCount: got %d, want %d", parsed.ReescalationCount, original.ReescalationCount)
	}
	if parsed.LastReescalatedAt != original.LastReescalatedAt {
		t.Errorf("LastReescalatedAt: got %q, want %q", parsed.LastReescalatedAt, original.LastReescalatedAt)
	}
	if parsed.LastReescalatedBy != original.LastReescalatedBy {
		t.Errorf("LastReescalatedBy: got %q, want %q", parsed.LastReescalatedBy, original.LastReescalatedBy)
	}
	if parsed.Fingerprint != original.Fingerprint {
		t.Errorf("Fingerprint: got %q, want %q", parsed.Fingerprint, original.Fingerprint)
	}
}

func TestFilterEscalationRecordsSkipsMailMessages(t *testing.T) {
	issues := []*Issue{
		{ID: "hq-root", Labels: []string{"gt:escalation"}},
		{ID: "hq-mail", Labels: []string{"gt:message"}},
	}

	got := filterEscalationRecords(issues)
	if len(got) != 1 || got[0].ID != "hq-root" {
		t.Fatalf("filterEscalationRecords() = %#v, want only root escalation", got)
	}
}

// Regression for hq-q0lc. `gt escalate` delivers an escalation AS mail, so the root
// bead carries BOTH gt:escalation and gt:message — 74 of 74 escalation beads in the
// hq store look like this, and none has the bare gt:escalation shape the original
// fixture used. Filtering on gt:message alone therefore dropped every escalation and
// `gt escalate list` always printed "No escalations found" while the queue held 13
// open, 12 of them HIGH. The old test passed under the bug because it only ever
// constructed the one shape production never produces.
func TestFilterEscalationRecordsKeepsEscalationsDeliveredAsMail(t *testing.T) {
	issues := []*Issue{
		{ID: "hq-real", Labels: []string{"gt:escalation", "gt:message"}},
		{ID: "hq-mail-only", Labels: []string{"gt:message"}},
	}

	got := filterEscalationRecords(issues)
	if len(got) != 1 || got[0].ID != "hq-real" {
		t.Fatalf("filterEscalationRecords() = %#v, want the escalation that was delivered as mail", got)
	}
}

func TestBumpSeverity(t *testing.T) {
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
			got := bumpSeverity(tt.input)
			if got != tt.want {
				t.Errorf("bumpSeverity(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestCreateEscalationBead_PassesDescriptionViaStdin verifies that
// CreateEscalationBead passes the multi-line description through bd's stdin
// (--body-file=-) rather than embedding newlines in --description=...
//
// Regression test for dc-1bxe: bd 1.0.3+ rejects newlines inside --description
// flag values, which broke `gt escalate` for any escalation containing the
// structured YAML metadata block (severity, reason, escalated_by, etc.).
func TestCreateEscalationBead_PassesDescriptionViaStdin(t *testing.T) {
	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")
	stdinPath := filepath.Join(stubDir, "stdin.txt")

	// Stub bd: write each arg on its own line to args.txt, capture stdin to
	// stdin.txt, and emit a minimal valid issue JSON so unmarshal succeeds.
	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
cat > "` + stdinPath + `"
echo '{"id":"dc-test1","title":"x","status":"open","priority":2,"type":"task","labels":["gt:escalation"]}'
exit 0
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Reset --allow-stale capability cache so the stub gets probed fresh.
	ResetBdAllowStaleCacheForTest()

	b := New(t.TempDir())
	fields := &EscalationFields{
		Severity:    "high",
		Reason:      "multi-line\nreason\nwith embedded newlines",
		EscalatedBy: "test/agent",
		EscalatedAt: "2026-05-08T15:00:00Z",
		Fingerprint: "escalation-fp:abc123def456",
	}

	if _, err := b.CreateEscalationBead("Test escalation", fields); err != nil {
		t.Fatalf("CreateEscalationBead: %v", err)
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	args := string(argsData)

	// Must use --body-file=- to read description from stdin.
	if !strings.Contains(args, "--body-file=-") {
		t.Errorf("expected --body-file=- in bd args, got:\n%s", args)
	}
	if !strings.Contains(args, "--labels=escalation-fp:abc123def456") {
		t.Errorf("expected fingerprint label in bd args, got:\n%s", args)
	}
	// Must NOT pass --description=... at all (any --description value would
	// embed the newline-containing structured description and fail bd 1.0.3+).
	for _, line := range strings.Split(args, "\n") {
		if strings.HasPrefix(line, "--description=") {
			t.Errorf("--description=... must not be used (bd rejects newlines), got %q", line)
		}
	}

	stdinData, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stub stdin: %v", err)
	}
	stdin := string(stdinData)
	// The structured description must reach bd via stdin.
	wantInStdin := []string{
		"Test escalation",
		"severity: high",
		"escalated_by: test/agent",
	}
	for _, want := range wantInStdin {
		if !strings.Contains(stdin, want) {
			t.Errorf("expected stdin to contain %q, got:\n%s", want, stdin)
		}
	}
	// Sanity: stdin must contain newlines (it's the multi-line description).
	if !strings.Contains(stdin, "\n") {
		t.Errorf("expected stdin to be multi-line, got %q", stdin)
	}
}

// TestCreateEscalationDeliveryBead_IsDurableAndLinked verifies the bead written
// for the "bead" routing action is the artifact the rest of the escalation
// machinery keys off: durable (never --ephemeral) and carrying the
// "escalation:<record-id>" link plus the severity label.
//
// Regression test for gt-3i4e: the "bead" action was reported as created on
// every escalation and implemented nowhere, so a route with no mail: action
// (the default "low" route) left the escalation as a wisp only — GC'd unread.
func TestCreateEscalationDeliveryBead_IsDurableAndLinked(t *testing.T) {
	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")
	stdinPath := filepath.Join(stubDir, "stdin.txt")

	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
cat > "` + stdinPath + `"
echo '{"id":"hq-copy1","title":"x","status":"open","priority":3,"type":"task","labels":["gt:escalation"]}'
exit 0
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()

	b := New(t.TempDir())
	issue, err := b.CreateEscalationDeliveryBead("[LOW] disk filling", "Escalation ID: hq-rec1\nSeverity: low\n", "hq-rec1", "low")
	if err != nil {
		t.Fatalf("CreateEscalationDeliveryBead: %v", err)
	}
	if issue.ID != "hq-copy1" {
		t.Fatalf("expected the created bead, got %#v", issue)
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	args := strings.Split(string(argsData), "\n")
	has := func(want string) bool { return slices.Contains(args, want) }

	for _, want := range []string{
		"--labels=gt:escalation",
		"--labels=escalation:hq-rec1", // what openEscalationCopies lists by
		"--labels=severity:low",
		"--priority=3",
		"--body-file=-", // bd 1.0.3+ rejects newlines in flag values (dc-1bxe)
	} {
		if !has(want) {
			t.Errorf("expected %q in bd args, got:\n%s", want, string(argsData))
		}
	}
	// The whole point: this bead must OUTLIVE the escalation record wisp.
	if has("--ephemeral") {
		t.Error("the escalation delivery bead must not be ephemeral — a wisp is exactly what gets GC'd unread")
	}

	stdinData, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stub stdin: %v", err)
	}
	if !strings.Contains(string(stdinData), "hq-rec1") {
		t.Errorf("expected the escalation body on stdin, got %q", string(stdinData))
	}
}

func TestCreateEscalationDeliveryBead_RefusesUnlinkedBead(t *testing.T) {
	// An unlinked copy is unreachable from its record: it would never be listed,
	// acked or closed with the escalation.
	if _, err := New(t.TempDir()).CreateEscalationDeliveryBead("[LOW] x", "body", "", "low"); err == nil {
		t.Fatal("expected an error when no escalation record ID is given")
	}
}

func TestEscalationPriority(t *testing.T) {
	for severity, want := range map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3} {
		got, ok := escalationPriority(severity)
		if !ok || got != want {
			t.Errorf("escalationPriority(%q) = %d, %v; want %d, true", severity, got, ok, want)
		}
	}
	// An unknown severity must not silently land at P0.
	if _, ok := escalationPriority("bogus"); ok {
		t.Error("escalationPriority should not map an unknown severity")
	}
}

// --- Escalation record/copy reconciliation (gt-4xl) ---------------------------
//
// An escalation is two beads: an ephemeral RECORD wisp and one durable mail COPY
// per target. `gt escalate close` used to write only the record while
// `gt escalate list` renders only the copies, so a closed escalation stayed in
// the Mayor's queue as an open HIGH forever and the close reported success.

// escalationStub installs a fake `bd` on PATH that answers `show` and `list`
// from JSON fixtures and appends every other invocation to a log. It is enough
// to observe exactly which beads a reconciliation path writes to.
type escalationStub struct {
	t       *testing.T
	dir     string
	logPath string
}

func newEscalationStub(t *testing.T) *escalationStub {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "bd.log")

	// Subcommand is the first non-flag arg (bd may be called as
	// `bd --allow-stale show <id> --json`); the ID is the next non-flag arg.
	// The pinned suffix matters: `bd list --status=open` is silently
	// `--no-pinned`, so the two halves are genuinely different result sets and a
	// stub that cannot tell them apart cannot show a caller losing one (gt-qee3).
	// It falls back to the unsuffixed fixture, which is what every test that does
	// not care about pinning registers.
	script := `#!/bin/sh
sub=""
id=""
label=""
pin=""
for arg in "$@"; do
  case "$arg" in
    --label=*)
      if [ -z "$label" ]; then label="${arg#--label=}"; fi
      continue
      ;;
    --pinned) pin="-pinned"; continue ;;
    --no-pinned) pin="-nopinned"; continue ;;
    -*) continue ;;
  esac
  if [ -z "$sub" ]; then sub="$arg"; continue; fi
  if [ -z "$id" ]; then id="$arg"; fi
done
case "$sub" in
  version) ;;
  show)
    f="` + dir + `/show-$id.json"
    if [ -f "$f" ]; then cat "$f"; else echo '[]'; fi
    ;;
  list)
    base="` + dir + `/list-$(printf '%s' "$label" | tr ':/' '__')"
    if [ -n "$pin" ] && [ -f "$base$pin.json" ]; then
      cat "$base$pin.json"
    elif [ -f "$base.json" ]; then
      cat "$base.json"
    else
      echo '[]'
    fi
    ;;
  *)
    printf '%s\n' "$*" >> "` + logPath + `"
    ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()

	return &escalationStub{t: t, dir: dir, logPath: logPath}
}

// bead registers the JSON `bd show <id>` returns for a bead.
func (s *escalationStub) bead(issue *Issue) {
	s.t.Helper()
	data, err := json.Marshal([]*Issue{issue})
	if err != nil {
		s.t.Fatalf("marshal fixture %s: %v", issue.ID, err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "show-"+issue.ID+".json"), data, 0644); err != nil {
		s.t.Fatalf("write fixture %s: %v", issue.ID, err)
	}
}

// list registers the JSON `bd list --label=<label>` returns.
func (s *escalationStub) list(label string, issues ...*Issue) {
	s.t.Helper()
	if issues == nil {
		issues = []*Issue{}
	}
	data, err := json.Marshal(issues)
	if err != nil {
		s.t.Fatalf("marshal list fixture %s: %v", label, err)
	}
	name := strings.NewReplacer(":", "_", "/", "_").Replace(label)
	if err := os.WriteFile(filepath.Join(s.dir, "list-"+name+".json"), data, 0644); err != nil {
		s.t.Fatalf("write list fixture %s: %v", label, err)
	}
}

// listSplit registers what `bd list --label=<label>` returns for each half of
// the pinned split: `--no-pinned` (bd's silent default) and `--pinned`.
//
// Registering only one half is what production does to a caller that asks only
// once, so a caller that still asks once sees the unpinned half and nothing
// else.
func (s *escalationStub) listSplit(label string, unpinned, pinned []*Issue) {
	s.t.Helper()
	s.listVariant(label, "-nopinned", unpinned)
	s.listVariant(label, "-pinned", pinned)
}

func (s *escalationStub) listVariant(label, suffix string, issues []*Issue) {
	s.t.Helper()
	if issues == nil {
		issues = []*Issue{}
	}
	data, err := json.Marshal(issues)
	if err != nil {
		s.t.Fatalf("marshal list fixture %s%s: %v", label, suffix, err)
	}
	name := strings.NewReplacer(":", "_", "/", "_").Replace(label)
	if err := os.WriteFile(filepath.Join(s.dir, "list-"+name+suffix+".json"), data, 0644); err != nil {
		s.t.Fatalf("write list fixture %s%s: %v", label, suffix, err)
	}
}

// writes returns every mutating bd invocation, one per line.
func (s *escalationStub) writes() []string {
	s.t.Helper()
	data, err := os.ReadFile(s.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		s.t.Fatalf("read bd log: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func (s *escalationStub) hasWrite(substrings ...string) bool {
	s.t.Helper()
next:
	for _, line := range s.writes() {
		for _, want := range substrings {
			if !strings.Contains(line, want) {
				continue next
			}
		}
		return true
	}
	return false
}

// escalationRecord builds an escalation record wisp fixture.
func escalationRecord(id string) *Issue {
	return &Issue{
		ID:          id,
		Title:       "Dolt: server unreachable",
		Description: FormatEscalationDescription("Dolt: server unreachable", &EscalationFields{Severity: "high", EscalatedBy: "gastown/witness"}),
		Status:      "open",
		Labels:      []string{"gt:escalation", "severity:high"},
	}
}

// escalationCopy builds a delivered escalation mail bead fixture. Its
// description is the MAIL BODY, not the structured escalation record.
func escalationCopy(id, recordID, assignee string) *Issue {
	return &Issue{
		ID:          id,
		Title:       "[HIGH] Dolt: server unreachable",
		Description: "Escalation ID: " + recordID + "\nSeverity: high\nFrom: gastown/witness",
		Status:      "open",
		Assignee:    assignee,
		Labels: []string{
			"gt:message", "gt:escalation", "msg-type:escalation",
			EscalationLinkLabelPrefix + recordID, "thread:" + recordID, "severity:high",
		},
	}
}

func TestEscalationRecordID(t *testing.T) {
	tests := []struct {
		name  string
		issue *Issue
		want  string
	}{
		{
			name:  "record returns its own ID",
			issue: escalationRecord("hq-wisp-r1"),
			want:  "hq-wisp-r1",
		},
		{
			name:  "delivered copy returns the record it belongs to",
			issue: escalationCopy("hq-c1", "hq-wisp-r1", "mayor/"),
			want:  "hq-wisp-r1",
		},
		{
			// escalation-fp: is a fingerprint, not a link. A prefix match without
			// the colon would read it as one and reconcile against a hash.
			name:  "fingerprint label is not a link",
			issue: &Issue{ID: "hq-wisp-r2", Labels: []string{"gt:escalation", "escalation-fp:abc123def456"}},
			want:  "hq-wisp-r2",
		},
		{
			name:  "self-referential link is not a link",
			issue: &Issue{ID: "hq-wisp-r3", Labels: []string{"escalation:hq-wisp-r3"}},
			want:  "hq-wisp-r3",
		},
		{
			name:  "nil issue",
			issue: nil,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EscalationRecordID(tt.issue); got != tt.want {
				t.Errorf("EscalationRecordID() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Closing by the record ID — the ID printed in the escalation mail and in the
// documented close command — must also close the delivered copies. Before the
// fix this closed the wisp, printed "✓ Escalation closed", and left the copy
// open in the Mayor's queue permanently.
func TestCloseEscalation_ClosesDeliveredCopies(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	copyA := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	copyB := escalationCopy("hq-c2", "hq-wisp-r1", "overseer/")
	stub.bead(record)
	stub.bead(copyA)
	stub.bead(copyB)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-r1", copyA, copyB)

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-wisp-r1", "mayor/", "resolved: dolt restarted")
	if err != nil {
		t.Fatalf("CloseEscalation: %v", err)
	}

	if result.RecordID != "hq-wisp-r1" {
		t.Errorf("RecordID = %q, want hq-wisp-r1", result.RecordID)
	}
	if len(result.CopyIDs) != 2 {
		t.Errorf("CopyIDs = %v, want both delivered copies", result.CopyIDs)
	}

	// --force on the record too: escalation records are routinely pinned against
	// bd purge, and the pin guard refuses a plain close (gt-u3mo). This is the
	// control for TestCloseEscalation_ClosesPinnedRecord — an UNPINNED record
	// closes by the same path, so the fix did not simply special-case pinning.
	if !stub.hasWrite("close", "hq-wisp-r1", "--force", "--reason=resolved: dolt restarted") {
		t.Errorf("record was not closed; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
	for _, id := range []string{"hq-c1", "hq-c2"} {
		// --force: the copy is assigned to its recipient, and the agent
		// resolving an escalation is rarely that recipient.
		if !stub.hasWrite("close", id, "--force") {
			t.Errorf("delivered copy %s was not closed; bd writes:\n%s", id, strings.Join(stub.writes(), "\n"))
		}
	}
}

// A copy's description is the mail body. Rewriting it with the structured
// escalation format would destroy the delivered message.
func TestCloseEscalation_DoesNotRewriteCopyDescriptions(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	copyA := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(copyA)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-r1", copyA)

	b := New(t.TempDir())
	if _, err := b.CloseEscalation("hq-wisp-r1", "mayor/", "resolved"); err != nil {
		t.Fatalf("CloseEscalation: %v", err)
	}

	for _, line := range stub.writes() {
		if strings.HasPrefix(line, "update hq-c1") && strings.Contains(line, "--body-file") {
			t.Errorf("copy description must not be rewritten, got: %q", line)
		}
	}
	if !stub.hasWrite("update hq-c1", "--add-label=resolved") {
		t.Errorf("copy was not labelled resolved; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
	// The record does carry the structured fields and must be updated.
	if !stub.hasWrite("update hq-wisp-r1", "--body-file=-") {
		t.Errorf("record description was not updated; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

// `gt escalate list` prints the COPY's ID, so that is the ID a reader has in
// hand. Closing by it must resolve the same escalation as closing by the record.
func TestCloseEscalation_AcceptsDeliveredCopyID(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	copyA := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(copyA)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-r1", copyA)

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-c1", "mayor/", "resolved")
	if err != nil {
		t.Fatalf("CloseEscalation: %v", err)
	}

	if result.RecordID != "hq-wisp-r1" {
		t.Errorf("RecordID = %q, want the record hq-wisp-r1", result.RecordID)
	}
	if !stub.hasWrite("close", "hq-wisp-r1") {
		t.Errorf("record was not closed when named by its copy; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
	if !stub.hasWrite("close", "hq-c1") {
		t.Errorf("named copy was not closed; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

// Every escalation closed before this fix has a closed record and open copies.
// Re-running the documented close must reconcile them rather than fail on the
// already-closed record.
func TestCloseEscalation_ReconcilesCopiesOfAlreadyClosedRecord(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	record.Status = "closed"
	copyA := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(copyA)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-r1", copyA)

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-wisp-r1", "mayor/", "already resolved")
	if err != nil {
		t.Fatalf("CloseEscalation on an already-closed record: %v", err)
	}
	if len(result.CopyIDs) != 1 || result.CopyIDs[0] != "hq-c1" {
		t.Errorf("CopyIDs = %v, want [hq-c1]", result.CopyIDs)
	}
	if stub.hasWrite("close", "hq-wisp-r1") {
		t.Errorf("already-closed record must not be closed again; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
	if !stub.hasWrite("close", "hq-c1") {
		t.Errorf("stranded copy was not closed; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

// A record is an ephemeral wisp with a TTL; a copy can outlive it. Closing must
// still clear the copy instead of erroring on the missing record.
func TestCloseEscalation_ClosesCopyWhenRecordWasReaped(t *testing.T) {
	stub := newEscalationStub(t)
	copyA := escalationCopy("hq-c1", "hq-wisp-gone", "mayor/")
	stub.bead(copyA) // no fixture for hq-wisp-gone: bd show returns []
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-gone", copyA)

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-c1", "mayor/", "resolved")
	if err != nil {
		t.Fatalf("CloseEscalation with a reaped record: %v", err)
	}
	if len(result.CopyIDs) != 1 || result.CopyIDs[0] != "hq-c1" {
		t.Errorf("CopyIDs = %v, want [hq-c1]", result.CopyIDs)
	}
}

// A close that leaves a copy open must say so. Reporting success while the
// escalation stays live in the queue is the defect this fixes.
func TestCloseEscalation_ReportsCopiesItCouldNotClose(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	stub.bead(record)
	// The list fixture names a copy that bd show cannot resolve, and closing it
	// is what fails: the stub's `update` succeeds, so simulate failure by having
	// no fixture and asserting on the error path via a bd that rejects the ID.
	copyA := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(copyA)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-r1", copyA)

	// Replace bd with one that fails every close of a copy.
	failing := `#!/bin/sh
case "$*" in
  *"close hq-c1"*) echo "bd: assignee is mayor/, actor is gastown/witness" >&2; exit 1 ;;
esac
exec "` + filepath.Join(stub.dir, "bd") + `" "$@"
`
	failDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(failDir, "bd"), []byte(failing), 0755); err != nil {
		t.Fatalf("write failing bd stub: %v", err)
	}
	t.Setenv("PATH", failDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-wisp-r1", "mayor/", "resolved")
	if err == nil {
		t.Fatalf("CloseEscalation reported success while hq-c1 stayed open in the queue")
	}
	if !strings.Contains(err.Error(), "hq-c1") {
		t.Errorf("error must name the copy left open, got: %v", err)
	}
	if result == nil || result.RecordID != "hq-wisp-r1" {
		t.Errorf("result must still report the closed record, got %#v", result)
	}
}

// The "acked" label is read off the bead `gt escalate list` prints — the copy.
// Acking only the record showed up nowhere.
func TestAckEscalation_LabelsDeliveredCopies(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	copyA := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(copyA)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-r1", copyA)

	b := New(t.TempDir())
	if err := b.AckEscalation("hq-wisp-r1", "mayor/"); err != nil {
		t.Fatalf("AckEscalation: %v", err)
	}

	if !stub.hasWrite("update hq-wisp-r1", "--add-label=acked", "--body-file=-") {
		t.Errorf("record was not acked; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
	if !stub.hasWrite("update hq-c1", "--add-label=acked") {
		t.Errorf("delivered copy was not marked acked; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
	for _, line := range stub.writes() {
		if strings.HasPrefix(line, "update hq-c1") && strings.Contains(line, "--body-file") {
			t.Errorf("copy description must not be rewritten, got: %q", line)
		}
	}
}

// Copies stranded by pre-fix closes must drop out of the queue with no
// migration — but only on positive evidence that the record is closed, and the
// ones dropped must be handed back rather than lost (gt-f0b3).
func TestPartitionResolvedEscalations(t *testing.T) {
	stub := newEscalationStub(t)

	closedRecord := escalationRecord("hq-wisp-closed")
	closedRecord.Status = "closed"
	openRecord := escalationRecord("hq-wisp-open")
	stub.bead(closedRecord)
	stub.bead(openRecord)
	// hq-wisp-reaped has no fixture: bd show returns [] (ErrNotFound).

	resolved := escalationCopy("hq-resolved", "hq-wisp-closed", "mayor/")
	live := escalationCopy("hq-live", "hq-wisp-open", "mayor/")
	orphan := escalationCopy("hq-orphan", "hq-wisp-reaped", "mayor/")
	standalone := escalationRecord("hq-wisp-open2")

	b := New(t.TempDir())
	kept, strandedCopies := b.partitionResolvedEscalations([]*Issue{resolved, live, orphan, standalone})

	want := []string{"hq-live", "hq-orphan", "hq-wisp-open2"}
	if got := issueIDs(kept); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("kept = %v, want %v (a reaped record is not evidence of resolution)", got, want)
	}
	// Every bead in this half is still OPEN, so it still counts wherever beads
	// are counted. Losing it here is what made `gt escalate list` disagree with
	// `bd list --label=gt:escalation --status=open` with nothing to explain it.
	if got := issueIDs(strandedCopies); strings.Join(got, ",") != "hq-resolved" {
		t.Errorf("stranded = %v, want [hq-resolved]: the hidden copies must be reported, not discarded", got)
	}
}

// ListEscalationsWithStranded is what `gt escalate list` reads. Its second
// return is exactly the gap between that list and any count of open
// gt:escalation beads.
func TestListEscalationsWithStranded_ReportsTheHiddenCopies(t *testing.T) {
	stub := newEscalationStub(t)
	closedRecord := escalationRecord("hq-wisp-closed")
	closedRecord.Status = "closed"
	openRecord := escalationRecord("hq-wisp-open")
	stub.bead(closedRecord)
	stub.bead(openRecord)

	resolved := escalationCopy("hq-resolved", "hq-wisp-closed", "mayor/")
	live := escalationCopy("hq-live", "hq-wisp-open", "mayor/")
	stub.list("gt:escalation", resolved, live)

	b := New(t.TempDir())
	open, strandedCopies, err := b.ListEscalationsWithStranded()
	if err != nil {
		t.Fatalf("ListEscalationsWithStranded: %v", err)
	}
	if got := issueIDs(open); strings.Join(got, ",") != "hq-live" {
		t.Errorf("open = %v, want [hq-live]", got)
	}
	if got := issueIDs(strandedCopies); strings.Join(got, ",") != "hq-resolved" {
		t.Errorf("stranded = %v, want [hq-resolved]", got)
	}
	// The two halves must account for every open bead the query returned:
	// anything in neither is hidden with no signal at all.
	if len(open)+len(strandedCopies) != 2 {
		t.Errorf("open(%d) + stranded(%d) must account for all 2 open escalation beads", len(open), len(strandedCopies))
	}
}

// --- Pinned escalations (gt-qee3) --------------------------------------------
//
// `bd list --status=open` is silently `--no-pinned`: measured on hq 2026-08-26,
// the default returned 686 open issues, `--pinned` returned 3 more, and SQL
// confirmed 686/3. `gt escalate list` therefore printed "No escalations found"
// while three escalations sat open — including a HIGH — and `--all` rendered all
// of them, which is what made the renderer look healthy while the filter was not.
func TestListEscalationsWithStranded_IncludesPinnedEscalations(t *testing.T) {
	stub := newEscalationStub(t)
	stub.bead(escalationRecord("hq-wisp-open"))
	stub.bead(escalationRecord("hq-wisp-pinned"))

	unpinned := escalationCopy("hq-live", "hq-wisp-open", "mayor/")
	pinned := escalationCopy("hq-pinned", "hq-wisp-pinned", "mayor/")
	stub.listSplit("gt:escalation", []*Issue{unpinned}, []*Issue{pinned})

	b := New(t.TempDir())
	open, strandedCopies, err := b.ListEscalationsWithStranded()
	if err != nil {
		t.Fatalf("ListEscalationsWithStranded: %v", err)
	}
	if len(strandedCopies) != 0 {
		t.Errorf("stranded = %v, want none: both records are open", issueIDs(strandedCopies))
	}
	got := issueIDs(open)
	sort.Strings(got)
	if strings.Join(got, ",") != "hq-live,hq-pinned" {
		t.Errorf("open = %v, want both hq-live and hq-pinned: pinning an escalation must not delete it from the list", got)
	}
}

// The union must not double-count. Nothing distinguishes the two queries at the
// bd level but the flag, so a store that answers both with the same row — or a
// bd whose default starts including pinned issues — must still render it once.
func TestListEscalationsWithStranded_UnionDoesNotDuplicate(t *testing.T) {
	stub := newEscalationStub(t)
	stub.bead(escalationRecord("hq-wisp-open"))

	live := escalationCopy("hq-live", "hq-wisp-open", "mayor/")
	// One fixture, no pinned suffix: the stub returns it to BOTH halves.
	stub.list("gt:escalation", live)

	b := New(t.TempDir())
	open, _, err := b.ListEscalationsWithStranded()
	if err != nil {
		t.Fatalf("ListEscalationsWithStranded: %v", err)
	}
	if got := issueIDs(open); strings.Join(got, ",") != "hq-live" {
		t.Errorf("open = %v, want [hq-live] exactly once", got)
	}
}

// Duplicate suppression reads the same open-escalation query. A pinned
// escalation missing from it does not merely go unseen — it stops matching its
// own fingerprint, so the identical escalation re-fires on every raise.
func TestListEscalationsByFingerprint_FindsPinnedEscalations(t *testing.T) {
	stub := newEscalationStub(t)
	stub.bead(escalationRecord("hq-wisp-pinned"))

	pinned := escalationCopy("hq-pinned", "hq-wisp-pinned", "mayor/")
	stub.listSplit("gt:escalation", nil, []*Issue{pinned})

	b := New(t.TempDir())
	got, err := b.ListEscalationsByFingerprint("escalation-fp:abc123")
	if err != nil {
		t.Fatalf("ListEscalationsByFingerprint: %v", err)
	}
	if len(got) != 1 || got[0].ID != "hq-pinned" {
		t.Errorf("ListEscalationsByFingerprint = %v, want [hq-pinned]: a pinned duplicate must still suppress", issueIDs(got))
	}
}

func issueIDs(issues []*Issue) []string {
	var ids []string
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

// ListEscalations is what `gt escalate list`, the Mayor's queue, and
// `gt escalate stale` all read. A resolved escalation must not appear — stale
// re-escalation would otherwise bump closed escalations to critical.
func TestListEscalations_HidesResolvedEscalations(t *testing.T) {
	stub := newEscalationStub(t)
	closedRecord := escalationRecord("hq-wisp-closed")
	closedRecord.Status = "closed"
	openRecord := escalationRecord("hq-wisp-open")
	stub.bead(closedRecord)
	stub.bead(openRecord)

	resolved := escalationCopy("hq-resolved", "hq-wisp-closed", "mayor/")
	live := escalationCopy("hq-live", "hq-wisp-open", "mayor/")
	stub.list("gt:escalation", resolved, live)

	b := New(t.TempDir())
	got, err := b.ListEscalations()
	if err != nil {
		t.Fatalf("ListEscalations: %v", err)
	}
	if len(got) != 1 || got[0].ID != "hq-live" {
		var ids []string
		for _, issue := range got {
			ids = append(ids, issue.ID)
		}
		t.Errorf("ListEscalations() = %v, want only the unresolved escalation hq-live", ids)
	}
}

// --- Severity actually applied, and ack/close surviving the record (gt-psh) ---
//
// Two halves of one complaint. Severity was recorded as a label and nowhere
// else, so `--severity high` landed at bd's default P2 and everything that
// triages by priority read it as routine. And the record is an ephemeral wisp,
// so the ID printed in every escalation mail — the one the mail body itself
// tells you to ack and close with — stops resolving once the record ages out.

// The reported shape exactly: severity:high on the bead, priority 2 in the row.
func TestCreateEscalationBead_CarriesSeverityAsPriority(t *testing.T) {
	for severity, want := range map[string]string{
		"critical": "--priority=0",
		"high":     "--priority=1",
		"medium":   "--priority=2",
		"low":      "--priority=3",
	} {
		t.Run(severity, func(t *testing.T) {
			stubDir := t.TempDir()
			argsPath := filepath.Join(stubDir, "args.txt")
			stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
cat > /dev/null
echo '{"id":"hq-wisp-p1","title":"x","status":"open","type":"task","labels":["gt:escalation"]}'
exit 0
`
			if err := os.WriteFile(filepath.Join(stubDir, "bd"), []byte(stubScript), 0755); err != nil {
				t.Fatalf("write bd stub: %v", err)
			}
			t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			ResetBdAllowStaleCacheForTest()

			b := New(t.TempDir())
			if _, err := b.CreateEscalationBead("Dolt: server unreachable", &EscalationFields{Severity: severity}); err != nil {
				t.Fatalf("CreateEscalationBead: %v", err)
			}

			argsData, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatalf("read stub args: %v", err)
			}
			if !slices.Contains(strings.Split(string(argsData), "\n"), want) {
				t.Errorf("severity %q must set %s on the record, got:\n%s", severity, want, string(argsData))
			}
		})
	}
}

// An unrecognised severity must keep bd's default rather than landing at P0 —
// the same guard escalationPriority gives the delivery bead.
func TestCreateEscalationBead_LeavesUnknownSeverityAtDefaultPriority(t *testing.T) {
	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")
	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
cat > /dev/null
echo '{"id":"hq-wisp-p2","title":"x","status":"open","type":"task","labels":["gt:escalation"]}'
exit 0
`
	if err := os.WriteFile(filepath.Join(stubDir, "bd"), []byte(stubScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()

	b := New(t.TempDir())
	if _, err := b.CreateEscalationBead("odd one", &EscalationFields{Severity: "bogus"}); err != nil {
		t.Fatalf("CreateEscalationBead: %v", err)
	}
	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	for _, arg := range strings.Split(string(argsData), "\n") {
		if strings.HasPrefix(arg, "--priority=") {
			t.Errorf("unknown severity must not set a priority, got %q", arg)
		}
	}
}

// The documented command is `gt escalate close <record-id>`, printed in the mail
// body and in the delivery bead. Once the record wisp ages out, that ID must
// still resolve through the copies — failing here left the escalation live in
// the Mayor's queue forever with no command able to clear it.
func TestCloseEscalation_AcceptsReapedRecordID(t *testing.T) {
	stub := newEscalationStub(t)
	copyA := escalationCopy("hq-c1", "hq-wisp-gone", "mayor/")
	stub.bead(copyA) // no fixture for hq-wisp-gone: bd show returns []
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-gone", copyA)

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-wisp-gone", "mayor/", "resolved")
	if err != nil {
		t.Fatalf("CloseEscalation by reaped record ID: %v", err)
	}
	if result.RecordID != "hq-wisp-gone" {
		t.Errorf("RecordID = %q, want hq-wisp-gone", result.RecordID)
	}
	if len(result.CopyIDs) != 1 || result.CopyIDs[0] != "hq-c1" {
		t.Errorf("CopyIDs = %v, want [hq-c1]", result.CopyIDs)
	}
	if !stub.hasWrite("close hq-c1", "--force") {
		t.Errorf("the stranded copy was not closed; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

func TestAckEscalation_AcceptsReapedRecordID(t *testing.T) {
	stub := newEscalationStub(t)
	copyA := escalationCopy("hq-c1", "hq-wisp-gone", "mayor/")
	stub.bead(copyA)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-gone", copyA)

	b := New(t.TempDir())
	if err := b.AckEscalation("hq-wisp-gone", "mayor/"); err != nil {
		t.Fatalf("AckEscalation by reaped record ID: %v", err)
	}
	if !stub.hasWrite("update hq-c1", "--add-label=acked") {
		t.Errorf("the copy was not marked acked; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

// A genuinely unknown ID must still fail. Without this the fallback would turn
// every typo into a silent no-op success.
func TestCloseEscalation_StillFailsOnUnknownID(t *testing.T) {
	newEscalationStub(t) // no fixtures at all: show returns [], list returns []

	b := New(t.TempDir())
	if _, err := b.CloseEscalation("hq-wisp-nosuch", "mayor/", "resolved"); err == nil {
		t.Fatal("closing an ID with no record and no copies must fail, not report success")
	}
}

// --- Pinned escalations must still close (gt-u3mo) ---------------------------
//
// Escalation records were pinned town-wide to keep `bd purge` from deleting
// them. bd's pin guard rejects a plain `bd close`, so the mitigation for
// "escalations can be deleted" produced "escalations cannot be resolved": every
// escalation in the town became un-closeable through the command that exists to
// close it. Pinning protects a record from DELETION; a pinned+closed row is
// still purge-protected, so closing must go through.

// pinGuard puts a bd in front of the stub that refuses any close lacking
// --force, exactly as bd's pin guard does, and passes everything else through
// so the stub keeps answering shows and recording writes.
func (s *escalationStub) pinGuard() {
	s.t.Helper()
	s.interpose(`
forced=""
for arg in "$@"; do
  if [ "$arg" = "--force" ]; then forced=1; fi
done
case "$1" in
  close)
    if [ -z "$forced" ]; then
      echo "Error: cannot modify pinned issue $2 (use --force to override)" >&2
      exit 1
    fi
    ;;
esac
`)
}

// interpose installs a bd on PATH that runs body and then execs the stub's bd.
func (s *escalationStub) interpose(body string) {
	s.t.Helper()
	dir := s.t.TempDir()
	script := "#!/bin/sh\n" + body + "\nexec \"" + filepath.Join(s.dir, "bd") + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(script), 0755); err != nil {
		s.t.Fatalf("write interposed bd: %v", err)
	}
	s.t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()
}

func TestCloseEscalation_ClosesPinnedRecord(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	copyA := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(copyA)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-r1", copyA)
	stub.pinGuard()

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-wisp-r1", "mayor/", "resolved: dolt restarted")
	if err != nil {
		t.Fatalf("CloseEscalation on a pinned record: %v", err)
	}

	// Both halves, not just the record: forcing one half by hand is exactly the
	// closed-record/open-copy split gt-4xl fixed.
	if !stub.hasWrite("close", "hq-wisp-r1", "--force") {
		t.Errorf("pinned record was not closed; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
	if len(result.CopyIDs) != 1 || result.CopyIDs[0] != "hq-c1" {
		t.Errorf("CopyIDs = %v, want [hq-c1]", result.CopyIDs)
	}
	if !stub.hasWrite("close", "hq-c1", "--force") {
		t.Errorf("delivered copy was not closed; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

// bd's guard advises "(use --force to override)". That remedy is unreachable
// from where the operator stands — `gt escalate close` has no --force flag, so
// following it verbatim produces "unknown flag" — and stale besides, since the
// close path already passes --force. Surfacing it sends the reader after a flag
// that does not exist.
func TestCloseEscalation_DoesNotAdviseAFlagTheCommandLacks(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	stub.bead(record)
	stub.list(EscalationLinkLabelPrefix + "hq-wisp-r1")
	// A bd that refuses the close even WITH --force, still advising the flag.
	stub.interpose(`
case "$1" in
  close)
    echo "Error: cannot modify pinned issue $2 (use --force to override)" >&2
    exit 1
    ;;
esac
`)

	b := New(t.TempDir())
	_, err := b.CloseEscalation("hq-wisp-r1", "mayor/", "resolved")
	if err == nil {
		t.Fatal("CloseEscalation reported success while the record stayed open")
	}
	if strings.Contains(err.Error(), "use --force to override") {
		t.Errorf("error advises a flag gt escalate close does not accept: %v", err)
	}
	// The diagnosis itself must survive the scrub — dropping "pinned" would
	// trade misleading advice for no advice at all.
	if !strings.Contains(err.Error(), "pinned") {
		t.Errorf("error lost the reason for the failure: %v", err)
	}
}

// Anything that is not bd's exact --force remedy passes through untouched: a
// scrub that rewrote unrelated errors would hide real diagnoses.
func TestScrubForceAdvice_LeavesOtherErrorsAlone(t *testing.T) {
	if got := scrubForceAdvice(nil); got != nil {
		t.Errorf("scrubForceAdvice(nil) = %v, want nil", got)
	}
	orig := errors.New("bd close hq-c1: assignee is mayor/, actor is gastown/witness")
	if got := scrubForceAdvice(orig); got != orig {
		t.Errorf("scrubForceAdvice rewrote an unrelated error: %v", got)
	}
}

// --- Re-escalation applies the new severity where it is read (gt-psh) --------

func TestReescalateEscalation_RaisesPriorityWithSeverity(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1") // severity high
	copyA := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(copyA)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-r1", copyA)

	b := New(t.TempDir())
	result, err := b.ReescalateEscalation("hq-wisp-r1", "gastown/deacon", 0)
	if err != nil {
		t.Fatalf("ReescalateEscalation: %v", err)
	}
	if result.NewSeverity != "critical" {
		t.Fatalf("NewSeverity = %q, want critical", result.NewSeverity)
	}

	// critical is P0. Bumping the label without the priority is the whole defect:
	// the escalation still sorts as routine work after being re-escalated.
	if !stub.hasWrite("update hq-wisp-r1", "--priority=0", "--add-label=severity:critical") {
		t.Errorf("record priority did not follow the severity; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
	// The copy is what `gt escalate list` and the Mayor's queue render.
	if !stub.hasWrite("update hq-c1", "--priority=0", "--add-label=severity:critical", "--remove-label=severity:high") {
		t.Errorf("delivered copy did not follow the severity; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
	// ...but its description is the mail body and must survive untouched.
	for _, line := range stub.writes() {
		if strings.HasPrefix(line, "update hq-c1") && strings.Contains(line, "--body-file") {
			t.Errorf("copy description must not be rewritten, got: %q", line)
		}
	}
}

// `gt escalate stale` feeds this whatever bead its list turned up, and that list
// renders copies. Running the bump against a copy would rewrite the mail body as
// a structured record and re-derive severity from it.
func TestReescalateEscalation_BumpsTheRecordWhenGivenACopyID(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	copyA := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(copyA)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-r1", copyA)

	b := New(t.TempDir())
	result, err := b.ReescalateEscalation("hq-c1", "gastown/deacon", 0)
	if err != nil {
		t.Fatalf("ReescalateEscalation: %v", err)
	}
	if result.ID != "hq-wisp-r1" {
		t.Errorf("ID = %q, want the record hq-wisp-r1", result.ID)
	}
	if !stub.hasWrite("update hq-wisp-r1", "--body-file=-") {
		t.Errorf("the structured record was not rewritten; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

// A reaped record has no severity, count or history to bump, and the copies'
// severity must not be re-derived from a mail body.
func TestReescalateEscalation_SkipsWhenRecordWasReaped(t *testing.T) {
	stub := newEscalationStub(t)
	copyA := escalationCopy("hq-c1", "hq-wisp-gone", "mayor/")
	stub.bead(copyA)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-gone", copyA)

	b := New(t.TempDir())
	result, err := b.ReescalateEscalation("hq-wisp-gone", "gastown/deacon", 0)
	if err != nil {
		t.Fatalf("ReescalateEscalation with a reaped record: %v", err)
	}
	if !result.Skipped || result.SkipReason == "" {
		t.Errorf("expected a reported skip, got %#v", result)
	}
	if stub.hasWrite("update hq-c1") {
		t.Errorf("a reaped record must not bump its copies blindly; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

// A record and every one of its copies carry "gt:escalation", so an un-deduped
// stale list bumped the same escalation once per bead in one pass — low reaching
// high in a single run, re-mailing the targets at each step.
func TestListStaleEscalations_ReturnsOneEntryPerEscalation(t *testing.T) {
	stub := newEscalationStub(t)
	old := "2020-01-01T00:00:00Z"

	record := escalationRecord("hq-wisp-r1")
	record.CreatedAt = old
	copyA := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	copyA.CreatedAt = old
	copyB := escalationCopy("hq-c2", "hq-wisp-r1", "gastown/witness")
	copyB.CreatedAt = old
	other := escalationRecord("hq-wisp-r2")
	other.CreatedAt = old

	stub.bead(record)
	stub.bead(other)
	stub.list("gt:escalation", record, copyA, copyB, other)

	b := New(t.TempDir())
	stale, err := b.ListStaleEscalations(time.Hour)
	if err != nil {
		t.Fatalf("ListStaleEscalations: %v", err)
	}

	seen := map[string]int{}
	for _, issue := range stale {
		seen[EscalationRecordID(issue)]++
	}
	if len(stale) != 2 || seen["hq-wisp-r1"] != 1 || seen["hq-wisp-r2"] != 1 {
		var ids []string
		for _, issue := range stale {
			ids = append(ids, issue.ID)
		}
		t.Errorf("ListStaleEscalations() = %v, want one entry per escalation record", ids)
	}
}

// TestParseEscalationFields_FallsBackToMailFrom covers the provenance half of
// gt-nhp's first defect. `gt escalate list` renders the delivered COPIES, and a
// mail-delivered copy's description is the mail body — which spells the sender
// "From: ...", not "escalated_by: ...". So the live queue printed "From:" with
// nothing after it on every row (10 of 10 on hq, 2026-08-18).
func TestParseEscalationFields_FallsBackToMailFrom(t *testing.T) {
	mailBody := strings.Join([]string{
		"Escalation ID: hq-wisp-rec1",
		"Severity: high",
		"From: gastown/witness",
		"",
		"Reason:",
		"a live nuke hazard",
	}, "\n")

	fields := ParseEscalationFields(mailBody)
	if fields.EscalatedBy != "gastown/witness" {
		t.Errorf("EscalatedBy = %q, want gastown/witness — the mail body is what the "+
			"delivered copy carries, and it is the copies the queue renders", fields.EscalatedBy)
	}
	if fields.Severity != "high" {
		t.Errorf("Severity = %q, want high", fields.Severity)
	}
}

// An explicit escalated_by always wins over a "From:" line, in either order: the
// structured field is authored by the escalation machinery, "From:" is a
// fallback read out of prose.
func TestParseEscalationFields_ExplicitEscalatedByWinsOverFrom(t *testing.T) {
	for name, description := range map[string]string{
		"escalated_by first": "severity: high\nescalated_by: gastown/refinery\nFrom: someone-else\n",
		"from first":         "severity: high\nFrom: someone-else\nescalated_by: gastown/refinery\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := ParseEscalationFields(description).EscalatedBy; got != "gastown/refinery" {
				t.Errorf("EscalatedBy = %q, want gastown/refinery", got)
			}
		})
	}
}

// --- The stranded-copy reconcile (gt-w0z8) -----------------------------------
//
// gt-qee3 fixed the pinned exclusion on the escalation LIST query and left it in
// place on the COPIES query, which is what every reconcile path walks. So the
// list learned to name its stranded copies and print a reconcile command, and
// that command could not clear any of them: it resolved the copy to its record,
// found the record already closed (which is what "stranded" MEANS), listed zero
// copies because the copy was pinned, wrote to nothing, and printed a checkmark.
//
// Measured on hq 2026-08-26 against the real stranded copy hq-9mxa7:
//
//	bd list --label=escalation:hq-wisp-51nirc --status=open              -> []
//	bd list --label=escalation:hq-wisp-51nirc --status=open --pinned     -> hq-9mxa7
//	bd list --label=escalation:hq-wisp-51nirc --status=open --no-pinned  -> 0
//
// The remedy was unreachable for the entire population it was offered to.

// The pinned half of the copies query, isolated: the close is issued by the
// RECORD's ID, so the named-bead path below cannot rescue it and only the query
// can find the copy.
func TestCloseEscalation_ClosesPinnedStrandedCopy(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	record.Status = "closed" // stranded is closed-record-by-definition
	pinnedCopy := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(pinnedCopy)
	// The copy answers ONLY the --pinned half, exactly as hq-9mxa7 does.
	stub.listSplit(EscalationLinkLabelPrefix+"hq-wisp-r1", nil, []*Issue{pinnedCopy})

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-wisp-r1", "mayor/", "reconciled")
	if err != nil {
		t.Fatalf("CloseEscalation: %v", err)
	}
	if len(result.CopyIDs) != 1 || result.CopyIDs[0] != "hq-c1" {
		t.Errorf("CopyIDs = %v, want [hq-c1]: a pinned copy is still a copy", result.CopyIDs)
	}
	if !stub.hasWrite("close", "hq-c1") {
		t.Errorf("pinned stranded copy was not closed; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
	if !result.Changed() {
		t.Error("Changed() = false after closing a copy")
	}
}

// The named-bead path, isolated: the copies query returns nothing at all in
// BOTH halves, and closing by the copy's own ID must still close that copy.
// This is the guarantee the printed reconcile command rests on — it names a
// copy ID, so that copy is closed by construction rather than by whatever the
// label query happens to be able to see.
func TestCloseEscalation_ClosesTheNamedCopyWhenTheQueryMissesIt(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	record.Status = "closed"
	strandedCopy := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(strandedCopy)
	stub.listSplit(EscalationLinkLabelPrefix+"hq-wisp-r1", nil, nil) // blind query

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-c1", "mayor/", "reconciled")
	if err != nil {
		t.Fatalf("CloseEscalation: %v", err)
	}
	if len(result.CopyIDs) != 1 || result.CopyIDs[0] != "hq-c1" {
		t.Errorf("CopyIDs = %v, want [hq-c1]: the bead the operator named must be closed", result.CopyIDs)
	}
	if !stub.hasWrite("close", "hq-c1") {
		t.Errorf("named copy was not closed; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

// The result must name the ID the caller passed, not the record it resolved to.
// The tell was on screen the whole time — "✓ Escalation closed: hq-wisp-51nirc"
// after typing hq-9mxa7 — and only a reader who noticed the ID had changed
// could have caught it.
func TestCloseEscalation_ResultNamesTheRequestedID(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	copyA := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(copyA)
	stub.list(EscalationLinkLabelPrefix+"hq-wisp-r1", copyA)

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-c1", "mayor/", "resolved")
	if err != nil {
		t.Fatalf("CloseEscalation: %v", err)
	}
	if result.RequestedID != "hq-c1" {
		t.Errorf("RequestedID = %q, want hq-c1", result.RequestedID)
	}
	if result.RecordID != "hq-wisp-r1" {
		t.Errorf("RecordID = %q, want hq-wisp-r1", result.RecordID)
	}
	if !result.RecordClosed {
		t.Error("RecordClosed = false, but the record was open and was closed by this call")
	}
}

// A close that wrote to nothing must report that it wrote to nothing. Without a
// negative case the success line is unfalsifiable: it was printed for two real
// reconciles that cleared nothing, and the operator saw two green checkmarks.
func TestCloseEscalation_ReportsANoOpAsANoOp(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	record.Status = "closed"
	stub.bead(record)
	stub.listSplit(EscalationLinkLabelPrefix+"hq-wisp-r1", nil, nil)

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-wisp-r1", "mayor/", "resolved")
	if err != nil {
		t.Fatalf("CloseEscalation: %v", err)
	}
	if result.Changed() {
		t.Errorf("Changed() = true, but nothing was closed: %#v", result)
	}
	// Control: the same assertion must go the other way when there IS work.
	// A Changed() that is always false would pass the check above.
	if stub.hasWrite("close", "hq-wisp-r1") {
		t.Errorf("an already-closed record must not be closed again; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

// Ack and re-escalation walk the same copies query, so the pinned blindness was
// never confined to close: an ack of a pinned escalation labelled the record and
// left the bead the queue renders unmarked.
func TestAckEscalation_LabelsPinnedCopies(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	pinnedCopy := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(pinnedCopy)
	stub.listSplit(EscalationLinkLabelPrefix+"hq-wisp-r1", nil, []*Issue{pinnedCopy})

	b := New(t.TempDir())
	if err := b.AckEscalation("hq-wisp-r1", "mayor/"); err != nil {
		t.Fatalf("AckEscalation: %v", err)
	}
	if !stub.hasWrite("update hq-c1", "--add-label=acked") {
		t.Errorf("pinned copy was not marked acked; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

func TestReescalateEscalation_UpdatesPinnedCopies(t *testing.T) {
	stub := newEscalationStub(t)
	record := escalationRecord("hq-wisp-r1")
	pinnedCopy := escalationCopy("hq-c1", "hq-wisp-r1", "mayor/")
	stub.bead(record)
	stub.bead(pinnedCopy)
	stub.listSplit(EscalationLinkLabelPrefix+"hq-wisp-r1", nil, []*Issue{pinnedCopy})

	b := New(t.TempDir())
	if _, err := b.ReescalateEscalation("hq-wisp-r1", "gastown/witness", 0); err != nil {
		t.Fatalf("ReescalateEscalation: %v", err)
	}
	if !stub.hasWrite("update hq-c1", "--add-label=severity:critical") {
		t.Errorf("pinned copy kept the old severity; bd writes:\n%s", strings.Join(stub.writes(), "\n"))
	}
}

// A record that has been reaped is reachable only through its copies, so the
// pinned blindness also decided whether the close could resolve the ID at all.
func TestCloseEscalation_ResolvesReapedRecordThroughAPinnedCopy(t *testing.T) {
	stub := newEscalationStub(t)
	pinnedCopy := escalationCopy("hq-c1", "hq-wisp-gone", "mayor/")
	stub.bead(pinnedCopy) // no fixture for hq-wisp-gone: bd show returns []
	stub.listSplit(EscalationLinkLabelPrefix+"hq-wisp-gone", nil, []*Issue{pinnedCopy})

	b := New(t.TempDir())
	result, err := b.CloseEscalation("hq-wisp-gone", "mayor/", "resolved")
	if err != nil {
		t.Fatalf("CloseEscalation by a reaped record ID with only a pinned copy: %v", err)
	}
	if len(result.CopyIDs) != 1 || result.CopyIDs[0] != "hq-c1" {
		t.Errorf("CopyIDs = %v, want [hq-c1]", result.CopyIDs)
	}
}
