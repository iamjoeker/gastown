package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sessionCreators are the tmux entry points that bring an agent session into
// existence with its initial environment. Whatever is in that env map is the
// only environment the running pane will ever have: SetEnvironment afterwards
// writes to the tmux session table, which reaches panes spawned later but never
// the one already running (gt-neycp).
var sessionCreators = map[string]bool{
	"NewSessionWithCommandAndEnv":         true,
	"EnsureSessionFreshWithCommandAndEnv": true,
}

// TestSpawnPathsCarryRuntimeConfigDir is the structural half of the gt-gllf fix.
//
// CLAUDE_CONFIG_DIR selects which account an agent authenticates as. A spawn
// path that omits it does not fail — the session comes up on the default
// ~/.claude account and works until that account's credentials expire, at which
// point every agent spawned that way is logged out with nothing reporting it as
// an auth problem. On 2026-08-23 the Deacon, Boot and all four reaper dogs came
// up logged out together and the town's execution tier stopped; the same defect
// had previously left the Deacon logged out for 3.27 days (hq-nms9g).
//
// It keeps recurring because it is invisible per call site. Six of the nine
// spawn paths resolved the account correctly, and the three that did not looked
// exactly like the six until someone read /proc/<pid>/environ on a live
// process. A rule that has to be remembered once per spawn path has the same
// shape as the defect, so this test does the remembering.
//
// Two forms are checked, covering every way a session is created in this repo:
//
//   - A session.SessionConfig literal must set RuntimeConfigDir. This is the
//     unified lifecycle used by boot, dog and mayor.
//   - A function that calls one of the sessionCreators directly must mention
//     RuntimeConfigDir somewhere in its body. This is the hand-rolled form used
//     by the daemon, deacon, crew, polecat, refinery and witness.
//
// The second check is deliberately loose about where the field appears: it
// asserts the author thought about the account, not the shape of the plumbing.
// Passing an empty string is still legal — a town with no accounts.json
// resolves to empty and must keep behaving as it did.
func TestSpawnPathsCarryRuntimeConfigDir(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch {
			case path == filepath.Join(repoRoot, "internal", "tmux"):
				// The primitives are declared here; declaring one is not a spawn.
				return fs.SkipDir
			case strings.HasPrefix(d.Name(), ".") && path != repoRoot,
				d.Name() == "vendor",
				d.Name() == "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		violations = append(violations, spawnsMissingConfigDir(t, repoRoot, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", repoRoot, err)
	}

	if len(violations) > 0 {
		t.Fatalf("every agent spawn path must resolve the town's CLAUDE_CONFIG_DIR and pass it "+
			"as RuntimeConfigDir, or the agent silently runs on the default account and goes "+
			"logged out when that account expires (gt-gllf). Use config.ResolveTownRuntimeConfigDir "+
			"— reading os.Getenv alone is not enough, because the daemon that spawns dogs and Boot "+
			"has no CLAUDE_CONFIG_DIR of its own:\n%s", strings.Join(violations, "\n"))
	}
}

// spawnsMissingConfigDir reports the spawn sites in one file that do not carry
// a runtime config dir.
func spawnsMissingConfigDir(t *testing.T, repoRoot, path string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	rel, relErr := filepath.Rel(repoRoot, path)
	if relErr != nil {
		rel = path
	}
	at := func(pos token.Pos) string {
		return rel + ":" + itoa(fset.Position(pos).Line)
	}

	var violations []string

	// Form 1: session.SessionConfig literals.
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isSessionConfigType(lit.Type) {
			return true
		}
		if !hasKey(lit, "RuntimeConfigDir") {
			violations = append(violations, "  "+at(lit.Pos())+": SessionConfig literal does not set RuntimeConfigDir")
		}
		return true
	})

	// Form 2: functions that create a session directly.
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var creator ast.Node
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !sessionCreators[sel.Sel.Name] {
				return true
			}
			if creator == nil {
				creator = call
			}
			return true
		})
		if creator == nil || mentionsIdent(fn.Body, "RuntimeConfigDir") {
			continue
		}
		violations = append(violations, "  "+at(creator.Pos())+": "+fn.Name.Name+
			" creates a session without a RuntimeConfigDir anywhere in its body")
	}

	return violations
}

// isSessionConfigType reports whether a composite literal's type is
// session.SessionConfig, written either qualified or bare (inside this package).
func isSessionConfigType(expr ast.Expr) bool {
	switch typ := expr.(type) {
	case *ast.Ident:
		return typ.Name == "SessionConfig"
	case *ast.SelectorExpr:
		pkg, ok := typ.X.(*ast.Ident)
		return ok && pkg.Name == "session" && typ.Sel.Name == "SessionConfig"
	}
	return false
}

// hasKey reports whether a keyed composite literal sets the named field.
func hasKey(lit *ast.CompositeLit, name string) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == name {
			return true
		}
	}
	return false
}

// mentionsIdent reports whether the identifier appears anywhere in the subtree,
// as a bare name, a field key, or a selector.
func mentionsIdent(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		if found {
			return false
		}
		if id, ok := node.(*ast.Ident); ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// itoa avoids pulling strconv in for one call.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
