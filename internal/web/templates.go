// Package web provides HTTP server and templates for the Gas Town dashboard.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"strings"

	"github.com/steveyegge/gastown/internal/activity"
)

//go:embed templates/*.html
var templateFS embed.FS

// ConvoyData represents data passed to the convoy template.
//
// Every panel whose source can fail carries the reason alongside its rows. The
// pairing is deliberate and load-bearing: len(nil) is 0 whether the town is
// quiet or the query never ran, so a panel handed rows alone cannot render the
// difference, and "could not read" reaches the operator as "nothing there".
type ConvoyData struct {
	Convoys []ConvoyRow
	// ConvoysUnavailable holds the reason the convoy query failed, or "" when it
	// succeeded. The circuit breaker makes this routine: while it is open no
	// query runs at all, so the panel knows nothing rather than nothing being there.
	ConvoysUnavailable string
	MergeQueue         []MergeQueueRow
	// MergeQueueFailedRigs names rigs whose merge-queue query errored. Non-empty
	// means the rendered count is a floor, not a total.
	MergeQueueFailedRigs []string
	// MergeQueueUnavailable holds the reason no rig could be asked at all, or ""
	// when the queue was read. It is the floor of the same scale
	// MergeQueueFailedRigs measures: a count short by every rig is not a count,
	// and "Merge queue empty" is the most consequential thing this dashboard can
	// wrongly claim — it is what an operator checks before concluding that work
	// has stopped arriving.
	MergeQueueUnavailable string
	Workers               []WorkerRow
	// WorkersUnavailable holds the reason the worker query failed, or "" when it
	// succeeded. An unaskable tmux is not a town with no polecats.
	WorkersUnavailable string
	// WorkersWarning names the stores whose assigned-bead query did not fully
	// answer. Non-empty means some workers may be shown as idle only because
	// the bead they are carrying could not be read.
	//
	// It is a separate field from WorkersUnavailable because it describes a
	// different source failing in a different way — the worker list is present
	// and complete, only the beads behind it are partial. Collapsing the two
	// would lose whichever fact the survivor was not about.
	WorkersWarning string
	Mail           []MailRow
	// MailUnavailable holds the reason the mail query failed, or "" when it
	// succeeded. A town whose bd cannot be reached has not gone quiet.
	MailUnavailable string
	Rigs            []RigRow
	// RigsUnavailable holds the reason the rig list could not be read, or ""
	// when it was read — including when the town genuinely has no rigs
	// registered, which is a real zero rather than an unknown one.
	RigsUnavailable string
	Dogs            []DogRow
	// DogsUnavailable holds the reason the kennel could not be read, or "" when
	// it was read — including when there is no kennel at all, which is a real
	// zero rather than an unknown one.
	DogsUnavailable string
	Escalations     []EscalationRow
	// EscalationsUnavailable holds the reason the escalation query failed, or
	// "" when it succeeded. Non-empty means the escalation count is unknown —
	// which the panel must render differently from a count of zero.
	EscalationsUnavailable string
	Health                 *HealthRow
	// HealthUnavailable holds the reason the heartbeat could not be read, or ""
	// when it was read. This one hides rather than lies: the banner renders its
	// liveness stat only when Health is non-nil, so a failed read used to delete
	// the indicator from the page — and an absent warning light reads as a
	// working one.
	HealthUnavailable string
	Queues            []QueueRow
	// QueuesUnavailable holds the reason the queue query failed, or "" when it
	// succeeded. The panel hides itself when there are no queues, so without
	// this a failed query would remove the panel from the page entirely.
	QueuesUnavailable string
	Sessions          []SessionRow
	// SessionsUnavailable holds the reason the session query failed, or "" when
	// it succeeded.
	SessionsUnavailable string
	Hooks               []HookRow
	// HooksWarning names the stores whose hooked-bead query did not fully
	// answer. Non-empty means the rendered count is a floor, not a total.
	HooksWarning string
	// HooksUnavailable holds the reason NO store could be read, or "" when at
	// least one answered. It is the end of the same scale HooksWarning starts:
	// a floor of zero drawn from no sources at all is not a floor, and the
	// panel must say so instead of rendering "No hooked work".
	HooksUnavailable string
	Mayor            *MayorStatus
	// MayorUnavailable holds the reason the Mayor lookup failed, or "" when it
	// succeeded. A nil Mayor with no reason is the banner's "Unknown" state; a
	// reason turns that into a stated one, so the banner never claims "Detached"
	// on the strength of a tmux it could not reach.
	MayorUnavailable string
	Issues           []IssueRow
	// IssuesWarning is the same caveat for the backlog union.
	IssuesWarning string
	// IssuesUnavailable is the same "no source answered" reason for the backlog
	// union.
	IssuesUnavailable string
	Activity          []ActivityRow
	// ActivityUnavailable holds the reason the event log could not be read, or
	// "" when it was read (including when it does not exist yet).
	ActivityUnavailable string
	Summary             *DashboardSummary
	Expand              string // Panel to show fullscreen (from ?expand=name)
	CSRFToken           string // Token for CSRF protection on POST requests
}

