//go:build !windows

package git

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A stalled fetch is not a slow fetch, and that is what makes it dangerous.
//
// Measured on 2026-08-23 (gt-i9wz): two `gt patrol branches` invocations wedged
// on two different rigs at the same moment, each with an ssh child past its
// handshake that had written 22 bytes and then moved ZERO across a 25-second
// sample, still alive at 4:27. A fresh ssh to the same host authenticated in
// under a second during the stall. Nothing in gt → git → ssh had a deadline, so
// there was no elapsed time at which any of them would have given up.
//
// These tests hold the fetch paths to three properties: the call returns, the
// whole process TREE dies with it, and a sweep does not pay the deadline once
// per branch.

// TestFetchTimeoutDefault pins the value production actually runs with.
//
// fetchTimeout is a var so the tests below can shorten it, and a shortened
// fixture is exactly how a timeout gets asserted at a value nobody ships:
// every test passes against 2 seconds while the binary ships something else, or
// ships zero. This is the control for that.
func TestFetchTimeoutDefault(t *testing.T) {
	if fetchTimeout != 120*time.Second {
		t.Fatalf("fetchTimeout = %v, want 120s — tests below shorten it, so this is the only assertion about what ships", fetchTimeout)
	}
	if fetchTimeout <= lsRemoteTimeout {
		t.Fatalf("fetchTimeout %v must exceed lsRemoteTimeout %v: a fetch transfers objects where ls-remote reads an advertisement", fetchTimeout, lsRemoteTimeout)
	}
}

// shortFetchTimeout shortens the fetch deadline for one test and restores it.
func shortFetchTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	original := fetchTimeout
	fetchTimeout = d
	t.Cleanup(func() { fetchTimeout = original })
}

