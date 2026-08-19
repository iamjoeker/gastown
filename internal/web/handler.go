package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

//go:embed static
var staticFiles embed.FS

// ConvoyFetcher defines the interface for fetching convoy data.
type ConvoyFetcher interface {
	FetchConvoys() ([]ConvoyRow, error)
	FetchMergeQueue() (MergeQueueResult, error)
	FetchWorkers() (StoreResult[WorkerRow], error)
	FetchMail() ([]MailRow, error)
	FetchRigs() ([]RigRow, error)
	FetchDogs() ([]DogRow, error)
	FetchEscalations() ([]EscalationRow, error)
	FetchHealth() (*HealthRow, error)
	FetchQueues() ([]QueueRow, error)
	FetchSessions() ([]SessionRow, error)
	FetchHooks() (StoreResult[HookRow], error)
	FetchMayor() (*MayorStatus, error)
	FetchIssues() (StoreResult[IssueRow], error)
	FetchActivity() ([]ActivityRow, error)
}

// expandCacheEntry holds a cached expanded-view response.
type expandCacheEntry struct {
	body []byte
	time time.Time
}

// ConvoyHandler handles HTTP requests for the convoy dashboard.
type ConvoyHandler struct {
	fetcher      ConvoyFetcher
	template     *template.Template
	fetchTimeout time.Duration
	csrfToken    string

	// Response cache: prevents cascading bd process storms when multiple
	// browser tabs or htmx auto-refresh requests arrive faster than fetches
	// complete. See GH#2618.
	cacheMu    sync.Mutex
	cacheBody  []byte
	cacheTime  time.Time
	cacheTTL   time.Duration
	cacheInUse sync.Mutex // serializes concurrent fetches (only one runs at a time)

	// Expanded-view cache: expanded views previously bypassed the response
	// cache entirely, allowing process storms via repeated ?expand= requests.
	// See GH#3117.
	expandCacheMu sync.Mutex
	expandCache   map[string]expandCacheEntry
}

// defaultCacheTTL is the minimum interval between full dashboard fetches.
// Requests arriving within this window get the cached response.
const defaultCacheTTL = 10 * time.Second

// NewConvoyHandler creates a new convoy handler with the given fetcher, fetch timeout, and CSRF token.
func NewConvoyHandler(fetcher ConvoyFetcher, fetchTimeout time.Duration, csrfToken string) (*ConvoyHandler, error) {
	tmpl, err := LoadTemplates()
	if err != nil {
		return nil, err
	}

	return &ConvoyHandler{
		fetcher:      fetcher,
		template:     tmpl,
		fetchTimeout: fetchTimeout,
		csrfToken:    csrfToken,
		cacheTTL:     defaultCacheTTL,
	}, nil
}

