package web

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/activity"
)

// Test error for simulating fetch failures
var errFetchFailed = errors.New("fetch failed")

// MockConvoyFetcher is a mock implementation for testing.
type MockConvoyFetcher struct {
	Convoys              []ConvoyRow
	MergeQueue           []MergeQueueRow
	MergeQueueFailedRigs []string
	// MergeQueueError is the whole-queue failure, distinct from
	// MergeQueueFailedRigs: one rig short is a partial count, no rig list at all
	// is no count.
	MergeQueueError error
	Workers         []WorkerRow
	// WorkersError and the other per-panel errors are the levers for the
	// "could not read" render path. Each panel needs its own: a single shared
	// error cannot show that one panel failing leaves the others intact.
	WorkersError error
	// WorkersFailedStores names stores whose assigned-bead query failed, so the
	// "these workers may only look idle" caveat can be driven from a test.
	// Separate from WorkersError because the two caveats are separate facts.
	WorkersFailedStores []string
	Mail                []MailRow
	MailError           error
	Rigs                []RigRow
	RigsError           error
	Dogs                []DogRow
	DogsError           error
	Escalations         []EscalationRow
	EscalationsError    error
	Health              *HealthRow
	HealthError         error
	Queues              []QueueRow
	QueuesError         error
	Sessions            []SessionRow
	SessionsError       error
	Hooks               []HookRow
	// HooksFailedStores names stores whose hooked-bead query failed, so the
	// handler's partial-union caveat can be driven from a test.
	HooksFailedStores []string
	// HooksTruncatedStores and IssuesTruncatedStores name stores that filled
	// their whole row allowance. Separate levers from the *FailedStores ones
	// because a truncated read is the case the banner used to render as a
	// complete one, and a test that can only fail a store cannot reach it.
	HooksTruncatedStores []string
	// HooksError and IssuesError are the whole-query failure of a union panel,
	// which leaves no StoreResult to name failed stores with.
	HooksError  error
	IssuesError error
	Mayor       *MayorStatus
	MayorError  error
	Issues      []IssueRow
	// IssuesFailedStores is the same lever for the backlog union.
	IssuesFailedStores    []string
	IssuesTruncatedStores []string
	// IssuesReadStores names stores that answered. It is what keeps a mock with
	// no rows and a failed store from reading as "no store answered at all":
	// Unreadable() is the "?" render, and a truncation test needs the panel to
	// be readable so the "+" render is the one under test.
	IssuesReadStores []string
	HooksReadStores  []string
	Activity         []ActivityRow
	ActivityError    error
	// Error is the convoy fetch's error.
	Error error
}

func (m *MockConvoyFetcher) FetchConvoys() ([]ConvoyRow, error) {
	return m.Convoys, m.Error
}

func (m *MockConvoyFetcher) FetchMergeQueue() (MergeQueueResult, error) {
	return MergeQueueResult{Rows: m.MergeQueue, FailedRigs: m.MergeQueueFailedRigs}, m.MergeQueueError
}

func (m *MockConvoyFetcher) FetchWorkers() (StoreResult[WorkerRow], error) {
	return StoreResult[WorkerRow]{Rows: m.Workers, FailedStores: m.WorkersFailedStores}, m.WorkersError
}

func (m *MockConvoyFetcher) FetchMail() ([]MailRow, error) {
	return m.Mail, m.MailError
}

func (m *MockConvoyFetcher) FetchRigs() ([]RigRow, error) {
	return m.Rigs, m.RigsError
}

func (m *MockConvoyFetcher) FetchDogs() ([]DogRow, error) {
	return m.Dogs, m.DogsError
}

func (m *MockConvoyFetcher) FetchEscalations() ([]EscalationRow, error) {
	return m.Escalations, m.EscalationsError
}

func (m *MockConvoyFetcher) FetchHealth() (*HealthRow, error) {
	return m.Health, m.HealthError
}

func (m *MockConvoyFetcher) FetchQueues() ([]QueueRow, error) {
	return m.Queues, m.QueuesError
}

func (m *MockConvoyFetcher) FetchSessions() ([]SessionRow, error) {
	return m.Sessions, m.SessionsError
}

func (m *MockConvoyFetcher) FetchHooks() (StoreResult[HookRow], error) {
	return StoreResult[HookRow]{
		Rows:            m.Hooks,
		FailedStores:    m.HooksFailedStores,
		TruncatedStores: m.HooksTruncatedStores,
		ReadStores:      m.HooksReadStores,
	}, m.HooksError
}

func (m *MockConvoyFetcher) FetchMayor() (*MayorStatus, error) {
	return m.Mayor, m.MayorError
}

func (m *MockConvoyFetcher) FetchIssues() (StoreResult[IssueRow], error) {
	return StoreResult[IssueRow]{
		Rows:            m.Issues,
		FailedStores:    m.IssuesFailedStores,
		TruncatedStores: m.IssuesTruncatedStores,
		ReadStores:      m.IssuesReadStores,
	}, m.IssuesError
}

func (m *MockConvoyFetcher) FetchActivity() ([]ActivityRow, error) {
	return m.Activity, m.ActivityError
}

func TestConvoyHandler_RendersTemplate(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{
			{
				ID:           "hq-cv-abc",
				Title:        "Test Convoy",
				Status:       "open",
				Progress:     "2/5",
				Completed:    2,
				Total:        5,
				LastActivity: activity.Calculate(time.Now().Add(-1 * time.Minute)),
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Check convoy data is rendered
	if !strings.Contains(body, "hq-cv-abc") {
		t.Error("Response should contain convoy ID")
	}
	// Note: Convoy titles are no longer shown in the simplified dashboard table view
	if !strings.Contains(body, "2/5") {
		t.Error("Response should contain progress")
	}
}

func TestConvoyHandler_LastActivityColors(t *testing.T) {
	tests := []struct {
		name      string
		age       time.Duration
		wantClass string
	}{
		{"green for active", 30 * time.Second, "activity-green"},
		{"yellow for stale", 6 * time.Minute, "activity-yellow"},
		{"red for stuck", 11 * time.Minute, "activity-red"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &MockConvoyFetcher{
				Convoys: []ConvoyRow{
					{
						ID:           "hq-cv-test",
						Title:        "Test",
						Status:       "open",
						LastActivity: activity.Calculate(time.Now().Add(-tt.age)),
					},
				},
			}

			handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
			if err != nil {
				t.Fatalf("NewConvoyHandler() error = %v", err)
			}

			req := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			body := w.Body.String()
			if !strings.Contains(body, tt.wantClass) {
				t.Errorf("Response should contain %q", tt.wantClass)
			}
		})
	}
}

func TestConvoyHandler_EmptyConvoys(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if !strings.Contains(body, "No active convoys") {
		t.Error("Response should show empty state message")
	}
}

func TestConvoyHandler_ContentType(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", contentType)
	}
}

