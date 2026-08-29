package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/activity"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	convoyops "github.com/steveyegge/gastown/internal/convoy"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// runCmd executes a command with a timeout and returns stdout.
// Returns empty buffer on timeout or error.
//
// stderr is captured into the error rather than discarded: a command's exit
// status says only that it failed, and the panels need to tell one failure from
// another — "no server running" is an empty town, anything else is a town the
// dashboard could not read. See tmuxServerAbsent.
func runCmd(timeout time.Duration, name string, args ...string) (*bytes.Buffer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%s timed out after %v", name, timeout)
		}
		if detail := firstLine(stderr.String()); detail != "" {
			return nil, fmt.Errorf("%s: %w: %s", name, err, detail)
		}
		return nil, err
	}
	return &stdout, nil
}

// firstLine returns the first non-empty line of s, trimmed.
//
// A failing command's stderr can run to many lines, and these errors are
// rendered inline in a panel notice rather than only logged. One line is enough
// to say what happened; the rest would break the notice across the layout.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// tmuxServerAbsent reports whether a tmux command failed because there is no
// tmux server at all.
//
// That is a town with nothing running — genuinely zero sessions — and it must
// stay distinguishable from a tmux the dashboard could not ask. tmux exits
// non-zero for both, so the discrimination is on the message: without it, the
// "unreadable" caveat would appear on every town whose server is simply down
// and stop carrying information.
func tmuxServerAbsent(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no server running") ||
		strings.Contains(msg, "error connecting to")
}

// runTmuxCmd runs a tmux command using the per-town socket.
// Without -L, tmux queries the default socket which has no Gas Town sessions.
func (f *LiveConvoyFetcher) runTmuxCmd(args ...string) (*bytes.Buffer, error) {
	fullArgs := []string{}
	if f.tmuxSocket != "" {
		fullArgs = append(fullArgs, "-L", f.tmuxSocket)
	}
	fullArgs = append(fullArgs, args...)
	return fetcherRunCmd(f.tmuxCmdTimeout, "tmux", fullArgs...)
}

var fetcherRunCmd = runCmd
var fetcherGetSessionEnv = func(sessionName, key string) (string, error) {
	return tmux.NewTmux().GetEnvironment(sessionName, key)
}

// runBdCmd executes a bd command with the configured cmdTimeout in the specified beads directory.
//
// Like runCmd, it folds bd's own words into the error instead of discarding
// them. The panels that read through here now render the reason a query failed,
// and "connection refused" and "unknown label" send an operator to different
// places — "exit status 1" sends them nowhere, which is a notice that admits it
// could not look while withholding the one fact worth having (gt-1jrl).
func (f *LiveConvoyFetcher) runBdCmd(beadsDir string, args ...string) (*bytes.Buffer, error) {
	// bd v0.59+ requires --flat for list --json to produce JSON output
	args = beads.InjectFlatForListJSON(args)

	ctx, cancel := context.WithTimeout(context.Background(), f.cmdTimeout)
	defer cancel()

	bin := f.bdBin
	if bin == "" {
		bin = "bd"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = beadsDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("bd timed out after %v", f.cmdTimeout)
		}
		// If we got some output, return it anyway (bd may exit non-zero with warnings)
		if stdout.Len() > 0 {
			return &stdout, nil
		}
		if detail := firstLine(stderr.String()); detail != "" {
			return nil, fmt.Errorf("%w: %s", err, detail)
		}
		return nil, err
	}
	return &stdout, nil
}

// fetchCircuitBreaker tracks consecutive failures for a fetch operation
// and applies exponential backoff to prevent process storms.
type fetchCircuitBreaker struct {
	mu          sync.Mutex
	failures    int
	lastAttempt time.Time
	backoff     time.Duration
	inFlight    bool
}

// maxBackoff is the maximum backoff duration for the circuit breaker.
const maxBackoff = 5 * time.Minute

// allow returns true if enough time has passed since the last failure to permit
// a new attempt, and reserves that attempt so concurrent callers do not all
// stampede through when backoff opens.
func (cb *fetchCircuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.inFlight {
		return false
	}
	if cb.failures == 0 {
		cb.inFlight = true
		return true
	}
	if time.Since(cb.lastAttempt) < cb.backoff {
		return false
	}
	cb.inFlight = true
	return true
}

// unavailableReason explains why allow() refused, so the caller can report
// that it has no data instead of returning an empty result.
//
// A backed-off fetch knows nothing about the town: the breaker exists to stop
// process storms, not to make an unreadable panel look like a quiet one.
func (cb *fetchCircuitBreaker) unavailableReason() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.failures == 0 {
		return errors.New("a query is already in flight")
	}
	retryIn := cb.backoff - time.Since(cb.lastAttempt)
	if retryIn < 0 {
		retryIn = 0
	}
	return fmt.Errorf("backing off after %d consecutive failures, retrying in %v",
		cb.failures, retryIn.Round(time.Second))
}

// recordFailure increments the failure count and sets exponential backoff.
// Backoff doubles from 10s up to maxBackoff.
func (cb *fetchCircuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.lastAttempt = time.Now()
	cb.inFlight = false
	// Exponential backoff: 10s, 20s, 40s, 80s, 160s, capped at maxBackoff
	cb.backoff = time.Duration(1<<min(cb.failures, 10)) * 5 * time.Second
	if cb.backoff > maxBackoff {
		cb.backoff = maxBackoff
	}
}

// recordSuccess resets the circuit breaker on a successful fetch.
func (cb *fetchCircuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	cb.backoff = 0
	cb.inFlight = false
}

// LiveConvoyFetcher fetches convoy data from beads.
type LiveConvoyFetcher struct {
	townRoot  string
	townBeads string

	// bdBin is the bd binary name or path. Defaults to "bd" if empty.
	bdBin string

	// registry is a prefix registry built from the town's rigs.json.
	// Used for parsing tmux session names instead of relying on the
	// package-level DefaultRegistry, which may not be initialized in
	// the dashboard process context.
	registry *session.PrefixRegistry

	// Configurable timeouts (from TownSettings.WebTimeouts)
	cmdTimeout     time.Duration
	ghCmdTimeout   time.Duration
	tmuxCmdTimeout time.Duration

	// Configurable worker status thresholds (from TownSettings.WorkerStatus)
	staleThreshold          time.Duration
	stuckThreshold          time.Duration
	heartbeatFreshThreshold time.Duration
	mayorActiveThreshold    time.Duration

	// tmuxSocket is the per-town tmux socket name (e.g., "dipgt-651c6b").
	// All tmux commands must use -L with this socket; the default socket
	// has no Gas Town sessions.
	tmuxSocket string

	// Circuit breaker for FetchConvoys — prevents process storms when
	// bd list by convoy label fails persistently (e.g., schema mismatch).
	convoyBreaker fetchCircuitBreaker
}

// NewLiveConvoyFetcher creates a fetcher for the current workspace.
// Loads timeout and threshold config from TownSettings; falls back to defaults if missing.
func NewLiveConvoyFetcher() (*LiveConvoyFetcher, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	webCfg := config.DefaultWebTimeoutsConfig()
	workerCfg := config.DefaultWorkerStatusConfig()
	if ts, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot)); err == nil {
		// Replace entire defaults — individual fields fall back via ParseDurationOrDefault
		// (empty string → hardcoded default). Add explicit zero-value guards for non-duration fields.
		if ts.WebTimeouts != nil {
			webCfg = ts.WebTimeouts
		}
		if ts.WorkerStatus != nil {
			workerCfg = ts.WorkerStatus
		}
	}

	// Build a local prefix registry from the town's rigs.json so session
	// name parsing works regardless of whether the package-level
	// DefaultRegistry was initialized (gt-y24).
	registry, regErr := session.BuildPrefixRegistryFromTown(townRoot)
	if regErr != nil {
		log.Printf("dashboard: failed to build prefix registry: %v (falling back to default)", regErr)
		registry = session.DefaultRegistry()
	}

	return &LiveConvoyFetcher{
		townRoot:                townRoot,
		townBeads:               filepath.Join(townRoot, ".beads"),
		registry:                registry,
		tmuxSocket:              tmux.GetDefaultSocket(),
		cmdTimeout:              config.ParseDurationOrDefault(webCfg.CmdTimeout, 15*time.Second),
		ghCmdTimeout:            config.ParseDurationOrDefault(webCfg.GhCmdTimeout, 10*time.Second),
		tmuxCmdTimeout:          config.ParseDurationOrDefault(webCfg.TmuxCmdTimeout, 2*time.Second),
		staleThreshold:          config.ParseDurationOrDefault(workerCfg.StaleThreshold, 5*time.Minute),
		stuckThreshold:          config.ParseDurationOrDefault(workerCfg.StuckThreshold, constants.GUPPViolationTimeout),
		heartbeatFreshThreshold: config.ParseDurationOrDefault(workerCfg.HeartbeatFreshThreshold, config.DefaultDeaconHeartbeatStaleThreshold),
		mayorActiveThreshold:    config.ParseDurationOrDefault(workerCfg.MayorActiveThreshold, 5*time.Minute),
	}, nil
}

