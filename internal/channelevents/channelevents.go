// Package channelevents provides file-based event emission for named channels.
//
// Channel events are JSON files written to ~/gt/events/... and consumed by
// await-event subscribers (e.g., the refinery watching for MERGE_READY events).
// This is distinct from the activity feed events in the events package
// (~/gt/.events.jsonl).
//
// # Channel scoping
//
// Channels are single-consumer: only one process may watch a channel, because
// consumers delete events after reading them (await-event --cleanup). Most
// channels name a per-rig agent — every rig runs its own refinery and witness —
// so a flat, town-global namespace would put every rig's agent on the same
// channel. One rig's refinery would then consume and delete another rig's
// MQ_SUBMIT, find an empty queue, and go back to sleep, while the originating
// polecat blocked forever on a merge no refinery would process.
//
// Rig-scoped channels therefore live at <town>/events/<rig>/<channel>, and only
// channels consumed by a town-level singleton (the mayor, the deacon) stay flat
// at <town>/events/<channel>. ChannelDir is the single resolver for both ends:
// emitters, watchers, and the daemon's spawn gate all call it, so the emit and
// watch sites cannot disagree about where a channel lives.
package channelevents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/workspace"
)

// ValidChannelName restricts channel names to safe characters (no path traversal).
var ValidChannelName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ValidRigName restricts rig names to safe characters. Rig names become a path
// segment in the channel directory, so they get the same treatment as channels.
var ValidRigName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// townScopedChannels lists channels whose consumer is a town-level singleton.
// Exactly one agent in the town watches each of these, so they need no rig
// segment. Every other channel is rig-scoped.
//
// Add to this map only when the consuming agent is genuinely a town-wide
// singleton. Defaulting to rig-scoped is the safe direction: an over-scoped
// channel loses a wake-up (the tmux nudge still fires), while an under-scoped
// one lets a different rig destroy events it cannot even identify.
var townScopedChannels = map[string]struct{}{
	"mayor":  {},
	"deacon": {},
}

// emitSeq is an atomic counter to ensure unique event filenames even when
// time.Now().UnixNano() has low resolution.
var emitSeq atomic.Uint64

// IsTownScoped reports whether the channel is consumed by a town-level
// singleton and therefore is not scoped by rig.
func IsTownScoped(channel string) bool {
	_, ok := townScopedChannels[channel]
	return ok
}

// ChannelDir returns the on-disk directory for a channel without creating it.
//
// Rig-scoped channels resolve to <townRoot>/events/<rigName>/<channel>;
// town-scoped channels resolve to <townRoot>/events/<channel> and ignore
// rigName. Callers may pass their own rig unconditionally.
//
// A rig-scoped channel with an empty rigName is an error rather than a silent
// fallback to the flat path: falling back is exactly the cross-rig collision
// this layout exists to prevent.
func ChannelDir(townRoot, rigName, channel string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-]", channel)
	}
	if IsTownScoped(channel) {
		return filepath.Join(townRoot, "events", channel), nil
	}
	if rigName == "" {
		return "", fmt.Errorf("channel %q is rig-scoped: a rig name is required (pass --rig, set GT_RIG, or run from inside a rig)", channel)
	}
	if !ValidRigName.MatchString(rigName) {
		return "", fmt.Errorf("invalid rig name %q: must match [a-zA-Z0-9_-]", rigName)
	}
	return filepath.Join(townRoot, "events", rigName, channel), nil
}

// EnsureChannelDir resolves the channel directory and creates it if needed.
func EnsureChannelDir(townRoot, rigName, channel string) (string, error) {
	dir, err := ChannelDir(townRoot, rigName, channel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating event directory: %w", err)
	}
	return dir, nil
}

// EmitToRig creates an event file on a rig-scoped channel using an explicit
// town root. Used by internal callers that already know both the town root and
// the rig whose agent should receive the event.
func EmitToRig(townRoot, rigName, channel, eventType string, payloadPairs []string) (string, error) {
	if IsTownScoped(channel) {
		return "", fmt.Errorf("channel %q is town-scoped: use EmitToTown", channel)
	}
	return emit(townRoot, rigName, channel, eventType, payloadPairs)
}

