package polecat

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
)

// writeAccountsConfig lays down a town's mayor/accounts.json with one default
// account, and returns the config dir that account resolves to.
func writeAccountsConfig(t *testing.T, townRoot string) string {
	t.Helper()

	configDir := filepath.Join(townRoot, "accounts", "town")
	cfg := config.AccountsConfig{
		Version: config.CurrentAccountsVersion,
		Accounts: map[string]config.Account{
			"town": {Email: "town@example.test", ConfigDir: configDir},
		},
		Default: "town",
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshalling accounts config: %v", err)
	}
	path := constants.MayorAccountsPath(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return configDir
}

// TestResolveSessionRuntimeConfigDir is the regression test for gt-acb1.
//
// The defect was not in this function — it is that no such function existed, and
// SessionManager.Start passed opts.RuntimeConfigDir straight through. Three of
// its four callers (`gt session start`, `gt session restart`, `gt up`) built a
// zero-valued SessionStartOptions, so the session came up with no
// CLAUDE_CONFIG_DIR, fell back to the default ~/.claude profile, and the agent
// was logged out with nothing reporting an auth problem.
//
// The empty-town case is asserted as hard as the resolving one, because the
// fallback must not invent an account where the town has none: a town with no
// mayor/accounts.json and no CLAUDE_CONFIG_DIR in the environment has to keep
// behaving exactly as it did.
func TestResolveSessionRuntimeConfigDir(t *testing.T) {
	t.Run("empty caller resolves the town account", func(t *testing.T) {
		townRoot := t.TempDir()
		// ResolveTownRuntimeConfigDir consults CLAUDE_CONFIG_DIR only when the
		// town resolves nothing; clear it so this case measures the town road.
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		t.Setenv("GT_ACCOUNT", "")
		want := writeAccountsConfig(t, townRoot)

		if got := resolveSessionRuntimeConfigDir(townRoot, ""); got != want {
			t.Errorf("resolveSessionRuntimeConfigDir(townRoot, %q) = %q, want %q — "+
				"a session started with no account comes up logged out (gt-acb1)", "", got, want)
		}
	})

	t.Run("an explicit account is never overridden", func(t *testing.T) {
		townRoot := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		t.Setenv("GT_ACCOUNT", "")
		townAccount := writeAccountsConfig(t, townRoot)

		explicit := filepath.Join(townRoot, "accounts", "chosen")
		got := resolveSessionRuntimeConfigDir(townRoot, explicit)
		if got != explicit {
			t.Errorf("resolveSessionRuntimeConfigDir(townRoot, %q) = %q, want %q",
				explicit, got, explicit)
		}
		// `gt polecat spawn --account` is the caller this protects: it resolves a
		// specific account and the fallback must not silently replace it with the
		// town default.
		if got == townAccount {
			t.Errorf("an explicitly resolved account was replaced by the town default %q", townAccount)
		}
	})

	t.Run("a town with no accounts resolves to empty", func(t *testing.T) {
		townRoot := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		t.Setenv("GT_ACCOUNT", "")

		if got := resolveSessionRuntimeConfigDir(townRoot, ""); got != "" {
			t.Errorf("resolveSessionRuntimeConfigDir on a town with no accounts.json = %q, want %q — "+
				"an empty result means 'leave CLAUDE_CONFIG_DIR unset', and inventing one here "+
				"would point every session at a profile that does not exist", got, "")
		}
	})

	t.Run("the ambient CLAUDE_CONFIG_DIR is the last resort", func(t *testing.T) {
		townRoot := t.TempDir()
		ambient := filepath.Join(townRoot, "ambient")
		t.Setenv("CLAUDE_CONFIG_DIR", ambient)
		t.Setenv("GT_ACCOUNT", "")

		if got := resolveSessionRuntimeConfigDir(townRoot, ""); got != ambient {
			t.Errorf("resolveSessionRuntimeConfigDir with no accounts.json = %q, want the ambient %q",
				got, ambient)
		}
	})
}

// TestSessionStartOptionsDoNotDecideTheAccount pins the property the fix rests
// on: an empty RuntimeConfigDir in the options struct must NOT mean "start this
// session with no account".
//
// It is separate from the table above because it is about the OPTION, not the
// resolver. The polecat session manager was the only agent session manager that
// took the account from its caller — dog, deacon, refinery and witness all call
// config.ResolveTownRuntimeConfigDir inside their own managers, where no caller
// can get it wrong. A rule that has to be remembered once per call site has the
// same shape as the defect it is guarding against, and three of four callers did
// not remember it (gt-acb1).
func TestSessionStartOptionsDoNotDecideTheAccount(t *testing.T) {
	townRoot := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("GT_ACCOUNT", "")
	want := writeAccountsConfig(t, townRoot)

	// The literal every un-fixed caller built.
	opts := SessionStartOptions{}
	if got := resolveSessionRuntimeConfigDir(townRoot, opts.RuntimeConfigDir); got != want {
		t.Fatalf("a zero-valued SessionStartOptions resolved to %q, want %q — this is exactly the "+
			"literal `gt session restart` passes, and the session it produced was logged out", got, want)
	}
}

// TestStartDoesNotReadTheRawOption closes the hole the two tests above cannot
// reach: they exercise the resolver, and a revert that put opts.RuntimeConfigDir
// back into the env build would leave both of them green while restoring the
// defect exactly.
//
// Reverting is easy to do by accident, because the raw field is right there and
// reads correctly. So the rule is structural: inside Start, the account is
// resolveSessionRuntimeConfigDir's answer, and the option field is read once —
// as that function's argument — and nowhere else. There are two env writes that
// need it (config.AgentEnv's RuntimeConfigDir and the non-Claude ConfigDirEnv
// fallback), and losing the fallback on either one produces a session that comes
// up logged out with nothing reporting an auth problem (gt-acb1).
//
// This is a check on the code's shape, which is a weaker thing than a check on
// its behaviour. It is here because the behavioural version needs a tmux seam
// the session manager does not have, and a shape check that names the exact
// failure beats no check.
func TestStartDoesNotReadTheRawOption(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "session_manager.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	var start *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Start" || fn.Recv == nil {
			continue
		}
		start = fn
		break
	}
	if start == nil {
		t.Fatal("could not find (*SessionManager).Start in session_manager.go — " +
			"this guard is now blind; re-aim it rather than deleting it")
	}

	// Every read of opts.RuntimeConfigDir inside Start, and whether it is the
	// sanctioned one (an argument to the resolver).
	sanctioned := make(map[ast.Node]bool)
	ast.Inspect(start.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "resolveSessionRuntimeConfigDir" {
			return true
		}
		for _, arg := range call.Args {
			sanctioned[arg] = true
		}
		return true
	})

	resolverCalls := len(sanctioned)
	var bare []string
	ast.Inspect(start.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "RuntimeConfigDir" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "opts" {
			return true
		}
		if sanctioned[ast.Expr(sel)] {
			return true
		}
		bare = append(bare, fset.Position(sel.Pos()).String())
		return true
	})

	if resolverCalls == 0 {
		t.Fatal("(*SessionManager).Start no longer calls resolveSessionRuntimeConfigDir, so a " +
			"session started without an explicit account comes up on the default profile and the " +
			"agent is logged out (gt-acb1)")
	}
	for _, at := range bare {
		t.Errorf("%s: Start reads opts.RuntimeConfigDir directly. Use the value from "+
			"resolveSessionRuntimeConfigDir instead — the raw field is empty for `gt session start`, "+
			"`gt session restart` and `gt up`, and an empty CLAUDE_CONFIG_DIR is what logs the agent "+
			"out (gt-acb1)", at)
	}
}
