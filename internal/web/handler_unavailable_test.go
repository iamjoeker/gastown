package web

// An empty panel and an unreadable panel used to render identically. During a
// Dolt or tmux outage that made the dashboard calmest exactly when the town was
// sickest. These tests pin both halves: a panel that failed says so, and a panel
// that succeeded with nothing in it still reads as a confident zero (gt-1jrl).

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// renderDashboard renders the dashboard once and returns the HTML.
func renderDashboard(t *testing.T, mock *MockConvoyFetcher) string {
	t.Helper()

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	return w.Body.String()
}

func TestConvoyHandler_EveryUnreadablePanelSaysSo(t *testing.T) {
	mock := &MockConvoyFetcher{
		Error:         errors.New("listing convoys: connection refused"),
		WorkersError:  errors.New("listing worker sessions: lost server"),
		SessionsError: errors.New("listing tmux sessions: lost server"),
		DogsError:     errors.New("reading kennel: permission denied"),
		QueuesError:   errors.New("listing queues: connection refused"),
		ActivityError: errors.New("reading event log: input/output error"),
	}

	body := renderDashboard(t, mock)

	// Each panel names itself and the reason, so the operator knows which
	// numbers on the page mean nothing and what to go fix.
	for _, notice := range []string{
		"Convoys unavailable: listing convoys: connection refused",
		"Polecats unavailable: listing worker sessions: lost server",
		"Sessions unavailable: listing tmux sessions: lost server",
		"Dogs unavailable: reading kennel: permission denied",
		"Queues unavailable: listing queues: connection refused",
		"Activity unavailable: reading event log: input/output error",
	} {
		if !strings.Contains(body, notice) {
			t.Errorf("missing unavailable notice: %q", notice)
		}
	}

	// The empty states belong to successful queries that found nothing. A
	// failed query must not borrow them.
	for _, emptyState := range []string{
		"No active convoys",
		"<p>No polecats</p>",
		"No active sessions",
		"No dogs in kennel",
		"No recent activity",
	} {
		if strings.Contains(body, emptyState) {
			t.Errorf("unreadable panel rendered the empty state %q", emptyState)
		}
	}

	// The banner is what an operator reads at a glance.
	if strings.Contains(body, "All clear") {
		t.Error("a dashboard that cannot read six panels is not 'All clear'")
	}
	for _, panel := range []string{"convoys", "polecats", "sessions", "dogs", "queues", "activity"} {
		if !strings.Contains(body, panel) {
			t.Errorf("banner should name the unreadable panel %q", panel)
		}
	}

	// Unknown counts render as "?" — never as a confident zero. One per failed
	// panel: convoys, polecats, sessions, dogs, queues, activity.
	if got := strings.Count(body, `<span class="count">?</span>`); got != 6 {
		t.Errorf("panel counts rendering as '?' = %d, want 6", got)
	}
	// The summary tiles are counted from the same rows, so they are unknown too.
	if got := strings.Count(body, `<span class="stat-value">?</span>`); got != 2 {
		t.Errorf("summary stats rendering as '?' = %d, want 2 (polecats, convoys)", got)
	}
}

func TestConvoyHandler_QuietTownStillReadsAsZero(t *testing.T) {
	// The control for the test above: with every query succeeding and finding
	// nothing, zero still means zero. Without this, a fetcher that always
	// errored would pass.
	body := renderDashboard(t, &MockConvoyFetcher{})

	for _, emptyState := range []string{
		"No active convoys",
		"<p>No polecats</p>",
		"No active sessions",
		"No dogs in kennel",
		"No recent activity",
	} {
		if !strings.Contains(body, emptyState) {
			t.Errorf("a successful empty query should render %q", emptyState)
		}
	}
	if strings.Contains(body, "unavailable:") {
		t.Error("a successful query must not render an unavailable notice")
	}
	if strings.Contains(body, "This is NOT an empty panel") {
		t.Error("a successful query must not claim the panel is unreadable")
	}
	if !strings.Contains(body, "All clear") {
		t.Error("a quiet, readable town should still render 'All clear'")
	}
}

func TestConvoyHandler_UnreadableQueuePanelIsStillShown(t *testing.T) {
	// The queue panel is hidden when there are no queues. A panel that
	// disappears on failure is the same silence as an empty one, so a failed
	// query has to bring it back.
	body := renderDashboard(t, &MockConvoyFetcher{
		QueuesError: errors.New("listing queues: connection refused"),
	})

	if !strings.Contains(body, "📋 Queues") {
		t.Error("the queue panel must be rendered when its query failed")
	}
	if !strings.Contains(body, "Queues unavailable") {
		t.Error("the queue panel should say why it is empty")
	}
}
