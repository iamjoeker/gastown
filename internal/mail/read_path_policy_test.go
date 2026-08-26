package mail

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

// This file is the guardrail for the shape of gt-qffl, which is not "the wrong
// function was called" but something narrower and much easier to reintroduce:
//
//	The closing code existed, was correct, and was not wired to the CLI.
//
// internal/mail had markReadBeads (acks, then closes) and markReadOnlyBeads
// (acks, then labels "read") side by side for months. Every command that reads
// mail called the second. Nothing was broken, no test failed, and the only
// visible symptom was a number nobody was watching: 520 messages on the hq
// store had been read, acknowledged, and left open, and 71% of all open beads
// were mail.
//
// A unit test on the predicate cannot catch that — the predicate was right.
// What has to be checked is which door the CLI walks through, so that is what
// this checks.
//
// MarkReadOnly stays exported and stays correct. It is the primitive
// MarkReadConsumed falls back to, and a caller outside this package that
// genuinely wants it can say so by fixing this test's list with a reason. What
// it must not be again is the DEFAULT that every read path reaches for without
// anyone deciding.

// readPathScanPackages are the packages whose mail-reading commands this rule
// covers. internal/mail itself is excluded: MarkReadOnly is defined there and
// MarkReadConsumed calls it on purpose.
var readPathScanPackages = []string{
	"internal/acp",
	"internal/cmd",
	"internal/daemon",
	"internal/deacon",
	"internal/nudge",
	"internal/protocol",
	"internal/refinery",
	"internal/witness",
}

// TestCLIReadPathsGoThroughMarkReadConsumed fails when a caller outside
// internal/mail marks mail read without letting the consumed check run.
//
// The fix is to call MarkReadConsumed, which does the same thing for anything
// that might still be owed and closes the rest. It is not to add an exception
// here.
func TestCLIReadPathsGoThroughMarkReadConsumed(t *testing.T) {
	repoRoot := repoRootForReadPathPolicy(t)

	var violations []string
	for _, pkg := range readPathScanPackages {
		dir := filepath.Join(repoRoot, pkg)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			for _, where := range markReadOnlyCallSites(t, path) {
				rel, relErr := filepath.Rel(repoRoot, path)
				if relErr != nil {
					rel = path
				}
				violations = append(violations, fmt.Sprintf("  %s:%d", rel, where))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", pkg, err)
		}
	}
	sort.Strings(violations)

	if len(violations) > 0 {
		t.Errorf(`mail read path(s) calling MarkReadOnly directly:

%s
MarkReadOnly leaves the bead open forever. That is right for a message that
still owes a reply and wrong for the automated traffic that makes up most of an
inbox, and having every read path default to it is how 520 read-and-acknowledged
messages stayed in the work queue (gt-qffl).

Call MarkReadConsumed(id, msg) instead. It marks read exactly as before unless
the message is provably consumed by being read, in which case it closes it.`,
			strings.Join(violations, "\n"))
	}
}

// TestMarkReadOnlyDetectorSeesAKnownCall is the control for the test above.
//
// A source scan that finds nothing and a source scan that cannot find anything
// produce the same output, and the second is what a zero usually means. This
// parses a fixture containing the exact call the rule looks for and requires
// the detector to find it, so a clean run above is evidence rather than
// silence.
func TestMarkReadOnlyDetectorSeesAKnownCall(t *testing.T) {
	const fixture = `package p

func read(mailbox *Mailbox, id string) error {
	return mailbox.MarkReadOnly(id)
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if got := markReadOnlyCallSites(t, path); len(got) != 1 {
		t.Fatalf("detector found %d MarkReadOnly calls in a fixture containing exactly one; "+
			"the rule has gone blind and its clean results prove nothing", len(got))
	}

	// Negative control: the detector must not fire on the replacement, or it
	// would flag every correctly-wired read path and be disabled for noise.
	const consumed = `package p

func read(mailbox *Mailbox, id string) error {
	_, err := mailbox.MarkReadConsumed(id, nil)
	return err
}
`
	consumedPath := filepath.Join(dir, "consumed.go")
	if err := os.WriteFile(consumedPath, []byte(consumed), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if got := markReadOnlyCallSites(t, consumedPath); len(got) != 0 {
		t.Fatalf("detector fired %d times on MarkReadConsumed, which it must ignore", len(got))
	}
}

// markReadOnlyCallSites returns the line numbers of `<x>.MarkReadOnly(...)`
// calls in one file.
func markReadOnlyCallSites(t *testing.T, path string) []int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var lines []int
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "MarkReadOnly" {
			return true
		}
		lines = append(lines, fset.Position(call.Pos()).Line)
		return true
	})
	return lines
}

func repoRootForReadPathPolicy(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}