// FetchConvoys fetches all open convoys with their activity data.
// Uses a circuit breaker to avoid hammering bd/dolt when listing fails
// persistently (e.g., "invalid issue type: convoy" schema mismatch).
func (f *LiveConvoyFetcher) FetchConvoys() ([]ConvoyRow, error) {
	if !f.convoyBreaker.allow() {
		// Backed off, so this call read nothing. Reporting that is the whole
		// point: the panel that renders a silent backoff as an empty list is
		// claiming there are no convoys on the evidence of a query never run.
		return nil, fmt.Errorf("listing convoys: %w", f.convoyBreaker.unavailableReason())
	}

	// List all open issues and filter locally so legacy type=convoy beads remain visible.
	//
	// Asked as both pinned halves and unioned by ID: `bd list --status=open`
	// defaults to `--no-pinned` with no include-pinned flag, so a pinned convoy
	// would otherwise vanish from this panel silently (same shape as gt-z5h7,
	// gt-qee3, hq-ztj4g).
	type convoyIssue struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Status    string   `json:"status"`
		CreatedAt string   `json:"created_at"`
		IssueType string   `json:"issue_type"`
		Labels    []string `json:"labels"`
	}
	var convoys []convoyIssue
	seenConvoys := make(map[string]bool)
	for _, pinnedFilter := range []string{"--no-pinned", "--pinned"} {
		stdout, err := f.runBdCmd(f.townRoot, "list", "--status=open", pinnedFilter, "--json", "--limit=0")
		if err != nil {
			f.convoyBreaker.recordFailure()
			return nil, fmt.Errorf("listing convoys (%s): %w", pinnedFilter, err)
		}

		var half []convoyIssue
		if err := json.Unmarshal(stdout.Bytes(), &half); err != nil {
			f.convoyBreaker.recordFailure()
			return nil, fmt.Errorf("parsing convoy list (%s): %w", pinnedFilter, err)
		}
		for _, c := range half {
			if seenConvoys[c.ID] {
				continue
			}
			seenConvoys[c.ID] = true
			convoys = append(convoys, c)
		}
	}

	// One classification environment for the whole pass: the scheduled-bead scan
	// reads every store in town, so it must not run per convoy.
	env := f.convoyClassifierEnv()

	// Build convoy rows with activity data
	rows := make([]ConvoyRow, 0, len(convoys))
	for _, c := range convoys {
		if c.IssueType != "convoy" && !webConvoyHasLabel(c.Labels, "gt:convoy") {
			continue
		}
		row := ConvoyRow{
			ID:     c.ID,
			Title:  c.Title,
			Status: c.Status,
		}

		// Get tracked issues for progress and activity calculation
		tracked, err := f.getTrackedIssues(c.ID)
		if err != nil {
			log.Printf("warning: skipping convoy %s: %v", c.ID, err)
			continue
		}
		row.Total = len(tracked)

		var mostRecentActivity time.Time
		var mostRecentUpdated time.Time
		var hasAssignee bool
		assigneeSet := make(map[string]struct{})
		for _, t := range tracked {
			if t.Status == "closed" {
				row.Completed++
			}
			// Track most recent activity from workers
			if t.LastActivity.After(mostRecentActivity) {
				mostRecentActivity = t.LastActivity
			}
			// Track most recent updated_at as fallback
			if t.UpdatedAt.After(mostRecentUpdated) {
				mostRecentUpdated = t.UpdatedAt
			}
			if t.Assignee != "" {
				hasAssignee = true
				assigneeSet[t.Assignee] = struct{}{}
			}
		}

		// Collect unique assignees (sorted for stable display order)
		row.Assignees = make([]string, 0, len(assigneeSet))
		for a := range assigneeSet {
			row.Assignees = append(row.Assignees, a)
		}
		sort.Strings(row.Assignees)

		row.Progress = fmt.Sprintf("%d/%d", row.Completed, row.Total)
		if row.Total > 0 {
			row.ProgressPct = (row.Completed * 100) / row.Total
		}

		// Calculate activity info from most recent worker activity
		if !mostRecentActivity.IsZero() {
			// Have active tmux session activity from assigned workers
			row.LastActivity = activity.Calculate(mostRecentActivity)
		} else if !hasAssignee {
			// No assignees found in beads - try fallback to any running polecat activity
			// This handles cases where bd update --assignee didn't persist or wasn't returned
			if polecatActivity := f.getAllPolecatActivity(); polecatActivity != nil {
				info := activity.Calculate(*polecatActivity)
				info.FormattedAge = info.FormattedAge + " (polecat active)"
				row.LastActivity = info
			} else if !mostRecentUpdated.IsZero() {
				// Fall back to issue updated_at if no polecats running
				info := activity.Calculate(mostRecentUpdated)
				info.FormattedAge = info.FormattedAge + " (unassigned)"
				row.LastActivity = info
			} else {
				row.LastActivity = activity.Info{
					FormattedAge: "unassigned",
					ColorClass:   activity.ColorUnknown,
				}
			}
		} else {
			// Has assignee but no active session
			row.LastActivity = activity.Info{
				FormattedAge: "idle",
				ColorClass:   activity.ColorUnknown,
			}
		}

		// Classify the convoy from execution state, using the same code
		// `gt convoy stranded` uses. Activity age is rendered in its own column
		// and decides nothing (gt-skzk.1).
		row.WorkStatus, row.Evidence = f.convoyWorkStatus(tracked, env)

		// The work chips come off the same evidence as the badge. Counting an
		// assignee as "active" is the presence-for-state substitution this bead
		// is about at bead scale: a polecat that died mid-run leaves its name on
		// the bead, and a bead that was never routable is not work anyone can
		// pick up.
		row.ReadyBeads = row.Evidence[convoyops.DispoReady]
		row.InProgress = row.Evidence[convoyops.DispoWorking]
		row.InQueue = row.Evidence[convoyops.DispoInQueue]

		// Get tracked issues for expandable view
		row.TrackedIssues = make([]TrackedIssue, len(tracked))
		for i, t := range tracked {
			row.TrackedIssues[i] = TrackedIssue{
				ID:       t.ID,
				Title:    t.Title,
				Status:   t.Status,
				Assignee: t.Assignee,
			}
		}

		rows = append(rows, row)
	}

	f.convoyBreaker.recordSuccess()
	return rows, nil
}

func webConvoyHasLabel(labels []string, target string) bool {
	for _, label := range labels {
		if label == target {
			return true
		}
	}
	return false
}

// trackedIssueInfo holds info about an issue being tracked by a convoy.
type trackedIssueInfo struct {
	ID           string
	Title        string
	Status       string
	IssueType    string
	Assignee     string
	Blocked      bool
	LastActivity time.Time
	UpdatedAt    time.Time // Fallback for activity when no assignee
}

// webTrackedIssues narrows the panel's rows to the fields that decide a
// disposition, so the dashboard hands the shared classifier exactly what
// `gt convoy stranded` hands it.
func webTrackedIssues(tracked []trackedIssueInfo) []convoyops.TrackedIssue {
	out := make([]convoyops.TrackedIssue, 0, len(tracked))
	for _, t := range tracked {
		out = append(out, convoyops.TrackedIssue{
			ID:        t.ID,
			Status:    t.Status,
			IssueType: t.IssueType,
			Assignee:  t.Assignee,
			Blocked:   t.Blocked,
		})
	}
	return out
}

// convoyClassifierEnv assembles the live lookups the shared convoy classifier
// needs. It is built once per fetch: the sling-context scan reads every beads
// store in town, and the merge-queue lookup is consulted only for beads whose
// session has died, which is where it changes the answer.
//
// One deliberate difference from `gt convoy stranded`: when the sling-context
// scan cannot read every store, the CLI treats every bead as scheduled, because
// there the answer guards dispatch and over-reporting "scheduled" only costs a
// missed dispatch. Here the same choice would paint every convoy in town as
// benignly waiting on the strength of a scan that failed, so the panel uses the
// partial result and logs what it could not read.
func (f *LiveConvoyFetcher) convoyClassifierEnv() convoyops.Env {
	liveSessions := f.liveSessionNames()

	scheduled, err := fetcherScheduledBeads(f.townRoot)
	if err != nil {
		log.Printf("dashboard: %v — scheduled beads may render as ready", err)
	}

	return convoyops.Env{
		Scheduled:    scheduled,
		SessionAlive: func(sessionName string) bool { return liveSessions[sessionName] },
		QueuedMR:     func(beadID string) bool { return fetcherHasQueuedMR(f.townRoot, beadID) },
	}
}

// Injection points for the two live lookups (stubbed in tests). Both talk to
// beads stores directly rather than through runBdCmd.
var (
	fetcherScheduledBeads = convoyops.OpenSlingContextWorkBeads
	fetcherHasQueuedMR    = convoyops.HasQueuedMergeRequest
)

// convoyWorkStatus decides a convoy's verdict from its tracked beads.
//
// Nothing here reads a clock. That is the fix: the previous version took the
// activity-age COLOR as its only input beyond the completed count, so silence
// and stalling were the same observation to it (gt-skzk.1).
func (f *LiveConvoyFetcher) convoyWorkStatus(tracked []trackedIssueInfo, env convoyops.Env) (string, map[string]int) {
	_, evidence := convoyops.ClassifyAll(f.townRoot, webTrackedIssues(tracked), env)
	return convoyops.WorkStatus(len(tracked), evidence), evidence
}

// liveSessionNames returns the set of tmux sessions currently running on the
// town's socket. One listing answers "is this worker alive?" for every convoy in
// the pass; asking per assignee would be one tmux exec per tracked bead.
//
// An unreadable tmux server yields an empty set, which reads as "no worker is
// alive". That is the same answer `gt convoy stranded` gives when has-session
// fails, and it errs toward showing work as needing attention rather than
// toward a worker that is not there.
func (f *LiveConvoyFetcher) liveSessionNames() map[string]bool {
	live := make(map[string]bool)
	stdout, err := f.runTmuxCmd("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return live
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			live[name] = true
		}
	}
	return live
}

// getTrackedIssues fetches tracked issues for a convoy.
func (f *LiveConvoyFetcher) getTrackedIssues(convoyID string) ([]trackedIssueInfo, error) {
	issueIDs, err := f.trackedIssueIDs(convoyID)
	if err != nil {
		return nil, err
	}

	// Batch fetch issue details
	details, err := f.getIssueDetailsBatch(issueIDs)
	if err != nil {
		return nil, fmt.Errorf("fetching tracked issue details for %s: %w", convoyID, err)
	}

	// Get worker activity from tmux sessions based on assignees
	workers := f.getWorkersFromAssignees(details)

	// Build result
	result := make([]trackedIssueInfo, 0, len(issueIDs))
	for _, id := range issueIDs {
		info := trackedIssueInfo{ID: id}

		if d, ok := details[id]; ok {
			info.Title = d.Title
			info.Status = d.Status
			info.IssueType = d.IssueType
			info.Assignee = d.Assignee
			info.Blocked = d.Blocked
			info.UpdatedAt = d.UpdatedAt
		} else {
			// Not observed, not absent: the bead's store was unreadable from
			// here. The classifier reads this as "unknown" rather than guessing
			// a status for it.
			info.Title = "(external)"
			info.Status = convoyops.StatusUnknown
		}

		if w, ok := workers[id]; ok && w.LastActivity != nil {
			info.LastActivity = *w.LastActivity
		}

		result = append(result, info)
	}

	return result, nil
}