func TestConvoyHandler_MultipleConvoys(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{
			{ID: "hq-cv-1", Title: "First Convoy", Status: "open"},
			{ID: "hq-cv-2", Title: "Second Convoy", Status: "closed"},
			{ID: "hq-cv-3", Title: "Third Convoy", Status: "open"},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	body := w.Body.String()

	// Check all convoys are rendered
	for _, id := range []string{"hq-cv-1", "hq-cv-2", "hq-cv-3"} {
		if !strings.Contains(body, id) {
			t.Errorf("Response should contain convoy %s", id)
		}
	}
}

// Integration tests for error handling
// Note: The dashboard handler treats fetch errors as non-fatal — the page still
// renders — but a panel whose fetch failed says so rather than rendering the
// empty state it would show for a genuinely quiet town (gt-egq9).

func TestConvoyHandler_FetchConvoysError(t *testing.T) {
	mock := &MockConvoyFetcher{
		Error: errFetchFailed,
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Fetch errors are non-fatal - the dashboard still renders
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d (fetch errors are non-fatal)", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Convoys unavailable") {
		t.Error("Panel should say convoys are unavailable when the fetch failed")
	}
	if !strings.Contains(body, errFetchFailed.Error()) {
		t.Error("Notice should name the reason the query failed")
	}
	if strings.Contains(body, "No active convoys") {
		t.Error("A failed fetch must not render the empty state — that is the bug")
	}
}

// Integration tests for merge queue rendering

func TestConvoyHandler_MergeQueueRendering(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
		MergeQueue: []MergeQueueRow{
			{
				HasPR:      true,
				Number:     123,
				Repo:       "roxas",
				Title:      "Fix authentication bug",
				URL:        "https://github.com/test/repo/pull/123",
				CIStatus:   "pass",
				Mergeable:  "ready",
				ColorClass: "mq-green",
			},
			{
				HasPR:      true,
				Number:     456,
				Repo:       "gastown",
				Title:      "Add dashboard feature",
				URL:        "https://github.com/test/repo/pull/456",
				CIStatus:   "pending",
				Mergeable:  "pending",
				ColorClass: "mq-yellow",
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Check merge queue section header
	if !strings.Contains(body, "Merge Queue") {
		t.Error("Response should contain merge queue section header")
	}

	// Check PR numbers are rendered
	if !strings.Contains(body, "#123") {
		t.Error("Response should contain PR #123")
	}
	if !strings.Contains(body, "#456") {
		t.Error("Response should contain PR #456")
	}

	// Check repo names
	if !strings.Contains(body, "roxas") {
		t.Error("Response should contain repo 'roxas'")
	}

	// Check CI status badges (now display text, not classes)
	if !strings.Contains(body, "CI Pass") {
		t.Error("Response should contain 'CI Pass' text for passing PR")
	}
	if !strings.Contains(body, "CI Running") {
		t.Error("Response should contain 'CI Running' text for pending PR")
	}
}

// TestConvoyHandler_MergeQueueFailedRigNotice asserts a partial queue is never
// rendered as if it were complete (gt-4qp).
func TestConvoyHandler_MergeQueueFailedRigNotice(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
		MergeQueue: []MergeQueueRow{
			{ID: "gt-mr-1", Repo: "gastown", Title: "MR", Branch: "b", Target: "main"},
			{ID: "gt-mr-2", Repo: "gastown", Title: "MR", Branch: "b", Target: "main"},
		},
		MergeQueueFailedRigs: []string{"beads"},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "truncation-notice") {
		t.Error("A failed rig should render an incomplete-count notice")
	}
	if !strings.Contains(body, "unavailable for: beads") {
		t.Error("Notice should name the failed rig")
	}
	// The count must read as a floor, not a total.
	if !strings.Contains(body, `<span class="count">2+</span>`) {
		t.Error("Incomplete count should render with a '+' suffix")
	}
}

// TestConvoyHandler_EscalationsUnavailableNotice asserts an unreadable
// escalation panel renders as unreadable — not as a calm, empty town (gt-edty).
func TestConvoyHandler_EscalationsUnavailableNotice(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys:          []ConvoyRow{},
		EscalationsError: errors.New("listing escalations: connection refused"),
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "Escalations unavailable") {
		t.Error("Panel should say escalations are unavailable")
	}
	if !strings.Contains(body, "connection refused") {
		t.Error("Notice should name the reason the query failed")
	}
	if strings.Contains(body, "<p>No escalations</p>") {
		t.Error("An unreadable panel must not render the empty state")
	}
	// The count must not read as a confident zero.
	if !strings.Contains(body, `<span class="count count-alert">?</span>`) {
		t.Error("Unknown escalation count should render as '?', not a number")
	}
	// The banner is the part an operator reads at a glance.
	if strings.Contains(body, "All clear") {
		t.Error("A dashboard that cannot see escalations is not 'All clear'")
	}
	if !strings.Contains(body, "escalations unreadable") {
		t.Error("Summary alerts should flag the unreadable escalation panel")
	}
}

// TestConvoyHandler_NoEscalationsRendersEmptyState is the control for
// TestConvoyHandler_EscalationsUnavailableNotice: when the query SUCCEEDS and
// finds nothing, zero still means zero and the town is still all clear.
func TestConvoyHandler_NoEscalationsRendersEmptyState(t *testing.T) {
	mock := &MockConvoyFetcher{Convoys: []ConvoyRow{}}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "<p>No escalations</p>") {
		t.Error("A successful empty query should render the empty state")
	}
	if strings.Contains(body, "Escalations unavailable") {
		t.Error("A successful query must not render the unavailable notice")
	}
	if !strings.Contains(body, "All clear") {
		t.Error("A quiet, readable town should still render 'All clear'")
	}
}

// TestConvoyHandler_UnreadablePanelsSaySo covers the six panels that used to
// answer a failed fetch with (nil, nil): each must name the reason it has no
// rows instead of rendering the empty state a quiet town produces (gt-egq9).
func TestConvoyHandler_UnreadablePanelsSaySo(t *testing.T) {
	// A distinct reason per panel: one shared message could not show that each
	// panel reports its OWN failure rather than a page-wide banner.
	mock := &MockConvoyFetcher{
		Error:         errors.New("listing convoys: backing off after 3 consecutive failures"),
		WorkersError:  errors.New("listing worker sessions: tmux timed out after 2s"),
		QueuesError:   errors.New("listing queues: connection refused"),
		SessionsError: errors.New("listing tmux sessions: tmux timed out after 2s"),
		MayorError:    errors.New("checking mayor session: tmux timed out after 2s"),
		ActivityError: errors.New("reading event log: permission denied"),
		// The Dogs panel reached the handler with its error already computed and
		// dropped it here, so it was the last of the nine sites still rendering
		// a failure as an empty kennel (gt-1jrl).
		DogsError: errors.New("reading kennel: permission denied"),
		// These four outlived that fix: their fetchers returned an error, and the
		// handler logged it and rendered the panel from the zero value anyway, so
		// the same lie survived one layer up (gt-xw1t).
		MailError:       errors.New("listing mail: bd timed out after 30s"),
		RigsError:       errors.New("loading rigs config: parsing config: unexpected end of JSON input"),
		HealthError:     errors.New("reading deacon heartbeat: permission denied"),
		MergeQueueError: errors.New("loading rigs config: parsing config: unexpected end of JSON input"),
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()

	for _, want := range []string{
		"Convoys unavailable: listing convoys: backing off after 3 consecutive failures",
		"Polecats unavailable: listing worker sessions: tmux timed out after 2s",
		"Queues unavailable: listing queues: connection refused",
		"Sessions unavailable: listing tmux sessions: tmux timed out after 2s",
		"Mayor status unavailable: checking mayor session: tmux timed out after 2s",
		"Activity unavailable: reading event log: permission denied",
		"Dogs unavailable: reading kennel: permission denied",
		"Mail unavailable: listing mail: bd timed out after 30s",
		"Rigs unavailable: loading rigs config: parsing config: unexpected end of JSON input",
		"Merge queue unavailable: loading rigs config: parsing config: unexpected end of JSON input",
		"heartbeat unreadable",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panel should name why it could not read: missing %q", want)
		}
	}

	// The empty states are the exact renders the bug produced. None may appear.
	for _, unwanted := range []string{
		"No active convoys",
		"<p>No polecats</p>",
		"No active sessions",
		"No recent activity",
		"No dogs in kennel",
		"No mail traffic",
		"No rigs configured",
		"Merge queue empty",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("an unreadable panel must not render its empty state: found %q", unwanted)
		}
	}

	// "Detached" is a claim about tmux, and tmux could not be reached.
	if strings.Contains(body, "badge-muted\">Detached") {
		t.Error("the Mayor banner must not claim 'Detached' when tmux could not be asked")
	}

	// The banner is what an operator reads at a glance.
	if strings.Contains(body, "All clear") {
		t.Error("a dashboard that cannot see ten panels is not 'All clear'")
	}
	for _, want := range []string{
		"polecats unreadable",
		"convoys unreadable",
		"mail unreadable",
		"rigs unreadable",
		"health unreadable",
		"merge queue unreadable",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("summary alerts should flag the unreadable panel: missing %q", want)
		}
	}
}