// blackholeRemote starts a listener that accepts connections and then says
// nothing, ever. This is the failure being reproduced and not a proxy for it: a
// refused connection or an unresolvable host errors immediately and would prove
// nothing about a deadline, because the deadline would never be reached.
func blackholeRemote(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var held []net.Conn
		for {
			conn, err := ln.Accept()
			if err != nil {
				for _, c := range held {
					_ = c.Close()
				}
				return
			}
			// Held open, deliberately unread and unanswered.
			held = append(held, conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return "git://" + ln.Addr().String() + "/blackhole.git"
}

// TestFetchPruneReturnsWhenTheRemoteStopsAnswering is the property the witness
// needs: the call comes back. `gt patrol branches` refreshes its comparison
// target with exactly this, and an unbounded one parks a witness inside patrol
// step sweep-branches, where the pane still shows "esc to interrupt" and it
// therefore reads as WORKING from every liveness surface the town has.
func TestFetchPruneReturnsWhenTheRemoteStopsAnswering(t *testing.T) {
	shortFetchTimeout(t, 2*time.Second)
	dir := initTestRepo(t)
	g := NewGit(dir)
	runGit(t, dir, "remote", "add", "origin", blackholeRemote(t))

	start := time.Now()
	err := g.FetchPrune("origin")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("FetchPrune against a blackholed remote returned nil: a stall must be an error, never an all-clear")
	}
	if !IsRemoteUnresponsive(err) {
		t.Fatalf("FetchPrune error = %v, want one matching ErrRemoteUnresponsive — callers that sweep many refs key on this to stop asking", err)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("FetchPrune took %v against a 2s deadline", elapsed)
	}
}

// TestFetchDeadlineKillsTheSshGrandchild is the half a naive timeout misses.
//
// Measured on the wedged chain, each layer had to be signalled individually:
// killing gt left `git fetch` running, and killing git left ssh running,
// REPARENTED to init, still holding a dead TCP connection to github with no
// parent left to attribute it to. A deadline that kills only its immediate
// child does not end that — it converts a visible hang into an orphan nothing
// in the town will ever reap, one per rig per patrol cycle.
func TestFetchDeadlineKillsTheSshGrandchild(t *testing.T) {
	shortFetchTimeout(t, 2*time.Second)
	dir := initTestRepo(t)
	g := NewGit(dir)

	pidFile := filepath.Join(t.TempDir(), "ssh.pid")
	sshScript := filepath.Join(t.TempDir(), "hang-ssh.sh")
	// exec, so the pid recorded is the pid of the process that survives: a
	// script that forked would let the test pass while the real sleeper lived.
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nexec sleep 600\n"
	if err := os.WriteFile(sshScript, []byte(script), 0o755); err != nil {
		t.Fatalf("write ssh script: %v", err)
	}

	runGit(t, dir, "remote", "add", "origin", "ssh://git@127.0.0.1/blackhole.git")
	runGit(t, dir, "config", "core.sshCommand", sshScript)

	start := time.Now()
	err := g.Fetch("origin")
	if err == nil {
		t.Fatal("Fetch through a hanging ssh returned nil")
	}
	if !IsRemoteUnresponsive(err) {
		t.Fatalf("Fetch error = %v, want ErrRemoteUnresponsive", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("Fetch took %v against a 2s deadline", elapsed)
	}

	pid := readPIDFile(t, pidFile)
	// The kill is asynchronous with respect to our return, so poll rather than
	// sample once — a single immediate sample would be flaky in the direction
	// that FAILS a correct fix, which is the tolerable direction but still noise.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Do not leave a 10-minute sleeper behind on a failure.
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("ssh grandchild %d survived the fetch deadline: the timeout killed the process, not the process GROUP, so the stall was orphaned rather than ended", pid)
}

// TestFetchSucceedsAgainstARealRemote is the positive control for both tests
// above. A deadline that also broke ordinary fetches would satisfy every
// assertion about hanging while making the tool useless, and the two failures
// are indistinguishable from a red suite alone.
func TestFetchSucceedsAgainstARealRemote(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)
	if err := g.FetchPrune("origin"); err != nil {
		t.Fatalf("FetchPrune against a live remote: %v", err)
	}
	if err := g.FetchBranch("origin", mainBranch); err != nil {
		t.Fatalf("FetchBranch against a live remote: %v", err)
	}
}

// TestSshKeepaliveEnvIsInstalledByDefault covers the layer that works after gt
// is gone.
//
// The deadline only binds while gt is alive to enforce it, and the gastown
// witness ran its patrol under `timeout 240` — which kills gt and nothing else.
// ServerAliveInterval runs INSIDE the orphan, so it is the only thing that can
// terminate an ssh whose parent has already been reaped.
func TestSshKeepaliveEnvIsInstalledByDefault(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
	g := NewGit(initTestRepo(t))

	env := g.sshKeepaliveEnv()
	if len(env) != 1 {
		t.Fatalf("sshKeepaliveEnv() = %v, want exactly one GIT_SSH_COMMAND entry", env)
	}
	value := strings.TrimPrefix(env[0], "GIT_SSH_COMMAND=")
	if value == env[0] {
		t.Fatalf("sshKeepaliveEnv() = %q, want a GIT_SSH_COMMAND= assignment", env[0])
	}
	// Asserted by behaviour rather than by string equality with the constant,
	// which would only prove the constant equals itself.
	for _, want := range []string{"ServerAliveInterval=15", "ServerAliveCountMax=4", "ConnectTimeout=10"} {
		if !strings.Contains(value, want) {
			t.Fatalf("GIT_SSH_COMMAND = %q, missing %s", value, want)
		}
	}
}

// TestSshKeepaliveEnvYieldsToAConfiguredSshCommand keeps the fix from breaking
// authentication to cure a timeout. GIT_SSH_COMMAND overrides core.sshCommand
// AND GIT_SSH, so setting it unconditionally would silently replace a rig's
// configured transport — and a rig that cannot authenticate does not hang, it
// fails, which is a different and louder kind of broken.
func TestSshKeepaliveEnvYieldsToAConfiguredSshCommand(t *testing.T) {
	t.Setenv("GIT_SSH", "")

	t.Run("core.sshCommand", func(t *testing.T) {
		t.Setenv("GIT_SSH_COMMAND", "")
		dir := initTestRepo(t)
		runGit(t, dir, "config", "core.sshCommand", "ssh -i /custom/key")
		if env := NewGit(dir).sshKeepaliveEnv(); env != nil {
			t.Fatalf("sshKeepaliveEnv() = %v with core.sshCommand set, want nil", env)
		}
	})

	t.Run("GIT_SSH_COMMAND", func(t *testing.T) {
		t.Setenv("GIT_SSH_COMMAND", "ssh -i /custom/key")
		if env := NewGit(initTestRepo(t)).sshKeepaliveEnv(); env != nil {
			t.Fatalf("sshKeepaliveEnv() = %v with GIT_SSH_COMMAND set, want nil", env)
		}
	})
}

// TestDeadlineErrorNamesTheSubcommand guards the message a wedged agent reads.
//
// The subcommand is taken before --git-dir is prepended: taken after, every
// bare-repo timeout reported itself as "git --git-dir=/... timed out", which
// names the flag rather than the operation and sends the reader looking in the
// wrong place.
func TestDeadlineErrorNamesTheSubcommand(t *testing.T) {
	err := deadlineError("fetch", 90*time.Second)
	if !errors.Is(err, ErrRemoteUnresponsive) {
		t.Fatalf("deadlineError does not wrap ErrRemoteUnresponsive: %v", err)
	}
	for _, want := range []string{"fetch", "1m30s", "process group"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("deadlineError = %q, missing %q", err.Error(), want)
		}
	}
	if IsRemoteUnresponsive(errors.New("git fetch: some other failure")) {
		t.Fatal("IsRemoteUnresponsive matched an ordinary git failure: a sweep would then stop comparing branches because ONE branch was bad")
	}
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("ssh stand-in never recorded its pid at %s — it was not invoked, so this test proves nothing about killing it", path)
	return 0
}

// processAlive reports whether pid still exists. Signal 0 performs the
// permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
