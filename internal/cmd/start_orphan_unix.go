//go:build !windows

package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/util"
)

// Seams for tests. The real implementations shell out to ps and send signals to
// live PIDs, neither of which a unit test can do safely.
var (
	findOrphanedProcs = util.FindOrphanedClaudeProcesses
	findZombieProcs   = util.FindZombieClaudeProcesses
	sendSignal        = syscall.Kill
	sleepFor          = time.Sleep

	// orphanKillSettle is how long to wait after the final SIGKILL before
	// re-reading process state. SIGKILL is not synchronous: the kernel tears
	// the process down and reaps it a moment after the call returns, so an
	// immediate liveness check reports a false survivor.
	orphanKillSettle = 250 * time.Millisecond
)

// cleanupOrphanedClaude finds and kills orphaned Claude processes with a grace period.
// This is a simpler synchronous implementation that:
// 1. Finds orphaned processes (TTY-less, older than 60s, not in Gas Town sessions)
// 2. Sends SIGTERM to all of them
// 3. Waits for the grace period
// 4. Sends SIGKILL to any that are still alive
func cleanupOrphanedClaude(graceSecs int) {
	// Find orphaned processes
	orphans, err := util.FindOrphanedClaudeProcesses()
	if err != nil {
		fmt.Printf("  %s Warning: %v\n", style.Bold.Render("⚠"), err)
		return
	}

	if len(orphans) == 0 {
		fmt.Printf("  %s No orphaned processes found\n", style.Dim.Render("○"))
		return
	}

	// Send SIGTERM to all orphans
	var termPIDs []int
	for _, orphan := range orphans {
		if err := syscall.Kill(orphan.PID, syscall.SIGTERM); err != nil {
			if err != syscall.ESRCH {
				fmt.Printf("  %s PID %d: failed to send SIGTERM: %v\n",
					style.Bold.Render("⚠"), orphan.PID, err)
			}
			continue
		}
		termPIDs = append(termPIDs, orphan.PID)
		fmt.Printf("  %s PID %d: sent SIGTERM (waiting %ds before SIGKILL)\n",
			style.Bold.Render("→"), orphan.PID, graceSecs)
	}

	if len(termPIDs) == 0 {
		return
	}

	// Wait for grace period
	fmt.Printf("  %s Waiting %d seconds for processes to terminate gracefully...\n",
		style.Dim.Render("⏳"), graceSecs)
	time.Sleep(time.Duration(graceSecs) * time.Second)

	// Check which processes are still alive and send SIGKILL
	var killedCount, alreadyDeadCount int
	for _, pid := range termPIDs {
		// Check if process still exists
		if err := syscall.Kill(pid, 0); err != nil {
			// Process is gone (either died from SIGTERM or doesn't exist)
			alreadyDeadCount++
			continue
		}

		// Process still alive - send SIGKILL
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			if err != syscall.ESRCH {
				fmt.Printf("  %s PID %d: failed to send SIGKILL: %v\n",
					style.Bold.Render("⚠"), pid, err)
			}
			continue
		}
		killedCount++
		fmt.Printf("  %s PID %d: sent SIGKILL (did not respond to SIGTERM)\n",
			style.Bold.Render("✓"), pid)
	}

	if alreadyDeadCount > 0 {
		fmt.Printf("  %s %d process(es) terminated gracefully from SIGTERM\n",
			style.Bold.Render("✓"), alreadyDeadCount)
	}
	if killedCount == 0 && alreadyDeadCount > 0 {
		fmt.Printf("  %s All processes cleaned up successfully\n",
			style.Bold.Render("✓"))
	}
}