// TestConvoyHandler_QuietTownRendersEmptyStates is the control for
// TestConvoyHandler_UnreadablePanelsSaySo. Without it, a caveat rendered on
// every load would pass the test above and carry no information at all.
func TestConvoyHandler_QuietTownRendersEmptyStates(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
		// A town that answers about its health is the control for the banner's
		// "?" stat: without a readable heartbeat here, an indicator that never
		// renders would pass the failure test above.
		Health: &HealthRow{DeaconHeartbeat: "Jan 2, 3:04 PM", HeartbeatFresh: true},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()

	for _, want := range []string{
		"No active convoys",
		"<p>No polecats</p>",
		"No active sessions",
		"No recent activity",
		"No dogs in kennel",
		"No mail traffic",
		"No rigs configured",
		"Merge queue empty",
		"💓 Jan 2, 3:04 PM",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("a readable, quiet panel should render its empty state: missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"Convoys unavailable",
		"Polecats unavailable",
		"Queues unavailable",
		"Sessions unavailable",
		"Mayor status unavailable",
		"Activity unavailable",
		"Dogs unavailable",
		"Mail unavailable",
		"Rigs unavailable",
		"Merge queue unavailable",
		"heartbeat unreadable",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("a successful query must not render an unavailable notice: found %q", unwanted)
		}
	}
	if !strings.Contains(body, "All clear") {
		t.Error("a quiet, readable town should still render 'All clear'")
	}
}

