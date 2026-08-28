// Package nudge provides non-destructive nudge delivery for Gas Town agents.
//
// The nudge queue allows messages to be delivered cooperatively: instead of
// sending text directly to a tmux session (which cancels in-flight tool calls),
// nudges are written to a queue directory and picked up by the agent's
// UserPromptSubmit hook at the next natural turn boundary.
//
// Queue location: <townRoot>/.runtime/nudge_queue/<session>/
// Each nudge is a JSON file named by timestamp for FIFO ordering.
package nudge

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
)

// Priority levels for nudge delivery.
const (
	// PriorityNormal is the default — delivered at next turn boundary.
	PriorityNormal = "normal"
	// PriorityUrgent means the agent should handle this promptly.
	PriorityUrgent = "urgent"
)

// Kinds of queued nudge. A kind names what the nudge is *about*, which is what
// lets a consumer decide whether the nudge is still live: a "mail" nudge whose
// message has been read is spent, while a plain `gt nudge` carries its whole
// content inline and can never go stale. Kind is empty for those.
const (
	// KindMail announces an ordinary mail message.
	KindMail = "mail"
	// KindEscalation announces escalation mail.
	KindEscalation = "escalation"
	// KindReplyReminder reminds the recipient to reply via gt mail send.
	KindReplyReminder = "reply-reminder"
)

// Operational limits and defaults.
// These are compiled-in fallbacks. Configurable via operational.nudge
// in settings/config.json (ZFC pattern).
const (
	// DefaultNormalTTL is the time-to-live for normal-priority nudges.
	DefaultNormalTTL = 30 * time.Minute

	// DefaultUrgentTTL is the time-to-live for urgent-priority nudges.
	DefaultUrgentTTL = 2 * time.Hour

	// MaxQueueDepth is the maximum number of pending nudges per session.
	MaxQueueDepth = 50

	// staleClaimThreshold is how long a .claimed file must be untouched
	// before Drain considers it orphaned (from a crashed drainer) and removes it.
	staleClaimThreshold = 5 * time.Minute
)

// nudgeConfig loads nudge-specific thresholds from town settings.
func nudgeConfig(townRoot string) *config.NudgeThresholds {
	return config.LoadOperationalConfig(townRoot).GetNudgeConfig()
}

// QueuedNudge represents a nudge message stored in the queue.
type QueuedNudge struct {
	Sender   string `json:"sender"`
	Message  string `json:"message"`
	Priority string `json:"priority"`
	Kind     string `json:"kind,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	// MessageID is the mail bead this nudge announces, when it announces one.
	// Thread ID alone is too coarse to decide liveness: a thread outlives any
	// single message on it, so a spent notification for an old message stays
	// "live" as long as anything newer on the thread is unread — which is
	// exactly how a revoked order kept being replayed (gt-loz6).
	MessageID string `json:"message_id,omitempty"`
	// ReplyTo is the address a reply-reminder is owed to — the sender of the
	// message the reminder is about. Without it a reminder records only what it
	// is about and not who it is to, so the only event that could retire it was
	// a mail reply on the same thread. An answer sent over the channel agents
	// are told to prefer left no trace the reminder could see, and the reminder
	// outlived the conversation it was chasing (gt-w4ba).
	ReplyTo   string    `json:"reply_to,omitempty"`
	Severity  string    `json:"severity,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// DeliverAfter, if non-zero, defers delivery until this time has passed.
	// Drain skips (but does not discard) the nudge until the deadline is met.
	DeliverAfter time.Time `json:"deliver_after,omitempty"`
}

// IsMailDerived reports whether a nudge merely points at a mail message rather
// than carrying its own content. These are the nudges that can outlive what
// they announce, and the only ones a liveness filter may discard.
func (n QueuedNudge) IsMailDerived() bool {
	switch n.Kind {
	case KindMail, KindEscalation, KindReplyReminder:
		return true
	}
	return false
}

// queueDir returns the nudge queue directory for a given session.
// Path: <townRoot>/.runtime/nudge_queue/<session>/
func queueDir(townRoot, session string) string {
	// Sanitize session name for filesystem safety
	safe := strings.ReplaceAll(session, "/", "_")
	return filepath.Join(townRoot, constants.DirRuntime, "nudge_queue", safe)
}

