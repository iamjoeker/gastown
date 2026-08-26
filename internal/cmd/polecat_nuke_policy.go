package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/style"
)

// Restart-first policy enforcement (gt-dsgp policy, gt-y20 enforcement).
//
// The policy itself is old: "The witness NEVER nukes polecats automatically.
// Nuking only happens via explicit `gt polecat nuke` command from a human or
// Mayor." Until gt-y20 that sentence lived only in prose, several screens above
// the step that does the checking, in the mol-witness-patrol preamble — while
// every machine-readable surface pointed the other way at the moment of
// decision: check-recovery rendered "✓ Safe to nuke - no work at risk." in
// success green, git-state rendered "CLEAN (safe to kill)", and the command
// help was addressed to the Witness. Proximity won, and duly_noted/obsidian was
// destroyed out of policy (dn-v29).
//
// The fix is to put the policy in the binary instead of the preamble: the nuke
// path refuses a witness identity outright, and no polecat command offers
// nuking to a witness as an available action. Wording here is kept in sync with
// internal/formula/formulas/mol-witness-patrol.formula.toml and
// templates/witness-CLAUDE.md so the two cannot drift again.

const (
	// restartFirstPolicyRule is the policy sentence, quoted from
	// mol-witness-patrol's "Restart-First Policy (gt-dsgp)" preamble. Any
	// rewording must be applied to both places.
	restartFirstPolicyRule = "the witness NEVER nukes polecats — nuking only happens via explicit `gt polecat nuke` from a human or Mayor (restart-first policy, gt-dsgp)"

	// restartFirstOverrideFlag is the deliberate escape hatch. It exists so a
	// human driving a witness shell is not locked out, not so the witness can
	// route around the policy on its own initiative.
	restartFirstOverrideFlag = "override-restart-first"

	// witnessActionRestart is the only slot-reclaiming action the restart-first
	// policy permits a witness to take. Surfaced in check-recovery JSON as
	// witness_action so the policy is machine-readable, not just prose.
	witnessActionRestart = "restart"

	// witnessActionClearState is the second permitted action, and it exists
	// because restart could not do this job: no restart path writes agent_state,
	// so a polecat parked at a paused state (stuck, awaiting-gate, paused,
	// escalated) read paused again the moment its new session came up. Every
	// action a witness is permitted to take left the state untouched and nuking
	// is forbidden, so the prescribed remedy and the permitted actions did not
	// intersect and the slot's disposition never moved (gt-fbgq).
	//
	// Like restart, it is non-destructive: it writes one field on the agent bead
	// and touches neither the worktree nor the session.
	witnessActionClearState = "clear-state"
)

// nukeCallerIdentity resolves the identity of whoever is invoking a destructive
// polecat command. GT_ROLE is authoritative — it is injected into every agent
// session (e.g. "gastown/witness") and is absent from a human's shell. BD_ACTOR
// is the beads-attribution fallback for agents that set it without GT_ROLE.
//
// Returns the parsed role and the raw identity string for the error message.
// An empty role means "no agent identity" — a human, which the policy permits.
func nukeCallerIdentity() (Role, string) {
	for _, envVar := range []string{EnvGTRole, "BD_ACTOR"} {
		raw := strings.TrimSpace(os.Getenv(envVar))
		if raw == "" {
			continue
		}
		role, _, _ := parseRoleString(raw)
		return role, raw
	}
	return "", ""
}

// checkRestartFirstNukePolicy refuses a nuke when the caller is a witness
// identity. The Mayor and human paths are untouched: they return nil here and
// fall through to the existing safety checks.
//
// command names the invoking command in the refusal so the message points at
// the right override (`gt polecat nuke` vs `gt polecat stale --cleanup`).
func checkRestartFirstNukePolicy(command string, override bool) error {
	role, actor := nukeCallerIdentity()
	if role != RoleWitness {
		return nil
	}

	if override {
		// Loud on purpose: an override is a deliberate act by a human at a
		// witness shell, and it should read that way in the transcript.
		fmt.Fprintf(os.Stderr, "%s %s is overriding the restart-first policy via --%s. %s\n",
			style.Warning.Render("⚠"), actor, restartFirstOverrideFlag, restartFirstPolicyRule)
		return nil
	}

	return fmt.Errorf(`refused: %s is a witness identity and may not nuke polecats

%s

Reclaim the slot without destroying the sandbox:
  gt session restart <rig>/<polecat>

If work is at risk instead, escalate rather than nuking:
  gt escalate -s HIGH "polecat needs recovery before cleanup"

A human or the Mayor can run %s directly. A human driving this witness shell
can pass --%s to proceed anyway`,
		actor, restartFirstPolicyRule, command, restartFirstOverrideFlag)
}