// trackedIssueIDs returns the IDs a convoy tracks.
//
// It reads the dependencies table directly, because `bd dep list -t tracks`
// joins against the issues table and so returns NOTHING for a dependency whose
// target lives in another Dolt database — which is every HQ convoy tracking rig
// work. Measured 2026-08-25: the join returned [] for all five live convoys
// while the raw table returned one tracked bead each, so the panel rendered a
// town of working polecats as five convoys with no work in them (gt-skzk.1).
//
// The join is kept as a fallback for stores that are not Dolt-in-server-mode,
// where there is no server to query.
func (f *LiveConvoyFetcher) trackedIssueIDs(convoyID string) ([]string, error) {
	ids, err := fetcherTrackedIssueIDs(f.townRoot, convoyID)
	if err == nil {
		return ids, nil
	}

	stdout, bdErr := f.runBdCmd(f.townRoot, "dep", "list", convoyID, "-t", "tracks", "--json")
	if bdErr != nil {
		// Report BOTH: "the dep table was unreachable" and "bd also failed" are
		// different outages, and a panel that names only the second sends an
		// operator to the wrong place.
		return nil, fmt.Errorf("querying tracked issues for %s: dep table: %v; bd dep list: %w", convoyID, err, bdErr)
	}

	var deps []struct {
		ID string `json:"id"`
	}
	if jsonErr := json.Unmarshal(stdout.Bytes(), &deps); jsonErr != nil {
		return nil, fmt.Errorf("parsing tracked issues for %s: %w", convoyID, jsonErr)
	}

	// Collect resolved issue IDs, unwrapping external:prefix:id format
	issueIDs := make([]string, 0, len(deps))
	for _, dep := range deps {
		issueIDs = append(issueIDs, beads.ExtractIssueID(dep.ID))
	}
	return issueIDs, nil
}

// fetcherTrackedIssueIDs is the injection point for the dep-table read
// (stubbed in tests).
var fetcherTrackedIssueIDs = convoyops.TrackedIssueIDs

// issueDetail holds basic issue info.
type issueDetail struct {
	ID        string
	Title     string
	Status    string
	IssueType string
	Assignee  string
	Blocked   bool
	UpdatedAt time.Time
}

// getIssueDetailsBatch fetches details for multiple issues.
func (f *LiveConvoyFetcher) getIssueDetailsBatch(issueIDs []string) (map[string]*issueDetail, error) {
	result := make(map[string]*issueDetail)
	if len(issueIDs) == 0 {
		return result, nil
	}

	args := append([]string{"show"}, issueIDs...)
	args = append(args, "--json")

	stdout, err := fetcherRunCmd(f.cmdTimeout, "bd", args...)
	if err != nil {
		return nil, fmt.Errorf("bd show failed (issue_count=%d): %w", len(issueIDs), err)
	}

	// Decoded into the shared beads.Issue so blocker state is read the same way
	// everywhere: blocked_by_count alone is not reliable (bd omits it on some
	// paths), and HasUnresolvedBlockers falls back to the live dependency edges.
	var issues []beads.Issue
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return nil, fmt.Errorf("bd show returned invalid JSON (issue_count=%d): %w", len(issueIDs), err)
	}

	for i := range issues {
		issue := &issues[i]
		detail := &issueDetail{
			ID:        issue.ID,
			Title:     issue.Title,
			Status:    issue.Status,
			IssueType: issue.Type,
			Assignee:  issue.Assignee,
			Blocked:   beads.HasUnresolvedBlockers(issue),
		}
		// Parse updated_at timestamp
		if issue.UpdatedAt != "" {
			if t, err := time.Parse(time.RFC3339, issue.UpdatedAt); err == nil {
				detail.UpdatedAt = t
			}
		}
		result[issue.ID] = detail
	}

	return result, nil
}

// workerDetail holds worker info including last activity.
type workerDetail struct {
	Worker       string
	LastActivity *time.Time
}

// getWorkersFromAssignees gets worker activity from tmux sessions based on issue assignees.
// Assignees are in format "rigname/polecats/polecatname" which maps to tmux session "gt-rigname-polecatname".
func (f *LiveConvoyFetcher) getWorkersFromAssignees(details map[string]*issueDetail) map[string]*workerDetail {
	result := make(map[string]*workerDetail)

	// Collect unique assignees and map them to issue IDs
	assigneeToIssues := make(map[string][]string)
	for issueID, detail := range details {
		if detail == nil || detail.Assignee == "" {
			continue
		}
		assigneeToIssues[detail.Assignee] = append(assigneeToIssues[detail.Assignee], issueID)
	}

	if len(assigneeToIssues) == 0 {
		return result
	}

	// For each unique assignee, look up tmux session activity
	for assignee, issueIDs := range assigneeToIssues {
		activity := f.getSessionActivityForAssignee(assignee)
		if activity == nil {
			continue
		}

		// Apply this activity to all issues assigned to this worker
		for _, issueID := range issueIDs {
			result[issueID] = &workerDetail{
				Worker:       assignee,
				LastActivity: activity,
			}
		}
	}

	return result
}

// getSessionActivityForAssignee looks up tmux session activity for an assignee.
// Assignee format: "rigname/polecats/polecatname" -> session "gt-rigname-polecatname"
func (f *LiveConvoyFetcher) getSessionActivityForAssignee(assignee string) *time.Time {
	// Parse assignee: "roxas/polecats/dag" -> rig="roxas", polecat="dag"
	parts := strings.Split(assignee, "/")
	if len(parts) != 3 || parts[1] != "polecats" {
		return nil
	}
	rig := parts[0]
	polecat := parts[2]

	// Construct session name
	sessionName := session.PolecatSessionName(session.PrefixFor(rig), polecat)

	// Query tmux for session activity
	// Format: session_activity returns unix timestamp
	stdout, err := f.runTmuxCmd("list-sessions", "-F", "#{session_name}|#{session_activity}",
		"-f", fmt.Sprintf("#{==:#{session_name},%s}", sessionName))
	if err != nil {
		return nil
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return nil
	}

	// Parse output: "gt-roxas-dag|1704312345"
	outputParts := strings.Split(output, "|")
	if len(outputParts) < 2 {
		return nil
	}

	var activityUnix int64
	if _, err := fmt.Sscanf(outputParts[1], "%d", &activityUnix); err != nil || activityUnix == 0 {
		return nil
	}

	activity := time.Unix(activityUnix, 0)
	return &activity
}

// getAllPolecatActivity returns the most recent activity from any running polecat session.
// This is used as a fallback when no specific assignee activity can be determined.
// Returns nil if no polecat sessions are running.
func (f *LiveConvoyFetcher) getAllPolecatActivity() *time.Time {
	// List all tmux sessions matching gt-*-* pattern (polecat sessions)
	// Format: gt-{rig}-{polecat}
	stdout, err := f.runTmuxCmd("list-sessions", "-F", "#{session_name}|#{session_activity}")
	if err != nil {
		return nil
	}

	var mostRecent time.Time
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}

		sessionName := parts[0]
		// Check if it's a polecat or crew session (skip infrastructure roles).
		// Use the fetcher's own registry to avoid dependency on global
		// DefaultRegistry initialization (gt-y24).
		identity, err := session.ParseSessionNameWithRegistry(sessionName, f.registry)
		if err != nil {
			continue
		}
		if identity.Role != session.RolePolecat && identity.Role != session.RoleCrew {
			continue
		}

		var activityUnix int64
		if _, err := fmt.Sscanf(parts[1], "%d", &activityUnix); err != nil || activityUnix == 0 {
			continue
		}

		activityTime := time.Unix(activityUnix, 0)
		if activityTime.After(mostRecent) {
			mostRecent = activityTime
		}
	}

	if mostRecent.IsZero() {
		return nil
	}
	return &mostRecent
}

// calculateWorkStatus is gone: it decided a convoy's verdict from the AGE of
// the last activity event, so every convoy doing work slower than the ten-minute
// red threshold turned STUCK and stayed stuck until it completed — measured
// against 8-minute gate runs and 20-40 minute polecat work items, that is every
// convoy that does real work. Age answers "was anything logged recently", which
// is a different question from "is anyone on this". See convoy.WorkStatus for
// the replacement and gt-skzk.1 for the measurement.

// mergeRequestLabel marks a bead as a merge request. Same label `gt mq list`
// filters on — the dashboard and CLI must read the same source (gt-4qp).
const mergeRequestLabel = "gt:merge-request"

// MergeQueueResult holds the merge-request rows for the dashboard panel.
// Failed rigs are reported so a partial queue is never rendered as complete.
type MergeQueueResult struct {
	Rows []MergeQueueRow
	// FailedRigs names rigs whose MR query errored. Their rows are missing, so
	// the count is a floor, not a total.
	FailedRigs []string
}

// fetcherListMergeRequests is the injection point for MR queries (stubbed in tests).
// It mirrors `gt mq list`: a Beads wrapper rooted at the rig, queried for
// open merge-request beads across both the issues and wisps tables.
var fetcherListMergeRequests = func(rigPath string, opts beads.ListOptions) ([]*beads.Issue, error) {
	return beads.New(rigPath).ListMergeRequests(opts)
}

// FetchMergeQueue fetches the actual merge queue: open merge-request beads from
// each registered rig's own beads db.
//
// This deliberately does NOT list GitHub PRs. The panel previously ran
// `gh pr list` against each rig's git_url, which for forked rigs is the
// UPSTREAM — so it rendered the upstream community's backlog as our merge queue
// (62 rows when every queue was empty) and disagreed with `gt mq list` (gt-4qp).
// PR data is enrichment only, and only when an MR bead records one.
func (f *LiveConvoyFetcher) FetchMergeQueue() (MergeQueueResult, error) {
	// Load registered rigs from config
	rigsConfigPath := filepath.Join(f.townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return MergeQueueResult{}, fmt.Errorf("loading rigs config: %w", err)
	}

	// Sort rig names so rows render in a stable order.
	rigNames := make([]string, 0, len(rigsConfig.Rigs))
	for rigName := range rigsConfig.Rigs {
		rigNames = append(rigNames, rigName)
	}
	sort.Strings(rigNames)

	var result MergeQueueResult

	for _, rigName := range rigNames {
		rows, err := f.fetchMergeRequestsForRig(rigName)
		if err != nil {
			// Non-fatal: continue with other rigs, but say the count is short.
			log.Printf("dashboard: merge queue for rig %s failed: %v", rigName, err)
			result.FailedRigs = append(result.FailedRigs, rigName)
			continue
		}
		result.Rows = append(result.Rows, rows...)
	}

	// Layer GitHub PR state on top of the queue — never the other way around.
	f.enrichWithPRStatus(result.Rows)

	return result, nil
}

