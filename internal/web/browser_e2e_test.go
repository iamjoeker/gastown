//go:build browser

package web

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/steveyegge/gastown/internal/activity"
)

// =============================================================================
// Browser-based E2E Tests using Rod
//
// These tests launch a real browser (Chromium) to verify the convoy dashboard
// works correctly in an actual browser environment.
//
// Run with: go test -tags=browser -v ./internal/web -run TestBrowser
//
// By default, tests run headless. Set BROWSER_VISIBLE=1 to watch:
//   BROWSER_VISIBLE=1 go test -tags=browser -v ./internal/web -run TestBrowser
//
// =============================================================================

// browserTestConfig holds configuration for browser tests
type browserTestConfig struct {
	headless bool
	slowMo   time.Duration
}

// getBrowserConfig returns test configuration based on environment
func getBrowserConfig() browserTestConfig {
	cfg := browserTestConfig{
		headless: true,
		slowMo:   0,
	}

	if os.Getenv("BROWSER_VISIBLE") == "1" {
		cfg.headless = false
		cfg.slowMo = 300 * time.Millisecond
	}

	return cfg
}

// launchBrowser creates a browser instance with the given configuration.
func launchBrowser(cfg browserTestConfig) (*rod.Browser, func()) {
	l := launcher.New().
		NoSandbox(true).
		Headless(cfg.headless)

	if !cfg.headless {
		l = l.Devtools(false)
	}

	u := l.MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()

	if !cfg.headless {
		browser = browser.SlowMotion(cfg.slowMo)
	}

	cleanup := func() {
		browser.MustClose()
		l.Cleanup()
	}

	return browser, cleanup
}

// mockFetcher implements ConvoyFetcher for testing. It embeds the full mock
// from handler_test.go so it keeps satisfying the interface as methods are
// added, and only overrides the convoy rows these tests care about.
type mockFetcher struct {
	MockConvoyFetcher
	convoys []ConvoyRow
}

func (m *mockFetcher) FetchConvoys() ([]ConvoyRow, error) {
	return m.convoys, nil
}

// TestBrowser_ConvoyListLoads tests that the convoy list page loads correctly
func TestBrowser_ConvoyListLoads(t *testing.T) {
	// Setup test server with mock data
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{
			{
				ID:           "hq-cv-abc",
				Title:        "Feature X",
				Status:       "open",
				Progress:     "2/5",
				Completed:    2,
				Total:        5,
				LastActivity: activity.Calculate(time.Now().Add(-1 * time.Minute)),
			},
			{
				ID:           "hq-cv-def",
				Title:        "Bugfix Y",
				Status:       "closed",
				Progress:     "3/3",
				Completed:    3,
				Total:        3,
				LastActivity: activity.Calculate(time.Now().Add(-10 * time.Minute)),
			},
		},
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	// Verify page title
	title := page.MustElement("title").MustText()
	if !strings.Contains(title, "Gas Town") {
		t.Fatalf("Expected title to contain 'Gas Town', got: %s", title)
	}

	// Verify convoy IDs are displayed
	bodyText := page.MustElement("body").MustText()
	if !strings.Contains(bodyText, "hq-cv-abc") {
		t.Error("Expected convoy ID hq-cv-abc in page")
	}
	if !strings.Contains(bodyText, "hq-cv-def") {
		t.Error("Expected convoy ID hq-cv-def in page")
	}

	// Verify titles are displayed
	if !strings.Contains(bodyText, "Feature X") {
		t.Error("Expected title 'Feature X' in page")
	}
	if !strings.Contains(bodyText, "Bugfix Y") {
		t.Error("Expected title 'Bugfix Y' in page")
	}

	t.Log("PASSED: Convoy list loads correctly")
}

// TestBrowser_LastActivityColors tests that activity colors are displayed correctly
func TestBrowser_LastActivityColors(t *testing.T) {
	// Setup test server with convoys at different activity ages
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{
			{
				ID:           "hq-cv-green",
				Title:        "Active Work",
				Status:       "open",
				LastActivity: activity.Calculate(time.Now().Add(-1 * time.Minute)), // Green: <5min
			},
			{
				ID:           "hq-cv-yellow",
				Title:        "Stale Work",
				Status:       "open",
				LastActivity: activity.Calculate(time.Now().Add(-6 * time.Minute)), // Yellow: 5-10min
			},
			{
				ID:           "hq-cv-red",
				Title:        "Stuck Work",
				Status:       "open",
				LastActivity: activity.Calculate(time.Now().Add(-11 * time.Minute)), // Red: >=10min
			},
		},
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	// Check for activity color classes in the HTML
	html := page.MustHTML()

	if !strings.Contains(html, "activity-green") {
		t.Error("Expected activity-green class for recent activity")
	}
	if !strings.Contains(html, "activity-yellow") {
		t.Error("Expected activity-yellow class for stale activity")
	}
	if !strings.Contains(html, "activity-red") {
		t.Error("Expected activity-red class for stuck activity")
	}

	t.Log("PASSED: Activity colors display correctly")
}

