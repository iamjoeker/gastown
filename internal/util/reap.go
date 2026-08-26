package util

import (
	"os/exec"
	"sync"
)

// Fire-and-forget children must still be waited on.
//
// A process that has exited stays in the process table as a zombie until its
// PARENT calls wait(2). Nothing else reaps it: not Setpgid, not Setsid, not
// os.Process.Release — Release only drops Go's handle on the process, it does
// not collect the wait status. For a short-lived gt command the zombie is
// cleaned up when the command exits and init inherits the corpse. For a
// long-lived parent — `gt daemon run`, the proxy server, a TUI — it accumulates
// for the parent's whole lifetime.
//
// That is not a memory problem (a zombie holds only its exit status), but it
// corrupts gt's own process accounting: a zombie still has a PID, still appears
// in ps and /proc, and still matches pgrep. Anything that counts live children
// by walking the process table counts the dead ones too.
//
// ReapDetached is the one line every fire-and-forget Start needs.

// ReapDetached waits for a started command in the background so it cannot
// linger as a zombie. Call it immediately after a successful cmd.Start() for
// any child whose exit status the caller does not otherwise collect.
//
// The caller keeps using cmd.Process (its PID is still valid, and signaling it
// still works) — ReapDetached only takes ownership of the wait. It must not be
// paired with a later cmd.Wait(): the second Wait returns an error and the
// races are the usual ones for double-wait.
func ReapDetached(cmd *exec.Cmd) {
	ReapDetachedFunc(cmd, nil)
}

// ReapDetachedFunc waits for a started command in the background and reports
// how it ended. onExit runs on the reaping goroutine after the wait completes,
// and is where a caller turns an unexplained SIGKILL into a logged one — a
// detached child that is OOM-killed can leave no other trace.
//
// onExit may be nil.
func ReapDetachedFunc(cmd *exec.Cmd, onExit func(ExitInfo)) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	go func() {
		err := cmd.Wait()
		if onExit != nil {
			onExit(ClassifyExitError(err, nil))
		}
	}()
}

// ReapGroup waits for a set of detached children and lets the caller block on
// all of them at shutdown. Use it where a long-lived parent wants both the
// reaping and an orderly drain; plain ReapDetached is enough everywhere else.
type ReapGroup struct {
	wg sync.WaitGroup
}

// Add starts reaping cmd as part of the group.
func (g *ReapGroup) Add(cmd *exec.Cmd, onExit func(ExitInfo)) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		err := cmd.Wait()
		if onExit != nil {
			onExit(ClassifyExitError(err, nil))
		}
	}()
}

// Wait blocks until every child added to the group has been reaped.
func (g *ReapGroup) Wait() {
	g.wg.Wait()
}