// fetchMergeRequestsForRig returns open MR beads for one rig, matching the
// filtering `gt mq list <rig>` applies.
func (f *LiveConvoyFetcher) fetchMergeRequestsForRig(rigName string) ([]MergeQueueRow, error) {
	// Rig.BeadsPath() is the rig root; beads.New resolves .beads from there.
	rigPath := filepath.Join(f.townRoot, rigName)

	// Priority -1 means "no priority filter" — 0 would filter to P0 only.
	issues, err := fetcherListMergeRequests(rigPath, beads.ListOptions{
		Label:    mergeRequestLabel,
		Status:   "open",
		Priority: -1,
		Rig:      rigName,
	})
	if err != nil {
		return nil, fmt.Errorf("querying merge queue for %s: %w", rigName, err)
	}

	rows := make([]MergeQueueRow, 0, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		// bd list does not reliably honor the status filter; re-check here the
		// way gt mq list does.
		if !strings.EqualFold(issue.Status, "open") {
			continue
		}

		fields := beads.ParseMRFields(issue)

		// Wisps are shared across all rigs on the Dolt server, so an MR that
		// names a different rig must not appear under this one.
		if fields != nil && fields.Rig != "" && !strings.EqualFold(fields.Rig, rigName) {
			continue
		}

		rows = append(rows, mergeQueueRowFromMR(issue, fields, rigName))
	}

	return rows, nil
}

// mergeQueueRowFromMR builds a display row from an MR bead and its parsed fields.
func mergeQueueRowFromMR(issue *beads.Issue, fields *beads.MRFields, rigName string) MergeQueueRow {
	row := MergeQueueRow{
		ID:    issue.ID,
		Repo:  rigName,
		Title: issue.Title,
	}

	if created, err := time.Parse(time.RFC3339, issue.CreatedAt); err == nil {
		row.Age = formatMailAge(time.Since(created))
	}

	if fields == nil {
		// No parsable MR fields — still a real queue entry, just sparse.
		row.ColorClass = "mq-yellow"
		return row
	}

	row.Branch = fields.Branch
	row.Target = fields.Target
	row.SourceIssue = fields.SourceIssue
	row.Worker = fields.Worker
	row.RetryCount = fields.RetryCount
	row.ConvoyID = fields.ConvoyID

	// PR link is enrichment: shown only when the MR bead recorded one.
	row.HasPR = fields.PRURL != "" || fields.PRNumber > 0
	row.URL = fields.PRURL
	row.Number = fields.PRNumber

	// A conflict retry is the one queue-level signal we can color without
	// asking GitHub anything.
	if fields.RetryCount > 0 {
		row.ColorClass = "mq-red"
	} else {
		row.ColorClass = "mq-green"
	}

	return row
}

// prResponse represents the JSON response from a gh pr view lookup.
type prResponse struct {
	Number            int    `json:"number"`
	Title             string `json:"title"`
	URL               string `json:"url"`
	Mergeable         string `json:"mergeable"`
	StatusCheckRollup []struct {
		State      string `json:"state"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"statusCheckRollup"`
}

// mergeQueueEnrichConcurrency bounds simultaneous `gh pr view` lookups so a
// large queue cannot stall the dashboard render behind serial network calls.
const mergeQueueEnrichConcurrency = 4

// prURLPattern matches a GitHub PR URL, capturing owner/repo and the number.
var prURLPattern = regexp.MustCompile(`^https?://[^/]+/([^/]+/[^/]+)/pull/(\d+)`)

// parsePRURL extracts owner/repo and PR number from a PR URL.
func parsePRURL(prURL string) (repo string, number int, ok bool) {
	m := prURLPattern.FindStringSubmatch(prURL)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, false
	}
	return m[1], n, true
}

// enrichWithPRStatus fills CI and mergeable status for rows whose MR bead
// recorded a PR. Lookups are per-MR by PR number — never an unbounded
// `gh pr list`, which is what surfaced unrelated upstream PRs (gt-4qp).
// Failures are silent: PR status is decoration, and the row already exists
// on the strength of its MR bead.
func (f *LiveConvoyFetcher) enrichWithPRStatus(rows []MergeQueueRow) {
	type target struct {
		idx    int
		repo   string
		number int
	}

	var targets []target
	for i, row := range rows {
		if !row.HasPR || row.URL == "" {
			continue
		}
		repo, number, ok := parsePRURL(row.URL)
		if !ok {
			continue
		}
		// Prefer the number recorded on the bead; fall back to the URL's.
		if row.Number > 0 {
			number = row.Number
		}
		targets = append(targets, target{idx: i, repo: repo, number: number})
	}
	if len(targets) == 0 {
		return
	}

	sem := make(chan struct{}, mergeQueueEnrichConcurrency)
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			pr, err := f.fetchPRStatus(t.repo, t.number)
			if err != nil {
				return
			}
			rows[t.idx].CIStatus = determineCIStatus(pr.StatusCheckRollup)
			rows[t.idx].Mergeable = determineMergeableStatus(pr.Mergeable)
			rows[t.idx].ColorClass = determineColorClass(rows[t.idx].CIStatus, rows[t.idx].Mergeable)
		}(t)
	}
	wg.Wait()
}

// fetchPRStatus looks up a single PR's CI and mergeable state.
func (f *LiveConvoyFetcher) fetchPRStatus(repoFull string, number int) (*prResponse, error) {
	stdout, err := fetcherRunCmd(f.ghCmdTimeout, "gh", "pr", "view", strconv.Itoa(number),
		"--repo", repoFull,
		"--json", "number,title,url,mergeable,statusCheckRollup")
	if err != nil {
		return nil, fmt.Errorf("fetching PR %s#%d: %w", repoFull, number, err)
	}

	var pr prResponse
	if err := json.Unmarshal(stdout.Bytes(), &pr); err != nil {
		return nil, fmt.Errorf("parsing PR %s#%d: %w", repoFull, number, err)
	}
	return &pr, nil
}

