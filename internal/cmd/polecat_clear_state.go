package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/townlog"
	"github.com/steveyegge/gastown/internal/workspace"
)

// gt polecat clear-state exists because the witness had a prescribed remedy and
// no way to carry it out.
//
// agent_state=stuck is written by `gt done` for escalated and deferred exits and
// means "paused on purpose" (internal/beads/status.go). It is durable by design.
// But nothing on any witness-runnable path clears it: `gt session restart` kills
// and recreates the tmux session and writes no agent_state at all, and nuking is
// forbidden to witnesses by the restart-first policy (gt-dsgp). So the prescribed
// remedy and the permitted actions did not intersect, and a paused polecat's
// disposition never moved (gt-fbgq).
//
// This is the missing verb, and it is deliberately the narrowest thing that
// closes the loop: it writes one field on one agent bead. It does not touch the
// worktree, the branch, the session, or the hook. That is what makes it safe to
// hand to a witness, and it is why the fix is a new verb rather than teaching
// restart to clear the state — clearing agent_state as a side effect of a
// restart would discard a deliberate pause on every ordinary recycle.
var (
	polecatClearStateJSON  bool
	polecatClearStateForce bool
)

var polecatClearStateCmd = &cobra.Command{
	Use:   "clear-state <rig>/<polecat>",
	Short: "Lift a deliberate agent_state pause so the slot can be reused",
	Long: `Return a paused polecat's agent_state to idle.

Paused states (stuck, awaiting-gate, paused, escalated) survive a session
restart: no restart path writes agent_state, so restarting a paused polecat
leaves it paused. This command is the way back, and a witness may run it —
it modifies one field on the agent bead and touches nothing else. The
worktree, branch, session, and hook are left exactly as they are.

SAFETY:
It refuses unless 'gt polecat check-recovery' would answer NEEDS_STATE_CLEAR —
that is, unless the pause is the ONLY thing standing between this polecat and
reuse. A paused polecat that also has uncommitted work, unpushed commits, work
still on its hook, or work never submitted to the merge queue needs recovery
first; clearing the field there would hide work at risk behind a green slot.

If it refuses, the blockers it prints are the work to deal with. Escalate:
  gt escalate -s HIGH "polecat needs recovery before its state can be cleared"

--force bypasses the safety check. It is for a human or the Mayor with a
reason, not a way for a witness to route around the refusal.

Examples:
  gt polecat clear-state greenplace/Toast
  gt polecat clear-state greenplace/Toast --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatClearState,
}

func init() {
	polecatClearStateCmd.Flags().BoolVar(&polecatClearStateJSON, "json", false, "Output as JSON")
	polecatClearStateCmd.Flags().BoolVar(&polecatClearStateForce, "force", false,
		"Clear the state even when other blockers remain (human/Mayor escape hatch — can hide work at risk)")
	polecatCmd.AddCommand(polecatClearStateCmd)
}

// clearStateResult is the machine-readable outcome. PriorState and CurrentState
// are both reported, and CurrentState is READ BACK from the store after the
// write rather than assumed from it: a command whose entire job is one field
// update has no business reporting success it did not confirm.
type clearStateResult struct {
	Rig          string   `json:"rig"`
	Polecat      string   `json:"polecat"`
	AgentBead    string   `json:"agent_bead"`
	PriorState   string   `json:"prior_state"`
	CurrentState string   `json:"current_state"`
	Cleared      bool     `json:"cleared"`
	Reason       string   `json:"reason"`
	Verdict      string   `json:"verdict,omitempty"`
	Blockers     []string `json:"blockers,omitempty"`
	Forced       bool     `json:"forced,omitempty"`
}

func runPolecatClearState(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	p, err := mgr.Get(polecatName)
	if err != nil {
		return fmt.Errorf("polecat '%s' not found in rig '%s'", polecatName, rigName)
	}

	agentBeadID := polecatBeadIDForRig(r, rigName, polecatName)
	agentBd := beads.New(r.Path).ForAgentBead()
	_, fields, err := agentBd.GetAgentBead(agentBeadID)
	if err != nil {
		return fmt.Errorf("reading agent bead %s: %w", agentBeadID, err)
	}
	if fields == nil {
		return fmt.Errorf("agent bead %s has no parsable agent fields", agentBeadID)
	}

	result := clearStateResult{
		Rig:          rigName,
		Polecat:      polecatName,
		AgentBead:    agentBeadID,
		PriorState:   fields.AgentState,
		CurrentState: fields.AgentState,
	}

	prior := pausedAgentState(fields)
	if prior == "" {
		// Idempotent on purpose. A witness will run this from a patrol checklist
		// against whatever check-recovery named, and a second run — or a run
		// against a polecat that was never paused — is a no-op, not an error.
		result.Reason = "not-paused"
		return reportClearState(result, fmt.Sprintf(
			"agent_state=%s is not a paused state — nothing to clear.",
			displayAgentState(fields.AgentState)))
	}

	// The same measured classifier the reuse gate and check-recovery use, so this
	// command cannot develop its own opinion about what is safe.
	disposition := mgr.WorkstateDispositionForPolecat(polecatName, p.State, p.Issue)
	result.Verdict = disposition.Verdict
	result.Blockers = disposition.Blockers

	if disposition.Verdict != polecat.WorkstateVerdictNeedsStateClear {
		if !polecatClearStateForce {
			result.Reason = "blocked"
			if polecatClearStateJSON {
				return encodeClearStateJSON(result)
			}
			return fmt.Errorf(`refused: %s/%s is not clear of everything but its pause

  Verdict: %s
%s
The pause is not the only thing standing here, and clearing agent_state would
report the slot as available while that remains true. Deal with the blockers
first, or escalate:

  gt polecat check-recovery %s/%s
  gt escalate -s HIGH "polecat needs recovery before its state can be cleared"

A human or the Mayor can pass --force to clear it anyway`,
				rigName, polecatName, disposition.Verdict,
				formatClearStateBlockers(disposition.Blockers), rigName, polecatName)
		}
		result.Forced = true
		fmt.Fprintf(os.Stderr, "%s clearing agent_state=%s despite verdict %s (--force)\n",
			style.Warning.Render("⚠"), prior, disposition.Verdict)
	}

	idle := string(beads.AgentStateIdle)
	if err := mgr.SetAgentStateWithRetry(polecatName, idle); err != nil {
		return fmt.Errorf("clearing agent_state on %s: %w", agentBeadID, err)
	}

	// Read back. The write path is a bd subprocess against Dolt, and a returned
	// nil is not evidence the row changed.
	_, after, err := agentBd.GetAgentBead(agentBeadID)
	if err != nil {
		return fmt.Errorf("clearing agent_state on %s: write reported success but the bead could not be re-read: %w", agentBeadID, err)
	}
	if after == nil || after.AgentState != idle {
		observed := "<unparsable>"
		if after != nil {
			observed = displayAgentState(after.AgentState)
		}
		return fmt.Errorf("clearing agent_state on %s: write reported success but the bead still reads agent_state=%s", agentBeadID, observed)
	}
	result.CurrentState = after.AgentState
	result.Cleared = true
	result.Reason = "cleared"

	// Durable record of what the state was. The bead now says "idle" and carries
	// no memory of the pause, so without this the fact that somebody was parked
	// at stuck — and when, and by whose hand it was lifted — is gone.
	recordAgentStateCleared(rigName, polecatName, prior, result.Forced)

	return reportClearState(result, fmt.Sprintf(
		"Cleared agent_state on %s/%s: %s → %s. Worktree, branch, and session untouched.",
		rigName, polecatName, prior, idle))
}

func reportClearState(result clearStateResult, message string) error {
	if polecatClearStateJSON {
		return encodeClearStateJSON(result)
	}
	marker := style.Bold.Render("✓")
	if !result.Cleared {
		marker = style.Dim.Render("·")
	}
	fmt.Printf("%s %s\n", marker, message)
	return nil
}

func encodeClearStateJSON(result clearStateResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func formatClearStateBlockers(blockers []string) string {
	if len(blockers) == 0 {
		return "  (no blockers reported — the verdict itself is the refusal)\n"
	}
	out := ""
	for _, blocker := range blockers {
		out += "    - " + blocker + "\n"
	}
	return out
}

// displayAgentState renders an empty agent_state as something a reader can tell
// apart from a state that is genuinely named "".
func displayAgentState(state string) string {
	if state == "" {
		return "<empty>"
	}
	return state
}

// recordAgentStateCleared writes the audit line. Best-effort: a missing town log
// must never turn a completed state change into a failure, and the caller has
// already confirmed the write landed.
func recordAgentStateCleared(rigName, polecatName, prior string, forced bool) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return
	}
	context := fmt.Sprintf("cleared agent_state=%s to idle", prior)
	if forced {
		context += " (--force: other blockers were present)"
	}
	agent := fmt.Sprintf("%s/polecats/%s", rigName, polecatName)
	_ = townlog.NewLogger(townRoot).Log(townlog.EventAgentStateCleared, agent, context)
}