// ServeHTTP handles GET / requests and renders the convoy dashboard.
// Uses a response cache to prevent bd process storms from overlapping
// requests (htmx auto-refresh, multiple tabs). Only one fetch cycle
// runs at a time; concurrent requests get the cached response.
func (h *ConvoyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check for expand parameter — expanded views render a different template
	// variant but are still cached to prevent process storms (GH#3117).
	expandPanel := r.URL.Query().Get("expand")

	// Fast path: serve from cache if fresh.
	if expandPanel == "" {
		h.cacheMu.Lock()
		if len(h.cacheBody) > 0 && time.Since(h.cacheTime) < h.cacheTTL {
			body := h.cacheBody
			h.cacheMu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if _, err := w.Write(body); err != nil {
				log.Printf("dashboard: cached response write failed: %v", err)
			}
			return
		}
		h.cacheMu.Unlock()
	} else {
		// Expanded views: check per-panel cache to prevent process storms
		h.expandCacheMu.Lock()
		if entry, ok := h.expandCache[expandPanel]; ok && time.Since(entry.time) < h.cacheTTL {
			body := entry.body
			h.expandCacheMu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if _, err := w.Write(body); err != nil {
				log.Printf("dashboard: cached expand response write failed: %v", err)
			}
			return
		}
		h.expandCacheMu.Unlock()
	}

	// Serialize fetch cycles: only one request triggers a full fetch at a time.
	// Others wait and will likely hit the cache when this one finishes.
	h.cacheInUse.Lock()
	defer h.cacheInUse.Unlock()

	// Double-check cache after acquiring lock (another request may have populated it).
	if expandPanel == "" {
		h.cacheMu.Lock()
		if len(h.cacheBody) > 0 && time.Since(h.cacheTime) < h.cacheTTL {
			body := h.cacheBody
			h.cacheMu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if _, err := w.Write(body); err != nil {
				log.Printf("dashboard: cached response write failed: %v", err)
			}
			return
		}
		h.cacheMu.Unlock()
	} else {
		h.expandCacheMu.Lock()
		if entry, ok := h.expandCache[expandPanel]; ok && time.Since(entry.time) < h.cacheTTL {
			body := entry.body
			h.expandCacheMu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if _, err := w.Write(body); err != nil {
				log.Printf("dashboard: cached expand response write failed: %v", err)
			}
			return
		}
		h.expandCacheMu.Unlock()
	}

	body := h.fetchAndRender(r, expandPanel)
	if body == nil {
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
		return
	}

	// Update cache
	if expandPanel == "" {
		h.cacheMu.Lock()
		h.cacheBody = body
		h.cacheTime = time.Now()
		h.cacheMu.Unlock()
	} else {
		h.expandCacheMu.Lock()
		if h.expandCache == nil {
			h.expandCache = make(map[string]expandCacheEntry)
		}
		h.expandCache[expandPanel] = expandCacheEntry{body: body, time: time.Now()}
		h.expandCacheMu.Unlock()
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(body); err != nil {
		log.Printf("dashboard: response write failed: %v", err)
	}
}

// fetchAndRender runs all 14 fetchers in parallel and renders the template.
// Returns the rendered HTML bytes, or nil on template error.
func (h *ConvoyHandler) fetchAndRender(r *http.Request, expandPanel string) []byte {
	ctx, cancel := context.WithTimeout(r.Context(), h.fetchTimeout)
	defer cancel()

	// Each panel's error is kept, not just logged. A log line is a record for
	// whoever reads the server's output later; the operator reading the page
	// sees only the rows, and rows alone cannot say "I could not look"
	// (gt-edty, gt-egq9).
	var (
		convoys    []ConvoyRow
		convoysErr error
		mergeQueue MergeQueueResult
		// mergeQueueErr and mergeQueue.FailedRigs are the two scales of the same
		// fact: the error means no rig was ever asked (the rig list itself could
		// not be read), the failed rigs mean some answered and some did not.
		// Without the error the panel rendered "Merge queue empty" for a town
		// whose queue it had never looked at.
		mergeQueueErr error
		workers       StoreResult[WorkerRow]
		// workersErr and workers.Warning() are not two spellings of one fact:
		// the error means tmux could not be asked, so there is no worker list;
		// the warning means the list exists but a rig store could not say what
		// its workers are carrying. Both are rendered.
		workersErr error
		mail       []MailRow
		// mailErr is rendered: a bd that could not list mail is not a town that
		// sent none, and "No mail traffic" is the one render an operator reads as
		// proof that nothing was sent.
		mailErr error
		rigs    []RigRow
		// rigsErr is rendered for the same reason. An unreadable rigs.json is not
		// a town with no rigs — and it is the same failure that empties the merge
		// queue panel, so the two must not disagree about whether anything is wrong.
		rigsErr error
		dogs    []DogRow
		// FetchDogs already tells a kennel that has never held a dog from one it
		// could not read. That distinction died here, one layer above it: the
		// error was logged and the template got an empty slice either way, so an
		// unreadable kennel rendered "No dogs in kennel" (gt-1jrl).
		dogsErr     error
		escalations []EscalationRow
		// escalationsErr is rendered, not just logged: an unreadable escalation
		// panel must not look like an empty one (gt-edty).
		escalationsErr error
		health         *HealthRow
		// healthErr matters more than most: the heartbeat stat renders only when
		// health is non-nil, so a failed read did not render a wrong value — it
		// removed the town's liveness indicator from the banner entirely, leaving
		// nothing to notice.
		healthErr   error
		queues      []QueueRow
		queuesErr   error
		sessions    []SessionRow
		sessionsErr error
		hooks       StoreResult[HookRow]
		// hooksErr and issuesErr are the union panels' whole-query failure, one
		// level above the per-store failures StoreResult carries: with no result
		// at all there are no failed stores to name, so the reason has to come
		// from the error or the panel falls back to "No hooked work".
		hooksErr    error
		mayor       *MayorStatus
		mayorErr    error
		issues      StoreResult[IssueRow]
		issuesErr   error
		activity    []ActivityRow
		activityErr error
		wg          sync.WaitGroup
	)

	// Run all fetches in parallel with error logging
	wg.Add(14)

	go func() {
		defer wg.Done()
		convoys, convoysErr = h.fetcher.FetchConvoys()
		if convoysErr != nil {
			log.Printf("dashboard: FetchConvoys failed: %v", convoysErr)
		}
	}()
	go func() {
		defer wg.Done()
		mergeQueue, mergeQueueErr = h.fetcher.FetchMergeQueue()
		if mergeQueueErr != nil {
			log.Printf("dashboard: FetchMergeQueue failed: %v", mergeQueueErr)
		}
	}()
	go func() {
		defer wg.Done()
		workers, workersErr = h.fetcher.FetchWorkers()
		if workersErr != nil {
			log.Printf("dashboard: FetchWorkers failed: %v", workersErr)
		}
	}()
	go func() {
		defer wg.Done()
		mail, mailErr = h.fetcher.FetchMail()
		if mailErr != nil {
			log.Printf("dashboard: FetchMail failed: %v", mailErr)
		}
	}()
	go func() {
		defer wg.Done()
		rigs, rigsErr = h.fetcher.FetchRigs()
		if rigsErr != nil {
			log.Printf("dashboard: FetchRigs failed: %v", rigsErr)
		}
	}()
	go func() {
		defer wg.Done()
		dogs, dogsErr = h.fetcher.FetchDogs()
		if dogsErr != nil {
			log.Printf("dashboard: FetchDogs failed: %v", dogsErr)
		}
	}()
	go func() {
		defer wg.Done()
		escalations, escalationsErr = h.fetcher.FetchEscalations()
		if escalationsErr != nil {
			log.Printf("dashboard: FetchEscalations failed: %v", escalationsErr)
		}
	}()
	go func() {
		defer wg.Done()
		health, healthErr = h.fetcher.FetchHealth()
		if healthErr != nil {
			log.Printf("dashboard: FetchHealth failed: %v", healthErr)
		}
	}()
	go func() {
		defer wg.Done()
		queues, queuesErr = h.fetcher.FetchQueues()
		if queuesErr != nil {
			log.Printf("dashboard: FetchQueues failed: %v", queuesErr)
		}
	}()
	go func() {
		defer wg.Done()
		sessions, sessionsErr = h.fetcher.FetchSessions()
		if sessionsErr != nil {
			log.Printf("dashboard: FetchSessions failed: %v", sessionsErr)
		}
	}()
	go func() {
		defer wg.Done()
		hooks, hooksErr = h.fetcher.FetchHooks()
		if hooksErr != nil {
			log.Printf("dashboard: FetchHooks failed: %v", hooksErr)
		}
	}()
	go func() {
		defer wg.Done()
		mayor, mayorErr = h.fetcher.FetchMayor()
		if mayorErr != nil {
			log.Printf("dashboard: FetchMayor failed: %v", mayorErr)
		}
	}()
	go func() {
		defer wg.Done()
		issues, issuesErr = h.fetcher.FetchIssues()
		if issuesErr != nil {
			log.Printf("dashboard: FetchIssues failed: %v", issuesErr)
		}
	}()
	go func() {
		defer wg.Done()
		activity, activityErr = h.fetcher.FetchActivity()
		if activityErr != nil {
			log.Printf("dashboard: FetchActivity failed: %v", activityErr)
		}
	}()

	// Wait for fetches or timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All fetches completed
	case <-ctx.Done():
		log.Printf("dashboard: fetch timeout after %v", h.fetchTimeout)
		// Goroutines may still be writing to shared result variables.
		// Wait for them to finish to avoid a data race on read below.
		<-done
	}

	// Compute summary from already-fetched data
	summary := computeSummary(summaryInput{
		workers:    workers.Rows,
		workersErr: workersErr,
		hooks:      hooks.Rows,
		// The union panels have no error to carry — every store failing
		// separately still returns a result — so the banner is told whether the
		// rows it is counting came from any store at all.
		hooksUnreadable:  hooks.Unreadable() || hooksErr != nil,
		issues:           issues.Rows,
		issuesUnreadable: issues.Unreadable() || issuesErr != nil,
		convoys:          convoys,
		convoysErr:       convoysErr,
		escalations:      escalations,
		escalationsErr:   escalationsErr,
		activity:         activity,
		mailErr:          mailErr,
		rigsErr:          rigsErr,
		healthErr:        healthErr,
		mergeQueueErr:    mergeQueueErr,
	})

	data := ConvoyData{
		Convoys: convoys,
		// A failed query is reported, never rendered as an empty panel: zero
		// must mean zero, and unknown must look different.
		ConvoysUnavailable:     unavailableMessage(convoysErr),
		MergeQueue:             mergeQueue.Rows,
		MergeQueueFailedRigs:   mergeQueue.FailedRigs,
		MergeQueueUnavailable:  unavailableMessage(mergeQueueErr),
		Workers:                workers.Rows,
		WorkersUnavailable:     unavailableMessage(workersErr),
		WorkersWarning:         workers.Warning(),
		Mail:                   mail,
		MailUnavailable:        unavailableMessage(mailErr),
		Rigs:                   rigs,
		RigsUnavailable:        unavailableMessage(rigsErr),
		Dogs:                   dogs,
		DogsUnavailable:        unavailableMessage(dogsErr),
		Escalations:            escalations,
		EscalationsUnavailable: unavailableMessage(escalationsErr),
		Health:                 health,
		HealthUnavailable:      unavailableMessage(healthErr),
		Queues:                 queues,
		QueuesUnavailable:      unavailableMessage(queuesErr),
		Sessions:               sessions,
		SessionsUnavailable:    unavailableMessage(sessionsErr),
		Hooks:                  hooks.Rows,
		HooksWarning:           hooks.Warning(),
		HooksUnavailable:       unionUnavailable(hooksErr, hooks.UnavailableReason()),
		Mayor:                  mayor,
		MayorUnavailable:       unavailableMessage(mayorErr),
		Issues:                 enrichIssuesWithAssignees(issues.Rows, hooks.Rows),
		IssuesWarning:          issues.Warning(),
		IssuesUnavailable:      unionUnavailable(issuesErr, issues.UnavailableReason()),
		Activity:               activity,
		ActivityUnavailable:    unavailableMessage(activityErr),
		Summary:                summary,
		Expand:                 expandPanel,
		CSRFToken:              h.csrfToken,
	}

	var buf bytes.Buffer
	if err := h.template.ExecuteTemplate(&buf, "convoy.html", data); err != nil {
		log.Printf("dashboard: template execution failed: %v", err)
		return nil
	}

	return buf.Bytes()
}