// determineCIStatus evaluates the overall CI status from status checks.
func determineCIStatus(checks []struct {
	State      string `json:"state"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}) string {
	if len(checks) == 0 {
		return "pending"
	}

	hasFailure := false
	hasPending := false

	for _, check := range checks {
		// Check conclusion first (for completed checks)
		switch check.Conclusion {
		case "failure", "cancelled", "timed_out", "action_required": //nolint:misspell // GitHub API returns "cancelled" (British spelling)
			hasFailure = true
		case "success", "skipped", "neutral":
			// Pass
		default:
			// Check status for in-progress checks
			switch check.Status {
			case "queued", "in_progress", "waiting", "pending", "requested":
				hasPending = true
			}
			// Also check state field
			switch check.State {
			case "FAILURE", "ERROR":
				hasFailure = true
			case "PENDING", "EXPECTED":
				hasPending = true
			}
		}
	}

	if hasFailure {
		return "fail"
	}
	if hasPending {
		return "pending"
	}
	return "pass"
}

// determineMergeableStatus converts GitHub's mergeable field to display value.
func determineMergeableStatus(mergeable string) string {
	switch strings.ToUpper(mergeable) {
	case "MERGEABLE":
		return "ready"
	case "CONFLICTING":
		return "conflict"
	default:
		return "pending"
	}
}

// determineColorClass determines the row color based on CI and merge status.
func determineColorClass(ciStatus, mergeable string) string {
	if ciStatus == "fail" || mergeable == "conflict" {
		return "mq-red"
	}
	if ciStatus == "pending" || mergeable == "pending" {
		return "mq-yellow"
	}
	if ciStatus == "pass" && mergeable == "ready" {
		return "mq-green"
	}
	return "mq-yellow"
}

// FetchWorkers fetches all running worker sessions (polecats and refinery) with activity data.
//
// The rows come from tmux, but the issue each worker is carrying comes from the
// beads stores, and a worker whose issue cannot be found renders as idle. That
// makes the assignment lookup's failures part of this panel's honesty: a rig
// store that could not answer is carried out in the result rather than turning
// its workers idle without saying so. See getAssignedIssuesMap.
//
// The two failure channels are different facts and both are needed. The returned
// error says the tmux query failed, so there is no worker list at all; the
// result's FailedStores say the list was built but some rig could not name the
// bead its workers are carrying. Neither can stand in for the other.
func (f *LiveConvoyFetcher) FetchWorkers() (StoreResult[WorkerRow], error) {
	// Load registered rigs to filter sessions
	rigsConfigPath := filepath.Join(f.townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return StoreResult[WorkerRow]{}, fmt.Errorf("loading rigs config: %w", err)
	}

	// Build set of registered rig names
	registeredRigs := make(map[string]bool)
	for rigName := range rigsConfig.Rigs {
		registeredRigs[rigName] = true
	}

	// Pre-fetch assigned issues map: assignee -> (issueID, title)
	assigned := f.getAssignedIssuesMap()
	assignedIssues := assignedIssuesByAssignee(assigned.Rows)

	// Query all tmux sessions with window_activity for more accurate timing
	stdout, err := f.runTmuxCmd("list-sessions", "-F", "#{session_name}|#{window_activity}")
	if err != nil {
		if tmuxServerAbsent(err) {
			// No tmux server: there really are no workers. There are no worker
			// rows to caveat, so the assignment lookup's caveats are dropped
			// with them.
			return StoreResult[WorkerRow]{}, nil
		}
		// Any other tmux failure means the worker list is unknown, not empty.
		return StoreResult[WorkerRow]{}, fmt.Errorf("listing worker sessions: %w", err)
	}

	// Pre-fetch merge queue count to determine refinery idle status
	mergeQueueCount := f.getMergeQueueCount()

	var workers []WorkerRow
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}

		sessionName := parts[0]

		// Parse session name using the fetcher's own registry to avoid
		// dependency on global DefaultRegistry initialization (gt-y24).
		identity, err := session.ParseSessionNameWithRegistry(sessionName, f.registry)
		if err != nil {
			log.Printf("dashboard: FetchWorkers: skipping session %q: %v", sessionName, err)
			continue
		}

		rig := identity.Rig

		// Skip rigs not registered in this workspace
		if !registeredRigs[rig] {
			continue
		}

		// Skip non-worker sessions (witness, mayor, deacon, boot)
		switch identity.Role {
		case session.RoleMayor, session.RoleDeacon, session.RoleWitness:
			continue
		}

		// Determine agent type and worker name
		workerName := identity.Name
		agentType := constants.RolePolecat // Default for ephemeral sessions (polecats, crew)
		if identity.Role == session.RoleRefinery {
			agentType = constants.RoleRefinery
		}

		// Parse activity timestamp
		var activityUnix int64
		if _, err := fmt.Sscanf(parts[1], "%d", &activityUnix); err != nil || activityUnix == 0 {
			continue
		}
		activityTime := time.Unix(activityUnix, 0)
		activityAge := time.Since(activityTime)

		// Get status hint - special handling for refinery
		var statusHint string
		isRefinery := identity.Role == session.RoleRefinery
		if isRefinery {
			statusHint = f.getRefineryStatusHint(mergeQueueCount)
		} else {
			statusHint = f.getWorkerStatusHint(sessionName)
		}

		// Look up assigned issue for this worker
		// Assignee format: "rigname/polecats/workername"
		assignee := fmt.Sprintf("%s/polecats/%s", rig, workerName)
		var issueID, issueTitle string
		if issue, ok := assignedIssues[assignee]; ok {
			issueID = issue.ID
			issueTitle = issue.Title
			// Keep full title - CSS handles overflow
		}

		// Calculate work status based on activity age and issue assignment
		workStatus := calculateWorkerWorkStatus(activityAge, issueID, isRefinery, f.staleThreshold, f.stuckThreshold)

		workers = append(workers, WorkerRow{
			Name:         workerName,
			Rig:          rig,
			SessionID:    sessionName,
			LastActivity: activity.Calculate(activityTime),
			StatusHint:   statusHint,
			IssueID:      issueID,
			IssueTitle:   issueTitle,
			WorkStatus:   workStatus,
			AgentType:    agentType,
		})
	}

	return StoreResult[WorkerRow]{
		Rows:            workers,
		FailedStores:    assigned.FailedStores,
		TruncatedStores: assigned.TruncatedStores,
		UnreadStores:    assigned.UnreadStores,
	}, nil
}

// assignedIssue holds issue info for the assigned issues map.
type assignedIssue struct {
	ID    string
	Title string
}

// assignedBead is one bead somebody is currently working, as the Workers panel
// reads it from bd.
type assignedBead struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Assignee string `json:"assignee"`
}

// assignedStatuses are the statuses that mean "an agent is working this bead",
// matching beads.IssueStatus.IsAssigned.
//
// Both are asked for because Gas Town writes both: gt sling sets "hooked"
// (internal/beads/handoff.go), while the older assignment path sets
// "in_progress". internal/beads/beads.go:1573 already queries them together for
// the same reason. Measured against this town, not one assigned bead in any
// store was at "in_progress" while eleven sat at "hooked" — so asking for
// in_progress alone, as this lookup used to, found nothing at all.
var assignedStatuses = []string{string(beads.IssueStatusHooked), string(beads.StatusInProgress)}

// getAssignedIssuesMap returns the beads currently being worked, from every store.
//
// Assignments are per-rig data. A polecat's work bead lives in its own rig's
// store — measured against this town, the rigs held all 11 assigned beads and
// the town root held none — so the town-root-only query this replaced returned
// an empty map. That is not a cosmetic loss: FetchWorkers gives a worker with no
// issue an empty IssueID, and calculateWorkerWorkStatus reports empty as "idle".
// Every polecat in town rendered idle while working.
//
// The row budget is unlimited: this is a lookup table, not a display list, and a
// truncated one silently downgrades the workers it could not find to idle.
func (f *LiveConvoyFetcher) getAssignedIssuesMap() StoreResult[assignedBead] {
	var result StoreResult[assignedBead]
	for _, status := range assignedStatuses {
		result = result.merge(forEachStore(f, storeBudgetUnlimited, func(src storeSource, _ int) ([]assignedBead, error) {
			return f.listAssignedBeads(src.Dir, status)
		}))
	}
	return result
}

// listAssignedBeads reads one status from one store.
//
// Unlike the query it replaced, a bd failure is an error rather than an empty
// map: the resolver names the store that could not answer, so a rig whose store
// is unreachable stops looking like a rig whose workers are all idle.
func (f *LiveConvoyFetcher) listAssignedBeads(storeDir, status string) ([]assignedBead, error) {
	stdout, err := f.runBdCmd(storeDir, "list", "--status="+status, "--json", "--limit=0")
	if err != nil {
		return nil, fmt.Errorf("listing %s beads: %w", status, err)
	}

	var found []assignedBead
	if err := json.Unmarshal(stdout.Bytes(), &found); err != nil {
		return nil, fmt.Errorf("parsing %s beads: %w", status, err)
	}
	return found, nil
}

// assignedIssuesByAssignee indexes the union by assignee address.
//
// An agent holding more than one assigned bead keeps the first the union
// returned, which is store order: the town root, then rigs by name.
func assignedIssuesByAssignee(found []assignedBead) map[string]assignedIssue {
	result := make(map[string]assignedIssue, len(found))
	for _, bead := range found {
		if bead.Assignee == "" {
			continue
		}
		if _, seen := result[bead.Assignee]; seen {
			continue
		}
		result[bead.Assignee] = assignedIssue{ID: bead.ID, Title: bead.Title}
	}
	return result
}

// calculateWorkerWorkStatus determines the worker's work status based on activity and assignment.
// Returns: "working", "stale", "stuck", or "idle"
func calculateWorkerWorkStatus(activityAge time.Duration, issueID string, isRefinery bool, staleThreshold, stuckThreshold time.Duration) string {
	// Refinery has special handling - it's always "working" if it has PRs
	if isRefinery {
		return "working"
	}

	// No issue assigned = idle
	if issueID == "" {
		return "idle"
	}

	// Has issue - determine status based on activity
	switch {
	case activityAge < staleThreshold:
		return "working" // Active recently
	case activityAge < stuckThreshold:
		return "stale" // Might be thinking or stuck
	default:
		return "stuck" // Likely stuck - no activity for threshold+ minutes
	}
}

// getWorkerStatusHint captures the last non-empty line from a worker's pane.
func (f *LiveConvoyFetcher) getWorkerStatusHint(sessionName string) string {
	stdout, err := f.runTmuxCmd("capture-pane", "-t", sessionName, "-p", "-J")
	if err != nil {
		return ""
	}

	// Get last non-empty line
	lines := strings.Split(stdout.String(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			// Truncate long lines
			if len(line) > 60 {
				line = line[:57] + "..."
			}
			return line
		}
	}
	return ""
}

// mergeQueueCountUnknown marks a queue that could not be read. It is distinct
// from zero because the refinery's hint reads zero as "idle", which is a claim
// about the refinery made on the strength of a query that never answered.
const mergeQueueCountUnknown = -1

// getMergeQueueCount returns the total number of open PRs across all repos, or
// mergeQueueCountUnknown when the queue could not be read.
func (f *LiveConvoyFetcher) getMergeQueueCount() int {
	mergeQueue, err := f.FetchMergeQueue()
	if err != nil {
		log.Printf("dashboard: merge queue count unavailable: %v", err)
		return mergeQueueCountUnknown
	}
	return len(mergeQueue.Rows)
}

// getRefineryStatusHint returns appropriate status for refinery based on merge queue.
func (f *LiveConvoyFetcher) getRefineryStatusHint(mergeQueueCount int) string {
	switch {
	case mergeQueueCount == mergeQueueCountUnknown:
		return "Merge queue unreadable"
	case mergeQueueCount == 0:
		return "Idle - Waiting for PRs"
	case mergeQueueCount == 1:
		return "Processing 1 PR"
	}
	return fmt.Sprintf("Processing %d PRs", mergeQueueCount)
}

// parseActivityTimestamp parses a Unix timestamp string from tmux.
// Returns (0, false) for invalid or zero timestamps.
func parseActivityTimestamp(s string) (int64, bool) {
	var unix int64
	if _, err := fmt.Sscanf(s, "%d", &unix); err != nil || unix <= 0 {
		return 0, false
	}
	return unix, true
}

// mailFetchLimit caps the mail query. Unlike the caps on the other panels this
// one is meant: the panel shows recent traffic, and the town root held 386
// message beads when this was measured, nearly all of them long read.
//
// A deliberate cap is still a cap. len(rows) reaching it means the query came
// back full and the panel is showing a floor, which is why the handler turns
// this number into MailTruncated rather than letting the count render bare.
const mailFetchLimit = 50

// FetchMail fetches recent mail messages from the beads database.
func (f *LiveConvoyFetcher) FetchMail() ([]MailRow, error) {
	// List all message issues (mail)
	stdout, err := f.runBdCmd(f.townRoot, "list", "--label=gt:message", "--json", fmt.Sprintf("--limit=%d", mailFetchLimit))
	if err != nil {
		return nil, fmt.Errorf("listing mail: %w", err)
	}

	var messages []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Status    string   `json:"status"`
		CreatedAt string   `json:"created_at"`
		Priority  int      `json:"priority"`
		Assignee  string   `json:"assignee"`   // "to" address stored here
		CreatedBy string   `json:"created_by"` // "from" address
		Labels    []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &messages); err != nil {
		return nil, fmt.Errorf("parsing mail list: %w", err)
	}

	rows := make([]MailRow, 0, len(messages))
	for _, m := range messages {
		// Parse timestamp
		var timestamp time.Time
		var age string
		var sortKey int64
		if m.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
				timestamp = t
				age = formatTimestamp(t)
				sortKey = t.Unix()
			}
		}

		// Determine priority string
		priorityStr := "normal"
		switch m.Priority {
		case 0:
			priorityStr = "urgent"
		case 1:
			priorityStr = "high"
		case 2:
			priorityStr = "normal"
		case 3, 4:
			priorityStr = "low"
		}

		// Determine message type from labels
		msgType := "notification"
		for _, label := range m.Labels {
			if label == "task" || label == "reply" || label == "scavenge" {
				msgType = label
				break
			}
		}

		// Format from/to addresses for display
		from := formatAgentAddress(m.CreatedBy)
		to := formatAgentAddress(m.Assignee)

		rows = append(rows, MailRow{
			ID:        m.ID,
			From:      from,
			FromRaw:   m.CreatedBy,
			To:        to,
			Subject:   m.Title,
			Timestamp: timestamp.Format("15:04"),
			Age:       age,
			Priority:  priorityStr,
			Type:      msgType,
			Read:      m.Status == "closed",
			SortKey:   sortKey,
		})
	}

	// Sort by timestamp, newest first
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].SortKey > rows[j].SortKey
	})

	return rows, nil
}

// formatMailAge returns a human-readable age string.
func formatMailAge(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// formatTimestamp formats a time as "Jan 26, 3:45 PM" (or "Jan 26 2006, 3:45 PM" if different year).
func formatTimestamp(t time.Time) string {
	now := time.Now()
	if t.Year() != now.Year() {
		return t.Format("Jan 2 2006, 3:04 PM")
	}
	return t.Format("Jan 2, 3:04 PM")
}

// formatAgentAddress shortens agent addresses for display.
// "gastown/polecats/Toast" -> "Toast (gastown)"
// "mayor/" -> "Mayor"
func formatAgentAddress(addr string) string {
	if addr == "" {
		return "—"
	}
	if addr == "mayor/" || addr == "mayor" {
		return "Mayor"
	}

	parts := strings.Split(addr, "/")
	if len(parts) >= 3 && parts[1] == "polecats" {
		return fmt.Sprintf("%s (%s)", parts[2], parts[0])
	}
	if len(parts) >= 3 && parts[1] == "crew" {
		return fmt.Sprintf("%s (%s/crew)", parts[2], parts[0])
	}
	if len(parts) >= 2 {
		return fmt.Sprintf("%s/%s", parts[0], parts[len(parts)-1])
	}
	return addr
}

// FetchRigs returns all registered rigs with their agent counts.
func (f *LiveConvoyFetcher) FetchRigs() ([]RigRow, error) {
	// Load rigs config from mayor/rigs.json
	rigsConfigPath := filepath.Join(f.townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("loading rigs config: %w", err)
	}

	var rows []RigRow
	for name, entry := range rigsConfig.Rigs {
		row := RigRow{
			Name:   name,
			GitURL: entry.GitURL,
		}

		rigPath := filepath.Join(f.townRoot, name)

		// Count polecats
		polecatsDir := filepath.Join(rigPath, "polecats")
		if entries, err := os.ReadDir(polecatsDir); err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					row.PolecatCount++
				}
			}
		}

		// Count crew
		crewDir := filepath.Join(rigPath, "crew")
		if entries, err := os.ReadDir(crewDir); err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					row.CrewCount++
				}
			}
		}

		// Check for witness
		witnessPath := filepath.Join(rigPath, "witness")
		if _, err := os.Stat(witnessPath); err == nil {
			row.HasWitness = true
		}

		// Check for refinery
		refineryPath := filepath.Join(rigPath, "refinery", "rig")
		if _, err := os.Stat(refineryPath); err == nil {
			row.HasRefinery = true
		}

		rows = append(rows, row)
	}

	// Sort by name
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})

	return rows, nil
}

// FetchDogs returns all dogs in the kennel with their state.
func (f *LiveConvoyFetcher) FetchDogs() ([]DogRow, error) {
	kennelPath := filepath.Join(f.townRoot, "deacon", "dogs")

	entries, err := os.ReadDir(kennelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No kennel yet
		}
		return nil, fmt.Errorf("reading kennel: %w", err)
	}

	var rows []DogRow
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}

		// Read dog state file
		stateFile := filepath.Join(kennelPath, name, ".dog.json")
		data, err := os.ReadFile(stateFile)
		if err != nil {
			continue // Not a valid dog
		}

		var state struct {
			Name       string            `json:"name"`
			State      string            `json:"state"`
			LastActive time.Time         `json:"last_active"`
			Work       string            `json:"work,omitempty"`
			Worktrees  map[string]string `json:"worktrees,omitempty"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}

		rows = append(rows, DogRow{
			Name:       state.Name,
			State:      state.State,
			Work:       state.Work,
			LastActive: formatTimestamp(state.LastActive),
			RigCount:   len(state.Worktrees),
		})
	}

	// Sort by name
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})

	return rows, nil
}

