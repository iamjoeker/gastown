package daemon

import (
	"bytes"
	"log"
	"net"
	"strings"
	"testing"
)

// newTestDaemonForReachability builds a minimal Daemon sufficient to call
// ensureDoltReachableAtStartup, capturing its log output for assertions.
func newTestDaemonForReachability(t *testing.T, townRoot string) (*Daemon, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(&buf, "", 0),
	}
	return d, &buf
}

// TestEnsureDoltReachableAtStartupRemoteDoesNotAttemptStart verifies that when
// Dolt is configured as remote and unreachable, the daemon reports the
// problem but does not try to start a local process (gt has no business
// starting a process on another host).
func TestEnsureDoltReachableAtStartupRemoteDoesNotAttemptStart(t *testing.T) {
	townRoot := t.TempDir()
	// A hostname that does not resolve to loopback makes IsRemote() true and,
	// since nothing is listening there, CheckServerReachable() fails fast.
	t.Setenv("GT_DOLT_HOST", "dolt-remote.invalid.test")
	t.Setenv("GT_DOLT_PORT", "1")

	d, buf := newTestDaemonForReachability(t, townRoot)
	d.ensureDoltReachableAtStartup()

	out := buf.String()
	if !strings.Contains(out, "unreachable at daemon startup") {
		t.Fatalf("expected unreachable log line, got: %s", out)
	}
	if !strings.Contains(out, "remote") {
		t.Fatalf("expected remote-server log line, got: %s", out)
	}
	if strings.Contains(out, "Attempting to start Dolt server") {
		t.Fatalf("must not attempt to start a local process for a remote server: %s", out)
	}
}

// TestEnsureDoltReachableAtStartupNoOpWhenReachable verifies the fast path:
// when Dolt is already reachable, nothing is logged and no start is attempted.
func TestEnsureDoltReachableAtStartupNoOpWhenReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	townRoot := t.TempDir()
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	t.Setenv("GT_DOLT_HOST", host)
	t.Setenv("GT_DOLT_PORT", port)

	d, buf := newTestDaemonForReachability(t, townRoot)
	d.ensureDoltReachableAtStartup()

	if out := buf.String(); out != "" {
		t.Fatalf("expected no log output when already reachable, got: %s", out)
	}
}
