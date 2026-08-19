package tmux

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// interruptKeys are the keystrokes that only ever mean "interrupt whatever the
// agent in this pane is doing". Unlike a message, they are recognizable as
// agent-facing from the literal alone, which is what makes this rule checkable.
var interruptKeys = map[string]bool{
	KeyCtrlC:  true,
	KeyEscape: true,
	"C-d":     true,
}

// TestNoRawInterruptsOutsideTmux is the structural half of the gt-7s8 fix.
//
// The wrappers in agent_keys.go close the hole only for the call sites that
// were converted; nothing stops the next one from being written as
// SendKeysRaw(sess, "C-c") again, and that is exactly how gt-kqf left this
// residual behind — the guard moved to a choke point that one family of callers
// did not pass through. A guard that has to be remembered at each call site has
// the same shape as the defect it prevents, so this test does the remembering.
//
// The rule is narrow on purpose. It says nothing about SendKeys generally,
// because a keystroke that boots an agent process and one that interrupts a
// running agent are indistinguishable at that level — see
// TestSendKeys_StaysUnguarded. An interrupt key literal is not ambiguous.
func TestNoRawInterruptsOutsideTmux(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	tmuxDir := filepath.Dir(thisFile)
	repoRoot := filepath.Clean(filepath.Join(tmuxDir, "..", ".."))

	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch {
			case path == tmuxDir:
				// The primitives themselves live here, along with the wrappers
				// that guard them.
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
		violations = append(violations, rawInterruptCalls(t, repoRoot, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", repoRoot, err)
	}

	if len(violations) > 0 {
		t.Fatalf("interrupts aimed at a running agent must go through tmux.InterruptAgent, "+
			"which carries the test guard that keeps a unit test from cancelling a live agent's turn (gt-7s8):\n%s",
			strings.Join(violations, "\n"))
	}
}

// rawInterruptCalls reports calls of the form x.SendKeysRaw(sess, "C-c") in one
// file. It matches on the method name rather than resolving the receiver's type
// because the deacon reaches tmux through a local interface, so there is no one
// type to look for.
func rawInterruptCalls(t *testing.T, repoRoot, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(sel.Sel.Name, "SendKeys") || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		key, err := strconv.Unquote(lit.Value)
		if err != nil || !interruptKeys[key] {
			return true
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			rel = path
		}
		out = append(out, "  "+rel+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+
			": "+sel.Sel.Name+"(..., "+lit.Value+")")
		return true
	})
	return out
}