// summaryInput is what the banner is computed from: each panel's rows AND
// whether that panel could be read at all.
//
// They travel together because len(nil) is 0 either way, and the summary is
// where that difference decides between "✓ All clear" and an alert (gt-edty).
type summaryInput struct {
	workers    []WorkerRow
	workersErr error
	hooks      []HookRow
	// hooksUnreadable and issuesUnreadable are the union panels' version of an
	// error: no store answered, so the count below them is of nothing read.
	hooksUnreadable  bool
	issues           []IssueRow
	issuesUnreadable bool
	convoys          []ConvoyRow
	convoysErr       error
	escalations      []EscalationRow
	escalationsErr   error
	activity         []ActivityRow
	// These four have no count in the banner, only an alert: the operator's
	// glance must still show that the page is missing a panel, or "✓ All clear"
	// gets said about a dashboard that cannot see (gt-xw1t).
	mailErr       error
	rigsErr       error
	healthErr     error
	mergeQueueErr error
}

// computeSummary calculates dashboard stats and alerts from fetched data.
func computeSummary(in summaryInput) *DashboardSummary {
	summary := &DashboardSummary{
		PolecatCount:           len(in.workers),
		HookCount:              len(in.hooks),
		IssueCount:             len(in.issues),
		ConvoyCount:            len(in.convoys),
		EscalationCount:        len(in.escalations),
		EscalationsUnavailable: in.escalationsErr != nil,
		PolecatsUnavailable:    in.workersErr != nil,
		ConvoysUnavailable:     in.convoysErr != nil,
		HooksUnavailable:       in.hooksUnreadable,
		IssuesUnavailable:      in.issuesUnreadable,
		MailUnavailable:        in.mailErr != nil,
		RigsUnavailable:        in.rigsErr != nil,
		HealthUnavailable:      in.healthErr != nil,
		MergeQueueUnavailable:  in.mergeQueueErr != nil,
	}

	workers, hooks, issues, escalations, activity :=
		in.workers, in.hooks, in.issues, in.escalations, in.activity

	// Count stuck workers (status = "stuck")
	for _, w := range workers {
		if w.WorkStatus == "stuck" {
			summary.StuckPolecats++
		}
	}

	// Count stale hooks (IsStale = true)
	for _, h := range hooks {
		if h.IsStale {
			summary.StaleHooks++
		}
	}

	// Count unacked escalations
	for _, e := range escalations {
		if !e.Acked {
			summary.UnackedEscalations++
		}
	}

	// Count high priority issues (P1 or P2)
	for _, i := range issues {
		if i.Priority == 1 || i.Priority == 2 {
			summary.HighPriorityIssues++
		}
	}

	// Count recent session deaths from activity
	for _, a := range activity {
		if a.Type == "session_death" || a.Type == "mass_death" {
			summary.DeadSessions++
		}
	}

	// Set HasAlerts flag. Not being able to read a panel is itself an alert —
	// otherwise the banner reads "All clear" precisely when the dashboard has
	// lost sight of the panels that report trouble.
	summary.HasAlerts = summary.EscalationsUnavailable ||
		summary.PolecatsUnavailable ||
		summary.ConvoysUnavailable ||
		summary.HooksUnavailable ||
		summary.IssuesUnavailable ||
		summary.MailUnavailable ||
		summary.RigsUnavailable ||
		summary.HealthUnavailable ||
		summary.MergeQueueUnavailable ||
		summary.StuckPolecats > 0 ||
		summary.StaleHooks > 0 ||
		summary.UnackedEscalations > 0 ||
		summary.DeadSessions > 0 ||
		summary.HighPriorityIssues > 0

	return summary
}