// TestConvoyHandler_MergeQueuePROptional asserts PR link and CI status render
// only for MR beads that actually recorded a PR — PR data is enrichment, and
// a row exists on the strength of its MR bead alone.
func TestConvoyHandler_MergeQueuePROptional(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
		MergeQueue: []MergeQueueRow{
			{
				ID: "gt-mr-nopr", Repo: "gastown", Title: "No PR yet",
				Branch: "polecat/chrome/gt-9", Target: "main", Worker: "chrome",
				ColorClass: "mq-green",
			},
			{
				ID: "gt-mr-pr", Repo: "gastown", Title: "Has a PR",
				Branch: "polecat/nux/gt-8", Target: "main", Worker: "nux",
				HasPR: true, Number: 42, URL: "https://github.com/o/r/pull/42",
				CIStatus: "pass", Mergeable: "ready", ColorClass: "mq-green",
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()

	// MR bead IDs are the primary identity of a queue row.
	for _, id := range []string{"gt-mr-nopr", "gt-mr-pr"} {
		if !strings.Contains(body, id) {
			t.Errorf("Response should contain MR bead ID %q", id)
		}
	}
	if !strings.Contains(body, "#42") {
		t.Error("Row with a recorded PR should show its PR number")
	}
	if !strings.Contains(body, "CI Pass") {
		t.Error("Row with a recorded PR should show CI status")
	}
	// The PR detail view is driven by data-pr-url; a PR-less MR must not offer it.
	if strings.Count(body, "data-pr-url") != 1 {
		t.Error("Only the MR with a recorded PR should carry data-pr-url")
	}
	// Branch/target routing is queue information, not PR information.
	if !strings.Contains(body, "polecat/chrome/gt-9") {
		t.Error("Row should show the branch being merged")
	}
}

func TestConvoyHandler_EmptyMergeQueue(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys:    []ConvoyRow{},
		MergeQueue: []MergeQueueRow{},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	body := w.Body.String()

	// Should show empty state for merge queue
	if !strings.Contains(body, "Merge queue empty") {
		t.Error("Response should show empty merge queue message")
	}
}

// Integration tests for polecat workers rendering

func TestConvoyHandler_PolecatWorkersRendering(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
		Workers: []WorkerRow{
			{
				Name:         "dag",
				Rig:          "roxas",
				SessionID:    "gt-roxas-dag",
				LastActivity: activity.Calculate(time.Now().Add(-30 * time.Second)),
				StatusHint:   "Running tests...",
			},
			{
				Name:         "nux",
				Rig:          "roxas",
				SessionID:    "gt-roxas-nux",
				LastActivity: activity.Calculate(time.Now().Add(-5 * time.Minute)),
				StatusHint:   "Waiting for input",
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Check polecat section header
	if !strings.Contains(body, "Polecats") {
		t.Error("Response should contain polecats section header")
	}

	// Check polecat names
	if !strings.Contains(body, "dag") {
		t.Error("Response should contain polecat 'dag'")
	}
	if !strings.Contains(body, "nux") {
		t.Error("Response should contain polecat 'nux'")
	}

	// Check rig names
	if !strings.Contains(body, "roxas") {
		t.Error("Response should contain rig 'roxas'")
	}

	// Note: StatusHint is no longer displayed in the simplified dashboard view

	// Check activity colors (dag should be green, nux should be yellow/red)
	if !strings.Contains(body, "activity-green") {
		t.Error("Response should contain activity-green for recent activity")
	}
}

// Integration tests for work status rendering

func TestConvoyHandler_WorkStatusRendering(t *testing.T) {
	tests := []struct {
		name           string
		workStatus     string
		wantClass      string
		wantStatusText string
	}{
		{"complete status", "complete", "badge-green", "✓"},
		{"active status", "active", "badge-green", "Active"},
		{"stale status", "stale", "badge-yellow", "Stale"},
		{"stuck status", "stuck", "badge-red", "Stuck"},
		{"waiting status", "waiting", "badge-muted", "Wait"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &MockConvoyFetcher{
				Convoys: []ConvoyRow{
					{
						ID:           "hq-cv-test",
						Title:        "Test Convoy",
						Status:       "open",
						WorkStatus:   tt.workStatus,
						Progress:     "1/2",
						Completed:    1,
						Total:        2,
						LastActivity: activity.Calculate(time.Now()),
					},
				},
			}

			handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
			if err != nil {
				t.Fatalf("NewConvoyHandler() error = %v", err)
			}

			req := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			body := w.Body.String()

			// Check work status class is applied
			if !strings.Contains(body, tt.wantClass) {
				t.Errorf("Response should contain class %q for work status %q", tt.wantClass, tt.workStatus)
			}

			// Check work status text is displayed
			if !strings.Contains(body, tt.wantStatusText) {
				t.Errorf("Response should contain status text %q", tt.wantStatusText)
			}
		})
	}
}

// Integration tests for progress bar rendering

func TestConvoyHandler_ProgressBarRendering(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{
			{
				ID:           "hq-cv-progress",
				Title:        "Progress Test",
				Status:       "open",
				WorkStatus:   "active",
				Progress:     "3/4",
				Completed:    3,
				Total:        4,
				ProgressPct:  75,
				LastActivity: activity.Calculate(time.Now()),
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	body := w.Body.String()

	// Check progress text
	if !strings.Contains(body, "3/4") {
		t.Error("Response should contain progress '3/4'")
	}

	// Check progress bar element
	if !strings.Contains(body, "progress-bar") {
		t.Error("Response should contain progress-bar class")
	}

	// Check progress fill with percentage (75%)
	if !strings.Contains(body, "progress-fill") {
		t.Error("Response should contain progress-fill class")
	}
	if !strings.Contains(body, "width: 75%") {
		t.Error("Response should contain 75% width for 3/4 progress")
	}
}

// Integration test for HTMX auto-refresh

func TestConvoyHandler_HTMXAutoRefresh(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	body := w.Body.String()

	// Check htmx attributes for auto-refresh
	if !strings.Contains(body, "hx-get") {
		t.Error("Response should contain hx-get attribute for HTMX")
	}
	if !strings.Contains(body, "hx-trigger") {
		t.Error("Response should contain hx-trigger attribute for HTMX")
	}
	if !strings.Contains(body, "sse:dashboard-update") {
		t.Error("Response should contain 'sse:dashboard-update' trigger for SSE")
	}
	if !strings.Contains(body, "every 30s") {
		t.Error("Response should contain 'every 30s' polling fallback")
	}
}

// Integration test for full dashboard with all sections

func TestConvoyHandler_FullDashboard(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{
			{
				ID:           "hq-cv-full",
				Title:        "Full Test Convoy",
				Status:       "open",
				WorkStatus:   "active",
				Progress:     "2/3",
				Completed:    2,
				Total:        3,
				LastActivity: activity.Calculate(time.Now().Add(-1 * time.Minute)),
			},
		},
		MergeQueue: []MergeQueueRow{
			{
				HasPR:      true,
				Number:     789,
				Repo:       "testrig",
				Title:      "Test PR",
				CIStatus:   "pass",
				Mergeable:  "ready",
				ColorClass: "mq-green",
			},
		},
		Workers: []WorkerRow{
			{
				Name:         "worker1",
				Rig:          "testrig",
				SessionID:    "gt-testrig-worker1",
				LastActivity: activity.Calculate(time.Now()),
				StatusHint:   "Working...",
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Verify all three sections are present
	if !strings.Contains(body, "Convoys") {
		t.Error("Response should contain convoy section")
	}
	if !strings.Contains(body, "hq-cv-full") {
		t.Error("Response should contain convoy data")
	}
	if !strings.Contains(body, "Merge Queue") {
		t.Error("Response should contain merge queue section")
	}
	if !strings.Contains(body, "#789") {
		t.Error("Response should contain PR data")
	}
	if !strings.Contains(body, "Polecats") {
		t.Error("Response should contain polecats section")
	}
	if !strings.Contains(body, "worker1") {
		t.Error("Response should contain polecat data")
	}
}

// =============================================================================
// End-to-End Tests with httptest.Server
// =============================================================================

// TestE2E_Server_FullDashboard tests the full dashboard using a real HTTP server.
func TestE2E_Server_FullDashboard(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{
			{
				ID:           "hq-cv-e2e",
				Title:        "E2E Test Convoy",
				Status:       "open",
				WorkStatus:   "active",
				Progress:     "2/4",
				Completed:    2,
				Total:        4,
				LastActivity: activity.Calculate(time.Now().Add(-45 * time.Second)),
			},
		},
		MergeQueue: []MergeQueueRow{
			{
				HasPR:      true,
				Number:     101,
				Repo:       "roxas",
				Title:      "E2E Test PR",
				URL:        "https://github.com/test/roxas/pull/101",
				CIStatus:   "pass",
				Mergeable:  "ready",
				ColorClass: "mq-green",
			},
		},
		Workers: []WorkerRow{
			{
				Name:         "furiosa",
				Rig:          "roxas",
				SessionID:    "gt-roxas-furiosa",
				LastActivity: activity.Calculate(time.Now().Add(-30 * time.Second)),
				StatusHint:   "Running E2E tests",
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	// Create a real HTTP server
	server := httptest.NewServer(handler)
	defer server.Close()

	// Make HTTP request to the server
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	// Verify status code
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Verify content type
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", contentType)
	}

	// Read and verify body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}
	body := string(bodyBytes)

	// Verify all three sections render
	checks := []struct {
		name    string
		content string
	}{
		{"Convoy section", "Convoys"},
		{"Convoy ID", "hq-cv-e2e"},
		{"Convoy progress", "2/4"},
		{"Merge queue section", "Merge Queue"},
		{"PR number", "#101"},
		{"PR repo", "roxas"},
		{"Polecats section", "Polecats"},
		{"Polecat name", "furiosa"},
		{"HTMX SSE trigger", `hx-trigger="sse:dashboard-update`},
	}

	for _, check := range checks {
		if !strings.Contains(body, check.content) {
			t.Errorf("%s: should contain %q", check.name, check.content)
		}
	}
}

// TestE2E_Server_ActivityColors tests activity color rendering via HTTP server.
func TestE2E_Server_ActivityColors(t *testing.T) {
	tests := []struct {
		name      string
		age       time.Duration
		wantClass string
	}{
		{"green for recent", 20 * time.Second, "activity-green"},
		{"yellow for stale", 6 * time.Minute, "activity-yellow"},
		{"red for stuck", 11 * time.Minute, "activity-red"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &MockConvoyFetcher{
				Workers: []WorkerRow{
					{
						Name:         "test-worker",
						Rig:          "test-rig",
						SessionID:    "gt-test-rig-test-worker",
						LastActivity: activity.Calculate(time.Now().Add(-tt.age)),
						StatusHint:   "Testing",
					},
				},
			}

			handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
			if err != nil {
				t.Fatalf("NewConvoyHandler() error = %v", err)
			}

			server := httptest.NewServer(handler)
			defer server.Close()

			resp, err := http.Get(server.URL)
			if err != nil {
				t.Fatalf("HTTP GET failed: %v", err)
			}
			defer resp.Body.Close()

			bodyBytes, _ := io.ReadAll(resp.Body)
			body := string(bodyBytes)

			if !strings.Contains(body, tt.wantClass) {
				t.Errorf("Should contain activity class %q for age %v", tt.wantClass, tt.age)
			}
		})
	}
}

// TestE2E_Server_MergeQueueEmpty tests that empty merge queue shows message.
func TestE2E_Server_MergeQueueEmpty(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys:    []ConvoyRow{},
		MergeQueue: []MergeQueueRow{},
		Workers:    []WorkerRow{},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	// Section header should always be visible
	if !strings.Contains(body, "Merge Queue") {
		t.Error("Merge queue section should always be visible")
	}

	// Empty state message
	if !strings.Contains(body, "Merge queue empty") {
		t.Error("Should show 'Merge queue empty' when empty")
	}
}

// TestE2E_Server_MergeQueueStatuses tests all PR status combinations.
func TestE2E_Server_MergeQueueStatuses(t *testing.T) {
	tests := []struct {
		name       string
		ciStatus   string
		mergeable  string
		colorClass string
		wantCI     string
		wantMerge  string
	}{
		{"green when ready", "pass", "ready", "mq-green", "CI Pass", "Ready"},
		{"red when CI fails", "fail", "ready", "mq-red", "CI Fail", "Ready"},
		{"red when conflict", "pass", "conflict", "mq-red", "CI Pass", "Conflict"},
		{"yellow when pending", "pending", "pending", "mq-yellow", "CI Running", "Pending"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &MockConvoyFetcher{
				MergeQueue: []MergeQueueRow{
					{
						HasPR:      true,
						Number:     42,
						Repo:       "test",
						Title:      "Test PR",
						URL:        "https://github.com/test/test/pull/42",
						CIStatus:   tt.ciStatus,
						Mergeable:  tt.mergeable,
						ColorClass: tt.colorClass,
					},
				},
			}

			handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
			if err != nil {
				t.Fatalf("NewConvoyHandler() error = %v", err)
			}

			server := httptest.NewServer(handler)
			defer server.Close()

			resp, err := http.Get(server.URL)
			if err != nil {
				t.Fatalf("HTTP GET failed: %v", err)
			}
			defer resp.Body.Close()

			bodyBytes, _ := io.ReadAll(resp.Body)
			body := string(bodyBytes)

			if !strings.Contains(body, tt.colorClass) {
				t.Errorf("Should contain row class %q", tt.colorClass)
			}
			if !strings.Contains(body, tt.wantCI) {
				t.Errorf("Should contain CI text %q", tt.wantCI)
			}
			if !strings.Contains(body, tt.wantMerge) {
				t.Errorf("Should contain merge text %q", tt.wantMerge)
			}
		})
	}
}

// TestE2E_Server_HTMLStructure validates HTML document structure.
func TestE2E_Server_HTMLStructure(t *testing.T) {
	mock := &MockConvoyFetcher{Convoys: []ConvoyRow{}}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	// Validate HTML structure
	elements := []string{
		"<!DOCTYPE html>",
		"<html",
		"<head>",
		"<title>Gas Town Control Center</title>",
		"htmx.org",
		"<body>",
		"</body>",
		"</html>",
	}

	for _, elem := range elements {
		if !strings.Contains(body, elem) {
			t.Errorf("Should contain HTML element %q", elem)
		}
	}

	// Validate CSS file is linked (CSS variables are now in external file)
	if !strings.Contains(body, `href="/static/dashboard.css"`) {
		t.Error("Should link to external CSS file dashboard.css")
	}
}

// TestE2E_Server_RefineryInPolecats tests that refinery appears in polecat workers.
func TestE2E_Server_RefineryInPolecats(t *testing.T) {
	mock := &MockConvoyFetcher{
		Workers: []WorkerRow{
			{
				Name:         "refinery",
				Rig:          "roxas",
				SessionID:    "gt-roxas-refinery",
				LastActivity: activity.Calculate(time.Now().Add(-10 * time.Second)),
				StatusHint:   "Idle - Waiting for PRs",
			},
			{
				Name:         "dag",
				Rig:          "roxas",
				SessionID:    "gt-roxas-dag",
				LastActivity: activity.Calculate(time.Now().Add(-30 * time.Second)),
				StatusHint:   "Working on feature",
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	// Refinery should appear in polecat workers
	if !strings.Contains(body, "refinery") {
		t.Error("Refinery should appear in polecat workers section")
	}
	// Note: StatusHint is no longer displayed in the simplified dashboard view

	// Regular polecats should also appear
	if !strings.Contains(body, "dag") {
		t.Error("Regular polecat 'dag' should appear")
	}
}

// Test that merge queue and polecat errors are non-fatal

type MockConvoyFetcherWithErrors struct {
	Convoys         []ConvoyRow
	MergeQueueError error
	WorkersError    error
}

func (m *MockConvoyFetcherWithErrors) FetchConvoys() ([]ConvoyRow, error) {
	return m.Convoys, nil
}

func (m *MockConvoyFetcherWithErrors) FetchMergeQueue() (MergeQueueResult, error) {
	return MergeQueueResult{}, m.MergeQueueError
}

func (m *MockConvoyFetcherWithErrors) FetchWorkers() (StoreResult[WorkerRow], error) {
	return StoreResult[WorkerRow]{}, m.WorkersError
}

func (m *MockConvoyFetcherWithErrors) FetchMail() ([]MailRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchRigs() ([]RigRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchDogs() ([]DogRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchEscalations() ([]EscalationRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchHealth() (*HealthRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchQueues() ([]QueueRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchSessions() ([]SessionRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchHooks() (StoreResult[HookRow], error) {
	return StoreResult[HookRow]{}, nil
}

func (m *MockConvoyFetcherWithErrors) FetchMayor() (*MayorStatus, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchIssues() (StoreResult[IssueRow], error) {
	return StoreResult[IssueRow]{}, nil
}

func (m *MockConvoyFetcherWithErrors) FetchActivity() ([]ActivityRow, error) {
	return nil, nil
}

// TestConvoyHandler_TemplateErrorReturns500 verifies that template execution errors
// return a proper 500 status code, not 200 (which would happen if we wrote directly
// to the ResponseWriter and it failed mid-execution).
func TestConvoyHandler_TemplateErrorReturns500(t *testing.T) {
	// Create a template that writes some output, then fails
	failingFuncCalled := false
	tmpl := template.Must(template.New("convoy.html").Funcs(template.FuncMap{
		"failAfterOutput": func() (string, error) {
			failingFuncCalled = true
			return "", errors.New("intentional template error")
		},
	}).Parse(`<!DOCTYPE html><html>{{failAfterOutput}}</html>`))

	// Create handler with the failing template
	handler := &ConvoyHandler{
		fetcher:      &MockConvoyFetcher{Convoys: []ConvoyRow{}},
		template:     tmpl,
		fetchTimeout: 5 * time.Second,
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if !failingFuncCalled {
		t.Fatal("Template function was not called")
	}

	// The key assertion: status should be 500, not 200
	// If we write directly to ResponseWriter and it fails mid-execution,
	// headers (with 200) are already sent, so http.Error can't change it
	if w.Code != http.StatusInternalServerError {
		t.Errorf("Status = %d, want %d (template error should return 500, not 200)", w.Code, http.StatusInternalServerError)
	}

	// Error message should be in the body, not partial template content
	body := w.Body.String()
	if !strings.Contains(body, "Failed to render template") {
		t.Errorf("Response should contain error message, got: %q", body)
	}
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("Error response should not contain partial template output")
	}
}

// TestConvoyHandler_CachePreventsDuplicateFetches verifies that rapid requests
// reuse the cached response instead of spawning fresh fetches (GH#2618).
func TestConvoyHandler_CachePreventsDuplicateFetches(t *testing.T) {
	fetchCount := 0
	mock := &CountingMockFetcher{
		inner:      &MockConvoyFetcher{Convoys: []ConvoyRow{{ID: "hq-cv-cache", Title: "Cache Test", Status: "open"}}},
		fetchCount: &fetchCount,
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}
	handler.cacheTTL = 5 * time.Second // Explicit TTL for test

	// First request — should trigger a fetch
	req1 := httptest.NewRequest("GET", "/", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("First request status = %d, want 200", w1.Code)
	}
	if fetchCount != 1 {
		t.Fatalf("After first request, fetchCount = %d, want 1", fetchCount)
	}

	// Second request — should use cache (within TTL)
	req2 := httptest.NewRequest("GET", "/", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("Second request status = %d, want 200", w2.Code)
	}
	if fetchCount != 1 {
		t.Errorf("After second request, fetchCount = %d, want 1 (should use cache)", fetchCount)
	}

	// Verify both responses contain the same content
	if w1.Body.String() != w2.Body.String() {
		t.Error("Cached response should match original response")
	}
}

// TestConvoyHandler_CacheBypassOnExpand verifies that ?expand= requests bypass
// the normal response cache but have their own per-panel expand cache (GH#3117).
func TestConvoyHandler_CacheBypassOnExpand(t *testing.T) {
	fetchCount := 0
	mock := &CountingMockFetcher{
		inner:      &MockConvoyFetcher{Convoys: []ConvoyRow{{ID: "hq-cv-expand", Title: "Expand Test", Status: "open"}}},
		fetchCount: &fetchCount,
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}
	handler.cacheTTL = 5 * time.Second

	// Normal request to populate cache
	req1 := httptest.NewRequest("GET", "/", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if fetchCount != 1 {
		t.Fatalf("After first request, fetchCount = %d, want 1", fetchCount)
	}

	// First expand request — should bypass normal cache (different template)
	req2 := httptest.NewRequest("GET", "/?expand=convoys", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if fetchCount != 2 {
		t.Errorf("First expand request fetchCount = %d, want 2 (should bypass normal cache)", fetchCount)
	}

	// Second identical expand request — should hit expand cache (GH#3117)
	req3 := httptest.NewRequest("GET", "/?expand=convoys", nil)
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)

	if fetchCount != 2 {
		t.Errorf("Second expand request fetchCount = %d, want 2 (should hit expand cache)", fetchCount)
	}
}

func TestConvoyHandler_ExpandCachePreventsRepeatedFetchConvoysErrors(t *testing.T) {
	fetchCount := 0
	mock := &CountingMockFetcher{
		inner:      &MockConvoyFetcher{Error: errFetchFailed},
		fetchCount: &fetchCount,
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}
	handler.cacheTTL = 5 * time.Second

	req1 := httptest.NewRequest("GET", "/?expand=convoys", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("First expand request status = %d, want 200", w1.Code)
	}
	if fetchCount != 1 {
		t.Fatalf("After first expand request, fetchCount = %d, want 1", fetchCount)
	}

	req2 := httptest.NewRequest("GET", "/?expand=convoys", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("Second expand request status = %d, want 200", w2.Code)
	}
	if fetchCount != 1 {
		t.Fatalf("Second expand request should use cached error response; fetchCount = %d, want 1", fetchCount)
	}
	if w1.Body.String() != w2.Body.String() {
		t.Fatal("Cached expand error response should match original response")
	}
}

// CountingMockFetcher wraps a ConvoyFetcher and counts FetchConvoys calls.
type CountingMockFetcher struct {
	inner      ConvoyFetcher
	fetchCount *int
}

func (m *CountingMockFetcher) FetchConvoys() ([]ConvoyRow, error) {
	*m.fetchCount++
	return m.inner.FetchConvoys()
}
func (m *CountingMockFetcher) FetchMergeQueue() (MergeQueueResult, error) {
	return m.inner.FetchMergeQueue()
}
func (m *CountingMockFetcher) FetchWorkers() (StoreResult[WorkerRow], error) {
	return m.inner.FetchWorkers()
}
func (m *CountingMockFetcher) FetchMail() ([]MailRow, error) { return m.inner.FetchMail() }
func (m *CountingMockFetcher) FetchRigs() ([]RigRow, error)  { return m.inner.FetchRigs() }
func (m *CountingMockFetcher) FetchDogs() ([]DogRow, error)  { return m.inner.FetchDogs() }
func (m *CountingMockFetcher) FetchEscalations() ([]EscalationRow, error) {
	return m.inner.FetchEscalations()
}
func (m *CountingMockFetcher) FetchHealth() (*HealthRow, error)     { return m.inner.FetchHealth() }
func (m *CountingMockFetcher) FetchQueues() ([]QueueRow, error)     { return m.inner.FetchQueues() }
func (m *CountingMockFetcher) FetchSessions() ([]SessionRow, error) { return m.inner.FetchSessions() }
func (m *CountingMockFetcher) FetchHooks() (StoreResult[HookRow], error) {
	return m.inner.FetchHooks()
}
func (m *CountingMockFetcher) FetchMayor() (*MayorStatus, error) { return m.inner.FetchMayor() }
func (m *CountingMockFetcher) FetchIssues() (StoreResult[IssueRow], error) {
	return m.inner.FetchIssues()
}
func (m *CountingMockFetcher) FetchActivity() ([]ActivityRow, error) {
	return m.inner.FetchActivity()
}

func TestConvoyHandler_NonFatalErrors(t *testing.T) {
	mock := &MockConvoyFetcherWithErrors{
		Convoys: []ConvoyRow{
			{ID: "hq-cv-test", Title: "Test", Status: "open", WorkStatus: "active"},
		},
		MergeQueueError: errFetchFailed,
		WorkersError:    errFetchFailed,
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Should still return OK even if merge queue and polecats fail
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d (non-fatal errors should not fail request)", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Convoys should still render
	if !strings.Contains(body, "hq-cv-test") {
		t.Error("Response should contain convoy data even when other fetches fail")
	}
}

// TestFetchCircuitBreaker verifies exponential backoff on consecutive failures (GH#3117).
func TestFetchCircuitBreaker(t *testing.T) {
	var cb fetchCircuitBreaker

	// Initially allowed
	if !cb.allow() {
		t.Fatal("circuit breaker should allow first attempt")
	}
	if cb.allow() {
		t.Fatal("circuit breaker should reserve the in-flight attempt")
	}

	// Record a failure — should block immediate retry
	cb.recordFailure()
	if cb.allow() {
		t.Fatal("circuit breaker should block after first failure (within backoff)")
	}

	// Verify failure count and backoff are set
	cb.mu.Lock()
	if cb.failures != 1 {
		t.Errorf("failures = %d, want 1", cb.failures)
	}
	if cb.backoff < 5*time.Second {
		t.Errorf("backoff = %v, want >= 5s", cb.backoff)
	}
	cb.mu.Unlock()

	// Record success — should reset
	cb.recordSuccess()
	if !cb.allow() {
		t.Fatal("circuit breaker should allow after success reset")
	}
	cb.mu.Lock()
	if cb.failures != 0 {
		t.Errorf("failures after reset = %d, want 0", cb.failures)
	}
	cb.mu.Unlock()

	// Multiple failures should increase backoff
	cb.recordFailure()
	cb.mu.Lock()
	backoff1 := cb.backoff
	cb.mu.Unlock()

	cb.recordFailure()
	cb.mu.Lock()
	backoff2 := cb.backoff
	cb.mu.Unlock()

	if backoff2 <= backoff1 {
		t.Errorf("backoff should increase: first=%v, second=%v", backoff1, backoff2)
	}

	// Backoff should cap at maxBackoff
	for i := 0; i < 20; i++ {
		cb.recordFailure()
	}
	cb.mu.Lock()
	if cb.backoff > maxBackoff {
		t.Errorf("backoff %v exceeds maxBackoff %v", cb.backoff, maxBackoff)
	}
	cb.mu.Unlock()
}

// TestConvoyHandler_PartialStoreNotices asserts the Work and Hooks panels admit
// an incomplete union instead of rendering a store that could not be read as a
// store that held nothing (gt-c332).
func TestConvoyHandler_PartialStoreNotices(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys:            []ConvoyRow{},
		Issues:             []IssueRow{{ID: "gt-1", Title: "work", Priority: 2}},
		IssuesFailedStores: []string{"beads"},
		Hooks:              []HookRow{{ID: "gt-2", Title: "hooked", Agent: "nux"}},
		HooksFailedStores:  []string{"duly_noted"},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{
		"Work backlog partial results (unreadable: beads)",
		"Hooks partial results (unreadable: duly_noted)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Response should name the unreadable store: missing %q", want)
		}
	}
	// Both counts must read as floors, not totals.
	if !strings.Contains(body, `<span class="count">1+</span>`) {
		t.Error("Incomplete Work count should render with a '+' suffix")
	}
	if !strings.Contains(body, `1+</span>`) {
		t.Error("Incomplete Hooks count should render with a '+' suffix")
	}
}

// TestConvoyHandler_WorkCountIsTheBacklogNotThePage asserts the Work number is
// the size of the backlog, not the size of the list under it (gt-eolg). A page
// that renders a capped slice and counts the slice reports the cap: the number
// stops falling when work is closed, which is exactly what an operator reads it
// for.
func TestConvoyHandler_WorkCountIsTheBacklogNotThePage(t *testing.T) {
	const backlog = issuesDisplayLimit + 43

	issues := make([]IssueRow, 0, backlog)
	for i := 0; i < backlog; i++ {
		issues = append(issues, IssueRow{ID: fmt.Sprintf("gt-%d", i), Title: "work", Priority: 3})
	}

	mock := &MockConvoyFetcher{Convoys: []ConvoyRow{}, Issues: issues}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()

	if want := fmt.Sprintf(`<span class="count">%d</span>`, backlog); !strings.Contains(body, want) {
		t.Errorf("Work count should be the whole backlog: missing %q", want)
	}
	if unwanted := fmt.Sprintf(`<span class="count">%d</span>`, issuesDisplayLimit); strings.Contains(body, unwanted) {
		t.Errorf("Work count rendered the page size %d instead of the backlog %d", issuesDisplayLimit, backlog)
	}
	if want := fmt.Sprintf("Showing %d of %d", issuesDisplayLimit, backlog); !strings.Contains(body, want) {
		t.Errorf("A shortened list must say how much of the backlog it is showing: missing %q", want)
	}
	// The rows really are capped — the count being honest is not a licence to
	// render the whole backlog into the page.
	if got := strings.Count(body, `class="issue-row`); got != issuesDisplayLimit {
		t.Errorf("rendered %d issue rows, want %d", got, issuesDisplayLimit)
	}
	// A complete union is not a floor, so no "+" and no partial-results notice.
	if strings.Contains(body, fmt.Sprintf(`<span class="count">%d+</span>`, backlog)) {
		t.Error("a complete backlog must not render as a floor")
	}
}

// TestConvoyHandler_UnreadableUnionPanelsSaySo covers the last two panels of
// the swallowed-error family (gt-8nhx). The Hooks and Work panels have no error
// to render: they union every beads store, so every store failing separately
// still returns a result, and the panel used to describe a total blackout as a
// count of zero with a "count is incomplete" footnote under it.
//
// Zero rows with no store read is not a floor. It is no number at all, and the
// panel, its header count and the banner stat must each say so.
func TestConvoyHandler_UnreadableUnionPanelsSaySo(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
		// No rows, and no store that could have supplied them.
		HooksFailedStores:  []string{"town", "gastown"},
		IssuesFailedStores: []string{"town", "gastown"},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()

	for _, want := range []string{
		"Hooks unavailable: no store could be read (unreadable: town, gastown)",
		"Work backlog unavailable: no store could be read (unreadable: town, gastown)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panel should name why it could not read: missing %q", want)
		}
	}

	// The empty states are the exact renders the bug produced.
	for _, unwanted := range []string{"No hooked work", "No work items"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("an unreadable panel must not render its empty state: found %q", unwanted)
		}
	}

	// "0+" is the floor render, and a floor drawn from no sources is a claim
	// the panel cannot make.
	if strings.Contains(body, "0+</span>") {
		t.Error("an unreadable union must not render its count as a floor of zero")
	}

	// The banner is what an operator reads at a glance.
	if strings.Contains(body, "All clear") {
		t.Error("a dashboard that cannot read hooks or work is not 'All clear'")
	}
	for _, want := range []string{"🪝 hooks unreadable", "📋 work unreadable"} {
		if !strings.Contains(body, want) {
			t.Errorf("summary alerts should flag the unreadable panel: missing %q", want)
		}
	}
	// The banner stat is the other half: an operator who never opens the panel
	// reads the town off these two numbers.
	for _, label := range []string{"🪝 Hooks", "📋 Work"} {
		if got := bannerStat(t, body, label); got != "?" {
			t.Errorf("banner stat %q = %q, want %q for a count that could not be read", label, got, "?")
		}
	}
}

// TestConvoyHandler_UnionQueryFailureSaysSo covers the union panels' OTHER
// failure channel: the query failing outright, before any StoreResult exists.
// There are no failed stores to name in that case, so a panel that reads only
// the StoreResult finds "" — indistinguishable from every store answering — and
// renders "No hooked work" for a query that never ran (gt-xw1t).
func TestConvoyHandler_UnionQueryFailureSaysSo(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys:     []ConvoyRow{},
		HooksError:  errors.New("resolving stores: reading rigs config: permission denied"),
		IssuesError: errors.New("resolving stores: reading rigs config: permission denied"),
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()

	for _, want := range []string{
		"Hooks unavailable: resolving stores: reading rigs config: permission denied",
		"Work backlog unavailable: resolving stores: reading rigs config: permission denied",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panel should name why it could not read: missing %q", want)
		}
	}
	for _, unwanted := range []string{"No hooked work", "No work items"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("a failed union query must not render its empty state: found %q", unwanted)
		}
	}
	for _, label := range []string{"🪝 Hooks", "📋 Work"} {
		if got := bannerStat(t, body, label); got != "?" {
			t.Errorf("banner stat %q = %q, want %q for a query that never ran", label, got, "?")
		}
	}
	if strings.Contains(body, "All clear") {
		t.Error("a dashboard whose hook and work queries failed is not 'All clear'")
	}
}

// bannerStat returns the value the summary banner renders above the given stat
// label, so a test can assert on the number an operator actually sees rather
// than on a substring that any other panel's zero would satisfy.
func bannerStat(t *testing.T, body, label string) string {
	t.Helper()

	pattern := regexp.MustCompile(`stat-value">([^<]*)</span>\s*<span class="stat-label">` + regexp.QuoteMeta(label))
	match := pattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("no banner stat found for label %q", label)
	}
	return match[1]
}

// TestConvoyHandler_ReadableUnionPanelsRenderTheirZero is the control for
// TestConvoyHandler_UnreadableUnionPanelsSaySo: a town whose stores answered
// and had nothing to say still gets its empty states and its zeroes. Without
// it, a notice rendered unconditionally would pass the test above.
func TestConvoyHandler_ReadableUnionPanelsRenderTheirZero(t *testing.T) {
	mock := &MockConvoyFetcher{Convoys: []ConvoyRow{}}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{"No hooked work", "No work items"} {
		if !strings.Contains(body, want) {
			t.Errorf("a readable, quiet panel should render its empty state: missing %q", want)
		}
	}
	for _, unwanted := range []string{"Hooks unavailable", "Work backlog unavailable"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("a successful union must not render an unavailable notice: found %q", unwanted)
		}
	}
	if !strings.Contains(body, "All clear") {
		t.Error("a quiet, readable town should still render 'All clear'")
	}
	for _, label := range []string{"🪝 Hooks", "📋 Work"} {
		if got := bannerStat(t, body, label); got != "0" {
			t.Errorf("banner stat %q = %q, want a plain %q when the stores answered", label, got, "0")
		}
	}
}

// TestConvoyHandler_WorkersPartialNotice asserts the Polecats panel says it
// could not read a store rather than leaving the workers it lost their issue for
// looking idle (gt-lf1n).
//
// The worker COUNT stays a total here — tmux supplies the rows, and tmux
// answered — so unlike the Work and Hooks panels this notice must not claim the
// count is incomplete. What is incomplete is the "Working On" column.
func TestConvoyHandler_WorkersPartialNotice(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
		Workers: []WorkerRow{
			{Name: "nux", Rig: "gastown", WorkStatus: "idle", AgentType: "polecat"},
		},
		WorkersFailedStores: []string{"gastown"},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if want := "Assigned work partial results (unreadable: gastown)"; !strings.Contains(body, want) {
		t.Errorf("Response should name the unreadable store: missing %q", want)
	}
	if !strings.Contains(body, "status may read idle for workers that are not") {
		t.Error("Notice should say what the failure does to the panel, not just that it happened")
	}
}

// TestConvoyHandler_WorkersCompleteHasNoNotice is the control: a readable town
// must not carry the caveat, or the caveat stops meaning anything.
func TestConvoyHandler_WorkersCompleteHasNoNotice(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
		Workers: []WorkerRow{
			{Name: "nux", Rig: "gastown", IssueID: "gt-1", WorkStatus: "working", AgentType: "polecat"},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if strings.Contains(w.Body.String(), "Assigned work partial results") {
		t.Error("A fully readable town should render no assigned-work caveat")
	}
}

// TestConvoyHandler_WorkersFailureChannelsStayDistinct pins the one thing the
// Polecats panel is easiest to get wrong on a rebase: it has TWO failure
// channels, from two different sources, and neither can stand in for the other.
//
//	WorkersUnavailable  tmux could not be asked, so there is no worker list
//	WorkersWarning      the list is complete, but a rig store could not say
//	                    which bead its workers are carrying
//
// Collapsing them — rendering one message for both, or dropping one on the
// grounds that the other already "says it failed" — puts the panel back to
// claiming something it does not know. gt-egq9 was rejected once for exactly
// this collision, so the distinctness is asserted rather than assumed.
func TestConvoyHandler_WorkersFailureChannelsStayDistinct(t *testing.T) {
	const (
		unavailable = "Polecats unavailable: listing worker sessions"
		partial     = "Assigned work partial results"
	)

	tests := []struct {
		name       string
		mock       *MockConvoyFetcher
		wantSubstr []string
		notSubstr  []string
	}{
		{
			name: "tmux failed, stores fine",
			mock: &MockConvoyFetcher{
				Convoys:      []ConvoyRow{},
				WorkersError: errors.New("listing worker sessions: tmux timed out after 2s"),
			},
			wantSubstr: []string{unavailable},
			// The stores answered. Saying the assigned-bead lookup was partial
			// would invent a second failure out of the first one.
			notSubstr: []string{partial},
		},
		{
			name: "tmux fine, one store failed",
			mock: &MockConvoyFetcher{
				Convoys: []ConvoyRow{},
				Workers: []WorkerRow{
					{Name: "nux", Rig: "gastown", WorkStatus: "idle", AgentType: "polecat"},
				},
				WorkersFailedStores: []string{"gastown"},
			},
			wantSubstr: []string{partial},
			// tmux answered and the rows are real. "Polecats unavailable" would
			// hide a panel the operator can still read.
			notSubstr: []string{unavailable},
		},
		{
			name: "both failed",
			mock: &MockConvoyFetcher{
				Convoys:             []ConvoyRow{},
				WorkersError:        errors.New("listing worker sessions: tmux timed out after 2s"),
				WorkersFailedStores: []string{"gastown"},
			},
			// With no worker list there is no "Working On" column to caveat, so
			// the unreadable notice is the whole story. The live fetcher cannot
			// even produce this pair — it returns an empty result on the tmux
			// error — but the precedence is pinned so a future change to that
			// cannot silently drop the reason the panel is blank.
			wantSubstr: []string{unavailable},
			notSubstr:  []string{"<p>No polecats</p>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := NewConvoyHandler(tt.mock, 8*time.Second, "test-token")
			if err != nil {
				t.Fatalf("NewConvoyHandler() error = %v", err)
			}

			req := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			body := w.Body.String()
			for _, want := range tt.wantSubstr {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, unwanted := range tt.notSubstr {
				if strings.Contains(body, unwanted) {
					t.Errorf("one channel was rendered as the other: found %q", unwanted)
				}
			}
		})
	}
}

// TestConvoyHandler_BannerMarksTruncatedCountsAsFloors is the acceptance test
// for gt-skzk.2. The *Unavailable family already made a FAILED read look
// different from a real zero; a read that SUCCEEDS but comes back short had no
// render at all, and reached the operator as an ordinary number.
//
// The two subtests are one test: the marker has to appear for a store that
// filled its allowance AND stay away for one that did not. A marker that is
// always on is decoration, and decoration on a monitoring surface is worse than
// nothing because it trains the reader to ignore it.
func TestConvoyHandler_BannerMarksTruncatedCountsAsFloors(t *testing.T) {
	rows := func(n int) ([]IssueRow, []HookRow) {
		issues := make([]IssueRow, 0, n)
		hooks := make([]HookRow, 0, n)
		for i := 0; i < n; i++ {
			issues = append(issues, IssueRow{ID: fmt.Sprintf("gt-i%d", i), Title: "work", Priority: 3})
			hooks = append(hooks, HookRow{ID: fmt.Sprintf("gt-h%d", i), Title: "hooked", Agent: "nux"})
		}
		return issues, hooks
	}

	t.Run("store above the cap renders a floor", func(t *testing.T) {
		issues, hooks := rows(3)
		mock := &MockConvoyFetcher{
			Convoys:               []ConvoyRow{},
			Issues:                issues,
			IssuesReadStores:      []string{"town", "gastown"},
			IssuesTruncatedStores: []string{"town"},
			Hooks:                 hooks,
			HooksReadStores:       []string{"town", "gastown"},
			HooksTruncatedStores:  []string{"town"},
		}

		body := renderDashboard(t, mock)

		for _, want := range []string{
			`<span class="stat-value">3+</span>`,
			"work count is a floor",
			"hooks count is a floor",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("a capped read must not render as a measured count: missing %q", want)
			}
		}
		// The banner's verdict has to move with it. "All clear" over a number
		// the dashboard knows is short is the exact false assurance the
		// *Unavailable flags were added to prevent, one degree quieter.
		if strings.Contains(body, "✓ All clear") {
			t.Error(`banner said "All clear" over counts it knows are floors`)
		}
	})

	t.Run("store below the cap renders a bare count", func(t *testing.T) {
		issues, hooks := rows(3)
		mock := &MockConvoyFetcher{
			Convoys:          []ConvoyRow{},
			Issues:           issues,
			IssuesReadStores: []string{"town", "gastown"},
			Hooks:            hooks,
			HooksReadStores:  []string{"town", "gastown"},
		}

		body := renderDashboard(t, mock)

		if !strings.Contains(body, `<span class="stat-value">3</span>`) {
			t.Error("a complete read must render as a plain number")
		}
		for _, unwanted := range []string{
			`<span class="stat-value">3+</span>`,
			"count is a floor",
		} {
			if strings.Contains(body, unwanted) {
				t.Errorf("marker fired on a complete read, so it discriminates nothing: found %q", unwanted)
			}
		}
	})
}

// TestConvoyHandler_UnreadableBeatsPartial pins the precedence between the two
// markers. Every store failing makes the union both unreadable AND partial, and
// rendering both would print "0+" — a floor drawn from nothing read — beside an
// alert saying the count is a floor. "?" says strictly more, so it wins alone.
func TestConvoyHandler_UnreadableBeatsPartial(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys:            []ConvoyRow{},
		IssuesFailedStores: []string{"town", "gastown"},
		HooksFailedStores:  []string{"town", "gastown"},
	}

	body := renderDashboard(t, mock)

	if !strings.Contains(body, `<span class="stat-value">?</span>`) {
		t.Error("a stat with no source at all must render ?")
	}
	for _, unwanted := range []string{
		`<span class="stat-value">0+</span>`,
		"count is a floor",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("unreadable and partial both rendered for the same stat: found %q", unwanted)
		}
	}
}

// TestConvoyHandler_BannerFlagsPartialMergeQueue covers the third acceptance
// case. The merge queue prints no number in the banner, so a rig short of the
// count could only reach the operator as an alert — and it did not, which meant
// a partially-read queue rendered "✓ All clear". The panel's own "+" is not a
// substitute: an operator whose banner is green does not scroll down to it.
func TestConvoyHandler_BannerFlagsPartialMergeQueue(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys:              []ConvoyRow{},
		MergeQueue:           []MergeQueueRow{{ID: "gt-mr1", Title: "a merge", Repo: "gastown"}},
		MergeQueueFailedRigs: []string{"roxas"},
		IssuesReadStores:     []string{"town"},
		HooksReadStores:      []string{"town"},
	}

	body := renderDashboard(t, mock)

	if !strings.Contains(body, "merge queue count is a floor") {
		t.Error("a queue short by a rig must reach the banner, not just the panel")
	}
	if strings.Contains(body, "✓ All clear") {
		t.Error(`banner said "All clear" over a merge queue it only partly read`)
	}
	// The two merge-queue failure scales must stay distinct: one rig short is
	// not the same claim as no rig list at all.
	if strings.Contains(body, "merge queue unreadable") {
		t.Error("a partial queue was reported as an unreadable one")
	}
}

// TestConvoyHandler_MailCountSaysWhenItIsCapped covers the one cap on this page
// that is deliberate. FetchMail asks for the most recent mailFetchLimit
// messages because the panel is "recent traffic" and the town root held 386
// message beads when this was measured. That is a good reason for the cap and
// no reason at all to print its result as a total.
func TestConvoyHandler_MailCountSaysWhenItIsCapped(t *testing.T) {
	full := make([]MailRow, 0, mailFetchLimit)
	for i := 0; i < mailFetchLimit; i++ {
		full = append(full, MailRow{ID: fmt.Sprintf("hq-m%d", i), Subject: "hello", From: "mayor"})
	}

	body := renderDashboard(t, &MockConvoyFetcher{Convoys: []ConvoyRow{}, Mail: full})
	if !strings.Contains(body, fmt.Sprintf(`id="mail-count">%d+<`, mailFetchLimit)) {
		t.Errorf("a mail query that came back exactly full must render as a floor")
	}
	if !strings.Contains(body, "the count is a floor") {
		t.Error("the mail panel should say the list is the most recent slice, not all of it")
	}

	short := full[:mailFetchLimit-1]
	body = renderDashboard(t, &MockConvoyFetcher{Convoys: []ConvoyRow{}, Mail: short})
	if !strings.Contains(body, fmt.Sprintf(`id="mail-count">%d<`, mailFetchLimit-1)) {
		t.Error("a mail query that came back short of the cap is a complete answer")
	}
}

// renderDashboard serves one request against the mock and returns the HTML.
func renderDashboard(t *testing.T, mock *MockConvoyFetcher) string {
	t.Helper()

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	return w.Body.String()
}