// TestBrowser_HtmxAutoRefresh tests that htmx auto-refresh attributes are present
func TestBrowser_HtmxAutoRefresh(t *testing.T) {
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{
			{
				ID:     "hq-cv-test",
				Title:  "Test Convoy",
				Status: "open",
			},
		},
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	// Check for htmx attributes
	html := page.MustHTML()

	if !strings.Contains(html, "hx-get") {
		t.Error("Expected hx-get attribute for auto-refresh")
	}
	if !strings.Contains(html, "hx-trigger") {
		t.Error("Expected hx-trigger attribute for auto-refresh")
	}
	if !strings.Contains(html, "every 30s") {
		t.Error("Expected 'every 30s' trigger for auto-refresh")
	}

	// Verify htmx library is loaded
	if !strings.Contains(html, "htmx.org") {
		t.Error("Expected htmx library to be loaded")
	}

	t.Log("PASSED: htmx auto-refresh attributes present")
}

// TestBrowser_EmptyState tests the empty state when no convoys exist
func TestBrowser_EmptyState(t *testing.T) {
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{}, // Empty convoy list
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	// Check for empty state message
	bodyText := page.MustElement("body").MustText()

	if !strings.Contains(bodyText, "No convoys") {
		t.Errorf("Expected 'No convoys' empty state message, got: %s", bodyText[:min(len(bodyText), 500)])
	}

	// Verify help text is shown
	if !strings.Contains(bodyText, "gt convoy create") {
		t.Error("Expected help text with 'gt convoy create' command")
	}

	t.Log("PASSED: Empty state displays correctly")
}

// TestBrowser_StatusIndicators tests open/closed status indicators
func TestBrowser_StatusIndicators(t *testing.T) {
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{
			{
				ID:     "hq-cv-open",
				Title:  "Open Convoy",
				Status: "open",
			},
			{
				ID:     "hq-cv-closed",
				Title:  "Closed Convoy",
				Status: "closed",
			},
		},
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	html := page.MustHTML()

	// Check for status classes
	if !strings.Contains(html, "status-open") {
		t.Error("Expected status-open class for open convoy")
	}
	if !strings.Contains(html, "status-closed") {
		t.Error("Expected status-closed class for closed convoy")
	}

	t.Log("PASSED: Status indicators display correctly")
}

// TestBrowser_ProgressDisplay tests progress bar rendering
func TestBrowser_ProgressDisplay(t *testing.T) {
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{
			{
				ID:        "hq-cv-progress",
				Title:     "Progress Convoy",
				Status:    "open",
				Progress:  "3/7",
				Completed: 3,
				Total:     7,
			},
		},
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	bodyText := page.MustElement("body").MustText()

	// Verify progress text
	if !strings.Contains(bodyText, "3/7") {
		t.Errorf("Expected progress '3/7' in page, got: %s", bodyText[:min(len(bodyText), 500)])
	}

	// Verify progress bar elements exist
	html := page.MustHTML()
	if !strings.Contains(html, "progress-bar") {
		t.Error("Expected progress-bar class in page")
	}
	if !strings.Contains(html, "progress-fill") {
		t.Error("Expected progress-fill class in page")
	}

	t.Log("PASSED: Progress display works correctly")
}

// =============================================================================
// gt-lrj: a paused panel must not silently drop updates, and must not look
// identical to a live one.
//
// These tests serve the real embedded dashboard.js against a controllable SSE
// endpoint, so the actual shipped script drives the assertions.
// =============================================================================

// pausableSSEServer wires the real convoy page and the real embedded static
// assets to an /api/events stream this test can push events into on demand.
type pausableSSEServer struct {
	mux    *http.ServeMux
	events chan string
}

func newPausableSSEServer(t *testing.T, fetcher ConvoyFetcher) *pausableSSEServer {
	t.Helper()

	convoyHandler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		t.Fatalf("Failed to open embedded static files: %v", err)
	}

	s := &pausableSSEServer{
		mux:    http.NewServeMux(),
		events: make(chan string, 16),
	}

	s.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	s.mux.HandleFunc("/api/events", s.serveEvents)
	s.mux.Handle("/", convoyHandler)

	return s
}

func (s *pausableSSEServer) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	fmt.Fprint(w, "event: connected\ndata: ok\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case hash := <-s.events:
			fmt.Fprintf(w, "event: dashboard-update\ndata: %s\n\n", hash)
			flusher.Flush()
		}
	}
}

// pushUpdate emits one dashboard-update event to the connected client.
func (s *pausableSSEServer) pushUpdate(hash string) {
	s.events <- hash
}

// waitFor polls fn until it returns true or the deadline passes.
func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func deferredCount(page *rod.Page) int {
	return page.MustEval(`() => window.gtDeferredUpdateCount()`).Int()
}

func bodyClass(page *rod.Page) string {
	return page.MustEval(`() => document.body.className`).String()
}