// EmitToTown creates an event file on a town-scoped channel (one consumed by a
// town-level singleton such as the mayor) using an explicit town root.
func EmitToTown(townRoot, channel, eventType string, payloadPairs []string) (string, error) {
	if !IsTownScoped(channel) {
		return "", fmt.Errorf("channel %q is rig-scoped: use EmitToRig", channel)
	}
	return emit(townRoot, "", channel, eventType, payloadPairs)
}

// EmitFromCwd creates an event file, resolving the town root from the current
// working directory. rigName is ignored for town-scoped channels.
func EmitFromCwd(rigName, channel, eventType string, payloadPairs []string) (string, error) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		// Prefer the explicit town-root env vars before guessing ~/gt. The town is
		// frequently NOT at ~/gt, and guessing is what silently wrote 80MB of archive
		// into a directory no reader ever looked at (hq-uwxo / gt-3sz).
		townRoot = os.Getenv("GT_TOWN_ROOT")
		if townRoot == "" {
			townRoot = os.Getenv("GT_ROOT")
		}
		if townRoot == "" {
			home, _ := os.UserHomeDir()
			townRoot = filepath.Join(home, "gt")
		}
	}
	return emit(townRoot, rigName, channel, eventType, payloadPairs)
}

// AllowTestEmitEnv opts a test in to real event emission. Tests that exercise
// this package against a temp town root must set it; every other test is
// refused, so no test can wake a live agent. See emit.
const AllowTestEmitEnv = "GT_ALLOW_TEST_CHANNEL_EVENTS"

// emit writes an event file to the resolved channel directory.
func emit(townRoot, rigName, channel, eventType string, payloadPairs []string) (string, error) {
	// Resolve before the test backstop so that a malformed channel or a missing
	// rig is still reported under `go test`. Only the write is suppressed there,
	// not the argument checking.
	eventDir, err := ChannelDir(townRoot, rigName, channel)
	if err != nil {
		return "", err
	}

	// Backstop: a unit test must never emit a real town event. Test binaries
	// commonly run inside the town workspace, where the town root resolves to
	// the live one, so an emit reached by accident wakes real agents into full
	// patrols and burns tokens on every rig. That is not hypothetical — a guard
	// that treated a blanked GT_TEST_NUDGE_LOG as "not a test" let
	// `go test ./internal/cmd/...` emit a real MQ_SUBMIT on every run, and the
	// resulting refinery wakeups were misdiagnosed as cross-rig event theft.
	//
	// Callers get a nil error and an empty path: emission is best-effort at
	// every call site (all of them discard the error), and failing the test
	// outright would punish tests that merely pass through this code.
	if testing.Testing() && os.Getenv(AllowTestEmitEnv) == "" {
		return "", nil
	}

	if err := os.MkdirAll(eventDir, 0755); err != nil {
		return "", fmt.Errorf("creating event directory: %w", err)
	}

	payload := make(map[string]string)
	for _, pair := range payloadPairs {
		key, val, found := strings.Cut(pair, "=")
		if found {
			payload[key] = val
		}
	}

	now := time.Now()
	event := map[string]interface{}{
		"type":      eventType,
		"channel":   channel,
		"timestamp": now.Format(time.RFC3339),
		"payload":   payload,
	}
	// Record the owning rig on the event itself. The directory already scopes
	// the event, but stamping it makes a stray event attributable after the
	// fact — the original defect was undiagnosable precisely because a
	// consumed event carried nothing identifying whose it was.
	if rigName != "" {
		event["rig"] = rigName
	}

	data, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling event: %w", err)
	}

	seq := emitSeq.Add(1)
	eventFile := filepath.Join(eventDir, fmt.Sprintf("%d-%d-%d.event", now.UnixNano(), seq, os.Getpid()))
	if err := os.WriteFile(eventFile, data, 0644); err != nil {
		return "", fmt.Errorf("writing event file: %w", err)
	}

	return eventFile, nil
}