// discardLogPath returns the path of the durable discard log for a session.
// It lives outside queueDir so it is never mistaken for a pending nudge by
// Pending/Drain/removeMatching (all of which only look at "*.json" entries
// directly inside queueDir) and never counted toward MaxQueueDepth.
func discardLogPath(townRoot, session string) string {
	safe := strings.ReplaceAll(session, "/", "_")
	return filepath.Join(townRoot, constants.DirRuntime, "nudge_discarded", safe+".jsonl")
}

// DiscardedNudge is one line of the discard log: a nudge that was removed
// without ever being delivered, and why.
type DiscardedNudge struct {
	QueuedNudge
	Reason      string    `json:"reason"`
	DiscardedAt time.Time `json:"discarded_at"`
}

// recordDiscard appends a durable record of a nudge that was destroyed
// without delivery, and prints a warning.
//
// Before this, an expired nudge was simply os.Remove'd: Drain (gt-1g2q)
// silently ate 87 of 95 queued nudges past their TTL, and nothing else ever
// looked at the queue to notice. A durable log turns "vanished, no trace" into
// something a human or a future patrol can actually find and count. The
// stderr warning is best-effort — the poller process discards its own stderr
// (buildPollerCommand), so the log file is the signal of record; stderr helps
// only in contexts (like `gt mail check`) where it isn't thrown away.
func recordDiscard(townRoot, session string, n QueuedNudge, reason string) {
	rec := DiscardedNudge{QueuedNudge: n, Reason: reason, DiscardedAt: time.Now()}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	path := discardLogPath(townRoot, session)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to record discarded nudge (mkdir): %v\n", err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to record discarded nudge: %v\n", err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(data, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write discarded nudge record: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "Warning: discarding %s nudge for %s from %s (queued %s ago): %.80s\n",
		reason, session, n.Sender, humanAge(time.Since(n.Timestamp)), n.Message)
}

// DiscardedSince returns nudges destroyed without delivery for a session
// since the given time (zero value returns the whole log). This is the
// read side of recordDiscard — the durable trace a TTL-discard or a purge
// leaves behind, for callers (patrols, `gt nudge` status output, tests) that
// need to know a queue's silence does not mean nothing was lost.
func DiscardedSince(townRoot, session string, since time.Time) ([]DiscardedNudge, error) {
	path := discardLogPath(townRoot, session)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading discard log: %w", err)
	}
	var out []DiscardedNudge
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec DiscardedNudge
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.DiscardedAt.Before(since) {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// randomSuffix returns a short random hex string to disambiguate filenames
// when multiple processes enqueue within the same nanosecond.
func randomSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Enqueue writes a nudge to the queue for the given session.
// The nudge will be picked up by the agent's hook at the next turn boundary.
// Returns an error if the queue is full (MaxQueueDepth reached).
func Enqueue(townRoot, session string, nudge QueuedNudge) error {
	// Structural backstop: a unit test must never put a message in front of a
	// live agent. Checked here, before the directory is created, because this is
	// the one function every new nudge enters the queue through. See
	// guardTestEnqueue for why the check does not live at the call sites, and
	// why guarding the tmux transport alone was not enough.
	if handled, err := guardTestEnqueue(townRoot, session, nudge.Message); handled {
		return err
	}

	dir := queueDir(townRoot, session)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating nudge queue dir: %w", err)
	}

	// Check queue depth before writing to prevent runaway senders.
	//
	// Expired entries are swept first. Without that sweep the cap is enforced by
	// age alone: a queue full of nudges that are already past their TTL — and so
	// would be discarded unread by the very next Drain — refuses the live message
	// arriving now. State has to be consulted before age, or a dead entry
	// outlives the message that supersedes it (gt-loz6).
	maxDepth := nudgeConfig(townRoot).MaxQueueDepthV()
	pending, _ := Pending(townRoot, session)
	if pending >= maxDepth {
		if swept, _ := PurgeExpired(townRoot, session); swept > 0 {
			pending, _ = Pending(townRoot, session)
		}
	}
	if pending >= maxDepth {
		return fmt.Errorf("nudge queue for %s is full (%d/%d pending)", session, pending, maxDepth)
	}

	if nudge.Timestamp.IsZero() {
		nudge.Timestamp = time.Now()
	}
	if nudge.Priority == "" {
		nudge.Priority = PriorityNormal
	}

	// Set expiry if not already specified by the caller.
	if nudge.ExpiresAt.IsZero() {
		switch nudge.Priority {
		case PriorityUrgent:
			nudge.ExpiresAt = nudge.Timestamp.Add(DefaultUrgentTTL)
		default:
			nudge.ExpiresAt = nudge.Timestamp.Add(DefaultNormalTTL)
		}
	}

	data, err := json.MarshalIndent(nudge, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling nudge: %w", err)
	}

	// Use nanosecond timestamp + random suffix for unique, ordered filenames.
	// The random suffix prevents collisions when multiple agents enqueue
	// nudges for the same session within the same nanosecond.
	filename := fmt.Sprintf("%d-%s.json", nudge.Timestamp.UnixNano(), randomSuffix())
	path := filepath.Join(dir, filename)

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing nudge to queue: %w", err)
	}

	return nil
}