// TestBrowser_PausedPanelDefersUpdates verifies that an SSE update arriving
// while a panel is expanded is recorded rather than discarded, and is flushed
// when the panel collapses.
func TestBrowser_PausedPanelDefersUpdates(t *testing.T) {
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{{
			ID:           "hq-cv-abc",
			Title:        "Feature X",
			Status:       "open",
			Progress:     "2/5",
			Completed:    2,
			Total:        5,
			LastActivity: activity.Calculate(time.Now().Add(-1 * time.Minute)),
		}},
	}

	srv := newPausableSSEServer(t, fetcher)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	browser, cleanup := launchBrowser(getBrowserConfig())
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()
	page.MustWaitLoad()

	// The script must have installed its accessor and counter.
	waitFor(t, "dashboard.js to initialize", func() bool {
		return page.MustEval(`() => typeof window.gtDeferredUpdateCount === 'function'`).Bool()
	})

	if got := deferredCount(page); got != 0 {
		t.Fatalf("expected 0 deferred updates before pausing, got %d", got)
	}

	// Expand a panel: this is the interaction that used to freeze the panel.
	page.MustElement(".expand-btn").MustClick()

	waitFor(t, "refresh to pause", func() bool {
		return page.MustEval(`() => window.pauseRefresh === true`).Bool()
	})

	// An update arrives while the operator is reading the expanded panel.
	srv.pushUpdate("hash-1")

	waitFor(t, "update to be deferred rather than dropped", func() bool {
		return deferredCount(page) == 1
	})

	// A second one, to prove they accumulate rather than overwrite.
	srv.pushUpdate("hash-2")
	waitFor(t, "second update to be deferred", func() bool {
		return deferredCount(page) == 2
	})

	// Collapsing resumes, which must flush the owed updates.
	page.MustEval(`() => { window.pauseRefresh = false; }`)

	waitFor(t, "deferred updates to flush on resume", func() bool {
		return deferredCount(page) == 0
	})

	if page.MustEval(`() => window.pauseRefresh`).Bool() {
		t.Error("expected refresh to be live after resume")
	}

	t.Log("PASSED: paused panel defers updates and flushes them on resume")
}

// TestBrowser_PausedPanelIsVisiblyDistinct verifies the epistemic half of the
// fix: while paused, the UI says so — a stale panel cannot look live.
func TestBrowser_PausedPanelIsVisiblyDistinct(t *testing.T) {
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{{
			ID:           "hq-cv-abc",
			Title:        "Feature X",
			Status:       "open",
			Progress:     "2/5",
			Completed:    2,
			Total:        5,
			LastActivity: activity.Calculate(time.Now().Add(-1 * time.Minute)),
		}},
	}

	srv := newPausableSSEServer(t, fetcher)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	browser, cleanup := launchBrowser(getBrowserConfig())
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()
	page.MustWaitLoad()

	waitFor(t, "dashboard.js to initialize", func() bool {
		return page.MustEval(`() => typeof window.gtDeferredUpdateCount === 'function'`).Bool()
	})

	// While live, nothing should claim staleness.
	if cls := bodyClass(page); strings.Contains(cls, "refresh-paused") {
		t.Errorf("expected no refresh-paused class while live, got %q", cls)
	}
	badgeVisible := func() bool {
		return page.MustEval(`() => {
			var b = document.getElementById('staleness-badge');
			return !!b && !b.hidden;
		}`).Bool()
	}
	if badgeVisible() {
		t.Error("expected staleness badge to be hidden while live")
	}

	// Pause by expanding, exactly as an operator reading the panel would.
	page.MustElement(".expand-btn").MustClick()

	waitFor(t, "staleness badge to appear", badgeVisible)

	waitFor(t, "paused body class", func() bool {
		return strings.Contains(bodyClass(page), "refresh-paused")
	})

	badgeText := page.MustEval(`() => document.getElementById('staleness-badge').textContent`).String()
	if !strings.Contains(badgeText, "Paused") {
		t.Errorf("expected badge to say it is paused, got %q", badgeText)
	}
	if !strings.Contains(badgeText, "as of") {
		t.Errorf("expected badge to carry an as-of time, got %q", badgeText)
	}

	// Once updates are actually owed, the badge must escalate and say how many.
	srv.pushUpdate("hash-1")

	waitFor(t, "badge to report pending updates", func() bool {
		txt := page.MustEval(`() => document.getElementById('staleness-badge').textContent`).String()
		return strings.Contains(txt, "1 update pending")
	})

	waitFor(t, "updates-pending body class", func() bool {
		return strings.Contains(bodyClass(page), "updates-pending")
	})

	if !page.MustEval(`() => document.getElementById('staleness-badge').classList.contains('staleness-badge-stale')`).Bool() {
		t.Error("expected badge to carry the stale modifier once updates are owed")
	}

	t.Log("PASSED: paused panel is visibly distinct from a live one")
}