// RigRow represents a registered rig in the dashboard.
type RigRow struct {
	Name         string
	GitURL       string
	PolecatCount int
	CrewCount    int
	HasWitness   bool
	HasRefinery  bool
}

// DogRow represents a Deacon helper worker.
type DogRow struct {
	Name       string // Dog name (e.g., "alpha")
	State      string // idle, working
	Work       string // Current work assignment
	LastActive string // Formatted age (e.g., "5m ago")
	RigCount   int    // Number of worktrees
}

// EscalationRow represents an escalation needing attention.
type EscalationRow struct {
	ID          string
	Title       string
	Severity    string // critical, high, medium, low
	EscalatedBy string
	Age         string
	Acked       bool
}

// HealthRow represents system health status.
type HealthRow struct {
	DeaconHeartbeat string // Age of heartbeat (e.g., "2m ago")
	DeaconCycle     int64
	HealthyAgents   int
	UnhealthyAgents int
	IsPaused        bool
	PauseReason     string
	HeartbeatFresh  bool // true if < 5min old
}

// QueueRow represents a work queue.
type QueueRow struct {
	Name       string
	Status     string // active, paused, closed
	Available  int
	Processing int
	Completed  int
	Failed     int
}

// SessionRow represents a tmux session.
type SessionRow struct {
	Name     string // Session name (e.g., "gt-gastown-witness")
	Role     string // witness, refinery, polecat, crew, deacon
	Rig      string // Rig name if applicable
	Worker   string // Worker name for polecats/crew
	Activity string // Age since last activity
	IsAlive  bool   // Whether Claude is running in session
}

// HookRow represents a hooked bead (work pinned to an agent).
type HookRow struct {
	ID       string // Bead ID (e.g., "gt-abc12")
	Title    string // Work item title
	Assignee string // Agent address (e.g., "gastown/polecats/nux")
	Agent    string // Formatted agent name
	Age      string // Time since hooked
	IsStale  bool   // True if hooked > 1 hour (potentially stuck)
}

// MayorStatus represents the Mayor's current state.
type MayorStatus struct {
	IsAttached   bool   // True if gt-mayor tmux session exists
	SessionName  string // Tmux session name
	LastActivity string // Age since last activity
	IsActive     bool   // True if activity < 5 min (likely working)
	Runtime      string // Which runtime (claude, codex, etc.)
}

// IssueRow represents an open issue in the backlog.
type IssueRow struct {
	ID       string // Bead ID (e.g., "gt-abc12")
	Title    string // Issue title
	Type     string // issue, bug, feature, task
	Priority int    // 1=critical, 2=high, 3=medium, 4=low
	Age      string // Time since created
	Labels   string // Comma-separated labels
	Assignee string // Who it's hooked to (empty if unassigned)
}

// ActivityRow represents an event in the activity feed.
type ActivityRow struct {
	Time         string // Formatted time (e.g., "2m ago")
	Icon         string // Emoji for event type
	Type         string // Event type (sling, done, mail, etc.)
	Category     string // Event category for filtering (agent, work, comms, system)
	Actor        string // Who did it
	Rig          string // Rig name extracted from actor (e.g., "gastown")
	Summary      string // Human-readable description
	RawTimestamp string // ISO 8601 timestamp for JS sorting/filtering
}

