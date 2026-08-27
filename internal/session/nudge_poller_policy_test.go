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

// minSessionCreators is the self-validating half of the policy test below.
//
// The check reports violations, so a walk that sees nothing passes — and a walk
// that sees nothing is exactly what a wrong repo root, a bad skip rule or a
// parse that quietly stopped produces. Requiring a floor makes a blind traversal
// fail instead of certifying the tree clean. The value is deliberately below the
// count at the time of writing (8): this is a control against seeing NOTHING,
// not a census that has to be edited whenever a spawn path is added or removed.
const minSessionCreators = 6

// TestSpawnPathsStartANudgePoller is the structural half of the gt-xmq6 fix.
//
// A nudge written to the file queue is taken back out by exactly two things: the
// target agent's turn-boundary hook (Claude's UserPromptSubmit), and a
// background nudge-poller. An agent parked at its prompt reaches no further turn
// boundary, so for a parked session with no poller a queued nudge is
// structurally undeliverable — and every producer that reaches nudge.Enqueue
// reports success over it, so nothing anywhere says so.
//
// It recurs because it is invisible per call site. A session with no poller
// looks identical to one with a poller from the outside: it comes up, it works,
// it answers when a human types at it. Only a nudge queued while it happens to
// be parked is lost, and the loss is silent. Four spawn paths — crew, deacon,
// refinery, witness — started a poller; polecat, mayor, dog and boot did not,
// and neither did the daemon's restart path, which recreates a session for ANY
// role and so silently dropped the poller of a role that had one. Measured on
// one box on 2026-08-26: 9 of 16 live sessions had none.
//
// The rule is checked in the two forms every session creation in this repo
// takes, mirroring TestSpawnPathsCarryRuntimeConfigDir:
//
//   - A session.SessionConfig literal needs nothing: StartSession starts the
//     poller for every session it creates, which is why boot, dog and mayor are
//     covered without a line of their own.
//   - A function that calls one of the sessionCreators directly must mention a
//     poller somewhere in its body. This is the hand-rolled form used by the
//     daemon, deacon, crew, polecat, refinery and witness.
//
// The second check is loose about how: any identifier containing "Poller"
// counts, so StartPoller, startNudgePoller and EnsureNudgePoller all satisfy it.
// It asserts the author thought about the drain, not the shape of the plumbing.
func TestSpawnPathsStartANudgePoller(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	var violations []string
	var creators []string
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
		found, missing := spawnsMissingNudgePoller(t, repoRoot, path)
		creators = append(creators, found...)
		violations = append(violations, missing...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", repoRoot, err)
	}

	if len(creators) < minSessionCreators {
		t.Fatalf("found only %d functions that create an agent session under %s (%v), want at least %d. "+
			"This test reports violations, so a traversal that sees nothing passes silently — the floor is "+
			"here so a blind walk fails instead. Either the walk broke or the spawn paths moved; do not "+
			"lower the floor to make this pass",
			len(creators), repoRoot, creators, minSessionCreators)
	}

	if len(violations) > 0 {
		t.Fatalf("every agent spawn path must start a background nudge-poller for the session it creates, "+
			"or a nudge queued for that session while the agent is parked at its prompt is never delivered "+
			"and every producer reports success anyway (gt-xmq6). Call session.EnsureNudgePoller(townRoot, "+
			"sessionID) after the session is up and WARN on the error — do not swallow it:\n%s",
			strings.Join(violations, "\n"))
	}
}

// spawnsMissingNudgePoller reports the session-creating functions in one file,
// and which of them do not arrange a drain for the session they create.
func spawnsMissingNudgePoller(t *testing.T, repoRoot, path string) (creators, violations []string) {
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
		if creator == nil {
			continue
		}
		creators = append(creators, at(creator.Pos())+" "+fn.Name.Name)
		if mentionsIdentContaining(fn.Body, "Poller") {
			continue
		}
		violations = append(violations, "  "+at(creator.Pos())+": "+fn.Name.Name+
			" creates a session without starting a nudge-poller for it")
	}

	return creators, violations
}

// mentionsIdentContaining reports whether any identifier in the subtree contains
// the substring, so that StartPoller, startNudgePoller and EnsureNudgePoller all
// match one rule.
func mentionsIdentContaining(n ast.Node, substr string) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		if found {
			return false
		}
		if id, ok := node.(*ast.Ident); ok && strings.Contains(id.Name, substr) {
			found = true
			return false
		}
		return true
	})
	return found
}