// FetchEscalations returns open escalations needing attention.
//
// A failed query is an error, never an empty list. This is the panel whose
// whole purpose is to surface problems, so it is the one place where "I see
// nothing" must not render as "there is nothing": bd being unreachable is
// exactly when escalations are most likely to exist, and swallowing the failure
// made the town look calmest at its worst moment (gt-edty).
func (f *LiveConvoyFetcher) FetchEscalations() ([]EscalationRow, error) {
	// List open escalations.
	//
	// --limit=0 is load-bearing, not tidiness: `bd list` defaults to 50, so
	// omitting the flag capped this query silently. The escalation count is a
	// banner stat and the banner had no way to say it was short — a town with
	// 60 open escalations would have displayed 50, in the same typeface as a
	// measured count, and the ten it could not see are the ten nobody is
	// coming for. An unbounded query cannot be truncated, so there is nothing
	// to mark; the count is a true count (gt-skzk.2).
	//
	// The pinned half is asked for separately for the same reason `--limit=0`
	// is passed: bd's default `--status=open` is silently `--no-pinned`, and it
	// has no include-pinned flag. Measured on hq 2026-08-26 the single default
	// query returned 1 of the 4 open escalations, the other 3 being pinned —
	// two of them HIGH. Truncation at least has a cause a reader could guess;
	// this one drops precisely the rows an operator pinned to keep in view, and
	// the panel renders the town calmest at its worst moment (gt-z5h7, and the
	// same shape as gt-edty and gt-qee3). Unioned by ID rather than switched
	// between, so it stays correct if bd's default ever changes.
	type escalationIssue struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		CreatedAt   string   `json:"created_at"`
		CreatedBy   string   `json:"created_by"`
		Labels      []string `json:"labels"`
		Description string   `json:"description"`
	}

	var issues []escalationIssue
	seen := make(map[string]bool)
	for _, pinnedFilter := range []string{"--no-pinned", "--pinned"} {
		stdout, err := f.runBdCmd(f.townRoot, "list", "--label=gt:escalation", "--status=open", pinnedFilter, "--json", "--limit=0")
		if err != nil {
			return nil, fmt.Errorf("listing escalations (%s): %w", pinnedFilter, err)
		}

		var half []escalationIssue
		if err := json.Unmarshal(stdout.Bytes(), &half); err != nil {
			return nil, fmt.Errorf("parsing escalations (%s): %w", pinnedFilter, err)
		}
		for _, issue := range half {
			if seen[issue.ID] {
				continue
			}
			seen[issue.ID] = true
			issues = append(issues, issue)
		}
	}

	var rows []EscalationRow
	for _, issue := range issues {
		row := EscalationRow{
			ID:          issue.ID,
			Title:       issue.Title,
			EscalatedBy: formatAgentAddress(issue.CreatedBy),
			Severity:    "medium", // default
		}

		// Parse severity from labels
		for _, label := range issue.Labels {
			if strings.HasPrefix(label, "severity:") {
				row.Severity = strings.TrimPrefix(label, "severity:")
			}
			if label == "acked" {
				row.Acked = true
			}
		}

		// Calculate age
		if issue.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, issue.CreatedAt); err == nil {
				row.Age = formatTimestamp(t)
			}
		}

		rows = append(rows, row)
	}

	// Sort by severity (critical first), then by age
	severityOrder := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	sort.Slice(rows, func(i, j int) bool {
		si, sj := severityOrder[rows[i].Severity], severityOrder[rows[j].Severity]
		return si < sj
	})

	return rows, nil
}

// FetchHealth returns system health status.
//
// Only an absent file is a real answer here. A heartbeat that cannot be read or
// parsed used to arrive at the banner as "no heartbeat" or as a zero cycle —
// both of which are claims about the Deacon, made from a file this never
// managed to look at (gt-xw1t).
func (f *LiveConvoyFetcher) FetchHealth() (*HealthRow, error) {
	row := &HealthRow{}

	// Read deacon heartbeat
	heartbeatFile := filepath.Join(f.townRoot, "deacon", "heartbeat.json")
	data, err := os.ReadFile(heartbeatFile)
	switch {
	case err == nil:
		var hb struct {
			LastHeartbeat   time.Time `json:"timestamp"`
			Cycle           int64     `json:"cycle"`
			HealthyAgents   int       `json:"healthy_agents"`
			UnhealthyAgents int       `json:"unhealthy_agents"`
		}
		if err := json.Unmarshal(data, &hb); err != nil {
			return nil, fmt.Errorf("parsing deacon heartbeat: %w", err)
		}
		row.DeaconCycle = hb.Cycle
		row.HealthyAgents = hb.HealthyAgents
		row.UnhealthyAgents = hb.UnhealthyAgents
		if !hb.LastHeartbeat.IsZero() {
			age := time.Since(hb.LastHeartbeat)
			row.DeaconHeartbeat = formatTimestamp(hb.LastHeartbeat)
			row.HeartbeatFresh = age < f.heartbeatFreshThreshold
		} else {
			row.DeaconHeartbeat = "no timestamp"
		}
	case os.IsNotExist(err):
		// The Deacon has never beaten. That is a fact about the town.
		row.DeaconHeartbeat = "no heartbeat"
	default:
		return nil, fmt.Errorf("reading deacon heartbeat: %w", err)
	}

	// Check pause state. A pause file that cannot be read is not an unpaused
	// town: "not paused" is what the banner shows when this field is false.
	pauseFile := filepath.Join(f.townRoot, ".runtime", "deacon", "paused.json")
	pauseData, err := os.ReadFile(pauseFile)
	switch {
	case err == nil:
		var pause struct {
			Paused bool   `json:"paused"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(pauseData, &pause); err != nil {
			return nil, fmt.Errorf("parsing deacon pause state: %w", err)
		}
		row.IsPaused = pause.Paused
		row.PauseReason = pause.Reason
	case os.IsNotExist(err):
		// No pause file is the ordinary running state.
	default:
		return nil, fmt.Errorf("reading deacon pause state: %w", err)
	}

	return row, nil
}

// FetchQueues returns work queues and their status.
//
// A bd failure is an error, not an empty queue list: "bd not available" and "no
// queues" are different facts, and only one of them means the panel can be
// trusted.
func (f *LiveConvoyFetcher) FetchQueues() ([]QueueRow, error) {
	// List queue beads. --limit=0 for the same reason FetchEscalations carries
	// it: bd's own default of 50 would cap this read with nothing on the page
	// to say so.
	stdout, err := f.runBdCmd(f.townRoot, "list", "--label=gt:queue", "--json", "--limit=0")
	if err != nil {
		return nil, fmt.Errorf("listing queues: %w", err)
	}

	var queues []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Status      string `json:"status"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &queues); err != nil {
		return nil, fmt.Errorf("parsing queues: %w", err)
	}

	var rows []QueueRow
	for _, q := range queues {
		row := QueueRow{
			Name:   q.Title,
			Status: q.Status,
		}

		// Parse counts from description (key: value format)
		// Best-effort parsing - ignore Sscanf errors as missing/malformed data is acceptable
		for _, line := range strings.Split(q.Description, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "available_count:") {
				_, _ = fmt.Sscanf(line, "available_count: %d", &row.Available)
			} else if strings.HasPrefix(line, "processing_count:") {
				_, _ = fmt.Sscanf(line, "processing_count: %d", &row.Processing)
			} else if strings.HasPrefix(line, "completed_count:") {
				_, _ = fmt.Sscanf(line, "completed_count: %d", &row.Completed)
			} else if strings.HasPrefix(line, "failed_count:") {
				_, _ = fmt.Sscanf(line, "failed_count: %d", &row.Failed)
			} else if strings.HasPrefix(line, "status:") {
				// Override with parsed status if present
				var s string
				_, _ = fmt.Sscanf(line, "status: %s", &s)
				if s != "" {
					row.Status = s
				}
			}
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// FetchSessions returns active tmux sessions with role detection.
func (f *LiveConvoyFetcher) FetchSessions() ([]SessionRow, error) {
	// List tmux sessions
	stdout, err := f.runTmuxCmd("list-sessions", "-F", "#{session_name}:#{session_activity}")
	if err != nil {
		if tmuxServerAbsent(err) {
			return nil, nil // No tmux server: there really are no sessions.
		}
		// Any other tmux failure means the session list is unknown, not empty.
		return nil, fmt.Errorf("listing tmux sessions: %w", err)
	}

	var rows []SessionRow
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}

		// SplitN always returns >= 1 element; parts[0] is safe unconditionally
		parts := strings.SplitN(line, ":", 2)
		name := parts[0]

		// Only include Gas Town sessions
		if !session.IsKnownSession(name) {
			continue
		}

		row := SessionRow{
			Name:    name,
			IsAlive: true, // Session exists
		}

		// Parse activity timestamp
		if len(parts) > 1 {
			if ts, ok := parseActivityTimestamp(parts[1]); ok && ts > 0 {
				row.Activity = formatTimestamp(time.Unix(ts, 0))
			}
		}

		// Detect role from session name using fetcher's own registry (gt-y24)
		if identity, err := session.ParseSessionNameWithRegistry(name, f.registry); err == nil {
			row.Rig = identity.Rig
			row.Role = string(identity.Role)
			row.Worker = identity.Name
		}

		rows = append(rows, row)
	}

	// Sort by rig, then role, then worker
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Rig != rows[j].Rig {
			return rows[i].Rig < rows[j].Rig
		}
		if rows[i].Role != rows[j].Role {
			return rows[i].Role < rows[j].Role
		}
		return rows[i].Worker < rows[j].Worker
	})

	return rows, nil
}