// DashboardSummary provides at-a-glance stats and alerts.
type DashboardSummary struct {
	// Stats
	PolecatCount    int
	HookCount       int
	IssueCount      int
	ConvoyCount     int
	EscalationCount int

	// EscalationsUnavailable is true when the escalation query failed, making
	// EscalationCount and UnackedEscalations meaningless zeroes. It is itself an
	// alert: a dashboard that cannot see escalations is not "all clear".
	EscalationsUnavailable bool

	// PolecatsUnavailable and ConvoysUnavailable say the same thing about the
	// other two stats the banner prints. A count computed from rows a failed
	// query never returned is not a count, and the banner is where an operator
	// forms their impression of the town at a glance.
	PolecatsUnavailable bool
	ConvoysUnavailable  bool

	// HooksUnavailable and IssuesUnavailable say it about the two union-backed
	// stats. These panels read every store, so their stat reaches zero the
	// moment the last store fails — the failure mode is a banner that prints a
	// calm "0 Hooks / 0 Work" for a town whose beads are simply unreachable.
	HooksUnavailable  bool
	IssuesUnavailable bool

	// These four panels print no stat in the banner, so they are alert-only.
	// They are here because HasAlerts is what decides between "✓ All clear" and
	// a warning, and a panel the dashboard could not read is precisely the case
	// where "All clear" is a claim it has no basis for (gt-xw1t).
	MailUnavailable       bool
	RigsUnavailable       bool
	HealthUnavailable     bool
	MergeQueueUnavailable bool

	// Alerts (things needing attention)
	StuckPolecats      int // No activity > 5 min
	StaleHooks         int // Hooked > 1 hour
	UnackedEscalations int
	DeadSessions       int // Sessions that died recently
	HighPriorityIssues int // P1/P2 issues

	// Computed
	HasAlerts bool
}

// MailRow represents a mail message in the dashboard.
type MailRow struct {
	ID        string // Message ID (e.g., "hq-msg-abc123")
	From      string // Sender (e.g., "gastown/polecats/Toast")
	FromRaw   string // Raw sender address for color hashing
	To        string // Recipient (e.g., "mayor/")
	Subject   string // Message subject
	Timestamp string // Formatted timestamp
	Age       string // Human-readable age (e.g., "5m ago")
	Priority  string // low, normal, high, urgent
	Type      string // task, notification, reply
	Read      bool   // Whether message has been read
	SortKey   int64  // Unix timestamp for sorting
}

// WorkerRow represents a worker (polecat or refinery) in the dashboard.
type WorkerRow struct {
	Name         string        // e.g., "dag", "nux", "refinery"
	Rig          string        // e.g., "roxas", "gastown"
	SessionID    string        // e.g., "gt-roxas-dag"
	LastActivity activity.Info // Colored activity display
	StatusHint   string        // Last line from pane (optional)
	IssueID      string        // Currently assigned issue ID (e.g., "hq-1234")
	IssueTitle   string        // Issue title (truncated)
	WorkStatus   string        // working, stale, stuck, idle
	AgentType    string        // "polecat" (ephemeral sessions) or "refinery" (permanent)
}

// MergeQueueRow represents one merge-request bead in the merge queue.
//
// Rows come from rig-local MR beads — the same source `gt mq list <rig>` reads —
// so the dashboard and the CLI can never disagree (gt-4qp). GitHub PR data is
// enrichment layered on top, present only when the MR bead records a PR.
type MergeQueueRow struct {
	ID          string // MR bead ID (e.g., "gt-mr-abc12")
	Repo        string // Rig name (e.g., "roxas", "gastown")
	Title       string
	Branch      string // Source branch being merged
	Target      string // Target branch (e.g., "main")
	SourceIssue string // The work item being merged
	Worker      string // Who did the work
	RetryCount  int    // Conflict-resolution cycles so far
	ConvoyID    string // Parent convoy, if any
	Age         string // Human-readable age since submission

	// PR enrichment — populated only when the MR bead carries PRURL/PRNumber.
	HasPR      bool
	Number     int
	URL        string
	CIStatus   string // "pass", "fail", "pending"
	Mergeable  string // "ready", "conflict", "pending"
	ColorClass string // "mq-green", "mq-yellow", "mq-red"
}

// ConvoyRow represents a single convoy in the dashboard.
type ConvoyRow struct {
	ID            string
	Title         string
	Status        string // "open" or "closed" (raw beads status)
	WorkStatus    string // Computed: "complete", "active", "stale", "stuck", "waiting"
	Progress      string // e.g., "2/5"
	Completed     int
	Total         int
	ProgressPct   int      // 0-100, computed from Completed/Total
	ReadyBeads    int      // open beads with no assignee (available to pick up)
	InProgress    int      // beads currently being worked on
	Assignees     []string // unique assignees across tracked issues
	LastActivity  activity.Info
	TrackedIssues []TrackedIssue
}

