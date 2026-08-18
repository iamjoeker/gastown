package tmux

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newPrivateTmux returns a Tmux connected to a tmux server that this test
// alone owns, already running so that bind-key works.
//
// The package-level server started by TestMain is shared by every test, and a
// tmux server's key tables are global to it: a test that binds prefix-n
// changes what every test after it observes. That is not hypothetical — it is
// how TestGetKeyBinding_CapturesDefaultBinding came to report a pass by
// skipping ("prefix-n is already a GT binding in this environment") once the
// cycle-binding tests ran before it. A skip is not a verdict, and which tests
// ran first is not a property anyone asserted.
//
// Tests that read or write key bindings therefore take a server of their own,
// so their result depends only on what they set up. The server is killed when
// the test finishes.
func newPrivateTmux(t *testing.T) *Tmux {
	t.Helper()
	if !hasTmux() {
		t.Skip("tmux not installed")
	}

	socket := privateSocketName(t)
	// bind-key and list-keys both need a running server. The sentinel session
	// keeps it up for the whole test even if the test kills its own sessions.
	if out, err := exec.Command("tmux", "-u", "-L", socket, "new-session", "-d", "-s", "sentinel").CombinedOutput(); err != nil {
		t.Fatalf("starting private tmux server on socket %s: %v\n%s", socket, err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})

	return NewTmuxWithSocket(socket)
}

// privateSocketName builds a socket name unique to one test. tmux places the
// socket at /tmp/tmux-<uid>/<name>, so the name is kept short and free of path
// separators (subtest names contain "/").
func privateSocketName(t *testing.T) string {
	t.Helper()
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, t.Name())
	const maxNameLen = 48
	if len(safe) > maxNameLen {
		safe = safe[len(safe)-maxNameLen:]
	}
	// The pid keeps concurrent `go test` runs of this package apart; the
	// nanosecond keeps a retried or repeated (-count=N) test apart from its
	// own earlier socket, which the kernel may not have reaped yet.
	return fmt.Sprintf("gt-t%d-%d-%s", os.Getpid(), time.Now().UnixNano()%1e6, safe)
}

// fixtureTownRoot writes a town whose mayor/rigs.json registers exactly the
// given rig prefixes, points GT_ROOT and GT_TOWN_ROOT at it, and returns the
// root.
//
// sessionPrefixPattern derives its alternation from the registered rigs, so
// without this a test comparing against sessionPrefixPattern() is really
// comparing against the rigs installed on the machine running it. That makes
// the pattern unpredictable (it was "^(bd|dn|gt|hq)-" on the host that found
// this) and, worse, lets a test go vacuously green: on a machine with no rigs
// registered the pattern collapses to "^(gt|hq)-", which is exactly the
// "stale" pattern the cycle-binding tests are supposed to detect as different.
func fixtureTownRoot(t *testing.T, prefixes ...string) string {
	t.Helper()

	root := t.TempDir()
	mayorDir := filepath.Join(root, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	type beadsCfg struct {
		Prefix string `json:"prefix"`
	}
	type rigEntry struct {
		GitURL string   `json:"git_url"`
		Beads  beadsCfg `json:"beads"`
	}
	rigs := map[string]rigEntry{}
	for _, p := range prefixes {
		rigs[p] = rigEntry{GitURL: "https://example.invalid/" + p + ".git", Beads: beadsCfg{Prefix: p + "-"}}
	}
	data, err := json.Marshal(map[string]any{"version": 1, "rigs": rigs})
	if err != nil {
		t.Fatalf("marshal rigs.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), data, 0o644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}

	t.Setenv("GT_ROOT", root)
	t.Setenv("GT_TOWN_ROOT", root)
	return root
}

// TestSessionPrefixPattern_FromFixtureRigs pins the pattern sessionPrefixPattern
// builds from a known rigs.json. "gt" and "hq" are always included; registered
// rig prefixes are added and the whole set is sorted.
func TestSessionPrefixPattern_FromFixtureRigs(t *testing.T) {
	fixtureTownRoot(t, "zz", "aa")

	if got, want := sessionPrefixPattern(), "^(aa|gt|hq|zz)-"; got != want {
		t.Errorf("sessionPrefixPattern() = %q, want %q", got, want)
	}
}

// TestBindKeyLineKey covers the parse that replaced the list-keys [key] filter,
// including the escaped key names tmux emits for punctuation.
func TestBindKeyLineKey(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		table   string
		wantKey string
		wantOK  bool
	}{
		{
			name:    "plain key",
			line:    `bind-key    -T prefix n       next-window`,
			table:   "prefix",
			wantKey: "n",
			wantOK:  true,
		},
		{
			name:    "repeat flag before table",
			line:    `bind-key -r -T prefix Up      select-pane -U`,
			table:   "prefix",
			wantKey: "Up",
			wantOK:  true,
		},
		{
			name:    "escaped key",
			line:    `bind-key    -T prefix \#      list-buffers`,
			table:   "prefix",
			wantKey: "#",
			wantOK:  true,
		},
		{
			name:   "different table",
			line:   `bind-key    -T root MouseDown1Pane  select-pane -t =`,
			table:  "prefix",
			wantOK: false,
		},
		{
			name:   "no table flag",
			line:   `bind-key n next-window`,
			table:  "prefix",
			wantOK: false,
		},
		{
			name:   "truncated line",
			line:   `bind-key -T prefix`,
			table:  "prefix",
			wantOK: false,
		},
		{
			name:   "empty line",
			line:   ``,
			table:  "prefix",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, ok := bindKeyLineKey(tt.line, tt.table)
			if ok != tt.wantOK {
				t.Fatalf("bindKeyLineKey(%q, %q) ok = %v, want %v", tt.line, tt.table, ok, tt.wantOK)
			}
			if ok && key != tt.wantKey {
				t.Errorf("bindKeyLineKey(%q, %q) key = %q, want %q", tt.line, tt.table, key, tt.wantKey)
			}
		})
	}
}

// TestKeyBindingLine_MatchesKeyExactly guards the reason keyBindingLine exists:
// it must find a binding without using list-keys' [key] filter argument, which
// tmux 3.7 answers with nothing for every key. It also must not confuse a key
// with another key that merely starts the same way.
func TestKeyBindingLine_MatchesKeyExactly(t *testing.T) {
	tm := newPrivateTmux(t)

	if _, err := tm.run("bind-key", "-T", "prefix", "F11", "display-message", "f11-cmd"); err != nil {
		t.Fatalf("bind F11: %v", err)
	}
	if _, err := tm.run("bind-key", "-T", "prefix", "F1", "display-message", "f1-cmd"); err != nil {
		t.Fatalf("bind F1: %v", err)
	}

	if got := tm.keyBindingLine("prefix", "F11"); !strings.Contains(got, "f11-cmd") {
		t.Errorf("keyBindingLine(prefix, F11) = %q, want it to contain %q", got, "f11-cmd")
	}
	if got := tm.keyBindingLine("prefix", "F1"); !strings.Contains(got, "f1-cmd") {
		t.Errorf("keyBindingLine(prefix, F1) = %q, want it to contain %q", got, "f1-cmd")
	}
	if got := tm.keyBindingLine("prefix", "F12"); got != "" {
		t.Errorf("keyBindingLine(prefix, F12) = %q, want %q for an unbound key", got, "")
	}
	// The key table is per-table, not global.
	if got := tm.keyBindingLine("root", "F11"); got != "" {
		t.Errorf("keyBindingLine(root, F11) = %q, want %q", got, "")
	}
}