// FetchHooks returns all hooked beads (work pinned to agents), from every store.
//
// Hooks are per-rig data: a polecat's hook bead lives in its own rig's store,
// not in the town root. Measured against this town, the town root held 0 hooked
// beads while the gastown rig held 5 — so the town-root-only query this
// replaced rendered "no work is hooked anywhere" on a town with five hooked
// beads. Every hooked bead in every store is the panel's actual subject.
func (f *LiveConvoyFetcher) FetchHooks() (StoreResult[HookRow], error) {
	result := forEachStore(f, storeBudgetUnlimited, func(src storeSource, _ int) ([]HookRow, error) {
		return f.listHookRows(src.Dir)
	})

	// Sort by stale first (stuck work), then by age
	sort.Slice(result.Rows, func(i, j int) bool {
		if result.Rows[i].IsStale != result.Rows[j].IsStale {
			return result.Rows[i].IsStale // Stale items first
		}
		return result.Rows[i].Age > result.Rows[j].Age
	})

	return result, nil
}

// listHookRows reads the hooked beads of one store.
//
// Unlike the query it replaced, a bd failure here is an error rather than an
// empty slice: the resolver names the store that could not answer, and a store
// that returned nothing because it broke must not read as a store that returned
// nothing because it was empty.
func (f *LiveConvoyFetcher) listHookRows(storeDir string) ([]HookRow, error) {
	stdout, err := f.runBdCmd(storeDir, "list", "--status=hooked", "--json", "--limit=0")
	if err != nil {
		return nil, fmt.Errorf("listing hooked beads: %w", err)
	}

	var beads []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Assignee  string `json:"assignee"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &beads); err != nil {
		return nil, fmt.Errorf("parsing hooked beads: %w", err)
	}

	rows := make([]HookRow, 0, len(beads))
	for _, bead := range beads {
		row := HookRow{
			ID:       bead.ID,
			Title:    bead.Title,
			Assignee: bead.Assignee,
			Agent:    formatAgentAddress(bead.Assignee),
		}

		// Keep full title - CSS handles overflow

		// Calculate age and stale status
		if bead.UpdatedAt != "" {
			if t, err := time.Parse(time.RFC3339, bead.UpdatedAt); err == nil {
				age := time.Since(t)
				row.Age = formatTimestamp(t)
				row.IsStale = age > time.Hour // Stale if hooked > 1 hour
			}
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// FetchMayor returns the Mayor's current status.
//
// This is the sharpest case of the swallowed-error family: a tmux the
// dashboard could not ask used to be rendered as the specific, confident claim
// that the Mayor is detached. Only an absent tmux server supports that claim —
// every other failure leaves the Mayor's state unknown.
func (f *LiveConvoyFetcher) FetchMayor() (*MayorStatus, error) {
	status := &MayorStatus{
		IsAttached: false,
	}

	// Get the actual mayor session name (e.g., "hq-mayor")
	mayorSessionName := session.MayorSessionName()

	// Check if mayor tmux session exists
	stdout, err := f.runTmuxCmd("list-sessions", "-F", "#{session_name}:#{session_activity}")
	if err != nil {
		if tmuxServerAbsent(err) {
			return status, nil // No tmux server: the Mayor really is detached.
		}
		return nil, fmt.Errorf("checking mayor session: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, mayorSessionName+":") {
			status.IsAttached = true
			status.SessionName = mayorSessionName

			// Parse activity timestamp
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				if activityTs, ok := parseActivityTimestamp(parts[1]); ok {
					age := time.Since(time.Unix(activityTs, 0))
					status.LastActivity = formatTimestamp(time.Unix(activityTs, 0))
					status.IsActive = age < f.mayorActiveThreshold
				}
			}
			break
		}
	}

	if status.IsAttached {
		status.Runtime = f.resolveMayorRuntime(mayorSessionName)
	}

	return status, nil
}

func (f *LiveConvoyFetcher) resolveMayorRuntime(sessionName string) string {
	if agentName, err := fetcherGetSessionEnv(sessionName, "GT_AGENT"); err == nil && strings.TrimSpace(agentName) != "" {
		agentName = strings.TrimSpace(agentName)
		rc, _, resolveErr := config.ResolveAgentConfigWithOverride(f.townRoot, "", agentName)
		if resolveErr == nil {
			return runtimeLabelForRuntimeConfig(rc, agentName)
		}
		if roleRC := config.ResolveRoleAgentConfig(constants.RoleMayor, f.townRoot, ""); roleRC != nil && strings.TrimSpace(roleRC.ResolvedAgent) == agentName {
			return runtimeLabelForRuntimeConfig(roleRC, agentName)
		}
		return agentName
	}

	return runtimeLabelForRuntimeConfig(config.ResolveRoleAgentConfig(constants.RoleMayor, f.townRoot, ""), "")
}

func runtimeLabelForRuntimeConfig(rc *config.RuntimeConfig, fallback string) string {
	if rc == nil {
		if fallback != "" {
			return fallback
		}
		return "claude"
	}
	if fallback == "" {
		fallback = rc.ResolvedAgent
	}
	return runtimeLabelFromConfig(rc.Command, rc.Args, fallback)
}

func runtimeLabelFromConfig(command string, args []string, fallback string) string {
	command = strings.TrimSpace(command)
	cmd := ""
	if command != "" {
		cmd = strings.TrimSpace(filepath.Base(command))
	}
	if cmd == "" {
		cmd = fallback
	}
	if cmd == "" {
		cmd = "claude"
	}
	if cmd == "cgroup-wrap" && len(args) > 0 {
		cmd = filepath.Base(args[0])
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "--model" || arg == "-m") && i+1 < len(args) && strings.TrimSpace(args[i+1]) != "" {
			return cmd + "/" + stripModelSuffix(strings.TrimSpace(args[i+1]))
		}
		if strings.HasPrefix(arg, "--model=") {
			if v := strings.TrimSpace(strings.TrimPrefix(arg, "--model=")); v != "" {
				return cmd + "/" + stripModelSuffix(v)
			}
		}
		if strings.HasPrefix(arg, "-m=") {
			if v := strings.TrimSpace(strings.TrimPrefix(arg, "-m=")); v != "" {
				return cmd + "/" + stripModelSuffix(v)
			}
		}
	}

	return cmd
}

// stripModelSuffix removes bracketed context-window hints (e.g. "[1m]")
// from model names so the dashboard label stays human-readable.
// "sonnet[1m]" → "sonnet", "opus" → "opus".
func stripModelSuffix(model string) string {
	if idx := strings.Index(model, "["); idx > 0 {
		return model[:idx]
	}
	return model
}

// issuesPerStoreLimit is a SAFETY CAP on the backlog fetch. It is not the size
// of the panel, and it must stay far above any real store's backlog.
//
// It used to be 50 per store, and the number the dashboard showed was the size
// of that sample presented as the size of the backlog. Measured 2026-08-25, the
// gastown rig held 59 non-internal open beads against a cap of 50, so it
// contributed exactly 50 whether it held 51 or 500: closing a bead only slid
// the next one into the sampled window, and the Work count could not fall until
// the store dropped below 50. The town root failed the other way — 28 of the 50
// rows it contributed were gt:message, fetched, counted against the cap and
// only then hidden, so it displayed 22 of its 186 real work items and NEW MAIL
// PUSHED WORK OUT OF THE SAMPLE. The displayed number fell as the backlog rose
// (gt-eolg).
//
// A cap this high is affordable because the panel reads six scalar fields and
// asks bd for nothing else: --brief took the town root's unlimited open query
// from 2.4MB/0.80s to 414KB/0.09s, the same cost as the 50-row query it
// replaces. A store that returns this many rows is still recorded in
// TruncatedStores, which is what keeps the count an explicit floor rather than
// a wrong number.
//
// It is set far above the largest store measured (the town root, 620 open beads
// including its mail) rather than just above it, because the cost of the fetch
// scales with the rows a store actually holds and not with the cap: raising it
// is free until the day it is needed, and the day it is needed is the day the
// count would otherwise start lying again.
const issuesPerStoreLimit = 5000

// FetchIssues returns open and hooked issues (the backlog) from every store.
//
// Rows is the whole backlog, not a page of it. The caller decides how much of
// it to render; len(Rows) is the count an operator can act on, and it falls
// when work is closed. Trimming for display belongs at the render site so the
// two numbers stay visibly distinct.
//
// Issues are per-rig data: measured against this town, the town root held 521
// open beads while the rigs held 65, 7 and 2 that this panel never showed.
func (f *LiveConvoyFetcher) FetchIssues() (StoreResult[IssueRow], error) {
	// Query both open AND hooked issues for the Work panel.
	// Open = ready to assign, Hooked = in progress.
	//
	// One union per status, joined after: a single pass returning both would
	// make the per-store row limit a limit on the two statuses combined, so a
	// store with 50 open beads would report its hooked beads as truncated away.
	open := forEachStorePerStore(f, issuesPerStoreLimit, func(src storeSource, limit int) ([]issueBead, error) {
		return f.listIssueBeads(src.Dir, "open", limit)
	})
	hooked := forEachStorePerStore(f, issuesPerStoreLimit, func(src storeSource, limit int) ([]issueBead, error) {
		return f.listIssueBeads(src.Dir, "hooked", limit)
	})

	// Internal beads are dropped here rather than in the fetch so the resolver
	// counts what bd actually returned when it decides a store was truncated.
	// The town root is mostly gt:message rows, so a store can fill its whole
	// allowance and still show almost nothing — that is a truncated store, and
	// filtering first would have hidden it.
	//
	// This also keeps isInternal the single authority on what counts as work.
	// Excluding the gt: labels in the bd query would be cheaper, but then the
	// count and the rows would answer to two different predicates, and any drift
	// between them would be invisible on the page.
	result := mapStoreRows(open.merge(hooked), func(b issueBead) (IssueRow, bool) {
		if b.isInternal() {
			return IssueRow{}, false
		}
		return b.row(), true
	})

	// Sort by priority (1=critical first), then by age
	sort.Slice(result.Rows, func(i, j int) bool {
		pi, pj := result.Rows[i].Priority, result.Rows[j].Priority
		if pi == 0 {
			pi = 5 // Treat unset priority as low
		}
		if pj == 0 {
			pj = 5
		}
		if pi != pj {
			return pi < pj
		}
		return result.Rows[i].Age > result.Rows[j].Age // Older first for same priority
	})

	return result, nil
}

// issueBead is one bead as the backlog panel reads it from bd, before the
// panel decides whether to display it.
//
// Type is tagged issue_type because that is the key `bd list --json` emits;
// there is no "type" key in its output at all. Tagged "type", the field decoded
// as "" on every bead ever fetched, which made the type arm of isInternal
// unreachable — a merge-request bead carrying no gt: label counted as work and
// nothing showed it.
type issueBead struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Type      string   `json:"issue_type"`
	Priority  int      `json:"priority"`
	Labels    []string `json:"labels"`
	CreatedAt string   `json:"created_at"`
}

// isInternal reports whether the bead is Gas Town plumbing — messages,
// convoys, queues, merge requests, wisps, agent identities — rather than work
// someone filed. Both the legacy type field and the gt: labels are checked.
func (b issueBead) isInternal() bool {
	switch b.Type {
	case "message", "convoy", "queue", "merge-request", "wisp", "agent":
		return true
	}
	for _, l := range b.Labels {
		switch l {
		case "gt:message", "gt:convoy", "gt:queue", "gt:merge-request", "gt:wisp", "gt:agent":
			return true
		}
	}
	return false
}

// row renders the bead as a backlog row.
func (b issueBead) row() IssueRow {
	row := IssueRow{
		ID:       b.ID,
		Title:    b.Title,
		Type:     b.Type,
		Priority: b.Priority,
	}

	// Keep full title - CSS handles overflow

	// Format labels (skip internal labels)
	var displayLabels []string
	for _, label := range b.Labels {
		if !strings.HasPrefix(label, "gt:") && !strings.HasPrefix(label, "internal:") {
			displayLabels = append(displayLabels, label)
		}
	}
	if len(displayLabels) > 0 {
		row.Labels = strings.Join(displayLabels, ", ")
		if len(row.Labels) > 25 {
			row.Labels = row.Labels[:22] + "..."
		}
	}

	// Calculate age
	if b.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, b.CreatedAt); err == nil {
			row.Age = formatTimestamp(t)
		}
	}

	return row
}

// listIssueBeads reads one status from one store, unfiltered.
//
// Unlike the query it replaced, a bd failure is an error rather than a silently
// skipped list: the resolver names the store that could not answer, so a store
// that broke stops looking like a store that was empty.
//
// --brief drops the free-form text (description, design, notes, payload), none
// of which issueBead reads. It is what makes a backlog-sized limit affordable:
// on the town root's open beads it is the difference between 2.4MB in 0.80s and
// 414KB in 0.09s.
func (f *LiveConvoyFetcher) listIssueBeads(storeDir, status string, limit int) ([]issueBead, error) {
	stdout, err := f.runBdCmd(storeDir, "list", "--status="+status, "--json", "--brief", fmt.Sprintf("--limit=%d", limit))
	if err != nil {
		return nil, fmt.Errorf("listing %s beads: %w", status, err)
	}

	var beads []issueBead
	if err := json.Unmarshal(stdout.Bytes(), &beads); err != nil {
		return nil, fmt.Errorf("parsing %s beads: %w", status, err)
	}
	return beads, nil
}

// FetchActivity returns recent activity from the event log.
//
// A missing log is a town that has not logged anything yet; an unreadable one
// is a town whose history the dashboard cannot see. The split follows FetchDogs.
func (f *LiveConvoyFetcher) FetchActivity() ([]ActivityRow, error) {
	eventsPath := filepath.Join(f.townRoot, ".events.jsonl")

	// Read events file
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No events file yet — nothing has happened.
		}
		return nil, fmt.Errorf("reading event log: %w", err)
	}

	// An empty log yields one empty line, which the loop below skips. There is no
	// "no lines" case for strings.Split to produce, so the guard that used to
	// stand here could only ever have been a third spelling of "no rows, no
	// error" that never ran (gt-1jrl).
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")

	// Take last 50 events for richer timeline
	start := 0
	if len(lines) > 50 {
		start = len(lines) - 50
	}

	var rows []ActivityRow
	for i := len(lines) - 1; i >= start; i-- {
		line := lines[i]
		if line == "" {
			continue
		}

		var event struct {
			Timestamp  string                 `json:"ts"`
			Type       string                 `json:"type"`
			Actor      string                 `json:"actor"`
			Payload    map[string]interface{} `json:"payload"`
			Visibility string                 `json:"visibility"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		// Skip audit-only events
		if event.Visibility == "audit" {
			continue
		}

		row := ActivityRow{
			Type:         event.Type,
			Category:     eventCategory(event.Type),
			Actor:        formatAgentAddress(event.Actor),
			Rig:          extractRig(event.Actor),
			Icon:         eventIcon(event.Type),
			RawTimestamp: event.Timestamp,
		}

		// Calculate time ago
		if t, err := time.Parse(time.RFC3339, event.Timestamp); err == nil {
			row.Time = formatTimestamp(t)
		}

		// Generate human-readable summary
		row.Summary = eventSummary(event.Type, event.Actor, event.Payload)

		rows = append(rows, row)
	}

	return rows, nil
}

