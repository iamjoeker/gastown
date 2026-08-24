package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

// writeAccounts lays down an accounts.json at the path
// ResolveTownRuntimeConfigDir will look for, under a fresh town root.
func writeAccounts(t *testing.T, body string) string {
	t.Helper()
	townRoot := t.TempDir()
	accountsPath := constants.MayorAccountsPath(townRoot)
	if err := os.MkdirAll(filepath.Dir(accountsPath), 0o755); err != nil {
		t.Fatalf("creating accounts dir: %v", err)
	}
	if err := os.WriteFile(accountsPath, []byte(body), 0o600); err != nil {
		t.Fatalf("writing accounts.json: %v", err)
	}
	return townRoot
}

// TestResolveTownRuntimeConfigDir_PrefersAccountsOverEnv pins the priority that
// makes the gt-gllf fix work at all.
//
// The daemon that spawns dogs and Boot generally has no CLAUDE_CONFIG_DIR of
// its own, so a resolver that consulted the environment first would return
// empty for exactly the callers that needed it — which is how those two paths
// stayed broken while six others worked. accounts.json has to win.
func TestResolveTownRuntimeConfigDir_PrefersAccountsOverEnv(t *testing.T) {
	townRoot := writeAccounts(t, `{
		"default": "town",
		"accounts": {"town": {"config_dir": "/accounts/town"}}
	}`)
	// A wrong ambient value: if this is what comes back, the priority is inverted.
	t.Setenv("CLAUDE_CONFIG_DIR", "/ambient/wrong")
	t.Setenv("GT_ACCOUNT", "")

	if got := ResolveTownRuntimeConfigDir(townRoot); got != "/accounts/town" {
		t.Errorf("ResolveTownRuntimeConfigDir = %q, want the accounts.json value %q", got, "/accounts/town")
	}
}

// TestResolveTownRuntimeConfigDir_FallsBackToEnv covers the spawn paths whose
// process does have the variable but whose town has no accounts.json — the
// pre-existing behaviour the getenv fallback preserves.
func TestResolveTownRuntimeConfigDir_FallsBackToEnv(t *testing.T) {
	townRoot := t.TempDir() // no accounts.json at all
	t.Setenv("CLAUDE_CONFIG_DIR", "/ambient/value")
	t.Setenv("GT_ACCOUNT", "")

	if got := ResolveTownRuntimeConfigDir(townRoot); got != "/ambient/value" {
		t.Errorf("ResolveTownRuntimeConfigDir = %q, want the ambient value %q", got, "/ambient/value")
	}
}

// TestResolveTownRuntimeConfigDir_EmptyWhenNeitherConfigured is the
// no-regression case. An empty return means callers pass an empty
// RuntimeConfigDir, AgentEnv omits CLAUDE_CONFIG_DIR, and the session inherits
// whatever it would have inherited before this change.
func TestResolveTownRuntimeConfigDir_EmptyWhenNeitherConfigured(t *testing.T) {
	townRoot := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("GT_ACCOUNT", "")

	if got := ResolveTownRuntimeConfigDir(townRoot); got != "" {
		t.Errorf("ResolveTownRuntimeConfigDir = %q, want empty", got)
	}
}

// TestResolveTownRuntimeConfigDir_MalformedAccountsFallsBack checks that a
// broken accounts.json degrades to the ambient value rather than to empty.
// ResolveAccountConfigDir swallows load errors, so this documents that the
// swallow is survivable here: the agent still gets an account rather than
// silently landing on the default one.
func TestResolveTownRuntimeConfigDir_MalformedAccountsFallsBack(t *testing.T) {
	townRoot := writeAccounts(t, `{ this is not json`)
	t.Setenv("CLAUDE_CONFIG_DIR", "/ambient/value")
	t.Setenv("GT_ACCOUNT", "")

	if got := ResolveTownRuntimeConfigDir(townRoot); got != "/ambient/value" {
		t.Errorf("ResolveTownRuntimeConfigDir = %q, want the ambient value %q", got, "/ambient/value")
	}
}