// unavailableMessage renders a failed panel query for display, or "" when the
// query succeeded. The reason is shown rather than a bare "failed" because the
// operator's next move differs by cause — bd timing out points at Dolt, a parse
// failure points at bd itself, an unreachable tmux at the session layer.
func unavailableMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// unionUnavailable renders why a union panel has no answer, from whichever of
// its two failure channels fired. The query failing outright wins: it means the
// StoreResult was never built, so its own reason would be an empty string that
// reads as "every store answered".
func unionUnavailable(err error, storeReason string) string {
	if err != nil {
		return err.Error()
	}
	return storeReason
}

// enrichIssuesWithAssignees adds Assignee info to issues by cross-referencing hooks.
func enrichIssuesWithAssignees(issues []IssueRow, hooks []HookRow) []IssueRow {
	// Build a map of issue ID -> assignee from hooks
	hookMap := make(map[string]string)
	for _, hook := range hooks {
		hookMap[hook.ID] = hook.Agent
	}

	// Enrich issues with assignee info
	for i := range issues {
		if assignee, ok := hookMap[issues[i].ID]; ok {
			issues[i].Assignee = assignee
		}
	}
	return issues
}

// generateCSRFToken creates a cryptographically random token for CSRF protection.
func generateCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("failed to generate CSRF token: %v", err)
	}
	return hex.EncodeToString(b)
}

// NewDashboardMux creates an HTTP handler that serves both the dashboard and API.
// webCfg may be nil, in which case defaults are used.
func NewDashboardMux(fetcher ConvoyFetcher, webCfg *config.WebTimeoutsConfig) (http.Handler, error) {
	if webCfg == nil {
		webCfg = config.DefaultWebTimeoutsConfig()
	}

	csrfToken := generateCSRFToken()

	fetchTimeout := config.ParseDurationOrDefault(webCfg.FetchTimeout, 8*time.Second)
	convoyHandler, err := NewConvoyHandler(fetcher, fetchTimeout, csrfToken)
	if err != nil {
		return nil, err
	}

	defaultRunTimeout := config.ParseDurationOrDefault(webCfg.DefaultRunTimeout, 30*time.Second)
	maxRunTimeout := config.ParseDurationOrDefault(webCfg.MaxRunTimeout, 60*time.Second)
	apiHandler := NewAPIHandler(defaultRunTimeout, maxRunTimeout, csrfToken)

	// Create static file server from embedded files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	staticHandler := http.FileServer(http.FS(staticFS))

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/static/", http.StripPrefix("/static/", staticHandler))
	mux.Handle("/", convoyHandler)

	return mux, nil
}