// TrackedIssue represents an issue tracked by a convoy.
type TrackedIssue struct {
	ID       string
	Title    string
	Status   string
	Assignee string
}

// LoadTemplates loads and parses all HTML templates.
func LoadTemplates() (*template.Template, error) {
	// Define template functions
	funcMap := template.FuncMap{
		"activityClass":      activityClass,
		"statusClass":        statusClass,
		"workStatusClass":    workStatusClass,
		"senderColorClass":   senderColorClass,
		"severityClass":      severityClass,
		"dogStateClass":      dogStateClass,
		"queueStatusClass":   queueStatusClass,
		"polecatStatusClass": polecatStatusClass,
		"activityTypeClass":  activityTypeClass,
		"contains": func(s, substr string) bool {
			return strings.Contains(s, substr)
		},
	}

	// Get the templates subdirectory
	subFS, err := fs.Sub(templateFS, "templates")
	if err != nil {
		return nil, err
	}

	// Parse all templates
	tmpl, err := template.New("").Funcs(funcMap).ParseFS(subFS, "*.html")
	if err != nil {
		return nil, err
	}

	return tmpl, nil
}

// activityClass returns the CSS class for an activity color.
func activityClass(info activity.Info) string {
	switch info.ColorClass {
	case activity.ColorGreen:
		return "activity-green"
	case activity.ColorYellow:
		return "activity-yellow"
	case activity.ColorRed:
		return "activity-red"
	default:
		return "activity-unknown"
	}
}

// statusClass returns the CSS class for a convoy status.
func statusClass(status string) string {
	switch status {
	case "open":
		return "status-open"
	case "closed":
		return "status-closed"
	default:
		return "status-unknown"
	}
}

// workStatusClass returns the CSS class for a computed work status.
func workStatusClass(workStatus string) string {
	switch workStatus {
	case "complete":
		return "work-complete"
	case "active":
		return "work-active"
	case "stale":
		return "work-stale"
	case "stuck":
		return "work-stuck"
	case "waiting":
		return "work-waiting"
	default:
		return "work-unknown"
	}
}

// senderColorClass returns a CSS class for sender-based color coding.
// Uses a simple hash to assign consistent colors to each sender.
func senderColorClass(fromRaw string) string {
	if fromRaw == "" {
		return "sender-default"
	}
	// Simple hash: sum of bytes mod number of colors
	var sum int
	for _, b := range []byte(fromRaw) {
		sum += int(b)
	}
	colors := []string{
		"sender-cyan",
		"sender-purple",
		"sender-green",
		"sender-yellow",
		"sender-orange",
		"sender-blue",
		"sender-red",
		"sender-pink",
	}
	return colors[sum%len(colors)]
}

// severityClass returns CSS class for escalation severity.
func severityClass(severity string) string {
	switch severity {
	case "critical":
		return "severity-critical"
	case "high":
		return "severity-high"
	case "medium":
		return "severity-medium"
	case "low":
		return "severity-low"
	default:
		return "severity-unknown"
	}
}

// dogStateClass returns CSS class for dog state.
func dogStateClass(state string) string {
	switch state {
	case "idle":
		return "dog-idle"
	case "working":
		return "dog-working"
	default:
		return "dog-unknown"
	}
}

// queueStatusClass returns CSS class for queue status.
func queueStatusClass(status string) string {
	switch status {
	case "active":
		return "queue-active"
	case "paused":
		return "queue-paused"
	case "closed":
		return "queue-closed"
	default:
		return "queue-unknown"
	}
}

// polecatStatusClass returns CSS class for polecat work status.
func polecatStatusClass(status string) string {
	switch status {
	case "working":
		return "polecat-working"
	case "stale":
		return "polecat-stale"
	case "stuck":
		return "polecat-stuck"
	case "idle":
		return "polecat-idle"
	default:
		return "polecat-unknown"
	}
}

// activityTypeClass returns CSS class for an activity event category.
func activityTypeClass(category string) string {
	switch category {
	case "agent":
		return "tl-cat-agent"
	case "work":
		return "tl-cat-work"
	case "comms":
		return "tl-cat-comms"
	case "system":
		return "tl-cat-system"
	default:
		return "tl-cat-default"
	}
}