// verifyNoOrphans checks that no Claude processes survived shutdown, SIGKILLs
// any that did, and confirms afterwards that they are actually gone. It returns
// an error naming the PIDs still running, so the caller cannot report a clean
// shutdown over a town that still has live agents in it.
//
// gt-dr6t: this used to discard every syscall.Kill error and return nothing, so
// the phase named "Verifying shutdown" verified nothing — `gt down` printed
// "shutdown complete" and exited 0 while the processes it had just failed to
// kill were still running. A verification step that cannot fail is not a
// verification step. Measure the state after the kill; never report the fact
// that the kill was attempted.
func verifyNoOrphans() error {
	orphans, err := findOrphanedProcs()
	if err != nil {
		fmt.Printf("  %s Could not verify: %v\n", style.Bold.Render("⚠"), err)
		return fmt.Errorf("cannot confirm that no agent processes survived shutdown: %w", err)
	}

	// Also check for zombie processes (have TTY but no tmux session)
	zombies, zErr := findZombieProcs()
	if zErr != nil {
		// Non-fatal, FindOrphanedClaudeProcesses already covered TTY-less ones
		zombies = nil
	}

	totalSurvivors := len(orphans) + len(zombies)
	if totalSurvivors == 0 {
		fmt.Printf("  %s No orphaned Claude processes detected\n", style.Bold.Render("✓"))
		return nil
	}

	fmt.Printf("  %s %d Claude process(es) survived shutdown:\n",
		style.Bold.Render("⚠"), totalSurvivors)

	// Kill orphans (TTY-less), then zombies (have TTY but no tmux session)
	pids := make([]int, 0, totalSurvivors)
	for _, o := range orphans {
		fmt.Printf("    PID %d (%s, age %ds) - sending SIGKILL\n", o.PID, o.Cmd, o.Age)
		pids = append(pids, o.PID)
	}
	for _, z := range zombies {
		fmt.Printf("    PID %d (%s, age %ds, tty %s) - sending SIGKILL\n", z.PID, z.Cmd, z.Age, z.TTY)
		pids = append(pids, z.PID)
	}

	return killAndConfirmGone(pids)
}

// killAndConfirmGone SIGKILLs each PID and then re-reads process state to find
// out which ones actually died. It reports what is still running, not what was
// attempted.
func killAndConfirmGone(pids []int) error {
	unconfirmed := make([]int, 0, len(pids))
	for _, pid := range pids {
		err := sendSignal(pid, syscall.SIGKILL)
		switch {
		case err == nil:
			// Signal delivered. Whether it worked is decided below.
		case errors.Is(err, syscall.ESRCH):
			// Already gone — that is the outcome we wanted.
			continue
		default:
			// Could not even deliver the signal (EPERM, typically). Still a
			// survivor until the liveness check says otherwise.
			fmt.Printf("    %s PID %d: SIGKILL failed: %v\n", style.Bold.Render("⚠"), pid, err)
		}
		unconfirmed = append(unconfirmed, pid)
	}

	if len(unconfirmed) > 0 {
		sleepFor(orphanKillSettle)
	}

	var alive []int
	for _, pid := range unconfirmed {
		// Signal 0 delivers nothing and only reports reachability. ESRCH means
		// the process is gone; EPERM means it exists but is not ours to signal,
		// which is still a survivor.
		err := sendSignal(pid, 0)
		if err == nil || errors.Is(err, syscall.EPERM) {
			alive = append(alive, pid)
		}
	}

	if len(alive) == 0 {
		fmt.Printf("  %s All %d surviving process(es) killed\n", style.Bold.Render("✓"), len(pids))
		return nil
	}

	list := make([]string, len(alive))
	for i, pid := range alive {
		list[i] = strconv.Itoa(pid)
	}
	joined := strings.Join(list, ", ")
	fmt.Printf("  %s %d process(es) survived SIGKILL: %s\n", style.ErrorPrefix, len(alive), joined)
	return fmt.Errorf("%d agent process(es) survived shutdown and could not be killed (PID %s); "+
		"inspect them with 'gt orphans procs list --aggressive' before starting the town again",
		len(alive), joined)
}
