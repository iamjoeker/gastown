package cmd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// This file guards the shape of gt-31nn, which is not "the pin is wrong" but
// something that has already happened once here:
//
//	The protection everything assumed was in place was never written by code.
//
// `gt mq submit` and the refinery both force-close their own MR wisps, each with
// a comment explaining that MR wisps are pinned and the queue must get past its
// own protection (gt-6dp, gt-obth). Nothing set the column. The protection was
// a manual sweep plus two agents' habits, and MR wisps arrived unpinned at a
// rate of two in ninety seconds.
//
// A unit test on the pin cannot catch the recurrence, because the pin is right.
// What has to be checked is that every place an MR wisp is BORN reaches for it —
// so that is what this checks. There were two such places when this was written
// and only one of them was the obvious one: `gt done` creates its own MR rather
// than calling `gt mq submit`, so a fix applied to the submit path alone would
// have been inert on the path almost every MR actually takes.

// TestEveryMRWispCreationPins fails when a function creates an MR wisp without
// pinning it.
//
// The fix is to call PinWisps on the new MR's ID, non-fatally, the way the two
// existing sites do. It is not to add an exception here: an unpinned MR wisp is
// exported and deleted by the archive-then-delete path, which selects exactly
// the rows that are protected by TYPE and not pinned.
func TestEveryMRWispCreationPins(t *testing.T) {
	repoRoot := repoRootForMRPinPolicy(t)

	var violations []string
	var scanned, mrCreates int
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		unpinned, all := mrCreateSites(t, path)
		mrCreates += all
		for _, line := range unpinned {
			rel, relErr := filepath.Rel(repoRoot, path)
			if relErr != nil {
				rel = path
			}
			violations = append(violations, fmt.Sprintf("  %s:%d", rel, line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repoRoot, err)
	}
	sort.Strings(violations)

	// Known positive. There were two MR creation sites when this rule was
	// written — internal/cmd/mq_submit.go and internal/cmd/done.go — and a scan
	// that finds none reports every one of them compliant. Losing sight of the
	// sites is the failure this rule exists to prevent, so it fails here rather
	// than passing quietly.
	if mrCreates < 2 {
		t.Fatalf("scanned %d non-test .go files and found %d MR wisp creates, want at least 2; "+
			"the rule is not looking where MR wisps are born, so its verdict on the rest is worthless",
			scanned, mrCreates)
	}

	if len(violations) > 0 {
		t.Errorf(`MR wisp created without pinning it:

%s
The gt:merge-request label keeps the row out of a plain purge. It does NOT keep
it out of the archive-then-delete path, which selects rows that are protected by
TYPE and not pinned, exports them, and deletes them. wisps.pinned = 1 is what
makes the merge record survive retention (gt-31nn).

Call bd.PinWisps(rigName, mrID) after the create, and warn rather than fail on
error — the MR is still submittable without it.`,
			strings.Join(violations, "\n"))
	}
}

// TestMRPinDetectorSeesAKnownCreate is the control for the test above.
//
// A source scan that finds nothing and a source scan that CANNOT find anything
// produce identical output, and for this rule the second is the likelier reading:
// it is looking for the absence of a call, which is what a blind detector reports
// everywhere. The fixtures make a clean run evidence rather than silence.
func TestMRPinDetectorSeesAKnownCreate(t *testing.T) {
	dir := t.TempDir()

	const unpinned = `package p

func submit(bd *beads.Beads) {
	mr, _ := bd.Create(beads.CreateOptions{
		Title:     "Merge: gt-1",
		Labels:    []string{"gt:merge-request"},
		Ephemeral: true,
	})
	_ = mr
}
`
	if got, total := mrCreateSites(t, writeMRFixture(t, dir, "unpinned.go", unpinned)); len(got) != 1 || total != 1 {
		t.Fatalf("detector found %d unpinned MR creates in a fixture containing exactly one; "+
			"the rule has gone blind and its clean results prove nothing", len(got))
	}

	// Negative control: the detector must not fire on a create that does pin,
	// or every correctly-wired site would be flagged and the rule disabled.
	const pinned = `package p

func submit(bd *beads.Beads) {
	mr, _ := bd.Create(beads.CreateOptions{
		Title:     "Merge: gt-1",
		Labels:    []string{"gt:merge-request"},
		Ephemeral: true,
	})
	_ = bd.PinWisps("gastown", mr.ID)
}
`
	if got, total := mrCreateSites(t, writeMRFixture(t, dir, "pinned.go", pinned)); len(got) != 0 || total != 1 {
		t.Fatalf("detector fired %d times on a create that pins, which it must ignore", len(got))
	}

	// Second negative control: a create that is not an MR is none of this
	// rule's business. Without this, the rule would demand a pin on every wisp
	// in the tree and be deleted rather than fixed.
	const otherWisp = `package p

func spawn(bd *beads.Beads) {
	_, _ = bd.Create(beads.CreateOptions{
		Title:     "heartbeat",
		Labels:    []string{"gt:wisp"},
		Ephemeral: true,
	})
}
`
	if got, total := mrCreateSites(t, writeMRFixture(t, dir, "other.go", otherWisp)); len(got) != 0 || total != 0 {
		t.Fatalf("detector fired %d times on a non-MR wisp create, which it must ignore", len(got))
	}
}

func writeMRFixture(t *testing.T, dir, name, src string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
	return path
}

// mrCreateSites returns the line numbers of MR-wisp creates in one file whose
// enclosing function never calls PinWisps, alongside the total number of MR
// creates seen. The total is what lets a caller tell "nothing is unpinned" from
// "nothing was found".
//
// The scope is the whole enclosing function declaration, not the statement or
// the block: the two real sites separate the create from the pin by a read-back,
// a routing check and, in `gt done`, a goto label. A tighter rule would flag
// both and be deleted.
func mrCreateSites(t *testing.T, path string) (unpinned []int, total int) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var lines []int
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		var creates []int
		pins := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				if isMRCreateOptions(node) {
					creates = append(creates, fset.Position(node.Pos()).Line)
				}
			case *ast.CallExpr:
				if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "PinWisps" {
					pins = true
				}
			}
			return true
		})
		total += len(creates)
		if !pins {
			lines = append(lines, creates...)
		}
	}
	return lines, total
}

// isMRCreateOptions reports whether lit is a beads.CreateOptions literal that
// labels the new bead as a merge request.
func isMRCreateOptions(lit *ast.CompositeLit) bool {
	switch typ := lit.Type.(type) {
	case *ast.Ident:
		if typ.Name != "CreateOptions" {
			return false
		}
	case *ast.SelectorExpr:
		if typ.Sel.Name != "CreateOptions" {
			return false
		}
	default:
		return false
	}

	found := false
	ast.Inspect(lit, func(n ast.Node) bool {
		basic, ok := n.(*ast.BasicLit)
		if ok && basic.Kind == token.STRING && basic.Value == `"gt:merge-request"` {
			found = true
		}
		return !found
	})
	return found
}

func repoRootForMRPinPolicy(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}