// Requeue writes previously drained nudges back to the queue for later delivery.
// Existing timestamps are preserved so FIFO ordering remains stable relative to
// one another; only expired nudges are skipped.
func Requeue(townRoot, session string, nudges []QueuedNudge) error {
	for _, n := range nudges {
		if !n.ExpiresAt.IsZero() && time.Now().After(n.ExpiresAt) {
			continue
		}
		if err := Enqueue(townRoot, session, n); err != nil {
			return err
		}
	}
	return nil
}

// Drain reads and removes all queued nudges for a session, returning them
// in FIFO order. This is called by the hook to pick up pending nudges.
//
// Uses rename-then-process to prevent concurrent Drain calls from delivering
// the same nudge twice: each file is atomically renamed to a .claimed suffix
// before reading, so only one caller can claim each nudge.
//
// Expired nudges (past ExpiresAt) are silently discarded during drain.
// Orphaned .claimed files from crashed drainers are swept if older than 5 minutes.
func Drain(townRoot, session string) ([]QueuedNudge, error) {
	dir := queueDir(townRoot, session)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading nudge queue: %w", err)
	}

	// Requeue orphaned .claimed files from crashed drainers.
	// A .claimed file older than staleClaimThreshold is certainly orphaned —
	// normal processing completes in milliseconds. We rename it back to .json
	// so it gets picked up on this or a future Drain call, rather than deleting
	// it (which would permanently drop the nudge).
	staleThreshold := nudgeConfig(townRoot).StaleClaimThresholdD()
	now := time.Now()
	for _, entry := range entries {
		if !strings.Contains(entry.Name(), ".claimed") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > staleThreshold {
			orphanPath := filepath.Join(dir, entry.Name())
			// Strip everything from ".claimed" onward to restore original .json filename
			name := entry.Name()
			claimedIdx := strings.Index(name, ".claimed")
			restoredPath := filepath.Join(dir, name[:claimedIdx])
			if err := os.Rename(orphanPath, restoredPath); err != nil {
				// Rename failed — remove as last resort to prevent infinite accumulation
				fmt.Fprintf(os.Stderr, "Warning: failed to requeue orphaned claim %s: %v\n", entry.Name(), err)
				_ = os.Remove(orphanPath)
			}
		}
	}

	// Sort by name (timestamp-based) for FIFO ordering
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var nudges []QueuedNudge
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())

		// Atomically claim the file by renaming it. If another Drain call
		// is racing us, only one rename will succeed — the loser gets
		// ENOENT and moves on. This prevents double-delivery.
		//
		// Each drainer uses a unique claim suffix to avoid destination
		// collisions. On Windows, os.Rename to a shared destination is
		// not atomic — two goroutines can both "succeed" via
		// MOVEFILE_REPLACE_EXISTING, causing data loss. Unique suffixes
		// ensure each rename has a distinct target.
		claimPath := path + ".claimed." + randomSuffix()
		if err := os.Rename(path, claimPath); err != nil {
			// Another Drain got it first, or file was already removed
			continue
		}

		data, err := os.ReadFile(claimPath)
		if err != nil {
			if os.IsNotExist(err) {
				// File vanished between rename and read — treat as lost race
				continue
			}
			// Transient read error (e.g., Windows AV/indexer holding a share
			// lock) — unclaim so the nudge can be retried on a future Drain
			// call rather than permanently lost.
			_ = os.Rename(claimPath, path) // best-effort unclaim; orphan sweep catches failures
			continue
		}

		var n QueuedNudge
		if err := json.Unmarshal(data, &n); err != nil {
			// Malformed — clean up
			if rmErr := os.Remove(claimPath); rmErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to remove malformed claim %s: %v\n", entry.Name(), rmErr)
			}
			continue
		}

		// Skip expired nudges — stale messages create noise, not value.
		// The nudge itself is still recorded to the discard log before removal
		// so this destruction leaves a trace (gt-1g2q): silently deleting it
		// outright is what let 87 of 95 queued nudges vanish unnoticed.
		if !n.ExpiresAt.IsZero() && now.After(n.ExpiresAt) {
			recordDiscard(townRoot, session, n, "ttl-expired-drain")
			if rmErr := os.Remove(claimPath); rmErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to remove expired nudge %s: %v\n", entry.Name(), rmErr)
			}
			continue
		}

		// Deferred nudge: not ready yet — unclaim and leave in queue.
		if !n.DeliverAfter.IsZero() && now.Before(n.DeliverAfter) {
			if renameErr := os.Rename(claimPath, path); renameErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to unclaim deferred nudge %s: %v\n", entry.Name(), renameErr)
			}
			continue
		}

		nudges = append(nudges, n)

		// Remove the claimed file after successful processing
		if rmErr := os.Remove(claimPath); rmErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove processed claim %s: %v\n", entry.Name(), rmErr)
		}
	}

	return nudges, nil
}