// eventCategory classifies an event type into a filter category.
func eventCategory(eventType string) string {
	switch eventType {
	case "spawn", "kill", "session_start", "session_end", "session_death", "mass_death", "nudge", "handoff":
		return "agent"
	case "sling", "hook", "unhook", "done", "merge_started", "merged", "merge_failed":
		return "work"
	case "mail", "escalation_sent", "escalation_acked", "escalation_closed":
		return "comms"
	case "boot", "halt", "patrol_started", "patrol_complete":
		return "system"
	default:
		return "system"
	}
}

// extractRig extracts the rig name from an actor address like "gastown/polecats/nux".
func extractRig(actor string) string {
	if actor == "" {
		return ""
	}
	parts := strings.SplitN(actor, "/", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// eventIcon returns an emoji for an event type.
func eventIcon(eventType string) string {
	icons := map[string]string{
		"sling":             "🎯",
		"hook":              "🪝",
		"unhook":            "🔓",
		"done":              "✅",
		"mail":              "📬",
		"spawn":             "🦨",
		"kill":              "💀",
		"nudge":             "👉",
		"handoff":           "🤝",
		"session_start":     "▶️",
		"session_end":       "⏹️",
		"session_death":     "☠️",
		"mass_death":        "💥",
		"patrol_started":    "🔍",
		"patrol_complete":   "✔️",
		"escalation_sent":   "⚠️",
		"escalation_acked":  "👍",
		"escalation_closed": "🔕",
		"merge_started":     "🔀",
		"merged":            "✨",
		"merge_failed":      "❌",
		"boot":              "🚀",
		"halt":              "🛑",
	}
	if icon, ok := icons[eventType]; ok {
		return icon
	}
	return "📋"
}

// eventSummary generates a human-readable summary for an event.
func eventSummary(eventType, actor string, payload map[string]interface{}) string {
	shortActor := formatAgentAddress(actor)

	switch eventType {
	case "sling":
		bead, _ := payload["bead"].(string)
		target, _ := payload["target"].(string)
		return fmt.Sprintf("%s slung to %s", bead, formatAgentAddress(target))
	case "done":
		bead, _ := payload["bead"].(string)
		return fmt.Sprintf("%s completed %s", shortActor, bead)
	case "mail":
		to, _ := payload["to"].(string)
		subject, _ := payload["subject"].(string)
		if len(subject) > 25 {
			subject = subject[:22] + "..."
		}
		return fmt.Sprintf("→ %s: %s", formatAgentAddress(to), subject)
	case "spawn":
		return fmt.Sprintf("%s spawned", shortActor)
	case "kill":
		return fmt.Sprintf("%s killed", shortActor)
	case "hook":
		bead, _ := payload["bead"].(string)
		return fmt.Sprintf("%s hooked %s", shortActor, bead)
	case "unhook":
		bead, _ := payload["bead"].(string)
		return fmt.Sprintf("%s unhooked %s", shortActor, bead)
	case "merged":
		branch, _ := payload["branch"].(string)
		return fmt.Sprintf("merged %s", branch)
	case "merge_failed":
		reason, _ := payload["reason"].(string)
		if len(reason) > 30 {
			reason = reason[:27] + "..."
		}
		return fmt.Sprintf("merge failed: %s", reason)
	case "escalation_sent":
		return "escalation created"
	case "session_death":
		role, _ := payload["role"].(string)
		return fmt.Sprintf("%s session died", formatAgentAddress(role))
	case "mass_death":
		count, _ := payload["count"].(float64)
		return fmt.Sprintf("%.0f sessions died", count)
	default:
		return eventType
	}
}
