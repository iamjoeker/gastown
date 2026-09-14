package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var heartbeatCmd = &cobra.Command{
	Use:     "heartbeat",
	GroupID: GroupDiag,
	Short:   "Update agent heartbeat state",
	Long: `Update the agent heartbeat with a specific state.

Used by agents to self-report their state to the witness. The witness reads
the heartbeat state instead of inferring it from timers (ZFC: gt-3vr5).

States:
  working  - Actively processing (default)
  idle     - Waiting for input
  exiting  - In gt done flow
  stuck    - Self-reporting stuck (triggers witness escalation)

Examples:
  gt heartbeat --state=stuck "blocked on auth issue"
  gt heartbeat --state=idle
  gt heartbeat --state=working`,
	RunE: runHeartbeat,
}

var heartbeatState string

func init() {
	rootCmd.AddCommand(heartbeatCmd)
	heartbeatCmd.Flags().StringVar(&heartbeatState, "state", "working", "Agent state (working, idle, exiting, stuck)")
}

func runHeartbeat(cmd *cobra.Command, args []string) error {
	sessionName := os.Getenv("GT_SESSION")
	if sessionName == "" {
		return fmt.Errorf("GT_SESSION not set (not running in a Gas Town session)")
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return fmt.Errorf("could not find town root: %v", err)
	}

	state := polecat.HeartbeatState(heartbeatState)
	switch state {
	case polecat.HeartbeatWorking, polecat.HeartbeatIdle, polecat.HeartbeatExiting, polecat.HeartbeatStuck:
		// valid
	default:
		return fmt.Errorf("invalid state %q (must be working, idle, exiting, or stuck)", heartbeatState)
	}

	context := ""
	if len(args) > 0 {
		context = strings.Join(args, " ")
	}

	polecat.TouchSessionHeartbeatWithState(townRoot, sessionName, state, context, "")

	// Deacon liveness has extra stores beyond session heartbeat. Keep the
	// generic heartbeat command and `gt deacon heartbeat` on one shared path.
	if os.Getenv("GT_ROLE") == "deacon" {
		if err := syncDeaconHeartbeatStores(townRoot, context); err != nil {
			fmt.Printf("warning: failed to touch deacon heartbeat file: %v\n", err)
		}
	} else {
		// gt-64u14: every other role (witness, refinery, polecat, crew,
		// mayor) only ever touched the session heartbeat file above. Watchers
		// that read the heartbeat:EPOCH label on the agent bead — the
		// second-order signal Witness monitoring keys on — never saw it move
		// no matter how often the agent called `gt heartbeat`, so a
		// conscientious agent was indistinguishable from one that never
		// heartbeat at all. Stamp it here too, best-effort.
		if err := agentBeadHeartbeatSync(townRoot); err != nil {
			style.PrintWarning("could not stamp agent-bead heartbeat: %v", err)
		}
	}

	fmt.Printf("Heartbeat updated: state=%s\n", state)
	return nil
}

// deaconBeadHeartbeatSyncThreshold throttles agent-bead label refreshes from
// gt heartbeat: each refresh is a Dolt commit, so only sync when the label is
// stale enough to matter to watchers.
//
// This used to be deacon.HeartbeatStaleThreshold/2. It is a Dolt write cadence,
// not a health verdict, and it has nothing to do with how long a Deacon may
// legitimately park — so tying it to that threshold meant recalibrating the
// health signal silently retuned how often the town commits to Dolt. Kept at
// the value that coupling produced, now stated outright.
const deaconBeadHeartbeatSyncThreshold = 2*time.Minute + 30*time.Second

var deaconAgentBeadHeartbeatSync = syncDeaconAgentBeadHeartbeat

func syncDeaconHeartbeatStores(townRoot, action string) error {
	var err error
	if action != "" {
		err = deacon.TouchWithAction(townRoot, action, 0, 0)
	} else {
		err = deacon.Touch(townRoot)
	}
	// The agent-bead label write is best-effort (liveness is already recorded
	// in the other two stores), but a silently discarded error here is exactly
	// what let `gt heartbeat`/`gt deacon heartbeat` print "Heartbeat updated"
	// while the label watchers actually read stayed frozen (gt-oefn, mirrors
	// hq-97l7). Surface it instead of swallowing it.
	if beadErr := deaconAgentBeadHeartbeatSync(townRoot); beadErr != nil {
		style.PrintWarning("could not stamp deacon agent-bead heartbeat: %v", beadErr)
	}
	return err
}

// syncDeaconAgentBeadHeartbeat refreshes the heartbeat:EPOCH label on the
// Deacon's agent bead — the third heartbeat store, read by Witness
// second-order monitoring. Normally await-signal maintains it, but a Deacon
// session that never reaches await-signal (handoffs, long patrols, session
// limits) leaves it stale for hours and triggers false stuck escalations
// (hq-qxl9). The write itself is best-effort — callers do not fail the
// command over it — but the error is returned rather than discarded so a
// caller can at least warn instead of reporting unconditional success.
func syncDeaconAgentBeadHeartbeat(townRoot string) error {
	return syncAgentBeadHeartbeatThrottled(beads.DeaconBeadIDTown(), beads.ResolveBeadsDir(townRoot))
}

// agentBeadHeartbeatSync is an indirection over syncAgentBeadHeartbeat so
// tests can observe it without shelling out to bd.
var agentBeadHeartbeatSync = syncAgentBeadHeartbeat

// getRoleForHeartbeat is an indirection over GetRole so tests can supply a
// fixed RoleInfo instead of relying on real cwd/env-based role detection.
var getRoleForHeartbeat = GetRole

// syncAgentBeadHeartbeat refreshes the heartbeat:EPOCH label on the current
// role's own agent bead. This is the non-deacon counterpart to
// syncDeaconAgentBeadHeartbeat: `gt heartbeat` previously stamped this label
// only when GT_ROLE was "deacon", so a witness, refinery, polecat, crew, or
// mayor session calling `gt heartbeat` touched only the session heartbeat
// file — the agent bead's heartbeat:EPOCH label, which second-order Witness
// monitoring reads, never moved no matter how often the agent heartbeat
// (gt-64u14). Best-effort: a stamping failure must not fail the command.
func syncAgentBeadHeartbeat(townRoot string) error {
	ctx, err := getRoleForHeartbeat()
	if err != nil {
		return fmt.Errorf("detecting role: %w", err)
	}
	agentBead := getAgentBeadID(ctx)
	if agentBead == "" {
		// Unknown or bead-less role (e.g. detection failed): nothing to stamp.
		return nil
	}
	return syncAgentBeadHeartbeatThrottled(agentBead, beads.ResolveBeadsDir(townRoot))
}

// syncAgentBeadHeartbeatThrottled refreshes the heartbeat:EPOCH label on
// agentBead unless it was already refreshed within deaconBeadHeartbeatSyncThreshold —
// each refresh is a permanent Dolt commit, so this throttles the write
// cadence rather than stamping on every call.
func syncAgentBeadHeartbeatThrottled(agentBead, beadsDir string) error {
	labels, err := getAllAgentLabels(agentBead, beadsDir)
	if err != nil {
		return fmt.Errorf("reading agent bead labels: %w", err)
	}
	for _, label := range labels {
		epochStr, ok := strings.CutPrefix(label, "heartbeat:")
		if !ok {
			continue
		}
		if epoch, err := strconv.ParseInt(epochStr, 10, 64); err == nil {
			if time.Since(time.Unix(epoch, 0)) < deaconBeadHeartbeatSyncThreshold {
				return nil
			}
		}
	}
	return updateAgentHeartbeat(agentBead, beadsDir)
}
