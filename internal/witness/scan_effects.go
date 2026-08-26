package witness

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/tmux"
)

// ScanOptions controls what a patrol detection sweep is permitted to do.
//
// A scan's failure mode is destroying in-flight work: it restarts sessions, nukes
// worktrees and writes beads on evidence that can be stale by the time it acts.
// The guard that stops it (decideRestart) could not be exercised without putting
// a live polecat at risk, which left it unfalsifiable by construction — the only
// test was the accident. A mayor ruling on `gt patrol scan` had to be split
// rather than lifted for exactly that reason: permitted on rigs with no working
// polecat, still held wherever any polecat is working (gt-3516, hq-xwulu).
//
// DryRun closes that gap. The sweep runs whole — every probe, every
// classification, and specifically decideRestart — and reports the action it
// WOULD take, while every mutation is declined.
type ScanOptions struct {
	// DryRun performs the full scan and reports per-polecat verdicts without
	// restarting a session, nuking a worktree, writing a bead, or sending mail.
	DryRun bool
}

func (o ScanOptions) effects() scanEffects { return scanEffects{dryRun: o.DryRun} }

// DryRunWispID stands in for the cleanup wisp a live scan would have created.
// It is deliberately not a well-formed bead ID: anything that mistakes it for one
// and tries to act on it fails loudly instead of reaching a real bead.
const DryRunWispID = "(would-create)"

// scanEffects is the single place a patrol scan mutates the world. Every
// destructive or persistent operation reachable from a sweep goes through one of
// these methods, so a dry run is inert by construction rather than by each call
// site remembering to check a flag.
//
// The zero value performs everything, which is what a live scan wants and what a
// caller that forgets to plumb options gets.
type scanEffects struct {
	dryRun bool
}

// The primitives below are reached through package-level vars, following the
// existing idiom in this package (see verifyBranchAlreadyMerged). It is what
// lets a test assert the gate BOTH ways from one harness: that a dry run
// performs none of them, and — the half that can actually fail — that a live
// run performs each one. A gate proven only in the direction where nothing
// happens is not proven at all.
var (
	effRestartSession          = RestartPolecatSession
	effNukePolecat             = NukePolecat
	effCreateCleanupWisp       = createCleanupWisp
	effUpdateCleanupWispState  = UpdateCleanupWispState
	effClearCompletionMetadata = clearCompletionMetadata
	effNudgeRefinery           = nudgeRefinery
	effNotifyMayorSlotOpen     = notifyMayorSlotOpen
	effCloseBead               = func(bd *BdCli, workDir, beadID, reason string) {
		_, _ = bd.Exec(workDir, "close", beadID, "--reason="+reason)
	}
	effNudgeSession = func(t *tmux.Tmux, sessionName, message string) error {
		return t.NudgeSession(sessionName, message)
	}
	effDismissStartupDialogs = func(t *tmux.Tmux, sessionName string) error {
		return t.DismissStartupDialogsBlind(sessionName)
	}
)

func (e scanEffects) restartSession(workDir, rigName, polecatName string) error {
	if e.dryRun {
		return nil
	}
	return effRestartSession(workDir, rigName, polecatName)
}

func (e scanEffects) nukePolecat(bd *BdCli, workDir, rigName, polecatName string) error {
	if e.dryRun {
		return nil
	}
	return effNukePolecat(bd, workDir, rigName, polecatName)
}

func (e scanEffects) createCleanupWisp(bd *BdCli, workDir, polecatName, issueID, branch string) (string, error) {
	if e.dryRun {
		return DryRunWispID, nil
	}
	return effCreateCleanupWisp(bd, workDir, polecatName, issueID, branch)
}

func (e scanEffects) updateCleanupWispState(bd *BdCli, workDir, wispID, newState string) error {
	if e.dryRun {
		return nil
	}
	return effUpdateCleanupWispState(bd, workDir, wispID, newState)
}

func (e scanEffects) closeBead(bd *BdCli, workDir, beadID, reason string) {
	if e.dryRun {
		return
	}
	effCloseBead(bd, workDir, beadID, reason)
}

func (e scanEffects) clearCompletionMetadata(bd *BdCli, workDir, agentBeadID string) error {
	if e.dryRun {
		return nil
	}
	return effClearCompletionMetadata(bd, workDir, agentBeadID)
}

func (e scanEffects) nudgeSession(t *tmux.Tmux, sessionName, message string) error {
	if e.dryRun {
		return nil
	}
	return effNudgeSession(t, sessionName, message)
}

func (e scanEffects) dismissStartupDialogs(t *tmux.Tmux, sessionName string) error {
	if e.dryRun {
		return nil
	}
	return effDismissStartupDialogs(t, sessionName)
}

func (e scanEffects) nudgeRefinery(townRoot, rigName string) error {
	if e.dryRun {
		return nil
	}
	return effNudgeRefinery(townRoot, rigName)
}

func (e scanEffects) notifyMayorSlotOpen(workDir, rigName, polecatName, exitType string) {
	if e.dryRun {
		return
	}
	effNotifyMayorSlotOpen(workDir, rigName, polecatName, exitType)
}

// bdReadOnlySubcommands are the bd subcommands a detection sweep needs in order
// to observe. Anything not listed here is treated as a write.
//
// The list is an allowlist rather than a denylist on purpose: a bd subcommand
// added later is unknown to this file, and the safe reading of unknown is "may
// write". A denylist would let it through.
var bdReadOnlySubcommands = map[string]bool{
	"show":     true,
	"list":     true,
	"query":    true,
	"dep":      true,
	"ready":    true,
	"children": true,
	"stats":    true,
}

// ReadOnlyBdCli wraps a BdCli so any subcommand outside the read-only set fails
// instead of executing.
//
// This is a backstop, not the mechanism — scanEffects is what makes a dry run
// inert. It exists because gating N call sites is exactly the shape of change
// where one site gets missed, and a missed site here writes to the production
// bead store. A blocked write surfaces as an error in the scan output; a missed
// gate would not surface at all, which is the failure this whole flag exists to
// stop being possible.
func ReadOnlyBdCli(bd *BdCli) *BdCli {
	if bd == nil {
		return nil
	}
	refuse := func(args []string) error {
		sub := ""
		if len(args) > 0 {
			sub = args[0]
		}
		return fmt.Errorf("dry-run: refusing bd write %q (this is a bug — the scan reached an ungated mutation)", sub)
	}
	readable := func(args []string) bool {
		return len(args) > 0 && bdReadOnlySubcommands[args[0]]
	}
	return &BdCli{
		Exec: func(workDir string, args ...string) (string, error) {
			if !readable(args) {
				return "", refuse(args)
			}
			return bd.Exec(workDir, args...)
		},
		Run: func(workDir string, args ...string) error {
			if !readable(args) {
				return refuse(args)
			}
			return bd.Run(workDir, args...)
		},
	}
}