// Pending returns the count of queued nudges for a session without draining.
// This is an approximate count — it does not check expiry or read file contents.
func Pending(townRoot, session string) (int, error) {
	dir := queueDir(townRoot, session)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading nudge queue: %w", err)
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}

	return count, nil
}

// QueueLen returns the number of pending nudges for a session without draining.
// Returns 0 on error — callers use this for quick checks. Missing queue
// directories are expected (no nudges yet) and silenced; other filesystem
// errors are logged to stderr so they don't go unnoticed.
func QueueLen(townRoot, session string) int {
	n, err := Pending(townRoot, session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: nudge queue check failed for %s: %v\n", session, err)
	}
	return n
}

// RemoveKindByThread deletes queued nudges for a session that match both the
// provided kind and thread ID. It only removes queued .json files, leaving any
// in-flight claimed files alone so concurrent drainers can finish safely.
func RemoveKindByThread(townRoot, session, kind, threadID string) (int, error) {
	if kind == "" || threadID == "" {
		return 0, nil
	}
	return removeMatching(townRoot, session, func(n QueuedNudge) bool {
		return n.Kind == kind && n.ThreadID == threadID
	}, nil)
}

// RemoveReplyReminders deletes queued reply-reminder nudges for a session whose
// ReplyTo address satisfies owedTo, returning the number removed.
//
// The address comparison is the caller's: agent addresses have several live
// spellings and reconciling them is the mail package's boundary, not this one.
//
// Reminders written before ReplyTo existed carry an empty address and are never
// matched — owedTo is not consulted for them. They still retire on their own
// TTL, and on a mail reply through RemoveKindByThread.
func RemoveReplyReminders(townRoot, session string, owedTo func(replyTo string) bool) (int, error) {
	if owedTo == nil {
		return 0, nil
	}
	return removeMatching(townRoot, session, func(n QueuedNudge) bool {
		return n.Kind == KindReplyReminder && n.ReplyTo != "" && owedTo(n.ReplyTo)
	}, nil)
}

// PurgeExpired removes queued nudges whose TTL has already elapsed, returning
// the number removed. In-flight .claimed files are left alone so a concurrent
// drainer can finish.
//
// Drain already discards expired entries as it reads them, but Drain only runs
// when someone is there to receive; a queue nobody is draining keeps its dead
// entries indefinitely and they count against the depth cap. Each removal here
// is recorded to the discard log (see recordDiscard) — this is the path that
// silently ate most of a real backlog (gt-1g2q) precisely because no one was
// draining to see it happen.
func PurgeExpired(townRoot, session string) (int, error) {
	return removeMatching(townRoot, session, func(n QueuedNudge) bool {
		return !n.ExpiresAt.IsZero() && time.Now().After(n.ExpiresAt)
	}, func(n QueuedNudge) {
		recordDiscard(townRoot, session, n, "ttl-expired-purge")
	})
}