// witnessActionFor names the action the restart-first policy permits a witness
// to take for a given check-recovery verdict. Every verdict resolves to
// something other than nuking — that is the point.
//
// state is the polecat's lifecycle state, and it matters only on the
// SAFE_TO_NUKE road: see the default arm. The zero value ("state was not
// measured") leaves that arm's answer at restart, unchanged.
func witnessActionFor(verdict string, state polecat.State) string {
	switch verdict {
	case "NEEDS_MQ_SUBMIT", "NEEDS_RECOVERY":
		return "escalate"
	case polecat.WorkstateVerdictNeedsStateClear:
		// Not "escalate": nothing is at risk and there is nothing for the Mayor
		// to recover — only a field the witness is permitted to clear itself.
		return witnessActionClearState
	case polecat.WorkstateVerdictNeedsLogin:
		// Escalate, not restart — and this is the one verdict where the default
		// arm below would actively make things worse. No restart path can supply
		// credentials, so restarting a logged-out agent produces another
		// logged-out agent; only a human at a browser clears it. Restart-first is
		// correct for ordinary stalls and wrong for exactly this one (gt-acb1).
		return "escalate"
	case polecat.WorkstateVerdictSuspectStall:
		// Escalate, not restart, and not leave-alone.
		//
		// Leave-alone is what this case USED to get — a wedged agent renders the
		// busy marker, so it read as WORKING — and that is the whole defect
		// (gt-y39t). Restart is wrong in the other direction: the evidence is
		// two pane samples a minute apart, which is enough to say nothing is
		// moving and not enough to say the agent is dead, and restarting throws
		// away its context to find out. A person can settle it by looking at
		// the pane.
		return "escalate"
	case "PENDING_MR":
		return "leave-alone"
	case "UNVERIFIED":
		// UNVERIFIED means the caller gathered no git or merge-queue facts, so
		// it has ruled nothing out — including work at risk. Restart is the
		// default arm below and is not destructive to the worktree, but it is
		// still an action taken on no evidence; the honest answer is to go
		// measure with `gt polecat check-recovery` first (gt-49dp).
		return "leave-alone"
	case "WORKING":
		// Restart is non-destructive to the worktree but destroys the agent's
		// context, and WORKING means the agent is generating right now — often
		// while running `gt done` (gt-5tg). Leave it alone; it is not a slot to
		// reclaim yet.
		return "leave-alone"
	default:
		// SAFE_TO_NUKE, and the question restart has to answer here is "what
		// does this change that the reuse gate does not already do?"
		//
		// For an idle or done polecat the answer is nothing. Pool eligibility is
		// not a latch a restart flips — polecat.StateEligibleForPoolReuse is
		// evaluated afresh on every sling, and it already accepts both states —
		// so a polecat in one of them is a candidate right now and there is no
		// slot to reclaim. `gt session restart` writes no lifecycle state at
		// all; it stops a tmux session and starts another.
		//
		// The restart is not merely inert, it has a live downside. The new
		// session primes, finds nothing on its hook, and runs `gt done`, and
		// that is the exit path gt-j9uv and gt-gubw are about: a fork-mode
		// polecat whose source bead is already closed can park at
		// agent_state=stuck instead of exiting. Measured 2026-08-23 — the
		// witness followed this prescription on ghoul and synth, both parked,
		// and both had to be cleared by hand with `gt polecat clear-state`. Two
		// healthy done polecats spent as the price of reclaiming nothing.
		//
		// So the honest prescription is none: leave-alone, the same answer the
		// mol-witness-patrol preamble has always given in prose ("Done polecat
		// (bead closed) → leave alone (sandbox preserved)") and the only
		// machine-readable surface disagreed with (gt-t6k2).
		//
		// StateHandedOff deliberately keeps restart. The reuse gate does NOT
		// accept it, so unlike the two above it really is a slot nothing else
		// will reclaim, and restart is the one lever the restart-first policy
		// leaves a witness.
		if polecat.StateEligibleForPoolReuse(state) {
			return "leave-alone"
		}
		return witnessActionRestart
	}
}
