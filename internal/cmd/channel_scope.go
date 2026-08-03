package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// resolveChannelRig determines which rig's channel namespace this process
// belongs to, in order of decreasing explicitness:
//
//  1. an explicit --rig flag
//  2. the GT_RIG environment variable, which the session harness sets
//  3. inference from the current working directory
//
// An empty return is not an error here: town-scoped channels ignore the rig
// entirely, and channelevents.ChannelDir raises the actionable error when a
// rig-scoped channel actually needs one.
func resolveChannelRig(explicitRig, townRoot string) string {
	if explicitRig != "" {
		return explicitRig
	}
	if envRig := os.Getenv("GT_RIG"); envRig != "" {
		return envRig
	}
	if townRoot != "" {
		if inferred, err := inferRigFromCwd(townRoot); err == nil {
			return inferred
		}
	}
	return ""
}

// consumerTTL is how long a consumer registration stays live without a
// refresh. await-event refreshes on every invocation, and patrol waits are
// far shorter than this, so a registration older than the TTL means the
// consumer is gone rather than merely idle.
const consumerTTL = 15 * time.Minute

// consumersDirName holds consumer registrations inside a channel directory.
// Event readers skip subdirectories, so this is invisible to them.
const consumersDirName = ".consumers"

// unsafeConsumerChars matches everything not allowed in a registration filename.
var unsafeConsumerChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// consumerRegistration records one agent watching a channel.
type consumerRegistration struct {
	ID      string `json:"id"`
	PID     int    `json:"pid"`
	Updated int64  `json:"updated"`
}

// registerChannelConsumer records this agent as a live consumer of the channel
// and returns the ID of a different, still-live consumer if one exists.
//
// Channels are single-consumer because --cleanup deletes events after reading
// them; a second consumer on the same channel silently destroys events the
// first one never saw. Rig-scoping the channel directory is what prevents that
// in practice, and this registry is the backstop that turns the precondition
// from documentation into enforcement if a shared channel is ever reintroduced.
//
// Registration failures are reported as "no conflict": bookkeeping trouble must
// never stop an agent from doing its job.
func registerChannelConsumer(channelDir, consumerID string) (conflict string) {
	if consumerID == "" {
		return ""
	}
	safeID := unsafeConsumerChars.ReplaceAllString(consumerID, "_")
	if safeID == "" {
		return ""
	}

	dir := filepath.Join(channelDir, consumersDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ""
	}

	now := time.Now()
	data, err := json.Marshal(consumerRegistration{
		ID:      consumerID,
		PID:     os.Getpid(),
		Updated: now.Unix(),
	})
	if err != nil {
		return ""
	}
	self := filepath.Join(dir, safeID+".consumer")
	if err := os.WriteFile(self, data, 0644); err != nil {
		return ""
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	// Sort for a deterministic conflict report when more than one other
	// consumer is live.
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".consumer") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(dir, name)
		if path == self {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > consumerTTL {
			// Departed consumer. Prune so the registry does not accumulate
			// entries for agents that were recycled long ago.
			_ = os.Remove(path)
			continue
		}
		other := strings.TrimSuffix(name, ".consumer")
		if raw, err := os.ReadFile(path); err == nil {
			var reg consumerRegistration
			if json.Unmarshal(raw, &reg) == nil && reg.ID != "" {
				other = reg.ID
			}
		}
		return other
	}
	return ""
}

// channelConsumerConflictError explains a rejected --cleanup and how to resolve it.
func channelConsumerConflictError(channel, channelDir, self, other string) error {
	return fmt.Errorf(
		"refusing --cleanup on channel %q: consumer %q is already live on %s\n"+
			"Channels are single-consumer — with --cleanup, whichever consumer reads first\n"+
			"deletes the event, so the other one never sees it. Give each consumer its own\n"+
			"channel (rig-scoped channels do this automatically via --rig/GT_RIG), or drop\n"+
			"--cleanup so both can read.\n"+
			"This consumer: %s",
		channel, other, channelDir, self)
}