// RemoveByMessage deletes queued nudges announcing the given mail message.
// Called when the message is read or archived: the notification is spent, and
// leaving it queued is what replays a dead order at an agent (gt-loz6).
//
// Nudges predating the MessageID field carry only a thread ID; those are
// matched on threadID when one is supplied, which is as precise as the record
// they were written with allows.
func RemoveByMessage(townRoot, session, messageID, threadID string) (int, error) {
	if messageID == "" && threadID == "" {
		return 0, nil
	}
	return removeMatching(townRoot, session, func(n QueuedNudge) bool {
		if !n.IsMailDerived() {
			return false
		}
		if messageID != "" && n.MessageID == messageID {
			return true
		}
		return n.MessageID == "" && threadID != "" && n.ThreadID == threadID
	}, nil)
}

// removeMatching deletes queued .json entries for which match returns true.
// Claimed files are skipped: a concurrent drainer owns those.
//
// onRemove, if non-nil, is called with each nudge just before it is deleted.
// Callers that remove a nudge because it is spent (delivered, superseded,
// answered) pass nil — that removal is the intended lifecycle. PurgeExpired
// passes recordDiscard, because there the removal IS the failure the nudge
// was supposed to prevent: a message nobody drained in time.
func removeMatching(townRoot, session string, match func(QueuedNudge) bool, onRemove func(QueuedNudge)) (int, error) {
	dir := queueDir(townRoot, session)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading nudge queue: %w", err)
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("reading queued nudge %s: %w", entry.Name(), err)
		}

		var n QueuedNudge
		if err := json.Unmarshal(data, &n); err != nil {
			continue
		}
		if !match(n) {
			continue
		}

		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("removing queued nudge %s: %w", entry.Name(), err)
		}
		if onRemove != nil {
			onRemove(n)
		}
		removed++
	}

	return removed, nil
}

// FormatForInjection formats queued nudges as a system-reminder block
// suitable for Claude Code hook output.
func FormatForInjection(nudges []QueuedNudge) string {
	if len(nudges) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<system-reminder>\n")

	// Separate urgent from normal
	var urgent, normal []QueuedNudge
	for _, n := range nudges {
		if n.Priority == PriorityUrgent {
			urgent = append(urgent, n)
		} else {
			normal = append(normal, n)
		}
	}

	now := time.Now()
	if len(urgent) > 0 {
		b.WriteString(fmt.Sprintf("QUEUED NUDGE (%d urgent):\n\n", len(urgent)))
		for _, n := range urgent {
			b.WriteString(fmt.Sprintf("  [URGENT from %s%s] %s\n", n.Sender, stamp(n, now), n.Message))
		}
		if len(normal) > 0 {
			b.WriteString(fmt.Sprintf("\nPlus %d non-urgent nudge(s):\n", len(normal)))
			for _, n := range normal {
				b.WriteString(fmt.Sprintf("  [from %s%s] %s\n", n.Sender, stamp(n, now), n.Message))
			}
		}
		b.WriteString("\nHandle urgent nudges before continuing current work.\n")
	} else {
		b.WriteString(fmt.Sprintf("QUEUED NUDGE (%d message(s)):\n\n", len(normal)))
		for _, n := range normal {
			b.WriteString(fmt.Sprintf("  [from %s%s] %s\n", n.Sender, stamp(n, now), n.Message))
		}
		b.WriteString("\nThis is a background notification. Continue current work unless the nudge is higher priority.\n")
	}

	b.WriteString("</system-reminder>\n")
	return b.String()
}

// stamp renders when a nudge was sent, as " · 18:43 CDT (2h14m ago)".
//
// The banner used to carry sender and subject only. A queue can hold hours of
// messages at once, and several of them can be revisions of each other, so
// without a time on the line there is no way to tell which instruction is the
// current one — a witness reading ten Mayor orders said exactly that (gt-loz6).
// Ordering alone does not answer it either: entries are grouped by priority
// before they are printed.
func stamp(n QueuedNudge, now time.Time) string {
	if n.Timestamp.IsZero() {
		return ""
	}
	ts := n.Timestamp.Local()
	age := now.Sub(ts)
	layout := "15:04 MST"
	if age >= 24*time.Hour || age < 0 {
		layout = "Jan 2 15:04 MST"
	}
	switch {
	case age < 0:
		return fmt.Sprintf(" · %s", ts.Format(layout))
	case age < time.Minute:
		return fmt.Sprintf(" · %s (just now)", ts.Format(layout))
	default:
		return fmt.Sprintf(" · %s (%s ago)", ts.Format(layout), humanAge(age))
	}
}

// humanAge renders a duration as "3m", "2h14m" or "2d3h" — the resolution a
// reader needs to rank two instructions, without the seconds Duration.String
// would print.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
